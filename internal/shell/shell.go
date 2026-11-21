package shell

import (
	"fmt"
	"runtime"
)

type Spec struct {
	Path string
	Args []string
}

func (s Spec) Argv(cmd string) []string {
	out := make([]string, 0, len(s.Args)+1)
	out = append(out, s.Args...)
	out = append(out, cmd)
	return out
}

func argsFor(name string) []string {
	switch name {
	case "pwsh":
		return []string{"-NoProfile", "-Command"}
	case "bash":
		return []string{"-euo", "pipefail", "-c"}
	case "sh":
		return []string{"-e", "-c"}
	default:
		return []string{"-c"}
	}
}

// DefaultName is the platform default shell.
func DefaultName() string {
	if runtime.GOOS == "windows" {
		return "pwsh"
	}
	return "bash"
}

// IsBuiltin reports whether name is one of the builtin shells that can
// host a persistent instance (the sentinel protocol is scripted per
// builtin shell only).
func IsBuiltin(name string) bool {
	switch name {
	case "pwsh", "bash", "sh":
		return true
	}
	return false
}

// persistentArgs are the stdin command-channel spawn args: the shell
// reads commands from stdin and stays alive for the whole task.
func persistentArgs(name string) []string {
	switch name {
	case "pwsh":
		return []string{"-NoProfile", "-NonInteractive", "-Command", "-"}
	case "bash":
		return []string{"--norc", "--noprofile", "-s"}
	case "sh":
		return []string{"-s"}
	}
	return nil
}

// ResolvePersistent resolves a shell for a persistent instance: builtin
// names only (registry path wins, persistent args). Third-party shells
// have no scripted stdin protocol, so they are rejected.
func ResolvePersistent(name string, registry map[string]string) (Spec, error) {
	if !IsBuiltin(name) {
		return Spec{}, fmt.Errorf("shell %q: persistent instances need a builtin shell (pwsh/bash/sh)", name)
	}
	if path, ok := registry[name]; ok {
		return Spec{Path: path, Args: persistentArgs(name)}, nil
	}
	spec, err := resolveBuiltin(name)
	if err != nil {
		return Spec{}, err
	}
	spec.Args = persistentArgs(name)
	return spec, nil
}

func Resolve(name string, registry map[string]string) (Spec, error) {
	if name == "" {
		name = DefaultName()
	}
	if path, ok := registry[name]; ok {
		return Spec{Path: path, Args: argsFor(name)}, nil
	}
	return resolveBuiltin(name)
}
