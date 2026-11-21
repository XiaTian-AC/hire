package runtime

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	lua "github.com/yuin/gopher-lua"

	"hire/internal/adapter"
	"hire/internal/shell"
)

// syncWriter serializes a task's output writer: the sentinel scan loop
// writes shell stdout while os/exec's copier goroutine feeds shell stderr
// into the same destination. A bare *bytes.Buffer is not safe for that —
// its ReadFrom (io.Copy's fast path) even truncates bytes written while
// it parks — so every concurrent writer goes through one mutex.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

type Ctx struct {
	File       *adapter.File
	ShellName  string
	Registry   map[string]string
	Stdout     io.Writer
	Stderr     io.Writer
	Stdin      io.Reader
	Background bool

	reader      *bufio.Reader
	callDepth   int
	mt          *lua.LTable
	instances   map[instKey]*instance
	exportBuf   []string
	exporting   bool
	exportShell string
	warned      bool
}

const maxCallDepth = 100

// stdoutIsTTY reports whether the process stdout is a terminal, i.e.
// nobody is piping the task into eval/iex. Seamed as a var for tests.
var stdoutIsTTY = func() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

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
		mt.RawSetString("env", L.NewFunction(ctxEnv))
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

// --- x.sh ---

type shOpts struct {
	inst        int // persistent instance number; 0 = export mode
	shellName   string
	interactive bool
}

func ctxSh(L *lua.LState) int {
	c := getCtx(L)
	cmd := L.CheckString(1)
	o := shOpts{inst: 1}
	if L.GetTop() >= 2 {
		if opts, ok := L.Get(2).(*lua.LTable); ok {
			if v, ok := opts.RawGetString("instance").(lua.LNumber); ok {
				f := float64(v)
				if f != math.Trunc(f) {
					L.RaiseError("x.sh: instance must be an integer")
				}
				if f < 0 {
					L.RaiseError("x.sh: instance must be >= 0")
				}
				o.inst = int(f)
			}
			if s, ok := opts.RawGetString("shell").(lua.LString); ok && string(s) != "" {
				o.shellName = string(s)
			}
			if b, ok := opts.RawGetString("interactive").(lua.LBool); ok {
				o.interactive = bool(b)
			}
		}
	}
	c.execSh(L, cmd, o)
	L.Push(lua.LTrue)
	return 1
}

// RunCommand implements adapter.CmdRunner, so synthesized shorthand
// tasks (string/array/cmd) run through the same path as x.sh, default
// persistent instance included.
func (c *Ctx) RunCommand(L *lua.LState, cmd string) {
	c.execSh(L, cmd, shOpts{inst: 1})
}

// execSh dispatches one command: shell: prefix one-shot, export collect,
// interactive one-shot, explicit-shell one-shot, or persistent instance.
func (c *Ctx) execSh(L *lua.LState, cmd string, o shOpts) {
	if name, rest, ok := splitShellPrefix(cmd, c.Registry); ok {
		// A shell prefix always means one-shot, regardless of opts.
		c.runOneShot(L, rest, name, false)
		return
	}
	name := o.shellName
	if name == "" {
		name = c.ShellName
	}
	if name == "" {
		name = shell.DefaultName()
	}
	switch {
	case o.inst == 0:
		c.collectExport(L, cmd, name)
	case o.interactive:
		c.runOneShot(L, cmd, name, true)
	case o.shellName != "":
		// An explicit shell is an explicit request not to persist.
		c.runOneShot(L, cmd, name, false)
	default:
		c.runPersistent(L, name, o.inst, cmd)
	}
}

// splitShellPrefix splits "bash:cmd" at the first colon only when the
// text before it names a known shell (builtin or config-registered);
// "C:\path" and unknown prefixes stay whole.
func splitShellPrefix(cmd string, registry map[string]string) (name, rest string, ok bool) {
	i := strings.Index(cmd, ":")
	if i <= 0 {
		return "", "", false
	}
	name = cmd[:i]
	if shell.IsBuiltin(name) {
		return name, cmd[i+1:], true
	}
	if p, ok := registry[name]; ok && p != "" {
		return name, cmd[i+1:], true
	}
	return "", "", false
}

