package runtime

import (
	"fmt"
	"io"

	lua "github.com/yuin/gopher-lua"

	"hire/internal/adapter"
)

type Options struct {
	ShellName  string
	Registry   map[string]string
	Stdout     io.Writer
	Stderr     io.Writer
	Stdin      io.Reader
	Background bool
}

// Run executes one task from the chain on the task's own file (and its
// LState), so x.run stays within the file by construction. The --shell
// flag wins over the chain's shell declaration.
func Run(c *adapter.Chain, name string, args *lua.LTable, opts Options) error {
	task, ok := c.Tasks[name]
	if !ok {
		return fmt.Errorf("task %q not found; run 'hire ls' to list tasks", name)
	}
	file := c.FileOf(task)
	if file == nil {
		return fmt.Errorf("task %q not found; run 'hire ls' to list tasks", name)
	}
	shellName := opts.ShellName
	if shellName == "" {
		shellName = c.DefaultShell
	}
	ctx := &Ctx{
		File: file, ShellName: shellName, Registry: opts.Registry,
		Stdout: opts.Stdout, Stderr: opts.Stderr, Stdin: opts.Stdin,
		Background: opts.Background,
	}
	if err := callTask(file.L, ctx, task, args); err != nil {
		return fmt.Errorf("task %q: %v", name, err)
	}
	return nil
}
