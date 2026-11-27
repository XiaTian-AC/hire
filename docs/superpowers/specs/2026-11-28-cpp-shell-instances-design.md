# C++ 函数形式 shell 实例能力 — Design Spec

**Date:** 2026-11-28
**Status:** Draft（待用户批准）
**基线:** v2.1.0（master `4e794dc`）
**规模:** 一个特性（预计 v2.2.0）

## 1. 意图与动因

Lua 原生适配器的 `x.sh(cmd, {instance = N})` 提供持久 shell 实例（`cd`/`export`/`set` 跨命令存活）、`instance = 0` 导出模式、`shell:` 前缀 one-shot、内置 shell 白名单与三方 shell 降级。这些能力目前**只在 Lua 可用**——C++/Python/Node 的外部适配器只收到 HIRE_ARGS，拿不到 Go 侧 `runtime.Ctx`。

用户要让 **C++ 函数形式**也具备这套 shell 实例能力，并且用户侧代码保持最短（"别挂一个长火车头"）。本轮只做 C++ 函数形式；Python/Node 不在范围（若要，走 §3 的方案 B 契约扩展另立 spec）。

## 2. 用户侧目标形态（已裁决：一行 include）

用户文件顶部加一行 include，函数体直接调用 `hire::sh(...)`，签名与 Lua 对齐：

```cpp
#include "hire.hpp"                     // 唯一新增的样板：一行

// desc: demo
void demo() {
    hire::sh("cd /tmp");                 // 默认实例 1，状态跨调用保留
    hire::sh("export FOO=42");
    hire::sh("echo $FOO");               // 42
    hire::sh("echo other", 2);           // 2 号新实例
    hire::sh("echo x", 0);               // 导出模式：只收集，不执行
}
```

非目标（明确不做）：不内置 shell/解释器（铁律不变，shell 从用户 PATH 探测）；不在 Python/Node 暴露；不做 Lua 的 `x.exec`/`x.ask`/`x.run` 全量移植（先只做 sh 及实例语义，其余可后续扩展）。

## 3. 方案对比

| 方案 | 形态 | 取舍 | 裁决 |
|---|---|---|---|
| A 内嵌 SDK（本 spec） | 把 shell 实例协议移植进内嵌 `hire.hpp`，用户 include 一行 | 用户侧 1 行；hire 内部改动集中；要解两阶段可见性与判别冲突（§5） | **采纳** |
| A′ 零 include | 函数体直接 `hire::sh()`，discover 用 `-include` 注入声明头 | 用户侧 0 行，但 IDE 标红、两阶段声明须严格一致 | 否决（用户选 A） |
| A″ 独立小头 | 单独 `hire_shell.hpp`，只含 shell 能力 | 语义聚焦，但多一个内嵌常量 + contract 多一段全文 | 否决（用户选 A） |
| B 契约扩展 | 加 `__hire__ sh` 动词，由主进程 `Ctx` 执行 | 一次改协议、全语言受益；但改 discover/run 契约 + 双向通道，大改动 | 本轮不做，另立 spec |

## 4. 硬约束

1. **绝不内置工具链/解释器**（产品铁律）。shell 仍走用户 PATH 探测（`internal/shell`），SDK 只是薄封装，不打包任何二进制。
2. **铁律延伸到纯 Go 解析**：AST 解析、shell 协议不足依赖一律不引第三方包；SDK 是内嵌文本常量，不是运行时下载的二进制。
3. **零用户目录污染**：内嵌头只写到缓存目录 `<cachedir>/hire/cpp/`，与现有 wrapper 同目录（v2.1.0 已确立）。
4. **字节一致**：`CppHeaderTemplate` 与 `docs/contract.md` 的 C++ 样板全文必须逐字一致，由 `TestContractCppListings` pin（`init_test.go`）——改头必同步重印。
5. **缓存键已覆盖**：函数形式键含 `CppHeaderTemplate` 字节 + 代码生成版本盐（`cppfunc.go:704-714`），改头自动失效缓存，无需新增盐。

## 5. 落地要点（builder 必须解决的三处硬冲突）

### 5.1 判别冲突：`isContractForm` 不能再认 `hire.hpp` 字节

现状 `cppfunc.go:34-36`：文件含 `hire.hpp` 或 `__hire__` 子串即判遗留契约形态。函数形式文件现在要写 `#include "hire.hpp"`，会命中前者被误判 → 走 raw 路径 → `hire_main` 注册逻辑与纯函数冲突。

