# hire 语言 DX v2.3 — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让五种内置语言在同一个复杂任务上都能"短且诚实"——补 Node SDK、Python/Node 持久 shell、TOML 默认值、C++ export-safe echo、外部语言 when.os、sh_out。

**Architecture:** 主要改 `internal/adapter` 的模板常量（`templates.go`）与解析/分派（`exec.go`、`toml.go`、`cppfunc.go`），外加 `init.go` 的 Node 生成映射。SDK 是内嵌文本，跨语言移植 shell 协议时照抄 `internal/runtime/ctx.go:111-426`。所有新模板同步重印 `docs/contract.md`（字节一致测试 pin）。

**Tech Stack:** Go 1.x、Python 3、Node、C++20、内嵌文本 SDK。零新依赖。

**Spec:** `docs/superpowers/specs/2026-11-28-language-dx-v2.3-design.md`

## 派发批次（按 AGENTS.md 合并派发：3 个 subagent，不是 6 个）

- **Wave 1（subagent #1，free）**：Task 1（Node SDK）+ Task 2（Python/Node 持久 shell）。同改 `templates.go` + `init.go` + `contract.md`，逻辑连贯。
- **Wave 2（subagent #2，free）**：Task 3（TOML 默认值）+ Task 4（外部语言 when.os）。改 `toml.go` + `exec.go` + `cppfunc.go`；when 依赖 Wave 1 的 SDK 模板。
- **Wave 3（subagent #3，free）**：Task 5（C++ echo）+ Task 6（sh_out）。改 `CppHeaderTemplate` + Lua；与 Wave 1 同文件 templates.go，故排在最后。
- 波内逐 task commit；波间串行（Wave 2/3 依赖 Wave 1）。

## Global Constraints

- **绝不内置工具链/解释器**：shell/python/node/clang/cl 全 PATH 探测；SDK 是内嵌文本。
- **零新 Go 依赖。**
- **字节一致**：新/改的 C++/Python/Node 模板全文重印 `docs/contract.md`，`TestContract*Listings` 绿。
- **零用户目录污染**；生成物落 `hire init` 目标目录或缓存目录。
- **向后兼容**：手写 `__hire__` 契约、无 default 的 TOML、无 echo 的 C++ 全部继续合法。
- **C++/Lua 文件必须零反引号**（Go raw string 定界）。

## Review Focus

1. **持久 shell 无孤儿**：Python/Node 的 Shell 任务结束必须 teardown（异常路径也），不遗留子进程。
2. **哨兵不泄漏**：Python/Node shell 的 `__HIRE_RC:<nonce>:` 不能进用户 stdout。
3. **export 改道**：C++ `hire::echo` 在 export 模式必须走 stderr，否则污染 `eval`。
4. **when.os 不误杀**：不匹配平台=任务不存在；匹配平台正常；discover 保持纯。
5. **TOML default 类型**：num/bool/string 默认值类型不符要在加载期报错，不静默。
6. **Node SDK 向后兼容**：旧的 `init --lang node` 产物（纯契约样板）仍可加载。

---

## File Structure

- `internal/adapter/templates.go` — 新增 `NodeSDKTemplate`；改 `PythonSDKTemplate`（加 Shell）、`NodeTemplate`/`PythonTemplate`（瘦文件）、`CppHeaderTemplate`（echo/sh_out）；`LuaTemplate` 加 sh_out 说明。
- `internal/cli/init.go` — node 生成 `{hire.js, Taskfile.js}`。
- `internal/adapter/toml.go` — args 加 `default`。
- `internal/adapter/exec.go` — discover `when` 过滤。
- `internal/adapter/cppfunc.go` — `// when:` 注释解析（C++ 函数形式）。
- `docs/contract.md` — 各语言全文重印 + when/default/echo/sh_out 文档。
- `CHANGELOG.md` — v2.3.0。
- 测试：`init_test.go`、`toml_test.go`、`exec_test.go`、`cppfunc_test.go`、`runtime_test.go`（Python/Node shell 走真实解释器）。

---

### Task 1: Node SDK（`hire.js`）

**Files:**
- Modify: `internal/adapter/templates.go`（新增 `NodeSDKTemplate`；`NodeTemplate` 改为瘦 `Taskfile.js` 引用 `hire.js`）
- Modify: `internal/cli/init.go:23`（node 映射改为两文件）
- Modify: `docs/contract.md`（Node 节重印）
- Test: `internal/cli/init_test.go`

**Interfaces:**
- Consumes: 无
- Produces: `NodeSDKTemplate` 常量；`init --lang node` 生成 `hire.js` + `Taskfile.js`。导出 `task(name, opts, fn)`/`opt/req/flag/num`/`dispatch`。

