# Taskfile v2 — 语法简化 + TOML 支持 + shell 实例控制 — Design Spec

**Date:** 2026-08-30
**Status:** 已被 [2026-11-21-hire-v2-design.md](2026-11-21-hire-v2-design.md) 取代（本稿全部可行决定被其 §9 吸收，并扩展为多语言架构；本稿不再单独实施）
**前置:** 基于 [v1 设计规格](2026-08-21-tkx-design.md) 的演进，**破坏性变更**（breaking change）
**Stack:** Go 1.27 + gopher-lua + BurntSushi/toml（新增，唯一新增依赖）

## 1. 概述

三个目标，一次交付：

1. **Lua 语法极简化** —— 去掉 `local tasks = {...} return tasks` 样板，任务直接写全局；字符串/数组/裸函数简写；`x.` 前缀替代 `ctx:`；`task{}`/`req`/`bool` 辅助函数。**旧语法完全移除，不做兼容。**
2. **TOML 支持** —— `Taskfile.toml` 与 `Taskfile.lua` 同时支持、同时加载、无优先级；静态任务用 TOML，动态逻辑用内联 Lua。
3. **shell 实例控制** —— `x.sh(cmd, { instance = N })`：默认同一任务复用一个持久 shell 实例（cd/env 状态延续）；`instance=0` 为 export 模式（配合 `eval`/`iex` 影响当前终端）。

**破坏性声明**：现有用户的 `Taskfile.lua`（v1 语法）升级后失效。当前无外部用户，接受破坏。`docs/lua.md`、`testdata/Taskfile.lua`、integration 内嵌样例全部重写为新语法。

## 2. 已确认决策

| 决策点 | 结论 |
|---|---|
| 旧语法兼容 | **不兼容**，干净破坏，只认新语法 |
| run 签名 | `function(args)` 或 `function()`；args 恒传入，不声明则忽略；**args 不做全局**（嵌套 x.run 会污染） |
| x 前缀 | `x` 为全局注入（每次任务执行前 SetGlobal），`x.sh` 点号调用；嵌套 run 覆盖 x 无害（同一 Ctx） |
| instance=0 机制 | eval 模式语法糖：任务收集命令文本，结束时输出到 stdout，由用户 eval；shell 相关逻辑区分处理 |
| instance=0 命令的 map 形式 | **砍掉**，用户自己 if 判断写对应 shell 语法 |
| TOML 库 | BurntSushi/toml（事实标准，零传递依赖，经代理 go get） |
| 文件查找链 | `--taskfile` > 当前目录 > 全局；同目录 `.toml`+`.lua` **同时加载无优先级** |
| 跨文件合并 | 先全局后当前目录，同名任务后者赢 + stderr 警告 |
| 持久实例实现 | 真·持久 shell 进程 + stdin 命令通道 + 哨兵协议读退出码 |

## 3. 新 Lua 语法（唯一语法）

### 3.1 任务声明形式

```lua
-- 可选：全局默认 shell（覆盖平台默认 pwsh/bash），保留名
shell = "pwsh"

clean = "rm -rf dist/"                  -- 字符串 = 单命令

quick = {                               -- 全字符串数组 = 命令序列，顺序执行
  "echo line1",
  "echo line2",
}

hello = function()                      -- 裸函数 = 无参数任务
  x.echo("hi")
end

release = task {                        -- 完整形式
  desc = "build and release",
  tag   = req "git tag",                -- 必填 string
  force = bool "force push",            -- bool 开关（可选）
  limit = { desc = "并发数" },           -- 可选 string（全形式）
  run = function(args)                  -- function(args) / function() 均可
    x.sh("git tag " .. args.tag)
    if args.force then x.sh("git push --force --tags") end
  end,
}

build = task {                          -- 平台条件
  when = { os = "windows" },            -- os 值为 string 或 string 数组
  cmd = "go build -o app.exe .",        -- cmd 任务不能有 run
}

deploy = task {
  cmd = "bash:./deploy.sh",             -- 命令前缀指定 shell（one-shot）
}
```

### 3.2 保留名与全局收集

