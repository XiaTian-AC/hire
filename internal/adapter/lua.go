package adapter

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"

	lua "github.com/yuin/gopher-lua"

	"hire/internal/argparse"
)

// LoadLua loads a v2-syntax Taskfile.lua: tasks are declared as globals
// and collected by snapshot-diffing _G around DoFile. Only new or
// value-changed globals are task candidates, so builtins a file merely
// touches are never collected.
func LoadLua(path string) (*File, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no Taskfile at %s (create it with your tasks)", path)
		}
		return nil, err
	}
	L := lua.NewState()
	// Inject the helpers before the snapshot so they are never candidates.
	L.SetGlobal("task", L.NewFunction(luaTaskHelper))
	L.SetGlobal("req", L.NewFunction(luaReqHelper))
	L.SetGlobal("bool", L.NewFunction(luaBoolHelper))
	L.SetGlobal("num", L.NewFunction(luaNumHelper))
	before := snapshotGlobals(L)
	if err := L.DoFile(path); err != nil {
		L.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	after := snapshotGlobals(L)
	names := make([]string, 0, len(after))
	for name, v := range after {
		if before[name] == v {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	f := &File{L: L, Tasks: make(map[string]*Task)}
	for _, name := range names {
		v := after[name]
		if strings.HasPrefix(name, "__") {
			L.Close()
			return nil, fmt.Errorf("%s: task name %q must not start with __ (reserved for contract verbs)", path, name)
		}
		if reservedGlobals[name] {
			if name == "shell" {
				if s, ok := v.(lua.LString); ok {
					f.DefaultShell = string(s)
					continue
				}
			}
			L.Close()
			return nil, fmt.Errorf("%s: %q is a reserved name and can not be a task", path, name)
		}
		task, err := dispatch(L, name, v, path)
		if err != nil {
			L.Close()
			return nil, err
		}
		if task == nil { // warned+skipped or filtered by when
			continue
		}
		f.Tasks[name] = task
		f.Order = append(f.Order, name)
	}
	if len(f.Order) == 0 {
		if L.GetTop() > 0 {
			if _, ok := L.Get(1).(*lua.LTable); ok {
				L.Close()
				return nil, fmt.Errorf("%s: old v1 syntax (returning a tasks table) has been removed; declare tasks as globals, e.g. build = function() x.sh(\"go build .\") end", path)
			}
		}
		L.Close()
		return nil, fmt.Errorf("%s: no tasks defined", path)
	}
	sort.Strings(f.Order)
	return f, nil
}

func snapshotGlobals(L *lua.LState) map[string]lua.LValue {
	g := L.GetGlobal("_G").(*lua.LTable)
	snap := make(map[string]lua.LValue)
	g.ForEach(func(k, v lua.LValue) {
		if ks, ok := k.(lua.LString); ok {
			snap[string(ks)] = v
		}
	})
	return snap
}

func dispatch(L *lua.LState, name string, v lua.LValue, path string) (*Task, error) {
	switch tv := v.(type) {
	case lua.LString:
		return &Task{Name: name, File: path, Fn: synthCommand(L, []string{string(tv)})}, nil
	case *lua.LFunction:
		return &Task{Name: name, File: path, Fn: tv}, nil
	case *lua.LTable:
		if b, ok := tv.RawGetString("__hire").(lua.LBool); ok && bool(b) {
			return parseTaskTable(L, tv, name, path)
		}
		if cmds, ok := stringSlice(tv); ok {
			return &Task{Name: name, File: path, Fn: synthCommand(L, cmds)}, nil
		}
		return nil, fmt.Errorf("%s: task %q must be a string, an array of strings, a function, or task{...}", path, name)
	default:
		fmt.Fprintf(os.Stderr, "hire: warning: skipping %q in %s (value is not a string, table, or function)\n", name, path)
		return nil, nil
	}
}

// stringSlice reports whether t is a pure sequence of strings (no extra
// hash keys), i.e. the command-sequence shorthand.
func stringSlice(t *lua.LTable) ([]string, bool) {
	n := t.Len()
	if n == 0 {
		return nil, false
	}
	cmds := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		s, ok := t.RawGetInt(i).(lua.LString)
		if !ok {
			return nil, false
		}
		cmds = append(cmds, string(s))
	}
	extra := false
	t.ForEach(func(k, _ lua.LValue) {
		if extra {
			return
		}
		if num, ok := k.(lua.LNumber); ok && num >= 1 && num <= lua.LNumber(n) {
			return
		}
		extra = true
	})
	if extra {
		return nil, false
	}
	return cmds, true
}

func parseTaskTable(L *lua.LState, t *lua.LTable, name, path string) (*Task, error) {
	task := &Task{Name: name, File: path}
	if d, ok := t.RawGetString("desc").(lua.LString); ok {
		task.Desc = string(d)
	}
	if av := t.RawGetString("args"); av != lua.LNil {
		at, ok := av.(*lua.LTable)
		if !ok {
			return nil, fmt.Errorf("%s: task %q: args must be a table", path, name)
		}
		specs, err := parseArgSpecs(at, name, path)
		if err != nil {
			return nil, err
		}
		task.Args = specs
	}
	runV, cmdV := t.RawGetString("run"), t.RawGetString("cmd")
	switch {
	case runV != lua.LNil && cmdV != lua.LNil:
		return nil, fmt.Errorf("%s: task %q: 'run' and 'cmd' are mutually exclusive", path, name)
	case runV != lua.LNil:
		fn, ok := runV.(*lua.LFunction)
		if !ok {
			return nil, fmt.Errorf("%s: task %q: 'run' must be a function", path, name)
		}
		task.Fn = fn
	case cmdV != lua.LNil:
		cmd, ok := cmdV.(lua.LString)
		if !ok {
			return nil, fmt.Errorf("%s: task %q: 'cmd' must be a string", path, name)
		}
		task.Fn = synthCommand(L, []string{string(cmd)})
	default:
		return nil, fmt.Errorf("%s: task %q: needs 'run' or 'cmd'", path, name)
	}
	if wv := t.RawGetString("when"); wv != lua.LNil {
		wt, ok := wv.(*lua.LTable)
		if !ok {
			return nil, fmt.Errorf("%s: task %q: 'when' must be a table", path, name)
		}
		match, err := whenMatches(wt)
		if err != nil {
			return nil, fmt.Errorf("%s: task %q: %v", path, name, err)
		}
		if !match {
			return nil, nil // platform filtered out: the task does not exist
		}
	}
	return task, nil
}

func whenMatches(wt *lua.LTable) (bool, error) {
	bad := ""
	wt.ForEach(func(k, _ lua.LValue) {
		if bad != "" {
			return
		}
		if ks, ok := k.(lua.LString); ok && string(ks) != "os" {
			bad = string(ks)
		}
	})
	if bad != "" {
		return false, fmt.Errorf("'when' has unsupported key %q (only os is supported)", bad)
	}
	switch ov := wt.RawGetString("os").(type) {
	case lua.LString:
		return string(ov) == runtime.GOOS, nil
	case *lua.LTable:
		for i := 1; i <= ov.Len(); i++ {
			if s, ok := ov.RawGetInt(i).(lua.LString); ok && string(s) == runtime.GOOS {
				return true, nil
			}
		}
		return false, nil
	default:
		return false, fmt.Errorf("'when' requires os (string or array of strings)")
	}
}

func parseArgSpecs(at *lua.LTable, taskName, path string) (map[string]argparse.Spec, error) {
	specs := make(map[string]argparse.Spec)
	var failure error
	at.ForEach(func(k, v lua.LValue) {
		if failure != nil {
			return
		}
		name, ok := k.(lua.LString)
		if !ok {
			failure = fmt.Errorf("%s: task %q: arg names must be strings", path, taskName)
			return
		}
		if string(name) == "shell" {
			failure = fmt.Errorf("%s: task %q: argument name %q is reserved (--shell selects the shell)", path, taskName, string(name))
			return
		}
		st, ok := v.(*lua.LTable)
		if !ok {
			failure = fmt.Errorf("%s: task %q: arg %q must be a table (req/bool/num sugar or {type=, required=, desc=})", path, taskName, string(name))
			return
		}
		// Spec fields are strict (Task 2+3 review M2): a wrong Lua type or
		// an unknown field is a load-time error, matching the TOML
		// adapter's Undecoded strictness — never a silently-defaulted value.
		sp := argparse.Spec{Type: "string"}
		var unknown string
		st.ForEach(func(fk, fv lua.LValue) {
			if failure != nil || unknown != "" {
				return
			}
			field, ok := fk.(lua.LString)
			if !ok {
				failure = fmt.Errorf("%s: task %q: arg %q: field names must be strings", path, taskName, string(name))
				return
			}
			switch string(field) {
			case "type":
				s, ok := fv.(lua.LString)
				if !ok {
					failure = fmt.Errorf("%s: task %q: arg %q: 'type' must be a string", path, taskName, string(name))
					return
				}
				sp.Type = string(s)
			case "required":
				b, ok := fv.(lua.LBool)
				if !ok {
					failure = fmt.Errorf("%s: task %q: arg %q: 'required' must be a boolean", path, taskName, string(name))
					return
				}
				sp.Required = bool(b)
			case "desc":
				s, ok := fv.(lua.LString)
				if !ok {
					failure = fmt.Errorf("%s: task %q: arg %q: 'desc' must be a string", path, taskName, string(name))
					return
				}
				sp.Desc = string(s)
			default:
				unknown = string(field)
			}
		})
		if failure == nil && unknown != "" {
			failure = fmt.Errorf("%s: task %q: arg %q: unknown field %q (use type/required/desc)", path, taskName, string(name), unknown)
		}
		if failure == nil && !validArgType(sp.Type) {
			failure = fmt.Errorf("%s: task %q: arg %q: invalid type %q (use \"string\", \"bool\" or \"num\")", path, taskName, string(name), sp.Type)
		}
		if failure != nil {
			return
		}
		specs[string(name)] = sp
	})
	return specs, failure
}

// luaTaskHelper implements task{}: light validation plus the __hire
// marker; real validation happens Go-side where the task name is known.
func luaTaskHelper(L *lua.LState) int {
	t := L.Get(1)
	tt, ok := t.(*lua.LTable)
	if !ok {
		L.RaiseError("task{}: expected a table, got %s", t.Type().String())
	}
	tt.RawSetString("__hire", lua.LTrue)
	L.Push(tt)
	return 1
}

func luaReqHelper(L *lua.LState) int {
	t := L.NewTable()
	t.RawSetString("type", lua.LString("string"))
	t.RawSetString("required", lua.LTrue)
	t.RawSetString("desc", lua.LString(L.OptString(1, "")))
	L.Push(t)
	return 1
}

func luaBoolHelper(L *lua.LState) int {
	t := L.NewTable()
	t.RawSetString("type", lua.LString("bool"))
	t.RawSetString("required", lua.LFalse)
	t.RawSetString("desc", lua.LString(L.OptString(1, "")))
	L.Push(t)
	return 1
}

func luaNumHelper(L *lua.LState) int {
	t := L.NewTable()
	t.RawSetString("type", lua.LString("num"))
	t.RawSetString("required", lua.LFalse)
	t.RawSetString("desc", lua.LString(L.OptString(1, "")))
	L.Push(t)
	return 1
}
