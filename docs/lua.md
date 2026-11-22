# hire Taskfile 参考（Lua v2 语法）

hire 的 `Taskfile.lua` 是 Lua 5.1 脚本（gopher-lua 解释）。本文档只列 **hire 暴露给 taskfile 作者的表面**：任务声明、`x` 方法、`args` 形状、shell 实例语义。假设你会写 Lua。

## 1. 结构：任务是全局

v2 没有包装表。任务直接声明为全局变量，hire 加载文件后对 `_G` 做快照差分——**新增或值变化的键**才是任务候选：

```lua
clean = "rm -rf dist/"

hello = function()
  x.echo("hi")
end

release = task {
  desc = "build and release",
  args = { tag = req "version tag" },
  run = function(args)
    x.sh("git tag " .. args.tag)
  end,
}
```

> **从 v1 迁移**：旧的 `local tasks = { ... } return tasks` 语法已被移除，加载时报
> "old v1 syntax ... has been removed"。同时 `ctx:sh(...)` 冒号调用也不再存在——
> v2 里上下文是全局 `x`，一律点调用 `x.sh(...)`；任务函数签名是
> `function(args)`（或无参 `function()`），不再是 `function(ctx, args)`。

## 2. 四种声明形式

| 形式 | 例子 | 说明 |
|---|---|---|
| 字符串 | `clean = "rm -rf dist/"` | 单条 shell 命令 |
| 字符串数组 | `steps = { "echo one", "echo two" }` | 顺序执行的命令序列 |
| 裸函数 | `hello = function() x.echo("hi") end` | 无参数任务 |
| 完整形式 | `release = task { ... }` | desc / args / run 或 cmd / when |

### task{} 字段

| 字段 | 类型 | 说明 |
|---|---|---|
| `desc` | string | `hire ls` / `hire help <task>` 显示 |
| `args` | table | 参数名 → spec（见下）；声明即严格校验，未声明的 flag 报错 |
| `run` | function | 任务函数，与 `cmd` 互斥 |
| `cmd` | string | 命令字符串（可带 `shell:` 前缀），与 `run` 互斥 |
| `when` | table | `{ os = "windows" }` 或 `{ os = {"linux","darwin"} }`；不匹配则任务不存在 |

`run` 与 `cmd` 缺一不可且互斥，违反在加载期报错（含文件名 + 任务名）。

### args spec

```lua
release = task {
  args = {
    tag   = req "git tag",          -- 必填 string（糖）
    force = bool "force push",     -- bool 开关（糖）
    port = num "listen port",      -- 可选 num（糖）
    limit = { desc = "并发数" },     -- 可选 string（全形式）
  },
  run = function(args) end,
}
```

全形式字段：`type`（`"string"` | `"bool"` | `"num"`，缺省 string）、`required`（bool，缺省 false）、
`desc`。**字段类型严格**：`required = "true"`（字符串）或未知字段名在加载期报错，
不会静默取默认值——与 TOML 适配器的严格性一致。必填 num 走全形式
`{ type = "num", required = true }`（糖只做可选）。

### 保留名

- 任务名不得以 `__` 开头（保留给契约动词）
- 参数名 `shell` 保留（`--shell` 选择 shell），声明即报错
- 保留全局名 `x` / `shell` / `task` / `req` / `bool` / `num` 不能做任务名；
  顶层 `shell = "pwsh"` 是默认 shell 声明，不是任务

## 3. `x` 全局（点调用）

任务执行期间上下文以全局 `x` 注入。所有方法**点调用**：

| 方法 | 行为 | 错误时 |
|---|---|---|
| `x.sh(cmd, opts?)` | 经 shell 执行命令（见 §4） | 非零退出抛 Lua 错误 |
| `x.exec(name, args?)` | 直接 exec，不经 shell，无注入面 | 非零退出抛错 |
| `x.run(name, args?)` | 调用**同文件**另一任务（递归上限 100） | 任务不存在抛错 |
| `x.echo(...)` | 打印 stdout（export 模式下改走 stderr） | — |
| `x.ask(prompt, default?)` | 读一行输入；后台任务无 stdin 返回 default | — |
| `x.cwd()` / `x.os()` | 工作目录 / `"windows"` \| `"linux"` \| `"darwin"` | — |
| `x.env(name)` | 读环境变量，返回 string 或 nil | — |

