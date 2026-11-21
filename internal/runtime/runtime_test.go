package runtime

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	lua "github.com/yuin/gopher-lua"

	"hire/internal/adapter"
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
	// Lua task errors keep the generic wrap form (only exec-task exits
	// get the §11 single-name form).
	if !strings.HasPrefix(err.Error(), `task "fail": `) {
		t.Errorf("error = %q, want old wrap form %q", err.Error(), `task "fail": ...`)
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
