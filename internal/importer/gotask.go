package importer

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"

	"gopkg.in/yaml.v3"
)

// gtVarRe matches a plain go-task variable reference, {{.VAR}} or
// {{ .VAR }}; anything else inside {{...}} is a template expression.
var gtVarRe = regexp.MustCompile(`\{\{\s*\.([A-Za-z0-9_]+)\s*\}\}`)

// gtTmplRe matches any remaining {{...}} after baking: dynamic
// expressions kept literal with a warning (ruling 3).
var gtTmplRe = regexp.MustCompile(`\{\{[^{}]*\}\}`)

func parseGoTask(path string, src []byte) (*Result, error) {
	base := filepath.Base(path)
	var raw map[string]any
	if err := yaml.Unmarshal(src, &raw); err != nil {
		return nil, fmt.Errorf("%s: invalid YAML: %v", base, err)
	}
	tasksRaw, ok := raw["tasks"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: no %q key found", base, "tasks")
	}
	var res Result
	globalVars, _ := raw["vars"].(map[string]any)

	// File-level keys we consume silently; every other top-level key
	// (includes, env, dotenv, output, ...) is unmappable and warns once.
	for k := range raw {
		switch k {
		case "version", "vars", "tasks":
		default:
			warnf(&res.Warnings, "%s (file %s)", k, base)
		}
	}

	names := make([]string, 0, len(tasksRaw))
	for n := range tasksRaw {
		names = append(names, n)
	}
	sort.Strings(names) // YAML maps lose order; emit deterministically

	anyDeps := false
	for _, name := range names {
		tm, ok := tasksRaw[name].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: task %q: expected a mapping, got %T", base, name, tasksRaw[name])
		}
		task := Task{Name: name}

		if d, present := tm["desc"]; present {
			s, ok := d.(string)
			if !ok {
				return nil, fmt.Errorf("%s: task %q: desc must be a string", base, name)
			}
			task.Desc = s
		}

		// Task vars shadow global vars when baking {{.VAR}}.
		lookup := func(k string) (any, bool) {
			if tv, ok := tm["vars"].(map[string]any); ok {
				if v, ok := tv[k]; ok {
					return v, true
				}
			}
			v, ok := globalVars[k]
			return v, ok
		}

		// cmds: the single string form plus the list of strings/maps.
		var cmds []string
		if c, present := tm["cmd"]; present {
			s, ok := c.(string)
			if !ok {
				return nil, fmt.Errorf("%s: task %q: cmd must be a string", base, name)
			}
			cmds = append(cmds, s)
		}
		if cv, present := tm["cmds"]; present {
			list, ok := cv.([]any)
			if !ok {
				return nil, fmt.Errorf("%s: task %q: cmds must be a list", base, name)
			}
			for _, e := range list {
				switch ev := e.(type) {
				case string:
					cmds = append(cmds, ev)
				case map[string]any:
					if _, ok := ev["for"]; ok {
						warnf(&res.Warnings, "cmd \"for\" loop (task %s)", name)
						continue
					}
					if _, ok := ev["task"]; ok {
						warnf(&res.Warnings, "task call in cmds (task %s)", name)
						continue
					}
					cs, ok := ev["cmd"].(string)
					if !ok {
						warnf(&res.Warnings, "unrecognized cmds entry (task %s)", name)
						continue
					}
					cmds = append(cmds, cs)
					for k := range ev {
						if k != "cmd" {
							warnf(&res.Warnings, "cmds entry key %q (task %s)", k, name)
						}
					}
				default:
					return nil, fmt.Errorf("%s: task %q: cmds entries must be strings or mappings", base, name)
				}
			}
		}
		// deps: list of strings or {task: name} mappings.
		var deps []string
		if dv, present := tm["deps"]; present {
			entries, err := gtEntries(dv)
			if err != nil {
				return nil, fmt.Errorf("%s: task %q: %v", base, name, err)
			}
			for _, e := range entries {
				switch ev := e.(type) {
				case string:
					deps = append(deps, ev)
				case map[string]any:
					dn, ok := ev["task"].(string)
					if !ok {
						warnf(&res.Warnings, "unrecognized deps entry (task %s)", name)
						continue
					}
					deps = append(deps, dn)
					for k := range ev {
						if k != "task" {
							warnf(&res.Warnings, "dep %q attribute %q (task %s)", dn, k, name)
						}
					}
				default:
					return nil, fmt.Errorf("%s: task %q: deps entries must be strings or mappings", base, name)
				}
			}
		}
		task.Deps = deps
		if len(deps) > 0 {
			anyDeps = true
		}

		if len(cmds) == 0 {
			if len(deps) == 0 {
				warnf(&res.Warnings, "task with no commands (task %s)", name)
				continue
			}
			// deps-only task: hire requires a body, keep the chain reachable.
			cmds = []string{"echo " + name}
			warnf(&res.Warnings, "recipe-less task (echo no-op synthesized) (task %s)", name)
		}
		bakeVars(&task, cmds, lookup, &res.Warnings)

		if pv, present := tm["platforms"]; present {
			list, ok := pv.([]any)
			if !ok {
				return nil, fmt.Errorf("%s: task %q: platforms must be a list", base, name)
			}
			for _, e := range list {
				s, ok := e.(string)
				if !ok {
					return nil, fmt.Errorf("%s: task %q: platforms must be strings", base, name)
				}
				task.OS = append(task.OS, s)
			}
		}

		// Unmapped task keys warn (ruling 3): sources/generates as one
		// freshness line, the rest under their own names, sorted so the
		// warning order is deterministic.
		if _, ok := tm["sources"]; ok {
			warnf(&res.Warnings, "sources/generates freshness check (task %s)", name)
		} else if _, ok := tm["generates"]; ok {
			warnf(&res.Warnings, "sources/generates freshness check (task %s)", name)
		}
		var unknown []string
		for k := range tm {
			switch k {
			case "desc", "cmds", "cmd", "deps", "platforms", "vars", "sources", "generates":
			default:
				unknown = append(unknown, k)
			}
		}
		sort.Strings(unknown)
		for _, k := range unknown {
			warnf(&res.Warnings, "%s (task %s)", k, name)
		}
		res.Tasks = append(res.Tasks, task)
	}

	// The one file-level semantic warning (ruling 3): go-task runs deps
	// concurrently; hire runs them sequentially in listed order.
	if anyDeps {
		warnf(&res.Warnings, "go-task runs deps concurrently; hire runs them sequentially in listed order (file %s)", base)
	}
	if len(res.Tasks) == 0 {
		return nil, fmt.Errorf("%s: no importable tasks found", base)
	}
	return &res, nil
}

