package adapter_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"hire/internal/adapter"
	"hire/internal/config"
	"hire/internal/shell"
)

// spacedCacheDir points the compile cache at a directory whose path has
// spaces and non-ASCII characters — the paths build templates and
// child processes must survive.
func spacedCacheDir(t *testing.T) string {
	t.Helper()
	base := filepath.Join(t.TempDir(), "hire cache 缓存 dir")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CACHE_HOME", base)
	return base
}

// copyTemplate is a portable fake "compiler": it copies the prebuilt
// contract binary to {out} through the default shell.
func copyTemplate(prebuilt string) string {
	if runtime.GOOS == "windows" {
		return `Copy-Item '` + prebuilt + `' {out}`
	}
	return `cp '` + prebuilt + `' {out}`
}

// touchFailTemplate is a portable failing "compiler": it creates {out}
// and exits 1, mimicking a build interrupted after partial output.
func touchFailTemplate() string {
	if runtime.GOOS == "windows" {
		return `New-Item -ItemType File {out} | Out-Null; exit 1`
	}
	return `touch {out}; exit 1`
}

// cachedPath recomputes the spec'd cache location independently:
// <base>/hire/<rtName>/sha256(source + "\0" + build).exe
func cachedPath(t *testing.T, base, rtName string, src []byte, build string) string {
	t.Helper()
	h := sha256.New()
	h.Write(src)
	h.Write([]byte{0})
	h.Write([]byte(build))
	return filepath.Join(base, "hire", rtName, hex.EncodeToString(h.Sum(nil))+".exe")
}

func TestResolveRuntime(t *testing.T) {
	rts := map[string]adapter.RuntimeSpec{
		"python": {Cmd: "python3"},
		"node":   {Cmd: "node"},
		"cpp":    {Build: "g++ -std=c++20 -O2 -o {out} {src}"},
	}
	cases := []struct{ ext, name string }{
		{".py", "python"}, {"py", "python"}, {".PY", "python"},
		{".js", "node"}, {".mjs", "node"},
		{".cpp", "cpp"}, {".cc", "cpp"},
	}
	for _, c := range cases {
		spec, name, err := adapter.ResolveRuntime(c.ext, rts)
		if err != nil {
			t.Errorf("ResolveRuntime(%q): %v", c.ext, err)
			continue
		}
		if name != c.name {
			t.Errorf("ResolveRuntime(%q) name = %q, want %q", c.ext, name, c.name)
		}
		if !reflect.DeepEqual(spec, rts[c.name]) {
			t.Errorf("ResolveRuntime(%q) spec = %+v, want %+v", c.ext, spec, rts[c.name])
		}
	}

	// Unknown extension: the error lists registered runtimes and known
	// extensions so the user can fix either side.
	_, _, err := adapter.ResolveRuntime(".rb", rts)
	if err == nil {
		t.Fatal("expected error for unknown extension")
	}
	for _, want := range []string{".rb", "python", "node", "cpp", ".py", ".mjs", ".cc"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("unknown-ext error %q missing %q", err.Error(), want)
		}
	}

	// Builtin name missing from the registry: the preset default fills
	// in, so python works even with an empty entry set.
	noPython := map[string]adapter.RuntimeSpec{"node": {Cmd: "node"}, "cpp": {Build: "x"}}
	dspec, dname, derr := adapter.ResolveRuntime(".py", noPython)
	if derr != nil || dname != "python" || (dspec.Cmd != "python3" && dspec.Cmd != "python") {
		t.Fatalf("ResolveRuntime(.py) with no python entry = %q, %q, %v; want the preset default", dspec.Cmd, dname, derr)
	}
	// Unknown extension: the error lists registered runtimes and known
	// extensions so the user can fix either side.
	_, _, err = adapter.ResolveRuntime(".rb", noPython)
	if err == nil {
		t.Fatal("expected error for unknown extension")
	}
	for _, want := range []string{".rb", "node", "cpp", ".py", ".mjs", ".ts", ".cc"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("unknown-ext error %q missing %q", err.Error(), want)
		}
	}
}

