# Changelog

## v2.0.1

- **Preset languages work out of the box.** python (probes `python3` →
  `python`), node, ts, and cpp have built-in default commands; config.lua
  runtimes entries are now overrides, not requirements. `.ts` is a builtin
  extension (`--taskfile`-explicit, like `.mjs`/`.cc`) running on Node 23+
  native type stripping.
- **C++ SDK header.** `hire init --lang cpp` now generates `hire.hpp` (the
  `__hire__` contract SDK: dispatch, discover emission, a real HIRE_ARGS
  parser with escape handling) alongside a slim `Taskfile.cpp` — tasks are
  declared in `hire_main`, and the header provides `main()` itself.
- **Custom-extension runtimes.** config.lua runtimes can claim taskfile
  extensions beyond the builtin map — e.g. `deno = { cmd = "deno", ext = {".mts", ".x"} }`.
  Custom extensions are explicit `--taskfile` only, like `.mjs`/`.cc`.

## v2.0.0 (draft — unreleased)

### Breaking changes

Read these before upgrading; there is no migration layer (v2 was cut when the tool
had no external users):

1. **Rename: tkx → hire.** Binary, Go module, config directory
   (`~/.config/hire/`), data directory (`~/.local/share/hire/`), and Scoop /
   Homebrew package names. GitHub repo renames taskx → hire at release time.
2. **Environment variable prefix is now `HIRE_*`** (`HIRE_ARGS`, `HIRE_FILES`).
   The old `TKX_*` names are gone.
3. **v1 Lua taskfile syntax is removed.** `local tasks = { ... } return tasks`
   files fail to load with an explanatory error. Tasks are now plain globals:
   `build = function() x.sh("go build .") end`. The `ctx:` colon-call style is
   gone too — v2 uses the global `x` with dot calls (`x.sh`).
4. **Config directory moved** from `~/.config/taskx/` to `~/.config/hire/`
   (same for the data and cache directories).

### Added

- **Any-language taskfiles.** `Taskfile.py` / `.js` / `.cpp` (and `.mjs` / `.cc`
  via `--taskfile`) plug in through the two-verb `__hire__` JSON contract;
  runtimes are user-configured in `config.lua`; compiled languages build through
  a cached build template (`{out}`/`{src}` placeholders, content-hash cache).
- **TOML adapter.** Static tasks in `Taskfile.toml` with inline Lua `run`, strict
  unknown-field errors.
- **`hire init [--lang lua|python|node|cpp]`.** Runnable starter taskfiles;
  external-language ones embed the full contract boilerplate.
- **Persistent shell instances.** Commands in a task reuse one live shell
  (`cd`/env carry over); cmdlet failures raise via the amended pwsh sentinel.
  Persistent pwsh is Windows-only — on Unix, pwsh buffers `-Command -` stdin
  until EOF, so persistent requests degrade to one-shot with a warning
  (bash/sh persistence is unaffected).
- **Export mode.** `x.sh(cmd, { instance = 0 })` collects commands for
  `eval "$(hire task)"` / `iex (hire task)`.
- **Lookup chain + merge.** `Taskfile.{toml,lua,py,js,cpp}` in the working
  directory and the global config dir all load; same-name tasks: cwd wins with
  a warning; `bstart` hands the resolved file list to the background child via
  `HIRE_FILES` (fixes bstart running the wrong file after `--taskfile`).
- **UX.** Bare `hire` lists tasks + a usage hint (exit 0); did-you-mean
  suggestions; `<task> --help`; `--yes` for bstart/stop confirmations
  (non-interactive stdin aborts with exit 1); `clean --older-than` accepts both
  space and `=` forms.
- **Load-time strictness.** Reserved task-name prefix `__`, reserved argument
  name `shell`, and wrongly-typed args-spec fields are errors in every adapter.

### Fixed

- Audit #1: `bstart --taskfile <file>` runs that file's version in the
  background.
- Audit #2: `clean --older-than X` space form never falls through to clean-all.
- Audit #3: usage text line wrap.
- Audit #5: confirmations abort hard (exit 1) under non-interactive stdin.
- Audit #6/#7/#8/#10: reserved `shell` argument name, `ParseDuration` rewrite,
  dead `TKX_*` env fallbacks removed, unused-shell lint.

### Upgrade notes

- Rename or rewrite your `Taskfile.lua` to v2 syntax — see
  [docs/lua.md](docs/lua.md) (the load error message shows a worked example).
- Move `~/.config/taskx/Taskfile.lua` to `~/.config/hire/Taskfile.lua`.
- New languages need a `runtimes` entry in `config.lua` — see
  [docs/contract.md](docs/contract.md).
