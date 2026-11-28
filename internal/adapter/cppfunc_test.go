package adapter_test

// Live-toolchain tests for the C++ function form: the full chain
// (parse -> wrapper -> g++ build -> discover -> run) with a real clang
// and g++, the dual-form fork with both file kinds in one chain, and
// the clear errors when the parser is missing. Legs skip where the
// toolchains are absent.

import (
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"hire/internal/adapter"
)

// funcFormTaskfile is the chain-test source: real standard headers (so
// the clang dump is realistic and the file filter must exclude header
// decls), every mapped parameter family, and a HIRE_WRITE round trip.
const funcFormTaskfile = `#include <cstdio>
#include <cstdlib>
#include <string>

// desc: say hello
void hello(std::string name = "hire", int times = 3) {
    const char* w = std::getenv("HIRE_WRITE");
    FILE* f = std::fopen(w ? w : "", "wb");
    if (f) { std::fprintf(f, "hello, %s x%d\n", name.c_str(), times); std::fclose(f); }
}

// desc: serve traffic
void serve(int port) {
    const char* w = std::getenv("HIRE_WRITE");
    FILE* f = std::fopen(w ? w : "", "wb");
    if (f) { std::fprintf(f, "port=%d\n", port); std::fclose(f); }
}

static void helper_one() {}

namespace { void helper_two() {} }
`

