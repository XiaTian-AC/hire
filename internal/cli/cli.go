package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"hire/internal/adapter"
	"hire/internal/argparse"
	"hire/internal/bg"
	"hire/internal/config"
	"hire/internal/runtime"
)

type Deps struct {
	SelfPath string
	Version  string
	Stdin    io.Reader
	Stdout   io.Writer
	Stderr   io.Writer

	TaskfileOverride string
}

func Run(argv []string, d Deps) int {
	if d.Version == "" {
		d.Version = "dev"
	}
	if d.SelfPath == "" {
		d.SelfPath, _ = os.Executable()
	}
	if d.Stdin == nil {
		d.Stdin = os.Stdin
	}
	if d.Stdout == nil {
		d.Stdout = os.Stdout
	}
	if d.Stderr == nil {
		d.Stderr = os.Stderr
	}

	if len(argv) == 0 {
		usage(d.Stdout)
		return 2
	}

	if d.TaskfileOverride == "" {
		tfPath, rest := extractTaskfile(argv)
		d.TaskfileOverride = tfPath
		argv = rest
	}

	cmd, rest := argv[0], argv[1:]
	switch cmd {
	case "help", "--help", "-h":
		if len(rest) > 0 {
			return cmdHelpTask(rest[0], d)
		}
		usage(d.Stdout)
		return 0
	case "version", "--version":
		fmt.Fprintf(d.Stdout, "hire %s\n", d.Version)
		return 0
	case "ls":
		return cmdLs(d)
	case "start", "run":
		if len(rest) == 0 {
			fmt.Fprintf(d.Stderr, "hire: usage: hire %s <task> [args]\n", cmd)
			return 2
		}
		return cmdRunTask(rest, d)
	case "bstart":
		if len(rest) == 0 {
			fmt.Fprintln(d.Stderr, "hire: usage: hire bstart <task> [args]")
			return 2
		}
		return cmdBstart(rest, d)
	case "ls-running":
		return cmdLsRunning(d)
	case "clean":
		return cmdClean(rest, d)
	case "watch":
		if len(rest) == 0 {
			fmt.Fprintln(d.Stderr, "hire: usage: hire watch <name>[#N]")
			return 2
		}
		return cmdWatch(rest[0], d)
	case "stop":
		if len(rest) == 0 {
			fmt.Fprintln(d.Stderr, "hire: usage: hire stop <name>[#N]")
			return 2
		}
		return cmdStop(rest[0], d)
	case "config":
		return cmdConfig(rest, d)
	case "_run":
		if len(rest) == 0 {
			return 2
		}
		return cmdInternalRun(rest, d)
	}
	return cmdRunTask(argv, d)
}

func usage(out io.Writer) {
	cfgDir, _ := config.ConfigDir()
	fmt.Fprintf(out, `hire - task runner (any-language taskfiles + background execution)

Usage:
  hire <task> [args]        run a task in the foreground
  hire bstart <task> [args] run a task in the background (detached)
  hire ls                   list tasks
  hire ls-running           list background instances (filtered by display.ls_running.time)
  hire clean [--all|--older-than <dur>]  remove ended instances (and their logs)
  hire watch <name>[#N]     tail a background instance's log live
  hire stop <name>[#N]      stop background instance(s)
  hire config [section]     show effective config (all | display | shells)
  hire run <task> [args]    force-run a task (builtin-name collision escape hatch)
  hire help [task]          help (or task details)
  hire version              print version

Flags:
  --shell <name>           select shell for this run
  --taskfile <path>        use one specific taskfile (any supported extension)

Files:
  lookup: Taskfile.{toml,lua,py,js,cpp} in the working directory,
          then in %s (same-name task: cwd wins)
  config: %s/config.lua

Tasks are defined in a Taskfile.toml/lua/py/js/cpp in the working
directory or the hire config directory. Python/JS/C++ taskfiles need a
matching runtimes entry in config.lua.
`, cfgDir, cfgDir)
}

