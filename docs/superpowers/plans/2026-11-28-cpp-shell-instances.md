# C++ 函数形式 shell 实例能力 — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 C++ 函数形式 taskfile 能用 `hire::sh(...)` 获得与 Lua `x.sh` 对等的 shell 实例能力（持久实例、新实例、导出模式、`shell:` 前缀），用户侧只加一行 `#include "hire.hpp"`。

**Architecture:** 把 Lua 侧的 shell 实例协议（`internal/runtime/ctx.go`）移植成内嵌文本 SDK，写进 `CppHeaderTemplate`（`internal/adapter/templates.go`），随 wrapper 生成到缓存目录；Go 侧改三处：判别不再认 `hire.hpp` 字节（改认 `hire_main`/`__hire__`）、discover/compile 两阶段给 clang 与 build 注入 `-I<缓存目录>`、头文件在 parse 之前生成。teardown 用 RAII，导出模式用 `finish_export` 钩子。

**Tech Stack:** Go 1.x（`internal/adapter`）、C++20（用户自配 g++/clang）、内嵌 C++ 头文本常量。零新依赖（铁律）。

**Spec:** `docs/superpowers/specs/2026-11-28-cpp-shell-instances-design.md`

## Global Constraints

- **绝不内置工具链/解释器**：shell、clang、g++ 一律从用户 PATH 探测；SDK 是内嵌文本常量，不打包任何二进制。违反即 Critical。
- **零用户目录污染**：内嵌头只写 `<cachedir>/hire/<rtName>/`（与现有 wrapper 同目录）。
- **字节一致**：`CppHeaderTemplate` 改动后必须重印 `docs/contract.md` 对应全文，`TestContractCppListings` 必须绿（`internal/cli/init_test.go:258`）。
- **缓存键已覆盖**：函数形式键已含 `CppHeaderTemplate` 字节 + 版本盐（`cppfunc.go:704-714`），改头自动失效，不新增盐。
- **零新 Go/C++ 第三方依赖。**
- **平台**：Windows 主测 hsell 持久仅 pwsh；Unix 用 bash/sh。降级路径（第三方 shell、Unix 上的 pwsh）必须带警告，不得静默失败。
- **不改用户 build 模板**：`-I` 是自动追加的附加参数，不替换用户已有参数。

## Review Focus

1. **判别误伤**：任何含 `hire_main` 的遗留 SDK 形态文件必须仍走 raw 路径；任何纯函数 + `#include "hire.hpp"` 的文件必须走函数形式。误判会让用户 taskfile 直接加载失败。
2. **两阶段 include 解析**：用户文件里的 `#include "hire.hpp"` 在 discover（clang）与 compile（g++）都要能解析——`#pragma once` 不能替代 `-I`（预处理器要先找到文件）。
3. **进程泄漏**：函数形式是 one-shot 子进程，持久 shell 若不 teardown 会遗留孤儿 shell 进程；异常路径（命令失败抛异常）也必须关。
4. **stdout 污染**：哨兵行 `__HIRE_RC:<nonce>:<rc>` 绝不能被当作正常输出透传到用户终端；导出模式不能用哨兵。
5. **错误语义**：命令非零退出必须让整个 C++ 进程非零退出，最终由 hire 报 `task <name> failed (exit N)`；不能吞成 0。
6. **`shell:` 前缀误判**：`C:\path` 在 Windows 不能当成 `shell:` 前缀；只有内置 shell 名（pwsh/bash/sh）或（本轮不做）config 注册名才切分。

---

## File Structure

- `internal/adapter/cppfunc.go` — 判别（`isContractForm`）、`parseFuncForm` 注入 `-I`、`loadCppFuncForm` 前移头生成、`genWrapper` 保持。
- `internal/adapter/runtime.go` — `expandBuild`/`buildIfMissing` 为函数形式追加 `-I`。
- `internal/adapter/templates.go` — `CppHeaderTemplate` 新增 shell 引擎（`sh`/`close_instances`/`finish_export`/实例 map/哨兵/RAII 守卫）。
- `internal/adapter/cppfunc_internal_test.go` — 判别纯 Go 测试。
- `internal/adapter/cppfunc_test.go` — live 链测试（clang/g++）。
- `internal/adapter/runtime_test.go` — `-I` 注入与 shell 头编译/运行测试。
- `internal/cli/init_test.go` — 字节一致 pin（已有，重印后须仍绿）。
- `docs/contract.md` — C++ 全文重印 + 判别 prose 改写 + shell API 文档。
- `CHANGELOG.md` — v2.2.0 条目。

