package runtime

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	lua "github.com/yuin/gopher-lua"

	"hire/internal/adapter"
	"hire/internal/shell"
)

type Ctx struct {
	File       *adapter.File
	ShellName  string
	Registry   map[string]string
	Stdout     io.Writer
	Stderr     io.Writer
	Stdin      io.Reader
	Background bool
	reader     *bufio.Reader
	callDepth  int
	mt         *lua.LTable
}

const maxCallDepth = 100

func (c *Ctx) metatable(L *lua.LState) *lua.LTable {
	if c.mt == nil {
		mt := L.NewTable()
		mt.RawSetString("__index", mt)
		mt.RawSetString("sh", L.NewFunction(ctxSh))
		mt.RawSetString("exec", L.NewFunction(ctxExec))
		mt.RawSetString("run", L.NewFunction(ctxRun))
		mt.RawSetString("echo", L.NewFunction(ctxEcho))
		mt.RawSetString("ask", L.NewFunction(ctxAsk))
		mt.RawSetString("cwd", L.NewFunction(ctxCwd))
		mt.RawSetString("os", L.NewFunction(ctxOs))
		c.mt = mt
	}
	return c.mt
}

func newCtxUserData(L *lua.LState, c *Ctx) *lua.LUserData {
	ud := L.NewUserData()
	ud.Value = c
	L.SetMetatable(ud, c.metatable(L))
	return ud
}

// getCtx fetches the ctx from the global x. v2 tasks call methods with
// dot syntax (x.sh(...)), which passes no self argument, so the context
// always comes from the global set by callTask.
func getCtx(L *lua.LState) *Ctx {
	ud, ok := L.GetGlobal("x").(*lua.LUserData)
	if !ok {
		L.RaiseError("no task context (x) available")
	}
	c, ok := ud.Value.(*Ctx)
	if !ok {
		L.RaiseError("invalid task context")
	}
	return c
}

func ctxSh(L *lua.LState) int {
	c := getCtx(L)
	cmd := L.CheckString(1)
	shellName, interactive := "", false
	if L.GetTop() >= 2 {
		if opts, ok := L.Get(2).(*lua.LTable); ok {
			if s, ok := opts.RawGetString("shell").(lua.LString); ok && string(s) != "" {
				shellName = string(s)
			}
			if b, ok := opts.RawGetString("interactive").(lua.LBool); ok {
				interactive = bool(b)
			}
		}
	}
	c.runShell(L, cmd, shellName, interactive)
	L.Push(lua.LTrue)
	return 1
}

// runShell is the core of x.sh: resolve the shell, run the command
// one-shot, raise a Lua error on failure.
func (c *Ctx) runShell(L *lua.LState, cmd, shellName string, interactive bool) {
	if shellName == "" {
		shellName = c.ShellName
	}
	spec, err := shell.Resolve(shellName, c.Registry)
	if err != nil {
		L.RaiseError("%v", err)
	}
	execCmd := exec.Command(spec.Path, spec.Argv(cmd)...)
	execCmd.Stdout = c.Stdout
	execCmd.Stderr = c.Stderr
	if interactive {
		execCmd.Stdin = c.Stdin
	}
	if err := execCmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			L.RaiseError("command failed (exit %d): %s", ee.ExitCode(), cmd)
		}
		L.RaiseError("command failed: %v: %s", err, cmd)
	}
}

// RunCommand implements adapter.CmdRunner, so synthesized shorthand
// tasks (string/array/cmd) run through the same path as x.sh.
func (c *Ctx) RunCommand(L *lua.LState, cmd string) {
	c.runShell(L, cmd, "", false)
}

func ctxExec(L *lua.LState) int {
	c := getCtx(L)
	name := L.CheckString(1)
	var args []string
	if L.GetTop() >= 2 {
		if t, ok := L.Get(2).(*lua.LTable); ok {
			t.ForEach(func(_, v lua.LValue) {
				args = append(args, lua.LVAsString(v))
			})
		}
	}
	execCmd := exec.Command(name, args...)
	execCmd.Stdout = c.Stdout
	execCmd.Stderr = c.Stderr
	if err := execCmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			L.RaiseError("exec failed (exit %d): %s %s", ee.ExitCode(), name, strings.Join(args, " "))
		}
		L.RaiseError("exec failed: %v: %s", err, name)
	}
	L.Push(lua.LTrue)
	return 1
}

func ctxRun(L *lua.LState) int {
	c := getCtx(L)
	name := L.CheckString(1)
	c.callDepth++
	if c.callDepth > maxCallDepth {
		L.RaiseError("x.run: recursion too deep (possible cycle calling %q)", name)
	}
	defer func() { c.callDepth-- }()
	var argsTable *lua.LTable
	if L.GetTop() >= 2 {
		if t, ok := L.Get(2).(*lua.LTable); ok {
			argsTable = t
		}
	}
	if argsTable == nil {
		argsTable = L.NewTable()
	}
	// x.run only reaches tasks of the same file: the ctx is bound to the
	// entry task's file, whose LState is the one this call runs on.
	task, ok := c.File.Tasks[name]
	if !ok {
		L.RaiseError("x.run: task %q not found", name)
	}
	if err := callTask(L, c, task, argsTable); err != nil {
		L.RaiseError("%v", err)
	}
	L.Push(lua.LTrue)
	return 1
}

// callTask is the v2 calling protocol: expose ctx as the global x, then
// call the task function with the single args argument —
// run = function(args) with x.sh(...) resolving via the metatable.
func callTask(L *lua.LState, c *Ctx, task *adapter.Task, argsTable *lua.LTable) error {
	ctxUD := newCtxUserData(L, c)
	L.SetGlobal("x", ctxUD)
	L.Push(task.Fn)
	L.Push(argsTable)
	return L.PCall(1, 0, nil)
}

func ctxEcho(L *lua.LState) int {
	c := getCtx(L)
	n := L.GetTop()
	parts := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		parts = append(parts, L.Get(i).String())
	}
	fmt.Fprintln(c.Stdout, strings.Join(parts, " "))
	return 0
}

func ctxAsk(L *lua.LState) int {
	c := getCtx(L)
	prompt := L.CheckString(1)
	def := ""
	if L.GetTop() >= 2 {
		def = lua.LVAsString(L.Get(2))
	}
	fmt.Fprintf(c.Stdout, "%s ", prompt)
	if c.Background || c.Stdin == nil {
		fmt.Fprintf(c.Stdout, "[no stdin: using default %q]\n", def)
		L.Push(lua.LString(def))
		return 1
	}
	if c.reader == nil {
		c.reader = bufio.NewReader(c.Stdin)
	}
	line, err := c.reader.ReadString('\n')
	line = strings.TrimRight(line, "\r\n")
	if err != nil && line == "" {
		L.Push(lua.LString(def))
		return 1
	}
	if line == "" {
		line = def
	}
	L.Push(lua.LString(line))
	return 1
}

func ctxCwd(L *lua.LState) int {
	_ = getCtx(L)
	wd, err := os.Getwd()
	if err != nil {
		L.RaiseError("cwd: %v", err)
	}
	L.Push(lua.LString(wd))
	return 1
}

func ctxOs(L *lua.LState) int {
	_ = getCtx(L)
	L.Push(lua.LString(runtime.GOOS))
	return 1
}
