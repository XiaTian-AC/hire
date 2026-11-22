package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	lua "github.com/yuin/gopher-lua"

	"hire/internal/adapter"
)

func ConfigDir() (string, error) {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "hire"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "hire"), nil
}

func DataDir() (string, error) {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return filepath.Join(v, "hire"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "hire"), nil
}

func EnsureDataDir() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// Taskfiles returns the fixed five-file lookup candidates for dir, in
// probe order. .mjs/.cc are deliberately absent: they are reachable
// only through --taskfile.
func Taskfiles(dir string) []string {
	names := [...]string{"Taskfile.toml", "Taskfile.lua", "Taskfile.py", "Taskfile.js", "Taskfile.cpp"}
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = filepath.Join(dir, n)
	}
	return out
}

// Display holds display-related config options.
type Display struct {
	LsRunning LsRunningDisplay
}

type LsRunningDisplay struct {
	Time         time.Duration // 0 = running only; otherwise window for ended instances
	RunningFirst bool          // running instances on top
	NewestFirst  bool          // newest first within each group
}

// DefaultDisplay returns the default display config.
func DefaultDisplay() Display {
	return Display{
		LsRunning: LsRunningDisplay{
			Time:         time.Hour,
			RunningFirst: true,
			NewestFirst:  true,
		},
	}
}

// loadConfigTable reads dir/config.lua and returns the table it
// returns, or (nil, nil) when the file is missing or returns nothing.
func loadConfigTable(dir string) (*lua.LTable, error) {
	path := filepath.Join(dir, "config.lua")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	L := lua.NewState()
	defer L.Close()
	if err := L.DoFile(path); err != nil {
		return nil, err
	}
	if L.GetTop() == 0 {
		return nil, nil
	}
	tbl, ok := L.Get(-1).(*lua.LTable)
	if !ok {
		return nil, fmt.Errorf("config.lua: expected table, got %s", L.Get(-1).Type().String())
	}
	return tbl, nil
}

// Load reads config.lua from dir and returns the parsed options.
// Missing file returns defaults with no error.
func Load(dir string) (Display, map[string]string, error) {
	display := DefaultDisplay()
	shells := map[string]string{}
	tbl, err := loadConfigTable(dir)
	if err != nil || tbl == nil {
		return display, shells, err
	}

	if st, ok := tbl.RawGetString("shells").(*lua.LTable); ok {
		st.ForEach(func(k, v lua.LValue) {
			ks, kok := k.(lua.LString)
			vs, vok := v.(lua.LString)
			if kok && vok {
				shells[string(ks)] = string(vs)
			}
		})
	} else if tbl.RawGetString("shells") != lua.LNil {
		return display, shells, fmt.Errorf("config.lua: 'shells' must be a table, got %s", tbl.RawGetString("shells").Type().String())
	}

	if disp, ok := tbl.RawGetString("display").(*lua.LTable); ok {
		if lr, ok := disp.RawGetString("ls_running").(*lua.LTable); ok {
			if s, ok := lr.RawGetString("time").(lua.LString); ok {
				d, err := ParseDuration(string(s))
				if err != nil {
					return display, shells, fmt.Errorf("config.lua: display.ls_running.time: %w", err)
				}
				display.LsRunning.Time = d
			}
			if v, ok := lr.RawGetString("running_first").(lua.LBool); ok {
				display.LsRunning.RunningFirst = bool(v)
			}
			if v, ok := lr.RawGetString("newest_first").(lua.LBool); ok {
				display.LsRunning.NewestFirst = bool(v)
			}
		}
	}

	return display, shells, nil
}

// Shells loads only the shells registry. Convenience wrapper around Load.
func Shells(dir string) (map[string]string, error) {
	_, shells, err := Load(dir)
	return shells, err
}