- **保留全局名**：`x`、`shell`、`task`、`req`、`bool`。其中 `shell`（string 值）被提取为默认 shell 配置、**不是任务**；其余保留名若被赋值且快照差分判定为任务候选 → 加载期报错。
- **快照差分收集**：DoFile 前快照 `_G` 全部键值（含 Lua 内置 `print`/`pairs`/`error`/`string` 等）；DoFile 后**新增或值发生变化的键**才是任务候选。`task`/`req`/`bool` 由 Go 侧 DoFile 前注入（在快照内，天然排除）。
- 候选值类型分派：
  - `string` → 单命令任务
  - 全 `string` 元素的 `table` → 命令序列任务
  - `function` → 裸函数任务（等价 task{run=fn}，Args=nil 宽松解析）
  - 带 `__tkx=true` 隐藏标记的 `table` → task{} 完整任务
  - 未标记的 `table` → 报错：“任务必须是字符串、字符串数组、函数或 task{...}”
  - 其他类型 → stderr 警告 + 跳过
- `task{}` 是 Go 注入的 Lua 函数：校验字段后给表打 `__tkx=true` 标记并原样返回。
- `return` 的值**忽略**。若文件返回了 table 且快照差分为空 → 明确报错：“检测到旧语法（return 表），v2 已移除，请使用全局任务声明”。

### 3.3 task{} 字段

| 字段 | 类型 | 说明 |
|---|---|---|
| `desc` | string | `tkx ls` / `tkx help <task>` 显示 |
| `args` | table | 任务名 → spec（`req`/`bool` 糖或全形式 `{type=, required=, desc=}`）；声明即严格校验 |
| `run` | function | 任务函数，与 `cmd` 互斥 |
| `cmd` | string | 命令字符串，与 `run` 互斥；可带 `shell:` 前缀；args 声明仅用于 CLI 校验 |
| `when` | table | `{ os = "windows" }` 或 `{ os = {"linux","darwin"} }`；**仅支持 os**；不匹配 → 任务不存在（ls 不显示，run 报 not found） |

`cmd`/`run` 缺一不可且互斥 → 违反均加载期报错（文件名 + 任务名）。

## 4. x 全局与执行模型

### 4.1 x 方法

| 方法 | 行为 |
|---|---|
| `x.sh(cmd, opts?)` | 见 §5 |
| `x.exec(name, args?)` | 直接 exec，不经 shell（保持 v1 语义） |
| `x.run(name, args?)` | 调用同文件其他任务（递归上限 100，保持 v1） |
| `x.echo(...)` | 打印 stdout；**export 模式下自动改写 stderr** |
| `x.ask(prompt, default?)` | 保持 v1 语义（后台无 stdin 返回 default） |
| `x.cwd()` / `x.os()` | 保持 v1 |
| `x.env(name)` | **新增**：环境变量，返回 string 或 nil |

### 4.2 调用协议变更

```
v1: PCall(task.Fn, ctxUD, argsTable)     → run = function(ctx, args)   ctx:sh(...)
v2: SetGlobal("x", ctxUD); PCall(fn, argsTable) → run = function(args)  x.sh(...)
```

- `callTask` 改为传 1 个位置参数（argsTable）；x 走全局。
- 嵌套 `x.run`：内层 SetGlobal 覆盖 x —— 内外层共享同一 `*Ctx`，无实际影响。
- Taskfile 顶层定义的辅助函数可在任务执行期间通过全局 x 操作（加载期 x 为 nil，不可用）。
- `x` 参数仍可显式声明（`function(x, args)` 会把 argsTable 绑到第一个形参）——文档只教 `function(args)`。

## 5. x.sh 实例语义

### 5.1 opts 总表

| opts | 行为 |
|---|---|
| 省略 | instance=1：任务内**持久实例**（cd/env 状态延续） |
| `instance = 1` / `2` / `3`... | 指定持久实例号（首次使用冷启动；键 = 实例号+shell名） |
| `instance = 0` | export 模式，见 §5.4 |
| `shell = "name"` | 该次命令 one-shot（显式 shell = 明确不要持久） |
| `interactive = true` | one-shot + 接 stdin（持久实例占用 stdin 作命令通道，二者互斥） |

### 5.2 持久实例实现

- 实例池挂 `Ctx`：`map[key]*instance`，`key = {inst int, shell string}`；惰性创建。
- 持久模式**仅内建 shell**：pwsh / bash / sh。第三方注册 shell → 降级 one-shot + stderr 警告（每任务一次）。
- spawn 参数（区别于 v1 的一次性参数）：
  - pwsh：`-NoProfile -NonInteractive -Command -`（从 stdin 读命令）
  - bash：`--norc --noprofile -s`（**不带 -e**，错误经哨兵上报）
  - sh：`-s`
