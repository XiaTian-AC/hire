package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"hire/internal/argparse"
	"hire/internal/bg"
	"hire/internal/config"
)

func setupEnv(t *testing.T, taskfileContent string) (Deps, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	cfgDir := t.TempDir()
	dataDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	t.Setenv("XDG_DATA_HOME", dataDir)
	if taskfileContent != "" {
		tfDir := filepath.Join(cfgDir, "hire")
		os.MkdirAll(tfDir, 0o755)
		os.WriteFile(filepath.Join(tfDir, "Taskfile.lua"), []byte(taskfileContent), 0o644)
	}
	var out, errOut bytes.Buffer
	return Deps{
		SelfPath: os.Args[0],
		Version:  "test",
		Stdin:    strings.NewReader(""),
		Stdout:   &out,
		Stderr:   &errOut,
	}, &out, &errOut
}

func TestBareNoTaskfileExit0(t *testing.T) {
	// spec §10: bare hire is a discovery command now, not an error
	d, out, errOut := setupEnv(t, "")
	code := Run(nil, d)
	if code != 0 {
		t.Fatalf("bare hire without taskfile: code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "usage") {
		t.Errorf("bare hire should still print the usage hint: out=%q err=%q", out.String(), errOut.String())
	}
}

func TestBareListsTasks(t *testing.T) {
	d, out, _ := setupEnv(t, `
build = function() end
fmt = "cargo fmt"
`)
	code := Run(nil, d)
	if code != 0 {
		t.Fatalf("bare hire: code = %d, want 0", code)
	}
	s := out.String()
	if !strings.Contains(s, "build") || !strings.Contains(s, "fmt") {
		t.Errorf("bare hire should list tasks: %q", s)
	}
	if !strings.Contains(s, "usage") {
		t.Errorf("bare hire should print one usage line: %q", s)
	}
}

func TestRunVersion(t *testing.T) {
	d, out, _ := setupEnv(t, "")
	code := Run([]string{"version"}, d)
	if code != 0 || !strings.Contains(out.String(), "test") {
		t.Errorf("version: code=%d out=%q", code, out.String())
	}
}

func TestRunLs(t *testing.T) {
	d, out, _ := setupEnv(t, `
build = function() x.sh("cargo build") end
fmt = "cargo fmt --check"
check = { "echo step1", "echo step2" }
test = task { desc = "run tests", run = function() x.sh("cargo test") end }
`)
	code := Run([]string{"ls"}, d)
	if code != 0 {
		t.Errorf("ls: code = %d", code)
	}
	s := out.String()
	for _, want := range []string{"build", "fmt", "check", "test", "run tests"} {
		if !strings.Contains(s, want) {
			t.Errorf("ls output %q missing %q", s, want)
		}
	}
}

func TestRunTask(t *testing.T) {
	d, out, _ := setupEnv(t, `hello = function() x.echo("task ran") end`)
	code := Run([]string{"hello"}, d)
	if code != 0 {
		t.Errorf("hello: code = %d", code)
	}
	if !strings.Contains(out.String(), "task ran") {
		t.Errorf("hello output = %q", out.String())
	}
}

func TestRunTaskWithArgs(t *testing.T) {
	d, out, _ := setupEnv(t, `echo = task {
  args = { msg = req "the message" },
  run = function(args) x.echo("got=" .. args.msg) end,
}`)
	code := Run([]string{"echo", "--msg", "hello"}, d)
	if code != 0 {
		t.Errorf("echo: code = %d", code)
	}
	if !strings.Contains(out.String(), "got=hello") {
		t.Errorf("echo output = %q", out.String())
	}
}

func TestRunTaskNotFound(t *testing.T) {
	d, _, errOut := setupEnv(t, `a = function() end`)
	code := Run([]string{"nope"}, d)
	if code != 1 || !strings.Contains(errOut.String(), "not found") {
		t.Errorf("nope: code=%d err=%q", code, errOut.String())
	}
}

func TestHelpTask(t *testing.T) {
	d, out, _ := setupEnv(t, `release = task {
  desc = "release the project",
  args = { tag = req "version tag" },
  run = function(args) end,
}`)
	code := Run([]string{"help", "release"}, d)
	if code != 0 {
		t.Errorf("help release: code = %d", code)
	}
	s := out.String()
	if !strings.Contains(s, "release the project") || !strings.Contains(s, "--tag") {
		t.Errorf("help release output = %q", s)
	}
}

func TestLsRunningEmpty(t *testing.T) {
	d, out, _ := setupEnv(t, "")
	code := Run([]string{"ls-running"}, d)
	if code != 0 || !strings.Contains(out.String(), "no background") {
		t.Errorf("ls-running empty: code=%d out=%q", code, out.String())
	}
}

func TestLsRunningWithInstance(t *testing.T) {
	d, out, _ := setupEnv(t, "")
	dataDir, _ := config.DataDir()
	now := time.Now().UTC()
	bg.Register(dataDir, bg.Instance{
		ID: "build#1", Name: "build", N: 1, PID: 99999, Status: "exited", ExitCode: 0,
		Started: now, EndedAt: &now,
	})
	code := Run([]string{"ls-running"}, d)
	if code != 0 || !strings.Contains(out.String(), "build#1") {
		t.Errorf("ls-running: code=%d out=%q", code, out.String())
	}
}

func TestExtractShell(t *testing.T) {
	name, rest := extractShell([]string{"--tag", "v1", "--shell", "bash", "extra"})
	if name != "bash" || len(rest) != 3 || rest[0] != "--tag" || rest[1] != "v1" || rest[2] != "extra" {
		t.Errorf("extractShell: name=%q rest=%v", name, rest)
	}
}

func TestExtractShellEquals(t *testing.T) {
	name, rest := extractShell([]string{"--shell=bash", "pos"})
	if name != "bash" || len(rest) != 1 || rest[0] != "pos" {
		t.Errorf("extractShell=: name=%q rest=%v", name, rest)
	}
}

func TestExtractShellNotAfterDoubleDash(t *testing.T) {
	name, rest := extractShell([]string{"--", "--shell", "bash"})
	if name != "" || len(rest) != 3 {
		t.Errorf("extractShell should not cross --: name=%q rest=%v", name, rest)
	}
}

func TestConfirmYes(t *testing.T) {
	ok, eof := confirm("test?", strings.NewReader("y\n"), &bytes.Buffer{})
	if !ok || eof {
		t.Error("confirm('y') should be (true, false)")
	}
}

func TestConfirmNo(t *testing.T) {
	ok, eof := confirm("test?", strings.NewReader("n\n"), &bytes.Buffer{})
	if ok || eof {
		t.Error("confirm('n') should be (false, false)")
	}
}

func TestConfirmEOF(t *testing.T) {
	// non-interactive stdin: no answer possible, callers must abort
	// hard (exit 1), not treat it as a soft decline
	ok, eof := confirm("test?", strings.NewReader(""), &bytes.Buffer{})
	if ok || !eof {
		t.Error("confirm(EOF) should be (false, true)")
	}
}

func TestFindInstanceExact(t *testing.T) {
	all := []bg.Instance{
		{ID: "build#1", Name: "build", N: 1},
		{ID: "build#2", Name: "build", N: 2},
	}
	inst, err := findInstance(all, "build#1")
	if err != nil || inst.ID != "build#1" {
		t.Errorf("findInstance exact: %v %v", inst, err)
	}
}

func TestFindInstanceNewest(t *testing.T) {
	all := []bg.Instance{
		{ID: "build#1", Name: "build", N: 1},
		{ID: "build#2", Name: "build", N: 2},
	}
	inst, err := findInstance(all, "build")
	if err != nil || inst.ID != "build#2" {
		t.Errorf("findInstance newest: %v %v", inst, err)
	}
}

func TestFindInstanceMissing(t *testing.T) {
	_, err := findInstance(nil, "nope")
	if err == nil {
		t.Error("expected error for missing instance")
	}
}
func TestLsRunningFilterOld(t *testing.T) {
	d, out, _ := setupEnv(t, "")
	dataDir, _ := config.DataDir()
	started := time.Now().UTC().Add(-2 * time.Hour)
	ended := time.Now().UTC().Add(-2 * time.Hour)
	bg.Register(dataDir, bg.Instance{
		ID: "build#1", Name: "build", N: 1, PID: 99999, Status: "exited",
		Started: started, EndedAt: &ended,
	})
	code := Run([]string{"ls-running"}, d)
	if code != 0 || !strings.Contains(out.String(), "no recent") {
		t.Errorf("expected filtered: code=%d out=%q", code, out.String())
	}
}

func TestLsRunningRunningFirst(t *testing.T) {
	d, out, _ := setupEnv(t, "")
	dataDir, _ := config.DataDir()
	now := time.Now().UTC()
	bg.Register(dataDir, bg.Instance{ID: "old-task#1", Name: "old-task", N: 1, PID: 1, Status: "exited", Started: now, EndedAt: &now})
	bg.Register(dataDir, bg.Instance{ID: "live-task#1", Name: "live-task", N: 1, PID: 99999, Status: "running", Started: now})
	code := Run([]string{"ls-running"}, d)
	if code != 0 {
		t.Fatal(code)
	}
	s := out.String()
	idxRun := strings.Index(s, "live-task#1")
	idxExit := strings.Index(s, "old-task#1")
	if idxRun < 0 || idxExit < 0 || idxRun > idxExit {
		t.Errorf("running should come first: %q", s)
	}
}

func TestCleanAll(t *testing.T) {
	d, out, _ := setupEnv(t, "")
	dataDir, _ := config.DataDir()
	now := time.Now().UTC()
	bg.Register(dataDir, bg.Instance{ID: "stale#1", Name: "stale", N: 1, PID: 1, Status: "exited", Started: now, EndedAt: &now})
	bg.Register(dataDir, bg.Instance{ID: "live#1", Name: "live", N: 1, PID: 99999, Status: "running", Started: now})
	code := Run([]string{"clean"}, d)
	if code != 0 {
		t.Fatalf("clean exit = %d, out=%q", code, out.String())
	}
	all, _ := bg.Snapshot(dataDir)
	if len(all) != 1 || all[0].ID != "live#1" {
		t.Errorf("after clean = %v, want only running", all)
	}
	if !strings.Contains(out.String(), "cleaned 1") {
		t.Errorf("output = %q, want 'cleaned 1'", out.String())
	}
}

func TestCleanOlderThan(t *testing.T) {
	d, out, _ := setupEnv(t, "")
	dataDir, _ := config.DataDir()
	now := time.Now().UTC()
	old := now.Add(-48 * time.Hour)
	recent := now.Add(-10 * time.Minute)
	bg.Register(dataDir, bg.Instance{ID: "build#1", Name: "build", N: 1, PID: 1, Status: "exited", Started: old, EndedAt: &old})
	bg.Register(dataDir, bg.Instance{ID: "build#2", Name: "build", N: 2, PID: 2, Status: "exited", Started: now, EndedAt: &recent})
	code := Run([]string{"clean", "--older-than=1h"}, d)
	if code != 0 {
		t.Fatalf("clean exit = %d, out=%q", code, out.String())
	}
	all, _ := bg.Snapshot(dataDir)
	if len(all) != 1 || all[0].ID != "build#2" {
		t.Errorf("after clean older-than = %v, want only recent", all)
	}
}

func TestCleanOlderThanRunningSkipped(t *testing.T) {
	d, _, _ := setupEnv(t, "")
	dataDir, _ := config.DataDir()
	now := time.Now().UTC()
	bg.Register(dataDir, bg.Instance{ID: "build#1", Name: "build", N: 1, PID: 1, Status: "running", Started: now})
	code := Run([]string{"clean"}, d)
	if code != 0 {
		t.Fatalf("clean exit = %d", code)
	}
	all, _ := bg.Snapshot(dataDir)
	if len(all) != 1 {
		t.Errorf("running instance should be kept")
	}
}
func TestArgsHint(t *testing.T) {
	if got := argsHint(nil); got != "" {
		t.Errorf("argsHint(nil) = %q, want empty", got)
	}
	specs := map[string]argparse.Spec{
		"tag":   {Type: "string", Required: true},
		"force": {Type: "bool"},
	}
	got := argsHint(specs)
	want := "[args: --force, --tag (required)]"
	if got != want {
		t.Errorf("argsHint = %q, want %q", got, want)
	}
}

func TestLsShowsRequiredArgs(t *testing.T) {
	d, out, _ := setupEnv(t, `
release = task {
  desc = "release it",
  args = { tag = req "" },
  run = function() end,
}
plain = function() end
`)
	code := Run([]string{"ls"}, d)
	if code != 0 {
		t.Fatalf("ls exit = %d", code)
	}
	s := out.String()
	if !strings.Contains(s, "--tag (required)") {
		t.Errorf("ls output should mark required arg: %q", s)
	}
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, "plain") && strings.Contains(line, "[args") {
			t.Errorf("task without args should not have args hint: %q", line)
		}
	}
}

