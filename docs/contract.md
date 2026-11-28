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
除结果 JSON 外的任何东西、不做不可逆动作。hire 每次调用都现查（discover 无缓存；
唯一例外是 C++ 函数形式——解析+discover 结果缓存为 `<cachedir>/hire/cpp/<hash>.tasks.json`，
见下文 C++ 节），`ls`、`--help`、后台启动都会触发——一次 discover 里的一次 `git push` 会在你敲
`hire ls` 时真的推上去。描述任务，仅此而已。真正的副作用全部放在 `run` 里。
这与你信任 taskfile 本身是同一级的信任模型：跑任务 = 执行用户代码；但 discover
被调用得远比 run 频繁，不纯的 discover 会被日常列表操作意外引爆。

discover JSON 字段：`tasks`（数组），每项 `name`（必填）、`desc`、`args`（对象，
每个参数 `{ type: "string"|"bool"|"num", required: bool, desc: string }`，字段可省）。
hire 忽略未知字段，所以契约可以向后生长。

## HIRE_ARGS 形状

hire 完成 argparse 后，把解析结果以 JSON 放进环境变量 `HIRE_ARGS` 再发起 run：

```json
{"tag":"v1","force":true,"_":["positional","args"]}
```

- flags 按声明类型转换（string / bool / num；num 是 JSON number）
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
    cpp    = { build = "g++ -std=c++20 -O2 -o {out} {src}", parser = "clang++" }, -- 表 = 编译型
  },
}
```

表形态两种用法：`{ cmd = "..." , ext = "..." }` 声明一个拥有自定义扩展名的解释型
运行时（`ext` 可为字符串或数组，如 TypeScript 映到 node 24+ 的原生类型擦除）；
`{ build = "...", parser = "clang++" }` 声明编译型——`parser` 可选，是 C++ 函数形式
taskfile 的 AST 解析器（缺省探测 clang++ → clang；不写 config.lua 则 g++/clang 探测
PATH 即可）。同名扩展多运行时声明时按字母序第一个生效。

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


### Python（hire.py + Taskfile.py，装饰器 SDK）

`hire init --lang python` 生成两个文件：`hire.py`（契约 SDK——`__hire__` 分发、discover JSON 输出、`HIRE_ARGS` 解析全部收在 SDK 模块里）和只含任务本体的 `Taskfile.py`。声明助手：`opt`/`req`（string，可选/必填）、`flag`（bool）、`num`（数字，Args 属性为 float）；`a.name` 属性访问（缺失为 None），`a.get(name, default)` 显式兜底。手写裸单文件契约程序同样合法——SDK 只是便利品，不引入任何私有机制（形态见本节末尾）。

```python
"""hire.py - the hire contract SDK for Python taskfiles.

A taskfile is any program speaking the __hire__ contract; this SDK is a
convenience, not a requirement. Two legal shapes:

  SDK (what hire init --lang python scaffolds): keep this hire.py beside
  a thin Taskfile.py that imports it and only holds tasks:

      from hire import task, opt

      @task("hello", desc="say hello", args={"name": opt("who to greet")})
      def hello(a):
          print(f"hello, {a.name or 'hire'}")

      import hire
      if __name__ == "__main__":
          hire.dispatch()

  Raw single-file: write the two __hire__ verbs by hand in one program
  (a full listing lives in docs/contract.md) - hire.py adds no private
  mechanism, only boilerplate you would otherwise retype.

hire calls your program two ways:
  python Taskfile.py __hire__ discover     -> print {"tasks": [...]} to stdout
  python Taskfile.py __hire__ run <name>   -> run the task
Parsed CLI arguments arrive as JSON in the HIRE_ARGS environment variable:
{"name": "v2", "port": 8080, "_": ["positional"]} - flags plus the "_"
positional array, {} when the task declares no args.

discover must stay pure: describe tasks, no side effects.
"""

import json
import os
import sys

# registry maps task name -> (spec, fn). Insertion-ordered: discover
# lists tasks in the order their @task decorators ran (dicts keep
# insertion order on Python 3.7+).
_registry = {}


def opt(desc=""):
    """An optional string argument. The dict key in args={} is the name."""
    return {"type": "string", "required": False, "desc": desc}


def req(desc=""):
    """A required string argument. The dict key in args={} is the name."""
    return {"type": "string", "required": True, "desc": desc}


def flag(desc=""):
    """An optional bool argument. The dict key in args={} is the name."""
    return {"type": "bool", "required": False, "desc": desc}


def num(desc=""):
    """An optional number argument (an Args attribute is a float). The
    dict key in args={} is the name."""
    return {"type": "num", "required": False, "desc": desc}


def task(name, desc="", args=None):
    """Register the decorated function as a hire task."""

    def register(fn):
        _registry[name] = ({"name": name, "desc": desc, "args": args or {}}, fn)
        return fn

    return register


class Args:
    """The parsed HIRE_ARGS JSON: a.name (None when absent), a.get(name,
    default) for explicit fallbacks, a._ for the positional array."""

    def __init__(self, raw):
        self._raw = raw
        self._ = raw.get("_", [])

    def __getattr__(self, name):
        return self._raw.get(name)

    def get(self, name, default=None):
        return self._raw.get(name, default)


def dispatch():
    """Handle the __hire__ contract verbs for the registered tasks."""
    if len(sys.argv) < 3 or sys.argv[1] != "__hire__":
        sys.exit("this file is a hire taskfile; run its tasks via the hire CLI")
    if sys.argv[2] == "discover":
        json.dump({"tasks": [spec for spec, _ in _registry.values()]}, sys.stdout)
        return
    if sys.argv[2] == "run" and len(sys.argv) >= 4:
        name = sys.argv[3]
        if name not in _registry:
            sys.exit(f"hire: unknown task {name!r}")
        spec, fn = _registry[name]
        raw = json.loads(os.environ.get("HIRE_ARGS", "{}"))
        for arg, decl in spec["args"].items():
            # hire marshals 8080.0 as 8080, which json parses back as an
            # int; declared num args are coerced to floats here.
            if decl.get("type") == "num" and type(raw.get(arg)) is int:
                raw[arg] = float(raw[arg])
        fn(Args(raw))
        return
    sys.exit("usage: Taskfile.py __hire__ discover | run <name>")
