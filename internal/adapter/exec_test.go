package adapter_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"

	"hire/internal/adapter"
	"hire/internal/runtime"
)

// fakeInterp builds the __hire__ contract test double once per test run,
// so contract tests run even on machines without python/node/g++.
var fakeInterp = sync.OnceValue(func() string {
	dir, err := os.MkdirTemp("", "hire-fakeinterp-")
	if err != nil {
		panic(err)
	}
	exe := filepath.Join(dir, "fakeinterp")
	if goruntime.GOOS == "windows" {
		exe += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", exe, "main.go")
	cmd.Dir = filepath.Join("testdata", "fakeinterp")
	if out, err := cmd.CombinedOutput(); err != nil {
		panic("go build fakeinterp: " + err.Error() + "\n" + string(out))
	}
	return exe
})

// probeCmd returns the first name found on PATH, skipping the test when
// none is (toolchains are optional on dev machines).
func probeCmd(t *testing.T, names ...string) string {
	t.Helper()
	for _, n := range names {
		if _, err := exec.LookPath(n); err == nil {
			return n
		}
	}
	t.Skipf("none of %s found in PATH", strings.Join(names, ", "))
	return ""
}

// invokeTask calls a task's Fn the way runtime.callTask does: one
// args-table argument, the Lua error returned as-is.
func invokeTask(f *adapter.File, name string) error {
	L := f.L
	L.Push(f.Tasks[name].Fn)
	L.Push(L.NewTable())
	return L.PCall(1, 0, nil)
}

// trickyArgs carries spaces, quotes, a newline escape, a tab escape and
// non-ASCII text: the child must receive the exact bytes back.
const trickyArgs = `{"msg":"he said \"hi\"\n中文 second line\ttab","force":true,"_":["positional with space"]}`

// runEchoBoom asserts the two-task contract behavior shared by the fake
// and python taskfiles: tricky HIRE_ARGS round-trips exactly through the
// child, and boom's non-zero exit surfaces as a Lua error.
func runEchoBoom(t *testing.T, f *adapter.File) {
	t.Helper()
	writeTo := filepath.Join(t.TempDir(), "hire-args-out.txt")
	t.Setenv("HIRE_ARGS", trickyArgs)
	t.Setenv("HIRE_WRITE", writeTo)
	if err := invokeTask(f, "echo"); err != nil {
		t.Fatalf("echo run: %v", err)
	}
	got, err := os.ReadFile(writeTo)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != trickyArgs {
		t.Errorf("HIRE_ARGS round trip:\n got %q\nwant %q", string(got), trickyArgs)
	}
	// Spec §11 final form, through the runner wrap: single task name,
	// exact string (cli later prefixes "hire: ").
	err = runtime.Run(adapter.NewChain(f), "boom", f.L.NewTable(), runtime.Options{Registry: map[string]string{}})
	if err == nil {
		t.Fatal("boom should fail")
	}
	if want := `task "boom" failed (exit 3)`; err.Error() != want {
		t.Errorf("boom error = %q, want exactly %q", err.Error(), want)
	}
}

// The fake interpreter covers the whole contract without any external
// toolchain: discover parsing, args schema, HIRE_ARGS injection and the
// exit-code-to-Lua-error conversion.
func TestExecContractFake(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Taskfile.fake")
	if err := os.WriteFile(path, []byte("# content is irrelevant to fakeinterp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := adapter.LoadExec(path, adapter.RuntimeSpec{Cmd: fakeInterp()}, "fake")
	if err != nil {
		t.Fatalf("LoadExec: %v", err)
	}
	t.Cleanup(f.Close)

	if strings.Join(f.Order, ",") != "boom,echo" {
		t.Errorf("Order = %v, want [boom echo]", f.Order)
	}
	echo := f.Tasks["echo"]
	if echo == nil {
		t.Fatalf("echo task missing; tasks = %v", f.Order)
	}
	if echo.Desc != "writes HIRE_ARGS to HIRE_WRITE" {
		t.Errorf("echo.Desc = %q", echo.Desc)
	}
	msg := echo.Args["msg"]
	if msg.Type != "string" || msg.Required || msg.Desc != "a message" {
		t.Errorf("echo.Args[msg] = %+v", msg)
	}
	force := echo.Args["force"]
	if force.Type != "bool" || force.Required || force.Desc != "a flag" {
		t.Errorf("echo.Args[force] = %+v", force)
	}
	if len(echo.Args) != 2 {
		t.Errorf("echo.Args has %d entries, want 2", len(echo.Args))
	}
	if f.Tasks["boom"].Args != nil {
		t.Errorf("boom.Args should be nil, got %v", f.Tasks["boom"].Args)
	}

	runEchoBoom(t, f)
}

func TestExecDiscoverInvalidJSON(t *testing.T) {
	t.Setenv("FAKE_MODE", "garbage")
	path := filepath.Join(t.TempDir(), "Taskfile.fake")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := adapter.LoadExec(path, adapter.RuntimeSpec{Cmd: fakeInterp()}, "fake")
	if err == nil {
		t.Fatal("expected error for malformed discover JSON")
	}
	for _, want := range []string{path, "invalid discover JSON"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestExecDiscoverNonZeroExit(t *testing.T) {
	t.Setenv("FAKE_MODE", "fail")
	path := filepath.Join(t.TempDir(), "Taskfile.fake")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := adapter.LoadExec(path, adapter.RuntimeSpec{Cmd: fakeInterp()}, "fake")
	if err == nil {
		t.Fatal("expected error for failing discover")
	}
	for _, want := range []string{path, "exit 7", "discover exploded"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestExecBadArgType(t *testing.T) {
	t.Setenv("FAKE_MODE", "badtype")
	path := filepath.Join(t.TempDir(), "Taskfile.fake")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := adapter.LoadExec(path, adapter.RuntimeSpec{Cmd: fakeInterp()}, "fake")
	if err == nil {
		t.Fatal("expected error for invalid arg type in discover JSON")
	}
	for _, want := range []string{path, `task "t"`, `invalid type "integer"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

// The discover schema passes a num arg through: validArgType accepts it
// and the spec reaches the task intact (裁决 6).
func TestExecNumArgType(t *testing.T) {
	t.Setenv("FAKE_MODE", "numtype")
	path := filepath.Join(t.TempDir(), "Taskfile.fake")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := adapter.LoadExec(path, adapter.RuntimeSpec{Cmd: fakeInterp()}, "fake")
	if err != nil {
		t.Fatalf("LoadExec with num arg: %v", err)
	}
	t.Cleanup(f.Close)
	n := f.Tasks["t"].Args["n"]
	if n.Type != "num" || n.Required || n.Desc != "a number" {
		t.Errorf("t.Args[n] = %+v, want optional num", n)
	}
}

func TestExecFloatArgType(t *testing.T) {
	t.Setenv("FAKE_MODE", "floattype")
	path := filepath.Join(t.TempDir(), "Taskfile.fake")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := adapter.LoadExec(path, adapter.RuntimeSpec{Cmd: fakeInterp()}, "fake")
	if err == nil {
		t.Fatal("expected error for type float in discover JSON")
	}
	for _, want := range []string{path, `task "t"`, `invalid type "float"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestExecReservedNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Taskfile.fake")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := adapter.RuntimeSpec{Cmd: fakeInterp()}
	t.Setenv("FAKE_MODE", "reserved")
	_, err := adapter.LoadExec(path, spec, "fake")
	if err == nil || !strings.Contains(err.Error(), `"__secret"`) || !strings.Contains(err.Error(), "__") {
		t.Errorf("reserved task name: err = %v", err)
	}
	t.Setenv("FAKE_MODE", "shellarg")
	_, err = adapter.LoadExec(path, spec, "fake")
	if err == nil || !strings.Contains(err.Error(), `"shell"`) {
		t.Errorf("reserved arg name: err = %v", err)
	}
}

const pythonTaskfile = `import json, os, sys

def main():
    a = sys.argv[1:]
    if "__hire__" not in a:
        sys.exit(2)
    i = a.index("__hire__")
    verb = a[i+1] if i+1 < len(a) else ""
    if verb == "discover":
        tasks = [
            {"name": "echo", "desc": "writes HIRE_ARGS to HIRE_WRITE",
             "args": {"msg": {"type": "string", "required": True, "desc": "a message"}}},
            {"name": "boom", "desc": "always fails"},
        ]
        print(json.dumps({"tasks": tasks}))
        return
    if verb == "run" and i+2 < len(a):
        if a[i+2] == "echo":
            with open(os.environ["HIRE_WRITE"], "w", encoding="utf-8") as f:
                f.write(os.environ.get("HIRE_ARGS", ""))
            return
        if a[i+2] == "boom":
            print("boom: by design", file=sys.stderr)
            sys.exit(3)
    sys.exit(2)

main()
`

// A real python round trip: a taskfile speaking the contract through the
// actual interpreter, discover plus run, including tricky HIRE_ARGS bytes.
func TestExecPythonRoundTrip(t *testing.T) {
	py := probeCmd(t, "python3", "python")
	path := filepath.Join(t.TempDir(), "Taskfile.py")
	if err := os.WriteFile(path, []byte(pythonTaskfile), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := adapter.LoadExec(path, adapter.RuntimeSpec{Cmd: py}, "python")
	if err != nil {
		t.Fatalf("LoadExec: %v", err)
	}
	t.Cleanup(f.Close)

	if strings.Join(f.Order, ",") != "boom,echo" {
		t.Errorf("Order = %v, want [boom echo]", f.Order)
	}
	echo := f.Tasks["echo"]
	if echo == nil {
		t.Fatalf("echo missing; tasks = %v", f.Order)
	}
	if echo.Desc != "writes HIRE_ARGS to HIRE_WRITE" {
		t.Errorf("echo.Desc = %q", echo.Desc)
	}
	msg := echo.Args["msg"]
	if msg.Type != "string" || !msg.Required || msg.Desc != "a message" {
		t.Errorf("echo.Args[msg] = %+v", msg)
	}
	runEchoBoom(t, f)
}

const nodeTaskfile = `const a = process.argv.slice(2);
const i = a.indexOf("__hire__");
if (i < 0) process.exit(2);
const verb = a[i + 1];
if (verb === "discover") {
  console.log(JSON.stringify({ tasks: [{ name: "hello", desc: "node hello" }] }));
} else if (verb === "run" && a[i + 2] === "hello") {
  require("fs").writeFileSync(process.env.HIRE_WRITE, process.env.HIRE_ARGS || "{}");
} else {
  process.exit(2);
}
`

func TestExecNodeRoundTrip(t *testing.T) {
	node := probeCmd(t, "node")
	path := filepath.Join(t.TempDir(), "Taskfile.js")
	if err := os.WriteFile(path, []byte(nodeTaskfile), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := adapter.LoadExec(path, adapter.RuntimeSpec{Cmd: node}, "node")
	if err != nil {
		t.Fatalf("LoadExec: %v", err)
	}
	t.Cleanup(f.Close)
	hello := f.Tasks["hello"]
	if hello == nil {
		t.Fatalf("hello missing; tasks = %v", f.Order)
	}
	if hello.Desc != "node hello" {
		t.Errorf("hello.Desc = %q", hello.Desc)
	}
	writeTo := filepath.Join(t.TempDir(), "node-args-out.txt")
	t.Setenv("HIRE_ARGS", trickyArgs)
	t.Setenv("HIRE_WRITE", writeTo)
	if err := invokeTask(f, "hello"); err != nil {
		t.Fatalf("hello run: %v", err)
	}
	got, err := os.ReadFile(writeTo)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != trickyArgs {
		t.Errorf("node HIRE_ARGS round trip:\n got %q\nwant %q", string(got), trickyArgs)
	}
}

// TestExecWhenOS: a discover task may declare when.os (string or array).
// hire filters at load time, so a non-matching task does not exist at all
// (ls hides it, run reports not found) while a matching one loads normally.
func TestExecWhenOS(t *testing.T) {
	t.Setenv("FAKE_MODE", "when")
	path := filepath.Join(t.TempDir(), "Taskfile.fake")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := adapter.LoadExec(path, adapter.RuntimeSpec{Cmd: fakeInterp()}, "fake")
	if err != nil {
		t.Fatalf("LoadExec: %v", err)
	}
	t.Cleanup(f.Close)

	wantWin := goruntime.GOOS == "windows"
	wantLinux := goruntime.GOOS == "linux"
	if _, ok := f.Tasks["winonly"]; ok != wantWin {
		t.Errorf("winonly present = %v, want %v (tasks %v)", ok, wantWin, f.Order)
	}
	if _, ok := f.Tasks["linuxonly"]; ok != wantLinux {
		t.Errorf("linuxonly present = %v, want %v (tasks %v)", ok, wantLinux, f.Order)
	}
	if _, ok := f.Tasks["here"]; !ok {
		t.Errorf("task with the current os must exist (tasks %v)", f.Order)
	}
	if _, ok := f.Tasks["multi"]; !ok {
		t.Errorf("task whose os array contains the current os must exist (tasks %v)", f.Order)
	}
	if _, ok := f.Tasks["gone"]; ok {
		t.Errorf("non-matching when.os must hide the task (tasks %v)", f.Order)
	}
	wantCount := 2 // here + multi on any other platform
	if wantWin || wantLinux {
		wantCount = 3
	}
	if len(f.Order) != wantCount {
		t.Errorf("Order = %v, want %d matching tasks", f.Order, wantCount)
	}
}

// checkSDKWhenTasks asserts the platform filter on tasks declared through
// an SDK's task(..., when=...), not just hand-written discover JSON.
func checkSDKWhenTasks(t *testing.T, f *adapter.File) {
	t.Helper()
	wantWin := goruntime.GOOS == "windows"
	wantLinux := goruntime.GOOS == "linux"
	if _, ok := f.Tasks["win"]; ok != wantWin {
		t.Errorf("win present = %v, want %v (tasks %v)", ok, wantWin, f.Order)
	}
	if _, ok := f.Tasks["linux"]; ok != wantLinux {
		t.Errorf("linux present = %v, want %v (tasks %v)", ok, wantLinux, f.Order)
	}
	if _, ok := f.Tasks["here"]; !ok {
		t.Errorf("current-os task must exist (tasks %v)", f.Order)
	}
}

func TestExecPythonSDKWhenOS(t *testing.T) {
	py := probeCmd(t, "python3", "python")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hire.py"), []byte(adapter.PythonSDKTemplate), 0o644); err != nil {
		t.Fatal(err)
	}
	src := fmt.Sprintf(`from hire import task

@task("win", desc="windows only", when={"os": "windows"})
def win(a): pass

@task("linux", desc="linux only", when={"os": "linux"})
def linux(a): pass

@task("here", desc="current os", when={"os": %q})
def here(a): pass

import hire
if __name__ == "__main__":
    hire.dispatch()
`, goruntime.GOOS)
	path := filepath.Join(dir, "Taskfile.py")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := adapter.LoadExec(path, adapter.RuntimeSpec{Cmd: py}, "python")
	if err != nil {
		t.Fatalf("LoadExec: %v", err)
	}
	t.Cleanup(f.Close)
	checkSDKWhenTasks(t, f)
}

func TestExecNodeSDKWhenOS(t *testing.T) {
	node := probeCmd(t, "node")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hire.js"), []byte(adapter.NodeSDKTemplate), 0o644); err != nil {
		t.Fatal(err)
	}
	src := fmt.Sprintf(`const { task, dispatch } = require("./hire.js");
task("win", { desc: "windows only", when: { os: "windows" } }, function (a) {});
task("linux", { desc: "linux only", when: { os: "linux" } }, function (a) {});
task("here", { desc: "current os", when: { os: %q } }, function (a) {});
dispatch();
`, goruntime.GOOS)
	path := filepath.Join(dir, "Taskfile.js")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := adapter.LoadExec(path, adapter.RuntimeSpec{Cmd: node}, "node")
	if err != nil {
		t.Fatalf("LoadExec: %v", err)
	}
	t.Cleanup(f.Close)
	checkSDKWhenTasks(t, f)
}

// TestLoadLegacyNodeContract pins backward compatibility: a hand-written
// pure-contract Node taskfile (no hire.js) still loads after init changed
// to scaffold hire.js + Taskfile.js.
func TestLoadLegacyNodeContract(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	dir := t.TempDir()
	script := `const tasks = [{ name: "hello", desc: "hi", args: {} }];
const [, , verb, action] = process.argv;
if (verb !== "__hire__") { process.exit(1); }
if (action === "discover") { process.stdout.write(JSON.stringify({ tasks })); process.exit(0); }
process.exit(0);
`
	path := filepath.Join(dir, "Taskfile.js")
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := adapter.LoadExec(path, adapter.RuntimeSpec{Cmd: node}, "node")
	if err != nil {
		t.Fatalf("legacy pure-contract node taskfile must still load: %v", err)
	}
	t.Cleanup(f.Close)
	if _, ok := f.Tasks["hello"]; !ok {
		t.Errorf("legacy node tasks not discovered: %v", f.Order)
	}
}
