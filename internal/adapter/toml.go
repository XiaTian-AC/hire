package adapter

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	lua "github.com/yuin/gopher-lua"

	"hire/internal/argparse"
	"hire/internal/shell"
)

type tomlFile struct {
	Shell string              `toml:"shell"`
	Tasks map[string]tomlTask `toml:"tasks"`
}

type tomlTask struct {
	Desc string             `toml:"desc"`
	Cmd  interface{}        `toml:"cmd"`  // single command string
	Cmds interface{}        `toml:"cmds"` // command list
	Deps []string           `toml:"deps"`
	Run  string             `toml:"run"` // removed in v2.1.0; decoded only so the error can point at it
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

// LoadTOML loads a pure-static Taskfile.toml into the same File shape
// as LoadLua. Commands may reference declared args through {{name}}
// and bool conditionals {{flag:+word}}; deps run same-file tasks
// before the body. Real logic belongs in a py/cpp/lua taskfile.
func LoadTOML(path string) (f *File, err error) {
	if _, serr := os.Stat(path); serr != nil {
		if os.IsNotExist(serr) {
			return nil, fmt.Errorf("no Taskfile at %s (create it with your tasks)", path)
		}
		return nil, serr
	}
	var tf tomlFile
	md, derr := toml.DecodeFile(path, &tf)
	if derr != nil {
		return nil, fmt.Errorf("%s: %w", path, derr)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		names := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			names = append(names, k.String())
		}
		return nil, fmt.Errorf("%s: unknown field(s): %s", path, strings.Join(names, ", "))
	}

	L := lua.NewState()
	defer func() {
		if err != nil {
			L.Close()
		}
	}()
	f = &File{L: L, Tasks: make(map[string]*Task), DefaultShell: tf.Shell}
	compiled := make(map[string]*tomlCompiled)
	names := make([]string, 0, len(tf.Tasks))
	for name := range tf.Tasks {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		tt := tf.Tasks[name]
		if strings.HasPrefix(name, "__") {
			return nil, fmt.Errorf("%s: task name %q must not start with __ (reserved for contract verbs)", path, name)
		}
		if reservedGlobals[name] {
			return nil, fmt.Errorf("%s: %q is a reserved name and can not be a task", path, name)
		}
		if tt.When != nil {
			match, werr := whenMatchesTOML(tt.When)
			if werr != nil {
				return nil, fmt.Errorf("%s: task %q: %v", path, name, werr)
			}
			if !match {
				continue // platform filtered out: the task does not exist
			}
		}
		tc, cerr := compileTOMLTask(tt, name, path)
		if cerr != nil {
			return nil, cerr
		}
		compiled[name] = tc
		f.Tasks[name] = &Task{Name: name, File: path, Desc: tt.Desc, Args: tc.specs}
		f.Order = append(f.Order, name)
	}
	if len(f.Order) == 0 {
		return nil, fmt.Errorf("%s: no tasks defined", path)
	}
	sort.Strings(f.Order)

	if verr := validateTOMLDeps(compiled, path); verr != nil {
		return nil, verr
	}

	// Synthesize Fns in dependency order: a task with deps calls each
	// dep's Fn first, then its own interpolated commands. The graph is
	// validated acyclic, so this recursion always terminates.
	built := make(map[string]*lua.LFunction)
	var build func(name string) *lua.LFunction
	build = func(name string) *lua.LFunction {
		if fn, ok := built[name]; ok {
			return fn
		}
		tc := compiled[name]
		depFns := make([]*lua.LFunction, 0, len(tc.deps))
		for _, d := range tc.deps {
			depFns = append(depFns, build(d))
		}
		fn := tomlFn(L, tc.cmds, tc.deps, depFns)
		built[name] = fn
		return fn
	}
	for _, name := range f.Order {
		f.Tasks[name].Fn = build(name)
	}
	return f, nil
}

// tomlCompiled is one task after per-task validation: arg specs,
// compiled command templates and raw dep names (file-local).
type tomlCompiled struct {
	specs map[string]argparse.Spec
	cmds  []cmdTmpl
	deps  []string
}

