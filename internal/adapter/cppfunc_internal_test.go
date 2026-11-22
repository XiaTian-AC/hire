package adapter

// Pure-Go tests for the function-form C++ pipeline, pinned against the
// committed testdata/cppfunc-ast.json: the real JSON AST output of a
// local clang 19 for testdata/cppfunc-sample.cpp (see the regeneration
// note inside that file). Live toolchain legs live in cppfunc_test.go.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIsContractForm pins the dual-form discrimination (ruling 1): the
// two byte markers only ever appear in contract-form sources, so their
// presence is complete and honest.
func TestIsContractForm(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want bool
	}{
		{"hpp include", `#include "hire.hpp"` + "\nvoid hire_main() {}\n", true},
		{"contract verb", `#include <cstdio>\nint main(){ printf("__hire__"); }`, true},
		{"function form", "#include <iostream>\n\n// desc: hi\nvoid hi() {}\n", false},
		{"empty", "", false},
		{"binary garbage", "\x00\x01\x02", false},
	}
	for _, c := range cases {
		if got := isContractForm([]byte(strings.ReplaceAll(c.src, `\n`, "\n"))); got != c.want {
			t.Errorf("isContractForm(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestParseASTFixture pins the extraction against the committed real
// clang output: top-level non-static functions of the user's file are
// tasks, static and namespace-nested helpers and header-declared
// functions are not, desc comes from the first comment line (the
// "desc:" prefix stripped), and defaults keep their literal spelling.
func TestParseASTFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "cppfunc-ast.json"))
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := parseAST(bytes.NewReader(data), "cppfunc-sample.cpp")
	if err != nil {
		t.Fatalf("parseAST: %v", err)
	}
	type wantParam struct {
		name, cxxType, def string
		hasDefault         bool
	}
	type wantTask struct {
		name, desc string
		params     []wantParam
	}
	wants := []wantTask{
		{"hello", "say hello", []wantParam{{"name", "std::string", `"hire"`, true}}},
		{"greet_cn", "打个招呼", []wantParam{{"who", "std::string", "", false}}},
		{"build", "compile the app", []wantParam{
			{"target", "std::string", `"app"`, true},
			{"opt", "int", "2", true},
			{"verbose", "bool", "false", true},
			{"ratio", "double", "1.5", true},
		}},
		{"plain", "plain comment line without the prefix", nil},
		{"detached", "orphaned above a blank line, clang decides whether it attaches", nil},
		{"uncommented", "", nil},
	}
	if len(tasks) != len(wants) {
		t.Fatalf("extracted %d tasks, want %d: %+v", len(tasks), len(wants), tasks)
	}
	for i, w := range wants {
		got := tasks[i]
		if got.Name != w.name || got.Desc != w.desc {
			t.Errorf("task %d = %q desc %q, want %q desc %q", i, got.Name, got.Desc, w.name, w.desc)
		}
		if len(got.Params) != len(w.params) {
			t.Fatalf("task %s: %d params, want %d", w.name, len(got.Params), len(w.params))
		}
		for j, wp := range w.params {
			gp := got.Params[j]
			if gp.Name != wp.name || gp.CxxType != wp.cxxType || gp.HasDefault != wp.hasDefault || gp.Default != wp.def {
				t.Errorf("task %s param %d = %+v, want %+v", w.name, j, gp, wp)
			}
		}
	}
	for _, excluded := range []string{"helper_one", "helper_two", "declared_in_header"} {
		for _, got := range tasks {
			if got.Name == excluded {
				t.Errorf("%s must not be a task", excluded)
			}
		}
	}
}

