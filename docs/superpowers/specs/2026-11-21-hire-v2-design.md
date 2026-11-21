# hire v2 — 任意语言 taskfile + 改名 + 体验补齐 — Design Spec

**Date:** 2026-11-21
**Status:** Draft（待用户批准）
**取代：** [2026-08-30-taskfile-v2-design.md](2026-08-30-taskfile-v2-design.md)（已批准未实现；本稿吸收其全部可行决定，并扩展为多语言架构）
**Stack:** Go 1.27 + gopher-lua（既有）+ BurntSushi/toml（新增，唯一新增依赖）

## 1. 意图与动因

taskfile 可以用任何语言写（接口需要抽象），运行时由用户自配。首发必须跑通 **Python、Node.js、C++**。

为什么一次做完：仓库无外部用户（旧 v2 稿原话"当前无外部用户，接受破坏"），改名与接口改造同车发布只付一次迁移成本；拆开做要付三次。产品体验短板与审计 must-fix 一并进入本次发布。

## 2. 目标 / 非目标 / 反目标

**目标**

1. 任务文件 = "会说契约的程序"：任何语言、任何可执行形态，通过统一 JSON 契约接入
2. 运行时用户自配：config.lua 的 `runtimes` 表声明命令；C++ 等编译型语言由 build 模板 + 内容哈希缓存支持
3. Lua 保持内嵌原生（零样板、单二进制的默认体验）；TOML 保持静态内建
4. 吸收旧 v2 稿全部决定：简化 Lua 语法、x 全局、持久 shell 实例、export 模式、查找链、bstart 环境交接
5. 产品体验补齐 + 审计 must-fix 进入本次发布
6. 改名 tkx → **hire**（CLI、仓库、module、配置目录、环境变量前缀）

**非目标**

- 不内嵌更多解释器（Goja/Starlark 等）——"任意语言"靠外部运行时，不靠堆引擎
- 不做解释器安装/版本管理（用户自带 python/node/g++；hire 不是 mise）
- 不做沙箱/权限系统——taskfile 是用户自己的可信代码，与 make/just 同级信任
- 不做 discover 结果缓存——每次 CLI 调用现查（Python 冷启 ~100ms，可接受）

**反目标（做错即失败）**

- 引入 YAML 任务清单——"real code"是产品身份，manifest 化是倒退
- 破坏"单二进制、零运行时依赖"（依赖上限：gopher-lua + toml 两个）
- 后台任务机制复杂化——bg 层应几乎零改动

## 3. 命名（用户定名）

**hire**。用户指定：有意义但与工具无关联的完整单词（git 哲学——git 本身也只是英式俚语"讨厌鬼"）。叙事：雇它来干活（hire it to run your chores）。

查重（2026-11-21，逐一验证）：

- 无任何 `hire` CLI / 任务运行器 / 开发工具品类项目
- npm 仅存 8-11 年未动的 `hire-forms-*` 死库与 9 年弃坑的 `hire-me-node`——无 bin 冲突
- brew / scoop / pypi / crates 无 `hire` 二进制
- 已知噪音：`hire` 是 HR 高频词，搜索可发现性打折——用户知情选择
- 备胎留档：`hire`（查重亦干净：仅 2018 年 0★ npm 死库与生物信息缩写词项目）
- 已否决并留档：chore（antimatter-studios/chore，活跃同品类）、errand、gofer、tako、ox、yak、oddjob、hench、tosk（活跃 Rust CLI 任务应用）、butler/valet/alfred/mule（领域大项目）

改名清单：module `hire`、import 路径、二进制 `hire`、配置目录 `~/.config/hire/`、数据目录 `~/.local/share/hire/`、环境变量前缀 `HIRE_*`、goreleaser、scoop 清单 `hire.json`、brew formula `hire.rb`、README 双语、CI 仓库 URL、GitHub 仓库改名 taskx → hire（旧 URL 自动重定向）。**不写** taskx → hire 的迁移代码（无外部用户，README 声明即可）。

## 4. 方案对比（用户已选 B）

- **A. v2 先行、协议后补**：按旧稿先实现，多语言留 v3。否决——TOML 内联 Lua 与外部协议两套扩展机制重叠，二次迁移。
- **B. 折进 v2 一次到位（选定）**：接口抽象为脊柱，旧稿好决定全部保留。
- **C. 内嵌多引擎**：非真"任意语言"、二进制膨胀、运行时不可自配。否决。

## 5. 契约（外部 taskfile 的接口）

hire（调用方）与 taskfile 程序（被调方）之间只有两个动词：

```
<runtime命令> <taskfile> __hire__ discover
  → stdout: {"tasks":[{"name":"build","desc":"...",
        "args":{"tag":{"type":"string","required":true,"desc":"..."},
                "force":{"type":"bool"}}}]}
  → exit 0；非法 JSON / 任务名不合法 / args schema 非法 → 非零退出，stderr 原样透传

<runtime命令> <taskfile> __hire__ run <name>
  → 环境变量 HIRE_ARGS = 已解析参数的 JSON：{"tag":"v1","force":true,"_":["positional"]}
  → 未声明 args 的任务：HIRE_ARGS = "{}"
  → 退出码即任务结果；stdout/stderr 继承
```