func TestExtractTaskfile(t *testing.T) {
	path, rest := extractTaskfile([]string{"--taskfile", "/tmp/x.lua", "hello"})
	if path != "/tmp/x.lua" || len(rest) != 1 || rest[0] != "hello" {
		t.Errorf("extractTaskfile: path=%q rest=%v", path, rest)
	}
	path, rest = extractTaskfile([]string{"--taskfile=/y.lua"})
	if path != "/y.lua" || len(rest) != 0 {
		t.Errorf("extractTaskfile=: path=%q rest=%v", path, rest)
	}
	path, rest = extractTaskfile([]string{"--", "--taskfile", "x.lua"})
	if path != "" || len(rest) != 3 {
		t.Errorf("extractTaskfile should stop at --: path=%q rest=%v", path, rest)
	}
}

// C1 regression: `hire --taskfile <path>` with no command used to panic
// (argv[0] on a slice the flag extraction emptied). Both flag forms must
// behave like bare `hire`: list the override file's tasks plus the
// usage hint, exit 0 (spec §10).
func TestBareTaskfileOverrideListsTasks(t *testing.T) {
	localDir := t.TempDir()
	localTF := filepath.Join(localDir, "My.lua")
	if err := os.WriteFile(localTF, []byte(`local_task = function() end`), 0o644); err != nil {
		t.Fatal(err)
	}
	d, out, errOut := setupEnv(t, `global_task = function() end`)

	for _, argv := range [][]string{
		{"--taskfile", localTF},
		{"--taskfile=" + localTF},
	} {
		out.Reset()
		errOut.Reset()
		code := Run(argv, d)
		if code != 0 {
			t.Fatalf("Run(%q): code = %d, want 0", argv, code)
		}
		s := out.String()
		if !strings.Contains(s, "local_task") || strings.Contains(s, "global_task") {
			t.Errorf("Run(%q): should list only the override file's tasks: %q", argv, s)
		}
		if !strings.Contains(s, "usage") {
			t.Errorf("Run(%q): should print the usage hint: %q", argv, s)
		}
	}
}

