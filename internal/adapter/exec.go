package adapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	lua "github.com/yuin/gopher-lua"

	"hire/internal/argparse"
)

// discoverJSON is the __hire__ discover output contract (spec §5).
// Unknown JSON fields are ignored so the contract can grow.
type discoverJSON struct {
	Tasks []discoverTask `json:"tasks"`
}

type discoverTask struct {
	Name string                 `json:"name"`
	Desc string                 `json:"desc"`
	Args map[string]discoverArg `json:"args"`
}

type discoverArg struct {
	Type     string `json:"type"`
	Required bool   `json:"required"`
	Desc     string `json:"desc"`
}

// LoadExec loads an external taskfile through the __hire__ contract:
// discover lists its tasks, and each task's Fn re-execs the program
// with `run <name>` and HIRE_ARGS from the environment. Interpreted
// runtimes (spec.Cmd) call `<Cmd> <path>`; compiled ones call the
// binary built by the cached build step. The File's LState is synthetic:
// it hosts only the Go closures, never user Lua code.
func LoadExec(path string, spec RuntimeSpec, rtName string) (*File, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no Taskfile at %s (create it with your tasks)", path)
		}
		return nil, err
	}
	prog, extra := spec.Cmd, []string(nil)
	switch {
	case prog != "":
		extra = []string{path}
	case spec.Build != "":
		exe, err := ensureCompiled(path, rtName, spec)
		if err != nil {
			return nil, err
		}
		prog = exe
	default:
		return nil, fmt.Errorf("%s: runtime spec has neither Cmd nor Build", path)
	}

	stdout, stderr, err := runDiscover(prog, extra)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, discoverErr(path, fmt.Sprintf("__hire__ discover failed (exit %d)", ee.ExitCode()), stderr)
		}
		return nil, fmt.Errorf("%s: __hire__ discover: %v", path, err)
	}
	var dj discoverJSON
	if jerr := json.Unmarshal(stdout, &dj); jerr != nil {
		return nil, discoverErr(path, fmt.Sprintf("invalid discover JSON: %v", jerr), stderr)
	}

	L := lua.NewState()
	f := &File{L: L, Tasks: make(map[string]*Task)}
	for _, dt := range dj.Tasks {
		if strings.HasPrefix(dt.Name, "__") {
			f.Close()
			return nil, fmt.Errorf("%s: task name %q must not start with __ (reserved for contract verbs)", path, dt.Name)
		}
		if dt.Name == "" {
			f.Close()
			return nil, fmt.Errorf("%s: discover returned a task with an empty name", path)
		}
		if _, dup := f.Tasks[dt.Name]; dup {
			f.Close()
			return nil, fmt.Errorf("%s: discover listed task %q twice", path, dt.Name)
		}
		task := &Task{Name: dt.Name, Desc: dt.Desc, File: path}
		for argName, a := range dt.Args {
			if argName == "shell" {
				f.Close()
				return nil, fmt.Errorf("%s: task %q: argument name %q is reserved (--shell selects the shell)", path, dt.Name, argName)
			}
			if task.Args == nil {
				task.Args = make(map[string]argparse.Spec)
			}
			sp := argparse.Spec{Type: a.Type, Required: a.Required, Desc: a.Desc}
			if sp.Type == "" {
				sp.Type = "string"
			}
			if !validArgType(sp.Type) {
				f.Close()
				return nil, fmt.Errorf("%s: task %q: arg %q: invalid type %q (use \"string\", \"bool\" or \"num\")", path, dt.Name, argName, sp.Type)
			}
			task.Args[argName] = sp
		}
		task.Fn = execFn(L, prog, extra, dt.Name)
		f.Tasks[dt.Name] = task
		f.Order = append(f.Order, dt.Name)
	}
	if len(f.Order) == 0 {
		f.Close()
		return nil, fmt.Errorf("%s: discover returned no tasks", path)
	}
	sort.Strings(f.Order)
	return f, nil
}

// runDiscover executes `<prog> <extra...> __hire__ discover`,
// returning stdout and stderr alongside the run error.
func runDiscover(prog string, extra []string) ([]byte, string, error) {
	cmd := exec.Command(prog, append(append([]string{}, extra...), "__hire__", "discover")...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.String(), err
}

// discoverErr names the taskfile and appends a capped stderr excerpt,
// so a failing external program shows why it failed.
func discoverErr(path, msg, stderr string) error {
	if ex := excerpt(stderr); ex != "" {
		return fmt.Errorf("%s: %s (stderr: %s)", path, msg, ex)
	}
	return fmt.Errorf("%s: %s", path, msg)
}

// excerpt clips stderr for error messages: one bad taskfile must not
// flood the terminal with kilobytes of its own noise.
func excerpt(s string) string {
	s = strings.TrimSpace(s)
	const max = 512
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// TaskExitError marks an external task's non-zero exit so the runner
// can render the spec §11 single-name form instead of the generic wrap.
type TaskExitError struct {
	Name string
	Code int
}

func (e *TaskExitError) Error() string {
	return fmt.Sprintf("failed (exit %d)", e.Code)
}

// AsTaskExitError digs a *TaskExitError out of a callTask error. The
// exec closure raises it as a Lua error object, which PCall wraps in
// *lua.ApiError (no Unwrap method), so plain errors.As can not reach
// it without this ApiError.Object hop.
func AsTaskExitError(err error) (*TaskExitError, bool) {
	var tee *TaskExitError
	if errors.As(err, &tee) {
		return tee, true
	}
	var ae *lua.ApiError
	if errors.As(err, &ae) {
		if ud, ok := ae.Object.(*lua.LUserData); ok {
			tee, ok := ud.Value.(*TaskExitError)
			return tee, ok
		}
	}
	return nil, false
}

// execFn is the task Fn for external taskfiles: re-exec the program with
// the run verb, HIRE_ARGS from the environment ("{}" when unset),
// inherited stdio; a non-zero exit becomes a Lua error, isomorphic to
// how failing Lua task functions surface.
func execFn(L *lua.LState, prog string, extra []string, name string) *lua.LFunction {
	return L.NewFunction(func(L *lua.LState) int {
		args := os.Getenv("HIRE_ARGS")
		if args == "" {
			args = "{}"
		}
		cmd := exec.Command(prog, append(append([]string{}, extra...), "__hire__", "run", name)...)
		cmd.Env = append(os.Environ(), "HIRE_ARGS="+args)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				// Raised as a non-string error object: PCall keeps it
				// as ApiError.Object, and the runner composes §11's
				// "task <name> failed (exit N)" from it.
				ud := L.NewUserData()
				ud.Value = &TaskExitError{Name: name, Code: ee.ExitCode()}
				L.Error(ud, 1)
			}
			L.RaiseError("task %q failed: %v", name, err)
		}
		return 0
	})
}
