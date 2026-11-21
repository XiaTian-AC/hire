package adapter

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	lua "github.com/yuin/gopher-lua"
)

func loadSrc(t *testing.T, src string) *File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Taskfile.lua")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := LoadLua(path)
	if err != nil {
		t.Fatalf("LoadLua(%q): %v", src, err)
	}
	t.Cleanup(f.Close)
	return f
}

func loadErr(t *testing.T, src string) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Taskfile.lua")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := LoadLua(path)
	if err == nil {
		t.Fatalf("LoadLua(%q): expected error, got nil", src)
	}
	if f != nil {
		f.Close()
	}
	return err
}

// The four v2 declaration forms each produce a runnable Task (Fn set).
func TestStringForm(t *testing.T) {
	f := loadSrc(t, `clean = "rm -rf dist/"`)
	task, ok := f.Tasks["clean"]
	if !ok {
		t.Fatal("clean task not found")
	}
	if task.Fn == nil {
		t.Error("string shorthand should synthesize an Fn")
	}
	if task.Desc != "" {
		t.Errorf("Desc = %q, want empty", task.Desc)
	}
	if task.Args != nil {
		t.Errorf("Args = %v, want nil", task.Args)
	}
	if len(f.Order) != 1 || f.Order[0] != "clean" {
		t.Errorf("Order = %v", f.Order)
	}
}

func TestArrayForm(t *testing.T) {
	f := loadSrc(t, `quick = { "echo line1", "echo line2" }`)
	task, ok := f.Tasks["quick"]
	if !ok {
		t.Fatal("quick task not found")
	}
	if task.Fn == nil {
		t.Error("array shorthand should synthesize an Fn")
	}
}

func TestBareFunctionForm(t *testing.T) {
	f := loadSrc(t, `hello = function() x.echo("hi") end`)
	task, ok := f.Tasks["hello"]
	if !ok {
		t.Fatal("hello task not found")
	}
	if task.Fn == nil {
		t.Error("bare function task should keep its Fn")
	}
	if task.Args != nil {
		t.Errorf("Args = %v, want nil for bare function", task.Args)
	}
}

func TestTaskForm(t *testing.T) {
	f := loadSrc(t, `
release = task {
  desc = "build and release",
  args = {
    tag   = req "git tag",
    force = bool "force push",
    limit = { desc = "concurrent" },
  },
  run = function(args) end,
}`)
	task, ok := f.Tasks["release"]
	if !ok {
		t.Fatal("release task not found")
	}
	if task.Desc != "build and release" {
		t.Errorf("Desc = %q", task.Desc)
	}
	if task.Fn == nil {
		t.Error("task{} run should keep its Fn")
	}
	tag := task.Args["tag"]
	if tag.Type != "string" || !tag.Required || tag.Desc != "git tag" {
		t.Errorf("tag spec = %+v", tag)
	}
	force := task.Args["force"]
	if force.Type != "bool" || force.Required {
		t.Errorf("force spec = %+v", force)
	}
	limit := task.Args["limit"]
	if limit.Type != "string" || limit.Required || limit.Desc != "concurrent" {
		t.Errorf("limit spec = %+v", limit)
	}
}

func TestCmdTaskForm(t *testing.T) {
	f := loadSrc(t, `build = task { cmd = "go build -o app.exe ." }`)
	task, ok := f.Tasks["build"]
	if !ok {
		t.Fatal("build task not found")
	}
	if task.Fn == nil {
		t.Error("cmd task should synthesize an Fn")
	}
}

func TestWhenFilters(t *testing.T) {
	matching := loadSrc(t, fmt.Sprintf(strings.Join([]string{
		`here = task { cmd = "echo hi", when = { os = %q } }` + "\n",
		`multi = task { cmd = "echo hi", when = { os = {"other", %q} } }` + "\n",
		`gone = task { cmd = "echo hi", when = { os = "not-an-os" } }`,
	}, ""), runtime.GOOS, runtime.GOOS))
	if _, ok := matching.Tasks["here"]; !ok {
		t.Error("matching single-os task should exist")
	}
	if _, ok := matching.Tasks["multi"]; !ok {
		t.Error("matching os-array task should exist")
	}
	if _, ok := matching.Tasks["gone"]; ok {
		t.Error("non-matching when should make the task not exist")
	}
	if len(matching.Order) != 2 {
		t.Errorf("Order = %v, want 2 tasks", matching.Order)
	}
}

func TestShellDeclaration(t *testing.T) {
	f := loadSrc(t, `
shell = "pwsh"
build = "go build ."
`)
	if f.DefaultShell != "pwsh" {
		t.Errorf("DefaultShell = %q, want pwsh", f.DefaultShell)
	}
	if _, ok := f.Tasks["shell"]; ok {
		t.Error("shell must not be a task")
	}
	if _, ok := f.Tasks["build"]; !ok {
		t.Error("build task not found")
	}
}

func TestReservedTaskPrefix(t *testing.T) {
	err := loadErr(t, `__meta = "echo hi"`)
	if !strings.Contains(err.Error(), "__") {
		t.Errorf("error should mention __ prefix: %v", err)
	}
}

func TestReservedArgShell(t *testing.T) {
	err := loadErr(t, `bad = task {
  args = { shell = req "oops" },
  run = function() end,
}`)
	if !strings.Contains(err.Error(), "shell") {
		t.Errorf("error should mention shell: %v", err)
	}
}

func TestReservedGlobals(t *testing.T) {
	for _, name := range []string{"x", "task", "req", "bool"} {
		err := loadErr(t, name+` = "echo hi"`)
		if !strings.Contains(err.Error(), "reserved") {
			t.Errorf("%s: error should mention reserved: %v", name, err)
		}
	}
	err := loadErr(t, `shell = 5`)
	if !strings.Contains(err.Error(), "reserved") {
		t.Errorf("shell number: error should mention reserved: %v", err)
	}
}

