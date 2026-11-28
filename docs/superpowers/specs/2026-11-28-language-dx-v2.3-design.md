# hire 语言 DX v2.3 — 跨语言能力补齐 Design Spec

**Date:** 2026-11-28
**Status:** Draft（待用户批准）
**基线:** v2.2.0（master `bbf7e3b`）
**规模:** 一个发布内六个特性（v2.3.0）

## 1. 意图与动因

v2.1/v2.2 把各语言推到了"能写"，但仍有系统性落差，导致同一个复杂任务在不同语言里长度和能力差很多：

1. **Python/Node 没有共享 shell 实例**：每条 `subprocess`/`child_process` 是独立进程，`cd`/`export` 不跨命令保留——用户要么串 `&&`，要么自己写持久子进程（火车头）。
2. **Node 没有 SDK**：每个 taskfile 手写 `__hire__` 契约样板（discover/run/unknown/usage，约 15 行）。
3. **若干静默落差**：TOML 无参数默认值；C++ 无 export-safe echo（`cout` 会污染 `eval` 载荷）；Python/Node/C++ 无 `when.os`；`sh` 不返回输出（拿来当数据只能自己 `popen`）。

本轮一次性补齐，目标是：**同一个"release"任务在五种语言里都能短且诚实地写出来。**

## 2. 硬约束

- **绝不内置工具链/解释器**：shell/python/node/clang 全走 PATH 探测；SDK 是内嵌文本，不打包二进制。
- **零新 Go 依赖**；C++/Python/Node SDK 都是 Go 内嵌文本常量。
- **字节一致**：C++/Python/Node 模板全文进 `docs/contract.md`，由机械测试 pin。
- **零用户目录污染**：生成物落缓存目录或用户显式 init 目录。
- **向后兼容**：现有手写 `__hire__` 契约、TOML 无 default 写法、C++ 无 echo 写法全部继续合法。

## 3. 六个特性

### A. Node SDK（`hire.js`）

- **现状**：`NodeTemplate`（`templates.go:169`）是完整契约样板，用户每个 taskfile 手写 discover/run 分发与 unknown/usage（~15 行）。
- **目标**：`hire init --lang node` 生成 `hire.js` + 瘦 `Taskfile.js`，语义对齐 Python SDK。
- **API**：
  ```js
  const { task, opt, req, flag, num, dispatch } = require("./hire.js");
  task("hello", { desc: "say hello", args: { name: opt("who") } },
       (a) => { console.log("hello, " + (a.name || "hire")); });
  dispatch();
  ```
  `a` 是 `HIRE_ARGS` 解析后的对象（`a.name`、`a._` 位置参数）；`num` 值转 number。
- **边界**：`hire.js` 是内嵌常量 `NodeSDKTemplate`，进 contract.md；raw 手写契约仍合法（`isContractForm` 不涉及 Node，因为 Node 只有契约形态）；不做 TS 类型声明。

### B. Python/Node 持久 shell 助手（共享实例）

- **现状**：Python `subprocess` / Node `child_process` 每条独立进程，`cd`/`export` 不保留。
- **目标**：SDK 内提供 `Shell` 类，语义对齐 C++ `hire::sh` 与 Lua `x.sh`——默认持久实例、按 `(inst,shell)` 复用、`inst=0` 导出收集、`shell:` 前缀 one-shot、内置白名单 + 三方降级、哨兵取退出码、任务结束 teardown。
- **API（Python）**：
  ```python
  from hire import Shell
  with Shell() as sh:                # 实例 1
      sh.run("cd /tmp")
      sh.run("echo $PWD")            # /tmp
      sh.run("echo other", instance=2)
      sh.run("echo x", instance=0)   # 导出模式（仅收集）
  ```
- **API（Node）**：
  ```js
  const { Shell } = require("./hire.js");
  const sh = new Shell();
  sh.run("cd /tmp"); sh.run("echo $PWD");
  ```
- **方案**：移植 `internal/runtime/ctx.go:111-426` 的协议——哨兵 `__HIRE_RC:<nonce>:<rc>`、实例 map、stdin 持久、teardown（close stdin/wait/kill）、pwsh `$LASTEXITCODE` 播种、三方与现代差异降级。进程内管理；`atexit`（Python）/`process.on('exit')`（Node）兜底 teardown。
- **边界**：跨 hire 调用不共享（每次 `hire <task>` 一个新进程）；`inst=0` 导出模式本特性可选（先保证持久实例，导出可后置）；非零退出抛异常，由 dispatch 转成进程非零退出码。

### C. TOML 参数默认值

- **现状**：TOML args 无 `default`；可选参数未传 → `{{name}}` 替换为空串。
- **目标**：`[tasks.x.args.jobs]` 支持 `default = 4`（或 `"x"`/`true`），未传时用默认值渲染。
- **方案**：`toml.go` 的 args schema 加 `default` 字段，加载期校验类型与 `type` 一致（string/bool/num）；`renderTmpl` 在参数未传时用 default。`{{flag:+word}}` 的 bool 默认真值 → 一直插入。
- **边界**：`default` 与 `required = true` 互斥（有默认值即视为可选，二者并存加载期报错）；默认值类型严格；与 Lua/Python 的"默认值 = 可选"语义对齐。

### D. C++ `hire::echo`（export 改道）