// resolveTaskfiles returns the effective taskfile list in LOAD order
// (global first, cwd last, so a same-name task from cwd wins), or an
// error listing every probed path when none exists. --taskfile
// isolates to exactly one file, never merged.
func resolveTaskfiles(d Deps) ([]string, error) {
	if d.TaskfileOverride != "" {
		return []string{d.TaskfileOverride}, nil
	}
	cfgDir, err := config.ConfigDir()
	if err != nil {
		return nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	var load []string
	seen := map[string]bool{}
	for _, dir := range []string{cfgDir, cwd} {
		for _, p := range config.Taskfiles(dir) {
			abs, err := filepath.Abs(p)
			if err != nil {
				abs = p
			}
			if seen[abs] || !fileExists(p) {
				continue
			}
			seen[abs] = true
			load = append(load, p)
		}
	}
	if len(load) == 0 {
		var probed []string
		for _, dir := range []string{cwd, cfgDir} { // probe order: cwd first
			probed = append(probed, config.Taskfiles(dir)...)
		}
		return nil, fmt.Errorf("no Taskfile found (probed: %s)", strings.Join(probed, ", "))
	}
	return load, nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// loadChain loads the given files (the extension dispatches the
// adapter) and merges them in list order. Callers pass globals first
// so cwd tasks override on name clashes.
func loadChain(files []string) (*adapter.Chain, map[string]string, error) {
	cfgDir, err := config.ConfigDir()
	if err != nil {
		return nil, nil, err
	}
	_, shells, err := config.Load(cfgDir)
	if err != nil {
		return nil, nil, err
	}
	var rts map[string]adapter.RuntimeSpec
	for _, p := range files {
		if isExecExt(filepath.Ext(p)) {
			rts, err = config.Runtimes(cfgDir)
			if err != nil {
				return nil, nil, err
			}
			break
		}
	}
	loaded := make([]*adapter.File, 0, len(files))
	for _, p := range files {
		f, err := loadOne(p, rts)
		if err != nil {
			for _, done := range loaded {
				done.Close()
			}
			return nil, nil, err
		}
		loaded = append(loaded, f)
	}
	return adapter.NewChain(loaded...), shells, nil
}

func isExecExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".py", ".js", ".mjs", ".cpp", ".cc":
		return true
	}
	return false
}

// loadOne loads a single taskfile; the extension picks the adapter.
// .mjs/.cc dispatch too but are reachable only via --taskfile, since
// the auto-discovery list is the fixed five.
func loadOne(path string, rts map[string]adapter.RuntimeSpec) (*adapter.File, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".lua":
		return adapter.LoadLua(path)
	case ".toml":
		return adapter.LoadTOML(path)
	case ".py", ".js", ".mjs", ".cpp", ".cc":
		spec, name, err := adapter.ResolveRuntime(ext, rts)
		if err != nil {
			return nil, err
		}
		return adapter.LoadExec(path, spec, name)
	}
	return nil, fmt.Errorf("%s: unsupported taskfile extension %q (known: .toml .lua .py .js .cpp; --taskfile also accepts .mjs .cc)", path, ext)
}

func loadTaskfile(d Deps) (*adapter.Chain, map[string]string, error) {
	files, err := resolveTaskfiles(d)
	if err != nil {
		return nil, nil, err
	}
	return loadChain(files)
}

func loadDisplay(d Deps) (config.Display, error) {
	cfgDir, err := config.ConfigDir()
	if err != nil {
		return config.DefaultDisplay(), err
	}
	disp, _, err := config.Load(cfgDir)
	return disp, err
}

func extractShell(argv []string) (string, []string) {
	for i := 0; i < len(argv); i++ {
		if argv[i] == "--" {
			break
		}
		if argv[i] == "--shell" && i+1 < len(argv) {
			rest := append(append([]string{}, argv[:i]...), argv[i+2:]...)
			return argv[i+1], rest
		}
		if strings.HasPrefix(argv[i], "--shell=") {
			rest := append(append([]string{}, argv[:i]...), argv[i+1:]...)
			return strings.TrimPrefix(argv[i], "--shell="), rest
		}
	}
	return "", argv
}

