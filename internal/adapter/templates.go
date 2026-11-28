package adapter

// Taskfile templates for `hire init` (spec §10 item 1): one runnable
// hello task plus comments; the external-language ones embed the full
// __hire__ contract (python's contract lives in the generated hire.py
// SDK module). docs/contract.md reprints the external listings
// verbatim — edit both together.

// LuaTemplate is the default scaffold: the native adapter, no contract
// code needed.
const LuaTemplate = `-- hire Taskfile.lua - a taskfile is a plain Lua script: tasks are globals.
-- Run one with "hire <name>", list them with "hire ls".
--
-- Three ways to declare a task:
--   clean = "rm -rf dist/"               -- string = one shell command
--   steps = { "echo one", "echo two" }   -- array of strings = a sequence
--   full = task {                        -- full form: desc + args + run
--     desc = "build and release",
--     args = { tag = req "version tag" }, -- req = required string; bool = flag
--     run = function(args)
--       x.sh("git tag " .. args.tag)     -- x.sh runs a shell command
--     end,
--   }
--
-- Inside a task: x.sh(cmd) shells out, x.sh_out(cmd) captures stdout as a
-- string, x.echo(...) prints, x.run("other") calls a sibling task,
-- x.env("NAME") reads the environment.
-- The full reference lives in docs/lua.md.

hello = task {
  desc = "say hello (edit me)",
  run = function()
    x.echo("hello from hire")
  end,
}
`

// PythonSDKTemplate is hire.py, the __hire__ contract SDK for Python
// taskfiles: the @task decorator registry, the arg-declaration helpers,
// and dispatch() for the contract verbs. Edit together with
// PythonTemplate and the Python listings in docs/contract.md — they
// must stay identical.
const PythonSDKTemplate = `"""hire.py - the hire contract SDK for Python taskfiles.

A taskfile is any program speaking the __hire__ contract; this SDK is a
convenience, not a requirement. Two legal shapes:

  SDK (what hire init --lang python scaffolds): keep this hire.py beside
  a thin Taskfile.py that imports it and only holds tasks:

      from hire import task, opt

      @task("hello", desc="say hello", args={"name": opt("who to greet")})
      def hello(a):
          print(f"hello, {a.name or 'hire'}")

      import hire
      if __name__ == "__main__":
          hire.dispatch()

  Raw single-file: write the two __hire__ verbs by hand in one program
  (a full listing lives in docs/contract.md) - hire.py adds no private
  mechanism, only boilerplate you would otherwise retype.

hire calls your program two ways:
  python Taskfile.py __hire__ discover     -> print {"tasks": [...]} to stdout
  python Taskfile.py __hire__ run <name>   -> run the task
Parsed CLI arguments arrive as JSON in the HIRE_ARGS environment variable:
{"name": "v2", "port": 8080, "_": ["positional"]} - flags plus the "_"
positional array, {} when the task declares no args.

discover must stay pure: describe tasks, no side effects.
"""

import atexit
import json
import os
import secrets
import shutil
import subprocess
import sys

# registry maps task name -> (spec, fn). Insertion-ordered: discover
# lists tasks in the order their @task decorators ran (dicts keep
# insertion order on Python 3.7+).
_registry = {}


def opt(desc=""):
    """An optional string argument. The dict key in args={} is the name."""
    return {"type": "string", "required": False, "desc": desc}


def req(desc=""):
    """A required string argument. The dict key in args={} is the name."""
    return {"type": "string", "required": True, "desc": desc}


def flag(desc=""):
    """An optional bool argument. The dict key in args={} is the name."""
    return {"type": "bool", "required": False, "desc": desc}


def num(desc=""):
    """An optional number argument (an Args attribute is a float). The
    dict key in args={} is the name."""
    return {"type": "num", "required": False, "desc": desc}


def task(name, desc="", args=None, when=None):
    """Register the decorated function as a hire task. when is an optional
    discover guard, e.g. {"os": "windows"} or {"os": ["linux", "darwin"]}."""

    def register(fn):
        spec = {"name": name, "desc": desc, "args": args or {}}
        if when is not None:
            spec["when"] = when
        _registry[name] = (spec, fn)
        return fn

    return register


class Args:
    """The parsed HIRE_ARGS JSON: a.name (None when absent), a.get(name,
    default) for explicit fallbacks, a._ for the positional array."""

    def __init__(self, raw):
        self._raw = raw
        self._ = raw.get("_", [])

    def __getattr__(self, name):
        return self._raw.get(name)

    def get(self, name, default=None):
        return self._raw.get(name, default)


# --- persistent shell instances (ported from the Lua x.sh protocol) ---
# Shell.run shares one shell process per (instance, shell) so "cd" and
# "export" survive across calls; instance=0 collects commands for
# eval/iex, a builtin "shell:" prefix and an explicit shell= run one-shot,
# a non-zero exit raises, and every child is torn down at task end.

_BUILTIN_SHELLS = ("pwsh", "bash", "sh")
_INSTANCE_CLOSE_WAIT = 0.5
_instances = {}          # (instance, shell) -> _Instance
_export_buf = []
_task_failed = False
_warned = False


class ShellError(Exception):
    """A shell command failed or its persistent instance died."""


def _default_shell():
    return os.environ.get("HIRE_SHELL") or ("pwsh" if os.name == "nt" else "bash")


def _resolve_shell(name):
    if name == "pwsh":
        for candidate in ("pwsh", "pwsh.exe", "powershell", "powershell.exe"):
            path = shutil.which(candidate)
            if path:
                return path
        raise ShellError("shell pwsh not found in PATH")
    if name == "bash" and os.name == "nt":
        # bash.exe on PATH is often the WSL launcher; prefer Git Bash.
        git = shutil.which("git")
        if git:
            candidate = os.path.join(os.path.dirname(os.path.dirname(git)), "bin", "bash.exe")
            if os.path.isfile(candidate):
                return candidate
    if name in _BUILTIN_SHELLS:
        for candidate in (name, name + ".exe"):
            path = shutil.which(candidate)
            if path:
                return path
        raise ShellError("shell %s not found in PATH" % name)
    return shutil.which(name) or name   # third-party: one-shot via PATH


def _one_shot_argv(name, cmd):
    if name == "pwsh":
        return [_resolve_shell(name), "-NoProfile", "-Command", cmd]
    if name == "bash":
        return [_resolve_shell(name), "-euo", "pipefail", "-c", cmd]
    if name == "sh":
        return [_resolve_shell(name), "-e", "-c", cmd]
    return [_resolve_shell(name), "-c", cmd]


def _persistent_argv(name):
    if name == "pwsh":
        return [_resolve_shell(name), "-NoProfile", "-NonInteractive", "-Command", "-"]
    if name == "bash":
        return [_resolve_shell(name), "--norc", "--noprofile", "-s"]
    if name == "sh":
        return [_resolve_shell(name), "-s"]
    return [_resolve_shell(name), "-c"]


def _warn_once(name):
    global _warned
    if _warned:
        return
    _warned = True
    if name not in _BUILTIN_SHELLS:
        sys.stderr.write("hire: warning: shell %r does not support persistent instances; running one-shot\n" % name)
    else:
        sys.stderr.write("hire: warning: pwsh persistent instances are Windows-only; running one-shot\n")


def _split_shell_prefix(cmd):
    head, sep, rest = cmd.partition(":")
    if sep and head in _BUILTIN_SHELLS:
        return head, rest
    return None, cmd


def _one_shot(name, cmd):
    rc = subprocess.Popen(_one_shot_argv(name, cmd)).wait()
    if rc != 0:
        raise ShellError("command failed (exit %d): %s" % (rc, cmd))


class _Instance:
    def __init__(self, name):
        self.name = name
        self.proc = subprocess.Popen(_persistent_argv(name), stdin=subprocess.PIPE,
                                     stdout=subprocess.PIPE)
        self.closed = False
        if name == "pwsh":
            # $LASTEXITCODE is unset until the first native command runs.
            self.proc.stdin.write(b"$LASTEXITCODE = 0\n")
            self.proc.stdin.flush()

    def send(self, cmd):
        nonce = secrets.token_hex(8)
        prefix = "__HIRE_RC:" + nonce + ":"
        if self.name == "pwsh":
            echo = 'Write-Output "__HIRE_RC:' + nonce + ':$(if ($?) { $LASTEXITCODE } else { 1 })"'
        else:
            echo = "printf '__HIRE_RC:%s:%s\\n' " + nonce + ' "$?"'
        try:
            self.proc.stdin.write((cmd + "\n" + echo + "\n").encode("utf-8"))
            self.proc.stdin.flush()
        except (OSError, ValueError):
            raise ShellError("shell instance died while running: " + cmd)
        while True:
            raw = self.proc.stdout.readline()
            if not raw:
                raise ShellError("shell instance exited before reporting status: " + cmd)
            line = raw.decode("utf-8", "replace").rstrip("\r\n")
            if line.startswith(prefix):
                try:
                    return int(line[len(prefix):])
                except ValueError:
                    continue
            if line:
                sys.stdout.write(line + "\n")
                sys.stdout.flush()

    def teardown(self):
        if self.closed:
            return
        self.closed = True
        try:
            self.proc.stdin.close()
        except OSError:
            pass
        try:
            self.proc.wait(timeout=_INSTANCE_CLOSE_WAIT)
        except subprocess.TimeoutExpired:
            self.proc.kill()
            self.proc.wait()


class Shell:
    """A family of persistent shell instances; close() tears them all down."""

    def run(self, cmd, instance=1, shell=None):
        if not isinstance(instance, int) or isinstance(instance, bool) or instance < 0:
            raise ShellError("Shell.run: instance must be an integer >= 0")
        name, rest = _split_shell_prefix(cmd)
        if name:
            _one_shot(name, rest)
            return
        name = shell or _default_shell()
        if instance == 0:
            if name not in _BUILTIN_SHELLS:
                raise ShellError("instance=0 (export mode) needs a builtin shell (pwsh/bash/sh)")
            _export_buf.append(cmd)
            return
        if shell or name not in _BUILTIN_SHELLS or (name == "pwsh" and os.name != "nt"):
            _warn_once(name)
            _one_shot(name, cmd)
            return
        key = (instance, name)
        inst = _instances.get(key)
        if inst is None:
            inst = _Instance(name)
            _instances[key] = inst
        rc = inst.send(cmd)
        if rc != 0:
            raise ShellError("command failed (exit %d): %s" % (rc, cmd))

    def out(self, cmd):
        """One-shot: capture stdout as text, strip one trailing newline,
        leave stderr on the terminal, raise on a non-zero exit. Never uses
        a persistent shell instance (capture and state are orthogonal)."""
        name, rest = _split_shell_prefix(cmd)
        if name:
            cmd = rest
        else:
            name = _default_shell()
        proc = subprocess.Popen(_one_shot_argv(name, cmd), stdout=subprocess.PIPE)
        raw, _ = proc.communicate()
        if proc.returncode != 0:
            raise ShellError("command failed (exit %d): %s" % (proc.returncode, cmd))
        text = raw.decode("utf-8", "replace")
        if text.endswith("\n"):
            text = text[:-1]
            if text.endswith("\r"):
                text = text[:-1]
        return text

    def close(self):
        for inst in _instances.values():
            inst.teardown()
        _instances.clear()

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc, tb):
        self.close()
        return False


def _teardown():
    for inst in _instances.values():
        inst.teardown()
    _instances.clear()
    if _task_failed and _export_buf:
        sys.stderr.write("hire: task failed; export aborted, collected commands discarded\n")
    elif _export_buf:
        sys.stdout.write("\n".join(_export_buf) + "\n")
    _export_buf.clear()


atexit.register(_teardown)


def dispatch():
    """Handle the __hire__ contract verbs for the registered tasks."""
    global _task_failed
    if len(sys.argv) < 3 or sys.argv[1] != "__hire__":
        sys.exit("this file is a hire taskfile; run its tasks via the hire CLI")
    if sys.argv[2] == "discover":
        json.dump({"tasks": [spec for spec, _ in _registry.values()]}, sys.stdout)
        return
    if sys.argv[2] == "run" and len(sys.argv) >= 4:
        name = sys.argv[3]
        if name not in _registry:
            sys.exit(f"hire: unknown task {name!r}")
        spec, fn = _registry[name]
        raw = json.loads(os.environ.get("HIRE_ARGS", "{}"))
        for arg, decl in spec["args"].items():
            # hire marshals 8080.0 as 8080, which json parses back as an
            # int; declared num args are coerced to floats here.
            if decl.get("type") == "num" and type(raw.get(arg)) is int:
                raw[arg] = float(raw[arg])
        try:
            fn(Args(raw))
        except BaseException:
            _task_failed = True
            raise
        return
    sys.exit("usage: Taskfile.py __hire__ discover | run <name>")
`