- **哨兵协议**：每次写入 `cmd` + 换行 + 回显行，逐行扫描 stdout（边扫边透传到任务 stdout），命中哨兵行取出退出码：
  - bash/sh：`printf '__TKX_RC:%s:%s\n' <nonce> "$?"`
  - pwsh：`Write-Output "__TKX_RC:<nonce>:$LASTEXITCODE"`
  - nonce 每命令随机，防用户输出撞行
- 退出码 ≠ 0 → `RaiseError`（Lua 错误，与 v1 语义一致）；实例本身不销毁。
- 生命周期：任务结束（正常返回 / Lua 错误 / panic）统一关停——关 stdin → 短暂等待 → kill。实例进程崩溃/卡死 → 关停该实例，下次命令冷重启。

### 5.3 命令前缀

`"bash:cmd"` 形式：按**第一个 `:`** 切分，前缀**仅当匹配已知 shell 名**（内建 pwsh/bash/sh 或 config 注册名）才当作 shell 选择，否则整体按原命令执行（保护 `C:\path`、`D:\...` 盘符）。前缀命令一律 one-shot。

### 5.4 export 模式（instance = 0）

- `x.sh(cmd, { instance = 0 })` → **不执行**，命令文本按序收集进 Ctx 缓冲。
- 仅内建 shell 可用；第三方注册 shell 或 bstart 后台任务（`Ctx.Background`）→ 抛错。
- export 期间 `x.echo` → stderr（防污染 eval 输出）；普通 x.sh 照常子进程执行，可与 instance=0 混用。
- 任务正常结束 → 缓冲按序 join("\n") 写 stdout；任务失败 → 丢弃缓冲 + stderr 提示 export 已中止。
- **未 eval 检测**：stdout 为 TTY（`os.Stdout.Stat().Mode() & os.ModeCharDevice`，纯标准库）→ stderr 打用法提示：
  - pwsh：`提示: 此任务包含 instance=0 命令。请使用: iex (tkx <task>)`
  - bash/sh：`提示: 此任务包含 instance=0 命令。请使用: eval "$(tkx <task>)"`
- 管道/重定向（非 TTY）→ 不打提示（视为正在被 eval）。

## 6. TOML 支持（Taskfile.toml）

### 6.1 Schema

```toml
shell = "pwsh"                          # 可选，全局默认 shell

[tasks.clean]
cmd = "rm -rf dist/"

[tasks.quick]
cmd = ["echo line1", "echo line2"]

[tasks.release]
desc = "build and release"
run = """                               # 内联 Lua，能力等同 Lua 任务（x./args 可用）
x.sh("git tag " .. args.tag)
if args.force then x.sh("git push --force --tags") end
"""
[tasks.release.args.tag]                # 或内联表 args = { tag = { required = true } }
required = true
desc = "git tag"
[tasks.release.args.force]
type = "bool"

[tasks.build]
cmd = "go build -o app.exe ."
when = { os = "windows" }
```

### 6.2 解析与校验

- 库：`github.com/BurntSushi/toml`；新增 `internal/taskfile/toml.go`。
- 字段：`cmd`（string | []string）、`run`（string）、`desc`、`args.<name>.{type?, required?, desc?}`、`when.os`。
- `cmd` 与 `run` 互斥且必居其一；`type` 缺省 `"string"`；TOML 无 `req`/`bool` 糖（全形式足够）。
- `run` 字符串在加载期包一层合成 Lua 函数（`load` 语义），后续路径与 Lua 任务同构。
- TOML 任务与 Lua 简写任务共享同一“合成函数”机制（§7 方案 1）。

## 7. 架构与关键实现取舍

### 7.1 简写/TOML 任务接入管线（已定：方案 1）

Load 期把简写任务**合成为 `*lua.LFunction`**（Go 闭包持有命令列表，执行时走与 ctxSh 相同的持久实例路径）。下游 `callTask`/`runtime.Run`/`bg`/`ls`/`help` 零改动——所有任务在管线里都是函数。

> 备选（否决）：Task 加 Kind 字段执行期分支（波及全部消费方）；编译成 Lua 源码文本再 eval（转义地狱）。

### 7.2 持久实例（已定：方案 A）

真·持久进程 + 哨兵协议（§5.2）。

> 备选（否决）：one-shot + cd/export 重放（alias/venv/shell 函数全丢，假持久）；不持久（违背已确认语义）。

### 7.3 改动落点

