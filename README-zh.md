<div align="right">

[English](README.md) · [中文](README-zh.md)

</div>

<h1 align="center">hire</h1>

<p align="center">
  <strong>任意语言 taskfile 的任务运行器，自带后台任务管理。</strong>
  <br />
  <em>真代码不做 YAML · Lua 原生内嵌，Python/Node/C++ 走契约 · 全局任务 · 后台分离执行</em>
</p>

<p align="center">
  <a href="#五分钟首跑"><img src="https://img.shields.io/badge/快速上手-4CAF50?style=for-the-badge" alt="Quick Start" /></a>
  <a href="docs/lua.md"><img src="https://img.shields.io/badge/Lua_参考-2C2D72?style=for-the-badge&logo=lua&logoColor=white" alt="Lua Reference" /></a>
  <a href="docs/contract.md"><img src="https://img.shields.io/badge/契约-00ADD8?style=for-the-badge" alt="Contract" /></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/许可证-MIT-yellow?style=for-the-badge" alt="License" /></a>
</p>

<p align="center">
  <a href="https://github.com/XiaTian-AC/hire/releases"><img src="https://img.shields.io/github/v/release/XiaTian-AC/hire?style=flat&color=blue" alt="Release" /></a>
  <a href="https://github.com/XiaTian-AC/hire/actions/workflows/release.yml"><img src="https://img.shields.io/github/actions/workflow/status/XiaTian-AC/hire/release.yml?branch=master&label=release%20ci&style=flat" alt="Release CI" /></a>
  <img src="https://img.shields.io/badge/平台-Windows_%7C_Linux_%7C_macOS-lightgrey?style=flat" alt="Platforms" />
</p>

---

## 为什么是 hire？

- 🧑‍💻 **taskfile 是真代码** —— 不玩 YAML 杂技。Lua 原生内嵌（零样板、单二进制）；
  其它任何语言通过两动词 JSON 契约接入。
- 🌍 **任意语言** —— `Taskfile.py`、`Taskfile.js`、`Taskfile.cpp` 与 Lua 同权：
  在 `config.lua` 里声明运行时命令，该编译的 hire 编译并缓存，
  `hire ls` 跨全部文件列任务。
- 🌐 **全局任务** —— 在 `~/.config/hire/` 定义一次，任意目录可跑；工作目录的
  项目文件同名时后者赢（附警告）。
- 🌙 **后台执行** —— `hire bstart` 把任务从终端分离。关掉窗口任务照跑；
  `watch` / `stop` / `clean` 管理全生命周期。
- 🔁 **持久 shell** —— 任务内的命令复用一个活着的 shell 实例（`cd`/环境变量
  延续），另有 `instance=0` export 模式配合 `eval`/`iex`。
- 📦 **单二进制** —— 一个 ~8 MB 可执行文件，零运行时依赖。Python/Node/g++ 是
  *你自己的* 工具链，按配置调用；hire 只内嵌 Lua。

## 五分钟首跑

**安装** —— Scoop（Windows）：

```powershell
scoop bucket add XiaTian-AC https://github.com/XiaTian-AC/XiaTian-AC-bucket
scoop install hire
```

Homebrew（macOS/Linux）：

```bash
brew tap XiaTian-AC/XiaTian-AC-bucket
brew install hire
```