// The preset languages resolve with an empty registry (out of the box).
func TestResolveRuntimeDefaults(t *testing.T) {
	for _, c := range []struct{ ext, name string }{
		{".py", "python"}, {".js", "node"}, {".mjs", "node"},
		{".ts", "ts"}, {".cpp", "cpp"}, {".cc", "cpp"},
	} {
		spec, name, err := adapter.ResolveRuntime(c.ext, nil)
		if err != nil || name != c.name {
			t.Errorf("ResolveRuntime(%q) with empty rts = %q, %q, %v; want %q", c.ext, spec.Cmd, name, err, c.name)
		}
	}
	_, name, err := adapter.ResolveRuntime(".ts", nil)
	if err != nil || name != "ts" {
		t.Fatalf("ts default: %v", err)
	}
}

func TestRuntimesConfig(t *testing.T) {
	dir := t.TempDir()
	content := `return {
  runtimes = {
    python = "python3",
    node   = "node",
    cpp    = { build = "g++ -std=c++20 -O2 -o {out} {src}" },
  },
}`
	if err := os.WriteFile(filepath.Join(dir, "config.lua"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	rts, err := config.Runtimes(dir)
	if err != nil {
		t.Fatalf("Runtimes: %v", err)
	}
	if !reflect.DeepEqual(rts["python"], adapter.RuntimeSpec{Cmd: "python3"}) {
		t.Errorf(`rts["python"] = %+v`, rts["python"])
	}
	if !reflect.DeepEqual(rts["node"], adapter.RuntimeSpec{Cmd: "node"}) {
		t.Errorf(`rts["node"] = %+v`, rts["node"])
	}
	if !reflect.DeepEqual(rts["cpp"], adapter.RuntimeSpec{Build: "g++ -std=c++20 -O2 -o {out} {src}"}) {
		t.Errorf(`rts["cpp"] = %+v`, rts["cpp"])
	}
	if len(rts) != 3 {
		t.Errorf("len(rts) = %d, want 3", len(rts))
	}
}

func TestRuntimesConfigMissing(t *testing.T) {
	rts, err := config.Runtimes(t.TempDir())
	if err != nil {
		t.Fatalf("missing config.lua should not error: %v", err)
	}
	if len(rts) != 0 {
		t.Errorf("rts = %v, want empty", rts)
	}
}

// Custom-ext runtimes: cmd form with a declared extension, string or
// array, normalized to lowercase-with-dot.
func TestRuntimesConfigExt(t *testing.T) {
	dir := t.TempDir()
	content := `return {
  runtimes = {
    ts  = { cmd = "node", ext = ".TS" },
    deno = { cmd = "deno", ext = { ".mts", "ts2" } },
  },
}`
	if err := os.WriteFile(filepath.Join(dir, "config.lua"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	rts, err := config.Runtimes(dir)
	if err != nil {
		t.Fatalf("Runtimes: %v", err)
	}
	if !reflect.DeepEqual(rts["ts"], adapter.RuntimeSpec{Cmd: "node", Ext: []string{".ts"}}) {
		t.Errorf(`rts["ts"] = %+v, want cmd node + normalized .ts`, rts["ts"])
	}
	if !reflect.DeepEqual(rts["deno"], adapter.RuntimeSpec{Cmd: "deno", Ext: []string{".mts", ".ts2"}}) {
		t.Errorf(`rts["deno"] = %+v, want both extensions normalized`, rts["deno"])
	}
}

// ResolveRuntime honors declared extensions beyond the builtin map.
func TestResolveCustomExt(t *testing.T) {
	rts := map[string]adapter.RuntimeSpec{
		"node": {Cmd: "node"},
		"ts":   {Cmd: "node", Ext: []string{".ts"}},
	}
	spec, name, err := adapter.ResolveRuntime(".ts", rts)
	if err != nil || name != "ts" || spec.Cmd != "node" {
		t.Fatalf("ResolveRuntime(.ts) = %q, %q, %v; want ts/node", spec.Cmd, name, err)
	}
	// Builtin extensions still resolve through the builtin map first.
	spec, name, err = adapter.ResolveRuntime(".js", rts)
	if err != nil || name != "node" {
		t.Fatalf("ResolveRuntime(.js) = %q, %q, %v; want node", spec.Cmd, name, err)
	}
	// Unclaimed extension errors and points at the ext declaration.
	_, _, err = adapter.ResolveRuntime(".tsx", rts)
	if err == nil || !strings.Contains(err.Error(), "ext") {
		t.Fatalf("ResolveRuntime(.tsx) err = %v, want an ext hint", err)
	}
}

func TestRuntimesConfigInvalid(t *testing.T) {
	cases := []string{
		`return { runtimes = { python = 3 } }`,                    // bad value form
		`return { runtimes = { python = "" } }`,                  // empty command
		`return { runtimes = { cpp = {} } }`,                     // neither cmd nor build
		`return { runtimes = { cpp = { ext = ".cc" } } }`,        // ext without cmd or build
		`return { runtimes = { ts = { cmd = "x", ext = 5 } } }`,   // ext not a string
		`return { runtimes = { ts = { cmd = "x", ext = {5} } } }`, // ext array entry not a string
		`return { runtimes = "x" }`,                              // runtimes not a table
		`this is not lua!!!`,                                     // syntax error
	}
	for _, c := range cases {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.lua"), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := config.Runtimes(dir); err == nil {
			t.Errorf("Runtimes(%q): expected error", c)
		}
	}
}

// The compiled path end to end with the prebuilt fake as the "compiler
// output": miss -> build, hit -> cached exe runs without rebuilding, a
// changed build string -> new key, and a failing build -> error carrying
// the full builder output. All paths contain spaces and non-ASCII.
func TestExecCompiledCache(t *testing.T) {
	if _, err := shell.Resolve("", nil); err != nil {
		t.Skipf("no default shell for build templates: %v", err)
	}
	prebuilt := fakeInterp()
	base := spacedCacheDir(t)
	srcDir := filepath.Join(t.TempDir(), "src 目录 with spaces")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(srcDir, "Taskfile.fake")
	src := []byte("int main() { return 0; }")
	if err := os.WriteFile(path, src, 0o644); err != nil {
		t.Fatal(err)
	}
	tmpl := copyTemplate(prebuilt)
	spec := adapter.RuntimeSpec{Build: tmpl}

	// Cache miss: the build runs and the exe lands at the spec'd hash
	// path, then serves the contract.
	f, err := adapter.LoadExec(path, spec, "fake")
	if err != nil {
		t.Fatalf("LoadExec (miss -> build): %v", err)
	}
	t.Cleanup(f.Close)
	exe := cachedPath(t, base, "fake", src, tmpl)
	if _, err := os.Stat(exe); err != nil {
		t.Fatalf("built exe missing at %s: %v", exe, err)
	}
	runEchoBoom(t, f)

	// Cache hit: with the prebuilt binary renamed away a rebuild would
	// fail — success proves the cached exe ran instead.
	gone := prebuilt + ".gone"
	if err := os.Rename(prebuilt, gone); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Rename(gone, prebuilt) })
	f2, err := adapter.LoadExec(path, spec, "fake")
	if err != nil {
		t.Fatalf("LoadExec (hit): %v", err)
	}
	t.Cleanup(f2.Close)
	runEchoBoom(t, f2)

	// Changing the build string invalidates: new key, new exe path, and
	// the build runs again (its source is the old cached exe).
	tmpl2 := copyTemplate(exe)
	f3, err := adapter.LoadExec(path, adapter.RuntimeSpec{Build: tmpl2}, "fake")
	if err != nil {
		t.Fatalf("LoadExec (new build string): %v", err)
	}
	t.Cleanup(f3.Close)
	exe2 := cachedPath(t, base, "fake", src, tmpl2)
	if exe2 == exe {
		t.Fatal("changing the build string should move the cache path")
	}
	if _, err := os.Stat(exe2); err != nil {
		t.Fatalf("rebuilt exe missing at %s: %v", exe2, err)
	}
	runEchoBoom(t, f3)

	// Build failure: the error names the taskfile and carries the full
	// builder output.
	failSpec := adapter.RuntimeSpec{Build: copyTemplate(filepath.Join(base, "no such file.exe"))}
	_, err = adapter.LoadExec(path, failSpec, "fake")
	if err == nil {
		t.Fatal("expected build failure error")
	}
	for _, want := range []string{path, "build failed", "no such file.exe"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("build failure error %q missing %q", err.Error(), want)
		}
	}
}