**裁决**：判别依据改为 `bytes.Contains(src, "hire_main") || bytes.Contains(src, "__hire__")`，不再单独认 `hire.hpp` 字节。理由（builder check 修正了初稿的事实错误）：SDK 遗留形态的用户文件只有 `#include "hire.hpp"` + `hire_main` 定义，`__hire__` 字面量只在 hire.hpp 内部（`templates.go:472,518`），不在用户文件字节里；手写 raw 形态则含 `__hire__`。初稿"只认 `__hire__`"会把 SDK 遗留误判成函数形式，在 `validateFuncTasks`（`cppfunc.go:535`）被 "hire_main is reserved" 拒绝——这是不可接受的破坏。须补测试 pin 三种：纯函数 + include → 函数形式；SDK 遗留（include + hire_main）→ raw；手写 raw（`__hire__`）→ raw。契约文档 prose（`contract.md:390` 含 `hire.hpp` 或 `__hire__` 字样时走 raw）同步改写为按 `hire_main`/`__hire__` 判别。

### 5.2 两阶段可见性：头必须在解析前生成，且用户 include 能被解析

- **discover 阶段**：`parseFuncForm`（`cppfunc.go:462-465`）把用户文件直接喂给 clang。用户文件里的 `#include "hire.hpp"` 此时若头文件未生成会报错。须在 parse 之前先把 `CppHeaderTemplate` 写到缓存目录（或临时 include 目录），并给 clang 加 `-I<cachedir>`。
- **compile 阶段**：wrapper 已在缓存目录 `#include "hire.hpp"`（`cppfunc.go:603`），用户文件经绝对路径 include（`cppfunc.go:604`）——用户文件里的 `#include "hire.hpp"` 同样要靠 `-I<cachedir>` 解析。但 build 命令是用户自配、只有 `{out}`/`{src}` 占位符（`runtime.go:186`），**hire 不能篡改用户 build 模板塞 `-I`**。

**裁决**（builder check 修正：初稿的"`#pragma once` 让第二次 include 变 no-op"不成立——预处理器必须先**找到**文件才能 dedup，用户文件的 include 相对用户目录解析，那里没有头）：**给两条路径都注入 `-I<headerdir>`**——
1. discover：`parseFuncForm`（`cppfunc.go:462-465`）的 clang argv 加 `-I<headerdir>`；头必须在 parse 之前写出（`loadCppFuncForm` 的 `MkdirAll`/头写出需从 `:760-763` 前移到 parse `:750` 之前）。
2. compile：`expandBuild`（`runtime.go:185-186`）扩展——函数形式构建时在展开后的 build 命令里**自动追加** `-I"<headerdir>"`（`headerdir` = 头所在缓存目录）。用户 build 模板**不需改动**；`{inc}` 占位符仅作 escape hatch 保留（默认空），需在 contract.md 声明新占位符。
3. 自动追加的是纯附加 `-I`，不替换用户原有参数；仿 `{src}`/`{out}` 的加引号规则处理路径中的空格（`runtime.go:186`）。

### 5.3 生命周期：实例必须在进程退出前 teardown

外部适配器是 one-shot 子进程（`exec.go:218`：`__hire__ run <name>` 跑完即退出）。持久 shell 是独立进程，父进程退出不会自动回收 → 必须显式关闭。

