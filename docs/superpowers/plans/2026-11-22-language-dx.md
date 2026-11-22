# hire 语言 DX 实施计划（v2.1.0）

> **给执行代理：** 必备子技能：superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans。进度不写勾选表——执行启动时用 `agent_progress_report op=start` 注册本计划（短 slug 标识，如 `language-dx`，plan 字段指向本文件），逐 Task 报开始/完成/阻塞；工具不可用如实说「未上报」。

**Goal:** 落地语言 DX 设计五特性：C++ 函数形式（clang AST，用户自配）、num 参数类型、Python 装饰器 SDK、TOML 纯静态重设计（破坏性）、`hire import` 三格式转换器。

**Architecture:** 全部在现有 adapter/Chain 管线上叠加——num 是 argparse+三适配器的类型扩容；C++ 函数形式新增 clang 解析器 + `#include` 包装 TU（仍走 LoadExec/编译缓存）；TOML 重写内部加载器（外壳签名不变）；import 是新 CLI 子命令 + 独立包。

**Spec:** docs/superpowers/specs/2026-11-22-language-dx-design.md（执行者必读；其 §2 铁律"绝不内置工具链"约束每个 Task）。

## 全局约束

- Go 1.27；依赖上限修订后：gopher-lua + BurntSushi/toml + **gopkg.in/yaml.v3（仅 import 用）**，不新增其它
- clang/解释器/编译器一律用户自配：只探测 PATH（clang++→clang），绝不内置；探测失败报错指引
- 预设默认（python 探 python3→python 等）语义不变
- TOML `run` 字段移除为**破坏性变更**：含 `run` 的文件加载期报错并给出改写指引（指向 py/lua/cpp taskfile）
- contract.md 与 templates.go 逐字一致规则延续；plan/spec 一律中文
- 验收命令：`go test ./...` 与 `go test -tags integration ./integration/`；发版 tag v2.1.0

## Review Focus（易翻车输入，测试必须钉死）

1. **TOML 插值 × `bash:` 前缀叠加**：前缀切分先行、插值作用于命令文本——三组合例（前缀+插值、前缀+条件插入、纯插值含空格值）
2. **deps 加载期校验错误文案**：未知依赖 / 循环 / 依赖带必填参数 / when 过滤后的依赖，四个错误都必须点名文件+任务名+依赖名
3. **C++ 双形态判别边界**：含 `hire.hpp` 或 `__hire__` 的 .cpp 走既有 raw 契约路径，否则走函数形式；两类文件在同一链里混跑
4. **clang 缺失/挂死**：无 clang++ 时函数形式 discover 报错指引（装 clang 或用 hpp 形态），不 panic
5. **import 转义边界**：Just/Make 配方里的引号/反斜杠/中文进 TOML 字符串要安全转义；畸形输入不 panic

---

### Task 1: num 参数类型（基础，先行）

**Files:** Modify `internal/argparse/argparse.go`、`internal/adapter/adapter.go`（validArgType）、`internal/adapter/lua.go`（糖）、`internal/adapter/toml.go`+`exec.go`（错误文案）、`internal/runtime/ctx.go` 不动（LNumber 天然承载）

**Interfaces:**
- Produces: `Spec.Type` 合法值扩为 `"string"|"bool"|"num"`；argparse 对 num 做 `strconv.ParseFloat` 校验；`shared validArgType = {"string","bool","num"}`；Lua 糖 `num "desc"`（可选 num，必填走全形式）；hpp 链式 `task_def& num(std::string name, std::string desc)` + `double get_num(key, fallback)`；exec discover schema 透传 num

**Steps:** 1) argparse num 校验用例（合法/非法/必填）红→绿；2) validArgType + 三处文案 + 各适配器加载测试；3) lua `num` 糖注入 + 测试；4) hpp 模板 .num/get_num + cpp 集成腿断言；5) 全量门 + commit `feat: num arg type across all adapters`

### Task 2: TOML 纯静态重设计（破坏性）

**Files:** Rewrite `internal/adapter/toml.go`；Test `internal/adapter/toml_test.go`；Modify `docs/toml.md`

