package adapter_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"hire/internal/adapter"
)

// probeTool looks up a real interpreter / shell on PATH; toolchains are
// optional on dev machines, so absence skips the leg honestly.
func probeTool(t *testing.T, names ...string) string {
	t.Helper()
	for _, name := range names {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	t.Skipf("none of %s found in PATH", strings.Join(names, ", "))
	return ""
}

// shellProbeScript is the shared command sequence for both SDK legs:
// cd + export survive in instance 1, instance 2 and an explicit-shell
// one-shot stay isolated, and no sentinel reaches user stdout.
const shellProbeScript = `
    sh.run("cd /tmp")
    sh.run("echo MARK_PWD")
    sh.run("pwd")
    sh.run("export HIRE_TEST_FOO=42")
    sh.run("echo MARK_FOO")
    sh.run("echo $HIRE_TEST_FOO")
    sh.run("echo MARK_INST2")
    sh.run("echo $HIRE_TEST_FOO", 2)
    sh.run("echo MARK_ONESHOT")
    sh.run("echo $HIRE_TEST_FOO", 1, "sh")
`

func runSDKProbe(t *testing.T, prog, sdkFile, sdkBody, scriptFile, script string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, sdkFile), []byte(sdkBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, scriptFile), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(prog, scriptFile, "__hire__", "run", "probe")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HIRE_SHELL=sh", "HIRE_ARGS={}")
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s probe: %v\nstdout: %q\nstderr: %q", prog, err, stdout.String(), stderr.String())
	}
	return stdout.String(), stderr.String()
}

// assertShellProbe checks the protocol outcomes and parses the child
// shell pids printed by the probe script; every pid must be gone once
// the SDK process has exited (atexit / process.on('exit') teardown).
func assertShellProbe(t *testing.T, stdout, stderr string) {
	t.Helper()
	if strings.Contains(stdout, "__HIRE_RC") {
		t.Errorf("sentinel leaked into stdout: %q", stdout)
	}
	segments := map[string][]string{}
	current := ""
	for _, line := range strings.Split(strings.ReplaceAll(stdout, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "MARK_") {
			current = line
			segments[current] = nil
			continue
		}
		if current != "" {
			segments[current] = append(segments[current], line)
		}
	}
	if pwd := strings.Join(segments["MARK_PWD"], ""); !strings.Contains(pwd, "/tmp") {
		t.Errorf("cd did not persist in instance 1: pwd=%q (stdout %q)", pwd, stdout)
	}
	if foo := strings.Join(segments["MARK_FOO"], ""); !strings.Contains(foo, "42") {
		t.Errorf("export did not persist in instance 1: echo=%q (stdout %q)", foo, stdout)
	}
	if inst2 := strings.Join(segments["MARK_INST2"], ""); strings.Contains(inst2, "42") {
		t.Errorf("instance 2 is not isolated: echo=%q", inst2)
	}
	if oneShot := strings.Join(segments["MARK_ONESHOT"], ""); strings.Contains(oneShot, "42") {
		t.Errorf("explicit shell one-shot is not isolated: echo=%q", oneShot)
	}

	idx := strings.Index(stderr, "PIDS=")
	if idx < 0 {
		t.Fatalf("probe did not report child pids: stderr=%q", stderr)
	}
	tail := strings.TrimSpace(stderr[idx+len("PIDS="):])
	var pids []int
	if tail != "" {
		for _, field := range strings.Split(tail, ",") {
			pid, err := strconv.Atoi(field)
			if err != nil {
				t.Fatalf("bad pid %q in stderr %q", field, stderr)
			}
			pids = append(pids, pid)
		}
	}
	if len(pids) < 2 {
		t.Errorf("expected at least 2 persistent instances, got %v (stderr %q)", pids, stderr)
	}
	for _, pid := range pids {
		deadline := time.Now().Add(3 * time.Second)
		for processAlive(pid) && time.Now().Before(deadline) {
			time.Sleep(25 * time.Millisecond)
		}
		if processAlive(pid) {
			t.Errorf("orphaned shell pid %d after SDK exit", pid)
		}
	}
}

func processAlive(pid int) bool {
	if runtime.GOOS == "windows" {
		out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH").Output()
		return err == nil && strings.Contains(string(out), strconv.Itoa(pid))
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "pid=").Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

func TestPythonShellPersistent(t *testing.T) {
	py := probeTool(t, "python", "python3")
	probeTool(t, "sh") // the probe pins HIRE_SHELL=sh for portable Unix syntax
	const script = `import sys
from hire import Shell, task, dispatch
import hire

@task("probe", desc="persistent shell probe")
def probe(a):
    sh = Shell()
` + shellProbeScript + `
    sys.stderr.write("PIDS=" + ",".join(str(v.proc.pid) for v in hire._instances.values()) + "\n")
    sh.close()

if __name__ == "__main__":
    hire.dispatch()
`
	stdout, stderr := runSDKProbe(t, py, "hire.py", adapter.PythonSDKTemplate, "Taskfile.py", script)
	assertShellProbe(t, stdout, stderr)
}

func TestNodeShellPersistent(t *testing.T) {
	node := probeTool(t, "node")
	probeTool(t, "sh") // the probe pins HIRE_SHELL=sh for portable Unix syntax
	const script = `const sdk = require("./hire.js");
sdk.task("probe", { desc: "persistent shell probe" }, (a) => {
  const sh = new sdk.Shell();
` + shellProbeScript + `
  const pids = [];
  for (const inst of sdk._instances.values()) pids.push(inst.proc.pid);
  process.stderr.write("PIDS=" + pids.join(",") + "\n");
  sh.close();
});
sdk.dispatch();
`
	stdout, stderr := runSDKProbe(t, node, "hire.js", adapter.NodeSDKTemplate, "Taskfile.js", script)
	assertShellProbe(t, stdout, stderr)
}
