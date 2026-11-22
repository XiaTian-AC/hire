# hire 语言 DX 整修 — Design Spec

**Date:** 2026-11-22
**Status:** Draft（待用户批准，builder check 进行中）
**基线：** v2.0.3（master `01a37a1`）
**规模：** 一个发布内五个特性（v2.1.0）

## 1. 意图与动因

hire 的多语言架构已就位（v2.0.x），但各语言的**书写体验**参差：C++ 经三轮瘦身仍有 wrapper/lambda 壳，Python 样板 ~40 行，TOML 夹杂内联 Lua 违背其静态定位。本轮目标：每种预置语言达到其语言能力内的**最短诚实形态**，并以转换器承接老牌任务运行器（Just/Make/Task）的既有用户。

## 2. 硬约束（新增，产品铁律）

**绝不内置工具链/解释器。** 预设运行时默认值只探测 PATH 上已安装的二进制（python3→python、node、g++）；不随包分发任何编译器/解释器，不为"开箱即用"引入重量依赖。由此：否决 C++ 源码反射解析（拖 clang）；宏块形态是"函数即任务"在无反射 C++ 里的最近距离。

## 3. 方案对比（对话已裁）

| 决策点 | 备选 | 裁决 |
|---|---|---|
| C++ 机器补全载体 | hire 运行时拼接文本（行号漂移、IDE 全红） / 宏块（无反射极限） / **函数形式 + clang AST 解析** | **函数形式**（用户裁决）——clang 为用户自配前置（与 g++ 同地位，不违铁律）；纯 Go 解析 ast-dump JSON，无 Cgo；`#include` 包装 TU 保真实行号；宏块方案废弃 |
| TOML 对老牌工具 | A 落地兼容层（= 重实现三个工具 + 引 YAML 违反身份决策） | **B 迁移转换器**：映射共同 80%，未映射特性明确警告 |
| TOML 形态 | 内联 Lua run（现状，用户否决"有 lua 成分"） | **纯静态** + 参数插值 |
| Python 形态 | 自包含契约程序（现状 40 行样板） | **装饰器 SDK**（用户批准） |
| TOML deps 作用域 | A 仅本文件（加载期全校验） / B 合并链全域 | **A 仅本文件**（用户裁决，与 x.run 同文件原则一致） |

## 4. 特性 A：C++ 函数形式 + clang 解析（用户自配）

用户文件是**纯 C++ 函数**——无宏、无注册、无 main、无 include 任何 hire 头：

```cpp
#include <iostream>
#include <string>

// desc: say hello (edit me)
void hello(std::string name = "hire") {
    std::cout << "hello, " << name << "\n";
}

// desc: compile the app
void build(std::string target = "app", int opt = 2) {
    std::cout << "g++ -O" << opt << " -o " << target << " main.cpp\n";
}

namespace { void helper() {} }   // 匿名命名空间/static 函数 = 助手，不是任务
```

机制（全程不内置任何工具，clang 与 g++ 同为用户自配）：

1. **解析**：hire 调用用户 PATH 上的 `clang++ -Xclang -ast-dump=json -fsyntax-only -fparse-all-comments Taskfile.cpp`，用纯 Go 解析其 JSON AST 输出（**无 Cgo、无 libclang 链接**——hire 仍是小单二进制，只是 shell 出去问用户装的 clang）。提取：顶层非 static 自由函数（= 任务；`static`/匿名命名空间内 = 助手）、参数名/类型/默认值、函数紧邻上方注释首行（= desc 约定）
2. **映射**：参数类型 `std::string`/`string`→string、`bool`→flag、`int`/`long`→num、`double`/`float`→num（feature B 协同）；有默认值 = 可选参数，无默认 = 必填；不支持的参数类型 → 加载期报错（点名函数+参数）
3. **生成与编译**：缓存目录（不污染用户目录）生成包装 TU：`#include "hire.hpp"` + `#include "用户的 Taskfile.cpp"` + 逐函数注册代码（JSON→typed 转换后**直接以函数实参调用**，无插值）+ main()。**用 #include 引入用户文件** → 编译器诊断以"Taskfile.cpp:真实行号"报告（include 语义内建按文件跟踪行号，注入拼接的行号漂移问题不存在）；用户文件本身语法自洽，IDE 干净
4. **编译**仍走用户的 build 模板（g++ 即可——build 编译的是包装 TU）；**解析结果随编译缓存一起缓存**（`<hash>.tasks.json`），discover 零执行、源码未变时零解析、零重编
5. **前置与提示**：函数形式需要 `clang++`（解析）+ 用户的 build 命令（编译）；无 clang 时 discover 报错明确指引（装 clang++，或改用 hire.hpp 链式形态）。runtimes 增可选字段 `parser = "clang++"`（缺省自动探测 clang++→clang），`cpp = { build = "...", parser = "clang++" }`
6. **hire.hpp 链式形态降级为合法遗留**（已有文件不破坏；contract.md 两形态并列，函数形式为主推）；宏块方案（本稿前版）废弃