// Runtimes loads the external-runtime registry from config.lua: string
// values are interpreter commands, table values carry a compiled build
// template. Missing file returns an empty registry, no error.
func Runtimes(dir string) (map[string]adapter.RuntimeSpec, error) {
	rts := map[string]adapter.RuntimeSpec{}
	tbl, err := loadConfigTable(dir)
	if err != nil || tbl == nil {
		return rts, err
	}
	rtv := tbl.RawGetString("runtimes")
	if rtv == lua.LNil {
		return rts, nil
	}
	rt, ok := rtv.(*lua.LTable)
	if !ok {
		return rts, fmt.Errorf("config.lua: 'runtimes' must be a table, got %s", rtv.Type().String())
	}
	var failure error
	rt.ForEach(func(k, v lua.LValue) {
		if failure != nil {
			return
		}
		name, kok := k.(lua.LString)
		if !kok {
			failure = fmt.Errorf("config.lua: runtimes names must be strings")
			return
		}
		switch sv := v.(type) {
		case lua.LString:
			if string(sv) == "" {
				failure = fmt.Errorf("config.lua: runtimes.%s: command must not be empty", string(name))
				return
			}
			rts[string(name)] = adapter.RuntimeSpec{Cmd: string(sv)}
		case *lua.LTable:
			spec := adapter.RuntimeSpec{}
			if s, ok := sv.RawGetString("cmd").(lua.LString); ok {
				spec.Cmd = string(s)
			}
			if s, ok := sv.RawGetString("build").(lua.LString); ok {
				spec.Build = string(s)
			}
			if pv := sv.RawGetString("parser"); pv != lua.LNil {
				s, ok := pv.(lua.LString)
				if !ok {
					failure = fmt.Errorf("config.lua: runtimes.%s: parser must be a string (the clang command for C++ function-form taskfiles)", string(name))
					return
				}
				spec.Parser = string(s)
			}
			if spec.Cmd == "" && spec.Build == "" {
				failure = fmt.Errorf("config.lua: runtimes.%s: table form needs cmd = \"...\" (interpreted) or build = \"...\" (compiled)", string(name))
				return
			}
			switch ev := sv.RawGetString("ext").(type) {
			case lua.LString:
				spec.Ext = append(spec.Ext, normalizeExt(string(ev)))
			case *lua.LTable:
				ev.ForEach(func(_, xv lua.LValue) {
					if failure != nil {
						return
					}
					s, ok := xv.(lua.LString)
					if !ok {
						failure = fmt.Errorf("config.lua: runtimes.%s: ext entries must be strings", string(name))
						return
					}
					spec.Ext = append(spec.Ext, normalizeExt(string(s)))
				})
			default:
				if ev != lua.LNil {
					failure = fmt.Errorf("config.lua: runtimes.%s: ext must be a string or an array of strings", string(name))
					return
				}
			}
			if failure != nil {
				return
			}
			rts[string(name)] = spec
		default:
			failure = fmt.Errorf("config.lua: runtimes.%s must be a command string, { cmd = \"...\", ext = \"...\" }, or { build = \"...\" }", string(name))
		}
	})
	return rts, failure
}

// normalizeExt canonicalizes a configured extension: lowercased, with a
// leading dot, so ".TS", "ts" and ".ts" all compare equal at resolution.
func normalizeExt(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s != "" && !strings.HasPrefix(s, ".") {
		s = "." + s
	}
	return s
}

// FormatDuration renders a duration in hire's compact form: "0", "30m",
// "1h", "2d", "1w". Falls back to Go's default for odd values.
func FormatDuration(d time.Duration) string {
	if d == 0 {
		return "0"
	}
	switch {
	case d%(7*24*time.Hour) == 0:
		return fmt.Sprintf("%dw", int(d/(7*24*time.Hour)))
	case d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case d%(time.Hour) == 0:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d%(time.Minute) == 0:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return d.String()
}

// ParseDuration parses a short duration string:
//   "0"           -> 0 (running only)
//   "30" or "30h" -> 30 hours (h is default unit)
//   "30m"         -> 30 minutes
//   "2d"          -> 2 days
//   "1w"          -> 1 week
func ParseDuration(s string) (time.Duration, error) {
	if s == "" || s == "0" {
		return 0, nil
	}
	unit, num := byte('h'), s
	if last := s[len(s)-1]; last < '0' || last > '9' {
		unit, num = last, s[:len(s)-1]
	}
	n, err := strconv.Atoi(num)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid duration %q (use 0, 30m, 1h, 2d, 1w)", s)
	}
	switch unit {
	case 'm':
		return time.Duration(n) * time.Minute, nil
	case 'h':
		return time.Duration(n) * time.Hour, nil
	case 'd':
		return time.Duration(n) * 24 * time.Hour, nil
	case 'w':
		return time.Duration(n) * 7 * 24 * time.Hour, nil
	}
	return 0, fmt.Errorf("invalid duration unit %q (use m/h/d/w)", string(unit))
}