---

### Task 1: 判别改动（`isContractForm` 改认 `hire_main`/`__hire__`）

**Files:**
- Modify: `internal/adapter/cppfunc.go:30-36`
- Modify: `internal/adapter/cppfunc_internal_test.go:19-36`
- Modify: `docs/contract.md:390`（prose）

**Interfaces:**
- Consumes: 无（首个 task）
- Produces: `func isContractForm(src []byte) bool` 语义变更为 `bytes.Contains(src, []byte("hire_main")) || bytes.Contains(src, []byte("__hire__"))`；后续 task 依赖"含 include 的纯函数文件走函数形式"。

- [ ] **Step 1: 扩充失败测试**

在 `TestIsContractForm` 的 `cases` 里加一条新用例（现有 "hpp include" 用例已含 hire_main，保持 true）：

```go
{"function form with sdk include", `#include "hire.hpp"` + "\n// desc: hi\nvoid hi() {}\n", false},
```

并保留现有 `{"hpp include", "#include \"hire.hpp\"\nvoid hire_main() {}\n", true}`。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/adapter/ -run TestIsContractForm -count=1`
Expected: FAIL（新用例 want false，旧实现因含 `hire.hpp` 返回 true）。

- [ ] **Step 3: 改判别**

`cppfunc.go:30-36` 改为：

```go
// isContractForm reports whether a C++ source speaks the raw __hire__
// contract itself. The SDK legacy form defines hire_main (and includes
// hire.hpp); the hand-written raw form contains the __hire__ verb
// literal. Function-form files may include hire.hpp for hire::sh but
// never define hire_main, so those two markers are complete and honest.
func isContractForm(src []byte) bool {
	return bytes.Contains(src, []byte("hire_main")) || bytes.Contains(src, []byte("__hire__"))
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/adapter/ -run "TestIsContractForm|TestParseASTFixture" -count=1`
Expected: PASS。

- [ ] **Step 5: 改 contract.md prose**

`docs/contract.md:390` 那句"文件含 `hire.hpp` 或 `__hire__` 字样时走该 raw 契约路径"改为："文件定义 `hire_main` 或含 `__hire__` 字样时走该 raw 契约路径（函数形式文件可 include hire.hpp 用 `hire::sh`，但不定义 hire_main，故仍走函数形式）。"

- [ ] **Step 6: Commit**

```bash
git add internal/adapter/cppfunc.go internal/adapter/cppfunc_internal_test.go docs/contract.md
git commit -m "fix: discriminate C++ forms by hire_main/__hire__, not hire.hpp include"
```

---

### Task 2: 两阶段 include 可见性（头前置生成 + `-I` 注入）

**Files:**
- Modify: `internal/adapter/cppfunc.go`（`loadCppFuncForm` 前移 `MkdirAll`+头写出；`parseFuncForm` 加 `-I`）
- Modify: `internal/adapter/runtime.go`（`expandBuild` 或 `buildIfMissing` 为函数形式追加 `-I`）
- Test: `internal/adapter/runtime_test.go`

**Interfaces:**
- Consumes: Task 1 的判别；`CppHeaderTemplate` 常量。
- Produces: `parseFuncForm(srcPath, clangCmd, incDir string)`（新增第三参数）；`buildIfMissing(exePath, srcForCmd, build, displayPath string, incDir string)`（新增第五参数，空则不加 `-I`，保持 raw 路径行为不变）。

- [ ] **Step 1: 写失败测试（discover 侧）**

