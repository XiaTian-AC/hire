package importer_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"hire/internal/adapter"
	"hire/internal/importer"
)

func convert(t *testing.T, format, name, src string) *importer.Result {
	t.Helper()
	res, err := importer.Import(format, name, []byte(src))
	if err != nil {
		t.Fatalf("Import(%s): %v\nsource:\n%s", format, err, src)
	}
	return res
}

func wantErr(t *testing.T, format, name, src, want string) {
	t.Helper()
	_, err := importer.Import(format, name, []byte(src))
	if err == nil {
		t.Fatalf("Import(%s): expected error containing %q, got nil\nsource:\n%s", format, want, src)
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("Import(%s) error = %q, want containing %q", format, err, want)
	}
}

func wantWarnings(t *testing.T, res *importer.Result, want ...string) {
	t.Helper()
	got := strings.Join(res.Warnings, "\n")
	w := strings.Join(want, "\n")
	if got != w {
		t.Errorf("warnings:\n got: %s\nwant: %s", got, w)
	}
}

func findTask(t *testing.T, res *importer.Result, name string) importer.Task {
	t.Helper()
	for _, task := range res.Tasks {
		if task.Name == name {
			return task
		}
	}
	t.Fatalf("task %q not found in %v", name, res.Tasks)
	return importer.Task{}
}