func TestRunWithTaskfileOverride(t *testing.T) {
	d, out, errOut := setupEnv(t, `global_only = function() x.echo("from-global") end`)
	localDir := t.TempDir()
	localTF := filepath.Join(localDir, "My.lua")
	os.WriteFile(localTF, []byte(`local_task = function() x.echo("from-local") end`), 0o644)

	code := Run([]string{"--taskfile", localTF, "local_task"}, d)
	if code != 0 || !strings.Contains(out.String(), "from-local") {
		t.Errorf("--taskfile run: code=%d out=%q", code, out.String())
	}

	out.Reset()
	errOut.Reset()
	code = Run([]string{"--taskfile=" + localTF, "global_only"}, d)
	if code == 0 || !strings.Contains(errOut.String(), "not found") {
		t.Errorf("override should hide global tasks: code=%d err=%q", code, errOut.String())
	}
}

func TestConfigCommand(t *testing.T) {
	d, out, _ := setupEnv(t, "")
	cfgDir := os.Getenv("XDG_CONFIG_HOME")
	tfDir := filepath.Join(cfgDir, "hire")
	os.MkdirAll(tfDir, 0o755)
	os.WriteFile(filepath.Join(tfDir, "config.lua"), []byte(`return {
  shells = { gitbash = "C:/bin/bash.exe" },
  display = { ls_running = { time = "2d", running_first = false } },
}`), 0o644)

	code := Run([]string{"config"}, d)
	if code != 0 {
		t.Fatalf("config exit = %d", code)
	}
	s := out.String()
	if !strings.Contains(s, "time          = 2d") || !strings.Contains(s, "running_first = false") {
		t.Errorf("display section wrong:\n%s", s)
	}
	if !strings.Contains(s, "gitbash") {
		t.Errorf("shells section missing gitbash:\n%s", s)
	}

	out.Reset()
	code = Run([]string{"config", "shells"}, d)
	if code != 0 || !strings.Contains(out.String(), "gitbash") || strings.Contains(out.String(), "[display") {
		t.Errorf("config shells filter: code=%d out=%q", code, out.String())
	}

	out.Reset()
	code = Run([]string{"config", "bogus"}, d)
	if code != 2 {
		t.Errorf("unknown section should exit 2, got %d", code)
	}
}

