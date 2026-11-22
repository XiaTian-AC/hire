// cppfunc-sample.cpp - fixture source for cppfunc_test.go. The committed
// cppfunc-ast.json is this file's real clang JSON AST output, captured
// once with (from this directory):
//
//	clang++ -Xclang -ast-dump=json -fsyntax-only -fparse-all-comments cppfunc-sample.cpp > cppfunc-ast.json
//
// Regenerate when the fixture cases change; field shapes are pinned per
// spec §11 by whatever clang actually printed.
#include "cppfunc-stub.h"

// desc: say hello
void hello(std::string name = "hire") {}

// desc: 打个招呼
void greet_cn(std::string who) {}

// desc: compile the app
// only the first comment line is the desc
void build(std::string target = "app", int opt = 2, bool verbose = false, double ratio = 1.5) {}

// plain comment line without the prefix
void plain() {}

// orphaned above a blank line, clang decides whether it attaches

void detached() {}

void uncommented() {}

static void helper_one() {}

namespace { void helper_two() {} }