> v1 的冒号调用（`ctx:sh`）在 v2 会直接报错——`x.sh` 不接收 self。

## 4. x.sh 与 shell 实例

### opts 总表

| opts | 行为 |
|---|---|
| 省略 | `instance=1`：任务内**持久实例**，cd/env 状态延续 |
| `instance = N`（N≥1） | 指定持久实例号（首次使用冷启动） |
| `instance = 0` | export 模式（见 §5） |
| `shell = "name"` | 该次命令 one-shot（显式 shell = 明确不要持久） |
| `interactive = true` | one-shot + 接 stdin（与持久实例互斥） |

命令字符串可带前缀指定 one-shot shell：`"bash:./deploy.sh"`。按第一个 `:` 切分，
前缀必须是已知 shell 名（内建 pwsh/bash/sh 或 config.lua 注册名），否则整体按原命令
执行（`C:\path` 不会误判）。

### 持久实例注意事项（真实踩坑，务必读完）

持久实例是一个真的、活着的 shell 进程，stdin 是它的命令通道，stdout 逐行扫描哨兵行
取退出码。由此有几条硬约束：

1. **pwsh 持久模式下命令必须保持单行**。多行脚本（管道续行、here-string 跨行）会被
   逐行送进命令通道而错乱；bash/sh 实例没有这个问题。需要多行逻辑时用
   `x.sh(cmd, { shell = "pwsh" })` 走 one-shot，或写成单行。
2. **持久实例里读 stdin 的命令会挂死实例**。`Read-Host`、`python`（无参）、
   `ssh`（要密码）、交互式 `git push` 等——它们的 stdin 就是 hire 的命令通道，
   会把哨兵行当输入吃掉，任务永远等不到状态。这类命令一律加
   `{ interactive = true }` 走 one-shot，或换无交互形式。
3. **`exit N` 会杀死持久实例**。它在 shell 顶层执行等于退出 shell 本身，表现为
   `x.sh: shell instance exited before reporting status`。要给命令传退出码用
   `exit` 之外的写法（如 pwsh `exit` 只出现在脚本块内、bash 用子 shell
   `(exit 3)`）。
4. **持久透传是行导向的，不是字节精确的**。实例 stdout 逐行透传：CRLF 统一归一为
   LF；EOF 时最后半行补一个换行。二进制输出、精确字节布局（哈希工具、hexdump）
   不要走持久实例——用 `{ shell = ... }` one-shot。
5. **cmdlet 失败同样抛错，但持久模式仅限内建 shell**。哨兵行含 `$?` 判定
   （pwsh：`$(if ($?) { $LASTEXITCODE } else { 1 })`），cmdlet 失败不会静默；
   config.lua 注册的第三方 shell 没有脚本化的 stdin 协议，自动降级 one-shot 并
   每任务警告一次。
6. **pwsh 持久实例是 Windows 专属**。Unix 平台的 pwsh 把 `-Command -` 的 stdin
   缓冲到 EOF 才整块执行，持久实例会在第一条命令上死锁；非 Windows 平台
   `shell = "pwsh"` 的持久请求自动降级 one-shot 并每任务警告一次。Unix 默认
   shell 是 bash，持久语义完整可用。

实例生命周期：任务结束（正常返回 / 抛错 / panic）统一关停全部实例。实例崩溃或
`exit` 后，下一条命令自动冷重启。

## 5. export 模式（instance = 0）

`x.sh(cmd, { instance = 0 })` **不执行**命令，只把命令文本按序收集。任务正常结束时
缓冲按行 join 写到 stdout，由你 `eval` / `iex` 消费，影响当前终端：

```powershell
# pwsh
iex (hire env-setup)
```

```bash
# bash
eval "$(hire env-setup)"
```

规则：

