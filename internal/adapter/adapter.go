// Package adapter loads taskfiles of different formats into the uniform
// Task/File/Chain model consumed by the rest of hire. Every adapter
// produces the same shape; all tasks are functions ("all tasks are
// functions"): string/array/cmd shorthands get a synthesized Fn.
package adapter

import (
	"fmt"
	"os"
	"sort"

	lua "github.com/yuin/gopher-lua"

	"hire/internal/argparse"
)

// reservedGlobals may not be used as task names in any adapter.
var reservedGlobals = map[string]bool{
	"x": true, "shell": true, "task": true, "req": true, "bool": true,
}

// validArgType is the args-spec type allow-list shared by every adapter:
// only "string" and "bool" exist, and argparse silently treats any other
// value as string — so adapters reject it at load time (CHANGELOG:
// wrongly-typed args-spec fields are errors in every adapter).
func validArgType(t string) bool {
	return t == "string" || t == "bool"
}

// Task is one runnable task.
type Task struct {
	Name, Desc string
	Args       map[string]argparse.Spec
	Fn         *lua.LFunction
	File       string // source file, for warnings and x.run ownership
}

// File is one loaded taskfile with its own Lua state.
type File struct {
	L            *lua.LState
	Tasks        map[string]*Task
	Order        []string
	DefaultShell string // shell = "..." top-level declaration
}

// Chain merges files in load order (global first, cwd last). Same-name
// tasks: the later file wins, with a stderr warning. DefaultShell: the
// later declaration overrides.
type Chain struct {
	Files        []*File
	Tasks        map[string]*Task
	Order        []string
	DefaultShell string
}

// CmdRunner runs one shell command inside the current task context.
// runtime.Ctx implements it; synthesized command tasks reach it through
// the global x userdata so shorthand tasks and x.sh share one path.
type CmdRunner interface {
	RunCommand(L *lua.LState, cmd string)
}

func NewChain(files ...*File) *Chain {
	c := &Chain{Files: files, Tasks: make(map[string]*Task)}
	for _, f := range files {
		if f.DefaultShell != "" {
			c.DefaultShell = f.DefaultShell
		}
		for _, name := range f.Order {
			t := f.Tasks[name]
			if prev, dup := c.Tasks[name]; dup {
				fmt.Fprintf(os.Stderr, "hire: warning: task %q redefined: %s overrides %s\n",
					name, t.File, prev.File)
			}
			c.Tasks[name] = t
		}
	}
	c.Order = make([]string, 0, len(c.Tasks))
	for name := range c.Tasks {
		c.Order = append(c.Order, name)
	}
	sort.Strings(c.Order)
	return c
}

// synthCommand builds the Fn for string/array/cmd shorthand tasks: a Go
// closure holding the command list, executing via the global x at run
// time (same path as x.sh).
func synthCommand(L *lua.LState, cmds []string) *lua.LFunction {
	return L.NewFunction(func(L *lua.LState) int {
		ud, ok := L.GetGlobal("x").(*lua.LUserData)
		if !ok {
			L.RaiseError("command task: no task context (x) available")
		}
		runner, ok := ud.Value.(CmdRunner)
		if !ok {
			L.RaiseError("command task: invalid task context")
		}
		for _, cmd := range cmds {
			runner.RunCommand(L, cmd)
		}
		return 0
	})
}

// FileOf returns the file the given chain task was loaded from. Run and
// x.run execute on this file's LState, which keeps task calls within one
// file by construction.
func (c *Chain) FileOf(task *Task) *File {
	for _, f := range c.Files {
		if f.Tasks[task.Name] == task {
			return f
		}
	}
	return nil
}

func (f *File) Close() { f.L.Close() }

func (c *Chain) Close() {
	for _, f := range c.Files {
		f.Close()
	}
}