// runOneShot runs a single command in a fresh shell process and raises
// a Lua error on failure.
func (c *Ctx) runOneShot(L *lua.LState, cmd, shellName string, interactive bool) {
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

// --- persistent instances ---

type instKey struct {
	inst  int
	shell string
}

type instance struct {
	name   string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *bufio.Reader
}

const instanceCloseWait = 500 * time.Millisecond

func (c *Ctx) runPersistent(L *lua.LState, shellName string, instNum int, cmd string) {
	if !shell.IsBuiltin(shellName) {
		// Third-party shells have no scripted stdin protocol; degrade to
		// one-shot, warned once per task.
		if !c.warned {
			c.warned = true
			fmt.Fprintf(c.Stderr, "hire: warning: shell %q does not support persistent instances; running one-shot\n", shellName)
		}
		c.runOneShot(L, cmd, shellName, false)
		return
	}
	if c.instances == nil {
		c.instances = make(map[instKey]*instance)
	}
	key := instKey{inst: instNum, shell: shellName}
	inst := c.instances[key]
	if inst == nil {
		var err error
		inst, err = c.startInstance(shellName)
		if err != nil {
			L.RaiseError("x.sh: %v", err)
		}
		c.instances[key] = inst
	}
	rc, err := inst.send(c, cmd)
	if err != nil {
		// Instance crashed: tear it down; the next command cold-restarts.
		delete(c.instances, key)
		inst.teardown()
		L.RaiseError("x.sh: %v", err)
	}
	if rc != 0 {
		// Like one-shot: a failing command raises; the instance survives.
		L.RaiseError("command failed (exit %d): %s", rc, cmd)
	}
}

func (c *Ctx) startInstance(shellName string) (*instance, error) {
	spec, err := shell.ResolvePersistent(shellName, c.Registry)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(spec.Path, spec.Args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	cmd.Stderr = c.Stderr
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return nil, err
	}
	inst := &instance{name: shellName, cmd: cmd, stdin: stdin, reader: bufio.NewReader(stdout)}
	if shellName == "pwsh" {
		// $LASTEXITCODE is unset until the first native command runs;
		// seed it so the first sentinel line reports a number, not "".
		fmt.Fprintln(stdin, "$LASTEXITCODE = 0")
	}
	return inst, nil
}

// closeInstances shuts every persistent instance down at task end
// (normal return, Lua error, or panic — callers defer this).
func (c *Ctx) closeInstances() {
	for _, inst := range c.instances {
		inst.teardown()
	}
	c.instances = nil
}

// teardown closes the command channel and waits briefly for the shell to
// exit on stdin EOF, then kills it. cmd.Wait reaps the child and closes
// the stdout pipe, so nothing is left reading after return.
func (inst *instance) teardown() {
	inst.stdin.Close()
	done := make(chan struct{})
	go func() {
		_ = inst.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(instanceCloseWait):
		_ = inst.cmd.Process.Kill()
		<-done
	}
}

const sentinelPrefix = "__HIRE_RC:"

func newNonce() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// send writes the command plus a sentinel echo to the instance's stdin,
// then scans stdout line by line, passing every line through to the
// task stdout until the sentinel reports the exit code. A dead instance
// (EOF before the sentinel) surfaces as an error.
func (inst *instance) send(c *Ctx, cmd string) (int, error) {
	nonce := newNonce()
	prefix := sentinelPrefix + nonce + ":"
	var echo string
	if inst.name == "pwsh" {
		// Spec amendment (design spec §9): $? catches cmdlet failures,
		// which leave $LASTEXITCODE untouched; v1 one-shot parity.
		echo = `Write-Output "__HIRE_RC:` + nonce + `:$(if ($?) { $LASTEXITCODE } else { 1 })"`
	} else {
		echo = `printf '__HIRE_RC:%s:%s\n' ` + nonce + ` "$?"`
	}
	if _, err := fmt.Fprintf(inst.stdin, "%s\n%s\n", cmd, echo); err != nil {
		return 0, fmt.Errorf("shell instance died while running: %s", cmd)
	}
	for {
		line, err := inst.reader.ReadString('\n')
		trimmed := strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(trimmed, prefix) {
			if code, cerr := strconv.Atoi(strings.TrimPrefix(trimmed, prefix)); cerr == nil {
				return code, nil
			}
		}
		if trimmed != "" || err == nil {
			fmt.Fprintln(c.Stdout, trimmed)
		}
		if err != nil {
			return 0, fmt.Errorf("shell instance exited before reporting status: %s", cmd)
		}
	}
}

// --- export mode (instance = 0) ---

func (c *Ctx) collectExport(L *lua.LState, cmd, shellName string) {
	if c.Background {
		L.RaiseError("x.sh: instance=0 (export mode) is not available for background tasks (bstart)")
	}
	if !shell.IsBuiltin(shellName) {
		L.RaiseError("x.sh: instance=0 (export mode) needs a builtin shell (pwsh/bash/sh), got %q", shellName)
	}
	if !c.exporting {
		c.exporting = true
		c.exportShell = shellName
	}
	c.exportBuf = append(c.exportBuf, cmd)
}

// finishExport flushes the instance=0 buffer at task end: joined to
// stdout on success, discarded with a stderr note on failure. On a TTY
// stdout (nobody is eval-ing) it prints how to consume the task.
func (c *Ctx) finishExport(taskName string, failed bool) {
	if len(c.exportBuf) == 0 {
		return
	}
	if failed {
		c.exportBuf = nil
		fmt.Fprintln(c.Stderr, "hire: task failed; export aborted, collected commands discarded")
		return
	}
	fmt.Fprintln(c.Stdout, strings.Join(c.exportBuf, "\n"))
	c.exportBuf = nil
	if stdoutIsTTY() {
		if c.exportShell == "pwsh" {
			fmt.Fprintf(c.Stderr, "提示: 此任务包含 instance=0 命令。请使用: iex (hire %s)\n", taskName)
		} else {
			fmt.Fprintf(c.Stderr, "提示: 此任务包含 instance=0 命令。请使用: eval \"$(hire %s)\"\n", taskName)
		}
	}
}

// --- other x methods ---

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
	w := io.Writer(c.Stdout)
	if c.exporting {
		// Export mode: stdout is reserved for the eval payload.
		w = c.Stderr
	}
	fmt.Fprintln(w, strings.Join(parts, " "))
	return 0
}

func ctxEnv(L *lua.LState) int {
	_ = getCtx(L)
	name := L.CheckString(1)
	if v, ok := os.LookupEnv(name); ok {
		L.Push(lua.LString(v))
	} else {
		L.Push(lua.LNil)
	}
	return 1
}

func ctxAsk(L *lua.LState) int {
	c := getCtx(L)
	prompt := L.CheckString(1)
	def := ""
	if L.GetTop() >= 2 {
		def = lua.LVAsString(L.Get(2))
	}
	w := io.Writer(c.Stdout)
	if c.exporting {
		// Export mode: stdout is reserved for the eval payload; a prompt
		// printed there would be executed by `eval "$(hire t)"`.
		w = c.Stderr
	}
	fmt.Fprintf(w, "%s ", prompt)
	if c.Background || c.Stdin == nil {
		fmt.Fprintf(w, "[no stdin: using default %q]\n", def)
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