// PythonTemplate is the user-facing thin taskfile paired with
// PythonSDKTemplate (hire.py): only the tasks - the contract, the
// parsing, even the __hire__ verbs live in hire.py.
const PythonTemplate = `from hire import task, opt

@task("hello", desc="say hello (edit me)", args={"name": opt("who to greet")})
def hello(a):
    print(f"hello, {a.name or 'hire'}")

import hire
if __name__ == "__main__":
    hire.dispatch()
`

// NodeSDKTemplate is hire.js, the decorator-free __hire__ contract SDK
// for Node taskfiles: a flat registry, the arg-declaration helpers and
// dispatch() for the two contract verbs. Edit together with
// NodeTemplate and the Node listings in docs/contract.md — they must
// stay identical. (No template literals: this is a Go raw string.)
const NodeSDKTemplate = `// hire.js - the hire contract SDK for Node taskfiles.
//
// A taskfile is any program speaking the __hire__ contract; this SDK is a
// convenience, not a requirement. Two legal shapes:
//
//   SDK (what hire init --lang node scaffolds): keep this hire.js beside
//   a thin Taskfile.js that only registers tasks:
//
//       const { task, opt, dispatch } = require("./hire.js");
//       task("hello", { desc: "say hello", args: { name: opt("who") } },
//            (a) => { console.log("hello, " + (a.name || "hire")); });
//       dispatch();
//
//   Raw single-file: write the two __hire__ verbs by hand in one program
//   (a full listing lives in docs/contract.md) - hire.js adds no private
//   mechanism, only boilerplate you would otherwise retype.
//
// hire calls your program two ways:
//   node Taskfile.js __hire__ discover     -> print {"tasks": [...]} to stdout
//   node Taskfile.js __hire__ run <name>   -> run the task
// Parsed CLI arguments arrive as JSON in the HIRE_ARGS environment variable:
// {"name": "v2", "port": 8080, "_": ["positional"]} - flags plus the "_"
// positional array, {} when the task declares no args.
//
// discover must stay pure: describe tasks, no side effects.
"use strict";

const fs = require("fs");
const path = require("path");
const crypto = require("crypto");
const { spawn, spawnSync } = require("child_process");

const _registry = new Map();

function task(name, opts, fn) {
  const spec = { name: name, desc: (opts && opts.desc) || "", args: (opts && opts.args) || {} };
  // when is an optional discover guard: { os: "windows" } or { os: ["linux", "darwin"] }.
  if (opts && opts.when) spec.when = opts.when;
  _registry.set(name, { spec: spec, fn: fn });
}

function opt(desc) { return { type: "string", required: false, desc: desc || "" }; }
function req(desc) { return { type: "string", required: true, desc: desc || "" }; }
function flag(desc) { return { type: "bool", required: false, desc: desc || "" }; }
function num(desc) { return { type: "num", required: false, desc: desc || "" }; }

// --- persistent shell instances (ported from the Lua x.sh protocol) ---
// Shell.run shares one shell process per (instance, shell); a builtin
// "shell:" prefix and an explicit shell= run one-shot; instance=0
// collects commands for eval/iex. Commands are queued to the child in
// call order; the sentinel line carries the exit code and is never
// printed. dispatch drains every instance before the process exits.
// LIMIT (Windows pipes have no sync fd): a non-zero command surfaces as
// an uncaught Error -> process exit 1 (NOT catchable, unlike C++/Lua/Python),
// and commands already queued still run. Recover from failure by fixing
// the command, not by try/catch.

const BUILTIN_SHELLS = ["pwsh", "bash", "sh"];
const _instances = new Map();     // key: instance + ":" + shell
let _exportBuf = [];
let _taskFailed = false;
let _warned = false;
let _finishing = false;
let _finishDone = false;

function _which(name) {
  const dirs = (process.env.PATH || "").split(path.delimiter);
  const names = process.platform === "win32" ? [name, name + ".exe", name + ".cmd", name + ".bat"] : [name];
  for (const dir of dirs) {
    for (const candidate of names) {
      const full = path.join(dir, candidate);
      try { fs.accessSync(full, fs.constants.X_OK); return full; } catch (e) { /* keep looking */ }
    }
  }
  return null;
}

function _defaultShell() {
  return process.env.HIRE_SHELL || (process.platform === "win32" ? "pwsh" : "bash");
}

function _resolveShell(name) {
  if (name === "pwsh") {
    for (const c of ["pwsh.exe", "pwsh", "powershell.exe", "powershell"]) {
      const p = _which(c);
      if (p) return p;
    }
    throw new Error("shell pwsh not found in PATH");
  }
  if (name === "bash" && process.platform === "win32") {
    // bash.exe on PATH is often the WSL launcher; prefer Git Bash.
    const git = _which("git");
    if (git) {
      const candidate = path.join(path.dirname(path.dirname(git)), "bin", "bash.exe");
      if (fs.existsSync(candidate)) return candidate;
    }
  }
  if (BUILTIN_SHELLS.indexOf(name) >= 0) {
    const p = _which(name);
    if (p) return p;
    throw new Error("shell " + name + " not found in PATH");
  }
  return _which(name) || name;   // third-party: one-shot via PATH
}

function _oneShotArgs(name, cmd) {
  if (name === "pwsh") return ["-NoProfile", "-Command", cmd];
  if (name === "bash") return ["-euo", "pipefail", "-c", cmd];
  if (name === "sh") return ["-e", "-c", cmd];
  return ["-c", cmd];
}

function _persistentArgs(name) {
  if (name === "pwsh") return ["-NoProfile", "-NonInteractive", "-Command", "-"];
  if (name === "bash") return ["--norc", "--noprofile", "-s"];
  if (name === "sh") return ["-s"];
  return ["-c"];
}

function _warnOnce(name) {
  if (_warned) return;
  _warned = true;
  if (BUILTIN_SHELLS.indexOf(name) < 0)
    process.stderr.write("hire: warning: shell " + name + " does not support persistent instances; running one-shot\n");
  else
    process.stderr.write("hire: warning: pwsh persistent instances are Windows-only; running one-shot\n");
}

function _splitShellPrefix(cmd) {
  const i = cmd.indexOf(":");
  if (i > 0 && BUILTIN_SHELLS.indexOf(cmd.slice(0, i)) >= 0) {
    return { name: cmd.slice(0, i), rest: cmd.slice(i + 1) };
  }
  return null;
}

function _oneShot(name, cmd) {
  const r = spawnSync(_resolveShell(name), _oneShotArgs(name, cmd), { stdio: "inherit" });
  if (r.error) throw new Error("Shell: " + r.error.message);
  if (r.status !== 0) throw new Error("command failed (exit " + r.status + "): " + cmd);
}

class ShellInstance {
  constructor(name) {
    this.name = name;
    this.proc = spawn(_resolveShell(name), _persistentArgs(name), { stdio: ["pipe", "pipe", "inherit"] });
    this.buf = "";
    this.pending = 0;
    this.closing = false;
    this.closed = false;
    this.exited = false;
    this.prefixes = new Set();
    this.proc.on("error", (e) => { process.stderr.write("hire: shell: " + e.message + "\n"); });
    this.proc.stdout.on("data", (chunk) => this._onData(chunk.toString("utf8")));
    this.proc.on("close", () => {
      if (this.pending > 0) {
        _taskFailed = true;
        throw new Error("shell instance exited before reporting status: " + this.name);
      }
      this.exited = true;
      _maybeFinishDone();
    });
    if (name === "pwsh") this.proc.stdin.write("$LASTEXITCODE = 0\n");  // seed
  }

  _onData(text) {
    this.buf += text;
    let i;
    while ((i = this.buf.indexOf("\n")) >= 0) {
      const line = this.buf.slice(0, i).replace(/\r$/, "");
      this.buf = this.buf.slice(i + 1);
      let sentinel = false;
      for (const prefix of this.prefixes) {
        if (line.startsWith(prefix)) {
          const rest = line.slice(prefix.length);
          this.prefixes.delete(prefix);
          this.pending--;
          sentinel = true;
          if (/^\d+$/.test(rest) && parseInt(rest, 10) !== 0) {
            _taskFailed = true;
            throw new Error("command failed (exit " + parseInt(rest, 10) + ")");
          }
          break;
        }
      }
      if (!sentinel && line !== "") fs.writeSync(1, line + "\n");
      if (this.closing && this.pending === 0) this.teardown();
    }
    _maybeFinishDone();
  }

  send(cmd) {
    const nonce = crypto.randomBytes(8).toString("hex");
    const prefix = "__HIRE_RC:" + nonce + ":";
    const echo = this.name === "pwsh"
      ? 'Write-Output "__HIRE_RC:' + nonce + ':$(if ($?) { $LASTEXITCODE } else { 1 })"'
      : "printf '__HIRE_RC:" + nonce + ":%s\\n' \"$?\"";
    this.prefixes.add(prefix);
    this.pending++;
    this.proc.stdin.write(cmd + "\n" + echo + "\n");
  }

  teardown() {
    if (this.closed) return;
    this.closed = true;
    try { this.proc.stdin.end(); } catch (e) { /* already closed */ }
    if (this.proc.exitCode === null && this.proc.signalCode === null) this.proc.kill();
  }
}

// _maybeFinishDone flushes the instance=0 buffer only after every queued
// output line has been read, so export text stays ordered and is
// discarded when a task fails (ctx.go:405-426).
function _maybeFinishDone() {
  if (!_finishing || _finishDone) return;
  for (const inst of _instances.values()) {
    if (!inst.exited) return;
  }
  _finishDone = true;
  if (_taskFailed && _exportBuf.length) {
    process.stderr.write("hire: task failed; export aborted, collected commands discarded\n");
  } else if (_exportBuf.length) {
    fs.writeSync(1, _exportBuf.join("\n") + "\n");
  }
  _exportBuf = [];
}

function _finishInstances() {
  _finishing = true;
  for (const inst of _instances.values()) {
    inst.closing = true;
    if (inst.pending === 0) inst.teardown();
  }
  _maybeFinishDone();
}

process.on("exit", () => {
  for (const inst of _instances.values()) inst.teardown();
  _instances.clear();
});

class Shell {
  run(cmd, instance, shell) {
    const inst = instance === undefined ? 1 : instance;
    if (!Number.isInteger(inst) || inst < 0)
      throw new Error("Shell.run: instance must be an integer >= 0");
    const prefix = _splitShellPrefix(cmd);
    if (prefix) { _oneShot(prefix.name, prefix.rest); return; }
    const name = shell || _defaultShell();
    if (inst === 0) {
      if (BUILTIN_SHELLS.indexOf(name) < 0)
        throw new Error("instance=0 (export mode) needs a builtin shell (pwsh/bash/sh)");
      _exportBuf.push(cmd);
      return;
    }
    if (shell || BUILTIN_SHELLS.indexOf(name) < 0 || (name === "pwsh" && process.platform !== "win32")) {
      _warnOnce(name);
      _oneShot(name, cmd);
      return;
    }
    const key = inst + ":" + name;
    let child = _instances.get(key);
    // A closed/exited instance must not swallow the next command: treat it
    // as a cache miss and spawn a fresh shell (Python clears its map too).
    if (!child || child.closed || child.exited) {
      child = new ShellInstance(name);
      _instances.set(key, child);
    }
    child.send(cmd);
  }

  // One-shot: capture stdout as text, strip one trailing newline, leave
  // stderr on the terminal, throw on a non-zero exit. Never reuses a
  // persistent instance (capture and state are orthogonal).
  out(cmd) {
    const prefix = _splitShellPrefix(cmd);
    const name = prefix ? prefix.name : _defaultShell();
    if (prefix) cmd = prefix.rest;
    const r = spawnSync(_resolveShell(name), _oneShotArgs(name, cmd),
                        { stdio: ["inherit", "pipe", "inherit"], encoding: "utf8" });
    if (r.error) throw new Error("Shell: " + r.error.message);
    if (r.status !== 0) throw new Error("command failed (exit " + r.status + "): " + cmd);
    let text = r.stdout;
    if (text.endsWith("\n")) {
      text = text.slice(0, -1);
      if (text.endsWith("\r")) text = text.slice(0, -1);
    }
    return text;
  }

  close() { _finishInstances(); }
}

function dispatch() {
  const verb = process.argv[2];
  const action = process.argv[3];
  const name = process.argv[4];
  if (verb !== "__hire__") {
    process.stderr.write("this file is a hire taskfile; run its tasks via the hire CLI\n");
    process.exit(1);
  }
  if (action === "discover") {
    const tasks = [];
    for (const entry of _registry.values()) tasks.push(entry.spec);
    process.stdout.write(JSON.stringify({ tasks: tasks }));
    return;
  }
  if (action !== "run" || !name) {
    process.stderr.write("usage: Taskfile.js __hire__ discover | run <name>\n");
    process.exit(1);
  }
  const entry = _registry.get(name);
  if (!entry) {
    process.stderr.write("hire: unknown task " + name + "\n");
    process.exit(1);
  }
  let raw;
  try {
    raw = JSON.parse(process.env.HIRE_ARGS || "{}");
  } catch (e) {
    process.stderr.write("hire: invalid HIRE_ARGS: " + e.message + "\n");
    process.exit(1);
  }
  for (const key of Object.keys(entry.spec.args)) {
    if (entry.spec.args[key].type === "num" && raw[key] !== undefined) {
      raw[key] = Number(raw[key]);
    }
  }
  const a = Object.assign({}, raw);
  a._ = raw._ || [];
  try {
    entry.fn(a);
  } catch (e) {
    _taskFailed = true;
    _finishInstances();
    throw e;
  }
  _finishInstances();
}

module.exports = { task, opt, req, flag, num, dispatch, Shell, _instances };
`

