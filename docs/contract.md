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

**预置语言开箱即用**：python（自动探测 `python3` → `python`）、node、ts（走 node 23+ 原生类型擦除）、
cpp（g++）都有内置默认值，不写 config.lua 就能跑；config.lua 仅在你想覆盖时才需要——
ts 想用 bun/tsx、cpp 想换编译器或 flags 时才来改这里：

```lua
return {
  runtimes = {
    python = "python3",                                       -- 字符串 = 解释型命令
    node   = "node",
    ts     = { cmd = "tsx", ext = ".mts" },                    -- 表 = 自定义扩展名声明
    cpp    = { build = "g++ -std=c++20 -O2 -o {out} {src}" }, -- 表 = 编译型
  },
}
```

表形态两种用法：`{ cmd = "..." , ext = "..." }` 声明一个拥有自定义扩展名的解释型
运行时（`ext` 可为字符串或数组，如 TypeScript 映到 node 24+ 的原生类型擦除）；
`{ build = "..." }` 声明编译型。同名扩展多运行时声明时按字母序第一个生效。

- 扩展名内建映射：`.py`→python、`.js`/`.mjs`→node、`.cpp`/`.cc`→cpp；
  `.lua`/`.toml` 走内嵌适配器，不需要 runtimes
- **编译型**：缓存键 = sha256(源文件内容 + NUL + build 命令串)——改源码或改 flags
  都会让缓存失效；编译产物存 `<cachedir>/hire/cpp/<hash>.exe`，命中直接执行，
  未命中先经默认 shell 跑 build 模板（占位符仅 `{out}`/`{src}` 两个），编译失败
  报错附编译器完整输出
- 自动查找链固定探测 `Taskfile.{toml,lua,py,js,cpp}`；**`.mjs`/`.cc` 及一切自定义
  扩展名（含 `.ts`）只在 `--taskfile` 显式指定时有效**（自动发现保持短且可预测）
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

### C++（Taskfile.cpp + hire.hpp，编译型）

`hire init --lang cpp` 生成两个文件：`hire.hpp`（契约 SDK——`__hire__` 分发、discover JSON 输出、带转义处理的 `HIRE_ARGS` 解析全部收在头文件里）和只含任务本体的 `Taskfile.cpp`。手写单文件契约程序同样合法——头文件只是便利品，不引入任何私有机制。

