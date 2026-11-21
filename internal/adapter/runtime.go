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

// RuntimeSpec configures one external runtime in config.lua. Exactly one
// field is set: Cmd for interpreted languages (the taskfile is an
// argument to the command), Build for compiled ones (a shell template
// with {out}/{src} placeholders producing the program to run).
type RuntimeSpec struct {
	Cmd   string // e.g. "python3"
	Build string // e.g. "g++ -std=c++20 -O2 -o {out} {src}"
}

// extRuntimes maps taskfile extensions to runtime names. .mjs and .cc
// are reachable only via --taskfile, matching the lookup chain.
var extRuntimes = map[string]string{
	".py":  "python",
	".js":  "node",
	".mjs": "node",
	".cpp": "cpp",
	".cc":  "cpp",
}

// ResolveRuntime maps a taskfile extension to its runtime spec and
// registry name (the name also keys the compile-cache directory).
func ResolveRuntime(ext string, rts map[string]RuntimeSpec) (RuntimeSpec, string, error) {
	e := strings.TrimPrefix(strings.ToLower(ext), ".") // accept ".py" and "py"
	name, ok := extRuntimes["."+e]
	if !ok {
		return RuntimeSpec{}, "", fmt.Errorf("no runtime for extension %q (known: .py .js .mjs .cpp .cc; %s)", ext, registeredList(rts))
	}
	spec, ok := rts[name]
	if !ok {
		return RuntimeSpec{}, "", fmt.Errorf("runtime %q for %q files is not configured in config.lua runtimes (known: .py .js .mjs .cpp .cc; %s)", name, ext, registeredList(rts))
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
// shortcut: the cache is never pruned (disk grows with distinct source
// contents + build strings); add a `hire cache clean` when it matters.
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
	if st, err := os.Stat(exe); err == nil && !st.IsDir() {
		return exe, nil
	}
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		return "", err
	}
	sh, err := shell.Resolve("", nil)
	if err != nil {
		return "", fmt.Errorf("%s: build: %v", path, err)
	}
	cmdStr := expandBuild(spec.Build, exe, path)
	build := exec.Command(sh.Path, sh.Argv(cmdStr)...)
	var out bytes.Buffer
	build.Stdout = &out
	build.Stderr = &out
	if err := build.Run(); err != nil {
		return "", fmt.Errorf("%s: build failed: %v\ncommand: %s\n%s", path, err, cmdStr, strings.TrimSpace(out.String()))
	}
	if _, err := os.Stat(exe); err != nil {
		return "", fmt.Errorf("%s: build command did not produce the output file\ncommand: %s\n%s", path, cmdStr, strings.TrimSpace(out.String()))
	}
	return exe, nil
}

// expandBuild substitutes {out}/{src} with double-quoted paths so
// spaces and non-ASCII characters survive every supported shell.
func expandBuild(tmpl, out, src string) string {
	return strings.NewReplacer("{out}", `"`+out+`"`, "{src}", `"`+src+`"`).Replace(tmpl)
}
