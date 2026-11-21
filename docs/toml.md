# hire Taskfile.toml 参考

静态任务用 TOML 声明，动态逻辑内联 Lua——同一个文件里两者可以混着来。TOML
任务与 Lua 任务共用同一套合并、参数校验与后台机制；内联 run 的能力等同 Lua
任务（`x.` 全局可用，但**没有** `req`/`bool` 糖）。

## Schema

```toml
# 可选：本文件默认 shell（覆盖平台默认 pwsh/bash）
shell = "pwsh"

[tasks.clean]
cmd = "rm -rf dist/"

[tasks.quick]
cmd = ["echo line1", "echo line2"]   # 数组 = 命令序列

[tasks.release]
desc = "build and release"
run = """                              # 内联 Lua：x./args 可用
x.sh("git tag " .. args.tag)
if args.force then
  x.sh("git push --force --tags")
end
"""
[tasks.release.args.tag]
required = true
desc = "git tag"
[tasks.release.args.force]
type = "bool"

[tasks.build]
cmd = "go build -o app.exe ."
when = { os = "windows" }              # 不匹配则任务不存在
```

## 字段

| 字段 | 类型 | 说明 |
|---|---|---|
| `shell` | string | 顶层可选；本文件默认 shell |
| `tasks.<name>.desc` | string | `hire ls` / `help` 显示 |
| `tasks.<name>.cmd` | string 或 string[] | 命令 / 命令序列，与 `run` 互斥 |
| `tasks.<name>.run` | string | 内联 Lua 源码，与 `cmd` 互斥 |
| `tasks.<name>.args.<arg>.type` | string | `"string"`（缺省）/ `"bool"` |
| `tasks.<name>.args.<arg>.required` | bool | 缺省 false |
| `tasks.<name>.args.<arg>.desc` | string | |
| `tasks.<name>.when.os` | string 或 string[] | 平台过滤 |

规则与 Lua 适配器一致：

- `cmd` 与 `run` 缺一不可且互斥，违反加载期报错（文件名 + 任务名）
- 任务名不得以 `__` 开头；参数名 `shell` 保留；保留全局名不能做任务名
- **未知字段报错**（BurntSushi/toml 的 Undecoded 严格性）：拼错的键（如
  `cmds = "..."`）不会静默生效
- 内联 `run` 在该 TOML 文件专属的 Lua 状态里编译：`args` 以局部变量可见，
  `x` 走全局，`x.run` 只能调到本文件的任务
- 同目录多文件全部加载（toml → lua → py → js → cpp 探测序），同名任务后者赢
  + stderr 警告

## 什么时候用 TOML

任务本身没有逻辑（纯命令序列）时，TOML 比 Lua 少两层括号；一旦需要 if/循环/
拼接，切到 `run` 内联 Lua——或者干脆整个文件用 Lua。参数校验两种格式完全同权：
`args` 声明后由 hire 统一严格校验。

运行与查找跟其它 taskfile 一致：`hire <task>`、`hire ls`、`--taskfile` 指定。
外部语言（Python/Node/C++）的接入契约见 [contract.md](contract.md)。