```

```python
from hire import task, opt

@task("hello", desc="say hello (edit me)", args={"name": opt("who to greet")})
def hello(a):
    print(f"hello, {a.name or 'hire'}")

import hire
if __name__ == "__main__":
    hire.dispatch()
```

裸单文件契约形态（同样合法，不随 init 生成）：

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

### Node（hire.js + Taskfile.js，无装饰器 SDK）

`hire init --lang node` 生成两个文件：`hire.js`（契约 SDK——__hire__ 分发、discover JSON 输出、HIRE_ARGS 解析收在 SDK 模块里）和只含任务本体的瘦 `Taskfile.js`。声明助手：`opt`/`req`（string，可选/必填）、`flag`（bool）、`num`（数字，dispatch 时转 Number）；`a.name` 属性访问，`a._` 为位置参数数组。手写裸单文件契约程序同样合法——SDK 只是便利品，不引入任何私有机制。

`hire.js`：

```js
// hire.js - the hire contract SDK for Node taskfiles.
//
// A taskfile is any program speaking the __hire__ contract; this SDK is a
// convenience, not a requirement. Two legal shapes:
//
//   SDK (what hire init --lang node scaffolds): keep this hire.js beside
//   a thin Taskfile.js that only registers tasks:
//
//       const { task, opt, dispatch } = require("./hire.js");
//       task("hello", { desc: "say hello", args: { name: opt("who") } },
//            (a) => { console.log("hello, " + (a.name || "hire")); });
//       dispatch();
//
//   Raw single-file: write the two __hire__ verbs by hand in one program
//   (a full listing lives in docs/contract.md) - hire.js adds no private
//   mechanism, only boilerplate you would otherwise retype.
//
// hire calls your program two ways:
//   node Taskfile.js __hire__ discover     -> print {"tasks": [...]} to stdout
//   node Taskfile.js __hire__ run <name>   -> run the task
// Parsed CLI arguments arrive as JSON in the HIRE_ARGS environment variable:
// {"name": "v2", "port": 8080, "_": ["positional"]} - flags plus the "_"
// positional array, {} when the task declares no args.
//
// discover must stay pure: describe tasks, no side effects.
"use strict";

const _registry = new Map();

function task(name, opts, fn) {
  _registry.set(name, {
    spec: { name: name, desc: (opts && opts.desc) || "", args: (opts && opts.args) || {} },
    fn: fn,
  });
}

function opt(desc) { return { type: "string", required: false, desc: desc || "" }; }
function req(desc) { return { type: "string", required: true, desc: desc || "" }; }
function flag(desc) { return { type: "bool", required: false, desc: desc || "" }; }
function num(desc) { return { type: "num", required: false, desc: desc || "" }; }

function dispatch() {
  const verb = process.argv[2];
  const action = process.argv[3];
  const name = process.argv[4];
  if (verb !== "__hire__") {
    process.stderr.write("this file is a hire taskfile; run its tasks via the hire CLI\n");
    process.exit(1);
  }
  if (action === "discover") {
    const tasks = [];
    for (const entry of _registry.values()) tasks.push(entry.spec);
    process.stdout.write(JSON.stringify({ tasks: tasks }));
    return;
  }
  if (action !== "run" || !name) {
    process.stderr.write("usage: Taskfile.js __hire__ discover | run <name>\n");
    process.exit(1);
  }
  const entry = _registry.get(name);
  if (!entry) {
    process.stderr.write("hire: unknown task " + name + "\n");
    process.exit(1);
  }
  let raw;
  try {
    raw = JSON.parse(process.env.HIRE_ARGS || "{}");
  } catch (e) {
    process.stderr.write("hire: invalid HIRE_ARGS: " + e.message + "\n");
    process.exit(1);
  }
  for (const key of Object.keys(entry.spec.args)) {
    if (entry.spec.args[key].type === "num" && raw[key] !== undefined) {
      raw[key] = Number(raw[key]);
    }
  }
  const a = Object.assign({}, raw);
  a._ = raw._ || [];
  entry.fn(a);
}

module.exports = { task, opt, req, flag, num, dispatch };
```

`Taskfile.js`：

```js
const { task, opt, dispatch } = require("./hire.js");

task("hello", { desc: "say hello (edit me)", args: { name: opt("who to greet") } },
  (a) => {
    process.stdout.write("hello, " + (a.name || "hire") + "\n");
  });

dispatch();
```

### C++（Taskfile.cpp，函数形式 + 用户自配 clang++）

`hire init --lang cpp` 生成 `Taskfile.cpp`——纯 C++ 函数：每个顶层非 static
自由函数就是一个任务；紧邻函数上方的 `//` 注释首行（约定 `// desc: ...`）是任务描述
（注释归属按 clang `-fparse-all-comments` 的实际行为：注释与函数之间空一行仍会挂到该函数上，
所以"紧邻"并非必须零行距）；`static` 或匿名命名空间里的函数是助手。
同一函数的转发声明与定义会被 clang 以 `previousDecl` 关联，只算一个任务；`extern` 声明
（本翻译单元无函数体）不会被选为任务。参数映射：`std::string`/`string`→string、
`bool`→flag、`int`/`long`/`long long`/`double`/`float`→num；**有默认值 = 可选**
（默认值写进参数 desc 提示位，如 `default: 8080`），无默认 = 必填；不支持的参数类型
加载期报错（点名函数+参数）。

加载机制（clang 与 g++ 同为用户自配，绝不内置）：