方案对比（已裁）：宏块 = 无反射 C++ 的显式声明极限，但用户裁决要"函数即任务"的本体形态——clang 为用户自配前置时，其代价（PATH 上多一个工具）与本工具"运行时用户自配"哲学一致，换取零样板。

## 5. 特性 B：num 参数类型

`type = "num"`（float64 解析校验），全语言受益：

- argparse：Spec 校验 + help 显示 `num`
- Lua 糖：`num "port number"`（可选）；必填 num 走全形式 `{type="num", required=true}`
- C++：`num("desc")` 糖（可选 num）；`a.get_num(key, fallback)` 返回 double
- Python SDK：`num("desc")`；JSON 里天然是 number
- TOML：`{{port}}` 插值为数字文本
- exec discover schema 透传 JSON 机制无需改，但**类型白名单在三处适配器同步扩容**：`adapter.go` 的 `validArgType`（`string`|`bool`|`num`）+ lua/toml/exec 三处错误文案（"use \"string\", \"bool\" or \"num\""）+ 各自加载测试
- 链式 API 补 `.num("name", desc)` 糖保持对称（contract.md 逐字发布同前）

## 6. 特性 C：Python 装饰器 SDK（hire.py）

`init --lang python` 生成 `hire.py` + 瘦 `Taskfile.py`：

```python
from hire import task, opt

@task("hello", desc="say hello (edit me)", args={"name": opt("who to greet")})
def hello(a):
    print(f"hello, {a.name or 'hire'}")

import hire
if __name__ == "__main__":
    hire.dispatch()
```

- 装饰器注册进 hire.py 模块级 registry；`dispatch()` 处理 discover/run 两动词 + `HIRE_ARGS` JSON 解析；`a` 属性访问（`a.name`）+ `a.get("name")` 兜底
- init 提示沿用（预置默认 + 覆盖说明）；裸单文件契约程序依旧合法（contract.md 两种形态并列）

## 7. 特性 D：TOML 纯静态重设计（破坏性）

**移除内联 Lua `run` 字段**——TOML 只做声明；需要逻辑写 py/cpp/lua taskfile。字段参照 go-task 命名习惯：

```toml
shell = "bash"                              # 可选全局默认 shell（沿用现有）

[tasks.clean]
desc = "remove build artifacts"
cmds = ["rm -rf dist/"]                      # 单条可写 cmd = "..."（别名）

[tasks.build]
desc = "compile the app"
cmds = ["g++ -O2 -o app main.cpp"]

[tasks.release]
desc = "tag, build, push"
deps = ["build"]                             # 先序执行 build；错误即停，无 DAG
[tasks.release.args.tag]
required = true
desc = "git tag, e.g. v2.1.0"
[tasks.release.args.force]
type = "bool"
cmds = [
    "git tag {{tag}}",                       # string/num 插值：原文替换（同 go-task 语义，含空格需自加引号）
    "git push --tags {{force:+--force}}",    # bool 条件插入（Make 的 $(VAR:+word) 血统）：真值插入 --force，否则空
]
```

- 插值仅两条规则：`{{name}}`（string/num 原文替换）、`{{flag:+word}}`（bool 条件插入）；无模板语言、无条件分支、无函数库
- **插值边界语义（builder check 裁决）**：`{{typo}}` 引用未声明的参数 → **加载期报错**（延续 TOML 未知字段即错的严格先例）；可选参数未传 → **替换为空串**（bool 用 `{{flag:+word}}` 表条件，string 空 = 不写就是空）
- `cmds`（数组）与 `cmd`（单条）等价，**两者并存 = 互斥报错**（同现有 cmd/run 互斥）；`deps` **仅本文件**（用户裁决，与 `x.run` 同文件原则一致）——加载期完成全部校验：未知依赖、循环依赖（递归跟随其自身 deps）、依赖带必填参数（静态不可满足）均报错；`when.os` 过滤掉的依赖视为不存在；执行 = 顺序跑依赖一次再跑本体，错误即停；`when = { os = ... }` 沿用；单条命令的 `bash:` 前缀机制沿用（shell 选择）
- cmd 前缀 `bash:`、插值、deps 组合的执行序：deps 依次 → 本任务 cmds 依次；每条经持久/one-shot 同现有链路
- **破坏性**：`run` 字段（内联 Lua）移除；v2.0.x 的 TOML 文件如含 `run` 加载期报错并指引改写路径。无外部用户，接受

