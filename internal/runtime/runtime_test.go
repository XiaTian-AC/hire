package runtime

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	lua "github.com/yuin/gopher-lua"

	"hire/internal/adapter"
	"hire/internal/shell"
)

func writeChain(t *testing.T, content string) *adapter.Chain {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "Taskfile.lua")
	os.WriteFile(path, []byte(content), 0o644)
	f, err := adapter.LoadLua(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)
	return adapter.NewChain(f)
}

func runTask(t *testing.T, c *adapter.Chain, name string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := Run(c, name, c.Files[0].L.NewTable(), Options{
		Stdout: &out, Stderr: &out, Stdin: os.Stdin,
		Registry: map[string]string{},
	})
	return out.String(), err
}

func TestRunEcho(t *testing.T) {
	c := writeChain(t, `hello = function() x.echo("hello", "world") end`)
	out, err := runTask(t, c, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hello world") {
		t.Errorf("output = %q, want 'hello world'", out)
	}
}

func TestRunSh(t *testing.T) {
	c := writeChain(t, `shello = function() x.sh("echo from-shell") end`)
	out, err := runTask(t, c, "shello")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "from-shell") {
		t.Errorf("output = %q, want 'from-shell'", out)
	}
}

func TestRunShFail(t *testing.T) {
	c := writeChain(t, `fail = function() x.sh("exit 3") end`)
	_, err := runTask(t, c, "fail")
	if err == nil {
		t.Fatal("expected error for failing command")
	}
	if !strings.Contains(err.Error(), "exit 3") {
		t.Errorf("error = %v, want exit code 3", err)
	}
}

func TestRunStringShorthand(t *testing.T) {
	c := writeChain(t, `clean = "echo from-string-shorthand"`)
	out, err := runTask(t, c, "clean")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "from-string-shorthand") {
		t.Errorf("output = %q, want shorthand command output", out)
	}
}

func TestRunArrayShorthand(t *testing.T) {
	c := writeChain(t, `quick = { "echo one", "echo two" }`)
	out, err := runTask(t, c, "quick")
	if err != nil {
		t.Fatal(err)
	}
	iOne, iTwo := strings.Index(out, "one"), strings.Index(out, "two")
	if iOne < 0 || iTwo < 0 || iOne > iTwo {
		t.Errorf("output = %q, want one then two in order", out)
	}
}

func TestRunArgsSignature(t *testing.T) {
	// v2 protocol: the task function receives the args table as its
	// single argument.
	c := writeChain(t, `who = function(args) x.echo("hi " .. args.name) end`)
	var out bytes.Buffer
	args := c.Files[0].L.NewTable()
	args.RawSetString("name", lua.LString("hire"))
	err := Run(c, "who", args, Options{
		Stdout: &out, Stderr: &out, Stdin: os.Stdin,
		Registry: map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "hi hire") {
		t.Errorf("output = %q, want 'hi hire'", out.String())
	}
}

func TestRunTaskFormWithArgs(t *testing.T) {
	c := writeChain(t, `
tagged = task {
  desc = "tag it",
  args = { tag = req "the tag", force = bool "force" },
  run = function(args)
    x.echo("tag=" .. args.tag)
    if args.force then x.echo("forced") end
  end,
}`)
	var out bytes.Buffer
	args := c.Files[0].L.NewTable()
	args.RawSetString("tag", lua.LString("v9"))
	args.RawSetString("force", lua.LTrue)
	err := Run(c, "tagged", args, Options{
		Stdout: &out, Stderr: &out, Stdin: os.Stdin,
		Registry: map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, "tag=v9") || !strings.Contains(s, "forced") {
		t.Errorf("output = %q, want tag=v9 and forced", s)
	}
}

func TestRunChain(t *testing.T) {
	c := writeChain(t, `
base = function() x.echo("base-ran") end
chain = function() x.run("base") x.echo("chained") end
`)
	out, err := runTask(t, c, "chain")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "base-ran") || !strings.Contains(out, "chained") {
		t.Errorf("output = %q, want base-ran + chained", out)
	}
}

func TestRunEnvOs(t *testing.T) {
	t.Setenv("HIRE_TEST_ENV", "envval")
	c := writeChain(t, `
info = function()
  x.echo("env=" .. tostring(os.getenv("HIRE_TEST_ENV")))
  x.echo("os=" .. x.os())
end
`)
	out, err := runTask(t, c, "info")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "env=envval") {
		t.Errorf("output = %q, want env=envval", out)
	}
}

