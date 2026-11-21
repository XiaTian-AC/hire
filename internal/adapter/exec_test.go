package adapter_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"hire/internal/adapter"
)

// fakeInterp builds the __hire__ contract test double once per test run,
// so contract tests run even on machines without python/node/g++.
var fakeInterp = sync.OnceValue(func() string {
	dir, err := os.MkdirTemp("", "hire-fakeinterp-")
	if err != nil {
		panic(err)
	}
	exe := filepath.Join(dir, "fakeinterp")
	if runtime.GOOS == "windows" {
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
	err = invokeTask(f, "boom")
	if err == nil {
		t.Fatal("boom should fail")
	}
	if !strings.Contains(err.Error(), `task "boom" failed (exit 3)`) {
		t.Errorf("boom error = %q, want it to contain %q", err.Error(), `task "boom" failed (exit 3)`)
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