// TestCppFuncFormChain drives the whole function-form pipeline with the
// user's clang and g++: discover schema (types, required, default
// hints), a typed HIRE_ARGS round trip through the generated wrapper,
// the cache artifacts at the spec'd key paths, and the fast path - an
// unchanged source reloads from <key>.tasks.json with zero parse and
// zero exec, while a missing exe forces a full rebuild.
func TestCppFuncFormChain(t *testing.T) {
	clang := probeCmd(t, "clang++", "clang")
	probeCmd(t, "g++")
	base := spacedCacheDir(t)
	srcDir := filepath.Join(t.TempDir(), "c++ 源码 with spaces")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(srcDir, "Taskfile.cpp")
	src := []byte(funcFormTaskfile)
	if err := os.WriteFile(path, src, 0o644); err != nil {
		t.Fatal(err)
	}
	build := "g++ -std=c++20 -O2 -o {out} {src}"
	spec := adapter.RuntimeSpec{Build: build, Parser: clang}

	f, err := adapter.LoadExec(path, spec, "cpp")
	if err != nil {
		t.Fatalf("LoadExec (miss -> full pipeline): %v", err)
	}
	t.Cleanup(f.Close)
	if strings.Join(f.Order, ",") != "hello,serve" {
		t.Fatalf("Order = %v, want [hello serve]", f.Order)
	}
	hello := f.Tasks["hello"]
	if hello.Desc != "say hello" {
		t.Errorf("hello.Desc = %q", hello.Desc)
	}
	name := hello.Args["name"]
	if name.Type != "string" || name.Required || name.Desc != "default: hire" {
		t.Errorf("hello.Args[name] = %+v", name)
	}
	times := hello.Args["times"]
	if times.Type != "num" || times.Required || times.Desc != "default: 3" {
		t.Errorf("hello.Args[times] = %+v", times)
	}
	port := f.Tasks["serve"].Args["port"]
	if port.Type != "num" || !port.Required || port.Desc != "" {
		t.Errorf("serve.Args[port] = %+v, want a required num without desc", port)
	}

	// Typed round trip through the generated wrapper's conversion code.
	writeTo := filepath.Join(t.TempDir(), "cppfunc-out.txt")
	t.Setenv("HIRE_WRITE", writeTo)
	t.Setenv("HIRE_ARGS", `{"name":"e2e","times":2}`)
	if err := invokeTask(f, "hello"); err != nil {
		t.Fatalf("hello run: %v", err)
	}
	got, err := os.ReadFile(writeTo)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello, e2e x2\n" {
		t.Fatalf("hello round trip = %q, want %q", string(got), "hello, e2e x2\n")
	}
	t.Setenv("HIRE_ARGS", `{"port":8080}`)
	if err := invokeTask(f, "serve"); err != nil {
		t.Fatalf("serve run: %v", err)
	}
	got, err = os.ReadFile(writeTo)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "port=8080\n" {
		t.Fatalf("serve round trip = %q", string(got))
	}

	// Cache artifacts land beside the exe. The function-form key adds a
	// codegen salt on top of cacheKey(src, build), so locate the built
	// exe by glob rather than recomputing the raw key here.
	exes, err := filepath.Glob(filepath.Join(base, "hire", "cpp", "*.exe"))
	if err != nil || len(exes) != 1 {
		t.Fatalf("want one function-form exe, got %v (%v)", exes, err)
	}
	exe := exes[0]
	tasksJSON := strings.TrimSuffix(exe, ".exe") + ".tasks.json"
	if _, err := os.Stat(tasksJSON); err != nil {
		t.Fatalf("parse cache missing: %v", err)
	}
	if cached, err := os.ReadFile(tasksJSON); err != nil ||
		!strings.Contains(string(cached), `"desc":"default: hire"`) {
		t.Fatalf("cached discover payload missing the default hint: %v %s", err, cached)
	}

	// Fast path: with both artifacts present, a load needs neither clang
	// nor g++ - proven by stripping PATH and tampering the cached desc
	// with a marker only the cache itself could serve.
	const marker = "MARKED-CACHED-DESC"
	if err := os.WriteFile(tasksJSON,
		[]byte(`{"tasks":[{"name":"hello","desc":"`+marker+`","args":{"k":{"type":"num","required":true,"desc":""}}}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Strip PATH with an explicit save/restore pair: t.Setenv cannot be
	// undone before the test ends, and this leg needs PATH back for the
	// following runs.
	oldPath := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", oldPath) })
	os.Setenv("PATH", t.TempDir()) // no clang, no g++, no shell - nothing to run
	f2, err := adapter.LoadExec(path, spec, "cpp")
	if err != nil {
		t.Fatalf("LoadExec (cache hit, no toolchains): %v", err)
	}
	t.Cleanup(f2.Close)
	if d := f2.Tasks["hello"].Desc; d != marker {
		t.Fatalf("cache-hit desc = %q, want the cached marker %q", d, marker)
	}
	// The built exe still runs: discovery came from the cache, execution
	// from the binary. PATH is restored first - the load above already
	// proved zero parse/build/exec, and the run needs the mingw runtime
	// DLLs that live on PATH.
	os.Setenv("PATH", oldPath)
	t.Setenv("HIRE_ARGS", `{"k":1}`)
	if err := invokeTask(f2, "hello"); err != nil {
		t.Fatalf("hello after cache hit: %v", err)
	}

	// Rebuild: with the exe gone the slow path runs again and restores
	// the real discover payload (PATH is already restored above).
	if err := os.Remove(exe); err != nil {
		t.Fatal(err)
	}
	f3, err := adapter.LoadExec(path, spec, "cpp")
	if err != nil {
		t.Fatalf("LoadExec (exe miss -> rebuild): %v", err)
	}
	t.Cleanup(f3.Close)
	if d := f3.Tasks["hello"].Desc; d != "say hello" {
		t.Fatalf("rebuilt desc = %q, want the real desc restored", d)
	}
}

// rawContractTaskfile speaks the __hire__ contract itself (the legacy
// form) with a task named raw, so it must load through the raw path
// while the function form loads beside it in the same chain.
const rawContractTaskfile = `#include <cstdio>
#include <cstdlib>
#include <cstring>

// desc: write args to a file
int main(int argc, char** argv) {
    int i = 1;
    while (i < argc && strcmp(argv[i], "__hire__") != 0) i++;
    if (i + 1 >= argc) return 2;
    if (strcmp(argv[i + 1], "discover") == 0) {
        std::printf("{\"tasks\":[{\"name\":\"raw\",\"desc\":\"echo HIRE_ARGS to a file\",\"args\":{\"msg\":{\"type\":\"string\",\"required\":true,\"desc\":\"a message\"}}}]}");
        return 0;
    }
    if (i + 2 < argc && strcmp(argv[i + 1], "run") == 0 && strcmp(argv[i + 2], "raw") == 0) {
        const char* a = std::getenv("HIRE_ARGS");
        const char* w = std::getenv("HIRE_WRITE");
        FILE* f = std::fopen(w ? w : "", "w");
        if (!f) return 4;
        std::fputs(a ? a : "{}", f);
        std::fclose(f);
        return 0;
    }
    return 2;
}
`

// TestCppDualFormChain pins the fork: a contract-form .cpp (it contains
// __hire__) loads through the raw path, a function-form .cpp through
// the clang-parsed path, and both coexist in one chain.
func TestCppDualFormChain(t *testing.T) {
	clang := probeCmd(t, "clang++", "clang")
	probeCmd(t, "g++")
	spacedCacheDir(t)
	dir := filepath.Join(t.TempDir(), "two forms")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rawPath := filepath.Join(dir, "raw.cpp")
	if err := os.WriteFile(rawPath, []byte(rawContractTaskfile), 0o644); err != nil {
		t.Fatal(err)
	}
	funcPath := filepath.Join(dir, "funcs.cpp")
	if err := os.WriteFile(funcPath, []byte(funcFormTaskfile), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := adapter.RuntimeSpec{Build: "g++ -std=c++20 -O2 -o {out} {src}", Parser: clang}

	fRaw, err := adapter.LoadExec(rawPath, spec, "cpp")
	if err != nil {
		t.Fatalf("contract-form load: %v", err)
	}
	t.Cleanup(fRaw.Close)
	fFunc, err := adapter.LoadExec(funcPath, spec, "cpp")
	if err != nil {
		t.Fatalf("function-form load: %v", err)
	}
	t.Cleanup(fFunc.Close)
	chain := adapter.NewChain(fRaw, fFunc)
	if _, ok := chain.Tasks["raw"]; !ok {
		t.Fatal("contract-form task raw missing from the merged chain")
	}
	if _, ok := chain.Tasks["hello"]; !ok {
		t.Fatal("function-form task hello missing from the merged chain")
	}

	// One task through each side of the fork.
	writeTo := filepath.Join(t.TempDir(), "dualform.txt")
	t.Setenv("HIRE_WRITE", writeTo)
	t.Setenv("HIRE_ARGS", `{"msg":"from raw"}`)
	if err := invokeTask(fRaw, "raw"); err != nil {
		t.Fatalf("raw run: %v", err)
	}
	if got, _ := os.ReadFile(writeTo); string(got) != `{"msg":"from raw"}` {
		t.Fatalf("raw round trip = %q", string(got))
	}
	t.Setenv("HIRE_ARGS", `{"name":"dual","times":1}`)
	if err := invokeTask(fFunc, "hello"); err != nil {
		t.Fatalf("hello run: %v", err)
	}
	if got, _ := os.ReadFile(writeTo); string(got) != "hello, dual x1\n" {
		t.Fatalf("hello round trip = %q", string(got))
	}
}

// TestCppFuncFormForwardDeclAndExtern is the live reproduction for the
// review's Important 1 and 4: a forward declaration + definition of the
// same function folds into one task (not "declared twice"), the earlier
// declaration's comment survives, and an extern declaration with no body
// in this TU is not selected as a task.
func TestCppFuncFormForwardDeclAndExtern(t *testing.T) {
	clang := probeCmd(t, "clang++", "clang")
	probeCmd(t, "g++")
	spacedCacheDir(t)
	path := filepath.Join(t.TempDir(), "Taskfile.cpp")
	src := `// desc: forward-declared serve
void serve(int port);

extern void external_helper(int port);

// desc: definition below
void serve(int port) { (void)port; }
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := adapter.RuntimeSpec{Build: "g++ -std=c++20 -O2 -o {out} {src}", Parser: clang}
	f, err := adapter.LoadExec(path, spec, "cpp")
	if err != nil {
		t.Fatalf("LoadExec (forward decl + definition): %v", err)
	}
	t.Cleanup(f.Close)
	if strings.Join(f.Order, ",") != "serve" {
		t.Fatalf("Order = %v, want [serve] (extern excluded, redecl folded)", f.Order)
	}
	if got := f.Tasks["serve"].Desc; got != "forward-declared serve" {
		t.Errorf("serve.Desc = %q, want the earlier declaration's comment", got)
	}
}

// TestCppFuncFormValidationErrors drives the load-time rejections
// through the real clang parse; each error names the file, the function
// and, where relevant, the parameter.
func TestCppFuncFormValidationErrors(t *testing.T) {
	clang := probeCmd(t, "clang++", "clang")
	spacedCacheDir(t)
	spec := adapter.RuntimeSpec{Build: "g++ -std=c++20 -O2 -o {out} {src}", Parser: clang}
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			"reserved main",
			"int main() { return 0; }\n",
			[]string{`function "main" is reserved`},
		},
		{
			"overloads",
			"void dup(int x) {}\nvoid dup(double y) {}\n",
			[]string{`function "dup" declared twice`, "overloads"},
		},
		{
			"unsupported type",
			"// desc: nope\nvoid bad(unsigned int x) {}\n",
			[]string{`function "bad"`, `parameter "x"`, `unsupported type "unsigned int"`},
		},
		{
			"computed default",
			"void calc(int depth = 1 + 2) {}\n",
			[]string{`function "calc"`, `parameter "depth"`, "not a simple literal"},
		},
		{
			"unknown when os",
			"// desc: bad\n// when: not-an-os\nvoid bad() {}\n",
			[]string{`function "bad"`, "when", "not-an-os"},
		},
		{
			"all helpers, no tasks",
			"static void a() {}\nnamespace { void b() {} }\n",
			[]string{"no task functions found"},
		},
		{
			"syntax error keeps real file:line",
			"// desc: broken\nvoid broken() { int x = \"oops\"; }\n",
			[]string{"clang parse failed", "Taskfile.cpp:2:"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "Taskfile.cpp")
			if err := os.WriteFile(path, []byte(c.src), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := adapter.LoadExec(path, spec, "cpp")
			if err == nil {
				t.Fatalf("want a load error for %q", c.src)
			}
			for _, want := range append([]string{filepath.Base(path)}, c.want...) {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing %q", err.Error(), want)
				}
			}
		})
	}
}

