//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"hire/internal/adapter"
)

func projectRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod")
		}
		dir = parent
	}
}

func buildBinary(t *testing.T) string {
	t.Helper()
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	dir, err := os.MkdirTemp("", "hire-integration-")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "hire.test"+suffix)
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = projectRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func runBin(t *testing.T, bin string, args ...string) (string, int) {
	t.Helper()
	return runBinOpt(t, bin, "", nil, args...)
}

// runBinOpt is runBin with an optional working directory and extra
// environment entries.
func runBinOpt(t *testing.T, bin, dir string, env []string, args ...string) (string, int) {
	t.Helper()
	var out bytes.Buffer
	cmd := exec.Command(bin, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("run %v: %v", args, err)
		}
	}
	return out.String(), code
}

// langCmd returns the first command found on PATH, skipping only the
// calling leg when none is (toolchains are optional on dev machines;
// CI installs python/node, g++ legs stay skipped where absent).
func langCmd(t *testing.T, names ...string) string {
	t.Helper()
	for _, n := range names {
		if _, err := exec.LookPath(n); err == nil {
			return n
		}
	}
	t.Skipf("none of %s found in PATH; skipping leg", strings.Join(names, ", "))
	return ""
}

func writeTaskfile(dir, name, src string) string {
	path := filepath.Join(dir, name)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(path, []byte(src), 0o644)
	return path
}

