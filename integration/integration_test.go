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