// NodeTemplate is the user-facing thin taskfile paired with
// NodeSDKTemplate (hire.js): only the tasks - the contract, the arg
// parsing and even the __hire__ verbs live in the SDK module.
const NodeTemplate = `const { task, opt, dispatch } = require("./hire.js");

task("hello", { desc: "say hello (edit me)", args: { name: opt("who to greet") } },
  (a) => {
    process.stdout.write("hello, " + (a.name || "hire") + "\n");
  });

dispatch();
`

// CppHeaderTemplate is hire.hpp, the __hire__ contract SDK for C++
// taskfiles: registry/dispatch, discover JSON emission, and a real
// HIRE_ARGS parser (escapes handled). LEGACY form: function-form
// taskfiles (CppTemplate) are the primary and need no hire.hpp in the
// user directory — hire writes this header beside its generated wrapper
// itself. The hand-written contract form stays legal (spec §4.6); edit
// together with the C++ listings in docs/contract.md — they must stay
// identical.
const CppHeaderTemplate = `// hire.hpp - the hire contract SDK for C++ taskfiles (legacy form).
//
// Include it from Taskfile.cpp, define hire_main with your tasks; the
// header provides main() itself. The function form (plain top-level
// functions, parsed by your clang++) is the primary shape now — this
// header form stays fully legal:
//
//     #include "hire.hpp"
//
//     void hire_main(hire::registry& reg) {
//         reg.task("hello", "say hello")
//            .opt("name", "who to greet")
//            .run([](const hire::args& a) {
//                std::cout << "hello, " << a.get_string("name", "hire") << "\n";
//            });
//     }
//
// hire calls your compiled program two ways:
//   <built> __hire__ discover    -> print {"tasks": [...]} (must stay pure)
//   <built> __hire__ run <name>  -> run one task; parsed flags arrive as JSON
//                                  in the HIRE_ARGS env var. Full contract:
//                                  docs/contract.md.
#pragma once
#include <cstdlib>
#include <functional>
#include <iostream>
#include <map>
#include <string>
#include <utility>
#include <vector>
#include <memory>
#include <sstream>
#include <initializer_list>
#include <stdexcept>
#include <cstdio>
#include <ctime>
#ifdef _WIN32
#ifndef WIN32_LEAN_AND_MEAN
#define WIN32_LEAN_AND_MEAN
#endif
#include <windows.h>
#else
#include <unistd.h>
#include <sys/wait.h>
#include <signal.h>
#include <cerrno>
#include <time.h>
#endif

namespace hire {

// args is the parsed HIRE_ARGS JSON: string/bool/num flags plus the "_" array.
class args {
public:
    std::string get_string(const std::string& key, const std::string& fallback = "") const {
        std::map<std::string, std::string>::const_iterator it = raw_.find(key);
        return it != raw_.end() ? it->second : fallback;
    }
    bool get_bool(const std::string& key, bool fallback = false) const {
        std::map<std::string, std::string>::const_iterator it = raw_.find(key);
        return it != raw_.end() ? it->second == "true" : fallback;
    }
    double get_num(const std::string& key, double fallback = 0.0) const {
        std::map<std::string, std::string>::const_iterator it = raw_.find(key);
        return it != raw_.end() ? std::atof(it->second.c_str()) : fallback;
    }
    const std::vector<std::string>& positional() const { return pos_; }

    static args parse(const std::string& json);

private:
    std::map<std::string, std::string> raw_;
    std::vector<std::string> pos_;
};

// arg_spec mirrors one entry of the task's discover "args" object.
struct arg_spec {
    std::string type;     // "string" | "bool" | "num"
    std::string desc;
    bool required;
};

// task_def is one registered task; chain arg()/run() to fill it in.
// (The specs member is arg_specs, not args: a member named args would
// shadow the hire::args class inside this struct.)
struct task_def {
    std::string name;
    std::string desc;
    std::vector<std::pair<std::string, arg_spec> > arg_specs;  // declaration order
    std::function<void(const args&)> body;

    task_def& arg(std::string name, std::string type, bool required, std::string desc = "") {
        arg_specs.push_back(std::make_pair(std::move(name), arg_spec{std::move(type), std::move(desc), required}));
        return *this;
    }
    // Sugar mirroring the Lua adapter: req = required string, opt =
    // optional string, flag = optional bool, num = optional number;
    // .arg stays for the rest (e.g. a required num).
    task_def& req(std::string name, std::string desc = "") { return arg(std::move(name), "string", true, std::move(desc)); }
    task_def& opt(std::string name, std::string desc = "") { return arg(std::move(name), "string", false, std::move(desc)); }
    task_def& flag(std::string name, std::string desc = "") { return arg(std::move(name), "bool", false, std::move(desc)); }
    task_def& num(std::string name, std::string desc = "") { return arg(std::move(name), "num", false, std::move(desc)); }
    task_def& run(std::function<void(const args&)> fn) {
        body = std::move(fn);
        return *this;
    }
};

// registry holds tasks in registration order (= discover order).
class registry {
public:
    std::vector<task_def> tasks;

    task_def& task(std::string name, std::string desc = "") {
        task_def t;
        t.name = std::move(name);
        t.desc = std::move(desc);
        tasks.push_back(std::move(t));
        return tasks.back();
    }
};

// ---- persistent shell instances (ported from the Lua x.sh protocol) ----
//
// hire::sh runs a command in a shell. By default the command shares one
// persistent shell process per (inst, shell), so "cd" and "export" survive
// across calls. inst = 0 collects commands for export mode; a builtin
// "shell:" prefix forces a one-shot. A non-zero command exit throws, and
// dispatch turns that into a clean non-zero process exit code.
namespace detail {

inline bool is_builtin(const std::string& name) {
    return name == "pwsh" || name == "bash" || name == "sh";
}

inline std::string default_shell_name() {
    const char* e = std::getenv("HIRE_SHELL");
    if (e != nullptr && *e != '\0') return std::string(e);
#ifdef _WIN32
    return "pwsh";
#else
    return "bash";
#endif
}

// split_shell_prefix recognizes only builtin shell names, so a Windows
// path like C:\dir stays a whole command (review focus: no false prefix).
inline bool split_shell_prefix(const std::string& cmd, std::string& name, std::string& rest) {
    size_t i = cmd.find(':');
    if (i == std::string::npos || i == 0) return false;
    name = cmd.substr(0, i);
    if (!is_builtin(name)) return false;
    rest = cmd.substr(i + 1);
    return true;
}

inline std::vector<std::string> oneshot_argv(const std::string& name, const std::string& cmd) {
    std::vector<std::string> a;
    a.push_back(name);
    if (name == "pwsh") { a.push_back("-NoProfile"); a.push_back("-Command"); }
    else if (name == "bash") { a.push_back("-euo"); a.push_back("pipefail"); a.push_back("-c"); }
    else if (name == "sh") { a.push_back("-e"); a.push_back("-c"); }
    else { a.push_back("-c"); }
    a.push_back(cmd);
    return a;
}

inline std::vector<std::string> persistent_argv(const std::string& name) {
    std::vector<std::string> a;
    a.push_back(name);
    if (name == "pwsh") { a.push_back("-NoProfile"); a.push_back("-NonInteractive"); a.push_back("-Command"); a.push_back("-"); }
    else if (name == "bash") { a.push_back("--norc"); a.push_back("--noprofile"); a.push_back("-s"); }
    else if (name == "sh") { a.push_back("-s"); }
    return a;
}

inline std::string new_nonce() {
    static unsigned long counter = 0;
    char buf[48];
    std::snprintf(buf, sizeof(buf), "%lx%lx", (unsigned long)std::time(nullptr), ++counter);
    return std::string(buf);
}

inline bool starts_with(const std::string& s, const std::string& p) {
    return s.size() >= p.size() && s.compare(0, p.size(), p) == 0;
}

inline std::string rtrim_crlf(const std::string& s) {
    size_t e = s.size();
    while (e > 0 && (s[e - 1] == '\r' || s[e - 1] == '\n')) --e;
    return s.substr(0, e);
}

struct inst_key {
    int inst;
    std::string shell;
    bool operator<(const inst_key& o) const {
        if (inst != o.inst) return inst < o.inst;
        return shell < o.shell;
    }
};

// A persistent shell child: a command channel (stdin) and a line-oriented
// output channel (stdout), plus the process handle for teardown.
struct shell_instance {
    std::string name;
#ifdef _WIN32
    void* in_w = nullptr;
    void* out_r = nullptr;
    void* proc = nullptr;
#else
    int in_fd = -1;
    int out_fd = -1;
    long pid = -1;
#endif
    bool closed = false;
    std::string rdbuf;
    size_t rdpos = 0;

    ~shell_instance() { teardown(); }

    long read_some(char* buf, long cap) {
#ifdef _WIN32
        DWORD n = 0;
        if (!ReadFile((HANDLE)out_r, buf, (DWORD)cap, &n, nullptr)) return 0;
        return (long)n;
#else
        for (;;) {
            ssize_t n = ::read(out_fd, buf, (size_t)cap);
            if (n < 0 && errno == EINTR) continue;
            return n > 0 ? (long)n : 0;
        }
#endif
    }

    bool read_line(std::string& out) {
        out.clear();
        for (;;) {
            if (rdpos < rdbuf.size()) {
                char c = rdbuf[rdpos++];
                if (c == '\n') return true;
                out += c;
                continue;
            }
            char tmp[1024];
            long n = read_some(tmp, (long)sizeof(tmp));
            if (n <= 0) return false;
            rdbuf.assign(tmp, (size_t)n);
            rdpos = 0;
        }
    }

    bool write_all(const std::string& data) {
        const char* p = data.c_str();
        size_t left = data.size();
        while (left > 0) {
#ifdef _WIN32
            DWORD wrote = 0;
            if (!WriteFile((HANDLE)in_w, p, (DWORD)left, &wrote, nullptr) || wrote == 0) return false;
            p += wrote;
            left -= (size_t)wrote;
#else
            ssize_t wrote = ::write(in_fd, p, left);
            if (wrote < 0) { if (errno == EINTR) continue; return false; }
            if (wrote == 0) return false;
            p += wrote;
            left -= (size_t)wrote;
#endif
        }
        return true;
    }

    // send writes cmd + a sentinel echo, then scans stdout line by line.
    // Non-sentinel lines pass through to stdout; the sentinel itself is
    // consumed and never printed (review focus: no __HIRE_RC leak).
    int send(const std::string& cmd) {
        const std::string nonce = new_nonce();
        const std::string prefix = std::string("__HIRE_RC:") + nonce + ":";
        std::string echo;
        if (name == "pwsh") {
            // $? catches cmdlet failures; $LASTEXITCODE is seeded at start.
            echo = "Write-Output \"__HIRE_RC:" + nonce + ":$(if ($?) { $LASTEXITCODE } else { 1 })\"";
        } else {
            echo = "printf '__HIRE_RC:%s:%s\\n' " + nonce + " \"$?\"";
        }
        if (!write_all(cmd + "\n" + echo + "\n")) {
            throw std::runtime_error("hire::sh: shell instance died while running: " + cmd);
        }
        for (;;) {
            std::string line;
            const bool full = read_line(line);
            const std::string trimmed = rtrim_crlf(line);
            if (starts_with(trimmed, prefix)) {
                const std::string rest = trimmed.substr(prefix.size());
                if (!rest.empty()) {
                    char* end = nullptr;
                    const long code = std::strtol(rest.c_str(), &end, 10);
                    if (end != rest.c_str() && *end == '\0') return (int)code;
                }
                if (!full) break;   // malformed sentinel: consume, do not leak
                continue;
            }
            if (!trimmed.empty() || full) { std::cout << trimmed << "\n"; std::cout.flush(); }
            if (!full) break;
        }
        throw std::runtime_error("hire::sh: shell instance exited before reporting status: " + cmd);
    }

    void teardown() {
        if (closed) return;
        closed = true;
#ifdef _WIN32
        if (in_w) { CloseHandle((HANDLE)in_w); in_w = nullptr; }
        if (proc) {
            if (WaitForSingleObject((HANDLE)proc, 500) == WAIT_TIMEOUT) {
                TerminateProcess((HANDLE)proc, 1);
                WaitForSingleObject((HANDLE)proc, INFINITE);
            }
            CloseHandle((HANDLE)proc);
            proc = nullptr;
        }
        if (out_r) { CloseHandle((HANDLE)out_r); out_r = nullptr; }
#else
        if (in_fd >= 0) { ::close(in_fd); in_fd = -1; }
        if (pid > 0) {
            bool reaped = false;
            for (int k = 0; k < 50 && !reaped; ++k) {
                int st = 0;
                pid_t r = ::waitpid((pid_t)pid, &st, WNOHANG);
                if (r == (pid_t)pid || (r < 0 && errno == ECHILD)) { reaped = true; break; }
                struct timespec ts;
                ts.tv_sec = 0;
                ts.tv_nsec = 10 * 1000 * 1000;
                ::nanosleep(&ts, nullptr);
            }
            if (!reaped) { ::kill((pid_t)pid, SIGKILL); ::waitpid((pid_t)pid, nullptr, 0); }
            pid = -1;
        }
        if (out_fd >= 0) { ::close(out_fd); out_fd = -1; }
#endif
    }
};

#ifdef _WIN32
// resolve_shell_exe mirrors shell_windows.go: pwsh/powershell and sh come
// from PATH, but "bash" must be Git Bash, not the WSL launcher at
// C:\Windows\System32\bash.exe that shadows it and has no distro to exec.
inline std::string resolve_shell_exe(const std::string& name) {
    char buf[MAX_PATH];
    DWORD n;
    if (name == "pwsh") {
        n = SearchPathA(nullptr, "pwsh.exe", nullptr, MAX_PATH, buf, nullptr);
        if (n == 0) n = SearchPathA(nullptr, "powershell.exe", nullptr, MAX_PATH, buf, nullptr);
        if (n > 0 && n < MAX_PATH) return std::string(buf, n);
        return name;
    }
    if (name == "sh") {
        n = SearchPathA(nullptr, "sh.exe", nullptr, MAX_PATH, buf, nullptr);
        if (n > 0 && n < MAX_PATH) return std::string(buf, n);
        return name;
    }
    if (name == "bash") {
        n = SearchPathA(nullptr, "git.exe", nullptr, MAX_PATH, buf, nullptr);
        if (n > 0 && n < MAX_PATH) {
            std::string git(buf, n);
            size_t p = git.find_last_of("\\/");
            if (p != std::string::npos && p > 0) {
                size_t p2 = git.find_last_of("\\/", p - 1);
                if (p2 != std::string::npos) {
                    std::string cand = git.substr(0, p2) + "\\bin\\bash.exe";
                    if (GetFileAttributesA(cand.c_str()) != INVALID_FILE_ATTRIBUTES) return cand;
                }
            }
        }
        n = SearchPathA(nullptr, "bash.exe", nullptr, MAX_PATH, buf, nullptr);
        if (n > 0 && n < MAX_PATH) return std::string(buf, n);
        return name;
    }
    return name;  // third-party: let CreateProcess search PATH
}

// build_cmdline quotes argv for CreateProcessA (CommandLineToArgvW rules).
inline std::string win_quote(const std::string& s) {
    std::string r = "\"";
    size_t i = 0;
    while (i < s.size()) {
        size_t bs = 0;
        while (i < s.size() && s[i] == '\\') { ++bs; ++i; }
        if (i == s.size()) { r.append(bs * 2, '\\'); break; }
        if (s[i] == '"') { r.append(bs * 2 + 1, '\\'); r += '"'; ++i; }
        else { r.append(bs, '\\'); r += s[i]; ++i; }
    }
    r += "\"";
    return r;
}
inline std::string build_cmdline(const std::vector<std::string>& argv) {
    std::string out;
    for (size_t i = 0; i < argv.size(); ++i) {
        if (i) out += " ";
        out += win_quote(argv[i]);
    }
    return out;
}

// spawn_inherit runs a one-shot command with the parent's stdio.
inline int spawn_inherit(const std::vector<std::string>& argv) {
    std::vector<std::string> a = argv;
    a[0] = resolve_shell_exe(argv[0]);
    std::string cmdline = build_cmdline(a);
    std::vector<char> buf(cmdline.begin(), cmdline.end());
    buf.push_back('\0');
    STARTUPINFOA si;
    ZeroMemory(&si, sizeof(si));
    si.cb = sizeof(si);
    si.dwFlags = STARTF_USESTDHANDLES;
    HANDLE self = GetCurrentProcess();
    HANDLE in = nullptr, out = nullptr, err = nullptr;
    HANDLE s_in = GetStdHandle(STD_INPUT_HANDLE);
    HANDLE s_out = GetStdHandle(STD_OUTPUT_HANDLE);
    HANDLE s_err = GetStdHandle(STD_ERROR_HANDLE);
    if (s_in != nullptr && s_in != INVALID_HANDLE_VALUE)
        DuplicateHandle(self, s_in, self, &in, 0, TRUE, DUPLICATE_SAME_ACCESS);
    if (s_out != nullptr && s_out != INVALID_HANDLE_VALUE)
        DuplicateHandle(self, s_out, self, &out, 0, TRUE, DUPLICATE_SAME_ACCESS);
    if (s_err != nullptr && s_err != INVALID_HANDLE_VALUE)
        DuplicateHandle(self, s_err, self, &err, 0, TRUE, DUPLICATE_SAME_ACCESS);
    si.hStdInput = in ? in : s_in;
    si.hStdOutput = out ? out : s_out;
    si.hStdError = err ? err : s_err;
    PROCESS_INFORMATION pi;
    ZeroMemory(&pi, sizeof(pi));
    BOOL ok = CreateProcessA(nullptr, buf.data(), nullptr, nullptr, TRUE, 0, nullptr, nullptr, &si, &pi);
    if (in) CloseHandle(in);
    if (out) CloseHandle(out);
    if (err) CloseHandle(err);
    if (!ok) throw std::runtime_error("hire::sh: cannot start shell (CreateProcess failed)");
    CloseHandle(pi.hThread);
    WaitForSingleObject(pi.hProcess, INFINITE);
    DWORD code = 0;
    GetExitCodeProcess(pi.hProcess, &code);
    CloseHandle(pi.hProcess);
    return (int)code;
}

inline std::unique_ptr<shell_instance> start_instance(const std::string& name) {
    std::vector<std::string> argv = persistent_argv(name);
    std::vector<std::string> a = argv;
    a[0] = resolve_shell_exe(argv[0]);
    std::string cmdline = build_cmdline(a);
    std::vector<char> buf(cmdline.begin(), cmdline.end());
    buf.push_back('\0');
    SECURITY_ATTRIBUTES sa;
    sa.nLength = sizeof(sa);
    sa.lpSecurityDescriptor = nullptr;
    sa.bInheritHandle = TRUE;
    HANDLE in_r = nullptr, in_w = nullptr, out_r = nullptr, out_w = nullptr;
    if (!CreatePipe(&in_r, &in_w, &sa, 0)) throw std::runtime_error("hire::sh: CreatePipe failed");
    if (!CreatePipe(&out_r, &out_w, &sa, 0)) {
        CloseHandle(in_r); CloseHandle(in_w);
        throw std::runtime_error("hire::sh: CreatePipe failed");
    }
    SetHandleInformation(in_w, HANDLE_FLAG_INHERIT, 0);
    SetHandleInformation(out_r, HANDLE_FLAG_INHERIT, 0);
    STARTUPINFOA si;
    ZeroMemory(&si, sizeof(si));
    si.cb = sizeof(si);
    si.dwFlags = STARTF_USESTDHANDLES;
    si.hStdInput = in_r;
    si.hStdOutput = out_w;
    HANDLE err_src = GetStdHandle(STD_ERROR_HANDLE);
    HANDLE err = err_src;
    bool err_fresh = false;
    if (err_src != nullptr && err_src != INVALID_HANDLE_VALUE &&
        DuplicateHandle(GetCurrentProcess(), err_src, GetCurrentProcess(), &err, 0, TRUE, DUPLICATE_SAME_ACCESS)) {
        err_fresh = true;
    }
    si.hStdError = err;
    PROCESS_INFORMATION pi;
    ZeroMemory(&pi, sizeof(pi));
    BOOL ok = CreateProcessA(nullptr, buf.data(), nullptr, nullptr, TRUE, 0, nullptr, nullptr, &si, &pi);
    CloseHandle(in_r);
    CloseHandle(out_w);
    if (err_fresh && err) CloseHandle(err);
    if (!ok) {
        CloseHandle(in_w); CloseHandle(out_r);
        throw std::runtime_error("hire::sh: cannot start persistent shell (CreateProcess failed)");
    }
    CloseHandle(pi.hThread);
    std::unique_ptr<shell_instance> inst(new shell_instance());
    inst->name = name;
    inst->in_w = (void*)in_w;
    inst->out_r = (void*)out_r;
    inst->proc = (void*)pi.hProcess;
    if (name == "pwsh") inst->write_all("$LASTEXITCODE = 0\n");  // seed (ctx.go:309-313)
    return inst;
}
#else
inline int spawn_inherit(const std::vector<std::string>& argv) {
    std::vector<char*> cargv;
    for (size_t i = 0; i < argv.size(); ++i) cargv.push_back(const_cast<char*>(argv[i].c_str()));
    cargv.push_back(nullptr);
    pid_t pid = ::fork();
    if (pid < 0) throw std::runtime_error("hire::sh: fork failed");
    if (pid == 0) {
        ::execvp(cargv[0], cargv.data());
        ::_exit(127);
    }
    int status = 0;
    for (;;) {
        pid_t r = ::waitpid(pid, &status, 0);
        if (r == pid || (r < 0 && errno != EINTR)) break;
    }
    return WIFEXITED(status) ? WEXITSTATUS(status) : 1;
}

inline std::unique_ptr<shell_instance> start_instance(const std::string& name) {
    std::vector<std::string> argv = persistent_argv(name);
    std::vector<char*> cargv;
    for (size_t i = 0; i < argv.size(); ++i) cargv.push_back(const_cast<char*>(argv[i].c_str()));
    cargv.push_back(nullptr);
    int in_pipe[2], out_pipe[2];
    if (::pipe(in_pipe) != 0) throw std::runtime_error("hire::sh: pipe failed");
    if (::pipe(out_pipe) != 0) {
        ::close(in_pipe[0]); ::close(in_pipe[1]);
        throw std::runtime_error("hire::sh: pipe failed");
    }
    pid_t pid = ::fork();
    if (pid < 0) {
        ::close(in_pipe[0]); ::close(in_pipe[1]);
        ::close(out_pipe[0]); ::close(out_pipe[1]);
        throw std::runtime_error("hire::sh: fork failed");
    }
    if (pid == 0) {
        ::dup2(in_pipe[0], STDIN_FILENO);
        ::dup2(out_pipe[1], STDOUT_FILENO);
        ::close(in_pipe[0]); ::close(in_pipe[1]);
        ::close(out_pipe[0]); ::close(out_pipe[1]);
        ::execvp(cargv[0], cargv.data());
        ::_exit(127);
    }
    ::close(in_pipe[0]); ::close(out_pipe[1]);
    std::unique_ptr<shell_instance> inst(new shell_instance());
    inst->name = name;
    inst->in_fd = in_pipe[1];
    inst->out_fd = out_pipe[0];
    inst->pid = (long)pid;
    if (name == "pwsh") inst->write_all("$LASTEXITCODE = 0\n");  // seed (ctx.go:309-313)
    return inst;
}
#endif

inline std::map<inst_key, std::unique_ptr<shell_instance> >& instances() {
    static std::map<inst_key, std::unique_ptr<shell_instance> > m;
    return m;
}

inline std::vector<std::string>& export_buf() { static std::vector<std::string> b; return b; }
inline bool& warned() { static bool w = false; return w; }
inline bool& exporting() { static bool e = false; return e; }
inline std::string& export_shell() { static std::string s; return s; }

inline bool stdout_is_tty() {
#ifdef _WIN32
    DWORD mode = 0;
    return GetConsoleMode(GetStdHandle(STD_OUTPUT_HANDLE), &mode) != 0;
#else
    return ::isatty(STDOUT_FILENO) != 0;
#endif
}

// shell_degrades: third-party shells and pwsh-on-Unix cannot host a
// persistent stdin instance (ctx.go:239-260).
inline bool shell_degrades(const std::string& name) {
    if (!is_builtin(name)) return true;
#ifndef _WIN32
    if (name == "pwsh") return true;
#endif
    return false;
}

inline void degrade_warn(const std::string& name) {
    if (warned()) return;
    warned() = true;
    if (!is_builtin(name))
        std::cerr << "hire: warning: shell \"" << name << "\" does not support persistent instances; running one-shot\n";
    else
        std::cerr << "hire: warning: pwsh persistent instances are Windows-only; running one-shot\n";
}

inline void spawn_oneshot(const std::string& name, const std::string& cmd) {
    const int rc = spawn_inherit(oneshot_argv(name, cmd));
    if (rc != 0) throw std::runtime_error("hire::sh: command failed (exit " + std::to_string(rc) + "): " + cmd);
}

// capture_output runs a one-shot command with stdout piped back to the
// caller while stderr stays on the parent's terminal (sh_out: stdout is
// data, stderr is still diagnostic output). rc_out carries the exit code.
inline std::string capture_output(const std::string& name, const std::string& cmd, int& rc_out) {
    std::vector<std::string> argv = oneshot_argv(name, cmd);
#ifdef _WIN32
    argv[0] = resolve_shell_exe(argv[0]);
    std::string cmdline = build_cmdline(argv);
    std::vector<char> buf(cmdline.begin(), cmdline.end());
    buf.push_back('\0');
    SECURITY_ATTRIBUTES sa;
    sa.nLength = sizeof(sa);
    sa.lpSecurityDescriptor = nullptr;
    sa.bInheritHandle = TRUE;
    HANDLE out_r = nullptr, out_w = nullptr;
    if (!CreatePipe(&out_r, &out_w, &sa, 0)) throw std::runtime_error("hire::sh_out: CreatePipe failed");
    SetHandleInformation(out_r, HANDLE_FLAG_INHERIT, 0);
    HANDLE self = GetCurrentProcess();
    HANDLE in = nullptr, err = nullptr;
    HANDLE s_in = GetStdHandle(STD_INPUT_HANDLE);
    HANDLE s_err = GetStdHandle(STD_ERROR_HANDLE);
    if (s_in != nullptr && s_in != INVALID_HANDLE_VALUE)
        DuplicateHandle(self, s_in, self, &in, 0, TRUE, DUPLICATE_SAME_ACCESS);
    if (s_err != nullptr && s_err != INVALID_HANDLE_VALUE)
        DuplicateHandle(self, s_err, self, &err, 0, TRUE, DUPLICATE_SAME_ACCESS);
    STARTUPINFOA si;
    ZeroMemory(&si, sizeof(si));
    si.cb = sizeof(si);
    si.dwFlags = STARTF_USESTDHANDLES;
    si.hStdInput = in ? in : s_in;
    si.hStdOutput = out_w;
    si.hStdError = err ? err : s_err;
    PROCESS_INFORMATION pi;
    ZeroMemory(&pi, sizeof(pi));
    BOOL ok = CreateProcessA(nullptr, buf.data(), nullptr, nullptr, TRUE, 0, nullptr, nullptr, &si, &pi);
    if (in) CloseHandle(in);
    if (err) CloseHandle(err);
    CloseHandle(out_w);
    if (!ok) {
        CloseHandle(out_r);
        throw std::runtime_error("hire::sh_out: cannot start shell (CreateProcess failed)");
    }
    CloseHandle(pi.hThread);
    std::string out;
    char tmp[1024];
    DWORD n = 0;
    while (ReadFile(out_r, tmp, (DWORD)sizeof(tmp), &n, nullptr) && n > 0) out.append(tmp, (size_t)n);
    CloseHandle(out_r);
    WaitForSingleObject(pi.hProcess, INFINITE);
    DWORD code = 0;
    GetExitCodeProcess(pi.hProcess, &code);
    CloseHandle(pi.hProcess);
    rc_out = (int)code;
    return out;
#else
    std::vector<char*> cargv;
    for (size_t i = 0; i < argv.size(); ++i) cargv.push_back(const_cast<char*>(argv[i].c_str()));
    cargv.push_back(nullptr);
    int p[2];
    if (::pipe(p) != 0) throw std::runtime_error("hire::sh_out: pipe failed");
    pid_t pid = ::fork();
    if (pid < 0) {
        ::close(p[0]); ::close(p[1]);
        throw std::runtime_error("hire::sh_out: fork failed");
    }
    if (pid == 0) {
        ::dup2(p[1], STDOUT_FILENO);   // stderr stays inherited
        ::close(p[0]); ::close(p[1]);
        ::execvp(cargv[0], cargv.data());
        ::_exit(127);
    }
    ::close(p[1]);
    std::string out;
    char tmp[1024];
    for (;;) {
        ssize_t n = ::read(p[0], tmp, sizeof(tmp));
        if (n < 0 && errno == EINTR) continue;
        if (n <= 0) break;
        out.append(tmp, (size_t)n);
    }
    ::close(p[0]);
    int status = 0;
    for (;;) {
        pid_t r = ::waitpid(pid, &status, 0);
        if (r == pid || (r < 0 && errno != EINTR)) break;
    }
    rc_out = WIFEXITED(status) ? WEXITSTATUS(status) : 1;
    return out;
#endif
}

inline void collect_export(const std::string& cmd, const std::string& name) {
    if (!is_builtin(name))
        throw std::runtime_error("hire::sh: instance=0 (export mode) needs a builtin shell (pwsh/bash/sh), got " + name);
    if (!exporting()) { exporting() = true; export_shell() = name; }
    export_buf().push_back(cmd);
}

// finish_export flushes the instance=0 buffer at task end: joined to
// stdout on success, discarded with a stderr note on failure; a TTY hint
// tells the user how to eval/iex it (ctx.go:405-426).
inline void finish_export(const std::string& name, bool ok) {
    std::vector<std::string>& buf = export_buf();
    if (buf.empty()) return;
    if (!ok) {
        buf.clear();
        std::cerr << "hire: task failed; export aborted, collected commands discarded\n";
        return;
    }
    std::string joined;
    for (size_t i = 0; i < buf.size(); ++i) {
        if (i) joined += "\n";
        joined += buf[i];
    }
    std::cout << joined << "\n";
    buf.clear();
    if (stdout_is_tty()) {
        if (export_shell() == "pwsh")
            std::cerr << "\u63d0\u793a: \u6b64\u4efb\u52a1\u5305\u542b instance=0 \u547d\u4ee4\u3002\u8bf7\u4f7f\u7528: iex (hire " << name << ")\n";
        else
            std::cerr << "\u63d0\u793a: \u6b64\u4efb\u52a1\u5305\u542b instance=0 \u547d\u4ee4\u3002\u8bf7\u4f7f\u7528: eval \"$(hire " << name << ")\"\n";
    }
}

inline void run_persistent(const std::string& cmd, const std::string& name, int inst) {
    inst_key key;
    key.inst = inst;
    key.shell = name;
    std::map<inst_key, std::unique_ptr<shell_instance> >& m = instances();
    std::map<inst_key, std::unique_ptr<shell_instance> >::iterator it = m.find(key);
    if (it == m.end()) {
        std::unique_ptr<shell_instance> p = start_instance(name);
        it = m.emplace(key, std::move(p)).first;
    }
    int rc;
    try {
        rc = it->second->send(cmd);
    } catch (...) {
        m.erase(it);   // crashed instance: destructor tears it down; next call restarts
        throw;
    }
    if (rc != 0) throw std::runtime_error("hire::sh: command failed (exit " + std::to_string(rc) + "): " + cmd);
}

inline void close_instances() {
    instances().clear();   // ~shell_instance tears each one down (idempotent)
}

struct instance_guard {
    ~instance_guard() { close_instances(); }
};

}  // namespace detail

// sh runs a command in a shell (see the ported protocol above).
// inst: 1 = default persistent instance (reused by (inst, shell));
//       2+ = a separate persistent instance; 0 = export mode (collect only).
// shell: optional builtin shell name; a "name:" prefix on cmd forces one-shot.
inline void sh(const std::string& cmd, int inst = 1, const std::string& shell = "") {
    if (inst < 0) throw std::runtime_error("hire::sh: inst must be >= 0");
    std::string pname, rest;
    if (detail::split_shell_prefix(cmd, pname, rest)) {
        detail::spawn_oneshot(pname, rest);   // builtin shell: prefix -> one-shot
        return;
    }
    const std::string name = shell.empty() ? detail::default_shell_name() : shell;
    if (inst == 0) {
        detail::collect_export(cmd, name);
        return;
    }
    if (!shell.empty()) {
        // An explicit shell is an explicit request not to persist.
        if (detail::shell_degrades(name)) detail::degrade_warn(name);
        detail::spawn_oneshot(name, cmd);
        return;
    }
    if (detail::shell_degrades(name)) {   // third-party / pwsh-on-Unix
        detail::degrade_warn(name);
        detail::spawn_oneshot(name, cmd);
        return;
    }
    detail::run_persistent(cmd, name, inst);
}

// sh_out runs one command in a fresh shell, returns its stdout with exactly
// one trailing newline removed (and a preceding CR for CRLF). stderr stays
// on the terminal and a non-zero exit throws. It never reuses a persistent
// instance: capture and state are deliberately orthogonal (spec §3 F).
inline std::string sh_out(const std::string& cmd) {
    std::string pname, rest;
    const bool pref = detail::split_shell_prefix(cmd, pname, rest);
    const std::string name = pref ? pname : detail::default_shell_name();
    const std::string& target = pref ? rest : cmd;
    int rc = 0;
    const std::string raw = detail::capture_output(name, target, rc);
    if (rc != 0) throw std::runtime_error("hire::sh_out: command failed (exit " + std::to_string(rc) + "): " + cmd);
    std::string out = raw;
    if (!out.empty() && out[out.size() - 1] == '\n') {
        out.erase(out.size() - 1);
        if (!out.empty() && out[out.size() - 1] == '\r') out.erase(out.size() - 1);
    }
    return out;
}

// echo prints its printable arguments joined with no separator and appends
// one newline. In export mode (after a sh(cmd, 0)) stdout is reserved for
// the eval payload, so the line goes to stderr instead (spec §3 D).
template <typename... Ts>
inline void echo(Ts&&... parts) {
    std::ostringstream os;
    (void)std::initializer_list<int>{((os << std::forward<Ts>(parts)), 0)...};
    os << '\n';
    if (detail::exporting()) {
        std::cerr << os.str();
        std::cerr.flush();
    } else {
        std::cout << os.str();
        std::cout.flush();
    }
}

// json_str renders s as a quoted JSON string with escapes.
inline std::string json_str(const std::string& s) {
    std::string out = "\"";
    for (size_t i = 0; i < s.size(); ++i) {
        switch (s[i]) {
            case '"':  out += "\\\""; break;
            case '\\': out += "\\\\"; break;
            case '\n': out += "\\n"; break;
            case '\t': out += "\\t"; break;
            case '\r': out += "\\r"; break;
            default:   out += s[i];
        }
    }
    return out + "\"";
}

// parse decodes hire's flat HIRE_ARGS object: string values with escapes,
// true/false/number literals, and the "_" string array. Unknown shapes are
// skipped, not fatal - hire already validated the args against the
// discover schema before invoking the task.
inline args args::parse(const std::string& json) {
    args out;
    const size_t n = json.size();
    size_t i = 0;
    // skip_ws and parse_string advance i through the flat object
    // (lambdas, kept local: one parser, one place).
    auto skip_ws = [&]() {
        while (i < n && (json[i] == ' ' || json[i] == '\t' || json[i] == '\n' || json[i] == '\r')) {
            ++i;
        }
    };
    auto parse_string = [&]() -> std::string {
        std::string v;
        ++i;  // opening quote
        while (i < n && json[i] != '"') {
            if (json[i] == '\\' && i + 1 < n) {
                const char c = json[++i];
                ++i;
                switch (c) {
                    case 'n': v += '\n'; break;
                    case 't': v += '\t'; break;
                    case 'r': v += '\r'; break;
                    case 'b': v += '\b'; break;
                    case 'f': v += '\f'; break;
                    case 'u': {
                        // Go's json.Marshal emits < > & and control chars
                        // as \uXXXX; decode all four hex digits to UTF-8
                        // instead of appending a literal 'u003c'-style byte run.
                        if (i + 4 > n) break;
                        unsigned int cp = 0;
                        for (size_t k = 0; k < 4; ++k) {
                            const char h = json[i + k];
                            cp = (cp << 4) | static_cast<unsigned int>(
                                h <= '9' ? h - '0' : (h | 0x20) - 'a' + 10);
                        }
                        i += 4;
                        if (cp < 0x80) {
                            v += static_cast<char>(cp);
                        } else if (cp < 0x800) {
                            v += static_cast<char>(0xC0 | (cp >> 6));
                            v += static_cast<char>(0x80 | (cp & 0x3F));
                        } else {
                            v += static_cast<char>(0xE0 | (cp >> 12));
                            v += static_cast<char>(0x80 | ((cp >> 6) & 0x3F));
                            v += static_cast<char>(0x80 | (cp & 0x3F));
                        }
                        break;
                    }
                    default:  v += c;     // covers \" \\ \/ and any other
                }
            } else {
                v += json[i++];
            }
        }
        if (i < n) {
            ++i;  // closing quote
        }
        return v;
    };
    skip_ws();    if (i >= n || json[i] != '{') {
        return out;
    }
    ++i;
    while (true) {
        skip_ws();
        if (i >= n || json[i] == '}') {
            break;
        }
        if (json[i] == ',') {
            ++i;
            continue;
        }
        if (json[i] != '"') {
            break;  // malformed; keep what we have
        }
        const std::string key = parse_string();
        skip_ws();
        if (i >= n || json[i] != ':') {
            break;
        }
        ++i;
        skip_ws();
        if (i < n && json[i] == '"') {
            const std::string v = parse_string();
            if (key == "_") {
                out.pos_.push_back(v);
            } else {
                out.raw_[key] = v;
            }
        } else if (i < n && json[i] == '[') {
            ++i;  // the "_" positional array
            while (true) {
                skip_ws();
                if (i >= n || json[i] == ']') {
                    ++i;
                    break;
                }
                if (json[i] == ',') {
                    ++i;
                    continue;
                }
                if (json[i] != '"') {
                    ++i;
                    continue;
                }
                out.pos_.push_back(parse_string());
            }
        } else {
            const size_t start = i;
            while (i < n && json[i] != ',' && json[i] != '}') {
                ++i;
            }
            if (key != "_") {
                out.raw_[key] = json.substr(start, i - start);  // true/false, numbers
            }
        }
    }
    return out;
}

// dispatch handles the __hire__ contract verbs for the registry.
inline int dispatch(registry& reg, int argc, char** argv) {
    if (argc < 3 || std::string(argv[1]) != "__hire__") {
        std::cerr << "usage: __hire__ discover | run <name>\n";
        return 1;
    }
    const std::string action = argv[2];
    if (action == "discover") {
        std::cout << "{\"tasks\":[";
        for (size_t t = 0; t < reg.tasks.size(); ++t) {
            const task_def& task = reg.tasks[t];
            std::cout << (t ? "," : "")
                      << "{\"name\":" << json_str(task.name)
                      << ",\"desc\":" << json_str(task.desc);
            if (!task.arg_specs.empty()) {
                std::cout << ",\"args\":{";
                for (size_t a = 0; a < task.arg_specs.size(); ++a) {
                    std::cout << (a ? "," : "")
                              << json_str(task.arg_specs[a].first) << ":{"
                              << "\"type\":" << json_str(task.arg_specs[a].second.type)
                              << ",\"required\":" << (task.arg_specs[a].second.required ? "true" : "false")
                              << ",\"desc\":" << json_str(task.arg_specs[a].second.desc)
                              << "}";
                }
                std::cout << "}";
            }
            std::cout << "}";
        }
        std::cout << "]}\n";
        return 0;
    }
    if (action == "run" && argc >= 4) {
        const std::string name = argv[3];
        for (size_t t = 0; t < reg.tasks.size(); ++t) {
            if (reg.tasks[t].name != name) {
                continue;
            }
            if (!reg.tasks[t].body) {
                std::cerr << "hire: task " << name << " has no body\n";
                return 1;
            }
            const char* env = std::getenv("HIRE_ARGS");
            bool ok = true;
            try {
                reg.tasks[t].body(args::parse(env != nullptr ? env : "{}"));
            } catch (const std::exception& e) {
                std::cerr << e.what() << "\n";
                ok = false;
            } catch (...) {
                std::cerr << "hire: unknown error in task " << name << "\n";
                ok = false;
            }
            detail::finish_export(name, ok);   // both success and failure paths
            detail::exporting() = false;       // the flag is task-scoped, like the export buffer
            detail::export_shell().clear();
            return ok ? 0 : 1;
        }
        std::cerr << "hire: unknown task " << name << "\n";
        return 1;
    }
    std::cerr << "usage: __hire__ discover | run <name>\n";
    return 1;
}

}  // namespace hire

// hire_main is yours to define: declare tasks into reg. The main()
// below dispatches the __hire__ contract verbs around it - you never
// write main yourself.
void hire_main(hire::registry& reg);

int main(int argc, char** argv) {
    hire::detail::instance_guard guard;   // RAII: tear down persistent shells on every exit path
    hire::registry reg;
    hire_main(reg);
    return hire::dispatch(reg, argc, argv);
}
`

