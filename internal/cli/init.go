package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"hire/internal/adapter"
	"hire/internal/config"
)

// initLangs maps --lang values to the generated file and its template.
var initLangs = map[string]struct{ file, tmpl string }{
	"lua":    {"Taskfile.lua", adapter.LuaTemplate},
	"python": {"Taskfile.py", adapter.PythonTemplate},
	"node":   {"Taskfile.js", adapter.NodeTemplate},
	"cpp":    {"Taskfile.cpp", adapter.CppTemplate},
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
	if fileExists(spec.file) {
		fmt.Fprintf(d.Stderr, "hire: %s already exists; hire init does not overwrite\n", spec.file)
		return 1
	}
	if err := os.WriteFile(spec.file, []byte(spec.tmpl), 0o644); err != nil {
		fmt.Fprintf(d.Stderr, "hire: %v\n", err)
		return 1
	}
	fmt.Fprintf(d.Stdout, "created %s - try: hire hello\n", spec.file)
	if lang != "lua" {
		printRuntimeHint(lang, d)
	}
	return 0
}

// printRuntimeHint tells the user the generated file cannot run until
// config.lua declares its runtime.
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
	fmt.Fprintf(d.Stderr, "hire: note: running it needs a runtimes entry in %s\n  return {\n    runtimes = {\n      %s\n    },\n  }\n",
		filepath.Join(cfgDir, "config.lua"), snippet)
}