// TestCppFuncFormMissingClang pins the ruling-7 error: a function-form
// file with no clang anywhere (PATH stripped) fails with a clear
// pointer at installing clang or using the legacy hpp form. Runs on
// any machine - the stripped PATH hides clang on CI too.
func TestCppFuncFormMissingClang(t *testing.T) {
	spacedCacheDir(t)
	path := filepath.Join(t.TempDir(), "Taskfile.cpp")
	if err := os.WriteFile(path, []byte("// desc: hi\nvoid hi() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := adapter.RuntimeSpec{Build: "g++ -std=c++20 -O2 -o {out} {src}"}
	t.Setenv("PATH", t.TempDir()) // no clang++, no clang
	_, err := adapter.LoadExec(path, spec, "cpp")
	if err == nil {
		t.Fatal("want the missing-clang error")
	}
	for _, want := range []string{filepath.Base(path), "clang", "hire.hpp", "parser"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

// TestCppParserConfiguredMissing: a parser key pointing at nothing is
// an error naming the configured command - not a probe and not a deep
// exec failure.
func TestCppParserConfiguredMissing(t *testing.T) {
	spacedCacheDir(t)
	path := filepath.Join(t.TempDir(), "Taskfile.cpp")
	if err := os.WriteFile(path, []byte("void hi() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := adapter.RuntimeSpec{Build: "g++ -std=c++20 -O2 -o {out} {src}", Parser: "definitely-not-a-clang-xyz"}
	_, err := adapter.LoadExec(path, spec, "cpp")
	if err == nil {
		t.Fatal("want an error for the configured-but-missing parser")
	}
	for _, want := range []string{filepath.Base(path), "definitely-not-a-clang-xyz", "not found"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

// TestCppFuncFormWhenOS drives the function form with // when: <os>
// comments: a task for another platform is filtered before the wrapper
// is generated (so it is absent from discover and unrunnable), while a
// matching one goes through the full parse -> build -> discover chain.
func TestCppFuncFormWhenOS(t *testing.T) {
	clang := probeCmd(t, "clang++", "clang")
	probeCmd(t, "g++")
	spacedCacheDir(t)
	path := filepath.Join(t.TempDir(), "Taskfile.cpp")
	src := fmt.Sprintf(`// desc: windows only
// when: windows
void winonly() {}

// desc: linux only
// when: linux
void linuxonly() {}

// desc: current platform
// when: %s
void here() {}
`, goruntime.GOOS)
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := adapter.RuntimeSpec{Build: "g++ -std=c++20 -O2 -o {out} {src}", Parser: clang}
	f, err := adapter.LoadExec(path, spec, "cpp")
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
	if got := f.Tasks["here"].Desc; got != "current platform" {
		t.Errorf("here.Desc = %q, want the doc comment's desc (comment parsing must skip the when line)", got)
	}
	wantCount := 1 // here on any other platform
	if wantWin || wantLinux {
		wantCount = 2
	}
	if len(f.Order) != wantCount {
		t.Errorf("Order = %v, want %d tasks", f.Order, wantCount)
	}
}