```cpp
// hire.hpp - the hire contract SDK for C++ taskfiles.
//
// Include it from Taskfile.cpp, define hire_main with your tasks; the
// header provides main() itself:
//
//     #include "hire.hpp"
//
//     void hire_main(hire::registry& reg) {
//         reg.task("hello", "say hello")
//            .arg("name", "string", false, "who to greet")
//            .run([](const hire::args& a) {
//                std::cout << "hello, " << a.get_string("name", "hire") << "\n";
//            });
//     }
//
// hire calls your compiled program two ways:
//   <built> __hire__ discover    -> print {"tasks": [...]} (must stay pure)
//   <built> __hire__ run <name>  -> run one task; parsed flags arrive as JSON
//                                  in the HIRE_ARGS env var. Full contract:
//                                  docs/contract.md.
#pragma once
#include <cstdlib>
#include <functional>
#include <iostream>
#include <map>
#include <string>
#include <utility>
#include <vector>

namespace hire {

// args is the parsed HIRE_ARGS JSON: string/bool flags plus the "_" array.
class args {
public:
    std::string get_string(const std::string& key, const std::string& fallback = "") const {
        std::map<std::string, std::string>::const_iterator it = raw_.find(key);
        return it != raw_.end() ? it->second : fallback;
    }
    bool get_bool(const std::string& key, bool fallback = false) const {
        std::map<std::string, std::string>::const_iterator it = raw_.find(key);
        return it != raw_.end() ? it->second == "true" : fallback;
    }
    const std::vector<std::string>& positional() const { return pos_; }

    static args parse(const std::string& json);

private:
    std::map<std::string, std::string> raw_;
    std::vector<std::string> pos_;
};

// arg_spec mirrors one entry of the task's discover "args" object.
struct arg_spec {
    std::string type;     // "string" | "bool"
    std::string desc;
    bool required;
};

// task_def is one registered task; chain arg()/run() to fill it in.
// (The specs member is arg_specs, not args: a member named args would
// shadow the hire::args class inside this struct.)
struct task_def {
    std::string name;
    std::string desc;
    std::vector<std::pair<std::string, arg_spec> > arg_specs;  // declaration order
    std::function<void(const args&)> body;

    task_def& arg(std::string name, std::string type, bool required, std::string desc = "") {
        arg_specs.push_back(std::make_pair(std::move(name), arg_spec{std::move(type), std::move(desc), required}));
        return *this;
    }
    task_def& run(std::function<void(const args&)> fn) {
        body = std::move(fn);
        return *this;
    }
};

// registry holds tasks in registration order (= discover order).
class registry {
public:
    std::vector<task_def> tasks;

    task_def& task(std::string name, std::string desc = "") {
        task_def t;
        t.name = std::move(name);
        t.desc = std::move(desc);
        tasks.push_back(std::move(t));
        return tasks.back();
    }
};

// json_str renders s as a quoted JSON string with escapes.
inline std::string json_str(const std::string& s) {
    std::string out = "\"";
    for (size_t i = 0; i < s.size(); ++i) {
        switch (s[i]) {
            case '"':  out += "\\\""; break;
            case '\\': out += "\\\\"; break;
            case '\n': out += "\\n"; break;
            case '\t': out += "\\t"; break;
            case '\r': out += "\\r"; break;
            default:   out += s[i];
        }
    }
    return out + "\"";
}

// parse decodes hire's flat HIRE_ARGS object: string values with escapes,
// true/false literals, and the "_" string array. Unknown shapes are
// skipped, not fatal - hire already validated the args against the
// discover schema before invoking the task.
inline args args::parse(const std::string& json) {
    args out;
    const size_t n = json.size();
    size_t i = 0;
    // skip_ws and parse_string advance i through the flat object
    // (lambdas, kept local: one parser, one place).
    auto skip_ws = [&]() {
        while (i < n && (json[i] == ' ' || json[i] == '\t' || json[i] == '\n' || json[i] == '\r')) {
            ++i;
        }
    };
    auto parse_string = [&]() -> std::string {
        std::string v;
        ++i;  // opening quote
        while (i < n && json[i] != '"') {
            if (json[i] == '\\' && i + 1 < n) {
                const char c = json[++i];
                ++i;
                switch (c) {
                    case 'n': v += '\n'; break;
                    case 't': v += '\t'; break;
                    case 'r': v += '\r'; break;
                    default:  v += c;     // covers \" \\ \/ and any other
                }
            } else {
                v += json[i++];
            }
        }
        if (i < n) {
            ++i;  // closing quote
        }
        return v;
    };
    skip_ws();
    if (i >= n || json[i] != '{') {
        return out;
    }
    ++i;
    while (true) {
        skip_ws();
        if (i >= n || json[i] == '}') {
            break;
        }
        if (json[i] == ',') {
            ++i;
            continue;
        }
        if (json[i] != '"') {
            break;  // malformed; keep what we have
        }
        const std::string key = parse_string();
        skip_ws();
        if (i >= n || json[i] != ':') {
            break;
        }
        ++i;
        skip_ws();
        if (i < n && json[i] == '"') {
            const std::string v = parse_string();
            if (key == "_") {
                out.pos_.push_back(v);
            } else {
                out.raw_[key] = v;
            }
        } else if (i < n && json[i] == '[') {
            ++i;  // the "_" positional array
            while (true) {
                skip_ws();
                if (i >= n || json[i] == ']') {
                    ++i;
                    break;
                }
                if (json[i] == ',') {
                    ++i;
                    continue;
                }
                if (json[i] != '"') {
                    ++i;
                    continue;
                }
                out.pos_.push_back(parse_string());
            }
        } else {
            const size_t start = i;
            while (i < n && json[i] != ',' && json[i] != '}') {
                ++i;
            }
            if (key != "_") {
                out.raw_[key] = json.substr(start, i - start);  // true/false
            }
        }
    }
    return out;
}

// dispatch handles the __hire__ contract verbs for the registry.
inline int dispatch(registry& reg, int argc, char** argv) {
    if (argc < 3 || std::string(argv[1]) != "__hire__") {
        std::cerr << "usage: __hire__ discover | run <name>\n";
        return 1;
    }
    const std::string action = argv[2];
    if (action == "discover") {
        std::cout << "{\"tasks\":[";
        for (size_t t = 0; t < reg.tasks.size(); ++t) {
            const task_def& task = reg.tasks[t];
            std::cout << (t ? "," : "")
                      << "{\"name\":" << json_str(task.name)
                      << ",\"desc\":" << json_str(task.desc);
            if (!task.arg_specs.empty()) {
                std::cout << ",\"args\":{";
                for (size_t a = 0; a < task.arg_specs.size(); ++a) {
                    std::cout << (a ? "," : "")
                              << json_str(task.arg_specs[a].first) << ":{"
                              << "\"type\":" << json_str(task.arg_specs[a].second.type)
                              << ",\"required\":" << (task.arg_specs[a].second.required ? "true" : "false")
                              << ",\"desc\":" << json_str(task.arg_specs[a].second.desc)
                              << "}";
                }
                std::cout << "}";
            }
            std::cout << "}";
        }
        std::cout << "]}\n";
        return 0;
    }
    if (action == "run" && argc >= 4) {
        const std::string name = argv[3];
        for (size_t t = 0; t < reg.tasks.size(); ++t) {
            if (reg.tasks[t].name != name) {
                continue;
            }
            if (!reg.tasks[t].body) {
                std::cerr << "hire: task " << name << " has no body\n";
                return 1;
            }
            const char* env = std::getenv("HIRE_ARGS");
            reg.tasks[t].body(args::parse(env != nullptr ? env : "{}"));
            return 0;
        }
        std::cerr << "hire: unknown task " << name << "\n";
        return 1;
    }
    std::cerr << "usage: __hire__ discover | run <name>\n";
    return 1;
}

}  // namespace hire

// hire_main is yours to define: declare tasks into reg. The main()
// below dispatches the __hire__ contract verbs around it - you never
// write main yourself.
void hire_main(hire::registry& reg);

int main(int argc, char** argv) {
    hire::registry reg;
    hire_main(reg);
    return hire::dispatch(reg, argc, argv);
}
```

```cpp
// hire Taskfile.cpp - declare your tasks in hire_main; hire.hpp brings
// everything else (dispatch, discover, HIRE_ARGS parsing, even main()).
// hire compiles this file once with the build template from config.lua
// (cpp = { build = "g++ -std=c++20 -O2 -o {out} {src}" }), caches the
// binary by source hash. Full contract: docs/contract.md.

#include "hire.hpp"

void hire_main(hire::registry& reg) {
    reg.task("hello", "say hello (edit me)")
        .arg("name", "string", false, "who to greet")
        .run([](const hire::args& a) {
            std::cout << "hello, " << a.get_string("name", "hire") << "\n";
        });
}
```