- [ ] **Step 1: 写失败测试**

`init_test.go` 加：`init --lang node` 生成 `hire.js` 和 `Taskfile.js` 两个文件；`hire.js` 含 `function task(`；`TestContractNodeListings` 断言 contract.md 重印 `NodeSDKTemplate`+`NodeTemplate`。

- [ ] **Step 2: 确认失败**

Run: `go test ./internal/cli/ -run "TestInit.*Node|TestContractNode" -count=1`
Expected: FAIL（node 只生成 Taskfile.js）。

- [ ] **Step 3: 实现**

新增 `NodeSDKTemplate`（参照 `PythonSDKTemplate` 结构：`_registry`、`task(name, {desc,args}, fn)`、`opt/req/flag/num` 返回 `{type,required,desc}`、`dispatch()` 解析 `process.argv` + `HIRE_ARGS`，num 转 `Number`）。`NodeTemplate` 改为 `const { task, opt, dispatch } = require("./hire.js"); task(...); dispatch();`。`init.go` node 映射 `{{"hire.js", NodeSDKTemplate}, {"Taskfile.js", NodeTemplate}}`。

- [ ] **Step 4: 重印 contract.md + 验证**

`docs/contract.md` 的 Node 段换成新两清单。Run: `go test ./internal/cli/ -count=1`
Expected: PASS。

- [ ] **Step 5: Commit** `feat: node decorator-free SDK (hire.js)`

---

### Task 2: Python/Node 持久 shell（共享实例）

**Files:**
- Modify: `internal/adapter/templates.go`（`PythonSDKTemplate` 加 `Shell` 类；`NodeSDKTemplate` 加 `Shell` 类）
- Modify: `docs/contract.md`
- Test: `internal/adapter/runtime_test.go`（或新 `shell_sdk_test.go`，走真实 `python`/`node`）

**Interfaces:**
- Consumes: Task 1 的 NodeSDKTemplate
- Produces: `Shell` 类（Python/Node），`run(cmd, instance=1, shell=None)`；`close()`；`with`/exit 兜底。照抄 `ctx.go:111-426`。

- [ ] **Step 1: 写失败测试**

加 `TestPythonShellPersistent`（skip 无 python）：生成一个用 `Shell` 的脚本，`cd /tmp` 后 `pwd` → `/tmp`，`export FOO=42` 后 `echo $FOO` → 42；`instance=2` 隔离。`TestNodeShellPersistent` 同理（skip 无 node）。断言无 `__HIRE_RC` 泄漏、进程退出后无孤儿。

- [ ] **Step 2: 确认失败** — Run 上述测试 → FAIL（无 Shell 类）。

- [ ] **Step 3: 实现 Shell（Python + Node）**

每语言一个类：`Popen`/`spawn` 持久 shell（内置 pwsh/bash/sh，`shell:` 前缀 one-shot，三方降级），stdin 写命令 + 哨兵 `__HIRE_RC:<nonce>:$?`，逐行读 stdout 直到哨兵，实例 map 按 `(inst,shell)`，teardown（close stdin/wait/kill）。Python 用 `atexit`、Node 用 `process.on('exit')` 兜底。

- [ ] **Step 4: 重印 + 验证** — contract.md 重印；Run 新测试 + `TestContractPythonListings`。
Expected: PASS。

- [ ] **Step 5: Commit** `feat: persistent shell for python/node SDKs`

---

### Task 3: TOML 参数默认值

**Files:**
- Modify: `internal/adapter/toml.go`（args schema 加 `default`；`renderTmpl` 未传用默认）
- Modify: `docs/toml.md`
- Test: `internal/adapter/toml_test.go`

**Interfaces:**
- Consumes: 无
- Produces: `[tasks.x.args.y] default = 4|"s"|true`；`required` 与 `default` 互斥（并存加载期报错）。

- [ ] **Step 1: 写失败测试** — 未传 `jobs` → 命令渲染用 default；`default` 类型与 `type` 不符 → 报错；`required=true` + `default` → 报错。

- [ ] **Step 2: 确认失败** → FAIL。

- [ ] **Step 3: 实现** — args 解析加 `default` 字段（类型按 `type` 校验）；`default` 存在则参数视为可选；`renderTmpl` 未传值时用 default（num 最短形式、bool 条件、string 原文）。

- [ ] **Step 4: 验证** — Run `go test ./internal/adapter/ -run TestToml -count=1`。PASS。

- [ ] **Step 5: Commit** `feat: toml arg defaults`

---

### Task 4: 外部语言 `when.os`（平台过滤）

