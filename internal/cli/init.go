package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"hire/internal/adapter"
	"hire/internal/config"
)

// initFile is one generated file for a --lang value.
type initFile struct{ file, tmpl string }

// initLangs maps --lang values to the generated files. The cpp runtime
// generates two: hire.hpp (the contract SDK) plus the taskfile that
// includes it.
var initLangs = map[string][]initFile{
	"lua":    {{"Taskfile.lua", adapter.LuaTemplate}},
	"python": {{"Taskfile.py", adapter.PythonTemplate}},
	"node":   {{"Taskfile.js", adapter.NodeTemplate}},
	"cpp":    {{"hire.hpp", adapter.CppHeaderTemplate}, {"Taskfile.cpp", adapter.CppTemplate}},
}

// cmdInit implements `hire init [--lang lua|python|node|cpp]`: write a
// runnable starter taskfile into the working directory. Generation never
// depends on the toolchain being installed — the tests bootstrap the
// generated files through the normal load path instead.
func cmdInit(argv []string, d Deps) int {
	lang := "lua"
	for i := 0; i < len(argv); i++ {
		switch {
		case argv[i] == "--lang" && i+1 < len(argv):
			lang = argv[i+1]
			i++
		case strings.HasPrefix(argv[i], "--lang="):
			lang = strings.TrimPrefix(argv[i], "--lang=")
		default:
			fmt.Fprintln(d.Stderr, "hire: usage: hire init [--lang lua|python|node|cpp]")
			return 2
		}
	}
	spec, ok := initLangs[lang]
	if !ok {
		fmt.Fprintf(d.Stderr, "hire: unknown --lang %q (use: lua, python, node, cpp)\n", lang)
		return 2
	}
	// Per-file: keep what exists, create what is missing - a scaffold
	// must never overwrite, but a stale sibling must not block the
	// missing one either.
	created := 0
	for _, f := range spec {
		if fileExists(f.file) {
			fmt.Fprintf(d.Stderr, "hire: %s already exists; keeping it\n", f.file)
			continue
		}
		if err := os.WriteFile(f.file, []byte(f.tmpl), 0o644); err != nil {
			fmt.Fprintf(d.Stderr, "hire: %v\n", err)
			return 1
		}
		fmt.Fprintf(d.Stdout, "created %s\n", f.file)
		created++
	}
	if created == 0 {
		fmt.Fprintf(d.Stderr, "hire: nothing to create (all files exist)\n")
		return 1
	}
	fmt.Fprintf(d.Stdout, "try: hire hello\n")
	if lang != "lua" {
		printRuntimeHint(lang, d)
	}
	// A .cpp taskfile in a Go module root breaks go build (the Go
	// toolchain rejects stray C++ sources in a package directory).
	if lang == "cpp" && fileExists("go.mod") {
		fmt.Fprintf(d.Stderr, "hire: note: a .cpp taskfile in a Go module root breaks go build - keep taskfiles in a subdirectory\n")
	}
	return 0
}

// printRuntimeHint tells the user the preset defaults apply and where
// to override them.
func printRuntimeHint(lang string, d Deps) {
	cfgDir, err := config.ConfigDir()
	if err != nil {
		cfgDir = "<config dir>"
	}
	var snippet string
	switch lang {
	case "python":
		snippet = "python = \"python\",   -- or \"python3\""
	case "node":
		snippet = "node   = \"node\","
	case "cpp":
		snippet = "cpp    = { build = \"g++ -std=c++20 -O2 -o {out} {src}\" },"
	}
	fmt.Fprintf(d.Stderr, "hire: note: preset runtimes work out of the box; to override, add runtimes to %s\n  return {\n    runtimes = {\n      %s\n    },\n  }\n",
		filepath.Join(cfgDir, "config.lua"), snippet)
}
