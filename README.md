<div align="right">

[English](README.md) · [中文](README-zh.md)

</div>

<h1 align="center">hire</h1>

<p align="center">
  <strong>A task runner for taskfiles in any language, with background jobs built in.</strong>
  <br />
  <em>Real code over YAML · Lua native, Python/Node/C++ via contract · Global tasks · Detached background jobs</em>
</p>

<p align="center">
  <a href="#five-minutes-to-first-run"><img src="https://img.shields.io/badge/Quick_Start-4CAF50?style=for-the-badge" alt="Quick Start" /></a>
  <a href="docs/lua.md"><img src="https://img.shields.io/badge/Lua_Reference-2C2D72?style=for-the-badge&logo=lua&logoColor=white" alt="Lua Reference" /></a>
  <a href="docs/contract.md"><img src="https://img.shields.io/badge/Contract-00ADD8?style=for-the-badge" alt="Contract" /></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-yellow?style=for-the-badge" alt="License" /></a>
</p>

<p align="center">
  <a href="https://github.com/XiaTian-AC/hire/releases"><img src="https://img.shields.io/github/v/release/XiaTian-AC/hire?style=flat&color=blue" alt="Release" /></a>
  <a href="https://github.com/XiaTian-AC/hire/actions/workflows/release.yml"><img src="https://img.shields.io/github/actions/workflow/status/XiaTian-AC/hire/release.yml?branch=master&label=release%20ci&style=flat" alt="Release CI" /></a>
  <img src="https://img.shields.io/badge/platform-Windows_%7C_Linux_%7C_macOS-lightgrey?style=flat" alt="Platforms" />
</p>

---

## Why hire?

- 🧑‍💻 **Taskfiles are real code** — no YAML gymnastics. Lua is embedded natively (zero boilerplate, single binary); any other language plugs in through a two-verb JSON contract.
- 🌍 **Any language** — `Taskfile.py`, `Taskfile.js`, `Taskfile.cpp` work the same as Lua: declare a runtime command in `config.lua`, hire compiles/caches what needs compiling, and `hire ls` lists tasks across all files.
- 🌐 **Global tasks** — define once in `~/.config/hire/`, run from any directory; project files in the working directory win on name clashes (with a warning).
- 🌙 **Background execution** — `hire bstart` detaches a task from your terminal. Close the window, the job keeps running; `watch` / `stop` / `clean` manage the lifecycle.
- 🔁 **Persistent shells** — commands inside a task reuse one live shell instance (`cd`/`env` carry over), with an `instance=0` export mode for `eval`/`iex` integration.
- 📦 **Single binary** — one ~8 MB executable, zero runtime dependencies. Python/Node/g++ are *your* toolchain, invoked as configured; hire embeds nothing but Lua.

## Five minutes to first run

**Install** — Scoop (Windows):

```powershell
scoop bucket add XiaTian-AC https://github.com/XiaTian-AC/XiaTian-AC-bucket
scoop install hire
```

Homebrew (macOS/Linux):

```bash
brew tap XiaTian-AC/XiaTian-AC-bucket
brew install hire
```