// I1 regression: a build that touches {out} and then fails must not
// leave the partial artifact at the cache path — the next LoadExec
// would stat it as a hit and silently execute the corrupt binary.
func TestBuildFailureRemovesPartialOutput(t *testing.T) {
	if _, err := shell.Resolve("", nil); err != nil {
		t.Skipf("no default shell for build templates: %v", err)
	}
	base := spacedCacheDir(t)
	srcDir := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(srcDir, "Taskfile.fake")
	src := []byte("int main() { return 0; }")
	if err := os.WriteFile(path, src, 0o644); err != nil {
		t.Fatal(err)
	}
	spec := adapter.RuntimeSpec{Build: touchFailTemplate()}

	_, err := adapter.LoadExec(path, spec, "fake")
	if err == nil || !strings.Contains(err.Error(), "build failed") {
		t.Fatalf("LoadExec (failing build): err = %v, want build failure", err)
	}
	exe := cachedPath(t, base, "fake", src, spec.Build)
	if _, serr := os.Stat(exe); serr == nil {
		t.Fatalf("partial artifact left at %s: the next load would execute it", exe)
	}

	// The next load rebuilds rather than hitting a poisoned cache: the
	// rebuild fails the same way, while a poisoned hit would instead try
	// to run the 0-byte artifact (a discover/exec error, not "build failed").
	_, err = adapter.LoadExec(path, spec, "fake")
	if err == nil || !strings.Contains(err.Error(), "build failed") {
		t.Errorf("second LoadExec: err = %v, want a rebuild (build failed again)", err)
	}
}

