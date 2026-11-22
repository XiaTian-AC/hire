package importer

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// justAssignRe matches a justfile variable assignment (NAME := v).
var justAssignRe = regexp.MustCompile(`^([a-zA-Z_][a-zA-Z0-9_-]*)\s*:?=\s*(.*)$`)

// justTmplRe finds every {{...}} occurrence in recipe bodies.
var justTmplRe = regexp.MustCompile(`\{\{([^{}]*)\}\}`)

// justCondRe classifies the conditional form {{name:+word}}.
var justCondRe = regexp.MustCompile(`^([a-zA-Z_][a-zA-Z0-9_-]*)\s*:\+`)

// justIdentRe classifies plain identifiers (allowing the {{ .x }} dot form).
var justIdentRe = regexp.MustCompile(`^\.?([a-zA-Z_][a-zA-Z0-9_-]*)$`)

var justDirectiveRe = regexp.MustCompile(`^(import|mod|use|alias)\s`)

// justRec is a recipe while its body is still being collected.
type justRec struct {
	name    string
	line    int
	desc    string
	args    []Arg
	deps    []string
	body    []string
	attrs   []string
	atSeen  bool // @ on the signature or a body line
	script  bool // shebang or [script]: body is one joined command
	skip    bool // [private]
	hadBody bool
}

// parseJust converts a justfile per controller ruling 2: recipes to
// cmds, `# desc` comments to desc, parameters to args (bare name =
// required string, "text"/true-false/numeric default = optional
// string/bool/num with a "default: X" desc hint), [private] recipes
// skipped with a warning, and @/[script]/export/set/assignments and
// un-mappable {{...}} placeholders warned.
func parseJust(path string, src []byte) (*Result, error) {
	base := filepath.Base(path)
	var res Result
	var comment, attrs []string
	var cur *justRec

	// finalize turns the pending recipe into a task, a skip warning, or
	// a malformed-input error naming the signature line.
	finalize := func() error {
		r := cur
		cur = nil
		if r == nil {
			return nil
		}
		if !r.hadBody {
			return fmt.Errorf("%s:%d: recipe %q has no body", base, r.line, r.name)
		}
		if r.skip {
			warnf(&res.Warnings, "[private] recipe %q (skipped)", r.name)
			return nil
		}
		var cmds []string
		for _, ln := range r.body {
			t := strings.TrimSpace(ln)
			if t == "" {
				continue
			}
			if len(cmds) == 0 && strings.HasPrefix(t, "#!") {
				r.script = true
			}
			cmds = append(cmds, t)
		}
		if r.script {
			warnf(&res.Warnings, "script recipe (task %s)", r.name)
			cmds = []string{strings.Join(cmds, "\n")}
		}
		if len(cmds) == 0 {
			return fmt.Errorf("%s:%d: recipe %q has no body", base, r.line, r.name)
		}
		if r.atSeen {
			warnf(&res.Warnings, "@ prefix (task %s)", r.name)
		}
		justPlaceholders(r, cmds, &res.Warnings)
		res.Tasks = append(res.Tasks, Task{Name: r.name, Desc: r.desc, Cmds: cmds, Args: r.args, Deps: r.deps})
		return nil
	}

	lines := strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n")
	for i, raw := range lines {
		ln := strings.TrimSuffix(raw, "\r")
		no := i + 1
		indented := ln != "" && (ln[0] == ' ' || ln[0] == '\t')
		if cur != nil && (ln == "" || indented) {
			// recipe body: indented lines (blanks continue it)
			if t := strings.TrimSpace(ln); t != "" {
				text := strings.TrimLeft(ln, " \t")
				if strings.HasPrefix(text, "@") {
					text = strings.TrimPrefix(text, "@")
					cur.atSeen = true
				}
				cur.body = append(cur.body, text)
				cur.hadBody = true
			}
			continue
		}
		if cur != nil {
			// a column-0 non-blank line ends the open recipe
			if err := finalize(); err != nil {
				return nil, err
			}
		}
		if ln == "" {
			comment, attrs = nil, nil
			continue
		}
		if strings.HasPrefix(ln, "#!") {
			continue // file-level shebang
		}
		if strings.HasPrefix(ln, "#") {
			comment = append(comment, strings.TrimPrefix(strings.TrimPrefix(ln, "#"), " "))
			continue
		}
		if strings.HasPrefix(ln, "[") && strings.HasSuffix(strings.TrimSpace(ln), "]") {
			attrs = append(attrs, strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(ln, "["), "]")))
			continue
		}
		if sig := justSigRe.FindStringSubmatch(ln); sig != nil && !strings.HasPrefix(strings.TrimSpace(sig[4]), "=") {
			r, err := justRecipe(base, no, sig, comment, attrs, &res.Warnings)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: %v", base, no, err)
			}
			cur = r
			comment, attrs = nil, nil
			continue
		}
		body := ln
		switch {
		case strings.HasPrefix(body, "set ") || strings.HasPrefix(body, "set\t"):
			warnf(&res.Warnings, "set directive (file %s)", base)
		case strings.HasPrefix(body, "export "):
			warnf(&res.Warnings, "export directive (file %s)", base)
		case justDirectiveRe.MatchString(body):
			warnf(&res.Warnings, "directive %s (file %s)", strings.Fields(body)[0], base)
		default:
			if m := justAssignRe.FindStringSubmatch(body); m != nil {
				warnf(&res.Warnings, "variable assignment %s (file %s)", m[1], base)
				continue
			}
			return nil, fmt.Errorf("%s:%d: unrecognized line %q", base, no, ln)
		}
		comment, attrs = nil, nil
	}
	if err := finalize(); err != nil {
		return nil, err
	}
	if len(res.Tasks) == 0 {
		return nil, fmt.Errorf("%s: no importable recipes found", base)
	}
	return &res, nil
}