func TestIntegrationFlow(t *testing.T) {
	bin := buildBinary(t)
	cfgDir, _ := os.MkdirTemp("", "hire-cfg-")
	dataDir, _ := os.MkdirTemp("", "hire-data-")
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	t.Setenv("XDG_DATA_HOME", dataDir)

	taskfile := `
echo = function() x.echo("hello from echo task") end

quick = function()
  x.sh("echo line1")
  x.sh("echo line2")
  x.sh("echo line3")
end

longrun = function()
  x.sh("echo will-sleep")
  if x.os() == "windows" then
    x.sh("Start-Sleep -Seconds 30")
  else
    x.sh("sleep 30")
  end
end

tagged = task {
  desc = "echo the tag",
  args = { tag = req "a tag" },
  run = function(args) x.echo("tag=" .. args.tag) end,
}
`
	tfDir := filepath.Join(cfgDir, "hire")
	os.MkdirAll(tfDir, 0o755)
	os.WriteFile(filepath.Join(tfDir, "Taskfile.lua"), []byte(taskfile), 0o644)

	out, code := runBin(t, bin, "ls")
	if code != 0 || !strings.Contains(out, "echo") || !strings.Contains(out, "tagged") {
		t.Fatalf("ls: code=%d out=%q", code, out)
	}

	out, code = runBin(t, bin, "echo")
	if code != 0 || !strings.Contains(out, "hello from echo task") {
		t.Fatalf("echo: code=%d out=%q", code, out)
	}

	out, code = runBin(t, bin, "tagged", "--tag", "v1.0")
	if code != 0 || !strings.Contains(out, "tag=v1.0") {
		t.Fatalf("tagged: code=%d out=%q", code, out)
	}

	out, code = runBin(t, bin, "bstart", "quick")
	if code != 0 || !strings.Contains(out, "started quick#1") {
		t.Fatalf("bstart quick: code=%d out=%q", code, out)
	}

	out, _ = runBin(t, bin, "ls-running")
	if !strings.Contains(out, "quick#1") {
		t.Fatalf("ls-running: %q", out)
	}

	deadline := time.Now().Add(30 * time.Second)
	finished := false
	for time.Now().Before(deadline) {
		out, _ = runBin(t, bin, "ls-running")
		if strings.Contains(out, "exited") {
			finished = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !finished {
		t.Fatal("quick task did not finish within 30s")
	}

	out, code = runBin(t, bin, "watch", "quick#1")
	if code != 0 {
		t.Fatalf("watch: code=%d out=%q", code, out)
	}
	if !strings.Contains(out, "line1") || !strings.Contains(out, "line3") || !strings.Contains(out, "exited") {
		t.Fatalf("watch output missing content: %q", out)
	}

	out, _ = runBin(t, bin, "bstart", "longrun")
	if !strings.Contains(out, "started longrun#1") {
		t.Fatalf("bstart longrun: %q", out)
	}
	time.Sleep(1 * time.Second)

	out, code = runBin(t, bin, "stop", "longrun#1")
	if code != 0 || !strings.Contains(out, "stopped") {
		t.Fatalf("stop: code=%d out=%q", code, out)
	}

	out, _ = runBin(t, bin, "ls-running")
	if !strings.Contains(out, "longrun#1") {
		t.Fatalf("ls-running after stop: %q", out)
	}

	out, _ = runBin(t, bin, "bstart", "quick")
	if !strings.Contains(out, "started quick#2") {
		t.Fatalf("bstart quick #2: %q", out)
	}
	logsDir := filepath.Join(dataDir, "hire", "logs")
	entries, _ := os.ReadDir(logsDir)
	for _, f := range entries {
		if strings.Contains(f.Name(), "quick#1") {
			t.Errorf("old quick#1 log should be rotated, found: %s", f.Name())
		}
	}
}

// TestIntegrationBareListsTasks: bare `hire` is a discovery command —
// it lists tasks and one usage hint, exit 0 (spec §10).
func TestIntegrationBareListsTasks(t *testing.T) {
	bin := buildBinary(t)
	cfgDir, _ := os.MkdirTemp("", "hire-cfg-")
	dataDir, _ := os.MkdirTemp("", "hire-data-")
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	t.Setenv("XDG_DATA_HOME", dataDir)
	tfDir := filepath.Join(cfgDir, "hire")
	os.MkdirAll(tfDir, 0o755)
	os.WriteFile(filepath.Join(tfDir, "Taskfile.lua"), []byte(`alpha = function() x.echo("a") end`), 0o644)

	out, code := runBin(t, bin)
	if code != 0 {
		t.Fatalf("bare hire: code=%d out=%q", code, out)
	}
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "usage") {
		t.Fatalf("bare hire should list tasks + usage hint: %q", out)
	}
}

// TestIntegrationExportPipe: with stdout piped (non-TTY), an
// instance=0 task's collected commands ARE the stdout payload —
// exactly what `hire task | eval` / `iex` consumes, with no hint.
func TestIntegrationExportPipe(t *testing.T) {
	bin := buildBinary(t)
	cfgDir, _ := os.MkdirTemp("", "hire-cfg-")
	dataDir, _ := os.MkdirTemp("", "hire-data-")
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	t.Setenv("XDG_DATA_HOME", dataDir)
	tfDir := filepath.Join(cfgDir, "hire")
	os.MkdirAll(tfDir, 0o755)
	os.WriteFile(filepath.Join(tfDir, "Taskfile.lua"), []byte(`
exports = function()
  x.sh("export FOO=bar", { instance = 0 })
  x.sh("cd /tmp", { instance = 0 })
end
`), 0o644)

	out, code := runBin(t, bin, "exports")
	if code != 0 {
		t.Fatalf("exports: code=%d out=%q", code, out)
	}
	if out != "export FOO=bar\ncd /tmp\n" {
		t.Fatalf("piped export output = %q, want the two collected commands only", out)
	}
}

// TestIntegrationCleanOlderThanForms pins audit #2 end to end: both
// --older-than forms parse, and the space form never falls through to
// clean-all — a just-ended instance survives `--older-than 1h`.
func TestIntegrationCleanOlderThanForms(t *testing.T) {
	bin := buildBinary(t)
	cfgDir, _ := os.MkdirTemp("", "hire-cfg-")
	dataDir, _ := os.MkdirTemp("", "hire-data-")
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	t.Setenv("XDG_DATA_HOME", dataDir)
	tfDir := filepath.Join(cfgDir, "hire")
	os.MkdirAll(tfDir, 0o755)
	os.WriteFile(filepath.Join(tfDir, "Taskfile.lua"), []byte(`quick = { "echo done" }`), 0o644)

	if out, _ := runBin(t, bin, "bstart", "quick"); !strings.Contains(out, "started quick#1") {
		t.Fatalf("bstart quick: %q", out)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		out, _ := runBin(t, bin, "ls-running")
		if strings.Contains(out, "exited") {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Space form: the instance ended seconds ago — not older than 1h.
	out, code := runBin(t, bin, "clean", "--older-than", "1h")
	if code != 0 || !strings.Contains(out, "cleaned 0 instance(s) older than 1h") {
		t.Fatalf("clean --older-than 1h: code=%d out=%q", code, out)
	}
	out, _ = runBin(t, bin, "ls-running")
	if !strings.Contains(out, "quick#1") {
		t.Fatalf("space-form clean must not delete a just-ended instance: %q", out)
	}

	// Equals form with 0 = clean-all for ended instances.
	out, code = runBin(t, bin, "clean", "--older-than=0")
	if code != 0 || !strings.Contains(out, "cleaned 1 instance(s)") {
		t.Fatalf("clean --older-than=0: code=%d out=%q", code, out)
	}
	out, _ = runBin(t, bin, "ls-running")
	if strings.Contains(out, "quick#1") {
		t.Fatalf("cleaned instance must be gone: %q", out)
	}
}

// TestIntegrationBstartTaskfileHandoff pins audit #1: a task that
// exists only in a --taskfile file must bstart and run THAT file's
// version in the background, not whatever the global lookup finds.
func TestIntegrationBstartTaskfileHandoff(t *testing.T) {
	bin := buildBinary(t)
	cfgDir, _ := os.MkdirTemp("", "hire-cfg-")
	dataDir, _ := os.MkdirTemp("", "hire-data-")
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	t.Setenv("XDG_DATA_HOME", dataDir)
	tfDir := filepath.Join(cfgDir, "hire")
	os.MkdirAll(tfDir, 0o755)
	os.WriteFile(filepath.Join(tfDir, "Taskfile.lua"), []byte(`other = function() x.echo("global-other") end`), 0o644)

	work := t.TempDir()
	local := filepath.Join(work, "My.lua")
	os.WriteFile(local, []byte(`special = function() x.echo("from-special-taskfile") end`), 0o644)

	out, code := runBinOpt(t, bin, work, nil, "bstart", "special", "--taskfile", local)
	if code != 0 || !strings.Contains(out, "started special#1") {
		t.Fatalf("bstart --taskfile: code=%d out=%q", code, out)
	}

	// "special" exists only in the --taskfile, so a plain global
	// lookup (from a directory without taskfiles) must not list it
	out, _ = runBin(t, bin, "ls")
	if strings.Contains(out, "special") {
		t.Errorf("global ls should not list special: %q", out)
	}

	logPath := filepath.Join(dataDir, "hire", "logs", "special#1.log")
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(logPath); err == nil && strings.Contains(string(b), "from-special-taskfile") {
			return // the background child ran the --taskfile version
		}
		time.Sleep(250 * time.Millisecond)
	}
	b, _ := os.ReadFile(logPath)
	t.Fatalf("background child did not run the --taskfile version; log:\n%s", b)
}

// TestIntegrationRunFilesEnvMissingFile: _run with HIRE_FILES pointing
// at a vanished taskfile must report the path instead of silently
// re-probing (which could execute an unrelated global file).
func TestIntegrationRunFilesEnvMissingFile(t *testing.T) {
	bin := buildBinary(t)
	cfgDir, _ := os.MkdirTemp("", "hire-cfg-")
	dataDir, _ := os.MkdirTemp("", "hire-data-")
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	t.Setenv("XDG_DATA_HOME", dataDir)
	ghost := filepath.Join(t.TempDir(), "gone.lua")
	j, err := json.Marshal([]string{ghost})
	if err != nil {
		t.Fatal(err)
	}
	out, code := runBinOpt(t, bin, "", []string{"HIRE_FILES=" + string(j)}, "_run", "ghost", "--id", "ghost#1", "--")
	if code != 1 {
		t.Fatalf("_run with missing HIRE_FILES file: code=%d out=%q", code, out)
	}
	if !strings.Contains(out, "no Taskfile at") || !strings.Contains(out, ghost) {
		t.Fatalf("_run missing-file output should name the vanished path: %q", out)
	}
}

// TestIntegrationTriLingual: `hire init` scaffolds each external
// language and the generated file runs end to end through the
// __hire__ contract — discover, argparse against the discovered
// schema, HIRE_ARGS round trip, and (cpp) the compile cache.
func TestIntegrationTriLingual(t *testing.T) {
	bin := buildBinary(t)
	cfgDir, _ := os.MkdirTemp("", "hire-cfg-")
	dataDir, _ := os.MkdirTemp("", "hire-data-")
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	t.Setenv("XDG_DATA_HOME", dataDir)
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // cpp compile cache stays test-local

	for lang, candidates := range map[string][]string{
		"python": {"python", "python3"},
		"node":   {"node"},
		"cpp":    {"g++"},
	} {
		t.Run(lang, func(t *testing.T) {
			cmdOnPath := langCmd(t, candidates...)
			work := t.TempDir()
			cfg := map[string]string{
				"python": "return { runtimes = { python = " + luaString(cmdOnPath) + " } }",
				"node":   "return { runtimes = { node = \"node\" } }",
				"cpp":    "return { runtimes = { cpp = { build = \"g++ -std=c++20 -O2 -o {out} {src}\" } } }",
			}[lang]
			writeTaskfile(filepath.Join(cfgDir, "hire"), "config.lua", cfg)

			out, code := runBinOpt(t, bin, work, nil, "init", "--lang", lang)
			if code != 0 {
				t.Fatalf("init --lang %s: code=%d out=%q", lang, code, out)
			}
			out, code = runBinOpt(t, bin, work, nil, "ls")
			if code != 0 || !strings.Contains(out, "hello") {
				t.Fatalf("ls after init %s (discover): code=%d out=%q", lang, code, out)
			}
			out, code = runBinOpt(t, bin, work, nil, "hello")
			if code != 0 || !strings.Contains(out, "hello, hire") {
				t.Fatalf("hello after init %s: code=%d out=%q", lang, code, out)
			}
			out, code = runBinOpt(t, bin, work, nil, "hello", "--name", "e2e")
			if code != 0 || !strings.Contains(out, "hello, e2e") {
				t.Fatalf("hello --name e2e after init %s: code=%d out=%q", lang, code, out)
			}
		})
	}
}

func luaString(s string) string {
	return `"` + s + `"`
}

// TestIntegrationThreeFileMerge: TOML+Lua+py in one directory all
// load; the same-name task resolves to the last-loaded file (py) with
// a redefined warning on stderr.
func TestIntegrationThreeFileMerge(t *testing.T) {
	bin := buildBinary(t)
	cfgDir, _ := os.MkdirTemp("", "hire-cfg-")
	dataDir, _ := os.MkdirTemp("", "hire-data-")
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	t.Setenv("XDG_DATA_HOME", dataDir)
	py := langCmd(t, "python", "python3")

	work := t.TempDir()
	writeTaskfile(work, "Taskfile.toml", `
[tasks.hello]
cmd = "echo from-toml"

[tasks.tonly]
cmd = "echo only-toml"
`)
	writeTaskfile(work, "Taskfile.lua", `
hello = function() x.echo("from-lua") end
luonly = function() x.echo("only-lua") end
`)
	writeTaskfile(work, "Taskfile.py", `
import json, sys

TASKS = [
    {"name": "hello", "desc": "hello from the py file"},
    {"name": "pyonly", "desc": "only in py"},
]

def run(name):
    if name == "hello":
        print("from-py")
    elif name == "pyonly":
        print("only-py")

if __name__ == "__main__":
    if len(sys.argv) >= 3 and sys.argv[1] == "__hire__":
        if sys.argv[2] == "discover":
            json.dump({"tasks": TASKS}, sys.stdout)
        elif sys.argv[2] == "run" and len(sys.argv) >= 4:
            run(sys.argv[3])
`)
	writeTaskfile(filepath.Join(cfgDir, "hire"), "config.lua", "return { runtimes = { python = "+luaString(py)+" } }")

	out, code := runBinOpt(t, bin, work, nil, "hello")
	if code != 0 {
		t.Fatalf("hello (merged): code=%d out=%q", code, out)
	}
	if !strings.Contains(out, "from-py") {
		t.Errorf("merged hello must run the py version (last file wins): %q", out)
	}
	if !strings.Contains(out, "redefined") {
		t.Errorf("same-name merge must warn on stderr: %q", out)
	}

	out, code = runBinOpt(t, bin, work, nil, "ls")
	if code != 0 || !strings.Contains(out, "tonly") || !strings.Contains(out, "luonly") || !strings.Contains(out, "pyonly") {
		t.Fatalf("ls (merged): code=%d out=%q", code, out)
	}

	out, code = runBinOpt(t, bin, work, nil, "tonly")
	if code != 0 || !strings.Contains(out, "only-toml") {
		t.Fatalf("tonly (toml adapter through the merge): code=%d out=%q", code, out)
	}
}

// TestIntegrationCwdPriorityGlobalFallback: a same-name task resolves
// to the cwd file (with a warning), and a directory without taskfiles
// falls back to the global ones.
func TestIntegrationCwdPriorityGlobalFallback(t *testing.T) {
	bin := buildBinary(t)
	cfgDir, _ := os.MkdirTemp("", "hire-cfg-")
	dataDir, _ := os.MkdirTemp("", "hire-data-")
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	t.Setenv("XDG_DATA_HOME", dataDir)
	tfDir := filepath.Join(cfgDir, "hire")
	os.MkdirAll(tfDir, 0o755)
	os.WriteFile(filepath.Join(tfDir, "Taskfile.lua"), []byte(`
where = function() x.echo("from-global") end
gonly = function() x.echo("only-global") end
`), 0o644)

	work := t.TempDir()
	writeTaskfile(work, "Taskfile.lua", `
where = function() x.echo("from-cwd") end
conly = function() x.echo("only-cwd") end
`)

	out, code := runBinOpt(t, bin, work, nil, "where")
	if code != 0 || !strings.Contains(out, "from-cwd") {
		t.Fatalf("cwd where: code=%d out=%q", code, out)
	}
	if !strings.Contains(out, "redefined") {
		t.Errorf("cwd override must warn: %q", out)
	}

	out, code = runBinOpt(t, bin, work, nil, "gonly")
	if code != 0 || !strings.Contains(out, "only-global") {
		t.Fatalf("global task from cwd dir: code=%d out=%q", code, out)
	}

	empty := t.TempDir()
	out, code = runBinOpt(t, bin, empty, nil, "where")
	if code != 0 || !strings.Contains(out, "from-global") {
		t.Fatalf("global fallback from empty dir: code=%d out=%q", code, out)
	}
}

// TestIntegrationCcExplicitTaskfile pins T6+7 review M6's last leg:
// .cc dispatches through the cpp runtime when (and only when) named
// explicitly via --taskfile; the auto-discovery list stays the fixed
// five and never picks a .cc up.
func TestIntegrationCcExplicitTaskfile(t *testing.T) {
	bin := buildBinary(t)
	cfgDir, _ := os.MkdirTemp("", "hire-cfg-")
	dataDir, _ := os.MkdirTemp("", "hire-data-")
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	t.Setenv("XDG_DATA_HOME", dataDir)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	langCmd(t, "g++")
	writeTaskfile(filepath.Join(cfgDir, "hire"), "config.lua",
		"return { runtimes = { cpp = { build = \"g++ -std=c++20 -O2 -o {out} {src}\" } } }")

	work := t.TempDir()
	// CppTemplate includes hire.hpp, so the SDK header must sit beside it.
	writeTaskfile(work, "hire.hpp", adapter.CppHeaderTemplate)
	writeTaskfile(work, "Taskfile.cc", adapter.CppTemplate)

	// Auto-discovery probes only the fixed five: a lone .cc is invisible.
	out, code := runBinOpt(t, bin, work, nil, "ls")
	if code == 0 || !strings.Contains(out, "no Taskfile found") {
		t.Fatalf("auto-discovery must ignore Taskfile.cc: code=%d out=%q", code, out)
	}

	out, code = runBinOpt(t, bin, work, nil, "--taskfile", "Taskfile.cc", "hello", "--name", "ccruns")
	if code != 0 || !strings.Contains(out, "hello, ccruns") {
		t.Fatalf("--taskfile Taskfile.cc hello: code=%d out=%q", code, out)
	}
}

// TestIntegrationBstartExternalLifecycle: an external-language task
// walks the whole background lifecycle — bstart, ls-running, stop,
// watch, clean — through the same bg machinery as native tasks.
// (Watch runs after stop, like TestIntegrationFlow, so the tail
// drains deterministically instead of racing a live instance.)
func TestIntegrationBstartExternalLifecycle(t *testing.T) {
	bin := buildBinary(t)
	cfgDir, _ := os.MkdirTemp("", "hire-cfg-")
	dataDir, _ := os.MkdirTemp("", "hire-data-")
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	t.Setenv("XDG_DATA_HOME", dataDir)
	py := langCmd(t, "python", "python3")

	work := t.TempDir()
	writeTaskfile(work, "Taskfile.py", `
import sys, time

TASKS = [{"name": "sleepy", "desc": "nap for 30 seconds"}]

def run(name):
    if name == "sleepy":
        print("nap starting", flush=True)
        time.sleep(30)

if __name__ == "__main__":
    if len(sys.argv) >= 3 and sys.argv[1] == "__hire__":
        if sys.argv[2] == "discover":
            import json
            json.dump({"tasks": TASKS}, sys.stdout)
        elif sys.argv[2] == "run" and len(sys.argv) >= 4:
            run(sys.argv[3])
`)
	writeTaskfile(filepath.Join(cfgDir, "hire"), "config.lua", "return { runtimes = { python = "+luaString(py)+" } }")

	out, code := runBinOpt(t, bin, work, nil, "bstart", "sleepy")
	if code != 0 || !strings.Contains(out, "started sleepy#1") {
		t.Fatalf("bstart sleepy: code=%d out=%q", code, out)
	}

	out, _ = runBinOpt(t, bin, work, nil, "ls-running")
	if !strings.Contains(out, "sleepy#1") {
		t.Fatalf("ls-running: %q", out)
	}
	time.Sleep(1 * time.Second)

	out, code = runBinOpt(t, bin, work, nil, "stop", "sleepy#1")
	if code != 0 || !strings.Contains(out, "stopped sleepy#1") {
		t.Fatalf("stop sleepy#1: code=%d out=%q", code, out)
	}

	out, code = runBinOpt(t, bin, work, nil, "watch", "sleepy#1")
	if code != 0 || !strings.Contains(out, "nap starting") {
		t.Fatalf("watch sleepy#1: code=%d out=%q", code, out)
	}

	out, code = runBinOpt(t, bin, work, nil, "clean", "--older-than=0")
	if code != 0 || !strings.Contains(out, "cleaned 1 instance(s)") {
		t.Fatalf("clean sleepy: code=%d out=%q", code, out)
	}
	out, _ = runBinOpt(t, bin, work, nil, "ls-running")
	if strings.Contains(out, "sleepy#1") {
		t.Fatalf("cleaned instance must be gone: %q", out)
	}
}
