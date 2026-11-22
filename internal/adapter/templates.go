package adapter

// Taskfile templates for `hire init` (spec §10 item 1): one runnable
// hello task plus comments; the external-language ones embed the full
// __hire__ contract. docs/contract.md reprints the three external
// listings verbatim — edit both together.

// LuaTemplate is the default scaffold: the native adapter, no contract
// code needed.
const LuaTemplate = `-- hire Taskfile.lua - a taskfile is a plain Lua script: tasks are globals.
-- Run one with "hire <name>", list them with "hire ls".
--
-- Three ways to declare a task:
--   clean = "rm -rf dist/"               -- string = one shell command
--   steps = { "echo one", "echo two" }   -- array of strings = a sequence
--   full = task {                        -- full form: desc + args + run
--     desc = "build and release",
--     args = { tag = req "version tag" }, -- req = required string; bool = flag
--     run = function(args)
--       x.sh("git tag " .. args.tag)     -- x.sh runs a shell command
--     end,
--   }
--
-- Inside a task: x.sh(cmd) shells out, x.echo(...) prints, x.run("other")
-- calls a sibling task, x.env("NAME") reads the environment.
-- The full reference lives in docs/lua.md.

hello = task {
  desc = "say hello (edit me)",
  run = function()
    x.echo("hello from hire")
  end,
}
`

// PythonTemplate embeds the __hire__ contract for the python runtime.
const PythonTemplate = `#!/usr/bin/env python3
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
`

// NodeTemplate embeds the __hire__ contract for the node runtime.
// (No template literals: the constant is a Go raw string.)
const NodeTemplate = `#!/usr/bin/env node
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
`

// CppHeaderTemplate is hire.hpp, the __hire__ contract SDK for C++
// taskfiles: registry/dispatch, discover JSON emission, and a real
// HIRE_ARGS parser (escapes handled). Edit together with CppTemplate
// and the C++ listings in docs/contract.md — they must stay identical.
const CppHeaderTemplate = `// hire.hpp - the hire contract SDK for C++ taskfiles.
//
// Include it from Taskfile.cpp, define hire_main with your tasks; the
// header provides main() itself:
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
    // Sugar mirroring the Lua adapter: req = required string, opt =
    // optional string, flag = optional bool; .arg stays for the rest.
    task_def& req(std::string name, std::string desc = "") { return arg(std::move(name), "string", true, std::move(desc)); }
    task_def& opt(std::string name, std::string desc = "") { return arg(std::move(name), "string", false, std::move(desc)); }
    task_def& flag(std::string name, std::string desc = "") { return arg(std::move(name), "bool", false, std::move(desc)); }
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
`

// CppTemplate is the user-facing Taskfile.cpp that pairs with
// CppHeaderTemplate: only the tasks, declared in hire_main - the
// contract, the parsing, even main() live in hire.hpp.
const CppTemplate = `// hire Taskfile.cpp - tasks go in hire_main; hire.hpp does the rest (docs/contract.md).

#include "hire.hpp"

void hire_main(hire::registry& reg) {
    reg.task("hello", "say hello (edit me)")
        .opt("name", "who to greet")
        .run([](const hire::args& a) {
            std::cout << "hello, " << a.get_string("name", "hire") << "\n";
        });
}
`