// TestParseASTRedeclarations reproduces the review's Important 1:
// clang emits one FunctionDecl per declaration and stamps previousDecl
// on the later ones, so a forward declaration + definition (even a
// three-node chain) must fold into one linkable task, while a genuine
// overload stays distinct and is rejected by validation. The JSON shape
// here is copied from a real clang 19 -Xclang -ast-dump=json run.
func TestParseASTRedeclarations(t *testing.T) {
	const ast = `{"kind":"TranslationUnitDecl","inner":[
		{"kind":"FunctionDecl","id":"0x1","name":"serve","loc":{"file":"Taskfile.cpp"},"inner":[
			{"kind":"ParmVarDecl","name":"port","type":{"qualType":"int"}},
			{"kind":"FullComment","inner":[{"kind":"ParagraphComment","inner":[{"kind":"TextComment","text":" desc: serve traffic"}]}]}]},
		{"kind":"FunctionDecl","id":"0x2","previousDecl":"0x1","name":"serve","inner":[
			{"kind":"ParmVarDecl","name":"port","type":{"qualType":"int"}},
			{"kind":"CompoundStmt"}]},
		{"kind":"FunctionDecl","id":"0x3","previousDecl":"0x2","name":"serve","inner":[
			{"kind":"CompoundStmt"}]},
		{"kind":"FunctionDecl","id":"0x4","name":"ext","storageClass":"extern","loc":{"file":"Taskfile.cpp"},"inner":[
			{"kind":"ParmVarDecl","name":"x","type":{"qualType":"int"}}]}
	]}`
	tasks, err := parseAST(strings.NewReader(ast), "Taskfile.cpp")
	if err != nil {
		t.Fatalf("parseAST: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("extracted %d tasks, want 1 (forward decl + 2 redecls): %+v", len(tasks), tasks)
	}
	got := tasks[0]
	if got.Name != "serve" || got.Desc != "serve traffic" || !got.hasBody {
		t.Errorf("folded task = %+v, want serve / serve traffic / hasBody", got)
	}
	if err := validateFuncTasks("Taskfile.cpp", tasks); err != nil {
		t.Errorf("folded task rejected: %v", err)
	}
	for _, task := range tasks {
		if task.Name == "ext" {
			t.Error("extern declaration must not be a task")
		}
	}

	// Two defined overloads carry different ids and no previousDecl link:
	// they must survive as two tasks so validation reports the overload.
	const overloads = `{"kind":"TranslationUnitDecl","inner":[
		{"kind":"FunctionDecl","id":"0xa","name":"dup","loc":{"file":"Taskfile.cpp"},"inner":[
			{"kind":"ParmVarDecl","name":"x","type":{"qualType":"int"}},{"kind":"CompoundStmt"}]},
		{"kind":"FunctionDecl","id":"0xb","name":"dup","loc":{"file":"Taskfile.cpp"},"inner":[
			{"kind":"ParmVarDecl","name":"y","type":{"qualType":"double"}},{"kind":"CompoundStmt"}]}
	]}`
	tasks, err = parseAST(strings.NewReader(overloads), "Taskfile.cpp")
	if err != nil {
		t.Fatalf("parseAST (overloads): %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("overloads folded into %d tasks, want 2", len(tasks))
	}
	if err := validateFuncTasks("Taskfile.cpp", tasks); err == nil ||
		!strings.Contains(err.Error(), "declared twice") {
		t.Errorf("overloads must be rejected, got %v", err)
	}
}

// TestCppfuncCacheKeyVersioned pins the review's Important 2: the
// function-form key is the raw key plus the wrapper codegen inputs, so
// it differs from the raw path for the same src+build and moves when
// either the header bytes or the codegen version salt changes.
func TestCppfuncCacheKeyVersioned(t *testing.T) {
	src, build := []byte("void serve(int port) {}\n"), "g++ -std=c++20 -o {out} {src}"
	fnKey := cppfuncCacheKey(src, build)
	if fnKey == cacheKey(src, build) {
		t.Fatal("function-form key must differ from the raw cache key")
	}
	if fnKey != cppfuncKeyWith(src, build, CppHeaderTemplate, cppfuncCodegenVersion) {
		t.Fatal("function-form key does not hash the header and version")
	}
	if fnKey == cppfuncKeyWith(src, build, CppHeaderTemplate, cppfuncCodegenVersion+1) {
		t.Error("key did not change with the codegen version salt")
	}
	if fnKey == cppfuncKeyWith(src, build, CppHeaderTemplate+"\n// changed", cppfuncCodegenVersion) {
		t.Error("key did not change with the SDK header bytes")
	}
}

// TestParseASTMalformed pins the tolerance rules (ruling 2): garbage is
// an error, never a panic, and missing fields degrade to empty results.
func TestParseASTMalformed(t *testing.T) {
	for name, src := range map[string]string{
		"empty":          "",
		"not json":       "clang: error: no such file",
		"not an object":  `[1, 2, 3]`,
		"truncated":      `{"kind": "TranslationUnitDecl", "inner": [{"kind":`,
		"no inner":       `{"kind": "TranslationUnitDecl"}`,
		"inner not arr":  `{"kind": "TranslationUnitDecl", "inner": 5}`,
		"empty inner":    `{"kind": "TranslationUnitDecl", "inner": []}`,
		"skipped node":   `{"kind": "TranslationUnitDecl", "inner": [{"kind": "TypedefDecl", "loc": {}}]}`,
		"unnamed fn":     `{"kind": "TranslationUnitDecl", "inner": [{"kind": "FunctionDecl", "loc": {"file": "x.cpp"}}]}`,
		"static fn":      `{"kind": "TranslationUnitDecl", "inner": [{"kind": "FunctionDecl", "name": "s", "storageClass": "static", "loc": {"file": "x.cpp"}}]}`,
		"foreign fn":     `{"kind": "TranslationUnitDecl", "inner": [{"kind": "FunctionDecl", "name": "h", "loc": {"file": "other.h"}}]}`,
		"weird fn shape": `{"kind": "TranslationUnitDecl", "inner": [{"kind": "FunctionDecl", "name": 5, "loc": {"file": "x.cpp"}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			tasks, err := parseAST(strings.NewReader(src), "x.cpp")
			if err != nil && !strings.Contains(err.Error(), "clang AST") {
				t.Fatalf("error %q should be a clang AST error", err)
			}
			if err != nil && len(tasks) != 0 {
				t.Fatalf("error with %d tasks, want none", len(tasks))
			}
			if err == nil {
				for _, got := range tasks {
					if got.Name == "" {
						t.Errorf("extracted a nameless task: %+v", got)
					}
				}
			}
		})
	}
}

// TestMapCxxType pins the ruling-4 mapping set, including the
// unsupported spellings that must error at load.
func TestMapCxxType(t *testing.T) {
	ok := map[string]string{
		"std::string": "string", "string": "string",
		"bool": "bool",
		"int":  "num", "long": "num", "long long": "num", "double": "num", "float": "num",
	}
	for cxx, want := range ok {
		got, err := mapCxxType(cxx)
		if err != nil || got != want {
			t.Errorf("mapCxxType(%q) = %q, %v; want %q", cxx, got, err, want)
		}
	}
	for _, cxx := range []string{"unsigned int", "short", "const std::string &", "std::vector<int>", "char", ""} {
		if _, err := mapCxxType(cxx); err == nil {
			t.Errorf("mapCxxType(%q) should error", cxx)
		}
	}
}

// TestValidateFuncTasks pins the load-time rules and their exact error
// targets: reserved names, overloads, unnamed parameters, unmappable
// types, and defaults that are not simple literals.
func TestValidateFuncTasks(t *testing.T) {
	path := "Taskfile.cpp"
	valid := []FuncTask{{
		Name: "hello", Desc: "say hello",
		Params: []FuncParam{
			{Name: "name", CxxType: "std::string", HasDefault: true, Default: "hire", litKind: "StringLiteral"},
			{Name: "times", CxxType: "int", HasDefault: true, Default: "3", litKind: "IntegerLiteral"},
			{Name: "port", CxxType: "int"},
		},
	}}
	if err := validateFuncTasks(path, valid); err != nil {
		t.Fatalf("valid tasks rejected: %v", err)
	}
	cases := []struct {
		name  string
		tasks []FuncTask
		errIn string
	}{
		{"main reserved", []FuncTask{{Name: "main"}}, `function "main" is reserved`},
		{"hire_main reserved", []FuncTask{{Name: "hire_main"}}, `function "hire_main" is reserved`},
		{"overload", []FuncTask{{Name: "f"}, {Name: "f"}}, `function "f" declared twice`},
		{"unnamed param", []FuncTask{{Name: "f", Params: []FuncParam{{CxxType: "int"}}}}, `parameter 1 has no name`},
		{"bad type", []FuncTask{{Name: "f", Params: []FuncParam{{Name: "x", CxxType: "unsigned int"}}}}, `parameter "x": unsupported type "unsigned int"`},
		{"computed default", []FuncTask{{Name: "f", Params: []FuncParam{{Name: "x", CxxType: "int", HasDefault: true}}}}, `parameter "x": default value is not a simple literal`},
		{"string param with int default", []FuncTask{{Name: "f", Params: []FuncParam{{Name: "x", CxxType: "std::string", HasDefault: true, Default: "5", litKind: "IntegerLiteral"}}}}, `not a simple literal`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateFuncTasks(path, c.tasks)
			if err == nil || !strings.Contains(err.Error(), c.errIn) {
				t.Fatalf("validate error = %v, want one containing %q", err, c.errIn)
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error %q must name the file %q", err, path)
			}
		})
	}
}

// TestGenWrapper pins the generated wrapper's shape: the SDK include,
// the user file by absolute (slashed) path so diagnostics keep the
// user's real file:line, and the registration chain per task.
func TestGenWrapper(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "Taskfile.cpp")
	if err := os.WriteFile(srcPath, []byte("void hello() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tasks := []FuncTask{{
		Name: "hello", Desc: "say \"hi\"",
		Params: []FuncParam{
			// Defaults carry clang's literal spelling: a string default
			// is the C++ source text, quotes included - embeddable as-is.
			{Name: "who", CxxType: "std::string", HasDefault: true, Default: `"hire"`, litKind: "StringLiteral"},
			{Name: "port", CxxType: "int", HasDefault: true, Default: "3", litKind: "IntegerLiteral"},
			{Name: "loud", CxxType: "bool", HasDefault: true, Default: "false", litKind: "CXXBoolLiteralExpr"},
			{Name: "tag", CxxType: "std::string"},
		},
	}}
	wrapper, err := genWrapper(srcPath, dir, tasks)
	if err != nil {
		t.Fatalf("genWrapper: %v", err)
	}
	data, err := os.ReadFile(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{
		"#include \"hire.hpp\"",
		"#include \"" + filepath.ToSlash(srcPath) + "\"",
		"void hire_main(hire::registry& reg) {",
		`reg.task("hello", "say \"hi\"")`,
		`.opt("who", "default: hire")`,
		`.num("port", "default: 3")`,
		`.flag("loud", "default: false")`,
		`.req("tag")`,
		`hello(a.get_string("who", "hire"), a.get_num("port", 3), a.get_bool("loud", false), a.get_string("tag", ""));`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("wrapper missing %q:\n%s", want, got)
		}
	}
	// The SDK header is written beside the wrapper so its include resolves.
	if _, err := os.Stat(filepath.Join(dir, "hire.hpp")); err != nil {
		t.Errorf("hire.hpp not written beside wrapper: %v", err)
	}
	// A no-param task gets a parameterless-named lambda and a bare call.
	tasks2 := []FuncTask{{Name: "ping", Desc: ""}}
	wrapper2, err := genWrapper(srcPath, dir, tasks2)
	if err != nil {
		t.Fatalf("genWrapper (no params): %v", err)
	}
	data2, err := os.ReadFile(wrapper2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data2), "reg.task(\"ping\", \"\")") {
		t.Errorf("no-param registration missing:\n%s", data2)
	}
	if !strings.Contains(string(data2), ".run([](const hire::args&) { ping(); });") {
		t.Errorf("no-param call missing:\n%s", data2)
	}
	// Same source file regenerates to the same wrapper name.
	if again, err := genWrapper(srcPath, dir, tasks); err != nil || again != wrapper {
		t.Errorf("wrapper name not stable: %q vs %q (err %v)", again, wrapper, err)
	}
}