// gtEntries normalizes a list-or-single-entry YAML value.
func gtEntries(v any) ([]any, error) {
	switch cv := v.(type) {
	case []any:
		return cv, nil
	case map[string]any:
		return []any{cv}, nil
	}
	return nil, fmt.Errorf("deps must be a list or a mapping")
}

// bakeVars replaces {{.VAR}} in every command with the literal static
// value (string, number, bool) from the file vars; non-static or
// unknown templates stay literal, warn once per unique text and get an
// auto-declared optional arg so the emitted TOML passes the loader.
func bakeVars(task *Task, cmds []string, lookup func(string) (any, bool), warns *[]string) {
	out := make([]string, len(cmds))
	seen := map[string]bool{}
	for i, cmd := range cmds {
		out[i] = gtVarRe.ReplaceAllStringFunc(cmd, func(m string) string {
			name := gtVarRe.FindStringSubmatch(m)[1]
			v, ok := lookup(name)
			if !ok {
				return m // unknown: the literal-expression warning below covers it
			}
			if s, ok := scalarText(v); ok {
				return s
			}
			return m // non-static value (sh:, map, ...): kept literal, warned below
		})
		for _, m := range gtTmplRe.FindAllString(out[i], -1) {
			if !seen[m] {
				seen[m] = true
				warnf(warns, "dynamic template %s kept literal (task %s)", m, task.Name)
				autoPlaceholderArg(&task.Args, m[2:len(m)-2])
			}
		}
	}
	task.Cmds = out
}

// scalarText renders a static YAML scalar the way it appears in source;
// maps, lists and null are not static.
func scalarText(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case bool:
		if x {
			return "true", true
		}
		return "false", true
	case int:
		return strconv.Itoa(x), true
	case int64:
		return strconv.FormatInt(x, 10), true
	case uint64:
		return strconv.FormatUint(x, 10), true
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64), true
	}
	return "", false
}