1. hire 调用 PATH 上的 `clang++ -Xclang -ast-dump=json -fsyntax-only
   -fparse-all-comments Taskfile.cpp`，纯 Go 解析其 JSON AST——无 Cgo、无外部依赖。
   语法错误透传 clang 自己的诊断（按你的真实 文件:行 报告）。
2. 缓存目录（不污染用户目录）生成包装 TU：`#include "hire.hpp"`（hire 内嵌的同一份
   SDK 头）+ `#include "<Taskfile.cpp 绝对路径>"` + 逐函数注册代码（HIRE_ARGS JSON →
   类型化转换后**直接以实参调用**你的函数）+ main() 由 hire.hpp 提供。include 语义保证
   编译器诊断按你的真实行号报告。
3. 编译仍走你的 build 模板（对象 = 包装 TU，`{src}` 传包装 TU 路径）；函数形式的缓存键 =
   sha256(源文件内容 + NUL + build 命令 + NUL + 内嵌 SDK 头字节 + NUL + 代码生成版本号)，
   解析+discover 结果缓存为 `<cachedir>/hire/cpp/<hash>.tasks.json`——命中时零解析、零执行、
   零重编（遗留契约形态仍用 sha256(源文件内容 + NUL + build 命令)，不含代码生成盐）。
4. config.lua 可覆盖解析器：`cpp = { build = "...", parser = "clang++" }`
   （缺省探测 clang++ → clang）。无 clang 时函数形式 discover 报错指引：装
   clang++，或改用下面的 hpp 链式形态。

```cpp
// hire Taskfile.cpp - a taskfile is plain C++ functions: every top-level
// non-static function is a task. The // comment line right above one is
// its description (a "desc:" prefix is the convention); a default value
// makes the argument optional, and std::string/bool/int/double params
// become string/flag/num arguments.
//
// hire parses this file with your clang++ (probed on PATH; override with
// parser = "clang++" in config.lua) and compiles a small generated
// wrapper in its cache - this file stays plain C++. static functions and
// anonymous namespaces hold helpers, not tasks. Full contract:
// docs/contract.md.

#include <iostream>
#include <string>

// desc: say hello (edit me)
void hello(std::string name = "hire") {
    std::cout << "hello, " << name << "\n";
}

// desc: compile the app (opt = optimization level, scale = job multiplier)
void build(std::string target = "app", int opt = 2, double scale = 1.5) {
    std::cout << "g++ -O" << opt << " -j" << opt * scale
              << " -o " << target << " main.cpp\n";
}
```

**hpp 链式形态（合法遗留）**：v2.0.x 的写法继续有效——include hire.hpp、在
hire_main 里注册。文件定义 `hire_main` 或含 `__hire__` 字样时走该 raw 契约路径（函数形式文件可 include hire.hpp 用 `hire::sh`，但不定义 hire_main，故仍走函数形式）
（discover 每次现查，不走函数形式缓存）；`hire init` 不再生成它，全文如下
（手写契约程序同样合法——头文件只是便利品，不引入任何私有机制）：