func compileTOMLTask(tt tomlTask, name, path string) (*tomlCompiled, error) {
	if tt.Run != "" {
		return nil, fmt.Errorf("%s: task %q: 'run' has been removed; for logic use a py/cpp/lua taskfile", path, name)
	}
	tc := &tomlCompiled{}
	for argName, a := range tt.Args {
		if argName == "shell" {
			return nil, fmt.Errorf("%s: task %q: argument name %q is reserved (--shell selects the shell)", path, name, argName)
		}
		if tc.specs == nil {
			tc.specs = make(map[string]argparse.Spec)
		}
		sp := argparse.Spec{Type: a.Type, Required: a.Required, Desc: a.Desc}
		if sp.Type == "" {
			sp.Type = "string"
		}
		if !validArgType(sp.Type) {
			return nil, fmt.Errorf("%s: task %q: arg %q: invalid type %q (use \"string\", \"bool\" or \"num\")", path, name, argName, sp.Type)
		}
		tc.specs[argName] = sp
	}
	cmds, hasCmd, terr := tomlCmdString(tt.Cmd)
	if terr != nil {
		return nil, fmt.Errorf("%s: task %q: %v", path, name, terr)
	}
	list, hasCmds, lerr := tomlCmdList(tt.Cmds)
	if lerr != nil {
		return nil, fmt.Errorf("%s: task %q: %v", path, name, lerr)
	}
	switch {
	case hasCmd && hasCmds:
		return nil, fmt.Errorf("%s: task %q: 'cmd' and 'cmds' are mutually exclusive", path, name)
	case !hasCmd && !hasCmds:
		return nil, fmt.Errorf("%s: task %q: needs 'cmd' or 'cmds'", path, name)
	}
	raw := cmds
	if !hasCmd {
		raw = list
	}
	for _, c := range raw {
		tmpl, cerr := compileCmdTmpl(c, tc.specs)
		if cerr != nil {
			return nil, fmt.Errorf("%s: task %q: %v", path, name, cerr)
		}
		tc.cmds = append(tc.cmds, tmpl)
	}
	tc.deps = tt.Deps
	return tc, nil
}

func tomlCmdString(v interface{}) ([]string, bool, error) {
	switch cv := v.(type) {
	case nil:
		return nil, false, nil
	case string:
		return []string{cv}, true, nil
	default:
		return nil, false, fmt.Errorf("'cmd' must be a string (use 'cmds' for a list)")
	}
}

func tomlCmdList(v interface{}) ([]string, bool, error) {
	switch cv := v.(type) {
	case nil:
		return nil, false, nil
	case []interface{}:
		if len(cv) == 0 {
			return nil, false, nil
		}
		out := make([]string, 0, len(cv))
		for _, e := range cv {
			s, ok := e.(string)
			if !ok {
				return nil, false, fmt.Errorf("'cmds' must be an array of strings")
			}
			out = append(out, s)
		}
		return out, true, nil
	default:
		return nil, false, fmt.Errorf("'cmds' must be an array of strings (use 'cmd' for a single command)")
	}
}

// validateTOMLDeps checks the file-local dep graph at load time, where
// every same-file task name is known: deps must exist on this platform
// (when-os filtered tasks do not), no dep may need required args (deps
// run without args), and the graph must be acyclic.
func validateTOMLDeps(tc map[string]*tomlCompiled, path string) error {
	names := make([]string, 0, len(tc))
	for n := range tc {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, d := range tc[name].deps {
			dtc, ok := tc[d]
			if !ok {
				return fmt.Errorf("%s: task %q: unknown dep %q (deps must be tasks in this file; when-os filtered tasks do not exist)", path, name, d)
			}
			for _, sp := range dtc.specs {
				if sp.Required {
					return fmt.Errorf("%s: task %q: dep %q has required args and can not run as a dependency", path, name, d)
				}
			}
		}
	}
	const visiting, done = 1, 2
	state := make(map[string]int, len(tc))
	var visit func(name, chain string) error
	visit = func(name, chain string) error {
		switch state[name] {
		case done:
			return nil
		case visiting:
			return fmt.Errorf("%s: dependency cycle: %s -> %s", path, chain, name)
		}
		state[name] = visiting
		next := name
		if chain != "" {
			next = chain + " -> " + name
		}
		for _, d := range tc[name].deps {
			if err := visit(d, next); err != nil {
				return err
			}
		}
		state[name] = done
		return nil
	}
	for _, name := range names {
		if err := visit(name, ""); err != nil {
			return err
		}
	}
	return nil
}

// cmdTmpl is one compiled command: a static shell prefix ("bash:")
// plus interpolated segments. Per the ruling the prefix splits off
// BEFORE interpolation, so placeholders never touch shell selection;
// config-registered prefixes can not be known at load time and stay in
// the text — the runtime re-splits the final string, same behavior.
type cmdTmpl struct {
	prefix string
	segs   []tmplSeg
}