或下面的[源码构建](#源码构建)。

**脚手架 + 首跑** —— 在任意空目录：

```powershell
hire init          # 生成可运行的 Taskfile.lua
hire hello         # → hello from hire
```

整个闭环就这么多：`hire init` 写一个带注释的起步文件，`hire <任务名>` 跑任务，
`hire ls` 列任务。编辑 `Taskfile.lua` —— 任务是全局变量，没有包装表。

**带上你自己的语言**：

```powershell
hire init --lang python    # 或 node / cpp
```

生成的文件会说 [`__hire__` 契约](docs/contract.md) —— hire 调用程序做任务发现，
解析好的参数经 `HIRE_ARGS` 环境变量传入。在 `~/.config/hire/config.lua` 里声明
运行时：

```lua
return {
  runtimes = {
    python = "python",
    node   = "node",
    cpp    = { build = "g++ -std=c++20 -O2 -o {out} {src}" },  -- 编译 + 缓存
  },
}
```

## 用法

### 带参数的任务

hire 按每个任务声明的 schema 校验 CLI 参数 —— 未声明的 flag 直接拒绝并列出
允许项，任何代码都不会跑。

```lua
release = task {
  desc = "test, commit, tag, push",
  args = {
    tag   = req "git tag, e.g. v1.1.0",   -- 必填 string
    force = bool "force push",            -- 开关
  },
  run = function(args)
    x.sh("go test ./...")
    x.exec("git", {"tag", args.tag})       -- 不经 shell，无注入面
    x.sh(args.force and "git push --force --tags" or "git push --tags")
  end,
}
```

```powershell
hire release --tag v1.1.0 --force
hire help release            # desc + 类型化参数表
hire release --help          # 同上，任务级
```

### 后台生命周期

```powershell
hire bstart dev-server       # 分离启动；关终端照跑
hire ls-running              # dev-server#1  pid 12345  running
hire watch dev-server        # 实时追日志；Ctrl+C 只退出查看器
hire stop dev-server         # 杀整棵进程树
hire clean --older-than=7d   # 清理已结束实例 + 日志
```

`bstart` 对外部语言 taskfile 完全同权 —— 后台子进程重载同一份解析出的文件列表，
不管它是什么语言写的。

### 顺手命令

```powershell
hire                          # 裸 hire：列任务 + 一行 usage 提示
hire init                     # 生成起步 taskfile（lua 缺省）
hire config                   # 打印生效配置（display + shells）
hire --taskfile .\dev\My.cc hello    # 任意受支持扩展名，单文件隔离
hire build --shell bash       # 单次运行指定注册 shell
iex (hire env-setup)          # export 模式：收集的命令进你自己的 shell
```

## 文档

| 文档 | 内容 |
|---|---|
| [Lua 参考](docs/lua.md) | v2 语法：声明形式、`task{}`/`req`/`bool`、`x` API、持久实例（及其坑）、export 模式 |
| [契约](docs/contract.md) | `__hire__` 两动词、discover JSON、`HIRE_ARGS` 形状、runtimes 配置、Python/Node/C++ 样板全文 |
| [TOML 参考](docs/toml.md) | 纯静态 `Taskfile.toml` schema：`cmd`/`cmds`、参数、插值、`deps`、严格性规则 |
| [示例 taskfile](testdata/Taskfile.lua) | 覆盖主要形式的可运行样例 |

## 源码构建

module 名是 `hire`（裸名，不是托管路径），所以 `go install <repo>@latest`
不可用 —— clone 后 build：

```bash
git clone https://github.com/XiaTian-AC/hire.git
cd hire
go build -o hire .
# 跑测试：go test ./... && go test -tags integration ./integration/
```

## 配置

文件都在 hire 配置目录下（所有平台都是 `~/.config/hire/`，认 `XDG_CONFIG_HOME`）：

| 文件 | 用途 |
|---|---|
| `Taskfile.*` | 全局 taskfile —— 五个候选全部探测、合并加载。 |
| `config.lua` | 可选：`shells` 注册表、`display` 选项、外部语言 `runtimes`。 |

```lua
return {
  shells = {
    gitbash = "C:\\Program Files\\Git\\bin\\bash.exe",
  },
  display = {
    ls_running = {
      time = "1h",          -- "0" 只看运行中 · 30m/1h/2d/1w 已结束窗口
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

`hire config` 查看生效值。

## 项目结构

```
main.go                    入口，转 internal/cli
internal/
├── cli/                   命令分发（init/ls/bstart/watch/stop/clean/config…）
├── adapter/               lua / toml / exec 适配器 -> 统一 Task 模型
│   └── templates.go       hire init 样板（lua/python/node/cpp）
├── config/                XDG 路径、config.lua（shells、display、runtimes）
├── runtime/               任务执行：x 全局、持久 shell、export 模式
├── argparse/              通用 --flag 解析、严格 schema 校验
├── shell/                 shell 解析（含 Windows Git Bash 探测）
├── bg/                    注册表、分离启动器、进程表、watch
docs/                      lua.md、contract.md、toml.md
testdata/Taskfile.lua      可运行示例
integration/               端到端测试（go test -tags integration）
.goreleaser.yaml           多平台发布配置
.scoop/ .brew/             CI 渲染的包管理器模板
```

## 技术栈

| 层 | 选择 |
|---|---|
| 核心 | Go，尽量只用标准库 |
| 任务语言 | Lua 5.1 经 [gopher-lua](https://github.com/yuin/gopher-lua) 内嵌；Python/Node/C++ 走契约；TOML 经 [BurntSushi/toml](https://github.com/BurntSushi/toml) |
| 发布 | [GoReleaser](https://goreleaser.com) + GitHub Actions |

## 参与贡献

1. Fork、建分支、提交、推送、PR —— 常规流程。
2. 先跑门禁：`go test ./...` 与 `go test -tags integration ./integration/`
  （工具链缺失时外部语言用例自动 skip）。

## 许可证

[MIT](LICENSE)