```cpp
// hire.hpp - the hire contract SDK for C++ taskfiles (legacy form).
//
// Include it from Taskfile.cpp, define hire_main with your tasks; the
// header provides main() itself. The function form (plain top-level
// functions, parsed by your clang++) is the primary shape now — this
// header form stays fully legal:
//
//     #include "hire.hpp"
//
//     void hire_main(hire::registry& reg) {
//         reg.task("hello", "say hello")
//            .opt("name", "who to greet")
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
#include <memory>
#include <stdexcept>
#include <cstdio>
#include <ctime>
#ifdef _WIN32
#ifndef WIN32_LEAN_AND_MEAN
#define WIN32_LEAN_AND_MEAN
#endif
#include <windows.h>
#else
#include <unistd.h>
#include <sys/wait.h>
#include <signal.h>
#include <cerrno>
#include <time.h>
#endif

namespace hire {

// args is the parsed HIRE_ARGS JSON: string/bool/num flags plus the "_" array.
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
    double get_num(const std::string& key, double fallback = 0.0) const {
        std::map<std::string, std::string>::const_iterator it = raw_.find(key);
        return it != raw_.end() ? std::atof(it->second.c_str()) : fallback;
    }
    const std::vector<std::string>& positional() const { return pos_; }

    static args parse(const std::string& json);

private:
    std::map<std::string, std::string> raw_;
    std::vector<std::string> pos_;
};

// arg_spec mirrors one entry of the task's discover "args" object.
struct arg_spec {
    std::string type;     // "string" | "bool" | "num"
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
    // Sugar mirroring the Lua adapter: req = required string, opt =
    // optional string, flag = optional bool, num = optional number;
    // .arg stays for the rest (e.g. a required num).
    task_def& req(std::string name, std::string desc = "") { return arg(std::move(name), "string", true, std::move(desc)); }
    task_def& opt(std::string name, std::string desc = "") { return arg(std::move(name), "string", false, std::move(desc)); }
    task_def& flag(std::string name, std::string desc = "") { return arg(std::move(name), "bool", false, std::move(desc)); }
    task_def& num(std::string name, std::string desc = "") { return arg(std::move(name), "num", false, std::move(desc)); }
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

// ---- persistent shell instances (ported from the Lua x.sh protocol) ----
//
// hire::sh runs a command in a shell. By default the command shares one
// persistent shell process per (inst, shell), so "cd" and "export" survive
// across calls. inst = 0 collects commands for export mode; a builtin
// "shell:" prefix forces a one-shot. A non-zero command exit throws, and
// dispatch turns that into a clean non-zero process exit code.
namespace detail {

inline bool is_builtin(const std::string& name) {
    return name == "pwsh" || name == "bash" || name == "sh";
}

inline std::string default_shell_name() {
    const char* e = std::getenv("HIRE_SHELL");
    if (e != nullptr && *e != '\0') return std::string(e);
#ifdef _WIN32
    return "pwsh";
#else
    return "bash";
#endif
}

// split_shell_prefix recognizes only builtin shell names, so a Windows
// path like C:\dir stays a whole command (review focus: no false prefix).
inline bool split_shell_prefix(const std::string& cmd, std::string& name, std::string& rest) {
    size_t i = cmd.find(':');
    if (i == std::string::npos || i == 0) return false;
    name = cmd.substr(0, i);
    if (!is_builtin(name)) return false;
    rest = cmd.substr(i + 1);
    return true;
}

inline std::vector<std::string> oneshot_argv(const std::string& name, const std::string& cmd) {
    std::vector<std::string> a;
    a.push_back(name);
    if (name == "pwsh") { a.push_back("-NoProfile"); a.push_back("-Command"); }
    else if (name == "bash") { a.push_back("-euo"); a.push_back("pipefail"); a.push_back("-c"); }
    else if (name == "sh") { a.push_back("-e"); a.push_back("-c"); }
    else { a.push_back("-c"); }
    a.push_back(cmd);
    return a;
}

inline std::vector<std::string> persistent_argv(const std::string& name) {
    std::vector<std::string> a;
    a.push_back(name);
    if (name == "pwsh") { a.push_back("-NoProfile"); a.push_back("-NonInteractive"); a.push_back("-Command"); a.push_back("-"); }
    else if (name == "bash") { a.push_back("--norc"); a.push_back("--noprofile"); a.push_back("-s"); }
    else if (name == "sh") { a.push_back("-s"); }
    return a;
}

inline std::string new_nonce() {
    static unsigned long counter = 0;
    char buf[48];
    std::snprintf(buf, sizeof(buf), "%lx%lx", (unsigned long)std::time(nullptr), ++counter);
    return std::string(buf);
}

inline bool starts_with(const std::string& s, const std::string& p) {
    return s.size() >= p.size() && s.compare(0, p.size(), p) == 0;
}

inline std::string rtrim_crlf(const std::string& s) {
    size_t e = s.size();
    while (e > 0 && (s[e - 1] == '\r' || s[e - 1] == '\n')) --e;
    return s.substr(0, e);
}

struct inst_key {
    int inst;
    std::string shell;
    bool operator<(const inst_key& o) const {
        if (inst != o.inst) return inst < o.inst;
        return shell < o.shell;
    }
};

// A persistent shell child: a command channel (stdin) and a line-oriented
// output channel (stdout), plus the process handle for teardown.
struct shell_instance {
    std::string name;
#ifdef _WIN32
    void* in_w = nullptr;
    void* out_r = nullptr;
    void* proc = nullptr;
#else
    int in_fd = -1;
    int out_fd = -1;
    long pid = -1;
#endif
    bool closed = false;
    std::string rdbuf;
    size_t rdpos = 0;

    ~shell_instance() { teardown(); }

    long read_some(char* buf, long cap) {
#ifdef _WIN32
        DWORD n = 0;
        if (!ReadFile((HANDLE)out_r, buf, (DWORD)cap, &n, nullptr)) return 0;
        return (long)n;
#else
        for (;;) {
            ssize_t n = ::read(out_fd, buf, (size_t)cap);
            if (n < 0 && errno == EINTR) continue;
            return n > 0 ? (long)n : 0;
        }
#endif
    }

    bool read_line(std::string& out) {
        out.clear();
        for (;;) {
            if (rdpos < rdbuf.size()) {
                char c = rdbuf[rdpos++];
                if (c == '\n') return true;
                out += c;
                continue;
            }
            char tmp[1024];
            long n = read_some(tmp, (long)sizeof(tmp));
            if (n <= 0) return false;
            rdbuf.assign(tmp, (size_t)n);
            rdpos = 0;
        }
    }

    bool write_all(const std::string& data) {
        const char* p = data.c_str();
        size_t left = data.size();
        while (left > 0) {
#ifdef _WIN32
            DWORD wrote = 0;
            if (!WriteFile((HANDLE)in_w, p, (DWORD)left, &wrote, nullptr) || wrote == 0) return false;
            p += wrote;
            left -= (size_t)wrote;
#else
            ssize_t wrote = ::write(in_fd, p, left);
            if (wrote < 0) { if (errno == EINTR) continue; return false; }
            if (wrote == 0) return false;
            p += wrote;
            left -= (size_t)wrote;
#endif
        }
        return true;
    }

    // send writes cmd + a sentinel echo, then scans stdout line by line.
    // Non-sentinel lines pass through to stdout; the sentinel itself is
    // consumed and never printed (review focus: no __HIRE_RC leak).
    int send(const std::string& cmd) {
        const std::string nonce = new_nonce();
        const std::string prefix = std::string("__HIRE_RC:") + nonce + ":";
        std::string echo;
        if (name == "pwsh") {
            // $? catches cmdlet failures; $LASTEXITCODE is seeded at start.
            echo = "Write-Output \"__HIRE_RC:" + nonce + ":$(if ($?) { $LASTEXITCODE } else { 1 })\"";
        } else {
            echo = "printf '__HIRE_RC:%s:%s\\n' " + nonce + " \"$?\"";
        }
        if (!write_all(cmd + "\n" + echo + "\n")) {
            throw std::runtime_error("hire::sh: shell instance died while running: " + cmd);
        }
        for (;;) {
            std::string line;
            const bool full = read_line(line);
            const std::string trimmed = rtrim_crlf(line);
            if (starts_with(trimmed, prefix)) {
                const std::string rest = trimmed.substr(prefix.size());
                if (!rest.empty()) {
                    char* end = nullptr;
                    const long code = std::strtol(rest.c_str(), &end, 10);
                    if (end != rest.c_str() && *end == '\0') return (int)code;
                }
                if (!full) break;   // malformed sentinel: consume, do not leak
                continue;
            }
            if (!trimmed.empty() || full) { std::cout << trimmed << "\n"; std::cout.flush(); }
            if (!full) break;
        }
        throw std::runtime_error("hire::sh: shell instance exited before reporting status: " + cmd);
    }

    void teardown() {
        if (closed) return;
        closed = true;
#ifdef _WIN32
        if (in_w) { CloseHandle((HANDLE)in_w); in_w = nullptr; }
        if (proc) {
            if (WaitForSingleObject((HANDLE)proc, 500) == WAIT_TIMEOUT) {
                TerminateProcess((HANDLE)proc, 1);
                WaitForSingleObject((HANDLE)proc, INFINITE);
            }
            CloseHandle((HANDLE)proc);
            proc = nullptr;
        }
        if (out_r) { CloseHandle((HANDLE)out_r); out_r = nullptr; }
#else
        if (in_fd >= 0) { ::close(in_fd); in_fd = -1; }
        if (pid > 0) {
            bool reaped = false;
            for (int k = 0; k < 50 && !reaped; ++k) {
                int st = 0;
                pid_t r = ::waitpid((pid_t)pid, &st, WNOHANG);
                if (r == (pid_t)pid || (r < 0 && errno == ECHILD)) { reaped = true; break; }
                struct timespec ts;
                ts.tv_sec = 0;
                ts.tv_nsec = 10 * 1000 * 1000;
                ::nanosleep(&ts, nullptr);
            }
            if (!reaped) { ::kill((pid_t)pid, SIGKILL); ::waitpid((pid_t)pid, nullptr, 0); }
            pid = -1;
        }
        if (out_fd >= 0) { ::close(out_fd); out_fd = -1; }
#endif
    }
};

#ifdef _WIN32
// resolve_shell_exe mirrors shell_windows.go: pwsh/powershell and sh come
// from PATH, but "bash" must be Git Bash, not the WSL launcher at
// C:\Windows\System32\bash.exe that shadows it and has no distro to exec.
inline std::string resolve_shell_exe(const std::string& name) {
    char buf[MAX_PATH];
    DWORD n;
    if (name == "pwsh") {
        n = SearchPathA(nullptr, "pwsh.exe", nullptr, MAX_PATH, buf, nullptr);
        if (n == 0) n = SearchPathA(nullptr, "powershell.exe", nullptr, MAX_PATH, buf, nullptr);
        if (n > 0 && n < MAX_PATH) return std::string(buf, n);
        return name;
    }
    if (name == "sh") {
        n = SearchPathA(nullptr, "sh.exe", nullptr, MAX_PATH, buf, nullptr);
        if (n > 0 && n < MAX_PATH) return std::string(buf, n);
        return name;
    }
    if (name == "bash") {
        n = SearchPathA(nullptr, "git.exe", nullptr, MAX_PATH, buf, nullptr);
        if (n > 0 && n < MAX_PATH) {
            std::string git(buf, n);
            size_t p = git.find_last_of("\\/");
            if (p != std::string::npos && p > 0) {
                size_t p2 = git.find_last_of("\\/", p - 1);
                if (p2 != std::string::npos) {
                    std::string cand = git.substr(0, p2) + "\\bin\\bash.exe";
                    if (GetFileAttributesA(cand.c_str()) != INVALID_FILE_ATTRIBUTES) return cand;
                }
            }
        }
        n = SearchPathA(nullptr, "bash.exe", nullptr, MAX_PATH, buf, nullptr);
        if (n > 0 && n < MAX_PATH) return std::string(buf, n);
        return name;
    }
    return name;  // third-party: let CreateProcess search PATH
}

// build_cmdline quotes argv for CreateProcessA (CommandLineToArgvW rules).
inline std::string win_quote(const std::string& s) {
    std::string r = "\"";
    size_t i = 0;
    while (i < s.size()) {
        size_t bs = 0;
        while (i < s.size() && s[i] == '\\') { ++bs; ++i; }
        if (i == s.size()) { r.append(bs * 2, '\\'); break; }
        if (s[i] == '"') { r.append(bs * 2 + 1, '\\'); r += '"'; ++i; }
        else { r.append(bs, '\\'); r += s[i]; ++i; }
    }
    r += "\"";
    return r;
}
inline std::string build_cmdline(const std::vector<std::string>& argv) {
    std::string out;
    for (size_t i = 0; i < argv.size(); ++i) {
        if (i) out += " ";
        out += win_quote(argv[i]);
    }
    return out;
}

// spawn_inherit runs a one-shot command with the parent's stdio.
inline int spawn_inherit(const std::vector<std::string>& argv) {
    std::vector<std::string> a = argv;
    a[0] = resolve_shell_exe(argv[0]);
    std::string cmdline = build_cmdline(a);
    std::vector<char> buf(cmdline.begin(), cmdline.end());
    buf.push_back('\0');
    STARTUPINFOA si;
    ZeroMemory(&si, sizeof(si));
    si.cb = sizeof(si);
    si.dwFlags = STARTF_USESTDHANDLES;
    HANDLE self = GetCurrentProcess();
    HANDLE in = nullptr, out = nullptr, err = nullptr;
    HANDLE s_in = GetStdHandle(STD_INPUT_HANDLE);
    HANDLE s_out = GetStdHandle(STD_OUTPUT_HANDLE);
    HANDLE s_err = GetStdHandle(STD_ERROR_HANDLE);
    if (s_in != nullptr && s_in != INVALID_HANDLE_VALUE)
        DuplicateHandle(self, s_in, self, &in, 0, TRUE, DUPLICATE_SAME_ACCESS);
    if (s_out != nullptr && s_out != INVALID_HANDLE_VALUE)
        DuplicateHandle(self, s_out, self, &out, 0, TRUE, DUPLICATE_SAME_ACCESS);
    if (s_err != nullptr && s_err != INVALID_HANDLE_VALUE)
        DuplicateHandle(self, s_err, self, &err, 0, TRUE, DUPLICATE_SAME_ACCESS);
    si.hStdInput = in ? in : s_in;
    si.hStdOutput = out ? out : s_out;
    si.hStdError = err ? err : s_err;
    PROCESS_INFORMATION pi;
    ZeroMemory(&pi, sizeof(pi));
    BOOL ok = CreateProcessA(nullptr, buf.data(), nullptr, nullptr, TRUE, 0, nullptr, nullptr, &si, &pi);
    if (in) CloseHandle(in);
    if (out) CloseHandle(out);
    if (err) CloseHandle(err);
    if (!ok) throw std::runtime_error("hire::sh: cannot start shell (CreateProcess failed)");
    CloseHandle(pi.hThread);
    WaitForSingleObject(pi.hProcess, INFINITE);
    DWORD code = 0;
    GetExitCodeProcess(pi.hProcess, &code);
    CloseHandle(pi.hProcess);
    return (int)code;
}

inline std::unique_ptr<shell_instance> start_instance(const std::string& name) {
    std::vector<std::string> argv = persistent_argv(name);
    std::vector<std::string> a = argv;
    a[0] = resolve_shell_exe(argv[0]);
    std::string cmdline = build_cmdline(a);
    std::vector<char> buf(cmdline.begin(), cmdline.end());
    buf.push_back('\0');
    SECURITY_ATTRIBUTES sa;
    sa.nLength = sizeof(sa);
    sa.lpSecurityDescriptor = nullptr;
    sa.bInheritHandle = TRUE;
    HANDLE in_r = nullptr, in_w = nullptr, out_r = nullptr, out_w = nullptr;
    if (!CreatePipe(&in_r, &in_w, &sa, 0)) throw std::runtime_error("hire::sh: CreatePipe failed");
    if (!CreatePipe(&out_r, &out_w, &sa, 0)) {
        CloseHandle(in_r); CloseHandle(in_w);
        throw std::runtime_error("hire::sh: CreatePipe failed");
    }
    SetHandleInformation(in_w, HANDLE_FLAG_INHERIT, 0);
    SetHandleInformation(out_r, HANDLE_FLAG_INHERIT, 0);
    STARTUPINFOA si;
    ZeroMemory(&si, sizeof(si));
    si.cb = sizeof(si);
    si.dwFlags = STARTF_USESTDHANDLES;
    si.hStdInput = in_r;
    si.hStdOutput = out_w;
    HANDLE err_src = GetStdHandle(STD_ERROR_HANDLE);
    HANDLE err = err_src;
    bool err_fresh = false;
    if (err_src != nullptr && err_src != INVALID_HANDLE_VALUE &&
        DuplicateHandle(GetCurrentProcess(), err_src, GetCurrentProcess(), &err, 0, TRUE, DUPLICATE_SAME_ACCESS)) {
        err_fresh = true;
    }
    si.hStdError = err;
    PROCESS_INFORMATION pi;
    ZeroMemory(&pi, sizeof(pi));
    BOOL ok = CreateProcessA(nullptr, buf.data(), nullptr, nullptr, TRUE, 0, nullptr, nullptr, &si, &pi);
    CloseHandle(in_r);
    CloseHandle(out_w);
    if (err_fresh && err) CloseHandle(err);
    if (!ok) {
        CloseHandle(in_w); CloseHandle(out_r);
        throw std::runtime_error("hire::sh: cannot start persistent shell (CreateProcess failed)");
    }
    CloseHandle(pi.hThread);
    std::unique_ptr<shell_instance> inst(new shell_instance());
    inst->name = name;
    inst->in_w = (void*)in_w;
    inst->out_r = (void*)out_r;
    inst->proc = (void*)pi.hProcess;
    if (name == "pwsh") inst->write_all("$LASTEXITCODE = 0\n");  // seed (ctx.go:309-313)
    return inst;
}
#else
inline int spawn_inherit(const std::vector<std::string>& argv) {
    std::vector<char*> cargv;
    for (size_t i = 0; i < argv.size(); ++i) cargv.push_back(const_cast<char*>(argv[i].c_str()));
    cargv.push_back(nullptr);
    pid_t pid = ::fork();
    if (pid < 0) throw std::runtime_error("hire::sh: fork failed");
    if (pid == 0) {
        ::execvp(cargv[0], cargv.data());
        ::_exit(127);
    }
    int status = 0;
    for (;;) {
        pid_t r = ::waitpid(pid, &status, 0);
        if (r == pid || (r < 0 && errno != EINTR)) break;
    }
    return WIFEXITED(status) ? WEXITSTATUS(status) : 1;
}

inline std::unique_ptr<shell_instance> start_instance(const std::string& name) {
    std::vector<std::string> argv = persistent_argv(name);
    std::vector<char*> cargv;
    for (size_t i = 0; i < argv.size(); ++i) cargv.push_back(const_cast<char*>(argv[i].c_str()));
    cargv.push_back(nullptr);
    int in_pipe[2], out_pipe[2];
    if (::pipe(in_pipe) != 0) throw std::runtime_error("hire::sh: pipe failed");
    if (::pipe(out_pipe) != 0) {
        ::close(in_pipe[0]); ::close(in_pipe[1]);
        throw std::runtime_error("hire::sh: pipe failed");
    }
    pid_t pid = ::fork();
    if (pid < 0) {
        ::close(in_pipe[0]); ::close(in_pipe[1]);
        ::close(out_pipe[0]); ::close(out_pipe[1]);
        throw std::runtime_error("hire::sh: fork failed");
    }
    if (pid == 0) {
        ::dup2(in_pipe[0], STDIN_FILENO);
        ::dup2(out_pipe[1], STDOUT_FILENO);
        ::close(in_pipe[0]); ::close(in_pipe[1]);
        ::close(out_pipe[0]); ::close(out_pipe[1]);
        ::execvp(cargv[0], cargv.data());
        ::_exit(127);
    }
    ::close(in_pipe[0]); ::close(out_pipe[1]);
    std::unique_ptr<shell_instance> inst(new shell_instance());
    inst->name = name;
    inst->in_fd = in_pipe[1];
    inst->out_fd = out_pipe[0];
    inst->pid = (long)pid;
    if (name == "pwsh") inst->write_all("$LASTEXITCODE = 0\n");  // seed (ctx.go:309-313)
    return inst;
}
#endif

inline std::map<inst_key, std::unique_ptr<shell_instance> >& instances() {
    static std::map<inst_key, std::unique_ptr<shell_instance> > m;
    return m;
}

inline std::vector<std::string>& export_buf() { static std::vector<std::string> b; return b; }
inline bool& warned() { static bool w = false; return w; }
inline bool& exporting() { static bool e = false; return e; }
inline std::string& export_shell() { static std::string s; return s; }

inline bool stdout_is_tty() {
#ifdef _WIN32
    DWORD mode = 0;
    return GetConsoleMode(GetStdHandle(STD_OUTPUT_HANDLE), &mode) != 0;
#else
    return ::isatty(STDOUT_FILENO) != 0;
#endif
}

// shell_degrades: third-party shells and pwsh-on-Unix cannot host a
// persistent stdin instance (ctx.go:239-260).
inline bool shell_degrades(const std::string& name) {
    if (!is_builtin(name)) return true;
#ifndef _WIN32
    if (name == "pwsh") return true;
#endif
    return false;
}

inline void degrade_warn(const std::string& name) {
    if (warned()) return;
    warned() = true;
    if (!is_builtin(name))
        std::cerr << "hire: warning: shell \"" << name << "\" does not support persistent instances; running one-shot\n";
    else
        std::cerr << "hire: warning: pwsh persistent instances are Windows-only; running one-shot\n";
}

inline void spawn_oneshot(const std::string& name, const std::string& cmd) {
    const int rc = spawn_inherit(oneshot_argv(name, cmd));
    if (rc != 0) throw std::runtime_error("hire::sh: command failed (exit " + std::to_string(rc) + "): " + cmd);
}

inline void collect_export(const std::string& cmd, const std::string& name) {
    if (!is_builtin(name))
        throw std::runtime_error("hire::sh: instance=0 (export mode) needs a builtin shell (pwsh/bash/sh), got " + name);
    if (!exporting()) { exporting() = true; export_shell() = name; }
    export_buf().push_back(cmd);
}

// finish_export flushes the instance=0 buffer at task end: joined to
// stdout on success, discarded with a stderr note on failure; a TTY hint
// tells the user how to eval/iex it (ctx.go:405-426).
inline void finish_export(const std::string& name, bool ok) {
    std::vector<std::string>& buf = export_buf();
    if (buf.empty()) return;
    if (!ok) {
        buf.clear();
        std::cerr << "hire: task failed; export aborted, collected commands discarded\n";
        return;
    }
    std::string joined;
    for (size_t i = 0; i < buf.size(); ++i) {
        if (i) joined += "\n";
        joined += buf[i];
    }
    std::cout << joined << "\n";
    buf.clear();
    if (stdout_is_tty()) {
        if (export_shell() == "pwsh")
            std::cerr << "\u63d0\u793a: \u6b64\u4efb\u52a1\u5305\u542b instance=0 \u547d\u4ee4\u3002\u8bf7\u4f7f\u7528: iex (hire " << name << ")\n";
        else
            std::cerr << "\u63d0\u793a: \u6b64\u4efb\u52a1\u5305\u542b instance=0 \u547d\u4ee4\u3002\u8bf7\u4f7f\u7528: eval \"$(hire " << name << ")\"\n";
    }
}

inline void run_persistent(const std::string& cmd, const std::string& name, int inst) {
    inst_key key;
    key.inst = inst;
    key.shell = name;
    std::map<inst_key, std::unique_ptr<shell_instance> >& m = instances();
    std::map<inst_key, std::unique_ptr<shell_instance> >::iterator it = m.find(key);
    if (it == m.end()) {
        std::unique_ptr<shell_instance> p = start_instance(name);
        it = m.emplace(key, std::move(p)).first;
    }
    int rc;
    try {
        rc = it->second->send(cmd);
    } catch (...) {
        m.erase(it);   // crashed instance: destructor tears it down; next call restarts
        throw;
    }
    if (rc != 0) throw std::runtime_error("hire::sh: command failed (exit " + std::to_string(rc) + "): " + cmd);
}

inline void close_instances() {
    instances().clear();   // ~shell_instance tears each one down (idempotent)
}

struct instance_guard {
    ~instance_guard() { close_instances(); }
};

}  // namespace detail

// sh runs a command in a shell (see the ported protocol above).
// inst: 1 = default persistent instance (reused by (inst, shell));
//       2+ = a separate persistent instance; 0 = export mode (collect only).
// shell: optional builtin shell name; a "name:" prefix on cmd forces one-shot.
inline void sh(const std::string& cmd, int inst = 1, const std::string& shell = "") {
    if (inst < 0) throw std::runtime_error("hire::sh: inst must be >= 0");
    std::string pname, rest;
    if (detail::split_shell_prefix(cmd, pname, rest)) {
        detail::spawn_oneshot(pname, rest);   // builtin shell: prefix -> one-shot
        return;
    }
    const std::string name = shell.empty() ? detail::default_shell_name() : shell;
    if (inst == 0) {
        detail::collect_export(cmd, name);
        return;
    }
    if (!shell.empty()) {
        // An explicit shell is an explicit request not to persist.
        if (detail::shell_degrades(name)) detail::degrade_warn(name);
        detail::spawn_oneshot(name, cmd);
        return;
    }
    if (detail::shell_degrades(name)) {   // third-party / pwsh-on-Unix
        detail::degrade_warn(name);
        detail::spawn_oneshot(name, cmd);
        return;
    }
    detail::run_persistent(cmd, name, inst);
}

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
// true/false/number literals, and the "_" string array. Unknown shapes are
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
                    case 'b': v += '\b'; break;
                    case 'f': v += '\f'; break;
                    case 'u': {
                        // Go's json.Marshal emits < > & and control chars
                        // as \uXXXX; decode all four hex digits to UTF-8
                        // instead of appending a literal 'u003c'-style byte run.
                        if (i + 4 > n) break;
                        unsigned int cp = 0;
                        for (size_t k = 0; k < 4; ++k) {
                            const char h = json[i + k];
                            cp = (cp << 4) | static_cast<unsigned int>(
                                h <= '9' ? h - '0' : (h | 0x20) - 'a' + 10);
                        }
                        i += 4;
                        if (cp < 0x80) {
                            v += static_cast<char>(cp);
                        } else if (cp < 0x800) {
                            v += static_cast<char>(0xC0 | (cp >> 6));
                            v += static_cast<char>(0x80 | (cp & 0x3F));
                        } else {
                            v += static_cast<char>(0xE0 | (cp >> 12));
                            v += static_cast<char>(0x80 | ((cp >> 6) & 0x3F));
                            v += static_cast<char>(0x80 | (cp & 0x3F));
                        }
                        break;
                    }
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
    skip_ws();    if (i >= n || json[i] != '{') {
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
                out.raw_[key] = json.substr(start, i - start);  // true/false, numbers
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
            bool ok = true;
            try {
                reg.tasks[t].body(args::parse(env != nullptr ? env : "{}"));
            } catch (const std::exception& e) {
                std::cerr << e.what() << "\n";
                ok = false;
            } catch (...) {
                std::cerr << "hire: unknown error in task " << name << "\n";
                ok = false;
            }
            detail::finish_export(name, ok);   // both success and failure paths
            return ok ? 0 : 1;
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
    hire::detail::instance_guard guard;   // RAII: tear down persistent shells on every exit path
    hire::registry reg;
    hire_main(reg);
    return hire::dispatch(reg, argc, argv);
}
```