// tmplSeg is one piece of a command template: literal text, a {{name}}
// substitution, or a {{name:+word}} bool conditional.
type tmplSeg struct {
	lit  string
	name string // placeholder arg name; empty for pure literals
	word string // conditional insertion word (cond form only)
	cond bool   // true = conditional {{name:+word}}
}

// compileCmdTmpl splits the builtin shell prefix, then compiles and
// validates every placeholder against the task's declared args.
func compileCmdTmpl(cmd string, specs map[string]argparse.Spec) (cmdTmpl, error) {
	var t cmdTmpl
	text := cmd
	if i := strings.Index(text, ":"); i > 0 && shell.IsBuiltin(text[:i]) {
		t.prefix, text = text[:i+1], text[i+1:]
	}
	for text != "" {
		i := strings.Index(text, "{{")
		if i < 0 {
			t.segs = append(t.segs, tmplSeg{lit: text})
			break
		}
		if i > 0 {
			t.segs = append(t.segs, tmplSeg{lit: text[:i]})
		}
		rest := text[i+2:]
		j := strings.Index(rest, "}}")
		if j < 0 {
			return cmdTmpl{}, fmt.Errorf("unclosed \"{{\" in command %q", cmd)
		}
		content := rest[:j]
		text = rest[j+2:]
		if k := strings.Index(content, ":+"); k > 0 {
			name := content[:k]
			sp, ok := specs[name]
			if !ok {
				return cmdTmpl{}, fmt.Errorf("unknown placeholder {{%s}}", content)
			}
			if sp.Type != "bool" {
				return cmdTmpl{}, fmt.Errorf("conditional placeholder {{%s}} is only for bool args (arg %q has type %q)", content, name, sp.Type)
			}
			t.segs = append(t.segs, tmplSeg{name: name, word: content[k+2:], cond: true})
			continue
		}
		sp, ok := specs[content]
		if !ok {
			return cmdTmpl{}, fmt.Errorf("unknown placeholder {{%s}}", content)
		}
		if sp.Type == "bool" {
			return cmdTmpl{}, fmt.Errorf("bool arg %q must use the conditional form {{%s:+word}}", content, content)
		}
		t.segs = append(t.segs, tmplSeg{name: content})
	}
	return t, nil
}

// renderTmpl fills the compiled segments from the args table: strings
// verbatim, nums in shortest form (8080, not 8080.000000), missing
// optionals and false bools as empty text.
func renderTmpl(t cmdTmpl, args *lua.LTable) string {
	var b strings.Builder
	if t.prefix != "" {
		b.WriteString(t.prefix)
	}
	for _, s := range t.segs {
		switch {
		case s.name == "":
			b.WriteString(s.lit)
		case s.cond:
			if bv, ok := args.RawGetString(s.name).(lua.LBool); ok && bool(bv) {
				b.WriteString(s.word)
			}
		default:
			switch v := args.RawGetString(s.name).(type) {
			case lua.LString:
				b.WriteString(string(v))
			case lua.LNumber:
				b.WriteString(strconv.FormatFloat(float64(v), 'g', -1, 64))
			}
		}
	}
	return b.String()
}

// tomlFn is the synthesized Fn for a TOML task: deps' Fns run first in
// listed order (each with an empty args table — required-arg deps are
// rejected at load), then this task's own commands, rendered and run
// through the same x path as every synthesized command task. Any dep or
// command error stops the task (existing synthesized-task semantics).
func tomlFn(L *lua.LState, cmds []cmdTmpl, depNames []string, depFns []*lua.LFunction) *lua.LFunction {
	return L.NewFunction(func(L *lua.LState) int {
		for i, df := range depFns {
			L.Push(df)
			L.Push(L.NewTable())
			if err := L.PCall(1, 0, nil); err != nil {
				L.RaiseError("dep %q: %v", depNames[i], err)
			}
		}
		ud, ok := L.GetGlobal("x").(*lua.LUserData)
		if !ok {
			L.RaiseError("command task: no task context (x) available")
		}
		runner, ok := ud.Value.(CmdRunner)
		if !ok {
			L.RaiseError("command task: invalid task context")
		}
		args, _ := L.Get(1).(*lua.LTable)
		if args == nil {
			args = L.NewTable()
		}
		for _, ct := range cmds {
			runner.RunCommand(L, renderTmpl(ct, args))
		}
		return 0
	})
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