func findArg(t *testing.T, task importer.Task, name string) importer.Arg {
	t.Helper()
	for _, a := range task.Args {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("arg %q not found in task %q", name, task.Name)
	return importer.Arg{}
}

// --- sniffing (ruling 1) ---

func TestSniff(t *testing.T) {
	for _, tc := range []struct {
		name, src, want string
	}{
		{"justfile", "build:\n    echo hi\n", "just"},
		{"justfile", "\techo tabbed recipe\n", "just"},        // name wins over content
		{".justfile", "build:\n    echo hi\n", "just"},        // dotfile name
		{"Makefile", "hello:\n\techo hi\n", "make"},           // name wins
		{"GNUmakefile", "hello:\n\techo hi\n", "make"},        // GNU variant name
		{"Taskfile.yml", "version: '3'\ntasks: {}\n", "task"}, // canonical name
		{"Taskfile.yaml", "tasks: {}\n", "task"},
		{"build.tool", "version: '3'\ntasks: {}\n", "task"},    // content: leading version:
		{"misc", "tasks:\n  a:\n    cmds: [echo x]\n", "task"}, // content: leading tasks:
		{"misc", "\techo tabbed\n", "make"},                    // content: tab recipe line
		{"misc", "hello: dep\n\techo hi\n", "make"},            // tabs present -> make beats just
		{"misc", "hello:\n    echo hi\n", "just"},              // colon recipe, no tabs -> just
	} {
		got, err := importer.Sniff(tc.name, []byte(tc.src))
		if err != nil {
			t.Errorf("Sniff(%q): unexpected error %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Sniff(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestSniffAmbiguous(t *testing.T) {
	_, err := importer.Sniff("weird.tool", []byte("just some random text\n"))
	if err == nil {
		t.Fatal("Sniff on unrecognizable content: expected error listing --from values")
	}
	for _, want := range []string{"--from just", "--from make", "--from task"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Sniff error = %q, want it to list %q", err, want)
		}
	}
}

// --- just (ruling 2) ---

const justTypedSrc = `
# greet someone
greet name="world" force=false count=3 speed=1.5 target:
    echo {{name}} {{count}} {{speed}} {{target}}
    echo done
`

func TestJustParamTypeInference(t *testing.T) {
	res := convert(t, "just", "justfile", justTypedSrc)
	task := findTask(t, res, "greet")
	if task.Desc != "greet someone" {
		t.Errorf("desc = %q, want %q", task.Desc, "greet someone")
	}

	name := findArg(t, task, "name")
	if name.Type != "string" || name.Required || name.Desc != "default: world" {
		t.Errorf("name arg = %+v, want optional string with desc %q", name, "default: world")
	}
	force := findArg(t, task, "force")
	if force.Type != "bool" || force.Required || force.Desc != "default: false" {
		t.Errorf("force arg = %+v, want optional bool with desc %q", force, "default: false")
	}
	count := findArg(t, task, "count")
	if count.Type != "num" || count.Required || count.Desc != "default: 3" {
		t.Errorf("count arg = %+v, want optional num with desc %q", count, "default: 3")
	}
	speed := findArg(t, task, "speed")
	if speed.Type != "num" || speed.Desc != "default: 1.5" {
		t.Errorf("speed arg = %+v, want optional num with desc %q", speed, "default: 1.5")
	}
	target := findArg(t, task, "target")
	if target.Type != "string" || !target.Required || target.Desc != "" {
		t.Errorf("target arg = %+v, want required bare string without desc", target)
	}
	wantWarnings(t, res)
}

const justFeatureSrc = `set shell := ["bash", "-c"]
export FOO := "bar"
JUSTVAR := "x"

# quiet recipe
quiet:
    @echo silent

[private]
secret:
    echo hidden

[script]
gen:
    #!/usr/bin/env bash
    echo a
    echo b
`

func TestJustFeatureWarnings(t *testing.T) {
	res := convert(t, "just", "justfile", justFeatureSrc)
	wantWarnings(t, res,
		"hire: not migrated: set directive (file justfile)",
		"hire: not migrated: export directive (file justfile)",
		"hire: not migrated: variable assignment JUSTVAR (file justfile)",
		"hire: not migrated: @ prefix (task quiet)",
		"hire: not migrated: [private] recipe \"secret\" (skipped)",
		"hire: not migrated: script recipe (task gen)",
	)
	quiet := findTask(t, res, "quiet")
	if quiet.Desc != "quiet recipe" {
		t.Errorf("quiet desc = %q", quiet.Desc)
	}
	if strings.Join(quiet.Cmds, "|") != "echo silent" {
		t.Errorf("quiet cmds = %q, want @ stripped", quiet.Cmds)
	}
	for _, task := range res.Tasks {
		if task.Name == "secret" {
			t.Error("private recipe should be skipped")
		}
	}
	gen := findTask(t, res, "gen")
	want := "#!/usr/bin/env bash\necho a\necho b"
	if strings.Join(gen.Cmds, "|") != want {
		t.Errorf("script recipe cmds = %q, want one joined script %q", gen.Cmds, want)
	}
}

const justPlaceholderSrc = `push force=false:
    echo {{force}}

weird:
    echo {{ext}} {{uppercase(x)}}

pack +files:
    echo {{files}}
`

func TestJustPlaceholderWarnings(t *testing.T) {
	res := convert(t, "just", "justfile", justPlaceholderSrc)
	wantWarnings(t, res,
		"hire: not migrated: bool arg \"force\" must use the conditional form {{force:+word}} (task push)",
		"hire: not migrated: just template {{ext}} kept literal (task weird)",
		"hire: not migrated: just template {{uppercase(x)}} kept literal (task weird)",
		"hire: not migrated: variadic parameter \"+files\" (task pack)",
	)
	files := findArg(t, findTask(t, res, "pack"), "files")
	if files.Type != "string" || !files.Required {
		t.Errorf("+files arg = %+v, want required string", files)
	}
}

func TestJustMalformed(t *testing.T) {
	wantErr(t, "just", "justfile", "", "no importable recipes")
	wantErr(t, "just", "justfile", "# only a comment\n", "no importable recipes")
	wantErr(t, "just", "justfile", "dangling:\n", `justfile:1: recipe "dangling" has no body`)
	wantErr(t, "just", "justfile", "echo without a colon\n", `justfile:1: unrecognized line`)
}

// just dependencies live after the colon (`final: hello`) and map to
// hire deps; `&&` subsequent dependencies have no hire equivalent.
func TestJustDeps(t *testing.T) {
	res := convert(t, "just", "justfile", `hello:
    echo hi

final: hello
    echo done

chained: hello && late
    echo chained
`)
	wantWarnings(t, res,
		"hire: not migrated: subsequent dependencies (&&) (task chained)",
	)
	final := findTask(t, res, "final")
	if strings.Join(final.Deps, "|") != "hello" {
		t.Errorf("final deps = %q, want [hello]", final.Deps)
	}
	chained := findTask(t, res, "chained")
	if strings.Join(chained.Deps, "|") != "hello" {
		t.Errorf("chained deps = %q, want prior deps only", chained.Deps)
	}
}

// --- make (ruling 1) ---

const makeSrc = `.PHONY: hello
CC = gcc

hello:
	echo "hello from make"

build: hello
	$(CC) -O2 -o app main.cpp
`

func TestMakeMapping(t *testing.T) {
	res := convert(t, "make", "Makefile", makeSrc)
	wantWarnings(t, res,
		`hire: not migrated: make $VAR reference "$(CC)" (task build)`,
		"hire: not migrated: make timestamp/up-to-date incremental semantics (task build)",
	)
	hello := findTask(t, res, "hello")
	if strings.Join(hello.Cmds, "|") != `echo "hello from make"` {
		t.Errorf("hello cmds = %q", hello.Cmds)
	}
	build := findTask(t, res, "build")
	if strings.Join(build.Deps, "|") != "hello" {
		t.Errorf("build deps = %q, want [hello]", build.Deps)
	}
	if strings.Join(build.Cmds, "|") != "$(CC) -O2 -o app main.cpp" {
		t.Errorf("build cmds = %q, want raw $VAR text kept", build.Cmds)
	}
}

func TestMakeMultiTargetAndAggregate(t *testing.T) {
	res := convert(t, "make", "Makefile", `.PHONY: hello all
hello:
	echo hi

copy-a copy-b: hello
	echo one

all: hello
`)
	wantWarnings(t, res,
		"hire: not migrated: make timestamp/up-to-date incremental semantics (task copy-a)",
		"hire: not migrated: make timestamp/up-to-date incremental semantics (task copy-b)",
		"hire: not migrated: make recipe-less target (echo no-op synthesized) (task all)",
	)
	for _, name := range []string{"copy-a", "copy-b"} {
		task := findTask(t, res, name)
		if strings.Join(task.Deps, "|") != "hello" || strings.Join(task.Cmds, "|") != "echo one" {
			t.Errorf("%s = deps %q cmds %q, want dep hello + echo one", name, task.Deps, task.Cmds)
		}
	}
	all := findTask(t, res, "all")
	if strings.Join(all.Cmds, "|") != "echo all" {
		t.Errorf("aggregate all cmds = %q, want synthesized echo", all.Cmds)
	}
}

func TestMakeEscapesAndDeps(t *testing.T) {
	res := convert(t, "make", "Makefile", `.PHONY: deploy clean
deploy:
	echo $$HOME

build: | dir
	echo b
`)
	wantWarnings(t, res,
		"hire: not migrated: make $$ escape (task deploy)",
		"hire: not migrated: make order-only prerequisites (task build)",
		"hire: not migrated: make timestamp/up-to-date incremental semantics (task build)",
	)
	build := findTask(t, res, "build")
	if strings.Join(build.Deps, "|") != "dir" {
		t.Errorf("build deps = %q, want order-only stripped", build.Deps)
	}
}

func TestMakeUnmigratedDirectives(t *testing.T) {
	res := convert(t, "make", "Makefile", `include other.mk
ifeq ($(OS),Windows)
.PHONY: hello
hello:
	echo hi
endif
`)
	wantWarnings(t, res,
		"hire: not migrated: make include directive (file Makefile)",
		"hire: not migrated: make directive ifeq (file Makefile)",
		"hire: not migrated: make directive endif (file Makefile)",
	)
}

func TestMakeMalformed(t *testing.T) {
	wantErr(t, "make", "Makefile", "\techo orphan\n", `Makefile:1: recipe line without a target`)
	wantErr(t, "make", "Makefile", "# just a comment\n", "no targets found")
	wantErr(t, "make", "Makefile", "this is not make\n", `Makefile:1: missing separator`)
}

// @ silent prefixes are stripped and warned (hire never echoes); the
// - ignore-error prefix is stripped and warned (a failure always stops
// the task, so the semantic cannot survive); backslash continuations
// merge into one command.
func TestMakePrefixesAndContinuations(t *testing.T) {
	res := convert(t, "make", "Makefile", `.PHONY: hello clean wrap
hello:
	@echo hi

clean:
	-rm -rf tmp

wrap:
	echo one \
	two
`)
	wantWarnings(t, res,
		"hire: not migrated: make '@' silent prefix (task hello)",
		"hire: not migrated: make '-' ignore-errors prefix (task clean)",
	)
	hello := findTask(t, res, "hello")
	if strings.Join(hello.Cmds, "|") != "echo hi" {
		t.Errorf("hello cmds = %q, want @ stripped", hello.Cmds)
	}
	clean := findTask(t, res, "clean")
	if strings.Join(clean.Cmds, "|") != "rm -rf tmp" {
		t.Errorf("clean cmds = %q, want - stripped", clean.Cmds)
	}
	wrap := findTask(t, res, "wrap")
	if strings.Join(wrap.Cmds, "|") != "echo one two" {
		t.Errorf("wrap cmds = %q, want continuation merged", wrap.Cmds)
	}
}

// --- go-task (ruling 3) ---

const gotaskSrc = `version: '3'
vars:
  GREETING: hi-from-vars
  N: 3
tasks:
  greet:
    desc: say hi
    cmds:
      - echo {{.GREETING}} {{.N}}
  stale:
    cmds: [echo x]
    sources: [a.cpp]
    generates: [a.o]
    dotenv: [.env]
    env:
      KEY: v
    preconditions:
      - sh: test -f a.cpp
  both:
    cmds: [echo y]
    platforms: [windows, linux]
  single:
    cmds: [echo z]
    platforms: [windows]
  taskcall:
    cmds:
      - task: greet
      - echo done
  dyn:
    cmds:
      - echo {{now | date "2006"}}
  simple:
    cmd: echo single
`

func TestGotaskMapping(t *testing.T) {
	res := convert(t, "task", "Taskfile.yml", gotaskSrc)
	wantWarnings(t, res,
		`hire: not migrated: dynamic template {{now | date "2006"}} kept literal (task dyn)`,
		"hire: not migrated: sources/generates freshness check (task stale)",
		"hire: not migrated: dotenv (task stale)",
		"hire: not migrated: env (task stale)",
		"hire: not migrated: preconditions (task stale)",
		"hire: not migrated: task call in cmds (task taskcall)",
	)

	greet := findTask(t, res, "greet")
	if greet.Desc != "say hi" {
		t.Errorf("greet desc = %q", greet.Desc)
	}
	if strings.Join(greet.Cmds, "|") != "echo hi-from-vars 3" {
		t.Errorf("greet cmds = %q, want vars baked in", greet.Cmds)
	}

	if got := strings.Join(findTask(t, res, "both").OS, ","); got != "windows,linux" {
		t.Errorf("both os = %q, want array preserved", got)
	}
	if got := strings.Join(findTask(t, res, "single").OS, ","); got != "windows" {
		t.Errorf("single os = %q, want windows", got)
	}
	if got := strings.Join(findTask(t, res, "taskcall").Cmds, "|"); got != "echo done" {
		t.Errorf("taskcall cmds = %q, want task call skipped", got)
	}
	if got := strings.Join(findTask(t, res, "simple").Cmds, "|"); got != "echo single" {
		t.Errorf("simple cmds = %q, want single cmd form", got)
	}
}

func TestGotaskDepsWarningOnce(t *testing.T) {
	res := convert(t, "task", "Taskfile.yml", `version: '3'
tasks:
  a:
    cmds: [echo a]
  b:
    deps: [a]
    cmds: [echo b]
  c:
    deps: [a, b]
    cmds: [echo c]
`)
	wantWarnings(t, res,
		"hire: not migrated: go-task runs deps concurrently; hire runs them sequentially in listed order (file Taskfile.yml)",
	)
	b := findTask(t, res, "b")
	if strings.Join(b.Deps, "|") != "a" {
		t.Errorf("b deps = %q", b.Deps)
	}
	c := findTask(t, res, "c")
	if strings.Join(c.Deps, "|") != "a|b" {
		t.Errorf("c deps = %q", c.Deps)
	}
}

func TestGotaskTopLevelFeatures(t *testing.T) {
	res := convert(t, "task", "Taskfile.yml", `includes:
  x: ./other.yml
tasks:
  a:
    cmds: [echo hi]
`)
	wantWarnings(t, res,
		"hire: not migrated: includes (file Taskfile.yml)",
	)
}

func TestGotaskMalformed(t *testing.T) {
	wantErr(t, "task", "Taskfile.yml", "tasks:\n  - broken: [unclosed\n", "invalid YAML")
	wantErr(t, "task", "Taskfile.yml", "version: '3'\n", `no "tasks" key`)
}

// --- emitter: key order, escaping, loadability (rulings 4-5 of the brief) ---

func TestEmitKeyOrderAndRoundtrip(t *testing.T) {
	res := convert(t, "just", "justfile", justTypedSrc)
	out := string(importer.Emit(res, "justfile"))

	// The Task 2 review rule: scalar/array keys must precede any
	// [tasks.x.args.*] sub-table header, and every task header reopens
	// its own table before its args.
	if i, j := strings.Index(out, "[tasks.greet]"), strings.Index(out, "[tasks.greet.args.name]"); i < 0 || j < 0 || i > j {
		t.Fatalf("emitted TOML task header must precede its args sub-table:\n%s", out)
	}
	for _, key := range []string{"desc = ", "cmds = "} {
		if k := strings.Index(out, key); k < 0 || k > strings.Index(out, "[tasks.greet.args.name]") {
			t.Errorf("key %q must come before the args sub-table header\n%s", key, out)
		}
	}

	// BurntSushi must decode the emitted text back to the source cmds.
	var dec struct {
		Tasks map[string]struct {
			Desc string   `toml:"desc"`
			Cmds []string `toml:"cmds"`
		} `toml:"tasks"`
	}
	if err := toml.Unmarshal([]byte(out), &dec); err != nil {
		t.Fatalf("emitted TOML does not decode: %v\n%s", err, out)
	}
	if got := dec.Tasks["greet"].Cmds; len(got) != 2 || got[0] != "echo {{name}} {{count}} {{speed}} {{target}}" || got[1] != "echo done" {
		t.Errorf("roundtrip cmds = %q", got)
	}
	if dec.Tasks["greet"].Desc != "greet someone" {
		t.Errorf("roundtrip desc = %q", dec.Tasks["greet"].Desc)
	}

	// And the real loader must accept it end to end.
	dir := t.TempDir()
	path := filepath.Join(dir, "Taskfile.toml")
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := adapter.LoadTOML(path)
	if err != nil {
		t.Fatalf("LoadTOML on imported output: %v\n%s", err, out)
	}
	defer f.Close()
	if _, ok := f.Tasks["greet"]; !ok {
		t.Fatalf("greet not loadable from imported output:\n%s", out)
	}
}

func TestEmitEscapes(t *testing.T) {
	res := convert(t, "just", "justfile", `tricky:
    echo "he said \"hi\" \\ backslash 中文"
`)
	out := string(importer.Emit(res, "justfile"))
	var dec struct {
		Tasks map[string]struct {
			Cmd string `toml:"cmd"`
		} `toml:"tasks"`
	}
	if err := toml.Unmarshal([]byte(out), &dec); err != nil {
		t.Fatalf("emitted TOML does not decode: %v\n%s", err, out)
	}
	want := `echo "he said \"hi\" \\ backslash 中文"`
	if got := dec.Tasks["tricky"].Cmd; got != want {
		t.Errorf("escaped roundtrip = %q, want %q\n%s", got, want, out)
	}
}

func TestEmitWhenOSForms(t *testing.T) {
	res := convert(t, "task", "Taskfile.yml", gotaskSrc)
	out := string(importer.Emit(res, "Taskfile.yml"))
	if !strings.Contains(out, `when = { os = "windows" }`) {
		t.Errorf("single platform must emit scalar os:\n%s", out)
	}
	if !strings.Contains(out, `os = ["windows", "linux"]`) {
		t.Errorf("multi platform must emit os array:\n%s", out)
	}
}
