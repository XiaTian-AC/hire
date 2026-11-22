package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

const e2eJustfile = `# greet someone by name
greet name="world":
    echo "hello {{name}}"

# prepare things
prepare:
    echo prepared

[private]
secret:
    echo hidden

export JUST_VAR := "unused"
`

const e2eMakefile = `.PHONY: hello
CC = gcc

hello:
	echo "hello from make"

build: hello
	$(CC) -O2 -o app main.cpp
`

const e2eTaskfile = `version: '3'
vars:
  GREETING: hi-from-vars
tasks:
  pre:
    desc: prepare
    cmds:
      - echo preparing
  all:
    desc: run everything
    deps: [pre]
    cmds:
      - echo {{.GREETING}}
      - echo all-done
`

// runFor runs one cli command with fresh output buffers and returns
// (exit code, stdout).
func runFor(t *testing.T, d Deps, args ...string) (int, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	d.Stdout, d.Stderr = &out, &errOut
	code := Run(args, d)
	return code, out.String()
}

// collectWarnings pulls the "hire: not migrated:" lines out of stderr,
// in order, ignoring everything else.
func collectWarnings(stderr string) []string {
	var out []string
	for _, ln := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(ln, "hire: not migrated:") {
			out = append(out, ln)
		}
	}
	return out
}