设计取舍：

- argv 传动作 + env 传参数：argv 可手工调试（`python Taskfile.py __hire__ discover` 直接跑得通）；参数走 env 避开 Windows argv 引号泥潭
- argparse 严格校验留在 hire 侧：对着 discover 的 schema 校验通过后才发起 run，错误消息跨语言一致
- discover 会执行用户代码：契约文档规定 discover 必须纯（无副作用）；与"跑任务本身也是执行用户代码"同级信任
- 保留名：任务名不得以 `__` 开头（所有适配器加载期统一报错），防与契约动词撞名

## 6. 适配器架构（Go 接口抽象）

```
internal/adapter/
  adapter.go   # Task 内部模型（Name/Desc/Args/Fn）——沿用 v1 内部形状，bg/ls/help 零改动
  lua.go       # LuaAdapter：内嵌 gopher-lua + v2 简化语法（原 taskfile.go 重写）
  toml.go      # TomlAdapter：静态任务 + 内联 Lua run（旧 v2 §6）
  exec.go      # ExecAdapter：外部运行时契约调用；Fn = Go 闭包（拼命令、注 env、透传退出码）
  runtime.go   # 扩展名→runtime 解析、runtimes 表读取、编译缓存
```

- "全任务皆函数"管线保留（旧 v2 §7.1 方案 1）：三种适配器都产出 `Task{Fn}`，下游 `callTask`/`runtime.Run`/`bg`/`ls`/`help` 不感知适配器差异
- ExecAdapter 的 `Task.Fn`：拼 `<runtime> <file> __hire__ run <name>`，`cmd.Env` 追加 `HIRE_ARGS`，stdio 继承，非零退出 → 返回 error（与 Lua 任务抛错同构）
- 嵌套调用（旧 v2 `x.run`）仅存在于原生适配器，且**仅同文件**：每个 Lua 文件独立 LState（cwd/全局文件互不污染全局名）；TOML 内联 run 在该 TOML 文件专属 LState 中合成；跨文件不互调（TOML↔Lua 亦然）；外部 taskfile 想复用逻辑自己写函数——契约不做任务间调用（YAGNI）

## 7. 运行时与编译缓存

config.lua 沿用现有文件，新增 `runtimes` 表：

```lua
return {
  runtimes = {
    python = "python3",                                       -- 字符串 = 命令
    node   = "node",
    cpp    = { build = "g++ -std=c++20 -O2 -o {out} {src}" }, -- 表 = 编译型
  },
}
```

- 扩展名映射（内建）：`.lua`→内嵌、`.toml`→内嵌、`.py`→python、`.js`/`.mjs`→node、`.cpp`/`.cc`→cpp；未注册扩展名 → 报错列出已注册 runtime 与已知扩展名
- 编译型 runtime：缓存键 = sha256(源文件内容 + "\x00" + 解析后的 build 命令串)——改 flags 即失效 → `<cachedir>/hire/cpp/<hash>.exe`（cachedir = `XDG_CACHE_HOME` 或 `%LOCALAPPDATA%`）；命中直接执行；未命中先跑 build 模板（经用户默认 shell；占位符仅 `{out}`/`{src}` 两个）再执行；编译失败 → 报错附编译器完整输出
- 缓存不清理（shortcut: 磁盘增长按源文件数量计，做 `hire cache clean` 时再升级）

## 8. 查找链与合并（吸收旧 v2 §8 并泛化）

```
--taskfile 指定 → 只加载该文件（扩展名定适配器），不合并
否则按序加载存在的文件：
  <cwd>/Taskfile.toml, <cwd>/Taskfile.lua,
  <cwd>/Taskfile.py, <cwd>/Taskfile.js, <cwd>/Taskfile.cpp,
  <全局配置目录>/ 同序五种
一个都不存在 → 报错列出全部搜索路径
加载序 = 全局先、cwd 后；同名任务**后者（cwd）赢** + stderr 警告（含来源文件）——旧 v2 §2 已确认决策，上方列表序仅为探测顺序；`shell =` 声明同规则
查找链固定探测上述五文件；`.mjs`/`.cc` 仅在 --taskfile 显式指定时有效（刻意：自动发现保持短且可预测）
bstart：解析出的文件列表经环境变量 HIRE_FILES（JSON 数组字符串）传给 _run 子进程
  ——顺手修复审计 #1（bstart --taskfile 后台跑错文件）
```

## 9. 吸收自旧 v2 稿的决定（引用而非重抄）