**Interfaces:**
- Consumes: LoadTOML 外壳签名 `(path string) (*File, error)` 不变；Task/Fn 管线不变（合成函数机制沿用）
- Produces（schema 全量）：`cmd`（string）与 `cmds`（[]string）等价、并存互斥报错；`run` 字段 → 明确报错"已移除，需要逻辑请用 py/cpp/lua taskfile"；`deps []string`（**仅本文件**）；插值两规则 `{{name}}`（string/num 原文替换，可选未传=空串）与 `{{flag:+word}}`（bool 条件插入）；`{{typo}}` 未声明 → 加载期报错；`when={os=...}` 沿用
- **deps 加载期校验**（在 LoadTOML 内完成，全文件已知）：未知依赖 / 循环（DFS）/ 依赖带必填参数 / when 过滤掉的依赖（视为不存在）→ 全部报错点名（文件、任务、依赖名）；执行 = 合成函数先序调用每个依赖的 Fn 一次，再执行本体 cmds（走持久/one-shot 现有链路，错误即停）
- 合成函数生成顺序：前缀切分（`bash:`）先行 → 插值作用于命令文本 → 命令序列

**Steps:** 1) 全部加载期校验用例（Review Focus 2）红→绿；2) 插值与执行合成（Review Focus 1 组合例）；3) run 移除报错 + cmd/cmds 互斥；4) docs/toml.md 重写为纯静态 schema；5) 全量门 + commit `feat!: pure-static TOML (cmds/deps/interpolation, run removed)`

### Task 3: Python 装饰器 SDK（hire.py）

**Files:** Modify `internal/adapter/templates.go`（PythonSDKTemplate + 瘦身 PythonTemplate）、`internal/cli/init.go`（python 生成两文件）；Test `internal/cli/init_test.go`

**Interfaces:**
- Produces: `init --lang python` 生成 `hire.py` + `Taskfile.py`；装饰器 `@task(name, desc=..., args={...})` 注册进 hire.py 模块级 registry；`opt/req/flag/num` 声明助手；`a` 属性访问（`__getattr__` → dict get）；`Taskfile.py` 尾部 `import hire; if __name__ == "__main__": hire.dispatch()`；裸单文件契约依旧合法（contract.md 两形态并列）
- per-file init 语义复用（存在的保留明说、缺的生成）

**Steps:** 1) 自举测试：init python → ls 发现 → hello 跑通（本机 python 在，真跑）；2) num 参数经装饰器往返（协同 Task 1）；3) contract.md Python 节双形态 + 逐字同步；4) 全量门 + commit `feat: python decorator SDK (hire.py)`

### Task 4: C++ 函数形式 + clang 解析（最大件，独占）

**Files:** Create `internal/adapter/cppfunc.go`（AST 解析 + 包装 TU 生成 + 双形态判别）、`internal/adapter/cppfunc_test.go`；Modify `internal/adapter/templates.go`（CppTemplate 换函数形式）、`internal/adapter/runtime.go`（RuntimeSpec 增 `Parser` 字段 + 探测）、`internal/config/config.go`（表形态增 `parser` 键）、`internal/adapter/exec.go`（cpp 双形态分流）；Test 各配套

**Interfaces:**
- Consumes: Task 1 的 num（`int/long/double/float` 参数 → num）；LoadExec/ensureCompiled/cacheKey 管线
- Produces:
```go
// cppfunc.go
func isContractForm(src []byte) bool          // 含 "hire.hpp" 或 "__hire__" → raw 契约；否则函数形式
func parseFuncForm(src, path string, clangCmd string) ([]FuncTask, error)  // clang++ -Xclang -ast-dump=json -fsyntax-only -fparse-all-comments
type FuncTask struct{ Name, Desc string; Params []FuncParam }   // Params: name/type/default
func (t FuncTask) toTaskSpec() (adapter args spec, error)      // string→string, bool→flag, int/long/double/float→num；默认值=可选；不支持类型报错点名函数+参数
func genWrapper(srcPath, outDir string, tasks []FuncTask) (string, error)  // 缓存目录生成 #include 包装 TU（注册代码 + JSON→typed 转换 + main）
```
- 函数选择规则：顶层非 static 自由函数 = 任务；`static`/匿名命名空间 = 助手；desc = 函数紧邻上方注释首行
- `RuntimeSpec.Parser`（`parser = "clang++"` 可选，缺省探测 clang++→clang）；解析结果缓存 `<hash>.tasks.json`（源码+命令串键）；编译仍走用户 build 模板（编译对象 = 包装 TU）
- CppTemplate 换为纯函数示例（含 `// desc:` 注释示范）；contract.md 函数形式为主、hpp 链式降级为合法遗留