func TestRunNotFound(t *testing.T) {
	c := writeChain(t, `a = function() x.echo("a") end`)
	err := Run(c, "nope", c.Files[0].L.NewTable(), Options{
		Stdout: io.Discard, Stderr: io.Discard, Stdin: os.Stdin,
		Registry: map[string]string{},
	})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got %v", err)
	}
}

func TestRunExec(t *testing.T) {
	c := writeChain(t, `execed = function() x.exec("hostname") end`)
	out, err := runTask(t, c, "execed")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) == "" {
		t.Errorf("output = %q, want hostname output", out)
	}
}

// --- v2: persistent shell instances, export mode, prefixes, x.env ---

func runTaskIO(t *testing.T, c *adapter.Chain, name string) (string, string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	err := Run(c, name, c.Files[0].L.NewTable(), Options{
		Stdout: &out, Stderr: &errb, Stdin: os.Stdin,
		Registry: map[string]string{},
	})
	return out.String(), errb.String(), err
}

// mustShell skips the test when the named builtin shell is unavailable
// on this platform (e.g. no Git Bash on a bare Windows CI box).
func mustShell(t *testing.T, name string) shell.Spec {
	t.Helper()
	spec, err := shell.Resolve(name, map[string]string{})
	if err != nil {
		t.Skipf("skipping: %v", err)
	}
	return spec
}

func TestPersistentCdEnv(t *testing.T) {
	mustShell(t, "bash")
	// Forward slashes: Lua string escapes would eat backslashes in a
	// Windows path, and Git Bash accepts slash-separated paths fine.
	dir := filepath.ToSlash(t.TempDir())
	c := writeChain(t, fmt.Sprintf(`
shell = "bash"
persist = function()
  x.sh("cd '%s'")
  x.sh("touch hire-persist-marker.txt")
  x.sh("export HIRE_PERSIST=42")
  x.sh("echo $HIRE_PERSIST")
end
`, dir))
	out, _, err := runTaskIO(t, c, "persist")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "hire-persist-marker.txt")); err != nil {
		t.Errorf("marker file not created in %s: cd did not persist (%v)", dir, err)
	}
	if !strings.Contains(out, "42") {
		t.Errorf("output = %q, want the env var persisted across x.sh calls", out)
	}
}

func TestInstanceIsolation(t *testing.T) {
	mustShell(t, "bash")
	c := writeChain(t, `
shell = "bash"
iso = function()
  x.sh("export WHO=one")
  x.sh("export WHO=two", {instance=2})
  x.sh("echo $WHO")
end
`)
	out, _, err := runTaskIO(t, c, "iso")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "one") || strings.Contains(out, "two") {
		t.Errorf("output = %q, want 'one' only: instance 2 must not leak into instance 1", out)
	}
}

func TestPersistentRCFailure(t *testing.T) {
	mustShell(t, "bash")
	c := writeChain(t, `
shell = "bash"
rc = function() x.sh("false") end
rcsurvive = function()
  pcall(x.sh, "false")
  x.sh("echo still-alive")
end
`)
	_, _, err := runTaskIO(t, c, "rc")
	if err == nil || !strings.Contains(err.Error(), "exit 1") || !strings.Contains(err.Error(), "false") {
		t.Errorf("err = %v, want exit 1 error naming the command", err)
	}
	// A nonzero RC must not destroy the instance: the next command runs
	// on the same shell (state kept, no cold start needed).
	out, _, err := runTaskIO(t, c, "rcsurvive")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "still-alive") {
		t.Errorf("output = %q, want instance usable after RC != 0", out)
	}
}

func TestInstanceCrashColdRestart(t *testing.T) {
	mustShell(t, "bash")
	c := writeChain(t, `
shell = "bash"
crash = function()
  local ok = pcall(x.sh, "kill -9 $$")
  if ok then error("expected the crash to raise") end
  x.sh("echo revived")
end
`)
	out, _, err := runTaskIO(t, c, "crash")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "revived") {
		t.Errorf("output = %q, want a cold-restarted instance to answer", out)
	}
}

