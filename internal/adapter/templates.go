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

// CppTemplate embeds the __hire__ contract for the cpp runtime: hire
// compiles it once with the build template from config.lua
// (cpp = { build = "g++ -std=c++20 -O2 -o {out} {src}" }), caches the
// binary by source hash, then calls it like the interpreted ones.
const CppTemplate = `// hire Taskfile.cpp - a taskfile is any program speaking the __hire__ contract.
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
`