func extractTaskfile(argv []string) (string, []string) {
	for i := 0; i < len(argv); i++ {
		if argv[i] == "--" {
			break
		}
		if argv[i] == "--taskfile" {
			if i+1 >= len(argv) {
				return "", argv
			}
			rest := append(append([]string{}, argv[:i]...), argv[i+2:]...)
			return argv[i+1], rest
		}
		if strings.HasPrefix(argv[i], "--taskfile=") {
			rest := append(append([]string{}, argv[:i]...), argv[i+1:]...)
			return strings.TrimPrefix(argv[i], "--taskfile="), rest
		}
	}
	return "", argv
}

func cmdLs(d Deps) int {
	c, _, err := loadTaskfile(d)
	if err != nil {
		fmt.Fprintln(d.Stderr, "hire:", err)
		return 1
	}
	defer c.Close()
	if len(c.Order) == 0 {
		fmt.Fprintln(d.Stdout, "no tasks defined")
		return 0
	}
	maxLen := 0
	for _, name := range c.Order {
		if len(name) > maxLen {
			maxLen = len(name)
		}
	}
	for _, name := range c.Order {
		task := c.Tasks[name]
		desc := task.Desc
		if argsHint := argsHint(task.Args); argsHint != "" {
			desc = desc + "  " + argsHint
		}
		fmt.Fprintf(d.Stdout, "%-*s  %s\n", maxLen, name, desc)
	}
	return 0
}

func argsHint(specs map[string]argparse.Spec) string {
	if len(specs) == 0 {
		return ""
	}
	names := make([]string, 0, len(specs))
	for k := range specs {
		names = append(names, k)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		if specs[n].Required {
			parts = append(parts, "--"+n+" (required)")
		} else {
			parts = append(parts, "--"+n)
		}
	}
	return "[args: " + strings.Join(parts, ", ") + "]"
}