// --- Task 6: lookup chain, merge, HIRE_FILES handoff, HIRE_ARGS ---

// fakeInterp builds the adapter package's __hire__ contract test double
// once per test run, so chain tests cover exec dispatch without
// python/node/g++ on the machine.
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
	cmd.Dir = filepath.Join("..", "..", "internal", "adapter", "testdata", "fakeinterp")
	if out, err := cmd.CombinedOutput(); err != nil {
		panic("go build fakeinterp: " + err.Error() + "\n" + string(out))
	}
	return exe
})

// captureStderr swaps the real os.Stderr for the duration of fn and
// returns what was written to it: adapter.NewChain prints merge
// warnings there directly, not through Deps.Stderr.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()
	fn()
	w.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLookupChainNoFilesListsProbedPaths(t *testing.T) {
	d, _, errOut := setupEnv(t, "")
	t.Chdir(t.TempDir())
	code := Run([]string{"ls"}, d)
	if code != 1 {
		t.Fatalf("ls with no taskfiles: code = %d, want 1", code)
	}
	err := errOut.String()
	cfgDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "hire")
	cwd, _ := os.Getwd()
	for _, want := range []string{
		"Taskfile.toml", "Taskfile.lua", "Taskfile.py", "Taskfile.js", "Taskfile.cpp",
		filepath.Join(cfgDir, "Taskfile.toml"), filepath.Join(cwd, "Taskfile.cpp"),
	} {
		if !strings.Contains(err, want) {
			t.Errorf("error %q missing probed path %q", err, want)
		}
	}
}