**裁决**：用 **RAII` ——在 `CppHeaderTemplate` 的 `main()` 里放一个栈上守卫对象，析构时调用 `close_instances()`（close stdin + wait/kill，语义对齐 Go 的 `ctx.go:317-342` teardown）。不用"dispatch 之后显式调用"——`sh()` 命令失败会抛异常（`templates.go:501-513` 的 dispatch 无 try/catch），显式调用会被跳过导致 `std::terminate`；RAII 在异常展开时也析构。`dispatch` 内部加 try/catch 把异常转成干净的非零退出码（对齐 `exec.go:221-229` 的 `task failed (exit N)`）。**注意 discover 路径不得创建实例**（discover 必须纯——函数形式缓存例外已在 contract.md 声明）。

## 6. 移植的 API 面（与 Lua 对齐的最小集）

`hire.hpp` 新增一个 `hire::sh` 函数族 + 实例管理，签名建议（builder 可微调，须进 contract.md 全文）：

```cpp
namespace hire {
namespace detail { struct shell_instance; /* stdin pipe + reader + child */ }

// cmd: 要执行的命令；inst: 实例号（1 默认，2 新建，0 导出模式）
// shell: 可选 shell 名（默认探测；前缀 "bash:" 等强制 one-shot）
// 失败抛异常 —— dispatch 的 try/catch 转成干净的非零退出码
void sh(const std::string& cmd, int inst = 1, const std::string& shell = "");
void close_instances();               // RAII 守卫析构调用；幂等
namespace detail {
    void finish_export(const std::string& name, bool ok);  // 见下
}
}
```

语义对齐 `internal/runtime/ctx.go`：
- 默认 `inst=1` 持久实例；同 `(inst, shell)` 复用同一子进程（`ctx.go:264` 的 `instKey`）；
- `inst=0` 导出模式：命令只收集不执行，任务成功时 `detail::finish_export(name, ok)` 一次性打印到 stdout（TTY 时 `eval`/`iex` 提示，`ctx.go:391-426`；失败时丢弃并报 stderr）。**builder 注意**：`sh()`/`close_instances()` 不知道任务名与成功状态，`finish_export` 必须由 `main()`/`dispatch` 在 run 分支结束处调用（成功/失败两条路径都要调），形态由 builder 定；用户 `std::cout` 不经过 `x.echo` 的 export 改道（`ctx.go:504-507`），此差异须在 contract.md 注明；
- `shell:` 前缀强制 one-shot（`ctx.go:158-162, 183-199`）；**仅内置 shell 名**（pwsh/bash/sh）可被识别为前缀，config.lua 注册的第三方 shell 名 C++ 进程拿不到（`config.go:118-126`、`runner.go:45` 不下发，exec.go:218-220 只传 HIRE_ARGS）——**本轮明确分歧**：C++ 侧只支持内置 shell 名 + 默认探测，第三方 config shell 走降级 one-shot；要 parity 须另立 spec 扩展契约传 registry/default shell；
- 内置 shell（pwsh/bash/sh）支持持久；三方 shell 降级 one-shot 并警告一次（`ctx.go:239-260`）；pwsh 在 Unix 降级 one-shot（`ctx.go:250-260`）；
- 哨兵协议：`__HIRE_RC:<nonce>:<rc>`，逐行扫 stdout 取退出码；实例提前退出报错（`ctx.go:344-387`）；**pwsh 的两处细节必须照抄源码而非 spec 摘要**：`$LASTEXITCODE = 0` 播种（`ctx.go:309-313`）与 sentinel 用 `$?` 兜底（`ctx.go:365`）；
- `interactive` 选项（`ctx.go:116,173-174`）**本轮非目标**：C++ 的 stdin 已是进程级继承，不做 per-command 交互开关；
- 命令失败：非零退出 → 进程非零退出码 → hire 报 `task <name> failed (exit N)`（与现有外部适配器同构，`exec.go:221-229`）。

## 7. 测试与验收门

1. `TestContractCppListings` 字节一致（改头后重印 contract.md）。
2. discover/compile 两路径不因 `#include "hire.hpp"` 报"头文件找不到"（§5.2）。
3. 判别测试三种：纯函数 + include → 函数形式；SDK 遗留（include + hire_main）→ raw；手写 raw（`__hire__`）→ raw（§5.1）。
4. shell 实例语义集成腿（用本机已有 shell，如 `sh` / `bash`）：`cd`+`export` 跨调用存活；`inst=2` 状态隔离；`inst=0` 导出模式只收集；`shell:` 前缀 one-shot；命令失败非零退出；进程退出后无遗留 shell 子进程（§5.3）。
5. 缓存失效：改 `CppHeaderTemplate` 后函数形式缓存自动重建（§4.5）。
6. 全量门：`go test -count=1 ./...`、`go test -count=1 -tags integration ./integration/`、`go vet ./...` 全绿。

## 8. 非目标 / 风险

- Python/Node 不在本轮；如需走方案 B 另立 spec。
- shell registry 分歧（§6）：C++ 侧只认内置 shell 名，config.lua 注册的第三方 shell 无法被识别为 `shell:` 前缀或解析持久实例；要 parity 须另立 spec 扩展契约下发 registry。
- `interactive` 非目标（§6）。
- Windows 上持久实例仅 pwsh；三方 shell 降级路径须在 Windows 实测。
- clang 版本漂移：`-fparse-all-comments` 的 AST 形状已由 fixture pin，本轮不改解析逻辑。
- 用户 build 模板若自定义到极端（不用 g++ 而用 MSVC），`-I` 注入须按平台调整（MSVC 用 `/I`）。

## 9. 留给 builder 的决定（builder check 后收敛）

- API 签名的具体 C++ 形态（重载 vs 默认参数，须进 contract.md 并保字节一致）。
- `finish_export` 在 `main()`/`dispatch` 里的具体挂点（成功/失败两条路径都要调）。
- pwsh sentinel 细节照抄 `ctx.go:309-313,365`，不按 spec 摘要自创。