| 位置 | 改动 |
|---|---|
| `internal/taskfile/taskfile.go` | 重写：按扩展名分派加载器、快照差分、类型分派、合成函数、when 过滤 |
| `internal/taskfile/toml.go` | 新增：TOML 解析 + 校验 → 转内部任务模型 |
| `internal/runtime/ctx.go` | SetGlobal("x")、PCall 单参、实例池、export 缓冲、前缀解析、x.env、echo 导流 |
| `internal/shell/` | 持久模式 spawn 参数、`IsBuiltin` 判定（哨兵可用性） |
| `internal/cli/cli.go` | `loadTaskfile` 查找链 + 合并；`--taskfile` 单文件语义 |
| `internal/config/config.go` | `TaskfilePath()` → 目录探测助手（toml+lua 两个候选） |
| `internal/bg/` | `_run` 经环境变量接收已解析任务文件路径列表（防二次查找漂移） |
| `internal/argparse/` | 不变 |
| `docs/`、`testdata/`、`README*.md` | 全部重写为新语法；新增 `docs/toml.md` |

依赖变更：`go.mod` 新增 `github.com/BurntSushi/toml`（需网络，走代理 127.0.0.1:7890）。

## 8. 查找与合并

```
--taskfile 指定      → 只加载该文件（按扩展名分派），不合并其他文件
否则，按序探测并加载存在的文件：
  1. <cwd>/Taskfile.toml
  2. <cwd>/Taskfile.lua
  3. <全局配置目录>/Taskfile.toml
  4. <全局配置目录>/Taskfile.lua
  一个都不存在 → 报错，列出搜索过的路径
合并：按加载序依次合并入任务表，同名后者赢 + stderr 警告（含来源文件名）
shell 声明：后加载者覆盖
bstart：tkx bstart 解析出最终文件列表后经环境变量传给 _run 子进程
```

## 9. 错误处理总表

| 场景 | 行为 |
|---|---|
| 返回旧式 tasks 表且无新语法任务 | 明确报错“旧语法已移除” |
| 全局值类型非法（number 等） | stderr 警告 + 跳过 |
| 未标记 table 赋给任务名 | 报错提示 task{} |
| task{} 缺 run/cmd / 两者并存 | 加载期报错（文件名+任务名） |
| 任务占用保留名（x/shell/task/req/bool） | 加载期报错 |
| when 不匹配 | 任务不存在 |
| 持久 shell 崩溃/卡死 | 关停实例，下条命令冷重启 |
| 第三方 shell + instance=0 | 抛错 |
| bstart + instance=0 | 抛错 |
| 哨兵超时（命令挂死不回） | 哨兵读取无超时——挂死即任务挂死（Ctrl+C 清理）；**不做超时机制**（YAGNI，`tkx stop` 兜底） |

## 10. 测试计划

- **taskfile 单测**：四种声明形式；快照差分（重定义 `print` 成任务、内置名不误收）；保留名报错；未标记表报错；旧语法 return 报错；when 过滤；合成任务经 `ls` 可见可运行。
- **toml 单测**：cmd string/[]string、run 内联、args 全形式、cmd+run 互斥、when、非法 schema 报错。
- **runtime 单测**：持久实例 cd/env 延续（`x.sh("cd /tmp")` 后 `x.sh("pwd")` 输出 /tmp）；多实例隔离（instance=2 不受实例 1 影响）；RC≠0 抛错；export 输出格式与失败丢弃；echo 导流 stderr；前缀解析（`C:\` 不误判、`bash:cmd` 生效、未知前缀原样执行）。
- **shell 单测**：持久 spawn 参数表；IsBuiltin 判定。
- **cli 单测**：查找链四种组合；同目录双格式合并与同名警告；--taskfile 隔离；无文件报错路径列表。
- **integration**：新语法 e2e（含 task{}/req/bool）；TOML e2e；cwd 优先 + 全局 fallback；export 模式管道输出（`tkx task | cat` 断言 stdout 内容）；bstart 携带新语法任务。
- `testdata/Taskfile.lua` 迁移为新语法，兼当文档样例。

## 11. 文档迁移

- `docs/lua.md`：按新语法全量重写（结构、task{}、x 方法、instance、export/eval 用法、Lua 子集）。
- `docs/toml.md`：新增，TOML schema 与示例。
- `README.md` / `README-zh.md`：示例与特性列表同步。
- v1 设计规格标注“语法已被 v2 取代”链接到本文件。

## 12. 范围外（明确不做）

- `when` 支持 env / 自定义表达式（仅 os）
- task 级 `shell` 字段（用前缀或 opts.shell）
- TOML 的 req/bool 糖
- 旧语法任何形式的兼容层
- TOML steps 数组 + 步骤级 when（最终方案无 steps）
- instance 池跨任务/跨调用持久化
- 哨兵读取超时机制