**Files:**
- Modify: `internal/adapter/exec.go`（`discoverJSON` 加 `When`；`fileFromDiscover` 按 `runtime.GOOS` 过滤）
- Modify: `internal/adapter/cppfunc.go`（`// when:` 注释解析 → FuncTask.When）
- Modify: `internal/adapter/templates.go`（Python/Node SDK 的 task 声明支持 `when`；C++ 注释说明）
- Modify: `docs/contract.md`
- Test: `internal/adapter/exec_test.go`、`cppfunc_test.go`

**Interfaces:**
- Consumes: Task 1/2 的 SDK 模板
- Produces: discover task `when: {os: "windows"|[...]}`；不匹配 → task 不存在。

- [ ] **Step 1: 写失败测试** — discover JSON 含 `when:{os:"linux"}` 在 windows 上任务不出现；`when:{os:"windows"}` 出现；C++ `// when: linux` 函数在 windows 上不出现。

- [ ] **Step 2: 确认失败** → FAIL。

- [ ] **Step 3: 实现** — `discoverJSON` struct 加 `When`；`fileFromDiscover` 用 `runtime.GOOS` 过滤（不匹配 continue）；cppfunc 注释首行 `when:` 解析到 FuncTask 并注册时过滤；SDK 的 `task()` 接受 `when`。未知 os 值 → 加载期报错。

- [ ] **Step 4: 验证** — Run 新测试 + `TestParseAST*`。PASS。

- [ ] **Step 5: Commit** `feat: when.os for external-language taskfiles`

---

### Task 5: C++ `hire::echo`（export 改道）

**Files:**
- Modify: `internal/adapter/templates.go`（`CppHeaderTemplate` 加 `echo` + export 标记）
- Modify: `docs/contract.md`
- Test: `internal/adapter/runtime_test.go`（g++ probe）

**Interfaces:**
- Consumes: Task 2/3 不相关
- Produces: `hire::echo(...)`；export 模式写 stderr。

- [ ] **Step 1: 写失败测试** — g++ probe：普通任务 `echo("hi")` 到 stdout；含 `sh("x",0)` 的导出任务里 `echo("hi")` 到 stderr，stdout 不含 hi。

- [ ] **Step 2: 确认失败** → FAIL（echo 未定义）。

- [ ] **Step 3: 实现** — `detail` 加 `bool g_exporting`；`sh(cmd,0)` 置位；`echo(...)` 依标记选 `std::cout`/`std::cerr`，加换行；任务结束重置。

- [ ] **Step 4: 重印 + 验证** — contract.md 重印；Run g++ probe + `TestContractCppListings`。PASS。

- [ ] **Step 5: Commit** `feat: cpp hire::echo with export redirection`

---

### Task 6: `sh_out` 返回命令输出

**Files:**
- Modify: `internal/adapter/templates.go`（`CppHeaderTemplate` 加 `sh_out`；`LuaTemplate` 文档）
- Modify: `internal/runtime/ctx.go`（Lua `x.sh_out`）
- Modify: `internal/adapter/templates.go`（Python/Node SDK 加 `Shell.out`）
- Modify: `docs/contract.md`、`docs/lua.md`
- Test: `runtime_test.go`、`internal/runtime/runtime_test.go`

**Interfaces:**
- Consumes: Task 2 的 Shell（Python/Node）、Task 5 的 C++ 头
- Produces: `hire::sh_out(cmd)->std::string`；`x.sh_out(cmd)->string`；`Shell.out(cmd)`。

- [ ] **Step 1: 写失败测试** — C++ probe `sh_out("echo hi")` 返回 `hi`（去尾换行）；Lua `x.sh_out("echo hi")` 同；stderr 透传、非零抛错。

- [ ] **Step 2: 确认失败** → FAIL。

- [ ] **Step 3: 实现** — 各语言 `sh_out`：one-shot 捕获 stdout 到 buffer（不透传），去 1 个尾换行，stderr 透传，非零抛错。

- [ ] **Step 4: 重印 + 验证** — contract.md / lua.md 更新；Run 新测试 + 全量单测。PASS。

- [ ] **Step 5: Commit** `feat: sh_out (command output capture) across cpp/lua/python/node`

---

## Self-Review

- **Spec coverage**：A→T1、B→T2、C→T3、E→T4、D→T5、F→T6，§3 六特性全覆盖；§4 决策逐条落进对应 task 的边界。
- **Type consistency**：`Shell.run(cmd, instance, shell)`、`sh_out(cmd)->string`、`echo(...)`、discover `when` 在各 task 间一致。
- **Review Focus 落点**：孤儿（T2）、哨兵（T2）、export 改道（T5）、when 误杀（T4）、TOML 类型（T3）、Node 兼容（T1）。
- **Proportion**：只给签名/协议来源/断言，未写各语言 Shell 全量实现（YAGNI）。