func TestLookupChainGlobalOnly(t *testing.T) {
	d, out, _ := setupEnv(t, `hello = function() x.echo("global-ran") end`)
	t.Chdir(t.TempDir())
	if code := Run([]string{"hello"}, d); code != 0 {
		t.Fatalf("hello: code = %d", code)
	}
	if !strings.Contains(out.String(), "global-ran") {
		t.Errorf("out = %q", out.String())
	}
}

func TestLookupChainCwdOnly(t *testing.T) {
	d, out, _ := setupEnv(t, "") // no global taskfile
	cwd := t.TempDir()
	os.WriteFile(filepath.Join(cwd, "Taskfile.lua"), []byte(`hello = function() x.echo("cwd-ran") end`), 0o644)
	t.Chdir(cwd)
	if code := Run([]string{"hello"}, d); code != 0 {
		t.Fatalf("hello: code = %d", code)
	}
	if !strings.Contains(out.String(), "cwd-ran") {
		t.Errorf("out = %q", out.String())
	}
}

func TestLookupChainMergeCwdWins(t *testing.T) {
	d, out, errOut := setupEnv(t, `
shell = "sh"
dup = function() x.echo("from-global") end
globalonly = function() x.echo("globalonly-ran") end
`)
	cfgDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "hire")
	cwd := t.TempDir()
	os.WriteFile(filepath.Join(cwd, "Taskfile.lua"), []byte(`
shell = "pwsh"
dup = function() x.echo("from-cwd") end
localonly = function() x.echo("localonly-ran") end
`), 0o644)
	os.WriteFile(filepath.Join(cwd, "Taskfile.toml"), []byte(`
[tasks.tomlonly]
desc = "from the cwd toml file"
cmd = "echo toml"
`), 0o644)
	t.Chdir(cwd)

	var code int
	warn := captureStderr(t, func() {
		code = Run([]string{"dup"}, d)
	})
	if code != 0 {
		t.Fatalf("dup: code = %d", code)
	}
	if !strings.Contains(out.String(), "from-cwd") {
		t.Errorf("dup output = %q, want the cwd version", out.String())
	}
	warn += errOut.String()
	if !strings.Contains(warn, "redefined") ||
		!strings.Contains(warn, filepath.Join(cwd, "Taskfile.lua")) ||
		!strings.Contains(warn, filepath.Join(cfgDir, "Taskfile.lua")) {
		t.Errorf("merge warning should name both source files: %q", warn)
	}

	out.Reset()
	if code = Run([]string{"globalonly"}, d); code != 0 || !strings.Contains(out.String(), "globalonly-ran") {
		t.Errorf("globalonly: code=%d out=%q", code, out.String())
	}

	out.Reset()
	if code = Run([]string{"ls"}, d); code != 0 || !strings.Contains(out.String(), "tomlonly") {
		t.Errorf("ls should list the cwd TOML task: code=%d out=%q", code, out.String())
	}

	c, _, err := loadTaskfile(d)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.DefaultShell != "pwsh" {
		t.Errorf("DefaultShell = %q, want the cwd declaration to win", c.DefaultShell)
	}
}