// setHireArgs publishes the parsed task args for exec-adapter task
// closures, which re-exec the taskfile program with HIRE_ARGS read
// from the process environment. Tasks without declared args get {}.
func setHireArgs(task *adapter.Task, parsed *argparse.Parsed) error {
	if len(task.Args) == 0 {
		return os.Setenv("HIRE_ARGS", "{}")
	}
	m := make(map[string]any, len(parsed.Flags)+1)
	for k, v := range parsed.Flags {
		m[k] = v
	}
	if parsed.Positional != nil {
		m["_"] = parsed.Positional
	} else {
		m["_"] = []string{} // marshal as [], not null
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.Setenv("HIRE_ARGS", string(b))
}

func cmdRunTask(argv []string, d Deps) int {
	name := argv[0]
	shellName, taskArgs := extractShell(argv[1:])
	c, shells, err := loadTaskfile(d)
	if err != nil {
		fmt.Fprintln(d.Stderr, "hire:", err)
		return 1
	}
	defer c.Close()
	task, ok := c.Tasks[name]
	if !ok {
		fmt.Fprintf(d.Stderr, "hire: task %q not found; run 'hire ls'\n", name)
		return 1
	}
	parsed, err := argparse.Parse(taskArgs, task.Args)
	if err != nil {
		fmt.Fprintf(d.Stderr, "hire: task %q: %v\n", name, err)
		return 1
	}
	if err := setHireArgs(task, parsed); err != nil {
		fmt.Fprintln(d.Stderr, "hire:", err)
		return 1
	}
	argsTable := argparse.ToLuaTable(c.FileOf(task).L, parsed)
	err = runtime.Run(c, name, argsTable, runtime.Options{
		ShellName: shellName, Registry: shells,
		Stdout: d.Stdout, Stderr: d.Stderr, Stdin: d.Stdin,
	})
	if err != nil {
		fmt.Fprintln(d.Stderr, "hire:", err)
		return 1
	}
	return 0
}

func cmdBstart(argv []string, d Deps) int {
	name := argv[0]
	shellName, taskArgs := extractShell(argv[1:])
	files, err := resolveTaskfiles(d)
	if err != nil {
		fmt.Fprintln(d.Stderr, "hire:", err)
		return 1
	}
	c, shells, err := loadChain(files)
	if err != nil {
		fmt.Fprintln(d.Stderr, "hire:", err)
		return 1
	}
	defer c.Close()
	task, ok := c.Tasks[name]
	if !ok {
		fmt.Fprintf(d.Stderr, "hire: task %q not found; run 'hire ls'\n", name)
		return 1
	}
	_, err = argparse.Parse(taskArgs, task.Args)
	if err != nil {
		fmt.Fprintf(d.Stderr, "hire: task %q: %v\n", name, err)
		return 1
	}
	dataDir, err := config.EnsureDataDir()
	if err != nil {
		fmt.Fprintln(d.Stderr, "hire:", err)
		return 1
	}
	running, err := bg.Running(dataDir, name)
	if err != nil {
		fmt.Fprintln(d.Stderr, "hire:", err)
		return 1
	}
	if len(running) > 0 {
		fmt.Fprintf(d.Stdout, "%d instance(s) of %q already running:\n", len(running), name)
		for _, inst := range running {
			fmt.Fprintf(d.Stdout, "  %s (pid %d, started %s)\n", inst.ID, inst.PID, inst.Started.Format("15:04:05"))
		}
		fmt.Fprintln(d.Stdout, "WARNING: same-name instances may conflict (file/port contention, unlocked git operations).")
		if !confirm("Start another?", d.Stdin, d.Stdout) {
			fmt.Fprintln(d.Stdout, "aborted")
			return 0
		}
	}
	_ = shells
	cwd, _ := os.Getwd()
	inst, err := bg.Start(bg.StartOptions{
		SelfPath:  d.SelfPath,
		TaskName:  name,
		TaskArgs:  taskArgs,
		ShellName: shellName,
		CWD:       cwd,
		DataDir:   dataDir,
		Files:     files,
	})
	if err != nil {
		fmt.Fprintln(d.Stderr, "hire:", err)
		return 1
	}
	fmt.Fprintf(d.Stdout, "started %s (pid %d)\nlog: %s\nwatch: hire watch %s\nstop:  hire stop %s\n",
		inst.ID, inst.PID, inst.Log, inst.ID, inst.ID)
	return 0
}

func cmdLsRunning(d Deps) int {
	dataDir, err := config.DataDir()
	if err != nil {
		fmt.Fprintln(d.Stderr, "hire:", err)
		return 1
	}
	all, err := bg.Snapshot(dataDir)
	if err != nil {
		fmt.Fprintln(d.Stderr, "hire:", err)
		return 1
	}
	if len(all) == 0 {
		fmt.Fprintln(d.Stdout, "no background instances")
		return 0
	}
	disp, err := loadDisplay(d)
	if err != nil {
		fmt.Fprintln(d.Stderr, "hire:", err)
		return 1
	}
	now := time.Now()
	var visible []bg.Instance
	for _, inst := range all {
		if inst.Status == "running" {
			visible = append(visible, inst)
		} else if disp.LsRunning.Time > 0 && inst.EndedAt != nil && now.Sub(*inst.EndedAt) <= disp.LsRunning.Time {
			visible = append(visible, inst)
		}
	}
	if len(visible) == 0 {
		fmt.Fprintf(d.Stdout, "no recent instances (window %s)", disp.LsRunning.Time)
		return 0
	}
	sort.SliceStable(visible, func(i, j int) bool {
		if disp.LsRunning.RunningFirst {
			iRunning := visible[i].Status == "running"
			jRunning := visible[j].Status == "running"
			if iRunning != jRunning {
				return iRunning
			}
		}
		if disp.LsRunning.NewestFirst {
			return visible[i].Started.After(visible[j].Started)
		}
		return visible[i].Started.Before(visible[j].Started)
	})
	for _, inst := range visible {
		status := inst.Status
		if status == "running" && !bg.Alive(inst.PID) {
			status = "dead"
			_ = bg.MarkEnded(dataDir, inst.ID, "dead", -1)
		}
		if inst.Status == "exited" && inst.ExitCode != 0 {
			status = fmt.Sprintf("exited(%d)", inst.ExitCode)
		}
		age := time.Since(inst.Started).Round(time.Second)
		fmt.Fprintf(d.Stdout, "%-16s  pid %-7d  %-12s  %s  %s\n",
			inst.ID, inst.PID, status, age, inst.CWD)
	}
	return 0
}

func cmdClean(argv []string, d Deps) int {
	dataDir, err := config.DataDir()
	if err != nil {
		fmt.Fprintln(d.Stderr, "hire:", err)
		return 1
	}
	olderThan := ""
	for _, a := range argv {
		if a == "--all" {
			olderThan = "0"
			continue
		}
		if strings.HasPrefix(a, "--older-than=") {
			olderThan = strings.TrimPrefix(a, "--older-than=")
		}
	}
	all, err := bg.Snapshot(dataDir)
	if err != nil {
		fmt.Fprintln(d.Stderr, "hire:", err)
		return 1
	}
	var threshold time.Duration
	if olderThan != "" {
		threshold, err = config.ParseDuration(olderThan)
		if err != nil {
			fmt.Fprintf(d.Stderr, "hire: --older-than: %v\n", err)
			return 2
		}
	}
	now := time.Now()
	var toRemove []bg.Instance
	for _, inst := range all {
		if inst.Status == "running" {
			continue
		}
		if inst.EndedAt == nil {
			continue
		}
		if olderThan == "" || now.Sub(*inst.EndedAt) > threshold {
			toRemove = append(toRemove, inst)
		}
	}
	for _, inst := range toRemove {
		if err := bg.RemoveInstance(dataDir, inst.ID); err != nil {
			fmt.Fprintf(d.Stderr, "hire: remove %s: %v\n", inst.ID, err)
		}
	}
	if olderThan == "" {
		fmt.Fprintf(d.Stdout, "cleaned %d instance(s)\n", len(toRemove))
	} else {
		fmt.Fprintf(d.Stdout, "cleaned %d instance(s) older than %s\n", len(toRemove), olderThan)
	}
	return 0
}

func cmdWatch(ref string, d Deps) int {
	dataDir, err := config.DataDir()
	if err != nil {
		fmt.Fprintln(d.Stderr, "hire:", err)
		return 1
	}
	all, err := bg.Snapshot(dataDir)
	if err != nil {
		fmt.Fprintln(d.Stderr, "hire:", err)
		return 1
	}
	inst, err := findInstance(all, ref)
	if err != nil {
		fmt.Fprintln(d.Stderr, "hire:", err)
		return 1
	}
	isAlive := func() bool {
		return inst.Status == "running" && bg.Alive(inst.PID)
	}
	if err := bg.Watch(inst.Log, inst.ID, isAlive, d.Stdout); err != nil {
		fmt.Fprintln(d.Stderr, "hire:", err)
		return 1
	}
	return 0
}

func cmdStop(ref string, d Deps) int {
	dataDir, err := config.DataDir()
	if err != nil {
		fmt.Fprintln(d.Stderr, "hire:", err)
		return 1
	}
	all, err := bg.Snapshot(dataDir)
	if err != nil {
		fmt.Fprintln(d.Stderr, "hire:", err)
		return 1
	}
	var targets []bg.Instance
	if strings.Contains(ref, "#") {
		for _, inst := range all {
			if inst.ID == ref && inst.Status == "running" {
				targets = append(targets, inst)
				break
			}
		}
		if len(targets) == 0 {
			for _, inst := range all {
				if inst.ID == ref {
					fmt.Fprintf(d.Stdout, "%s: already %s\n", inst.ID, inst.Status)
					return 0
				}
			}
			fmt.Fprintf(d.Stderr, "hire: no instance %q\n", ref)
			return 1
		}
	} else {
		for _, inst := range all {
			if inst.Name == ref && inst.Status == "running" {
				targets = append(targets, inst)
			}
		}
		if len(targets) == 0 {
			fmt.Fprintf(d.Stdout, "no running instances of %q\n", ref)
			return 0
		}
		if len(targets) > 1 {
			fmt.Fprintf(d.Stdout, "stopping %d instances of %q:\n", len(targets), ref)
			for _, inst := range targets {
				fmt.Fprintf(d.Stdout, "  %s (pid %d)\n", inst.ID, inst.PID)
			}
			if !confirm("Stop all?", d.Stdin, d.Stdout) {
				fmt.Fprintln(d.Stdout, "aborted")
				return 0
			}
		}
	}
	for _, inst := range targets {
		if !bg.Alive(inst.PID) {
			fmt.Fprintf(d.Stdout, "%s: process already gone\n", inst.ID)
		} else if err := bg.Stop(inst.PID); err != nil {
			fmt.Fprintf(d.Stderr, "hire: stop %s: %v\n", inst.ID, err)
		} else {
			fmt.Fprintf(d.Stdout, "stopped %s\n", inst.ID)
		}
		_ = bg.MarkEnded(dataDir, inst.ID, "stopped", -1)
	}
	return 0
}

func cmdConfig(argv []string, d Deps) int {
	cfgDir, err := config.ConfigDir()
	if err != nil {
		fmt.Fprintln(d.Stderr, "hire:", err)
		return 1
	}
	disp, shells, err := config.Load(cfgDir)
	if err != nil {
		fmt.Fprintln(d.Stderr, "hire:", err)
		return 1
	}

	section := "all"
	if len(argv) > 0 {
		section = argv[0]
	}
	switch section {
	case "all", "display", "shells":
	default:
		fmt.Fprintf(d.Stderr, "hire: unknown config section %q (use: all | display | shells)\n", section)
		return 2
	}

	if section == "all" || section == "display" {
		fmt.Fprintf(d.Stdout, "[display.ls_running]\n")
		fmt.Fprintf(d.Stdout, "time          = %s\n", config.FormatDuration(disp.LsRunning.Time))
		fmt.Fprintf(d.Stdout, "running_first = %t\n", disp.LsRunning.RunningFirst)
		fmt.Fprintf(d.Stdout, "newest_first  = %t\n", disp.LsRunning.NewestFirst)
	}
	if section == "all" || section == "shells" {
		if section == "all" {
			fmt.Fprintln(d.Stdout)
		}
		fmt.Fprintf(d.Stdout, "[shells]\n")
		if len(shells) == 0 {
			fmt.Fprintln(d.Stdout, "(none registered; built-ins: pwsh, bash, sh)")
		} else {
			names := make([]string, 0, len(shells))
			for k := range shells {
				names = append(names, k)
			}
			sort.Strings(names)
			for _, n := range names {
				fmt.Fprintf(d.Stdout, "%-12s = %s\n", n, shells[n])
			}
		}
	}
	return 0
}

func cmdInternalRun(argv []string, d Deps) (exit int) {
	name := argv[0]
	rest := argv[1:]
	shellName := ""
	id := ""
	var taskArgs []string
	for i := 0; i < len(rest); {
		switch {
		case rest[i] == "--shell" && i+1 < len(rest):
			shellName = rest[i+1]
			i += 2
		case rest[i] == "--id" && i+1 < len(rest):
			id = rest[i+1]
			i += 2
		case rest[i] == "--":
			taskArgs = append(taskArgs, rest[i+1:]...)
			i = len(rest)
		default:
			taskArgs = append(taskArgs, rest[i])
			i++
		}
	}
	if id != "" {
		// Every exit path must retire the instance: a vanished
		// taskfile may fail the load below, and the registry entry
		// must not stay "running" forever.
		defer func() {
			dataDir, _ := config.DataDir()
			_ = bg.MarkEnded(dataDir, id, "exited", exit)
		}()
	}
	files, err := filesFromEnv()
	if err != nil {
		fmt.Fprintf(d.Stderr, "hire: %v\n", err)
		return 1
	}
	if len(files) == 0 {
		fmt.Fprintln(d.Stderr, "hire: HIRE_FILES not set; probing taskfiles")
		files, err = resolveTaskfiles(d)
		if err != nil {
			fmt.Fprintf(d.Stderr, "hire: %v\n", err)
			return 1
		}
	}
	c, shells, err := loadChain(files)
	if err != nil {
		fmt.Fprintf(d.Stderr, "hire: %v\n", err)
		return 1
	}
	defer c.Close()
	task, ok := c.Tasks[name]
	if !ok {
		fmt.Fprintf(d.Stderr, "hire: task %q not found\n", name)
		return 1
	}
	parsed, err := argparse.Parse(taskArgs, task.Args)
	if err != nil {
		fmt.Fprintf(d.Stderr, "hire: %v\n", err)
		return 1
	}
	if err := setHireArgs(task, parsed); err != nil {
		fmt.Fprintf(d.Stderr, "hire: %v\n", err)
		return 1
	}
	argsTable := argparse.ToLuaTable(c.FileOf(task).L, parsed)
	if err := runtime.Run(c, name, argsTable, runtime.Options{
		ShellName: shellName, Registry: shells,
		Stdout: d.Stdout, Stderr: d.Stderr, Stdin: nil,
		Background: true,
	}); err != nil {
		fmt.Fprintf(d.Stderr, "hire: %v\n", err)
		return 1
	}
	return 0
}

// filesFromEnv decodes the HIRE_FILES handoff from bstart: a JSON
// array of paths. Empty means "not handed off" (manual _run); a
// malformed value is a hard error rather than a silent re-probe, which
// could execute an unrelated file.
func filesFromEnv() ([]string, error) {
	v := os.Getenv("HIRE_FILES")
	if v == "" {
		return nil, nil
	}
	var files []string
	if err := json.Unmarshal([]byte(v), &files); err != nil {
		return nil, fmt.Errorf("HIRE_FILES: invalid file list: %v", err)
	}
	return files, nil
}

func cmdHelpTask(name string, d Deps) int {
	c, _, err := loadTaskfile(d)
	if err != nil {
		fmt.Fprintln(d.Stderr, "hire:", err)
		return 1
	}
	defer c.Close()
	task, ok := c.Tasks[name]
	if !ok {
		fmt.Fprintf(d.Stderr, "hire: task %q not found\n", name)
		return 1
	}
	fmt.Fprintf(d.Stdout, "hire %s\n", name)
	if task.Desc != "" {
		fmt.Fprintf(d.Stdout, "  %s\n", task.Desc)
	}
	if task.Args != nil {
		fmt.Fprintln(d.Stdout, "  arguments:")
		argNames := make([]string, 0, len(task.Args))
		for k := range task.Args {
			argNames = append(argNames, k)
		}
		sort.Strings(argNames)
		for _, argName := range argNames {
			sp := task.Args[argName]
			req := ""
			if sp.Required {
				req = " (required)"
			}
			desc := sp.Desc
			if desc == "" {
				desc = sp.Type
			}
			fmt.Fprintf(d.Stdout, "    --%s %s%s  %s\n", argName, sp.Type, req, desc)
		}
	}
	return 0
}

func confirm(prompt string, in io.Reader, out io.Writer) bool {
	fmt.Fprintf(out, "%s [y/N] ", prompt)
	reader := bufio.NewReader(in)
	line, err := reader.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	if err != nil && line == "" {
		return false
	}
	return line == "y" || line == "yes"
}

func findInstance(all []bg.Instance, ref string) (bg.Instance, error) {
	if strings.Contains(ref, "#") {
		for i := range all {
			if all[i].ID == ref {
				return all[i], nil
			}
		}
		return bg.Instance{}, fmt.Errorf("no instance %q", ref)
	}
	var newest *bg.Instance
	for i := range all {
		if all[i].Name == ref {
			if newest == nil || all[i].N > newest.N {
				newest = &all[i]
			}
		}
	}
	if newest == nil {
		return bg.Instance{}, fmt.Errorf("no instance for %q", ref)
	}
	return *newest, nil
}
