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
	"hire/internal/shell"
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

func mustShell(t *testing.T, name string) {
	t.Helper()
	if _, err := shell.Resolve(name, map[string]string{}); err != nil {
		t.Skipf("skipping: %v", err)
	}
}

// --- basic schema ---

func TestTOMLCmdString(t *testing.T) {
	f := loadTOML(t, `
[tasks.clean]
desc = "remove build artifacts"
cmd = "rm -rf dist/"
`)
	task, ok := f.Tasks["clean"]
	if !ok {
		t.Fatal("clean task not found")
	}
	if task.Desc != "remove build artifacts" {
		t.Errorf("Desc = %q", task.Desc)
	}
	if task.Fn == nil {
		t.Error("cmd string task should synthesize an Fn")
	}
	if task.Args != nil {
		t.Errorf("Args = %v, want nil", task.Args)
	}
}

func TestTOMLCmdsArray(t *testing.T) {
	f := loadTOML(t, `
[tasks.quick]
cmds = ["echo line1", "echo line2"]
`)
	task, ok := f.Tasks["quick"]
	if !ok {
		t.Fatal("quick task not found")
	}
	if task.Fn == nil {
		t.Error("cmds array task should synthesize an Fn")
	}
}

func TestTOMLCmdsOrder(t *testing.T) {
	f := loadTOML(t, `
[tasks.seq]
cmds = ["echo one", "echo two", "echo three"]
`)
	out, err := runTOMLTask(t, f, "seq", f.L.NewTable())
	if err != nil {
		t.Fatal(err)
	}
	i1, i2, i3 := strings.Index(out, "one"), strings.Index(out, "two"), strings.Index(out, "three")
	if i1 < 0 || i2 < 0 || i3 < 0 {
		t.Fatalf("output = %q, want one/two/three all present", out)
	}
	if !(i1 < i2 && i2 < i3) {
		t.Errorf("output = %q, cmds must run in order", out)
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
cmdd = "echo hi"
`)
	if !strings.Contains(err.Error(), "cmdd") {
		t.Errorf("error should name the unknown field: %v", err)
	}
}

// --- load validation: run removed, cmd/cmds exclusivity ---

func TestTOMLRunRemoved(t *testing.T) {
	err := loadTOMLErr(t, `
[tasks.bad]
run = "x.echo(\"hi\")"
`)
	for _, want := range []string{"bad", "removed", "py/cpp/lua"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
	// run alongside cmd still names the removal first.
	err = loadTOMLErr(t, `
[tasks.bad2]
cmd = "echo hi"
run = "x.echo(1)"
`)
	if !strings.Contains(err.Error(), "removed") {
		t.Errorf("error %q should say run was removed", err.Error())
	}
}

func TestTOMLCmdAndCmdsBoth(t *testing.T) {
	err := loadTOMLErr(t, `
[tasks.bad]
cmd = "echo one"
cmds = ["echo two"]
`)
	for _, want := range []string{"bad", "mutually exclusive"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestTOMLNeitherCmdNorCmds(t *testing.T) {
	err := loadTOMLErr(t, `
[tasks.bad]
desc = "incomplete"
`)
	if !strings.Contains(err.Error(), "cmd") || !strings.Contains(err.Error(), "cmds") {
		t.Errorf("error should mention cmd/cmds: %v", err)
	}
}

func TestTOMLCmdNotString(t *testing.T) {
	err := loadTOMLErr(t, `
[tasks.bad]
cmd = [1, 2]
`)
	for _, want := range []string{"bad", "cmd", "string"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestTOMLCmdsNotArray(t *testing.T) {
	err := loadTOMLErr(t, `
[tasks.bad]
cmds = "echo hi"
`)
	for _, want := range []string{"bad", "cmds", "array"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestTOMLCmdsNonStringElements(t *testing.T) {
	err := loadTOMLErr(t, `
[tasks.bad]
cmds = ["echo ok", 2]
`)
	for _, want := range []string{"bad", "cmds", "strings"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

// --- load validation: deps (file-local, full validation at load) ---

func TestTOMLUnknownDep(t *testing.T) {
	err := loadTOMLErr(t, `
[tasks.main]
deps = ["nope"]
cmd = "echo hi"
`)
	for _, want := range []string{"Taskfile.toml", "main", "nope", "unknown dep"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestTOMLDepWhenFiltered(t *testing.T) {
	err := loadTOMLErr(t, `
[tasks.gone]
cmd = "echo hi"
when = { os = "not-an-os" }

[tasks.main]
deps = ["gone"]
cmd = "echo hi"
`)
	for _, want := range []string{"Taskfile.toml", "main", "gone", "unknown dep"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestTOMLDepHasRequiredArgs(t *testing.T) {
	err := loadTOMLErr(t, `
[tasks.build]
cmd = "echo hi"
[tasks.build.args.tag]
required = true

[tasks.main]
deps = ["build"]
cmd = "echo hi"
`)
	for _, want := range []string{"Taskfile.toml", "main", "build", "required"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestTOMLDepCycle(t *testing.T) {
	err := loadTOMLErr(t, `
[tasks.a]
deps = ["c"]
cmd = "echo a"

[tasks.b]
deps = ["a"]
cmd = "echo b"

[tasks.c]
deps = ["b"]
cmd = "echo c"
`)
	for _, want := range []string{"cycle", "a -> c -> b -> a"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestTOMLDepSelfCycle(t *testing.T) {
	err := loadTOMLErr(t, `
[tasks.selfy]
deps = ["selfy"]
cmd = "echo hi"
`)
	for _, want := range []string{"cycle", "selfy -> selfy"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

// --- load validation: placeholders ---

func TestTOMLUnknownPlaceholder(t *testing.T) {
	err := loadTOMLErr(t, `
[tasks.bad]
cmd = "echo {{typo}}"
`)
	for _, want := range []string{"bad", "unknown placeholder", "{{typo}}"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestTOMLEmptyPlaceholder(t *testing.T) {
	err := loadTOMLErr(t, `
[tasks.bad]
cmd = "echo {{ }}"
`)
	for _, want := range []string{"bad", "unknown placeholder", "{{ }}"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestTOMLUnclosedPlaceholder(t *testing.T) {
	err := loadTOMLErr(t, `
[tasks.bad]
cmd = "echo {{oops"
`)
	for _, want := range []string{"bad", "unclosed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestTOMLConditionalOnNonBool(t *testing.T) {
	err := loadTOMLErr(t, `
[tasks.bad]
cmd = "git push {{tag:+--force}}"
[tasks.bad.args.tag]
required = true
`)
	for _, want := range []string{"bad", "tag", "bool"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestTOMLBoolPlainForm(t *testing.T) {
	err := loadTOMLErr(t, `
[tasks.bad]
cmd = "echo {{force}}"
[tasks.bad.args.force]
type = "bool"
`)
	for _, want := range []string{"bad", "force", "conditional"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

// --- interpolation execution ---

func TestTOMLInterpolationString(t *testing.T) {
	f := loadTOML(t, `
[tasks.hello]
cmd = "echo hello-{{name}}"
[tasks.hello.args.name]
required = true
desc = "who to greet"
`)
	task := f.Tasks["hello"]
	if sp := task.Args["name"]; !sp.Required || sp.Type != "string" || sp.Desc != "who to greet" {
		t.Errorf("name spec = %+v", sp)
	}
	args := f.L.NewTable()
	args.RawSetString("name", lua.LString("toml"))
	out, err := runTOMLTask(t, f, "hello", args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hello-toml") {
		t.Errorf("output = %q, want interpolated greeting", out)
	}
}

func TestTOMLInterpolationOptionalMissing(t *testing.T) {
	f := loadTOML(t, `
[tasks.opt]
cmd = "echo greet=[{{opt}}]"
[tasks.opt.args.opt]
desc = "optional suffix"
`)
	out, err := runTOMLTask(t, f, "opt", f.L.NewTable())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "greet=[]") {
		t.Errorf("output = %q, want optional-missing to render empty", out)
	}
}

func TestTOMLInterpolationSpaces(t *testing.T) {
	f := loadTOML(t, `
[tasks.spaced]
cmd = "echo a {{msg}} b"
[tasks.spaced.args.msg]
required = true
`)
	args := f.L.NewTable()
	args.RawSetString("msg", lua.LString("two words"))
	out, err := runTOMLTask(t, f, "spaced", args)
	if err != nil {
		t.Fatal(err)
	}
	// pwsh echoes each argument on its own line, bash keeps one line;
	// what matters is the words arriving literally and in order.
	ia, i1, i2, ib := strings.Index(out, "a "), strings.Index(out, "two"), strings.Index(out, "words"), strings.Index(out, " b")
	if i1 < 0 || i2 < 0 {
		t.Fatalf("output = %q, want the space-containing value inserted literally", out)
	}
	if !(i1 < i2) {
		t.Errorf("output = %q, want words in value order", out)
	}
	if ia >= 0 && ib >= 0 && !(ia < i1 && i2 < ib) {
		t.Errorf("output = %q, want the value between the literals", out)
	}
}

func TestTOMLInterpolationNum(t *testing.T) {
	f := loadTOML(t, `
[tasks.serve]
cmds = ["echo port={{port}}", "echo ratio={{ratio}}"]
[tasks.serve.args.port]
type = "num"
required = true
[tasks.serve.args.ratio]
type = "num"
`)
	args := f.L.NewTable()
	args.RawSetString("port", lua.LNumber(8080))
	args.RawSetString("ratio", lua.LNumber(2.5))
	out, err := runTOMLTask(t, f, "serve", args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "port=8080") {
		t.Errorf("output = %q, want shortest num form", out)
	}
	if strings.Contains(out, "8080.000000") {
		t.Errorf("output = %q, num must not render with decimals", out)
	}
	if !strings.Contains(out, "ratio=2.5") {
		t.Errorf("output = %q, want 2.5 rendered as given", out)
	}
}

func TestTOMLBoolConditionalTrue(t *testing.T) {
	f := loadTOML(t, `
[tasks.push]
cmd = "echo push{{force:+ --force}}"
[tasks.push.args.force]
type = "bool"
`)
	args := f.L.NewTable()
	args.RawSetString("force", lua.LBool(true))
	out, err := runTOMLTask(t, f, "push", args)
	if err != nil {
		t.Fatal(err)
	}
	// pwsh echoes each argument on its own line; order still proves insertion.
	ip, ifr := strings.Index(out, "push"), strings.Index(out, "--force")
	if ip < 0 || ifr < 0 || !(ip < ifr) {
		t.Errorf("output = %q, want conditional word inserted after the text", out)
	}
}

func TestTOMLBoolConditionalFalseAndMissing(t *testing.T) {
	f := loadTOML(t, `
[tasks.push]
cmd = "echo push{{force:+ --force}}"
[tasks.push.args.force]
type = "bool"
`)
	out, err := runTOMLTask(t, f, "push", f.L.NewTable()) // missing optional bool
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "push") || strings.Contains(out, "--force") {
		t.Errorf("output = %q, want no insertion for a missing bool", out)
	}
	args := f.L.NewTable()
	args.RawSetString("force", lua.LBool(false))
	out, err = runTOMLTask(t, f, "push", args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "push") || strings.Contains(out, "--force") {
		t.Errorf("output = %q, want no insertion for false", out)
	}
}

// --- prefix x interpolation (Review Focus 1 combos) ---

func TestTOMLPrefixInterpolation(t *testing.T) {
	mustShell(t, "bash")
	f := loadTOML(t, `
[tasks.pfx]
cmd = "bash:echo via-{{name}}"
[tasks.pfx.args.name]
required = true
`)
	args := f.L.NewTable()
	args.RawSetString("name", lua.LString("bash-toml"))
	out, err := runTOMLTask(t, f, "pfx", args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "via-bash-toml") {
		t.Errorf("output = %q, want prefix + interpolated text in bash", out)
	}
}

func TestTOMLPrefixConditional(t *testing.T) {
	mustShell(t, "bash")
	f := loadTOML(t, `
[tasks.pfxc]
cmd = "bash:echo flags{{force:+ --yes}}"
[tasks.pfxc.args.force]
type = "bool"
`)
	args := f.L.NewTable()
	args.RawSetString("force", lua.LBool(true))
	out, err := runTOMLTask(t, f, "pfxc", args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "flags --yes") {
		t.Errorf("output = %q, want conditional insertion under a prefix", out)
	}
}

// TestTOMLRegistryPrefixInterpolation: a config-registered shell prefix
// is not a builtin, so the load-time split leaves it in the command
// text; the runtime re-splits the rendered string — same one-shot run.
func TestTOMLRegistryPrefixInterpolation(t *testing.T) {
	spec, err := shell.Resolve("bash", map[string]string{})
	if err != nil {
		t.Skipf("skipping: %v", err)
	}
	f := loadTOML(t, `
[tasks.regpfx]
cmd = "mysh:echo via-{{name}}"
[tasks.regpfx.args.name]
required = true
`)
	args := f.L.NewTable()
	args.RawSetString("name", lua.LString("registry"))
	var out bytes.Buffer
	rerr := runtime.Run(adapter.NewChain(f), "regpfx", args, runtime.Options{
		Stdout: &out, Stderr: &out, Stdin: os.Stdin,
		Registry: map[string]string{"mysh": spec.Path},
	})
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !strings.Contains(out.String(), "via-registry") {
		t.Errorf("output = %q, want interpolated text under a registry prefix", out.String())
	}
}

// --- deps execution: order, recursion, error-stop, empty args ---

func TestTOMLDepsOrder(t *testing.T) {
	f := loadTOML(t, `
[tasks.first]
cmd = "echo dep-first"

[tasks.second]
cmd = "echo dep-second"

[tasks.main]
deps = ["first", "second"]
cmd = "echo main-ran"
`)
	out, err := runTOMLTask(t, f, "main", f.L.NewTable())
	if err != nil {
		t.Fatal(err)
	}
	i1, i2, i3 := strings.Index(out, "dep-first"), strings.Index(out, "dep-second"), strings.Index(out, "main-ran")
	if i1 < 0 || i2 < 0 || i3 < 0 {
		t.Fatalf("output = %q, want all three markers", out)
	}
	if !(i1 < i2 && i2 < i3) {
		t.Errorf("output = %q, deps must run in listed order before the body", out)
	}
}

func TestTOMLDepsRecursive(t *testing.T) {
	f := loadTOML(t, `
[tasks.gen]
cmd = "echo ran-gen"

[tasks.build]
deps = ["gen"]
cmd = "echo ran-build"

[tasks.release]
deps = ["build"]
cmd = "echo ran-release"
`)
	out, err := runTOMLTask(t, f, "release", f.L.NewTable())
	if err != nil {
		t.Fatal(err)
	}
	ig, ib, ir := strings.Index(out, "ran-gen"), strings.Index(out, "ran-build"), strings.Index(out, "ran-release")
	if ig < 0 || ib < 0 || ir < 0 {
		t.Fatalf("output = %q, want gen/build/release all present", out)
	}
	if !(ig < ib && ib < ir) {
		t.Errorf("output = %q, transitive deps must run innermost first", out)
	}
}

func TestTOMLDepsErrorStops(t *testing.T) {
	f := loadTOML(t, `
[tasks.boom]
cmd = "exit 3"

[tasks.parent]
deps = ["boom"]
cmd = "echo parent-ran"
`)
	out, err := runTOMLTask(t, f, "parent", f.L.NewTable())
	if err == nil {
		t.Fatalf("expected the failing dep to error the task; output %q", out)
	}
	if strings.Contains(out, "parent-ran") {
		t.Errorf("output = %q, body must not run after a dep fails", out)
	}
}

func TestTOMLDepsGetEmptyArgs(t *testing.T) {
	f := loadTOML(t, `
[tasks.greeter]
cmd = "echo greet=[{{opt}}]"
[tasks.greeter.args.opt]
desc = "optional greeting"

[tasks.host]
deps = ["greeter"]
cmd = "echo host-ran"
`)
	out, err := runTOMLTask(t, f, "host", f.L.NewTable())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "greet=[]") {
		t.Errorf("output = %q, want the dep's optional arg to render empty", out)
	}
	if !strings.Contains(out, "host-ran") {
		t.Errorf("output = %q, want the host body to run", out)
	}
}

// --- arg defaults (v2.3) ---

func TestTOMLDefaultUnpassed(t *testing.T) {
	f := loadTOML(t, `
[tasks.serve]
cmd = "echo jobs={{jobs}} flag={{flag:+--yes}} msg={{msg}}"
[tasks.serve.args.jobs]
type = "num"
default = 4
[tasks.serve.args.flag]
type = "bool"
default = true
[tasks.serve.args.msg]
default = "hi"
`)
	out, err := runTOMLTask(t, f, "serve", f.L.NewTable())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "jobs=4") || strings.Contains(out, "jobs=4.0") {
		t.Errorf("output = %q, want num default in shortest form", out)
	}
	if !strings.Contains(out, "--yes") {
		t.Errorf("output = %q, want a true bool default to insert the conditional word", out)
	}
	if !strings.Contains(out, "msg=hi") {
		t.Errorf("output = %q, want the string default verbatim", out)
	}

	// An explicitly passed arg still wins over its default.
	args := f.L.NewTable()
	args.RawSetString("jobs", lua.LNumber(9))
	args.RawSetString("flag", lua.LBool(false))
	args.RawSetString("msg", lua.LString("bye"))
	out, err = runTOMLTask(t, f, "serve", args)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "jobs=4") || strings.Contains(out, "--yes") || strings.Contains(out, "msg=hi") {
		t.Errorf("output = %q, explicit args must override defaults", out)
	}
	if !strings.Contains(out, "jobs=9") || !strings.Contains(out, "msg=bye") {
		t.Errorf("output = %q, want the passed values rendered", out)
	}
}

func TestTOMLDefaultTypeMismatch(t *testing.T) {
	err := loadTOMLErr(t, `
[tasks.serve]
cmd = "echo {{port}}"
[tasks.serve.args.port]
type = "num"
default = "8080"
`)
	for _, want := range []string{"Taskfile.toml", "serve", "port", "default", "num"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestTOMLDefaultRequiredConflict(t *testing.T) {
	err := loadTOMLErr(t, `
[tasks.tag]
cmd = "echo {{tag}}"
[tasks.tag.args.tag]
required = true
default = "v1"
`)
	for _, want := range []string{"Taskfile.toml", "tag", "required", "default"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}
