package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

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

// init python generates a file that discovers and runs through the
// __hire__ contract; skipped when no python is on PATH.
func TestInitPythonScaffold(t *testing.T) {
	py := probePath(t, "python", "python3")
	t.Chdir(t.TempDir())
	d, out, errOut := setupEnv(t, "")
	writeRuntimesConfig(t, "python", py)

	code := Run([]string{"init", "--lang", "python"}, d)
	if code != 0 {
		t.Fatalf("init --lang python: code=%d err=%q", code, errOut.String())
	}
	if _, err := os.Stat("Taskfile.py"); err != nil {
		t.Fatalf("Taskfile.py not created: %v", err)
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

// All external-language templates generate without the toolchain
// present, and each print the runtimes config hint.
func TestInitGeneratesAllLangs(t *testing.T) {
	for lang, files := range map[string][]string{
		"python": {"Taskfile.py"},
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
