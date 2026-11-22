package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"hire/internal/adapter"
	"hire/internal/config"
)

// probePath returns the first command found on PATH, skipping the test
// when none is (toolchains are optional on dev machines).
func probePath(t *testing.T, names ...string) string {
	t.Helper()
	for _, n := range names {
		if _, err := exec.LookPath(n); err == nil {
			return n
		}
	}
	t.Skipf("none of %s found in PATH", strings.Join(names, ", "))
	return ""
}

// writeRuntimesConfig drops a config.lua declaring the given runtime
// command into the (already isolated) hire config dir.
func writeRuntimesConfig(t *testing.T, name, cmd string) {
	t.Helper()
	cfgDir, err := config.ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := "return {\n  runtimes = {\n    " + name + " = " + strconv.Quote(cmd) + ",\n  },\n}\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.lua"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

// init lua bootstraps itself: the generated file must go through the
// real load path — ls lists hello, hello runs.
func TestInitLuaScaffold(t *testing.T) {
	t.Chdir(t.TempDir())
	d, out, errOut := setupEnv(t, "")

	code := Run([]string{"init"}, d)
	if code != 0 {
		t.Fatalf("init: code=%d err=%q", code, errOut.String())
	}
	if _, err := os.Stat("Taskfile.lua"); err != nil {
		t.Fatalf("Taskfile.lua not created: %v", err)
	}

	out.Reset()
	code = Run([]string{"ls"}, d)
	if code != 0 || !strings.Contains(out.String(), "hello") {
		t.Fatalf("ls after init: code=%d out=%q", code, out.String())
	}

	out.Reset()
	code = Run([]string{"hello"}, d)
	if code != 0 || !strings.Contains(out.String(), "hello from hire") {
		t.Fatalf("hello after init: code=%d out=%q", code, out.String())
	}
}

// init python generates the SDK pair (hire.py + Taskfile.py) and
// bootstraps itself through the real load path — ls discovers via
// dispatch, hello runs; skipped when no python is on PATH.
func TestInitPythonScaffold(t *testing.T) {
	py := probePath(t, "python", "python3")
	t.Chdir(t.TempDir())
	d, out, errOut := setupEnv(t, "")
	writeRuntimesConfig(t, "python", py)

	code := Run([]string{"init", "--lang", "python"}, d)
	if code != 0 {
		t.Fatalf("init --lang python: code=%d err=%q", code, errOut.String())
	}
	for _, file := range []string{"hire.py", "Taskfile.py"} {
		if _, err := os.Stat(file); err != nil {
			t.Fatalf("%s not created: %v", file, err)
		}
	}
	if !strings.Contains(errOut.String(), "runtimes") {
		t.Errorf("init python should print the runtimes config hint: err=%q", errOut.String())
	}

	out.Reset()
	code = Run([]string{"ls"}, d)
	if code != 0 || !strings.Contains(out.String(), "hello") {
		t.Fatalf("ls after init python (discover): code=%d out=%q", code, out.String())
	}

	// Exec tasks inherit process stdio by design, so their stdout is not
	// visible through Deps here; exit 0 still proves the full round trip
	// (discover schema -> argparse -> run). The output text is asserted by
	// the integration suite against the real binary's pipe.
	for _, argv := range [][]string{
		{"hello"},
		{"hello", "--name", "pyworld"},
	} {
		out.Reset()
		code = Run(argv, d)
		if code != 0 {
			t.Fatalf("Run(%q) after init python: code=%d out=%q err=%q", argv, code, out.String(), errOut.String())
		}
	}
}

// TestPythonSDKArgForms drives the decorator SDK through the real load
// path: discover lists tasks in registration order (dicts keep
// insertion order on Python 3.7+), req/flag/num helpers round-trip
// through HIRE_ARGS (num crosses as a JSON number and reads back as a
// float), positionals land in a._, and the run verb rejects unknown
// tasks on stderr with exit 1.
func TestPythonSDKArgForms(t *testing.T) {
	py := probePath(t, "python", "python3")
	t.Chdir(t.TempDir())
	d, out, errOut := setupEnv(t, "")
	writeRuntimesConfig(t, "python", py)
	if err := os.WriteFile("hire.py", []byte(adapter.PythonSDKTemplate), 0o644); err != nil {
		t.Fatal(err)
	}
	const taskfile = `from hire import task, req, flag, num
import hire

@task("zebra", desc="first registered", args={"who": req("who to greet")})
def zebra(a):
    if a.who != "dk":
        raise SystemExit(f"who={a.who!r}")

@task("alpha", desc="flag plus positionals", args={"loud": flag("shout")})
def alpha(a):
    if a.loud is not True:
        raise SystemExit(f"loud={a.loud!r}")
    if a._ != ["pos", "args"]:
        raise SystemExit(f"_={a._!r}")

@task("probe", desc="num round trip", args={"port": num("listen port")})
def probe(a):
    if a.port != 8080.0 or not isinstance(a.port, float):
        raise SystemExit(f"port={a.port!r} ({type(a.port).__name__})")

if __name__ == "__main__":
    hire.dispatch()
`
	if err := os.WriteFile("Taskfile.py", []byte(taskfile), 0o644); err != nil {
		t.Fatal(err)
	}

	// Discover order is the registration order, not alphabetical.
	var buf bytes.Buffer
	discover := exec.Command(py, "Taskfile.py", "__hire__", "discover")
	discover.Stdout = &buf
	if err := discover.Run(); err != nil {
		t.Fatalf("__hire__ discover: %v\n%s", err, buf.String())
	}
	var dj struct {
		Tasks []struct {
			Name string `json:"name"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(buf.Bytes(), &dj); err != nil {
		t.Fatalf("discover JSON: %v\n%s", err, buf.String())
	}
	names := make([]string, 0, len(dj.Tasks))
	for _, task := range dj.Tasks {
		names = append(names, task.Name)
	}
	if want := []string{"zebra", "alpha", "probe"}; !slices.Equal(names, want) {
		t.Fatalf("discover order = %v, want %v", names, want)
	}

	// Each declared form through the full argparse -> HIRE_ARGS -> Args
	// path; a wrong shape exits nonzero, so exit 0 pins the contract.
	for _, argv := range [][]string{
		{"zebra", "--who", "dk"},
		{"alpha", "pos", "args", "--loud"},
		{"probe", "--port", "8080"},
	} {
		out.Reset()
		if code := Run(argv, d); code != 0 {
			t.Fatalf("Run(%q): code=%d out=%q err=%q", argv, code, out.String(), errOut.String())
		}
	}
	out.Reset()
	if code := Run([]string{"zebra"}, d); code == 0 || !strings.Contains(errOut.String(), "missing required flag --who") {
		t.Fatalf("zebra without --who: code=%d err=%q (want required-flag failure)", code, errOut.String())
	}

	// The run verb rejects unknown tasks: stderr message, exit 1 (hire
	// itself never sends one, but the verb must stay honest).
	buf.Reset()
	run := exec.Command(py, "Taskfile.py", "__hire__", "run", "nope")
	run.Stdout = &buf
	run.Stderr = &buf
	if err := run.Run(); err == nil {
		t.Fatal("__hire__ run nope should exit nonzero")
	} else if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
		t.Fatalf("__hire__ run nope: %v (want exit 1)", err)
	}
	if !strings.Contains(buf.String(), "unknown task") {
		t.Fatalf("run nope stderr: %q", buf.String())
	}
}

// TestContractPythonListings pins the byte-identity rule: the Python
// SDK listings in docs/contract.md must reprint the templates.go
// constants verbatim. CRLF from an autocrlf checkout is normalized —
// the committed bytes are LF, like the constants.
func TestContractPythonListings(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skip("go.mod not found; running outside the module")
		}
		dir = parent
	}
	doc, err := os.ReadFile(filepath.Join(dir, "docs", "contract.md"))
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.ReplaceAll(string(doc), "\r\n", "\n")
	for _, tmpl := range []struct{ name, body string }{
		{"PythonSDKTemplate", adapter.PythonSDKTemplate},
		{"PythonTemplate", adapter.PythonTemplate},
	} {
		if !strings.Contains(normalized, tmpl.body) {
			t.Errorf("docs/contract.md must reprint %s verbatim (starting %q)", tmpl.name, strings.SplitN(tmpl.body, "\n", 2)[0])
		}
	}
}

// All external-language templates generate without the toolchain
// present, and each print the runtimes config hint.
func TestInitGeneratesAllLangs(t *testing.T) {
	for lang, files := range map[string][]string{
		"python": {"hire.py", "Taskfile.py"},
		"node":   {"Taskfile.js"},
		"cpp":    {"hire.hpp", "Taskfile.cpp"},
	} {
		t.Run(lang, func(t *testing.T) {
			t.Chdir(t.TempDir())
			d, _, errOut := setupEnv(t, "")
			code := Run([]string{"init", "--lang", lang}, d)
			if code != 0 {
				t.Fatalf("init --lang %s: code=%d err=%q", lang, code, errOut.String())
			}
			for _, file := range files {
				if _, err := os.Stat(file); err != nil {
					t.Fatalf("%s not created: %v", file, err)
				}
			}
			if !strings.Contains(errOut.String(), "runtimes") {
				t.Errorf("init %s should print the runtimes config hint: err=%q", lang, errOut.String())
			}
		})
	}
}

// An existing target file is never overwritten.
func TestInitRefusesOverwrite(t *testing.T) {
	t.Chdir(t.TempDir())
	d, _, errOut := setupEnv(t, "")
	const existing = `keep = function() end`
	if err := os.WriteFile("Taskfile.lua", []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	code := Run([]string{"init"}, d)
	if code == 0 {
		t.Fatal("init over an existing Taskfile.lua should fail")
	}
	if !strings.Contains(errOut.String(), "already exists") {
		t.Errorf("refusal message: %q", errOut.String())
	}
	got, err := os.ReadFile("Taskfile.lua")
	if err != nil || string(got) != existing {
		t.Errorf("existing file must be untouched: %q (err=%v)", string(got), err)
	}
}

// A stale sibling does not block the missing file: hire.hpp present,
// Taskfile.cpp absent - init creates only the missing one.
func TestInitPartialExists(t *testing.T) {
	t.Chdir(t.TempDir())
	d, out, errOut := setupEnv(t, "")
	const staleHeader = "// old header"
	if err := os.WriteFile("hire.hpp", []byte(staleHeader), 0o644); err != nil {
		t.Fatal(err)
	}

	code := Run([]string{"init", "--lang", "cpp"}, d)
	if code != 0 {
		t.Fatalf("init --lang cpp with existing hire.hpp: code=%d err=%q", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "already exists; keeping it") {
		t.Errorf("kept note missing: %q", errOut.String())
	}
	if !strings.Contains(out.String(), "created Taskfile.cpp") {
		t.Errorf("created note missing: %q", out.String())
	}
	got, err := os.ReadFile("hire.hpp")
	if err != nil || string(got) != staleHeader {
		t.Errorf("existing hire.hpp must be untouched: %q (err=%v)", string(got), err)
	}
	if _, err := os.Stat("Taskfile.cpp"); err != nil {
		t.Fatalf("Taskfile.cpp not created: %v", err)
	}
}

// A .cpp taskfile in a Go module root gets the go-build poison warning.
func TestInitCppInGoModuleWarns(t *testing.T) {
	t.Chdir(t.TempDir())
	d, _, errOut := setupEnv(t, "")
	if err := os.WriteFile("go.mod", []byte("module demo\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code := Run([]string{"init", "--lang", "cpp"}, d)
	if code != 0 {
		t.Fatalf("init --lang cpp in go module: code=%d err=%q", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "go build") {
		t.Errorf("go-mod warning missing: %q", errOut.String())
	}
}

func TestInitUnknownLang(t *testing.T) {
	t.Chdir(t.TempDir())
	d, _, errOut := setupEnv(t, "")
	code := Run([]string{"init", "--lang", "ruby"}, d)
	if code != 2 || !strings.Contains(errOut.String(), "unknown --lang") {
		t.Errorf("unknown lang: code=%d err=%q", code, errOut.String())
	}
}

func TestInitUsageError(t *testing.T) {
	t.Chdir(t.TempDir())
	d, _, _ := setupEnv(t, "")
	for _, argv := range [][]string{
		{"init", "extra"},
		{"init", "--lang"},
	} {
		code := Run(argv, d)
		if code != 2 {
			t.Errorf("Run(%q): code=%d, want 2 (usage error)", argv, code)
		}
	}
}
