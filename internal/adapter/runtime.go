package adapter

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"hire/internal/shell"
)

// RuntimeSpec configures one external runtime in config.lua. Cmd is set
// for interpreted languages (the taskfile is an argument to the command),
// Build for compiled ones (a shell template with {out}/{src} placeholders
// producing the program to run); Ext optionally declares extra taskfile
// extensions the runtime owns beyond the builtin map (e.g. {".ts"});
// Parser names the clang command used to parse function-form C++
// taskfiles (e.g. "clang++"; empty = probe clang++ then clang on PATH).
type RuntimeSpec struct {
	Cmd    string   // e.g. "python3"
	Build  string   // e.g. "g++ -std=c++20 -O2 -o {out} {src}"
	Ext    []string // e.g. {".ts"} — lowercased, leading dot
	Parser string   // e.g. "clang++" — C++ function-form AST parser
}

// extRuntimes maps taskfile extensions to runtime names. .mjs, .ts and
// .cc are reachable only via --taskfile, matching the lookup chain.
var extRuntimes = map[string]string{
	".py":  "python",
	".js":  "node",
	".mjs": "node",
	".ts":  "ts",
	".cpp": "cpp",
	".cc":  "cpp",
}

// defaultSpec returns the out-of-the-box runtime for a builtin language
// name: config.lua entries override these, so the preset languages work
// without any configuration. Python probes python3 first (the Linux
// habit) and falls back to python (the Windows habit).
func defaultSpec(name string) (RuntimeSpec, bool) {
	switch name {
	case "python":
		if _, err := exec.LookPath("python3"); err == nil {
			return RuntimeSpec{Cmd: "python3"}, true
		}
		return RuntimeSpec{Cmd: "python"}, true
	case "node", "ts":
		// node 23+ strips TypeScript types natively; older node fails
		// with its own clear message, and config.lua can point ts at
		// tsx or bun instead.
		return RuntimeSpec{Cmd: "node"}, true
	case "cpp":
		return RuntimeSpec{Build: "g++ -std=c++20 -O2 -o {out} {src}"}, true
	}
	return RuntimeSpec{}, false
}

// ResolveRuntime maps a taskfile extension to its runtime spec and
// registry name (the name also keys the compile-cache directory).
// Builtin names fall back to defaultSpec when config.lua does not
// register them, so the preset languages work out of the box.
func ResolveRuntime(ext string, rts map[string]RuntimeSpec) (RuntimeSpec, string, error) {
	e := "." + strings.TrimPrefix(strings.ToLower(ext), ".") // accept ".py" and "py"
	name, ok := extRuntimes[e]
	if !ok {
		// Custom-ext runtimes declared in config.lua, e.g.
		// ts = { cmd = "node", ext = ".mts" }. When several runtimes
		// claim the same extension, the alphabetically first wins so
		// resolution stays deterministic.
		var claiming []string
		for n, spec := range rts {
			for _, x := range spec.Ext {
				if strings.ToLower(x) == e {
					claiming = append(claiming, n)
					break
				}
			}
		}
		if len(claiming) > 0 {
			sort.Strings(claiming)
			return rts[claiming[0]], claiming[0], nil
		}
		return RuntimeSpec{}, "", fmt.Errorf("no runtime for extension %q (known: .py .js .mjs .ts .cpp .cc; custom extensions are declared with ext = \"...\" in a config.lua runtime; %s)", ext, registeredList(rts))
	}
	spec, ok := rts[name]
	if !ok {
		if d, has := defaultSpec(name); has {
			return d, name, nil
		}
		return RuntimeSpec{}, "", fmt.Errorf("runtime %q for %q files is not configured in config.lua runtimes (known: .py .js .mjs .ts .cpp .cc; %s)", name, ext, registeredList(rts))
	}
	return spec, name, nil
}

func registeredList(rts map[string]RuntimeSpec) string {
	if len(rts) == 0 {
		return "registered runtimes: none (add runtimes = { ... } to config.lua)"
	}
	names := make([]string, 0, len(rts))
	for n := range rts {
		names = append(names, n)
	}
	sort.Strings(names)
	return "registered runtimes: " + strings.Join(names, ", ")
}

// cacheKey is the compile-cache identity: the source content plus the
// build template, NUL-separated. Changing either gives a new key.
func cacheKey(src []byte, build string) string {
	h := sha256.New()
	h.Write(src)
	h.Write([]byte{0})
	h.Write([]byte(build))
	return hex.EncodeToString(h.Sum(nil))
}

// cacheBaseDir prefers XDG_CACHE_HOME (settable per machine and in
// tests) and falls back to the platform cache dir (LOCALAPPDATA on
// Windows, ~/.cache elsewhere).
func cacheBaseDir() (string, error) {
	if v := os.Getenv("XDG_CACHE_HOME"); v != "" {
		return v, nil
	}
	return os.UserCacheDir()
}

// ensureCompiled returns the built taskfile program, running the build
// template through the user's default shell on a cache miss. Compile
// failures carry the compiler's full output.
func ensureCompiled(path, rtName string, spec RuntimeSpec) (string, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	base, err := cacheBaseDir()
	if err != nil {
		return "", err
	}
	exe := filepath.Join(base, "hire", rtName, cacheKey(src, spec.Build)+".exe")
	return buildIfMissing(exe, path, spec.Build, path)
}

// buildIfMissing stats exePath and, on a miss, runs the build template
// through the user's default shell. srcForCmd is the {src} argument —
// the user's taskfile on the raw contract path, hire's generated wrapper
// for the C++ function form; displayPath names the taskfile in errors.
// shortcut: the cache is never pruned (disk grows with distinct source
// contents + build strings); add a `hire cache clean` when it matters.
func buildIfMissing(exePath, srcForCmd, build, displayPath string) (string, error) {
	if st, err := os.Stat(exePath); err == nil && !st.IsDir() {
		return exePath, nil
	}
	if err := os.MkdirAll(filepath.Dir(exePath), 0o755); err != nil {
		return "", err
	}
	sh, err := shell.Resolve("", nil)
	if err != nil {
		return "", fmt.Errorf("%s: build: %v", displayPath, err)
	}
	cmdStr := expandBuild(build, exePath, srcForCmd)
	buildCmd := exec.Command(sh.Path, sh.Argv(cmdStr)...)
	var out bytes.Buffer
	buildCmd.Stdout = &out
	buildCmd.Stderr = &out
	if err := buildCmd.Run(); err != nil {
		// A possibly-created partial exe must not become a cache hit:
		// the next load would silently execute the corrupt binary.
		_ = os.Remove(exePath)
		return "", fmt.Errorf("%s: build failed: %v\ncommand: %s\n%s", displayPath, err, cmdStr, strings.TrimSpace(out.String()))
	}
	if _, err := os.Stat(exePath); err != nil {
		return "", fmt.Errorf("%s: build command did not produce the output file\ncommand: %s\n%s", displayPath, cmdStr, strings.TrimSpace(out.String()))
	}
	return exePath, nil
}

// expandBuild substitutes {out}/{src} with double-quoted paths so
// spaces and non-ASCII characters survive every supported shell.
func expandBuild(tmpl, out, src string) string {
	return strings.NewReplacer("{out}", `"`+out+`"`, "{src}", `"`+src+`"`).Replace(tmpl)
}
