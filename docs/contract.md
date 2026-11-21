# hire 外部 taskfile 契约（`__hire__`）

hire 与外部语言 taskfile 之间只有两个动词。运行时命令由你在 config.lua 的
`runtimes` 表里自配——**任何**能说这套契约的东西都是合法 taskfile：Python、
Node、编译的 C++、甚至一个 shell 脚本。

```
<runtime命令> <taskfile> __hire__ discover
  → stdout: {"tasks":[{"name":"build","desc":"...",
        "args":{"tag":{"type":"string","required":true,"desc":"..."}}}]}
  → exit 0；非法 JSON / 任务名不合法 / args schema 非法 → 非零退出，stderr 原样透传

<runtime命令> <taskfile> __hire__ run <name>
  → 环境变量 HIRE_ARGS = 已解析参数的 JSON（见下）
  → 退出码即任务结果；stdout/stderr 继承
```

设计取舍：

- **argv 传动作、env 传参数**：`python Taskfile.py __hire__ discover` 手工就能跑，
  好调试；参数走环境变量避开 Windows argv 引号泥潭。
- **argparse 严格校验留在 hire 侧**：对着 discover 报告的 schema 校验通过后才发起
  run，错误消息跨语言一致。
- **保留名**：任务名不得以 `__` 开头（与契约动词撞名，加载期报错）；参数名 `shell`
  保留；同名任务在一次 discover 里不得列出两次。

## discover 必须纯

**`__hire__ discover` 会执行你的代码，但契约要求它纯**：不写文件、不发包、不打印
除结果 JSON 外的任何东西、不做不可逆动作。hire 每次调用都现查（discover 无缓存），
`ls`、`--help`、后台启动都会触发——一次 discover 里的一次 `git push` 会在你敲
`hire ls` 时真的推上去。描述任务，仅此而已。真正的副作用全部放在 `run` 里。
这与你信任 taskfile 本身是同一级的信任模型：跑任务 = 执行用户代码；但 discover
被调用得远比 run 频繁，不纯的 discover 会被日常列表操作意外引爆。

discover JSON 字段：`tasks`（数组），每项 `name`（必填）、`desc`、`args`（对象，
每个参数 `{ type: "string"|"bool", required: bool, desc: string }`，字段可省）。
hire 忽略未知字段，所以契约可以向后生长。

## HIRE_ARGS 形状

hire 完成 argparse 后，把解析结果以 JSON 放进环境变量 `HIRE_ARGS` 再发起 run：

```json
{"tag":"v1","force":true,"_":["positional","args"]}
```

- flags 按声明类型转换（string / bool）
- `_` 是位置参数数组（`--` 之后的 token 也进这里）
- **任务未声明 args 时，HIRE_ARGS = `{}`**——读取时对空对象要健壮

非法 JSON / 非零退出的处理：hire 报错并附文件名与你的 stderr 摘录（512 字节
封顶），不会 flood 终端。

## 运行时配置

config.lua（`~/.config/hire/config.lua`）声明每种语言的命令：

```lua
return {
  runtimes = {
    python = "python3",                                       -- 字符串 = 解释型命令
    node   = "node",
    cpp    = { build = "g++ -std=c++20 -O2 -o {out} {src}" }, -- 表 = 编译型
  },
}
```

- 扩展名内建映射：`.py`→python、`.js`/`.mjs`→node、`.cpp`/`.cc`→cpp；
  `.lua`/`.toml` 走内嵌适配器，不需要 runtimes
- **编译型**：缓存键 = sha256(源文件内容 + NUL + build 命令串)——改源码或改 flags
  都会让缓存失效；编译产物存 `<cachedir>/hire/cpp/<hash>.exe`，命中直接执行，
  未命中先经默认 shell 跑 build 模板（占位符仅 `{out}`/`{src}` 两个），编译失败
  报错附编译器完整输出
- 自动查找链固定探测 `Taskfile.{toml,lua,py,js,cpp}`；**`.mjs`/`.cc` 只在
  `--taskfile` 显式指定时有效**（自动发现保持短且可预测）
- 一个目录里多文件共存时全部加载、同名任务后者赢（stderr 警告）

## 三语言样板全文

以下即 `hire init --lang python|node|cpp` 生成的文件，逐字一致——init 出来
就能 `hire hello`，照着改就是你的 taskfile。


### Python（Taskfile.py）