func TestPwshPersistentEnv(t *testing.T) {
	mustShell(t, "pwsh")
	c := writeChain(t, `
shell = "pwsh"
pwshp = function()
  x.sh("$env:HIRE_PERSIST_PW = 'pw-works'")
  x.sh("Write-Output $env:HIRE_PERSIST_PW")
end
`)
	out, _, err := runTaskIO(t, c, "pwshp")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "pw-works") {
		t.Errorf("output = %q, want env var persisted in the pwsh instance", out)
	}
}

func TestExportCollect(t *testing.T) {
	c := writeChain(t, `
exp = function()
  x.sh("export A=1", {instance=0})
  x.sh("echo $A", {instance=0})
end
`)
	out, _, err := runTaskIO(t, c, "exp")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "export A=1\necho $A") {
		t.Errorf("stdout = %q, want collected commands joined by newline", out)
	}
}

func TestExportDiscardOnFailure(t *testing.T) {
	c := writeChain(t, `
expfail = function()
  x.sh("echo collected", {instance=0})
  error("boom")
end
`)
	out, stderr, err := runTaskIO(t, c, "expfail")
	if err == nil {
		t.Fatal("expected the task to fail")
	}
	if strings.Contains(out, "collected") {
		t.Errorf("stdout = %q, want the export buffer discarded on failure", out)
	}
	if !strings.Contains(stderr, "export") {
		t.Errorf("stderr = %q, want an export-aborted note", stderr)
	}
}

func TestExportEchoDivertsToStderr(t *testing.T) {
	c := writeChain(t, `
expecho = function()
  x.sh("echo collected", {instance=0})
  x.echo("sidebar")
end
`)
	out, stderr, err := runTaskIO(t, c, "expecho")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "sidebar") {
		t.Errorf("stdout = %q, want x.echo diverted away from eval output", out)
	}
	if !strings.Contains(stderr, "sidebar") {
		t.Errorf("stderr = %q, want x.echo on stderr during export", stderr)
	}
	if !strings.Contains(out, "echo collected") {
		t.Errorf("stdout = %q, want the collected commands flushed", out)
	}
}

func TestExportHintOnTTY(t *testing.T) {
	old := stdoutIsTTY
	stdoutIsTTY = func() bool { return true }
	t.Cleanup(func() { stdoutIsTTY = old })
	c := writeChain(t, `
expbash = function() x.sh("echo hi", {instance=0, shell="bash"}) end
exppw = function() x.sh("echo hi", {instance=0, shell="pwsh"}) end
`)
	_, stderr, err := runTaskIO(t, c, "expbash")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, `eval "$(hire expbash)"`) {
		t.Errorf("stderr = %q, want eval usage hint naming the task", stderr)
	}
	_, stderr, err = runTaskIO(t, c, "exppw")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, `iex (hire exppw)`) {
		t.Errorf("stderr = %q, want iex usage hint naming the task", stderr)
	}
}

func TestExportNoHintWhenPiped(t *testing.T) {
	old := stdoutIsTTY
	stdoutIsTTY = func() bool { return false }
	t.Cleanup(func() { stdoutIsTTY = old })
	c := writeChain(t, `exp = function() x.sh("echo hi", {instance=0}) end`)
	_, stderr, err := runTaskIO(t, c, "exp")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr, "提示") {
		t.Errorf("stderr = %q, want no usage hint when stdout is piped", stderr)
	}
}

func TestExportBackgroundError(t *testing.T) {
	c := writeChain(t, `expbg = function() x.sh("echo x", {instance=0}) end`)
	err := Run(c, "expbg", c.Files[0].L.NewTable(), Options{
		Stdout: io.Discard, Stderr: io.Discard, Stdin: os.Stdin,
		Registry: map[string]string{}, Background: true,
	})
	if err == nil || !strings.Contains(err.Error(), "bstart") {
		t.Errorf("err = %v, want instance=0 rejected under bstart", err)
	}
}

func TestExportThirdPartyShellError(t *testing.T) {
	c := writeChain(t, `expcustom = function() x.sh("echo x", {instance=0, shell="custom"}) end`)
	err := Run(c, "expcustom", c.Files[0].L.NewTable(), Options{
		Stdout: io.Discard, Stderr: io.Discard, Stdin: os.Stdin,
		Registry: map[string]string{"custom": "/x/sh"},
	})
	if err == nil || !strings.Contains(err.Error(), "builtin") {
		t.Errorf("err = %v, want instance=0 rejected for a third-party shell", err)
	}
}