func TestOldSyntaxRejected(t *testing.T) {
	err := loadErr(t, `return {
  build = function(ctx) ctx:sh("cargo build") end,
}`)
	if !strings.Contains(err.Error(), "v1") {
		t.Errorf("error should identify old v1 syntax: %v", err)
	}
}

func TestNoTasks(t *testing.T) {
	err := loadErr(t, `local helper = 1`)
	if !strings.Contains(err.Error(), "no tasks") {
		t.Errorf("error should say no tasks: %v", err)
	}
}

func TestUnmarkedTable(t *testing.T) {
	err := loadErr(t, `foo = { desc = "not a task" }`)
	if !strings.Contains(err.Error(), "task{") {
		t.Errorf("error should suggest task{}: %v", err)
	}
}

func TestTaskNeedsRunOrCmd(t *testing.T) {
	err := loadErr(t, `bad = task { desc = "incomplete" }`)
	if !strings.Contains(err.Error(), "run") || !strings.Contains(err.Error(), "cmd") {
		t.Errorf("error should mention run/cmd: %v", err)
	}
}

func TestTaskRunAndCmdBoth(t *testing.T) {
	err := loadErr(t, `bad = task { cmd = "echo hi", run = function() end }`)
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error should mention exclusivity: %v", err)
	}
}

func TestInvalidValueTypeSkipped(t *testing.T) {
	f := loadSrc(t, `
foo = 42
bar = "echo ok"
`)
	if _, ok := f.Tasks["foo"]; ok {
		t.Error("number global should be skipped, not a task")
	}
	if _, ok := f.Tasks["bar"]; !ok {
		t.Error("bar task not found")
	}
	if len(f.Order) != 1 {
		t.Errorf("Order = %v", f.Order)
	}
}

// Snapshot-diff: redefining a builtin (print) is a task candidate; untouched
// builtins (string, pairs, ...) are not collected.
func TestSnapshotDiff(t *testing.T) {
	f := loadSrc(t, `
print = "echo via print"
real = "echo real"
local s = string
`)
	if _, ok := f.Tasks["print"]; !ok {
		t.Error("redefined print should be a task")
	}
	if _, ok := f.Tasks["real"]; !ok {
		t.Error("real task not found")
	}
	for _, builtin := range []string{"string", "pairs", "os", "io"} {
		if _, ok := f.Tasks[builtin]; ok {
			t.Errorf("builtin %q must not be collected as a task", builtin)
		}
	}
	if len(f.Tasks) != 2 {
		t.Errorf("Tasks = %v, want exactly print and real", f.Tasks)
	}
}

func TestLoadLuaMissingFile(t *testing.T) {
	_, err := LoadLua(filepath.Join(t.TempDir(), "nonexistent.lua"))
	if err == nil || !strings.Contains(err.Error(), "no Taskfile") {
		t.Errorf("expected 'no Taskfile' error, got %v", err)
	}
}

func TestLoadLuaSyntaxError(t *testing.T) {
	err := loadErr(t, `this is not valid lua!!!`)
	_ = err // any error is fine
}

func TestChainMergeLaterWins(t *testing.T) {
	g := loadSrc(t, `
shell = "bash"
dup = "echo from global"
onlyglobal = "echo global"
`)
	l := loadSrc(t, `
shell = "pwsh"
dup = "echo from local"
onlylocal = "echo local"
`)
	c := NewChain(g, l)
	if got := c.Tasks["dup"].File; got != l.Tasks["dup"].File {
		t.Errorf("dup should be the local one, got file %q", got)
	}
	want := []string{"dup", "onlyglobal", "onlylocal"}
	if len(c.Order) != len(want) {
		t.Fatalf("Order = %v", c.Order)
	}
	for i, name := range want {
		if c.Order[i] != name {
			t.Errorf("Order[%d] = %q, want %q", i, c.Order[i], name)
		}
	}
	if c.DefaultShell != "pwsh" {
		t.Errorf("DefaultShell = %q, want pwsh (later overrides)", c.DefaultShell)
	}
}

func TestChainDefaultShellKeptWhenLaterFileHasNone(t *testing.T) {
	g := loadSrc(t, "shell = \"bash\"\na = \"echo a\"")
	l := loadSrc(t, "b = \"echo b\"")
	c := NewChain(g, l)
	if c.DefaultShell != "bash" {
		t.Errorf("DefaultShell = %q, want bash kept", c.DefaultShell)
	}
}

// Lua sugar helpers produce full-form spec tables (used by task{} args).
func TestSugarHelpers(t *testing.T) {
	L := lua.NewState()
	defer L.Close()
	L.SetGlobal("req", L.NewFunction(luaReqHelper))
	L.SetGlobal("bool", L.NewFunction(luaBoolHelper))
	if err := L.DoString(`return req "the desc", bool "other desc"`); err != nil {
		t.Fatal(err)
	}
	req := L.Get(1).(*lua.LTable)
	if req.RawGetString("type") != lua.LString("string") ||
		req.RawGetString("required") != lua.LTrue ||
		req.RawGetString("desc") != lua.LString("the desc") {
		t.Errorf("req spec = %+v", req)
	}
	b := L.Get(2).(*lua.LTable)
	if b.RawGetString("type") != lua.LString("bool") ||
		b.RawGetString("required") != lua.LFalse ||
		b.RawGetString("desc") != lua.LString("other desc") {
		t.Errorf("bool spec = %+v", b)
	}
}
