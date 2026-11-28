# hire Taskfile.toml 参考

TOML 是 hire 的**纯静态**格式：只声明命令、参数与依赖，不带任何逻辑。需要
if / 循环 / 拼接时，把那些任务写进 `Taskfile.py` / `Taskfile.cpp` /
`Taskfile.lua`——同目录多文件全部加载，任务级别混用无碍。

> **v2.1.0 破坏性变更**：内联 Lua 的 `run` 字段已移除。v2.0.x 的
> `Taskfile.toml` 如含 `run`，加载期报错；改写指引见文末「从 run 迁移」。

## Schema

```toml
shell = "bash"                          # 可选：本文件默认 shell（沿用）

[tasks.clean]
desc = "remove build artifacts"
cmds = ["rm -rf dist/"]                 # 单条可写 cmd = "..."（别名）

[tasks.build]
desc = "compile the app"
cmd = "g++ -O2 -o app main.cpp"

[tasks.serve]
desc = "serve with defaults"
cmd = "echo port={{port}} verbose={{verbose:+--verbose}}"
[tasks.serve.args.port]
type = "num"
default = 8080
[tasks.serve.args.verbose]
type = "bool"
default = true

[tasks.release]
desc = "tag, build, push"
deps = ["build"]                        # 先跑 build，再跑本体
cmds = [
    "git tag {{tag}}",                      # 规则一：原文替换
    "git push --tags {{force:+--force}}",   # 规则二：bool 条件插入
]
[tasks.release.args.tag]
required = true
desc = "git tag, e.g. v2.1.0"
[tasks.release.args.force]
type = "bool"

[tasks.winonly]
cmd = "echo hi"
when = { os = "windows" }                # 不匹配平台 = 任务不存在
```

## 字段

| 字段 | 类型 | 说明 |
|---|---|---|
| `shell` | string | 顶层可选；本文件默认 shell |
| `tasks.<name>.desc` | string | `hire ls` / `help` 显示 |
| `tasks.<name>.cmd` | string | 单条命令，`cmds` 的别名 |
| `tasks.<name>.cmds` | string[] | 命令序列，按序执行 |
| `tasks.<name>.deps` | string[] | 本文件内的前置任务（见下） |
| `tasks.<name>.args.<arg>.type` | string | `"string"`（缺省）/ `"bool"` / `"num"` |
| `tasks.<name>.args.<arg>.required` | bool | 缺省 false；与 `default` 互斥 |
| `tasks.<name>.args.<arg>.desc` | string | |
| `tasks.<name>.args.<arg>.default` | string / bool / num | 未传时的渲染值；类型须与 `type` 一致；有默认值即视为可选 |
| `tasks.<name>.when.os` | string 或 string[] | 平台过滤 |

`cmd` 与 `cmds` 等价但互斥：并存或两者皆缺 → 加载期报错（文件名 +
任务名）。未知字段报错（拼错的键不会静默生效）；任务名不得以 `__`
开头，参数名 `shell` 保留，保留全局名不能做任务名——规则与 Lua 适配器
一致，声明后的参数由 hire 统一严格校验。

## 插值（仅两条规则）

命令文本里的 `{{name}}` 引用**本任务已声明**的参数：

1. **`{{name}}`（string / num）**：原文替换。值里含空格需自加引号——
   `{{msg}}` 不会替你转义；num 按最短数字形式渲染（`--port 8080` 是
   `8080`，不是 `8080.000000`）。可选参数未传：有 `default` 则用默认值
   （string 原文、num 最短形式），否则替换为空串。
2. **`{{flag:+word}}`（bool）**：真值时插入 `word`，假值 / 未传时插入
   空串（Make 的 `$(VAR:+word)` 血统）；`default = true` 且未传 → 一直
   插入 `word`。显式传参始终覆盖默认值。

边界（全部加载期报错，点名文件 + 任务 + 占位符或参数）：

- `{{typo}}` 未声明 → 报错；`{{ }}` 空名同未声明报错
- 条件形式只对 bool 参数合法，用在 string / num 上 → 报错
- bool 参数没有"原文替换"形态：`{{flag}}` → 报错，提示改用条件形式
- `{{` 未闭合 → 报错
- `default` 类型与 `type` 不符（如 `type = "num"` 配 `default = "8080"`）→
  报错；`required = true` 与 `default` 并存 → 报错（有默认值即可选）

`bash:` 这类 shell 前缀先切分、不参与插值（前缀命令总是 one-shot，
机制见 [lua.md](lua.md)）：`bash:echo {{msg}}` 在 bash 里跑插值后的
命令文本。

## deps（仅本文件）

`deps = ["build"]` 声明**本文件内**的前置任务；语义：

- 依赖按列出顺序先序执行，每个依赖自己的 deps 递归先行（无并行、
  无 DAG）
- 依赖拿不到任何参数（空 args）：可选参数插值为空串，所以**带必填
  参数的任务不能做依赖**——加载期报错
- 任一依赖失败即停，本体不再执行（与合成任务的错误语义一致）
- `when.os` 过滤掉的任务视同不存在：引用它 = 未知依赖，加载期报错
- 循环依赖在加载期 DFS 检出，报错给出环上任务名链

## 从 run 迁移

v2.0.x 的 `run = """...lua..."""` 不再被接受，加载期报错
（`'run' has been removed; for logic use a py/cpp/lua taskfile`）。
改写路径：

- 纯命令拼装 → `cmds` + 插值。原来 run 里的大多数用法（拼 tag、
  按开关加 flag）就是两条插值规则覆盖的样子
- 需要真的逻辑 → 把该任务挪进 `Taskfile.py` / `Taskfile.cpp` /
  `Taskfile.lua`，与 TOML 任务同名共存无碍（外部语言接入契约见
  [contract.md](contract.md)）

## 运行与查找

跟其它 taskfile 一致：`hire <task>`、`hire ls`、`--taskfile` 指定；
后台（bstart）与参数校验机制对 TOML 任务同权；同目录多文件全部加载
（toml → lua → py → js → cpp 探测序），同名任务后者赢 + stderr 警告。