- export 期间 `x.echo` 自动改走 stderr（防污染 eval 载荷）
- 普通的 `x.sh(...)`（不带 instance=0）照常执行，可与 export 混用
- 任务失败 → 缓冲丢弃 + stderr 提示 export 已中止
- stdout 是 TTY（没人接管道）时，stderr 打印 `iex`/`eval` 用法提示；管道/重定向
  时不打（视为正在被消费）
- bstart 后台任务禁用 export（抛错）——后台没有"当前终端"可影响

## 6. `args` 形状

CLI 解析后的参数以**单参数** Lua table 传入任务函数：

```lua
-- CLI: hire release --tag v1.0 --force --port 8080 pos1 pos2
release = task {
  args = { tag = req "", force = bool "", port = num "" },
  run = function(args)
    args.tag    -- "v1.0"   (string)
    args.force  -- true     (bool)
    args.port   -- 8080     (number)
    args._      -- {"pos1", "pos2"}  (位置参数数组；无位置参数时为空表)
  end,
}
```

- `type="string"`：`--x VALUE` 或 `--x=VALUE`；裸 `--x` 报错（需要值）
- `type="bool"`：`--x` → true；`--x=true/false`；`--x value` 报错
- `type="num"`：`--x VALUE` 或 `--x=VALUE`，按 float64 解析（int 自然兼容）；
  非数字报错（want num）
- 未声明的 flag 报错（严格校验，错误消息列出允许的参数）
- `--` 之后所有 token 进 `args._`，即使长得像 flag
- `hire <task> --help` 拦截于 argparse 之前，显示任务详情

## 7. when（平台条件）

```lua
build = task { cmd = "go build -o app.exe .", when = { os = "windows" } }
deploy = task { cmd = "bash:./deploy.sh", when = { os = {"linux", "darwin"} } }
```

不匹配当前平台的任务**不存在**：`ls` 不显示，run 报 not found。仅支持 `os`。

## 8. x.run：同文件调用

`x.run("test")` 调用**同一个文件里**的另一任务（可带 args table）。每个 Lua 文件
是独立 LState，跨文件不互调；TOML 内联 run 同理只在该 TOML 文件的任务集合内。
外部语言 taskfile 想复用逻辑就写函数——契约不做任务间调用。

## 9. Lua 语言子集

gopher-lua（Lua 5.1）默认加载的标准库都能用：`string`、`table`、`math`、`io`、
`os`、`debug`、`coroutine`、`pcall`/`xpcall`。

**不能用**（Lua 5.2+ 或 gopher-lua 未实现）：

- `require`（没有 package.path）
- 整数除法 `//`、整数类型
- `string.pack` / `string.unpack`
- `goto` 以外的 `continue`
- `os.exit`（用 `error` 或让命令失败抛错替代）

调试打印到 stderr：`io.stderr:write("debug: " .. tostring(v) .. "\n")`；
`os.execute` 可用但**不经 x.sh 的错误处理**，自己检查退出码。

## 10. 完整示例

```lua
-- 可选：本文件默认 shell（覆盖平台默认 pwsh/bash）
shell = "pwsh"

clean = "rm -rf dist/"

steps = { "echo step one", "echo step two" }

build = task {
  desc = "build the app (windows only)",
  when = { os = "windows" },
  cmd = "go build -o app.exe .",
}

release = task {
  desc = "test, tag, push",
  args = {
    tag   = req "git tag, e.g. v1.2.3",
    force = bool "force push",
  },
  run = function(args)
    x.run("steps")
    x.sh("git tag " .. args.tag)
    if args.force then
      x.sh("git push --force --tags")
    else
      x.sh("git push --tags")
    end
  end,
}

env_setup = function()
  -- export 模式：收集给 eval/iex，不在 hire 里执行
  x.sh("export FOO=bar", { instance = 0 })
end
```

运行：`hire ls` 列任务；`hire release --tag v1.2.3` 跑任务；
`iex (hire env_setup)` 消费 export。更多语言（Python/Node/C++）见
[contract.md](contract.md)；TOML 静态任务见 [toml.md](toml.md)。
