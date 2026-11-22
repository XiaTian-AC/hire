package importer

import (
	"fmt"
	"path/filepath"
	"strings"
)

// makeDirectives are column-0 keywords that change make's behavior but
// produce nothing importable; each gets one warning line.
var makeDirectives = map[string]bool{
	"include": true, "-include": true, "sinclude": true,
	"ifeq": true, "ifneq": true, "ifdef": true, "ifndef": true,
	"else": true, "endif": true, "define": true, "endef": true,
	"export": true, "unexport": true, "override": true, "undefine": true,
	"vpath": true,
}

// mkGroup is one target rule while its tab recipe is being collected.
type mkGroup struct {
	names      []string // multi-target lines expand into one task per name
	deps       []string
	cmds       []string
	orderOnly  bool     // `|` prerequisites present
	varDeps    []string // $(...)-shaped prerequisites, dropped
	skip       bool     // .PHONY-style or pattern rule: recipes consumed, no tasks
	atWarned   bool     // '@' silent prefix seen in this recipe
	dashWarned bool     // '-' ignore-errors prefix seen in this recipe
	plusWarned bool     // '+' recursive prefix seen in this recipe
}

// parseMake converts a Makefile per controller ruling 1: targets and
// their tab recipes become cmds, prerequisites become deps, .PHONY is
// ignored, multi-target lines expand, and $VAR/$$/timestamp semantics,
// directives and pattern rules warn. Malformed input (orphan recipes,
// missing separators, no targets) errors naming file and line.
func parseMake(path string, src []byte) (*Result, error) {
	base := filepath.Base(path)
	var res Result
	lines := strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n")

	// .PHONY may appear after the targets it names, so collect it first.
	phony := map[string]bool{}
	for _, ln := range lines {
		if t := strings.TrimSpace(ln); strings.HasPrefix(t, ".PHONY:") {
			for _, f := range strings.Fields(t[len(".PHONY:"):]) {
				phony[f] = true
			}
		}
	}

	idx := map[string]int{} // target name -> index in res.Tasks; redefinition overwrites in place
	var cur *mkGroup
	var pending string // backslash continuation: command text awaiting its tail

	flush := func() {
		g := cur
		cur = nil
		if g == nil || g.skip || len(g.names) == 0 {
			return
		}
		for _, name := range g.names {
			cmds := g.cmds
			if len(cmds) == 0 && len(g.deps) > 0 {
				// make allows recipe-less aggregate targets; hire needs a
				// command, so the deps do the work behind a no-op echo.
				cmds = []string{"echo " + name}
				warnf(&res.Warnings, "make recipe-less target (echo no-op synthesized) (task %s)", name)
			}
			if len(cmds) == 0 {
				continue // an inert target: nothing to run, nothing to import
			}
			if g.orderOnly {
				warnf(&res.Warnings, "make order-only prerequisites (task %s)", name)
			}
			for _, tok := range g.varDeps {
				warnf(&res.Warnings, "make $VAR prerequisite %q dropped (task %s)", tok, name)
			}
			if ref := firstMakeVar(cmds); ref != "" {
				warnf(&res.Warnings, "make $VAR reference %q (task %s)", ref, name)
			}
			if hasDoubleDollar(cmds) {
				warnf(&res.Warnings, "make $$ escape (task %s)", name)
			}
			if !phony[name] && !strings.HasPrefix(name, ".") {
				// A file target only rebuilds when out of date; hire always
				// runs. Nothing to migrate, so the semantics warn.
				warnf(&res.Warnings, "make timestamp/up-to-date incremental semantics (task %s)", name)
			}
			task := Task{Name: name, Cmds: cmds, Deps: g.deps}
			if i, ok := idx[name]; ok {
				res.Tasks[i] = task // make redefines targets; last rule wins
			} else {
				idx[name] = len(res.Tasks)
				res.Tasks = append(res.Tasks, task)
			}
		}
	}

	for i, raw := range lines {
		ln := strings.TrimSuffix(raw, "\r")
		no := i + 1
		if strings.HasPrefix(ln, "\t") {
			if cur == nil {
				return nil, fmt.Errorf("%s:%d: recipe line without a target", base, no)
			}
			if cur.skip {
				continue
			}
			text := strings.TrimSpace(ln[1:])
			if pending != "" {
				text = strings.TrimSpace(pending + " " + text)
				pending = ""
			}
			if text == "" {
				continue // tab-only line: not a command
			}
			if strings.HasSuffix(text, "\\") {
				pending = strings.TrimRight(strings.TrimSuffix(text, "\\"), " \t")
				continue
			}
			// '@' silences make's echo (hire never echoes: strip),
			// '-' ignores errors (a failure always stops the task: strip)
			// and '+' forces recursion under make -n (hire has no dry
			// run: strip). None of the three semantics survives; each
			// warns once per task.
			for strings.HasPrefix(text, "@") || strings.HasPrefix(text, "-") || strings.HasPrefix(text, "+") {
				if strings.HasPrefix(text, "@") && !cur.atWarned {
					cur.atWarned = true
					warnf(&res.Warnings, "make '@' silent prefix (task %s)", cur.names[0])
				}
				if strings.HasPrefix(text, "-") && !cur.dashWarned {
					cur.dashWarned = true
					warnf(&res.Warnings, "make '-' ignore-errors prefix (task %s)", cur.names[0])
				}
				if strings.HasPrefix(text, "+") && !cur.plusWarned {
					cur.plusWarned = true
					warnf(&res.Warnings, "make '+' recursive prefix (task %s)", cur.names[0])
				}
				text = text[1:]
			}
			if text != "" {
				cur.cmds = append(cur.cmds, text)
			}
			continue
		}
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if w := strings.Fields(t)[0]; makeDirectives[w] {
			if w == "include" || w == "-include" || w == "sinclude" {
				warnf(&res.Warnings, "make include directive (file %s)", base)
			} else {
				warnf(&res.Warnings, "make directive %s (file %s)", w, base)
			}
			continue
		}
		// Assignment: `=` before any target `:` (covers =, :=, ?=, +=).
		if eq := strings.IndexByte(t, '='); eq >= 0 {
			if c := strings.IndexByte(t, ':'); c < 0 || eq < c || eq == c+1 {
				continue // variable definition; recipe references warn per task
			}
		}
		colon := strings.IndexByte(t, ':')
		if colon < 0 {
			return nil, fmt.Errorf("%s:%d: missing separator %q", base, no, t)
		}
		names := strings.Fields(t[:colon])
		if len(names) == 0 {
			return nil, fmt.Errorf("%s:%d: missing separator %q", base, no, t)
		}
		rest := t[colon+1:]
		if strings.HasPrefix(rest, ":") { // double-colon rule
			rest = rest[1:]
		}
		if pending != "" && cur != nil && !cur.skip {
			cur.cmds = append(cur.cmds, pending) // dangling continuation
		}
		pending = ""
		flush()
		if strings.HasPrefix(names[0], ".") {
			// .PHONY and the other special targets: ignored per ruling 1,
			// stray recipes consumed.
			cur = &mkGroup{skip: true}
			continue
		}
		if strings.Contains(names[0], "%") {
			warnf(&res.Warnings, "make pattern rule %q (file %s)", names[0], base)
			cur = &mkGroup{skip: true}
			continue
		}
		g := &mkGroup{names: names}
		if semi := strings.IndexByte(rest, ';'); semi >= 0 {
			if inline := strings.TrimSpace(rest[semi+1:]); inline != "" {
				g.cmds = append(g.cmds, inline)
				warnf(&res.Warnings, "make same-line recipe (task %s)", names[0])
			}
			rest = rest[:semi]
		}
		for _, d := range strings.Fields(rest) {
			switch {
			case strings.HasPrefix(d, "|"):
				g.orderOnly = true
			case strings.HasPrefix(d, "$"):
				g.varDeps = append(g.varDeps, d)
			case strings.HasPrefix(d, "."):
				// special targets as prerequisites never run
			default:
				g.deps = append(g.deps, d)
			}
		}
		cur = g
	}
	if pending != "" && cur != nil && !cur.skip {
		cur.cmds = append(cur.cmds, pending) // file ends mid-continuation
	}
	flush()
	if len(res.Tasks) == 0 {
		return nil, fmt.Errorf("%s: no targets found", base)
	}
	return &res, nil
}

// firstMakeVar returns the first $(VAR)/${VAR}/$@-style reference in the
// recipes, skipping $$ (that is the escaped-literal feature, warned
// separately) so it never misfires on it.
func firstMakeVar(cmds []string) string {
	for _, c := range cmds {
		for i := 0; i < len(c); i++ {
			if c[i] != '$' {
				continue
			}
			if i+1 < len(c) && c[i+1] == '$' {
				i++
				continue
			}
			if i+1 < len(c) && c[i+1] == '(' {
				if j := strings.IndexByte(c[i:], ')'); j > 0 {
					return c[i : i+j+1]
				}
			}
			if i+1 < len(c) && c[i+1] == '{' {
				if j := strings.IndexByte(c[i:], '}'); j > 0 {
					return c[i : i+j+1]
				}
			}
			if i+1 < len(c) {
				return c[i : i+2] // auto vars ($@ $< $^) and one-letter vars
			}
		}
	}
	return ""
}

func hasDoubleDollar(cmds []string) bool {
	for _, c := range cmds {
		if strings.Contains(c, "$$") {
			return true
		}
	}
	return false
}