**Steps:** 1) ast-dump JSON 解析对**本机 clang++ 真实输出**的固定样例测试（顶层函数提取/static 排除/参数默认值/注释 desc）红→绿；2) 双形态判别 + 分流（Review Focus 3）；3) 包装 TU 生成 → g++ 编译 → discover/run 全链自举（本机 clang++ 存在则真跑，缺失 skip）；4) Parser 探测 + config 表形态 + clang 缺失报错指引（Review Focus 4）；5) contract.md 同步；6) 全量门 + commit `feat: C++ function-form taskfiles via user-configured clang AST`

### Task 5: `hire import` 转换器

**Files:** Create `internal/importer/`（`just.go`/`make.go`/`gofile.go`+共享映射）、`internal/cli/import.go`；Modify `go.mod`（yaml.v3，走代理 go get）、`internal/cli/cli.go`（dispatch 增 import）；Test `internal/importer/*_test.go`

**Interfaces:**
- Consumes: Task 2 的新 TOML schema（转换产物）
- Produces: `hire import [--from just|make|task] <file>` → 写 `Taskfile.toml`（已存在则拒绝并提示 `--force`）；`--from` 缺省按内容嗅探（`justfile` 头/`Taskfile.yml`/`Makefile`）；映射：Make 目标+tab 配方→cmds、前驱→deps、`.PHONY` 忽略、`$VAR/$$`/时间戳语义→警告；Just 配方→cmds、`# desc`→desc、参数→args（无默认=required）、`[private]` 跳过警告、`@/[script]/export`→警告；go-task（yaml.v3）：desc/cmds/deps 直映、vars 烘焙内联、platforms→when、其余特性警告 + **deps 顺序执行语义警告**
- 转义边界（Review Focus 5）：命令行文本进 TOML 字符串安全转义；畸形输入报错不 panic

**Steps:** 1) 每格式一个真实最小样例 → 转换 → 产物可被 hire 直接加载运行的端到端用例；2) 未迁移警告逐条断言；3) 输出碰撞拒绝 + `--force`；4) go.mod 依赖（代理）；5) commit `feat: hire import (just/make/go-task -> toml)`

### Task 6: 文档 + 集成 + 发布收尾

**Files:** Rewrite `docs/toml.md`（若 Task 2 未尽）、`docs/contract.md` 终稿、`CHANGELOG.md`（v2.1.0 + TOML 破坏性声明）；Rewrite `integration/integration_test.go` 增腿（函数形式 C++ e2e、python SDK e2e、TOML 插值+deps e2e、import 产物直跑）

**Steps:** 1) 集成腿补齐跑到绿（clang 缺失优雅 skip）；2) `go test ./...` + integration 原样全绿；3) 文档终稿 + CHANGELOG；4) tag `v2.1.0` 前发布清单校对（goreleaser/scoop/brew 无需改）；5) commit `docs+test: v2.1.0 language DX suite`

---

## 依赖与波次

- **Task 1 先行**（num 是 3/4 的前置）
- 波次 1（并行，文件零交集）：Task 2（TOML）∥ Task 3（Python SDK）
- 波次 2（并行）：Task 4（C++ 函数形式，独占 templates.go）∥ Task 5（import，独立包 + cli.go）
- 波次 3：Task 6（收尾）
- Task 2 与 Task 4 的 `docs/contract.md` 冲突：Task 4 后做 contract 终稿（波次内 Task 4 → Task 6 顺序吸收）
- 同波次双 subagent 各自 worktree（先例：wave 模式），按序合并

## 执行备注

- clang++ 本机已装（设计阶段探测过）——函数形式集成腿可真跑；CI ubuntu-latest 自带 clang ✓
- yaml.v3 经代理 `go get`（127.0.0.1:7890）
- `{{` 在 TOML 字符串里出现但不符合两规则形态（如 `{{ }}` 空名）→ 同未声明参数处理（加载期报错）
- import 的 go-task 解析只做其 schema 子集（tasks.<name> 平铺结构），嵌套 includes 明确警告不递归
