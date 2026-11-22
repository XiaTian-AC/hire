package adapter_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	lua "github.com/yuin/gopher-lua"

	"hire/internal/adapter"
	"hire/internal/runtime"
)

func writeTOML(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Taskfile.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func loadTOML(t *testing.T, content string) *adapter.File {
	t.Helper()
	f, err := adapter.LoadTOML(writeTOML(t, content))
	if err != nil {
		t.Fatalf("LoadTOML(%q): %v", content, err)
	}
	t.Cleanup(f.Close)
	return f
}

func loadTOMLErr(t *testing.T, content string) error {
	t.Helper()
	f, err := adapter.LoadTOML(writeTOML(t, content))
	if err == nil {
		t.Fatalf("LoadTOML(%q): expected error, got nil", content)
	}
	if f != nil {
		f.Close()
	}
	return err
}

func runTOMLTask(t *testing.T, f *adapter.File, name string, args *lua.LTable) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runtime.Run(adapter.NewChain(f), name, args, runtime.Options{
		Stdout: &out, Stderr: &out, Stdin: os.Stdin,
		Registry: map[string]string{},
	})
	return out.String(), err
}

func TestTOMLCmdString(t *testing.T) {
	f := loadTOML(t, `
[tasks.clean]
cmd = "rm -rf dist/"
`)
	task, ok := f.Tasks["clean"]
	if !ok {
		t.Fatal("clean task not found")
	}
	if task.Fn == nil {
		t.Error("cmd string task should synthesize an Fn")
	}
	if task.Args != nil {
		t.Errorf("Args = %v, want nil", task.Args)
	}
}

func TestTOMLCmdArray(t *testing.T) {
	f := loadTOML(t, `
[tasks.quick]
cmd = ["echo line1", "echo line2"]
`)
	task, ok := f.Tasks["quick"]
	if !ok {
		t.Fatal("quick task not found")
	}
	if task.Fn == nil {
		t.Error("cmd array task should synthesize an Fn")
	}
}

func TestTOMLRunInlineExecutes(t *testing.T) {
	f := loadTOML(t, `
[tasks.greet]
desc = "say hi"
run = """
x.echo("hi " .. args.msg)
"""
[tasks.greet.args.msg]
required = true
desc = "who to greet"
`)
	task, ok := f.Tasks["greet"]
	if !ok {
		t.Fatal("greet task not found")
	}
	if task.Desc != "say hi" {
		t.Errorf("Desc = %q", task.Desc)
	}
	msg := task.Args["msg"]
	if !msg.Required || msg.Type != "string" || msg.Desc != "who to greet" {
		t.Errorf("msg spec = %+v", msg)
	}
	args := f.L.NewTable()
	args.RawSetString("msg", lua.LString("toml"))
	out, err := runTOMLTask(t, f, "greet", args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hi toml") {
		t.Errorf("output = %q, want inline run to execute with x.echo", out)
	}
}

func TestTOMLCmdAndRunBoth(t *testing.T) {
	err := loadTOMLErr(t, `
[tasks.bad]
cmd = "echo hi"
run = "x.echo(\"hi\")"
`)
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error should mention exclusivity: %v", err)
	}
}

func TestTOMLNeitherCmdNorRun(t *testing.T) {
	err := loadTOMLErr(t, `
[tasks.bad]
desc = "incomplete"
`)
	if !strings.Contains(err.Error(), "run") || !strings.Contains(err.Error(), "cmd") {
		t.Errorf("error should mention run/cmd: %v", err)
	}
}

func TestTOMLCmdBadType(t *testing.T) {
	err := loadTOMLErr(t, `
[tasks.bad]
cmd = [1, 2]
`)
	if !strings.Contains(err.Error(), "cmd") {
		t.Errorf("error should mention cmd: %v", err)
	}
}

func TestTOMLArgsForms(t *testing.T) {
	f := loadTOML(t, `
[tasks.release]
cmd = "echo ok"
[tasks.release.args.tag]
required = true
desc = "git tag"
[tasks.release.args.force]
type = "bool"
[tasks.release.args.limit]
desc = "concurrency"
`)
	task := f.Tasks["release"]
	tag := task.Args["tag"]
	if tag.Type != "string" || !tag.Required || tag.Desc != "git tag" {
		t.Errorf("tag spec = %+v", tag)
	}
	force := task.Args["force"]
	if force.Type != "bool" || force.Required {
		t.Errorf("force spec = %+v", force)
	}
	limit := task.Args["limit"]
	if limit.Type != "string" || limit.Required || limit.Desc != "concurrency" {
		t.Errorf("limit spec = %+v", limit)
	}
}