在 `internal/adapter/runtime_test.go` 加一个仅需 g++/clang 的测试（无 clang 则 skip），构造一个含 `#include "hire.hpp"` 的纯函数文件，断言 `parseFuncForm` 能在给定 incDir 下解析成功、不报 "hire.hpp: No such file"。测试名：`TestCppFuncFormIncludeResolves`。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/adapter/ -run TestCppFuncFormIncludeResolves -count=1`
Expected: FAIL（clang 报 hire.hpp not found，或编译期找不到头）。

- [ ] **Step 3: 头前置生成 + `-I` 注入（discover）**

`loadCppFuncForm`：把 `os.MkdirAll(dir, 0o755)` 与"写 hire.hpp 到 dir"移到 `parseFuncForm` 调用之前；`parseFuncForm` 签名加 `incDir`，argv 在 `srcPath` 前加 `-I`+incDir（保持 `-Xclang -ast-dump=json -fsyntax-only -fparse-all-comments`）。头写出逻辑抽成 `writeCppHeader(dir) error`（写 `CppHeaderTemplate` 到 `dir/hire.hpp`），`genWrapper` 复用它，避免重复。

- [ ] **Step 4: `-I` 注入（compile）**

`buildIfMissing` 加 `incDir string` 参数；`expandBuild` 后若有 incDir，在命令末尾追加 ` -I"<incDir>"`（仿 `runtime.go:186` 的加引号）。函数形式调用点（`cppfunc.go` 的 `buildIfMissing` 调用）传 `dir`；raw 路径调用点传 `""`。

- [ ] **Step 5: 运行确认通过**

Run: `go test ./internal/adapter/ -run "TestCppFuncFormIncludeResolves|TestCppFuncFormChain" -count=1`
Expected: PASS。

- [ ] **Step 6: Commit**

```bash
git add internal/adapter/cppfunc.go internal/adapter/runtime.go internal/adapter/runtime_test.go
git commit -m "fix: emit hire.hpp before parse and inject -I for both discover and build"
```

---

### Task 3: shell 实例引擎移植进 `hire.hpp`

**Files:**
- Modify: `internal/adapter/templates.go`（`CppHeaderTemplate` 内新增 shell 引擎；`main()` 加 RAII 守卫）
- Modify: `docs/contract.md`（C++ 全文重印 + shell API 说明）
- Test: `internal/adapter/runtime_test.go`

**Interfaces:**
- Consumes: Task 2 的 `-I` 注入（头可被 include）。
- Produces: `hire::sh(const std::string& cmd, int inst = 1, const std::string& shell = "")`；`hire::detail::close_instances()`；内部 `shell_instance`（stdin pipe + reader + child）。后续 Task 4 依赖这些符号与 `detail::finish_export` 挂点。

- [ ] **Step 1: 写失败测试（持久实例语义）**

在 `runtime_test.go` 加 `TestCppShPersistentInstance`（g++ 缺失则 skip）：编译一个用 `hire::sh` 的小程序，`sh("cd /tmp")` 后 `sh("pwd")` 断言输出 `/tmp`，`sh("export FOO=42")` 后 `sh("echo $FOO")` 断言 `42`；另一个实例 `sh("echo other", 2)` 不影响实例 1。断言进程退出后无孤儿（可在测试收尾检查进程表，或至少验证 teardown 不残留 pipe）。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/adapter/ -run TestCppShPersistentInstance -count=1`
Expected: FAIL（`hire::sh` 未定义）。

- [ ] **Step 3: 实现 shell 引擎（数据部分）**

在 `CppHeaderTemplate` 的 `namespace hire` 内、`args`/`registry` 之后加：

- `namespace detail`：`struct shell_instance`（`std::string name; FILE*/pipe handles; std::map<int, std::unique_ptr<shell_instance>> instances;`）、`sh_opts`（inst/shell）。
- `void sh(const std::string& cmd, int inst = 1, const std::string& shell = "")`：前缀切分（仅 pwsh/bash/sh）、`inst==0` 导出收集、显式 shell 走 one-shot、否则持久实例（按 `(inst,shell)` 复用）。
- 哨兵：每条命令后写 `printf '__HIRE_RC:%s:%s\n' <nonce> "$?"`（非 pwsh）或 pwsh 的 `$?` 兜底版；逐行读 stdout，命中即取 rc，其余行透传。
- 内置 shell 白名单 + 三方降级 one-shot + pwsh 在 Unix 降级（带一次警告）。
- 平台进程创建：`#ifdef _WIN32` 用 `CreatePipe`+`CreateProcess`，`#else` 用 `pipe`+`fork`+`execvp`。**照抄 `ctx.go:287-387` 的协议，不按摘要自创**（含 pwsh `$LASTEXITCODE` 播种）。

- [ ] **Step 4: 实现 teardown（RAII）**

在 `main()`（`templates.go:529-533`）栈上放守卫对象，析构时 `detail::close_instances()`（关 stdin、wait、超时 kill，对齐 `ctx.go:317-342`）；`dispatch` 内加 try/catch 把异常转成非零退出码。

- [ ] **Step 5: 重印 contract.md**

把 `docs/contract.md` 里 C++ 全文（legacy hire.hpp listing + 新 shell API 段）与 `CppHeaderTemplate` 逐字对齐；`TestContractCppListings` 校验。

- [ ] **Step 6: 运行确认通过**

Run: `go test ./internal/adapter/ -run "TestCppShPersistentInstance|TestContractCppListings" -count=1`（字节一致在 `internal/cli` 包）
Expected: PASS。