| 旧 v2 章节 | 处置 |
|---|---|
| §3 简化 Lua 语法（全局任务、快照差分、task{}/req/bool、字符串/数组/裸函数简写） | 原样吸收，成为 LuaAdapter 行为 |
| §4 x 全局、function(args) 签名、x.env 新增 | 原样吸收 |
| §5 持久 shell 实例 + 哨兵协议 + instance=0 export 模式 | 原样吸收；**仅 Lua/TOML 内建任务可用**（外部 taskfile 是完整程序，自管子进程；文档声明这是原生适配器的特权）。**修订 2026-11-21**：pwsh 哨兵退出码改为 `$(if ($?) { $LASTEXITCODE } else { 1 })`，使失败的 cmdlet 同样抛错，对齐 v1 one-shot 语义——否则 Windows 默认 shell 下失败静默（T5 评审 Important 裁决） |
| §6 TOML schema + 内联 Lua run | 原样吸收 |
| §8 查找链、合并、bstart 环境交接 | 吸收并按本稿 §8 泛化 |
| §10-12 测试/文档/范围外 | 合并入本稿 §12-14 |

## 10. 产品体验补齐（首发范围）

1. `hire init [--lang lua|python|node|cpp]`：生成可运行的起步 taskfile（外部语言附契约样板与 runtimes 配置提示）
2. 裸 `hire` → 列任务 + 一行 usage 提示（exit 0；不再是 usage + exit 2）
3. 任务名打错 → Levenshtein ≤2 的 did-you-mean 提示（纯标准库实现）
4. `hire <task> --help` → 任务详情（拦截于 argparse 之前）
5. bstart/stop 确认加 `--yes`；非交互 stdin 下 abort → exit 1（修复审计 #5 的正确性部分）
6. `clean --older-than X` 空格/等号两种形式都认（修复审计 #2）；usage 坏行修复（审计 #3）
7. 任务参数名 `shell` 列入保留名，加载期报错（审计 #6）
8. 顺手清理：ParseDuration 去 Sscanf 重写、删除 TKX_* 死环境回退、`_ = shells`（审计 #7/#8/#10）
9. 任务名与内建命令撞名时的报错直接提示 `hire run <name>`（现状藏在 usage 里）

审计编号对照（报告于 2026-11-21 会话交付，未单独入仓库）：#4 → 本节条目 4；#5 → 条目 5 整条修复；#9（Windows tasklist 每实例一次）→ §14 范围外

## 11. 错误处理总表

| 场景 | 行为 |
|---|---|
| runtime 未注册 / 扩展名未知 | 报错列出已注册 runtime 与已知扩展名 |
| discover 非零退出 / 非法 JSON / 任务名不合法 | 报错含文件名与 stderr 摘录 |
| 任务名以 `__` 开头（所有适配器） | 加载期报错 |
| cpp 编译失败 | 报错 + 编译器完整输出 |
| 外部任务非零退出 | 与 Lua 任务抛错同构：`hire: task "x" failed (exit N)` |
| bstart 外部语言任务 | 与内建同径：bg 子进程照旧是 `hire _run`，bg 层零改动 |

## 12. 测试计划

- adapter 单测：三适配器产出同构 Task；ExecAdapter 命令拼装与 HIRE_ARGS 注入；保留名报错；扩展名分派
- runtime 单测：runtimes 表解析（字符串/表两形态）；编译缓存命中/未命中/编译失败
- cli 单测：查找链组合；合并同名警告；init 各语言；did-you-mean；--help；--yes；clean 双形式；裸命令列任务；HIRE_FILES 交接
- 集成（`//go:build integration`）：python/node/cpp 三语 e2e（工具链缺失时 `t.Skip`；CI 装 python/node，g++ 缺失则跳 cpp 腿）；旧语法拒绝；多文件合并；bstart 外部语言任务全生命周期
- 持久实例、哨兵、export 的单测沿用旧 v2 §10 原样执行

## 13. 文档与发布

- `docs/lua.md` 重写为 hire 新语法；`docs/contract.md` 新增（JSON 契约 + 三语言样板全文 + "discover 必须纯"约定）；`docs/toml.md` 新增
- README 双语重写：hire 身份、五分钟首启路径（init → hello world）、多语言主张
- 旧 v2 spec 标注"已被本稿取代"；v1 spec 保持旧稿要求的标注
- 发布：goreleaser/scoop/brew 全部改 hire；打 v2.0.0，CHANGELOG 声明破坏性；GitHub 仓库改名

## 14. 范围外（明确不做）

- tasklist 批量化（Windows ls-running 性能；个位数实例无感）
- 后台任务完成通知
- 颜色输出、watch 改动
- discover 缓存、任务间调用契约、运行时版本管理

## 15. 留给实现者的决定

- adapter 包内部文件划分与是否引入 interface——倾向最少接口：Task 模型已统一，适配器是构造函数而非运行时多态
- did-you-mean 的多候选展示格式
- init 样板的示例任务与措辞
- 契约样板的逐语言代码（以"能通过 e2e"为验收，不逐行审批）