func wantWarningLines(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("warnings:\n got: %s\nwant: %s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// importIn writes the source file into the (already chdir'ed) temp dir,
// runs `hire import`, and returns deps plus the import exit code and
// the full stderr.
func importIn(t *testing.T, d Deps, name, content string, args ...string) (int, string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	d.Stdout, d.Stderr = &out, &errOut
	code := Run(append(append([]string{"import"}, name), args...), d)
	return code, errOut.String()
}

// Three end-to-end legs (brief Step 1): each format converts, the
// output is discovered by `hire ls` and one task really runs through
// the same Run() path a user's binary would take.
func TestImportJustEndToEnd(t *testing.T) {
	t.Chdir(t.TempDir())
	d, _, _ := setupEnv(t, "")
	code, stderr := importIn(t, d, "justfile", e2eJustfile)
	if code != 0 {
		t.Fatalf("import justfile: exit=%d stderr=%q", code, stderr)
	}
	wantWarningLines(t, collectWarnings(stderr),
		"hire: not migrated: [private] recipe \"secret\" (skipped)",
		"hire: not migrated: export directive (file justfile)",
	)

	code, lsOut := runFor(t, d, "ls")
	if code != 0 {
		t.Fatalf("ls after import: exit=%d", code)
	}
	for _, want := range []string{"greet", "prepare"} {
		if !strings.Contains(lsOut, want) {
			t.Errorf("ls output %q missing %q", lsOut, want)
		}
	}
	if strings.Contains(lsOut, "secret") {
		t.Errorf("ls output %q should not list the private recipe", lsOut)
	}

	code, runOut := runFor(t, d, "greet", "--name=maker")
	if code != 0 {
		t.Fatalf("hire greet: exit=%d", code)
	}
	if !strings.Contains(runOut, "hello maker") {
		t.Errorf("hire greet output = %q, want 'hello maker'", runOut)
	}
}

func TestImportMakeEndToEnd(t *testing.T) {
	t.Chdir(t.TempDir())
	d, _, _ := setupEnv(t, "")
	code, stderr := importIn(t, d, "Makefile", e2eMakefile)
	if code != 0 {
		t.Fatalf("import Makefile: exit=%d stderr=%q", code, stderr)
	}
	wantWarningLines(t, collectWarnings(stderr),
		`hire: not migrated: make $VAR reference "$(CC)" (task build)`,
		"hire: not migrated: make timestamp/up-to-date incremental semantics (task build)",
	)

	code, lsOut := runFor(t, d, "ls")
	if code != 0 || !strings.Contains(lsOut, "hello") || !strings.Contains(lsOut, "build") {
		t.Fatalf("ls after import: exit=%d out=%q", code, lsOut)
	}

	code, runOut := runFor(t, d, "hello")
	if code != 0 {
		t.Fatalf("hire hello: exit=%d", code)
	}
	if !strings.Contains(runOut, "hello from make") {
		t.Errorf("hire hello output = %q", runOut)
	}
}

func TestImportGotaskEndToEnd(t *testing.T) {
	t.Chdir(t.TempDir())
	d, _, _ := setupEnv(t, "")
	code, stderr := importIn(t, d, "Taskfile.yml", e2eTaskfile)
	if code != 0 {
		t.Fatalf("import Taskfile.yml: exit=%d stderr=%q", code, stderr)
	}
	wantWarningLines(t, collectWarnings(stderr),
		"hire: not migrated: go-task runs deps concurrently; hire runs them sequentially in listed order (file Taskfile.yml)",
	)

	code, lsOut := runFor(t, d, "ls")
	if code != 0 || !strings.Contains(lsOut, "pre") || !strings.Contains(lsOut, "all") {
		t.Fatalf("ls after import: exit=%d out=%q", code, lsOut)
	}

	code, runOut := runFor(t, d, "all")
	if code != 0 {
		t.Fatalf("hire all: exit=%d", code)
	}
	// baked vars + sequential deps: preparing runs before all-done
	ip, ia := strings.Index(runOut, "preparing"), strings.Index(runOut, "all-done")
	if !strings.Contains(runOut, "hi-from-vars") {
		t.Errorf("hire all output = %q, want baked var", runOut)
	}
	if ip < 0 || ia < 0 || ip > ia {
		t.Errorf("hire all output = %q, deps must run before the body", runOut)
	}
}

func TestImportCollisionAndForce(t *testing.T) {
	t.Chdir(t.TempDir())
	d, _, _ := setupEnv(t, "")
	sentinel := []byte("# existing taskfile\n[tasks.old]\ncmd = \"echo old\"\n")
	if err := os.WriteFile("Taskfile.toml", sentinel, 0o644); err != nil {
		t.Fatal(err)
	}

	code, stderr := importIn(t, d, "justfile", e2eJustfile)
	if code != 1 {
		t.Fatalf("collision: exit=%d, want 1", code)
	}
	if !strings.Contains(stderr, "--force") {
		t.Errorf("collision stderr %q must hint --force", stderr)
	}
	got, _ := os.ReadFile("Taskfile.toml")
	if string(got) != string(sentinel) {
		t.Fatal("collision must not touch the existing file")
	}

	code, _ = importIn(t, d, "justfile", e2eJustfile, "--force")
	if code != 0 {
		t.Fatalf("import --force: exit=%d", code)
	}
	got, _ = os.ReadFile("Taskfile.toml")
	if strings.Contains(string(got), "[tasks.old]") || !strings.Contains(string(got), "[tasks.greet]") {
		t.Errorf("--force output = %q, want the converted file", got)
	}
}

func TestImportFromExplicitUnknownAndAmbiguous(t *testing.T) {
	t.Chdir(t.TempDir())
	d, _, _ := setupEnv(t, "")

	// explicit --from works on an unrecognized file name
	code, stderr := importIn(t, d, "old.tool", "hello:\n\techo hi\n", "--from", "make")
	if code != 0 {
		t.Fatalf("import --from make: exit=%d stderr=%q", code, stderr)
	}
	if _, err := os.Stat("Taskfile.toml"); err != nil {
		t.Fatalf("Taskfile.toml not written: %v", err)
	}

	// unknown --from value is a usage error
	code, stderr = importIn(t, d, "old.tool", "hello:\n\techo hi\n", "--from", "bogus")
	if code != 2 {
		t.Fatalf("unknown --from: exit=%d, want 2", code)
	}
	if !strings.Contains(stderr, "just, make, task") {
		t.Errorf("unknown --from stderr = %q", stderr)
	}

	// no file at all
	var out, errOut bytes.Buffer
	d.Stdout, d.Stderr = &out, &errOut
	if code := Run([]string{"import"}, d); code != 2 {
		t.Fatalf("import without file: exit=%d, want 2", code)
	}

	// unrecognizable content: refuse and list the three --from values
	code, stderr = importIn(t, d, "weird.txt", "just some random text\n")
	if code != 1 {
		t.Fatalf("ambiguous import: exit=%d, want 1", code)
	}
	for _, want := range []string{"--from just", "--from make", "--from task"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("ambiguous stderr %q missing %q", stderr, want)
		}
	}
}

func TestImportMalformedNoPanic(t *testing.T) {
	t.Chdir(t.TempDir())
	d, _, _ := setupEnv(t, "")

	// empty justfile
	code, stderr := importIn(t, d, "justfile", "")
	if code != 1 {
		t.Fatalf("empty justfile: exit=%d, want 1", code)
	}
	if !strings.Contains(stderr, "justfile") {
		t.Errorf("empty justfile stderr %q must name the file", stderr)
	}

	// make: recipe line without a target
	code, stderr = importIn(t, d, "Makefile", "\techo orphan\n")
	if code != 1 {
		t.Fatalf("orphan make recipe: exit=%d, want 1", code)
	}
	if !strings.Contains(stderr, "Makefile:1") {
		t.Errorf("orphan make stderr %q must name file and line", stderr)
	}

	// go-task: invalid yaml
	code, stderr = importIn(t, d, "Taskfile.yml", "tasks:\n  - broken: [unclosed\n")
	if code != 1 {
		t.Fatalf("invalid yaml: exit=%d, want 1", code)
	}
	if !strings.Contains(stderr, "Taskfile.yml") || !strings.Contains(stderr, "YAML") {
		t.Errorf("invalid yaml stderr %q must name the file", stderr)
	}
}