func TestTOMLBadArgType(t *testing.T) {
	err := loadTOMLErr(t, `
[tasks.build]
cmd = "echo hi"
[tasks.build.args.count]
type = "integer"
`)
	if err == nil {
		t.Fatal("expected load error for invalid arg type")
	}
	for _, want := range []string{"build", "count", `invalid type "integer"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestTOMLNumArgType(t *testing.T) {
	f := loadTOML(t, `
[tasks.serve]
cmd = "echo ok"
[tasks.serve.args.port]
type = "num"
desc = "listen port"
[tasks.serve.args.tries]
type = "num"
required = true
`)
	port := f.Tasks["serve"].Args["port"]
	if port.Type != "num" || port.Required || port.Desc != "listen port" {
		t.Errorf("port spec = %+v, want optional num", port)
	}
	tries := f.Tasks["serve"].Args["tries"]
	if tries.Type != "num" || !tries.Required {
		t.Errorf("tries spec = %+v, want required num", tries)
	}
}

func TestTOMLFloatArgTypeRejected(t *testing.T) {
	err := loadTOMLErr(t, `
[tasks.serve]
cmd = "echo ok"
[tasks.serve.args.port]
type = "float"
`)
	if err == nil {
		t.Fatal("expected load error for type = float")
	}
	for _, want := range []string{"serve", "port", `invalid type "float"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestTOMLWhen(t *testing.T) {
	f := loadTOML(t, fmt.Sprintf(`
[tasks.here]
cmd = "echo hi"
when = { os = %q }

[tasks.multi]
cmd = "echo hi"
when = { os = ["other", %q] }

[tasks.gone]
cmd = "echo hi"
when = { os = "not-an-os" }
`, goruntime.GOOS, goruntime.GOOS))
	if _, ok := f.Tasks["here"]; !ok {
		t.Error("matching single-os task should exist")
	}
	if _, ok := f.Tasks["multi"]; !ok {
		t.Error("matching os-array task should exist")
	}
	if _, ok := f.Tasks["gone"]; ok {
		t.Error("non-matching when should make the task not exist")
	}
	if len(f.Order) != 2 {
		t.Errorf("Order = %v, want 2 tasks", f.Order)
	}
}

func TestTOMLShellDeclaration(t *testing.T) {
	f := loadTOML(t, `
shell = "pwsh"

[tasks.build]
cmd = "go build ."
`)
	if f.DefaultShell != "pwsh" {
		t.Errorf("DefaultShell = %q, want pwsh", f.DefaultShell)
	}
}

func TestTOMLReservedNames(t *testing.T) {
	for _, name := range []string{"__dunder", "x", "shell", "task", "req", "bool", "num"} {
		err := loadTOMLErr(t, fmt.Sprintf(`
[tasks.%s]
cmd = "echo hi"
`, name))
		if !strings.Contains(err.Error(), "reserved") && !strings.Contains(err.Error(), "__") {
			t.Errorf("task %s: expected reserved-name error, got %v", name, err)
		}
	}
	err := loadTOMLErr(t, `
[tasks.bad]
cmd = "echo hi"
[tasks.bad.args.shell]
required = true
`)
	if !strings.Contains(err.Error(), "shell") {
		t.Errorf("args.shell should error: %v", err)
	}
}

func TestTOMLMissingFile(t *testing.T) {
	_, err := adapter.LoadTOML(filepath.Join(t.TempDir(), "nonexistent.toml"))
	if err == nil || !strings.Contains(err.Error(), "no Taskfile") {
		t.Errorf("expected 'no Taskfile' error, got %v", err)
	}
}

func TestTOMLNoTasks(t *testing.T) {
	err := loadTOMLErr(t, `shell = "pwsh"`)
	if !strings.Contains(err.Error(), "no tasks") {
		t.Errorf("error should say no tasks: %v", err)
	}
}

func TestTOMLBadSyntax(t *testing.T) {
	err := loadTOMLErr(t, `this is [not toml`)
	if !strings.Contains(err.Error(), "Taskfile.toml") {
		t.Errorf("error should mention the file: %v", err)
	}
}

func TestTOMLUnknownField(t *testing.T) {
	err := loadTOMLErr(t, `
[tasks.typo]
cmds = "echo hi"
`)
	if !strings.Contains(err.Error(), "cmds") {
		t.Errorf("error should name the unknown field: %v", err)
	}
}

func TestTOMLRunCompileError(t *testing.T) {
	err := loadTOMLErr(t, `
[tasks.bad]
run = "x.echo(broken"
`)
	if !strings.Contains(err.Error(), "bad") {
		t.Errorf("error should name the task: %v", err)
	}
}

func TestTOMLRunExecutesViaShell(t *testing.T) {
	f := loadTOML(t, `
[tasks.also_sh]
run = "x.sh(\"echo from-inline-sh\")"
`)
	out, err := runTOMLTask(t, f, "also_sh", f.L.NewTable())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "from-inline-sh") {
		t.Errorf("output = %q, want inline run x.sh output", out)
	}
}
