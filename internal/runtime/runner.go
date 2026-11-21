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
	stdout, stderr := io.Writer(opts.Stdout), io.Writer(opts.Stderr)
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	ctx := &Ctx{
		File: file, ShellName: shellName, Registry: opts.Registry,
		Stdout: &syncWriter{w: stdout}, Stderr: &syncWriter{w: stderr},
		Stdin: opts.Stdin,
		Background: opts.Background,
	}
	// Task end (normal return, Lua error, or panic) always closes every
	// persistent instance: stdin EOF, brief wait, then kill.
	defer ctx.closeInstances()
	if err := callTask(file.L, ctx, task, args); err != nil {
		// Failure discards the export buffer before any return shape.
		ctx.finishExport(name, true)
		// External task exits get the spec §11 single-name form
		// (`hire: task "x" failed (exit N)`); everything else keeps the
		// generic wrap so Lua errors display as before.
		if tee, ok := adapter.AsTaskExitError(err); ok {
			return fmt.Errorf("task %q failed (exit %d)", name, tee.Code)
		}
		return fmt.Errorf("task %q: %v", name, err)
	}
	ctx.finishExport(name, false)
	return nil
}