Or [from source](#from-source) below.

**Scaffold and run** — in any empty directory:

```powershell
hire init          # writes a runnable Taskfile.lua
hire hello         # → hello from hire
```

That's the whole loop: `hire init` writes a commented starter file, `hire <name>` runs a
task, `hire ls` lists them. Edit `Taskfile.lua` — tasks are globals, no wrapper tables.

**Bring your own language**:

```powershell
hire init --lang python    # or node / cpp
```

The generated file speaks the [`__hire__` contract](docs/contract.md) — hire discovers
its tasks by calling the program and passes parsed flags through the `HIRE_ARGS`
environment variable. Add the runtime to `~/.config/hire/config.lua`:

```lua
return {
  runtimes = {
    python = "python",
    node   = "node",
    cpp    = { build = "g++ -std=c++20 -O2 -o {out} {src}" },  -- compiled + cached
  },
}
```

## Usage

### Tasks with arguments

hire validates CLI flags against each task's declared schema — unknown flags are
rejected with the allowed list, before any code runs.

```lua
release = task {
  desc = "test, commit, tag, push",
  args = {
    tag   = req "git tag, e.g. v1.1.0",   -- required string
    force = bool "force push",            -- flag
  },
  run = function(args)
    x.sh("go test ./...")
    x.exec("git", {"tag", args.tag})       -- no shell, no injection
    x.sh(args.force and "git push --force --tags" or "git push --tags")
  end,
}
```

```powershell
hire release --tag v1.1.0 --force
hire help release            # desc + typed arg list
hire release --help          # same, task-level
```

### Background lifecycle

```powershell
hire bstart dev-server       # detach; survives terminal close
hire ls-running              # dev-server#1  pid 12345  running
hire watch dev-server        # live tail; Ctrl+C exits viewer only
hire stop dev-server         # kills the process tree
hire clean --older-than=7d   # prune ended instances + logs
```

`bstart` works identically for external-language taskfiles — the background child
re-loads the same resolved file list, whatever language it's written in.

### Handy commands

```powershell
hire                          # bare hire lists tasks + one usage hint
hire init                     # scaffold a starter taskfile (lua default)
hire config                   # dump effective config (display + shells)
hire --taskfile .\dev\My.cc hello    # any supported extension, one file only
hire build --shell bash       # pick a registered shell for one run
iex (hire env-setup)          # export mode: collected commands into YOUR shell
```

## Documentation

| Doc | What's inside |
|---|---|
| [Lua reference](docs/lua.md) | v2 syntax: declaration forms, `task{}`/`req`/`bool`, the `x` API, persistent instances (and their sharp edges), export mode |
| [Contract](docs/contract.md) | The two `__hire__` verbs, discover JSON, `HIRE_ARGS` shape, runtimes config, full Python/Node/C++ boilerplate |
| [TOML reference](docs/toml.md) | `Taskfile.toml` schema, inline Lua `run`, strictness rules |
| [Example taskfile](testdata/Taskfile.lua) | Runnable sample covering the main forms |

## From source

The module is named `hire` (bare, not a hosted path), so `go install <repo>@latest`
does not work — clone and build:

```bash
git clone https://github.com/XiaTian-AC/hire.git
cd hire
go build -o hire .
# run tests: go test ./... && go test -tags integration ./integration/
```

## Configuration

Files live under the hire config directory (`~/.config/hire/` on every platform,
honoring `XDG_CONFIG_HOME`):

| File | Purpose |
|---|---|
| `Taskfile.*` | Your global taskfiles — all five are probed and merged. |
| `config.lua` | Optional: `shells` registry, `display` options, `runtimes` for external languages. |

```lua
return {
  shells = {
    gitbash = "C:\\Program Files\\Git\\bin\\bash.exe",
  },
  display = {
    ls_running = {
      time = "1h",          -- "0" running only · 30m/1h/2d/1w window for ended jobs
      running_first = true,
      newest_first = true,
    },
  },
  runtimes = {
    python = "python",
    node   = "node",
    cpp    = { build = "g++ -std=c++20 -O2 -o {out} {src}" },
  },
}
```

Inspect effective values with `hire config`.

## Project structure

```
main.go                    entry point, delegates to internal/cli
internal/
├── cli/                   command dispatch (init/ls/bstart/watch/stop/clean/config…)
├── adapter/               lua / toml / exec adapters -> uniform Task model
│   └── templates.go       hire init boilerplate (lua/python/node/cpp)
├── config/                XDG paths, config.lua (shells, display, runtimes)
├── runtime/               task execution: x global, persistent shells, export mode
├── argparse/              generic --flag parser, strict schema validation
├── shell/                 shell resolution incl. Windows Git Bash detection
├── bg/                    registry, detached launcher, procs, watch
docs/                      lua.md, contract.md, toml.md
testdata/Taskfile.lua      runnable example
integration/               end-to-end tests (go test -tags integration)
.goreleaser.yaml           multi-platform release config
.scoop/ .brew/             package manager templates rendered by CI
```

## Tech stack

| Layer | Choice |
|---|---|
| Core | Go, standard library only where possible |
| Task languages | Lua 5.1 embedded via [gopher-lua](https://github.com/yuin/gopher-lua); Python/Node/C++ via the contract; TOML via [BurntSushi/toml](https://github.com/BurntSushi/toml) |
| Releases | [GoReleaser](https://goreleaser.com) + GitHub Actions |

## Contributing

1. Fork, branch, commit, push, PR — the usual.
2. Run the gates first: `go test ./...` and `go test -tags integration ./integration/`
   (external-language tests skip cleanly when the toolchain is missing).

## License

[MIT](LICENSE)