// CppTemplate is the function-form Taskfile.cpp: plain C++ functions,
// no registration, no main, no hire includes — hire parses it with the
// user's clang and builds a generated wrapper. The comment line right
// above a function is its desc ("desc:" prefix stripped); default
// values make args optional. docs/contract.md reprints this verbatim —
// edit both together. (The hire.hpp contract form stays legal legacy,
// documented there; hire init no longer scaffolds it.)
const CppTemplate = `// hire Taskfile.cpp - a taskfile is plain C++ functions: every top-level
// non-static function is a task. The // comment line right above one is
// its description (a "desc:" prefix is the convention); a default value
// makes the argument optional, and std::string/bool/int/double params
// become string/flag/num arguments.
//
// hire parses this file with your clang++ (probed on PATH; override with
// parser = "clang++" in config.lua) and compiles a small generated
// wrapper in its cache - this file stays plain C++. static functions and
// anonymous namespaces hold helpers, not tasks. Full contract:
// docs/contract.md.

#include <iostream>
#include <string>

// desc: say hello (edit me)
void hello(std::string name = "hire") {
    std::cout << "hello, " << name << "\n";
}

// desc: compile the app (opt = optimization level, scale = job multiplier)
void build(std::string target = "app", int opt = 2, double scale = 1.5) {
    std::cout << "g++ -O" << opt << " -j" << opt * scale
              << " -o " << target << " main.cpp\n";
}
`
