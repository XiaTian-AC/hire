package adapter

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	lua "github.com/yuin/gopher-lua"

	"hire/internal/argparse"
)

type tomlFile struct {
	Shell string              `toml:"shell"`
	Tasks map[string]tomlTask `toml:"tasks"`
}

type tomlTask struct {
	Desc string             `toml:"desc"`
	Cmd  interface{}        `toml:"cmd"` // string or []string
	Run  string             `toml:"run"` // inline Lua
	Args map[string]tomlArg `toml:"args"`
	When *tomlWhen          `toml:"when"`
}

type tomlArg struct {
	Type     string `toml:"type"` // default "string"
	Required bool   `toml:"required"`
	Desc     string `toml:"desc"`
}

type tomlWhen struct {
	OS interface{} `toml:"os"` // string or []string
}

// LoadTOML loads a static Taskfile.toml into the same File shape as
// LoadLua. Inline run snippets are compiled in this file's own LState,
// wrapped so the args table is in scope as a local.
func LoadTOML(path string) (*File, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no Taskfile at %s (create it with your tasks)", path)
		}
		return nil, err
	}
	var tf tomlFile
	md, err := toml.DecodeFile(path, &tf)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		names := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			names = append(names, k.String())
		}
		return nil, fmt.Errorf("%s: unknown field(s): %s", path, strings.Join(names, ", "))
	}

	L := lua.NewState()
	f := &File{L: L, Tasks: make(map[string]*Task), DefaultShell: tf.Shell}
	names := make([]string, 0, len(tf.Tasks))
	for name := range tf.Tasks {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		tt := tf.Tasks[name]
		if strings.HasPrefix(name, "__") {
			L.Close()
			return nil, fmt.Errorf("%s: task name %q must not start with __ (reserved for contract verbs)", path, name)
		}
		if reservedGlobals[name] {
			L.Close()
			return nil, fmt.Errorf("%s: %q is a reserved name and can not be a task", path, name)
		}
		if tt.When != nil {
			match, err := whenMatchesTOML(tt.When)
			if err != nil {
				L.Close()
				return nil, fmt.Errorf("%s: task %q: %v", path, name, err)
			}
			if !match {
				continue // platform filtered out: the task does not exist
			}
		}
		task, err := buildTOMLTask(L, tt, name, path)
		if err != nil {
			L.Close()
			return nil, err
		}
		f.Tasks[name] = task
		f.Order = append(f.Order, name)
	}
	if len(f.Order) == 0 {
		L.Close()
		return nil, fmt.Errorf("%s: no tasks defined", path)
	}
	sort.Strings(f.Order)
	return f, nil
}

func buildTOMLTask(L *lua.LState, tt tomlTask, name, path string) (*Task, error) {
	task := &Task{Name: name, File: path, Desc: tt.Desc}
	for argName, a := range tt.Args {
		if argName == "shell" {
			return nil, fmt.Errorf("%s: task %q: argument name %q is reserved (--shell selects the shell)", path, name, argName)
		}
		if task.Args == nil {
			task.Args = make(map[string]argparse.Spec)
		}
		sp := argparse.Spec{Type: a.Type, Required: a.Required, Desc: a.Desc}
		if sp.Type == "" {
			sp.Type = "string"
		}
		task.Args[argName] = sp
	}
	cmds, hasCmd, err := tomlCmds(tt.Cmd)
	if err != nil {
		return nil, fmt.Errorf("%s: task %q: %v", path, name, err)
	}
	switch {
	case hasCmd && tt.Run != "":
		return nil, fmt.Errorf("%s: task %q: 'cmd' and 'run' are mutually exclusive", path, name)
	case hasCmd:
		task.Fn = synthCommand(L, cmds)
	case tt.Run != "":
		// Wrap so the args table (callTask's single argument) is in
		// scope as a local named args.
		fn, err := L.Load(strings.NewReader("local args = ...\n"+tt.Run), name)
		if err != nil {
			return nil, fmt.Errorf("%s: task %q: invalid run lua: %v", path, name, err)
		}
		task.Fn = fn
	default:
		return nil, fmt.Errorf("%s: task %q: needs 'cmd' or 'run'", path, name)
	}
	return task, nil
}

func tomlCmds(v interface{}) ([]string, bool, error) {
	switch cv := v.(type) {
	case nil:
		return nil, false, nil
	case string:
		return []string{cv}, true, nil
	case []interface{}:
		if len(cv) == 0 {
			return nil, false, nil
		}
		cmds := make([]string, 0, len(cv))
		for _, e := range cv {
			s, ok := e.(string)
			if !ok {
				return nil, false, fmt.Errorf("'cmd' must be a string or an array of strings")
			}
			cmds = append(cmds, s)
		}
		return cmds, true, nil
	default:
		return nil, false, fmt.Errorf("'cmd' must be a string or an array of strings")
	}
}

func whenMatchesTOML(w *tomlWhen) (bool, error) {
	switch osv := w.OS.(type) {
	case string:
		return osv == runtime.GOOS, nil
	case []interface{}:
		for _, e := range osv {
			if s, ok := e.(string); ok && s == runtime.GOOS {
				return true, nil
			}
		}
		return false, nil
	default:
		return false, fmt.Errorf("'when' requires os (string or array of strings)")
	}
}