func TestThirdPartyDegradesToOneShot(t *testing.T) {
	spec := mustShell(t, "pwsh")
	c := writeChain(t, `
shell = "custom"
degrade = function()
  x.sh("Write-Output one")
  x.sh("Write-Output two")
end
`)
	var out, errb bytes.Buffer
	err := Run(c, "degrade", c.Files[0].L.NewTable(), Options{
		Stdout: &out, Stderr: &errb, Stdin: os.Stdin,
		Registry: map[string]string{"custom": spec.Path},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "one") || !strings.Contains(out.String(), "two") {
		t.Errorf("output = %q, want both one-shot commands to run", out.String())
	}
	if n := strings.Count(errb.String(), "one-shot"); n != 1 {
		t.Errorf("stderr = %q, want exactly one degrade warning, got %d", errb.String(), n)
	}
}

func TestPrefixCPathNotSplit(t *testing.T) {
	mustShell(t, "bash")
	c := writeChain(t, `
shell = "bash"
cpath = function() x.sh("C:\\nope") end
`)
	_, _, err := runTaskIO(t, c, "cpath")
	if err == nil {
		t.Fatal("expected the verbatim command to fail (127)")
	}
	if !strings.Contains(err.Error(), `C:\nope`) {
		t.Errorf("err = %v, want the verbatim command in the error", err)
	}
	if strings.Contains(err.Error(), "unknown shell") {
		t.Errorf("err = %v, C:\\ must not be mistaken for a shell prefix", err)
	}
}

func TestPrefixBashOneShot(t *testing.T) {
	mustShell(t, "bash")
	c := writeChain(t, `
shell = "bash"
bashpfx = function() x.sh("bash:echo via-bash") end
`)
	out, _, err := runTaskIO(t, c, "bashpfx")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "via-bash") {
		t.Errorf("output = %q, want the bash: prefix to select bash", out)
	}
}

func TestPrefixUnknownVerbatim(t *testing.T) {
	mustShell(t, "bash")
	c := writeChain(t, `
shell = "bash"
unkpfx = function() x.sh("nosuchshell:echo hi") end
`)
	_, _, err := runTaskIO(t, c, "unkpfx")
	if err == nil {
		t.Fatal("expected the verbatim command to fail")
	}
	if !strings.Contains(err.Error(), "nosuchshell:echo hi") {
		t.Errorf("err = %v, want the whole command executed verbatim", err)
	}
	if strings.Contains(err.Error(), "unknown shell") {
		t.Errorf("err = %v, an unknown prefix must not be treated as a shell", err)
	}
}

func TestPrefixShorthandTask(t *testing.T) {
	mustShell(t, "bash")
	c := writeChain(t, `pfxshorthand = "bash:echo from-prefix-shorthand"`)
	out, _, err := runTaskIO(t, c, "pfxshorthand")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "from-prefix-shorthand") {
		t.Errorf("output = %q, want prefix handling in shorthand cmd tasks", out)
	}
}

func TestInteractiveStdin(t *testing.T) {
	mustShell(t, "bash")
	c := writeChain(t, `
shell = "bash"
inter = function() x.sh("read line; echo got:$line", {interactive=true}) end
`)
	var out bytes.Buffer
	err := Run(c, "inter", c.Files[0].L.NewTable(), Options{
		Stdout: &out, Stderr: io.Discard, Stdin: strings.NewReader("hello\n"),
		Registry: map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "got:hello") {
		t.Errorf("output = %q, want the command to read task stdin", out.String())
	}
}

func TestInstanceNegative(t *testing.T) {
	c := writeChain(t, `badinst = function() x.sh("echo x", {instance=-1}) end`)
	_, _, err := runTaskIO(t, c, "badinst")
	if err == nil || !strings.Contains(err.Error(), "instance") {
		t.Errorf("err = %v, want a negative instance number rejected", err)
	}
}

func TestXEnv(t *testing.T) {
	t.Setenv("HIRE_XENV_SET", "hello")
	c := writeChain(t, `
envtest = function()
  if x.env("HIRE_XENV_MISSING") ~= nil then error("expected nil for a missing env var") end
  x.echo("v=" .. x.env("HIRE_XENV_SET"))
end
`)
	out, _, err := runTaskIO(t, c, "envtest")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "v=hello") {
		t.Errorf("output = %q, want x.env to return the value", out)
	}
}