```python
#!/usr/bin/env python3
"""hire Taskfile.py - a taskfile is any program speaking the __hire__ contract.

hire calls it two ways:
  python Taskfile.py __hire__ discover     -> print {"tasks": [...]} to stdout
  python Taskfile.py __hire__ run <name>   -> run the task
Parsed CLI arguments arrive as JSON in the HIRE_ARGS environment variable:
{"name": "v2", "_": ["positional"]} - flags plus the "_" positional array,
{} when the task declares no args. Full contract: docs/contract.md.

discover must stay pure: describe tasks, no side effects.
"""

import json
import os
import sys

TASKS = [
    {
        "name": "hello",
        "desc": "say hello (edit me)",
        "args": {
            "name": {"type": "string", "required": False, "desc": "who to greet"},
        },
    },
]


def discover():
    json.dump({"tasks": TASKS}, sys.stdout)


def run(name):
    args = json.loads(os.environ.get("HIRE_ARGS", "{}"))
    if name == "hello":
        print(f"hello, {args.get('name') or 'hire'}")
        return
    sys.exit(f"hire: unknown task {name!r}")


if __name__ == "__main__":
    if len(sys.argv) >= 3 and sys.argv[1] == "__hire__":
        if sys.argv[2] == "discover":
            discover()
        elif sys.argv[2] == "run" and len(sys.argv) >= 4:
            run(sys.argv[3])
        else:
            sys.exit("usage: Taskfile.py __hire__ discover | run <name>")
    else:
        sys.exit("this file is a hire taskfile; run its tasks via the hire CLI")
```

### Node（Taskfile.js）

```js
#!/usr/bin/env node
// hire Taskfile.js - a taskfile is any program speaking the __hire__ contract.
//
// hire calls it two ways:
//   node Taskfile.js __hire__ discover     -> print {"tasks": [...]} to stdout
//   node Taskfile.js __hire__ run <name>   -> run the task
// Parsed CLI arguments arrive as JSON in the HIRE_ARGS environment variable:
// {"name": "v2", "_": ["positional"]} - flags plus the "_" positional array,
// {} when the task declares no args. Full contract: docs/contract.md.
//
// discover must stay pure: describe tasks, no side effects.

const tasks = [
  {
    name: "hello",
    desc: "say hello (edit me)",
    args: {
      name: { type: "string", required: false, desc: "who to greet" },
    },
  },
];

const verb = process.argv[2];
const action = process.argv[3];
const name = process.argv[4];

if (verb !== "__hire__") {
  process.stderr.write("this file is a hire taskfile; run its tasks via the hire CLI\n");
  process.exit(1);
}

if (action === "discover") {
  process.stdout.write(JSON.stringify({ tasks }));
  process.exit(0);
}

if (action !== "run" || !name) {
  process.stderr.write("usage: Taskfile.js __hire__ discover | run <name>\n");
  process.exit(1);
}

const args = JSON.parse(process.env.HIRE_ARGS || "{}");
if (name !== "hello") {
  process.stderr.write("hire: unknown task " + name + "\n");
  process.exit(1);
}
process.stdout.write("hello, " + (args.name || "hire") + "\n");
```

### C++（Taskfile.cpp，编译型）

```cpp
// hire Taskfile.cpp - a taskfile is any program speaking the __hire__ contract.
//
// hire compiles this file once with the build template from config.lua
// (cpp = { build = "g++ -std=c++20 -O2 -o {out} {src}" }), caches the
// binary by source hash, then calls it:
//   <built> __hire__ discover     -> print {"tasks": [...]} to stdout
//   <built> __hire__ run <name>   -> run the task
// Parsed CLI arguments arrive as JSON in the HIRE_ARGS environment variable:
// {"name": "v2", "_": ["positional"]} - flags plus the "_" positional array,
// {} when the task declares no args. Full contract: docs/contract.md.
//
// discover must stay pure: describe tasks, no side effects.

#include <cstdlib>
#include <iostream>
#include <string>

namespace {

// extract_string_field pulls "key":"value" from a flat JSON object.
// shortcut: no escape handling - fine for scaffold defaults; use a JSON
// library once your tasks take arbitrary values.
std::string extract_string_field(const std::string& json, const std::string& key) {
    const std::string needle = "\"" + key + "\":\"";
    const size_t start = json.find(needle);
    if (start == std::string::npos) {
        return "";
    }
    const size_t value = start + needle.size();
    const size_t end = json.find('"', value);
    return end == std::string::npos ? "" : json.substr(value, end - value);
}

std::string hire_args() {
    const char* value = std::getenv("HIRE_ARGS");
    return value == nullptr ? "{}" : value;
}

void run_hello() {
    const std::string who = extract_string_field(hire_args(), "name");
    std::cout << "hello, " << (who.empty() ? "hire" : who) << "\n";
}

}  // namespace

int main(int argc, char** argv) {
    if (argc >= 3 && std::string(argv[1]) == "__hire__") {
        const std::string action = argv[2];
        if (action == "discover") {
            std::cout << "{\"tasks\":[{"
                      << "\"name\":\"hello\","
                      << "\"desc\":\"say hello (edit me)\","
                      << "\"args\":{\"name\":{"
                      << "\"type\":\"string\",\"required\":false,"
                      << "\"desc\":\"who to greet\"}}}]}";
            return 0;
        }
        if (action == "run" && argc >= 4) {
            const std::string task = argv[3];
            if (task == "hello") {
                run_hello();
                return 0;
            }
            std::cerr << "hire: unknown task " << task << "\n";
            return 1;
        }
    }
    std::cerr << "usage: Taskfile.cpp __hire__ discover | run <name>\n";
    return 1;
}
```