- **现状**：C++ 无 `hire::echo`，用户用 `std::cout`；export 模式（`inst=0`，给 `eval "$(hire task)"` 消费）下 `cout` 会混进 eval 载荷。
- **目标**：加 `hire::echo(...)`——普通模式写 stdout，export 模式写 stderr（对齐 Lua `x.echo` 的 export 改道）。
- **方案**：进程内维护"当前任务是否已进入 export 收集"标记（第一次 `sh(cmd, 0)` 置位）；`echo` 依此选 stdout/stderr；参数用可变模板/流式拼接，加换行。
- **边界**：仅影响 `hire::echo`，不改 `cout`（用户自选的 cout 仍走 stdout）；标记在任务结束后重置。

### E. 外部语言 `when.os`（平台过滤）

- **现状**：discover JSON task 只有 `{name,desc,args}`；`when` 仅 Lua/TOML 适配器支持；Python/Node/C++ 无法声明平台过滤。
- **目标**：discover task 加可选 `when: {os: "windows"}` 或 `{os: ["linux","darwin"]}`；hire 加载期过滤：不匹配 = 任务不存在（`ls` 不显示、run 报 not found），与 Lua/TOML 一致。
- **方案**：`discoverJSON` struct 加 `When` 字段（`json.Unmarshal` 未知字段默认忽略，旧 payload 不受影响）；`fileFromDiscover` 按 `runtime.GOOS` 过滤。discover 仍纯（只声明，不执行）。
- **边界**：未知 os 字符串 → 视为永不匹配（或加载期报错，builder 定，倾向报错）；C++ 函数形式的 `when` 来源需决策——函数形式没有 discover JSON 直接声明点（要么 `// when:` 注释，要么不支持，见 §6）。

### F. `sh_out` 返回命令输出

- **现状**：`hire::sh` / `x.sh` 返回 void，输出透传；拿 `$(cmd)` 当数据只能自己 `popen`。
- **目标**：`hire::sh_out(cmd) -> std::string`（C++）、`x.sh_out(cmd) -> string`（Lua）、`Shell.out(cmd)`（B 落地后 Python/Node 一并提供）：捕获 stdout 返回，stderr 仍透传，非零抛错。
- **方案**：捕获子进程 stdout 到 buffer（不透传），去掉尾部一个换行（`$(cmd)` 语义）；内部换行保留；UTF-8。
- **边界**：`sh_out` 用 **one-shot**（每次新进程），不复用持久实例——捕获语义与"状态保留"正交，混用易误解；需要状态+输出时自己 `sh` + 重定向到文件读。

## 4. 关键决策（方案对比）

| 决策点 | 选项 | 裁决 |
|---|---|---|
| Node SDK 形态 | 装饰器（JS 无原生）/ 薄注册函数 | **薄注册 `task(name, opts, fn)`**——JS 的习惯写法 |
| 持久 shell 落点 | 用户自写 / SDK 内置 | **SDK 内置**（用户明确要共享实例，别再挂火车头） |
| TOML default 与 required | 允许共存 / 互斥 | **互斥**（有默认即可选；并存加载期报错） |
| `sh_out` 与持久实例 | 复用实例 / one-shot | **one-shot**（捕获语义清晰，不与状态混） |
| `echo` 换行 | 加 / 不加 | **加**（类 `echo`/`println`） |
| `when.os` 过滤时机 | discover 期 / hire 加载期 | **hire 加载期**（discover 保持纯声明） |

## 5. 非目标 / 风险

- 不做 TypeScript 类型声明（node 运行时即可）。
- 不做跨 hire 调用的 shell 共享（每次 run 一个进程）。
- Python/Node 的持久 shell 在 Windows 上要解析 pwsh/Git Bash，测试须实测；Unix 上 pwsh 降级沿用 v2.2.0。
- Node SDK 改变 `init` 产物，须保字节一致 + 向后兼容（旧手写契约仍合法）。
- TOML default 与现有严格校验的兼容要回归测试。
- **本机工具链**：NDK clang++ 19.0.1 在 `D:\Program\Scoop\apps\android-clt\...\ndk\28.2.13676358\...\bin\clang++.exe`，g++ 在 `D:\Program\Scoop\apps\mingw\current\bin\g++.exe`——C++/函数形式端到端本机可跑（加入 PATH），不必只靠 CI。

## 6. 留给 builder 的决定

- C++ 函数形式的 `when` 来源：`// when: windows` 注释（clang 注释已在解析）vs 不支持（只在契约形态支持）。倾向注释。
- `Shell.run` 的参数命名与 `instance`/`shell` 选项的精确形状。
- `echo` 的接受类型（可变模板 vs 仅 `std::string`）。
- `sh_out` 去尾换行的精确规则（去 1 个 `\n` vs 去 `\r\n`）。
- Node SDK 里 `num` 转 number 的时机（dispatch 前统一）。
- 各 SDK 是否也给 `inst=0` 导出模式（B 的可选子集）。

## 7. 测试与验收门

1. 字节一致：`NodeSDKTemplate` 新常量进 contract.md，`TestContract*Listings` 绿。
2. Python/Node 持久 shell：`cd`+`export` 跨调用存活、`inst=2` 隔离、`shell:` 前缀 one-shot、非零抛错、无孤儿进程（用真实 pwsh/bash）。
3. TOML default：未传用默认、类型不匹配报错、与 required 互斥报错。
4. C++ echo：export 模式走 stderr（用 subprocess 捕获断言）、普通模式走 stdout。
5. when.os：外部语言声明的任务在不匹配平台不出现；匹配平台正常。
6. sh_out：捕获输出正确、stderr 透传、非零抛错、尾部换行处理。
7. 全量门：`go test -count=1 ./...`、`-tags integration`、`go vet ./...`；C++ 腿用本机 NDK clang + g++ 实跑（不再只靠 CI）。