配套的 hpp 形态 Taskfile.cpp（init 不再生成；照此手写同样合法）：

```cpp
// hire Taskfile.cpp - tasks go in hire_main; hire.hpp does the rest (docs/contract.md).

#include "hire.hpp"

void hire_main(hire::registry& reg) {
    reg.task("hello", "say hello (edit me)")
        .opt("name", "who to greet")
        .run([](const hire::args& a) {
            std::cout << "hello, " << a.get_string("name", "hire") << "\n";
        });
}
```

### C++ 函数形式：`hire::sh`（shell 实例，v2.2.0）

函数形式文件也可 `#include "hire.hpp"`（头由 hire 写进缓存目录、discover 与 compile 两阶段注入 `-I`，见上），用内嵌 SDK 的 `hire::sh` 获得与 Lua `x.sh` 对等的 shell 实例能力——用户侧只多这一行 include：

```cpp
#include "hire.hpp"
// desc: demo
void demo() {
    hire::sh("cd /tmp");        // 默认实例 1，状态跨调用存活
    hire::sh("export FOO=42");
    hire::sh("echo $FOO");      // 42
    hire::sh("echo other", 2);  // 2 号新实例，状态隔离
}
```

签名 `void sh(const std::string& cmd, int inst = 1, const std::string& shell = "")`（全文见上）。语义照移植 `internal/runtime/ctx.go` 的 Lua 协议：