- [ ] **Step 7: Commit**

```bash
git add internal/adapter/templates.go docs/contract.md internal/adapter/runtime_test.go
git commit -m "feat: port persistent shell instances into C++ hire.hpp SDK"
```

---

### Task 4: 导出模式 + `finish_export` 钩子 + 错误语义

**Files:**
- Modify: `internal/adapter/templates.go`（`inst==0` 收集 + `detail::finish_export` + `main()` 成功/失败两路径调用）
- Modify: `docs/contract.md`（导出模式说明）
- Test: `internal/adapter/runtime_test.go`

**Interfaces:**
- Consumes: Task 3 的 `sh`/`close_instances`/实例 map。
- Produces: `hire::detail::finish_export(const std::string& name, bool ok)`。

- [ ] **Step 1: 写失败测试（导出模式）**

`TestCppShExportMode`：`sh("echo one", 0)` + `sh("echo two", 0)` 只收集不执行；程序成功退出时两条命令一起打到 stdout；任务失败时丢弃。断言不出现 `__HIRE_RC` 哨兵。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/adapter/ -run TestCppShExportMode -count=1`
Expected: FAIL。

- [ ] **Step 3: 实现导出 + finish_export**

`inst==0` 只 append 到缓冲；`dispatch` 的 run 分支成功/失败两路径调用 `detail::finish_export(name, ok)`（失败丢弃并写 stderr）。TTY 提示对齐 `ctx.go:419-425`（`eval`/`iex`）。

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/adapter/ -run "TestCppShExportMode|TestCppShPersistentInstance" -count=1`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/adapter/templates.go docs/contract.md internal/adapter/runtime_test.go
git commit -m "feat: C++ shell export mode and finish_export hook"
```

---

### Task 5: 集成腿 + CHANGELOG + 全量门

**Files:**
- Modify: `integration/integration_test.go`（加 C++ shell 实例端到端腿）
- Modify: `CHANGELOG.md`（v2.2.0）
- Modify: `docs/contract.md`（若 Task 3/4 有遗漏的 prose）

- [ ] **Step 1: 加集成腿**

在 `integration/integration_test.go` 加 `TestIntegrationCppShellInstances`（无 clang/g++ 则 skip）：一个函数形式 taskfile，含 `hire::sh` 持久实例 + 导出模式，跑 `hire <task>` 断言 `cd`/`export` 存活与导出输出。沿用现有集成测试的 skip guard 与 tmpdir 风格。

- [ ] **Step 2: 运行集成**

Run: `go test -count=1 -tags integration ./integration/ -run TestIntegrationCppShellInstances`
Expected: PASS（或本机无 clang/g++ 时 SKIP，如实记录）。

- [ ] **Step 3: CHANGELOG v2.2.0**

在 `CHANGELOG.md` 顶部加 `## v2.2.0`：C++ 函数形式 `hire::sh`（持久实例/新实例/导出模式/`shell:` 前缀）；判别改为 `hire_main`/`__hire__`；两阶段 `-I` 注入。写明无破坏性变更（遗留形态仍合法）。

- [ ] **Step 4: 全量门**

Run: `go test -count=1 ./... && go test -count=1 -tags integration ./integration/ && go vet ./...`
Expected: 全绿。

- [ ] **Step 5: Commit**

```bash
git add integration/integration_test.go CHANGELOG.md docs/contract.md
git commit -m "test: C++ shell instance integration leg; docs: v2.2.0 changelog"
```

---

## Self-Review

- **Spec coverage**：判别（Task 1）、两阶段 include（Task 2）、引擎+RAII（Task 3）、导出+finish_export（Task 4）、集成/文档（Task 5）——spec §5.1/5.2/5.3/6/7 全覆盖。
- **Type consistency**：`parseFuncForm(..., incDir string)` 与 `buildIfMissing(..., incDir string)` 在 Task 2 定义、Task 3/4 沿用；`hire::sh(cmd, inst, shell)` 在 Task 3 定义、Task 4 复用。
- **Review Focus 落点**：判别误伤（Task 1 Step 1 新用例）、两阶段 include（Task 2 Step 1）、进程泄漏（Task 3 Step 4 + Step 1 断言）、stdout 哨兵污染（Task 4 Step 1 断言）、错误语义（Task 3 Step 4 try/catch）、`shell:` 前缀（Task 3 Step 3 仅内置名）。
- **Proportion**：plan 未写 C++ 引擎完整实现，只给符号、协议来源（`ctx.go` 行号）与测试断言——算法由实现者按 `ctx.go` 移植。