## 8. 特性 E：`hire import` 转换器

```
hire import <file>              # 按扩展名/内容嗅探：justfile / Makefile / Taskfile.yml
hire import --from just|make|task <file>   # 显式指定
→ 生成 Taskfile.toml（特性 D schema）+ stderr 逐条"未迁移"警告
```

- **Make**：目标 + tab 配方 → `[tasks.x] cmds`；`.PHONY` 忽略；前驱 → `deps`；`$VAR`/`$$`/时间戳语义 → 警告未迁移；自建解析（语法小）
- **Just**：配方 → tasks；`# desc` 注释 → `desc`；参数（`=default`）→ args（无默认 = required）；`[private]` → 跳过警告；`@`/`[script]`/`export` → 警告；自建解析
- **go-task**：`desc/cmds/deps` 直映；`vars` 在转换时烘焙进 cmds（静态值直接内联）；`platforms: [windows]` → `when = { os = ... }`；`sources/generates`（新鲜度）、`dotenv/env/preconditions/includes` → 警告；额外一条语义警告：**hire 的 deps 为顺序执行、无并行**（go-task 并发 deps 语义差异）；**需要 yaml.v3 依赖（仅 import 子命令使用）**
- 输出碰撞：目标 `Taskfile.toml` 已存在 → 拒绝（延续 init 不覆盖先例）并提示 `--force` 覆盖——转换产物可再生，默认行为仍不破坏既有文件（builder check 裁决）
- 依赖上限修订：gopher-lua + toml + **yaml.v3（仅 import）**——import 是可选功能，运行时格式不引 YAML 的身份决策不变

## 9. 非目标

- 三个老牌工具的**执行级兼容**（变量模板引擎、DAG/并行依赖、时间戳新鲜度、dotenv）
- YAML 作为 hire 的原生格式（身份决策维持）
- Node SDK（本轮 Python 优先，Node 裸契约维持；对称 SDK 留待下轮）
- Lua 任何改动（保持不变；仍是 TOML 移除内联 run 后的"要逻辑就写真语言"逃生口之一）
- 内置任何工具链/解释器（§2 铁律）

## 10. 测试与文档

- 每特性 TDD 单测：**clang 函数形式**（顶层非 static 函数提取 / static+匿名命名空间排除 / 参数类型映射与默认值 / 注释 desc 约定 / 不支持类型报错 / AST JSON 解析对真实 clang 输出的固定样例）+ clang 缺失时的报错路径；num 校验与各语言往返；python 装饰器 discover/run 自举；TOML 插值两规则/deps 同文件校验（未知/循环/必填/when 过滤）/run 移除报错；import 三格式各一个真实样例 + 未迁移警告断言 + 输出碰撞拒绝
- 集成：函数形式 C++ e2e（本机有 clang++ 则真跑，缺失 skip）、python SDK e2e、TOML 插值+deps e2e、import 转换产物直接可跑
- 集成：宏块 C++ e2e、python SDK e2e、TOML 插值+deps e2e、import 转换产物直接可跑
- 文档：contract.md（C++ 宏块 + Python SDK + TOML 新 schema 逐字同步）、docs/toml.md 重写、CHANGELOG v2.1.0（含 TOML 破坏性声明）
- 终审与发布同前例（tag v2.1.0 触发流水线）

## 11. 留给实现者

- clang JSON AST 的解析边界：ast-dump=json 的字段形态随 clang 版本浮动——以本机 clang++ 输出为准做宽容解析（字段缺失降级而非 panic），版本敏感处在测试固定样例
- desc 注释约定的精确定义（紧邻函数上方、允许空行分隔与否）——以 clang RawComment 输出为准一次定清
- 包装 TU 的生成细节（缓存目录布局、`#include` 相对路径处理、JSON→typed 转换代码生成）
- python `a` 的属性访问实现（小 class，`__getattr__` → dict get）
- import 各格式的解析边界（Just 属性集、Make 的多目标行、go-task 的嵌套 vars）
- TOML 插值与 `bash:` 前缀的先后（前缀切分先行，插值作用于命令文本）