const cppTaskfile = `#include <cstdio>
#include <cstdlib>
#include <cstring>

int main(int argc, char** argv) {
    int i = 1;
    while (i < argc && strcmp(argv[i], "__hire__") != 0) i++;
    if (i >= argc) return 2;
    const char* verb = (i + 1 < argc) ? argv[i + 1] : "";
    const char* task = (i + 2 < argc) ? argv[i + 2] : "";
    if (strcmp(verb, "discover") == 0) {
        std::printf("{\"tasks\":[{\"name\":\"hello\",\"desc\":\"echo HIRE_ARGS to a file\"}]}\n");
        return 0;
    }
    if (strcmp(verb, "run") == 0 && strcmp(task, "hello") == 0) {
        const char* w = std::getenv("HIRE_WRITE");
        const char* a = std::getenv("HIRE_ARGS");
        if (!w) return 4;
        FILE* f = std::fopen(w, "w");
        if (!f) return 4;
        std::fputs(a ? a : "{}", f);
        std::fclose(f);
        return 0;
    }
    std::fprintf(stderr, "unknown verb or task\n");
    return 2;
}
`

// A real g++ round trip through the exact build template from the spec,
// with spaces and non-ASCII in both the source and cache paths. Skipped
// when g++ is missing.
func TestExecCppGxx(t *testing.T) {
	probeCmd(t, "g++")
	base := spacedCacheDir(t)
	srcDir := filepath.Join(t.TempDir(), "c++ 源码 with spaces")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(srcDir, "Taskfile.cpp")
	src := []byte(cppTaskfile)
	if err := os.WriteFile(path, src, 0o644); err != nil {
		t.Fatal(err)
	}
	tmpl := "g++ -std=c++20 -O2 -o {out} {src}"
	f, err := adapter.LoadExec(path, adapter.RuntimeSpec{Build: tmpl}, "cpp")
	if err != nil {
		t.Fatalf("LoadExec (real g++ build): %v", err)
	}
	t.Cleanup(f.Close)
	hello := f.Tasks["hello"]
	if hello == nil {
		t.Fatalf("hello missing; tasks = %v", f.Order)
	}
	if hello.Desc != "echo HIRE_ARGS to a file" {
		t.Errorf("hello.Desc = %q", hello.Desc)
	}
	exe := cachedPath(t, base, "cpp", src, tmpl)
	if _, err := os.Stat(exe); err != nil {
		t.Errorf("g++ output missing at %s: %v", exe, err)
	}
	writeTo := filepath.Join(t.TempDir(), "cpp-args.txt")
	argsJSON := `{"who":"world"}`
	t.Setenv("HIRE_ARGS", argsJSON)
	t.Setenv("HIRE_WRITE", writeTo)
	if err := invokeTask(f, "hello"); err != nil {
		t.Fatalf("hello run: %v", err)
	}
	got, err := os.ReadFile(writeTo)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != argsJSON {
		t.Errorf("cpp HIRE_ARGS round trip: got %q want %q", string(got), argsJSON)
	}
}