func TestTaskfileExplicitMjs(t *testing.T) {
	d, out, errOut := setupEnv(t, "")
	cfgDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "hire")
	os.MkdirAll(cfgDir, 0o755)
	cfg := "return { runtimes = { node = \"" + filepath.ToSlash(fakeInterp()) + "\" } }"
	os.WriteFile(filepath.Join(cfgDir, "config.lua"), []byte(cfg), 0o644)
	src := filepath.Join(t.TempDir(), "Taskfile.mjs")
	os.WriteFile(src, []byte("// fake mjs taskfile\n"), 0o644)

	if code := Run([]string{"--taskfile", src, "ls"}, d); code != 0 {
		t.Fatalf("ls via --taskfile .mjs: code=%d err=%q", code, errOut.String())
	}
	if s := out.String(); !strings.Contains(s, "echo") || !strings.Contains(s, "boom") {
		t.Errorf(".mjs ls output = %q", s)
	}

	out.Reset()
	writeTo := filepath.Join(t.TempDir(), "args-out.txt")
	t.Setenv("HIRE_WRITE", writeTo)
	if code := Run([]string{"--taskfile", src, "echo", "--msg", "hi"}, d); code != 0 {
		t.Fatalf("echo via --taskfile .mjs: code=%d err=%q", code, errOut.String())
	}
	got, err := os.ReadFile(writeTo)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"msg":"hi"`) {
		t.Errorf("HIRE_ARGS round trip through the exec adapter = %q", got)
	}

	// .mjs is never auto-discovered: none of the five probed files
	// exist, so plain ls must still report the lookup failure.
	out.Reset()
	errOut.Reset()
	if code := Run([]string{"ls"}, d); code != 1 || !strings.Contains(errOut.String(), "Taskfile.lua") {
		t.Errorf("bare ls should not discover .mjs: code=%d err=%q", code, errOut.String())
	}
}

func TestInternalRunHireFiles(t *testing.T) {
	d, out, errOut := setupEnv(t, "") // no global: _run must come entirely from HIRE_FILES
	src := filepath.Join(t.TempDir(), "Taskfile.lua")
	os.WriteFile(src, []byte(`hello = function() x.echo("env-file-ran") end`), 0o644)
	dataDir, _ := config.DataDir()
	bg.Register(dataDir, bg.Instance{ID: "envtest#1", Name: "hello", N: 1, Status: "running", Started: time.Now().UTC()})
	j, err := json.Marshal([]string{src})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HIRE_FILES", string(j))
	code := Run([]string{"_run", "hello", "--id", "envtest#1", "--"}, d)
	if code != 0 {
		t.Fatalf("_run: code=%d err=%q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "env-file-ran") {
		t.Errorf("_run out = %q", out.String())
	}
	all, _ := bg.Snapshot(dataDir)
	for _, inst := range all {
		if inst.ID == "envtest#1" {
			if inst.Status != "exited" || inst.ExitCode != 0 {
				t.Errorf("instance after _run = %+v, want exited(0)", inst)
			}
			return
		}
	}
	t.Errorf("envtest#1 missing from registry: %v", all)
}

func TestInternalRunHireFilesMissingFile(t *testing.T) {
	// The deterministic core of "bstart, then the taskfile vanishes":
	// HIRE_FILES pointing at a missing file must surface the path, and
	// the instance must be retired, not left "running" forever.
	d, _, errOut := setupEnv(t, "")
	dataDir, _ := config.DataDir()
	bg.Register(dataDir, bg.Instance{ID: "ghost#1", Name: "hello", N: 1, Status: "running", Started: time.Now().UTC()})
	ghost := filepath.Join(t.TempDir(), "gone.lua")
	j, _ := json.Marshal([]string{ghost})
	t.Setenv("HIRE_FILES", string(j))
	code := Run([]string{"_run", "hello", "--id", "ghost#1", "--"}, d)
	if code != 1 {
		t.Fatalf("_run with missing file: code = %d, want 1", code)
	}
	if err := errOut.String(); !strings.Contains(err, "no Taskfile at") || !strings.Contains(err, ghost) {
		t.Errorf("missing-file error = %q", err)
	}
	all, _ := bg.Snapshot(dataDir)
	for _, inst := range all {
		if inst.ID == "ghost#1" {
			if inst.Status != "exited" || inst.ExitCode != 1 {
				t.Errorf("ghost#1 = %+v, want exited(1)", inst)
			}
			return
		}
	}
	t.Errorf("ghost#1 missing from registry: %v", all)
}

func TestInternalRunFallbackProbe(t *testing.T) {
	d, out, errOut := setupEnv(t, `fb = function() x.echo("fallback-ran") end`)
	t.Setenv("HIRE_FILES", "")
	code := Run([]string{"_run", "fb", "--id", "fb#1", "--"}, d)
	if code != 0 {
		t.Fatalf("_run fallback: code=%d err=%q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "fallback-ran") {
		t.Errorf("out = %q", out.String())
	}
	if !strings.Contains(errOut.String(), "HIRE_FILES") {
		t.Errorf("fallback should note the missing HIRE_FILES: %q", errOut.String())
	}
}

func TestHireArgsInjectedForeground(t *testing.T) {
	d, _, _ := setupEnv(t, `echo = task {
  args = { msg = req "the message", force = bool "a flag" },
  run = function(args) x.echo("ran") end,
}`)
	t.Setenv("HIRE_ARGS", "")
	if code := Run([]string{"echo", "--msg", "hi", "--force", "pos1"}, d); code != 0 {
		t.Fatalf("echo: code = %d", code)
	}
	want := `{"_":["pos1"],"force":true,"msg":"hi"}`
	if got := os.Getenv("HIRE_ARGS"); got != want {
		t.Errorf("HIRE_ARGS = %q, want %q", got, want)
	}
}

func TestHireArgsNoDeclaredArgs(t *testing.T) {
	d, _, _ := setupEnv(t, `plain = function() x.echo("ran") end`)
	t.Setenv("HIRE_ARGS", "")
	if code := Run([]string{"plain"}, d); code != 0 {
		t.Fatalf("plain: code = %d", code)
	}
	if got := os.Getenv("HIRE_ARGS"); got != "{}" {
		t.Errorf("HIRE_ARGS = %q, want {}", got)
	}
}

func TestHireArgsInjectedInternalRun(t *testing.T) {
	d, _, errOut := setupEnv(t, `echo = task {
  args = { msg = req "the message" },
  run = function(args) x.echo("ran") end,
}`)
	t.Setenv("HIRE_FILES", "")
	t.Setenv("HIRE_ARGS", "")
	if code := Run([]string{"_run", "echo", "--msg", "hi", "--"}, d); code != 0 {
		t.Fatalf("_run echo: code=%d err=%q", code, errOut.String())
	}
	if got := os.Getenv("HIRE_ARGS"); got != `{"_":[],"msg":"hi"}` {
		t.Errorf("HIRE_ARGS = %q", got)
	}
}

// --- Task 7: UX polish + audit cleanup ---

func TestDidYouMeanNear(t *testing.T) {
	d, _, errOut := setupEnv(t, `build = function() end`)
	if code := Run([]string{"buld"}, d); code != 1 {
		t.Fatalf("buld: code = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), `did you mean "build"`) {
		t.Errorf("near typo should suggest: %q", errOut.String())
	}
	// edit distance exactly 2 is still close enough
	errOut.Reset()
	if code := Run([]string{"buildxx"}, d); code != 1 {
		t.Fatalf("buildxx: code = %d", code)
	}
	if !strings.Contains(errOut.String(), `did you mean "build"`) {
		t.Errorf("distance-2 typo should suggest: %q", errOut.String())
	}
}

func TestDidYouMeanFar(t *testing.T) {
	d, _, errOut := setupEnv(t, `build = function() end`)
	for _, name := range []string{"zzzzzz", "buildxxx"} { // distance >= 3
		errOut.Reset()
		if code := Run([]string{name}, d); code != 1 {
			t.Fatalf("%s: code = %d", name, code)
		}
		if strings.Contains(errOut.String(), "did you mean") {
			t.Errorf("far typo %q should not suggest: %q", name, errOut.String())
		}
	}
}

func TestTaskHelpFlagRoutesBeforeArgparse(t *testing.T) {
	d, out, _ := setupEnv(t, `deploy = task {
  desc = "deploy the app",
  args = { env = req "target env" },
  run = function(args) x.echo("deploying") end,
}`)
	code := Run([]string{"deploy", "--help"}, d)
	if code != 0 {
		t.Fatalf("deploy --help: code = %d, want 0", code)
	}
	s := out.String()
	if !strings.Contains(s, "deploy the app") || !strings.Contains(s, "--env") {
		t.Errorf("deploy --help output = %q", s)
	}
	if strings.Contains(s, "deploying") {
		t.Errorf("--help must not run the task: %q", s)
	}
}

func TestBstartNonInteractiveAndYes(t *testing.T) {
	d, out, errOut := setupEnv(t, `dup = function() end`)
	dataDir, _ := config.DataDir()
	bg.Register(dataDir, bg.Instance{ID: "dup#1", Name: "dup", N: 1, PID: 99999, Status: "running", Started: time.Now().UTC()})
	// the start attempt after --yes fails at spawn (bogus self path),
	// which still proves the confirmation was bypassed
	d.SelfPath = filepath.Join(t.TempDir(), "no-such-hire-binary")
	d.Stdin = strings.NewReader("") // non-interactive: immediate EOF

	code := Run([]string{"bstart", "dup"}, d)
	if code != 1 {
		t.Fatalf("bstart non-interactive abort: code = %d, want 1", code)
	}
	if !strings.Contains(out.String(), "aborted") {
		t.Errorf("out = %q", out.String())
	}
	all, _ := bg.Snapshot(dataDir)
	if len(all) != 1 {
		t.Errorf("abort must not register a second instance: %v", all)
	}

	out.Reset()
	errOut.Reset()
	code = Run([]string{"bstart", "dup", "--yes"}, d)
	if s := out.String(); strings.Contains(s, "[y/N]") || strings.Contains(s, "aborted") {
		t.Errorf("--yes should skip the confirmation: %q", s)
	}
	if code != 1 || !strings.Contains(errOut.String(), "spawn") {
		t.Errorf("--yes bstart should reach the spawn: code=%d err=%q", code, errOut.String())
	}
	all, _ = bg.Snapshot(dataDir)
	if len(all) != 2 {
		t.Errorf("the --yes attempt should register a second instance: %v", all)
	}
}

func TestStopNonInteractiveAndYes(t *testing.T) {
	d, out, _ := setupEnv(t, "")
	dataDir, _ := config.DataDir()
	now := time.Now().UTC()
	bg.Register(dataDir, bg.Instance{ID: "svc#1", Name: "svc", N: 1, PID: 99998, Status: "running", Started: now})
	bg.Register(dataDir, bg.Instance{ID: "svc#2", Name: "svc", N: 2, PID: 99999, Status: "running", Started: now})
	d.Stdin = strings.NewReader("")

	code := Run([]string{"stop", "svc"}, d)
	if code != 1 {
		t.Fatalf("stop non-interactive abort: code = %d, want 1", code)
	}
	if !strings.Contains(out.String(), "aborted") {
		t.Errorf("out = %q", out.String())
	}
	all, _ := bg.Snapshot(dataDir)
	for _, inst := range all {
		if inst.Status != "running" {
			t.Errorf("abort must not stop anything: %+v", inst)
		}
	}

	out.Reset()
	code = Run([]string{"stop", "svc", "--yes"}, d)
	if code != 0 {
		t.Fatalf("stop --yes: code = %d", code)
	}
	if s := out.String(); strings.Contains(s, "[y/N]") {
		t.Errorf("--yes should skip the confirmation: %q", s)
	}
	all, _ = bg.Snapshot(dataDir)
	for _, inst := range all {
		if inst.Status != "stopped" {
			t.Errorf("--yes should stop both instances: %+v", inst)
		}
	}
}

func TestCleanOlderThanSpaceForm(t *testing.T) {
	// audit #2 regression: `clean --older-than 1h` used to be ignored
	// and silently cleaned EVERYTHING
	d, _, _ := setupEnv(t, "")
	dataDir, _ := config.DataDir()
	now := time.Now().UTC()
	old := now.Add(-48 * time.Hour)
	recent := now.Add(-10 * time.Minute)
	bg.Register(dataDir, bg.Instance{ID: "build#1", Name: "build", N: 1, PID: 1, Status: "exited", Started: old, EndedAt: &old})
	bg.Register(dataDir, bg.Instance{ID: "build#2", Name: "build", N: 2, PID: 2, Status: "exited", Started: now, EndedAt: &recent})
	code := Run([]string{"clean", "--older-than", "1h"}, d)
	if code != 0 {
		t.Fatalf("clean --older-than 1h: code = %d", code)
	}
	all, _ := bg.Snapshot(dataDir)
	if len(all) != 1 || all[0].ID != "build#2" {
		t.Errorf("space form must delete only instances past the threshold, got %v", all)
	}
}

func TestCleanOlderThanMissingValue(t *testing.T) {
	// --older-than without a value must error, not fall back to
	// clean-all
	d, _, errOut := setupEnv(t, "")
	dataDir, _ := config.DataDir()
	now := time.Now().UTC()
	bg.Register(dataDir, bg.Instance{ID: "x#1", Name: "x", N: 1, PID: 1, Status: "exited", Started: now, EndedAt: &now})
	code := Run([]string{"clean", "--older-than"}, d)
	if code != 2 {
		t.Fatalf("clean --older-than without value: code = %d, want 2", code)
	}
	all, _ := bg.Snapshot(dataDir)
	if len(all) != 1 {
		t.Errorf("nothing should be removed: %v", all)
	}
	if !strings.Contains(errOut.String(), "--older-than") {
		t.Errorf("error should name the flag: %q", errOut.String())
	}
}

func TestBuiltinCollisionHint(t *testing.T) {
	// A task named like a builtin is unreachable by its bare name
	// (the builtin wins); the usage error must point at the escape
	// hatch directly instead of hiding it in `hire help`.
	d, _, errOut := setupEnv(t, `watch = function() x.echo("my watch task") end`)
	code := Run([]string{"watch"}, d)
	if code != 2 {
		t.Fatalf("bare watch with a shadowed task: code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "hire run watch") {
		t.Errorf("collision hint missing: %q", errOut.String())
	}
}