- **持久实例**：默认 `inst=1`，同一 `(inst, shell)` 复用同一 shell 子进程，`cd`/`export`/`set` 跨调用存活；`inst=2+` 另起隔离实例。每条命令后写哨兵 `__HIRE_RC:<nonce>:<rc>`，逐行扫 stdout 取退出码，哨兵行本身不透传；命令非零退出抛异常。
- **`shell:` 前缀**：`bash:`/`sh:`/`pwsh:` 强制 one-shot（仅内置名可识别，`C:\path` 不会被误判）；`sh` 的 `shell` 参数可指定内置 shell，显式 shell 一律不持久化。
- **降级**：第三方 shell 名（C++ 进程拿不到 config.lua 的 registry）与 Unix 上的 pwsh 无法承载持久 stdin 实例，降级为 one-shot 并只警告一次。Windows 上 `bash` 特意解析为 Git Bash，避开 PATH 上的 WSL 启动器 `C:\Windows\System32\bash.exe`。
- **默认 shell 自检**：C++ 是 one-shot 子进程，hire 只下发 `HIRE_ARGS`，拿不到 Lua 侧的 `ShellName`/registry；SDK 自行探测——Windows 默认 `pwsh`、Unix 默认 `bash`（可用环境变量 `HIRE_SHELL` 覆盖为任一内置名）。这是与 Lua 唯一有意的 registry 分歧。
- **生命周期与错误**：`main()` 放 RAII 守卫，析构时 `close_instances()`（关 stdin、等待、超时 kill），异常与正常返回都 teardown，不留孤儿 shell；`dispatch` 的 run 分支 try/catch 把异常转成干净的非零退出码（由 hire 报 `task <name> failed (exit N)`）。discover 分支保持纯，不创建实例。
- **平台**：`#ifdef _WIN32` 用 `CreatePipe`+`CreateProcess`（pwsh 播种 `$LASTEXITCODE = 0`、哨兵用 `$?` 兜底），否则 `pipe`+`fork`+`execvp`；零第三方依赖、零内置解释器。
- **导出模式（`inst=0`）**：命令只收集进缓冲、不执行也不写哨兵。任务成功时由 `dispatch` 的 run 分支调用 `detail::finish_export(name, true)`，把收集的命令用 `\n` 连接一次性打到 stdout（stdout 是 TTY 时另在 stderr 给 `eval "$(hire <name>)"` / pwsh 的 `iex (hire <name>)` 提示）；任务失败（含 `sh()` 抛异常）则调用 `finish_export(name, false)` 丢弃缓冲并在 stderr 提示。成功/失败两条路径都会调用——`sh()` 会抛异常，普通的“dispatch 之后再刷”写法会被跳过，所以钩子必须放在 try/catch 的两个分支里。用户自己的 `std::cout` 不经过这条改道（与 Lua `x.echo` 不同）。