// justRecipe parses a signature: name, @ prefix, doc-comment desc,
// attribute list and parameters with the ruling's type inference.
// Bare tokens are required string parameters (just has no dep syntax).
func justRecipe(base string, line int, sig []string, comment, attrs []string, warns *[]string) (*justRec, error) {
	r := &justRec{name: sig[2], line: line, attrs: attrs}
	if sig[1] == "@" {
		r.atSeen = true
	}
	if len(comment) > 0 {
		r.desc = strings.Join(comment, " ")
	}
	for _, a := range attrs {
		switch strings.TrimSpace(a) {
		case "private":
			r.skip = true
		case "script":
			r.script = true
		default:
			warnf(warns, "attribute [%s] (task %s)", strings.TrimSpace(a), r.name)
		}
	}
	for _, tok0 := range splitQuoted(sig[3]) {
		if tok0 == "" {
			continue
		}
		variadic := strings.HasPrefix(tok0, "+") || strings.HasPrefix(tok0, "*")
		tok := strings.TrimLeft(tok0, "+*")
		if variadic {
			warnf(warns, "variadic parameter %q (task %s)", tok0, r.name)
		}
		eq := strings.Index(tok, "=")
		if eq < 0 {
			if !justIdentRe.MatchString(tok) {
				return nil, fmt.Errorf("cannot parse parameter %q in recipe %q", tok, r.name)
			}
			r.args = append(r.args, Arg{Name: strings.TrimPrefix(tok, "."), Type: "string", Required: true})
			continue
		}
		name, val := tok[:eq], tok[eq+1:]
		if !justIdentRe.MatchString(name) {
			return nil, fmt.Errorf("cannot parse parameter %q in recipe %q", tok, r.name)
		}
		switch {
		case isQuoted(val):
			r.args = append(r.args, Arg{Name: name, Type: "string", Desc: "default: " + unquote(val)})
		case val == "true" || val == "false":
			r.args = append(r.args, Arg{Name: name, Type: "bool", Desc: "default: " + val})
		default:
			if _, err := strconv.ParseFloat(val, 64); err == nil && val != "" {
				r.args = append(r.args, Arg{Name: name, Type: "num", Desc: "default: " + val})
			} else {
				return nil, fmt.Errorf("cannot parse parameter %q in recipe %q", tok, r.name)
			}
		}
	}
	// Dependencies after the colon. just's prior dependencies map to
	// hire deps (run before the body, in listed order); `&&` subsequent
	// dependencies run AFTER the recipe and cannot be expressed, and
	// parenthesized tokens are recipe calls with arguments.
	subsequent := false
	for _, tok := range strings.Fields(sig[4]) {
		switch {
		case tok == "&&":
			subsequent = true
		case subsequent:
			// dropped; the warning below names the feature
		case strings.HasPrefix(tok, "("):
			warnf(warns, "dependency call arguments %s (task %s)", tok, r.name)
		default:
			r.deps = append(r.deps, tok)
		}
	}
	if subsequent {
		warnf(warns, "subsequent dependencies (&&) (task %s)", r.name)
	}
	return r, nil
}

// justPlaceholders validates every {{...}} in the commands against the
// declared args: declared string/num names pass through (hire's rule 1
// is just's own raw-substitution form), bool args must use the
// conditional form {{name:+word}}, and everything else (just functions,
// variables, unknown names) stays literal with one warning each.
func justPlaceholders(r *justRec, cmds []string, warns *[]string) {
	declared := map[string]int{}
	for i, a := range r.args {
		declared[a.Name] = i
	}
	seen := map[string]bool{}
	for _, cmd := range cmds {
		for _, m := range justTmplRe.FindAllStringSubmatch(cmd, -1) {
			content := m[1]
			if cm := justCondRe.FindStringSubmatch(content); cm != nil {
				if i, ok := declared[cm[1]]; ok && r.args[i].Type == "bool" {
					continue
				}
			}
			if im := justIdentRe.FindStringSubmatch(content); im != nil {
				if i, ok := declared[im[1]]; ok {
					if !seen[content] {
						seen[content] = true
						if r.args[i].Type == "bool" {
							// just prints a raw bool's text for {{name}};
							// the loader only allows bools in the
							// {{name:+word}} form, so demote to string.
							r.args[i].Type = "string"
							warnf(warns, "bool arg %q must use the conditional form {{%s:+word}} (task %s)", im[1], im[1], r.name)
						}
					}
					continue
				}
			}
			if !seen[content] {
				seen[content] = true
				warnf(warns, "just template {{%s}} kept literal (task %s)", content, r.name)
				autoPlaceholderArg(&r.args, content)
			}
		}
	}
}

// splitQuoted splits a parameter list on whitespace outside quotes so
// defaults containing spaces survive as single tokens (quotes stay in
// the token for the type inference to see).
func splitQuoted(s string) []string {
	var toks []string
	var cur strings.Builder
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			cur.WriteByte(c)
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
			cur.WriteByte(c)
		case c == ' ' || c == '\t':
			if cur.Len() > 0 {
				toks = append(toks, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		toks = append(toks, cur.String())
	}
	return toks
}

func isQuoted(s string) bool {
	return len(s) >= 2 &&
		((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\''))
}

func unquote(s string) string {
	if isQuoted(s) {
		return s[1 : len(s)-1]
	}
	return s
}
