package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"hire/internal/importer"
)

const importUsage = "hire: usage: hire import [--from just|make|task] [--force] <file>"

// cmdImport implements `hire import`: convert a justfile / Makefile /
// Taskfile.yml into a pure-static Taskfile.toml next to the source,
// printing one "hire: not migrated:" stderr line per unmapped feature.
// The output goes to the source file's directory, never the config
// dir; an existing Taskfile.toml is refused unless --force is set.
func cmdImport(argv []string, d Deps) int {
	from, force, file := "", false, ""
	for i := 0; i < len(argv); i++ {
		switch {
		case argv[i] == "--force":
			force = true
		case argv[i] == "--from" || strings.HasPrefix(argv[i], "--from="):
			if strings.HasPrefix(argv[i], "--from=") {
				from = strings.TrimPrefix(argv[i], "--from=")
				continue
			}
			if i+1 >= len(argv) {
				fmt.Fprintln(d.Stderr, importUsage)
				return 2
			}
			from = argv[i+1]
			i++
		case file == "":
			file = argv[i]
		default:
			fmt.Fprintln(d.Stderr, importUsage)
			return 2
		}
	}
	if file == "" {
		fmt.Fprintln(d.Stderr, importUsage)
		return 2
	}
	switch from {
	case "", "just", "make", "task":
	default:
		fmt.Fprintf(d.Stderr, "hire: unknown --from %q (use: just, make, task)\n", from)
		return 2
	}
	src, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintf(d.Stderr, "hire: %v\n", err)
		return 1
	}
	format := from
	if format == "" {
		format, err = importer.Sniff(file, src)
		if err != nil {
			fmt.Fprintf(d.Stderr, "hire: %v\n", err)
			return 1
		}
	}
	res, err := importer.Import(format, file, src)
	if err != nil {
		fmt.Fprintf(d.Stderr, "hire: %v\n", err)
		return 1
	}
	out := filepath.Join(filepath.Dir(file), "Taskfile.toml")
	if !force && fileExists(out) {
		fmt.Fprintf(d.Stderr, "hire: %s already exists; use --force to overwrite\n", out)
		return 1
	}
	if err := os.WriteFile(out, importer.Emit(res, file), 0o644); err != nil {
		fmt.Fprintf(d.Stderr, "hire: %v\n", err)
		return 1
	}
	for _, w := range res.Warnings {
		fmt.Fprintln(d.Stderr, w)
	}
	fmt.Fprintf(d.Stdout, "wrote %s (%d tasks)\n", out, len(res.Tasks))
	return 0
}
