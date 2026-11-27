package adapter

// Function-form C++ taskfiles: plain functions, no registration, no
// main, no hire includes. hire parses them with the user's clang
// (never bundled - PATH only), generates a small wrapper TU in the
// compile cache that includes hire.hpp (the same contract SDK as the
// legacy hpp form) and the user's file by absolute path, compiles the
// wrapper through the user's build template, and caches the discover
// output beside the exe so unchanged sources reload with zero parse
// and zero exec.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// isContractForm reports whether a C++ source speaks the raw __hire__
// contract itself. The SDK legacy form defines hire_main (and includes
// hire.hpp); the hand-written raw form contains the __hire__ verb
// literal. Function-form files may include hire.hpp for hire::sh but
// never define hire_main, so those two markers are complete and honest.
func isContractForm(src []byte) bool {
	return bytes.Contains(src, []byte("hire_main")) || bytes.Contains(src, []byte("__hire__"))
}

// FuncParam is one extracted function parameter.
type FuncParam struct {
	Name       string `json:"name"`
	CxxType    string `json:"cxx_type"`
	HasDefault bool   `json:"has_default"`
	Default    string `json:"default,omitempty"`
	litKind    string // literal node kind behind Default; "" = no simple literal
}

// FuncTask is one extracted top-level task function.
type FuncTask struct {
	Name   string      `json:"name"`
	Desc   string      `json:"desc"`
	Params []FuncParam `json:"params,omitempty"`

	hasBody bool `json:"-"` // the definition was seen; a bare declaration cannot link
}

// mapCxxType maps a C++ parameter spelling to a hire arg type. The
// exact-match set keeps the contract honest; anything else errors at
// load, naming the function and parameter.
func mapCxxType(cxx string) (string, error) {
	switch strings.TrimSpace(cxx) {
	case "std::string", "string":
		return "string", nil
	case "bool":
		return "bool", nil
	case "int", "long", "long long", "double", "float":
		return "num", nil
	}
	return "", fmt.Errorf("unsupported type %q (use std::string, bool, int, long, long long, double or float)", cxx)
}

// ---------- clang JSON AST parsing ----------

// astNode is the tolerant view of one clang JSON AST node: field
// shapes drift across clang versions, so unknown fields are ignored,
// missing fields degrade, and nodes that fail to decode are skipped.
type astNode struct {
	Kind         string          `json:"kind"`
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	StorageClass string          `json:"storageClass"`
	IsImplicit   bool            `json:"isImplicit"`
	Opcode       string          `json:"opcode"`
	PreviousDecl json.RawMessage `json:"previousDecl"`
	Type         *astType        `json:"type"`
	Init         json.RawMessage `json:"init"`
	Text         string          `json:"text"`
	Value        json.RawMessage `json:"value"`
	Loc          *astLoc         `json:"loc"`
	Inner        []astNode       `json:"inner"`
}

type astType struct {
	QualType string `json:"qualType"`
}

type astLoc struct {
	File string `json:"file"`
}

// astProbe is the shallow peek at one translation-unit child: enough to
// track the current source file (clang prints loc.file only on the
// first location of each file, omitting it afterwards) and to pick
// FunctionDecls for a full decode, without materializing the subtree.
type astProbe struct {
	Kind string  `json:"kind"`
	Loc  *astLoc `json:"loc"`
}

// parseAST extracts the task functions from a clang JSON AST dump. Only
// the translation unit's direct children are examined: static functions
// are excluded by storageClass, explicit-extern declarations (defined
// in another TU) are skipped, header decls by the source-file filter,
// and namespace-nested helpers (anonymous or not) never appear at this
// level at all. Redefinition chains (forward declaration + definition)
// merge into one task via previousDecl; only genuine overloads survive
// as duplicates and fail validation. Each kept node is decoded whole,
// so memory stays bounded by one top-level declaration's subtree (the
// <iostream> NamespaceDecl is buffered whole too) even when system
// headers dominate the dump.
func parseAST(r io.Reader, srcPath string) ([]FuncTask, error) {
	dec := json.NewDecoder(r)
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("clang AST output is not JSON: %v", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("clang AST output is not a JSON object")
	}
	var tasks []FuncTask
	byID := map[string]int{} // clang decl id -> index into tasks
	file := ""               // last file clang printed a location for
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("clang AST output is not valid JSON: %v", err)
		}
		if key, ok := keyTok.(string); !ok || key != "inner" {
			if err := skipJSONValue(dec); err != nil {
				return nil, fmt.Errorf("clang AST output is not valid JSON: %v", err)
			}
			continue
		}
		if _, err := dec.Token(); err != nil { // consume '['
			return nil, fmt.Errorf("clang AST output is not valid JSON: %v", err)
		}
		for dec.More() {
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				return nil, fmt.Errorf("clang AST output is not valid JSON: %v", err)
			}
			var probe astProbe
			if err := json.Unmarshal(raw, &probe); err != nil {
				continue // a drifted node shape degrades to a skip
			}
			if probe.Loc != nil && probe.Loc.File != "" {
				file = probe.Loc.File
			}
			if probe.Kind != "FunctionDecl" {
				continue
			}
			decl, ok, err := funcTaskFromNode(raw, file, srcPath)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			// previousDecl is clang's own proof that this node is the
			// same entity as an earlier declaration, not an overload:
			// fold the chain into one task, keeping the definition's
			// body and the declaration's comment/defaults. Genuine
			// overloads carry no such link, so they stay distinct and
			// validateFuncTasks still rejects the duplicate name.
			if idx, seen := byID[decl.PrevID]; decl.PrevID != "" && seen {
				mergeRedecl(&tasks[idx], decl.Task)
				if decl.ID != "" {
					byID[decl.ID] = idx // keep long chains linkable
				}
				continue
			}
			if decl.ID != "" {
				byID[decl.ID] = len(tasks)
			}
			tasks = append(tasks, decl.Task)
		}
		if _, err := dec.Token(); err != nil { // consume ']'
			return nil, fmt.Errorf("clang AST output is not valid JSON: %v", err)
		}
	}
	return tasks, nil
}

// skipJSONValue consumes one arbitrary JSON value (object, array or
// scalar) from dec, passing over fields the parser ignores.
func skipJSONValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	d, isDelim := tok.(json.Delim)
	if !isDelim {
		return nil // scalar
	}
	for dec.More() {
		if d == '{' {
			if _, err := dec.Token(); err != nil { // member key
				return err
			}
		}
		if err := skipJSONValue(dec); err != nil {
			return err
		}
	}
	_, err = dec.Token() // closing '}' or ']'
	return err
}

// funcDecl is one decoded FunctionDecl candidate plus clang's identity
// links: ID is this declaration's node id, PrevID the id clang stamped
// on its previousDecl (empty when the node starts a new redecl chain).
type funcDecl struct {
	Task   FuncTask
	ID     string
	PrevID string
}

// funcTaskFromNode decodes one top-level FunctionDecl candidate into a
// FuncTask. ok=false with a nil error means "not a task": implicit,
// static or extern functions, or decls belonging to another source file.
func funcTaskFromNode(raw json.RawMessage, file, srcPath string) (funcDecl, bool, error) {
	var fn astNode
	if jerr := json.Unmarshal(raw, &fn); jerr != nil {
		return funcDecl{}, false, nil // drifted shape: skip, not fatal
	}
	if fn.Name == "" || fn.IsImplicit || fn.StorageClass == "static" ||
		fn.StorageClass == "extern" {
		return funcDecl{}, false, nil // extern has no body in this TU: linking would fail
	}
	if fn.Loc != nil && fn.Loc.File != "" {
		file = fn.Loc.File
	}
	if !clangFileMatches(file, srcPath) {
		return funcDecl{}, false, nil
	}
	t := FuncTask{Name: fn.Name}
	for i := range fn.Inner {
		child := &fn.Inner[i]
		switch child.Kind {
		case "FullComment":
			t.Desc = commentDesc(child)
		case "ParmVarDecl":
			t.Params = append(t.Params, funcParam(child))
		case "CompoundStmt":
			t.hasBody = true // a definition; a bare declaration cannot link
		}
	}
	return funcDecl{Task: t, ID: fn.ID, PrevID: previousDeclID(fn.PreviousDecl)}, true, nil
}

// previousDeclID reads clang's previousDecl link. clang 19 emits the id
// as a bare JSON string; tolerate an object with an "id" from other
// versions, and an absent/null link on a first declaration.
func previousDeclID(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var id string
	if json.Unmarshal(raw, &id) == nil {
		return id
	}
	var obj struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.ID
	}
	return ""
}

// mergeRedecl folds a later declaration of the same entity into the
// task extracted from an earlier one. The earlier node usually holds
// the doc comment and default arguments; the later one may hold the
// body. Prefer what is already there so neither source of metadata is
// lost, and remember that a definition has been seen.
func mergeRedecl(dst *FuncTask, src FuncTask) {
	if dst.Desc == "" {
		dst.Desc = src.Desc
	}
	if len(dst.Params) == 0 {
		dst.Params = src.Params
	}
	dst.hasBody = dst.hasBody || src.hasBody
}

// funcParam extracts one parameter: its name, the as-written type, and
// the default value's literal text when the default is a simple
// literal.
func funcParam(n *astNode) FuncParam {
	p := FuncParam{Name: n.Name}
	if n.Type != nil {
		p.CxxType = strings.TrimSpace(n.Type.QualType)
	}
	if len(n.Init) > 0 && string(n.Init) != "null" {
		p.HasDefault = true
		if lit := simpleLiteral(n.Inner); lit != nil {
			p.Default = lit.text
			p.litKind = lit.kind
		}
	}
	return p
}

// literalInfo is the literal at the heart of a default expression.
type literalInfo struct {
	kind string
	text string
}

// simpleLiteral walks a default-expression tree through clang's cast /
// cleanup / temporary / construct wrappers to the literal beneath; a
// unary minus in front of a numeric literal is kept. Anything else
// (binary operators, calls, constants, init lists) is not a simple
// literal and yields nil - validate then rejects the default with a
// clear message rather than passing a wrong value silently.
// shortcut: only literal defaults are reproducible in the wrapper; render from source text when computed defaults matter
func simpleLiteral(nodes []astNode) *literalInfo {
	for i := range nodes {
		n := &nodes[i]
		if len(n.Value) > 0 {
			// clang prints a literal's value as the C++ source text:
			// `"hire"`, `3`, `false` - already a valid C++ expression,
			// embeddable in the wrapper verbatim.
			return &literalInfo{kind: n.Kind, text: renderLiteralValue(n.Value)}
		}
		switch n.Kind {
		case "ExprWithCleanups", "CXXBindTemporaryExpr", "MaterializeTemporaryExpr",
			"ImplicitCastExpr", "CXXFunctionalCastExpr", "CXXConstructExpr",
			"ConstantExpr", "FullExpr", "ParenExpr":
			return simpleLiteral(n.Inner)
		case "UnaryOperator":
			if n.Opcode == "-" {
				if lit := simpleLiteral(n.Inner); lit != nil && numericKind(lit.kind) {
					lit.text = "-" + lit.text
					return lit
				}
			}
			return nil
		default:
			return nil
		}
	}
	return nil
}

func numericKind(kind string) bool {
	return kind == "IntegerLiteral" || kind == "FloatingLiteral"
}

// literalMatchesType checks the literal family against the mapped arg
// type: a string default must be a string literal, a num default an
// integer or float, a bool default a bool (or integer) literal.
func literalMatchesType(kind, hireType string) bool {
	switch hireType {
	case "string":
		return kind == "StringLiteral"
	case "bool":
		return kind == "CXXBoolLiteralExpr" || kind == "IntegerLiteral"
	case "num":
		return numericKind(kind)
	}
	return false
}

// renderLiteralValue turns a clang literal "value" into display text:
// JSON string literals (StringLiteral) are unquoted, numbers and bools
// pass through as written.
func renderLiteralValue(raw json.RawMessage) string {
	s := string(bytes.TrimSpace(raw))
	if strings.HasPrefix(s, `"`) {
		var v string
		if json.Unmarshal(raw, &v) == nil {
			return v
		}
	}
	return s
}

// displayDefault renders a default for the arg desc hint: a string
// literal's C++ source quotes are dropped; everything else shows as
// written.
func displayDefault(p FuncParam) string {
	if p.litKind == "StringLiteral" &&
		len(p.Default) >= 2 && strings.HasPrefix(p.Default, `"`) && strings.HasSuffix(p.Default, `"`) {
		return p.Default[1 : len(p.Default)-1]
	}
	return p.Default
}

// commentDesc reads the desc off a clang FullComment: the first
// TextComment's text - the first line of the comment block immediately
// above the function - with an optional "desc:" prefix stripped and
// surrounding whitespace trimmed. (The exact field was determined
// against local clang 19/20 output: FunctionDecl.inner -> FullComment ->
// ParagraphComment -> TextComment.text.)
func commentDesc(n *astNode) string {
	text := strings.TrimSpace(firstText(n))
	if len(text) >= 5 && strings.EqualFold(text[:5], "desc:") {
		text = strings.TrimSpace(text[5:])
	}
	return text
}

// firstText returns the first TextComment's text in document order.
func firstText(n *astNode) string {
	if n.Kind == "TextComment" {
		return n.Text
	}
	for i := range n.Inner {
		if t := firstText(&n.Inner[i]); t != "" {
			return t
		}
	}
	return ""
}

// clangFileMatches reports whether the clang-reported source file is
// the taskfile hire handed to clang. Clang echoes the spelling it was
// given (or its absolute form), so both spellings are accepted;
// comparison is slash-normalized and case-insensitive on Windows.
func clangFileMatches(reported, srcPath string) bool {
	if reported == "" || srcPath == "" {
		return false
	}
	if normClangPath(reported) == normClangPath(srcPath) {
		return true
	}
	if abs, err := filepath.Abs(srcPath); err == nil {
		return normClangPath(reported) == normClangPath(abs)
	}
	return false
}

func normClangPath(p string) string {
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return filepath.ToSlash(filepath.Clean(p))
}

// ---------- live parse ----------

// cppfuncParseTimeout bounds a clang parse so a pathological include
// cannot hang `hire ls` forever. Task execution stays unbounded; only
// this discovery-time parse is time-limited.
const cppfuncParseTimeout = 60 * time.Second

// parseFuncForm runs the user's clang parser over a function-form
// taskfile and extracts its task functions. The AST stream is decoded
// incrementally, so even header-heavy dumps stay memory-bounded.
// Syntax errors surface clang's own diagnostics, which name the user's
// real file:line.
func parseFuncForm(srcPath, clangCmd string) ([]FuncTask, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cppfuncParseTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, clangCmd, "-Xclang", "-ast-dump=json", "-fsyntax-only", "-fparse-all-comments", srcPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("run clang parser %q: %v", clangCmd, err)
	}
	tasks, perr := parseAST(stdout, srcPath)
	werr := cmd.Wait()
	if werr != nil && ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("clang parser %q timed out after %s", clangCmd, cppfuncParseTimeout)
	}
	if werr != nil {
		var ee *exec.ExitError
		if errors.As(werr, &ee) {
			return nil, fmt.Errorf("clang parse failed (exit %d) (stderr: %s)", ee.ExitCode(), excerpt(stderr.String()))
		}
		return nil, fmt.Errorf("run clang parser %q: %v", clangCmd, werr)
	}
	if perr != nil {
		return nil, perr
	}
	return tasks, nil
}

// resolveCppParser returns the AST parser command for a function-form
// load: the configured parser wins; otherwise PATH probing, clang++
// first, then plain clang (which also drives .cpp as C++). "" = none
// found.
func resolveCppParser(spec RuntimeSpec) string {
	if spec.Parser != "" {
		return spec.Parser
	}
	if _, err := exec.LookPath("clang++"); err == nil {
		return "clang++"
	}
	if _, err := exec.LookPath("clang"); err == nil {
		return "clang"
	}
	return ""
}

// ensureParserFound gives a configured parser a clear up-front error
// instead of an exec failure deep in the parse.
func ensureParserFound(parser string) error {
	if strings.ContainsAny(parser, `/\`) {
		if _, err := os.Stat(parser); err != nil {
			return fmt.Errorf("parser %q not found: %v", parser, err)
		}
		return nil
	}
	if _, err := exec.LookPath(parser); err != nil {
		return fmt.Errorf("parser %q not found in PATH: %v", parser, err)
	}
	return nil
}

// validateFuncTasks enforces the load-time rules for function-form
// taskfiles: the wrapper's own entry points (main, hire_main) must not
// collide, overloads are not representable (one discover schema per
// name), parameters must be named, every parameter type must map, and a
// default must be a literal of the matching family. Every error names
// the file, the function and, where relevant, the parameter.
func validateFuncTasks(path string, tasks []FuncTask) error {
	seen := map[string]bool{}
	for i := range tasks {
		t := &tasks[i]
		if t.Name == "main" || t.Name == "hire_main" {
			return fmt.Errorf("%s: function %q is reserved - the generated wrapper defines it; rename the task function", path, t.Name)
		}
		if seen[t.Name] {
			return fmt.Errorf("%s: function %q declared twice - overloads are not supported", path, t.Name)
		}
		seen[t.Name] = true
		for j := range t.Params {
			p := &t.Params[j]
			if p.Name == "" {
				return fmt.Errorf("%s: function %q: parameter %d has no name - task parameters must be named", path, t.Name, j+1)
			}
			typ, err := mapCxxType(p.CxxType)
			if err != nil {
				return fmt.Errorf("%s: function %q: parameter %q: %v", path, t.Name, p.Name, err)
			}
			if p.HasDefault && !literalMatchesType(p.litKind, typ) {
				return fmt.Errorf("%s: function %q: parameter %q: default value is not a simple literal (use a string, bool or number literal)", path, t.Name, p.Name)
			}
		}
	}
	return nil
}

// ---------- wrapper generation ----------

// cxxStr renders s as a C++ string literal (source-code escaping, the
// static counterpart of hire.hpp's runtime json_str).
func cxxStr(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// genWrapper writes the hire.hpp SDK and the generated wrapper TU into
// outDir - the compile-cache directory, never beside the user's file -
// and returns the wrapper path. The wrapper includes the user's
// taskfile by absolute path so compiler diagnostics report the user's
// real file:line, and defines hire_main registering one task per
// function; main() itself comes from hire.hpp (the reuse-SDK variant).
func genWrapper(srcPath, outDir string, tasks []FuncTask) (string, error) {
	abs, err := filepath.Abs(srcPath)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(outDir, "hire.hpp"), []byte(CppHeaderTemplate), 0o644); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("// hire-generated wrapper for the taskfile included below - do not edit.\n")
	b.WriteString("// Diagnostics below, and the taskfile's own through the include, report real file:line.\n")
	b.WriteString("#include \"hire.hpp\"\n")
	b.WriteString("#include \"" + filepath.ToSlash(abs) + "\"\n\n")
	b.WriteString("void hire_main(hire::registry& reg) {\n")
	for i := range tasks {
		if err := writeTaskRegistration(&b, &tasks[i]); err != nil {
			return "", err
		}
	}
	b.WriteString("}\n")
	sum := sha256.Sum256([]byte(abs))
	wrapper := filepath.Join(outDir, hex.EncodeToString(sum[:8])+".wrapper.cpp")
	if err := os.WriteFile(wrapper, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	return wrapper, nil
}

// writeTaskRegistration emits one task's registration chain: the arg
// declarations (a default rides into the arg desc as "default: x")
// and the run lambda converting HIRE_ARGS to typed C++ arguments and
// calling the user's function directly.
func writeTaskRegistration(b *strings.Builder, t *FuncTask) error {
	b.WriteString("    reg.task(" + cxxStr(t.Name) + ", " + cxxStr(t.Desc) + ")")
	callArgs := make([]string, 0, len(t.Params))
	for i := range t.Params {
		p := &t.Params[i]
		typ, err := mapCxxType(p.CxxType)
		if err != nil {
			return err // unreachable: validateFuncTasks ran first
		}
		desc := ""
		if p.HasDefault && p.Default != "" {
			desc = "default: " + displayDefault(*p)
		}
		chain := func(call string) {
			if desc != "" {
				b.WriteString("\n        ." + call + "(" + cxxStr(p.Name) + ", " + cxxStr(desc) + ")")
			} else {
				b.WriteString("\n        ." + call + "(" + cxxStr(p.Name) + ")")
			}
		}
		switch {
		case typ == "string" && !p.HasDefault:
			b.WriteString("\n        .req(" + cxxStr(p.Name) + ")")
		case typ == "string":
			chain("opt")
		case typ == "bool" && p.HasDefault:
			chain("flag")
		case typ == "num" && p.HasDefault:
			chain("num")
		default: // required bool / num
			b.WriteString("\n        .arg(" + cxxStr(p.Name) + ", \"" + typ + "\", true)")
		}
		switch typ {
		case "string":
			// Default is a valid C++ literal expression already: clang's
			// literal "value" is the source text, so it embeds as-is.
			fallback := `""`
			if p.HasDefault {
				fallback = p.Default
			}
			callArgs = append(callArgs, "a.get_string("+cxxStr(p.Name)+", "+fallback+")")
		case "bool":
			fallback := "false"
			if p.HasDefault {
				fallback = p.Default
			}
			callArgs = append(callArgs, "a.get_bool("+cxxStr(p.Name)+", "+fallback+")")
		case "num":
			fallback := "0"
			if p.HasDefault && p.Default != "" {
				fallback = p.Default
			}
			callArgs = append(callArgs, "a.get_num("+cxxStr(p.Name)+", "+fallback+")")
		}
	}
	receiver := "const hire::args&"
	if len(callArgs) > 0 {
		receiver += " a"
	}
	b.WriteString("\n        .run([](" + receiver + ") { " + t.Name + "(" + strings.Join(callArgs, ", ") + "); });\n\n")
	return nil
}

// ---------- load orchestration ----------

// cppfuncCodegenVersion salts the function-form cache key. Bump it when
// wrapper generation changes incompatibly, so a stale exe built by an
// older generator can never be reused. The raw-contract path keeps the
// plain cacheKey(src, build) identity.
const cppfuncCodegenVersion = 1

// cppfuncCacheKey is the function-form cache identity: the raw key plus
// the wrapper codegen inputs (the SDK header bytes and the codegen
// version). A change to either yields a fresh key and a full reload.
func cppfuncCacheKey(src []byte, build string) string {
	return cppfuncKeyWith(src, build, CppHeaderTemplate, cppfuncCodegenVersion)
}

// cppfuncKeyWith is the hashing core, taking the codegen inputs explicitly
// so tests can pin that both the header and the version salt change the key.
func cppfuncKeyWith(src []byte, build, header string, version int) string {
	h := sha256.New()
	h.Write(src)
	h.Write([]byte{0})
	h.Write([]byte(build))
	h.Write([]byte{0})
	h.Write([]byte(header))
	h.Write([]byte{0})
	h.Write([]byte(strconv.Itoa(version)))
	return hex.EncodeToString(h.Sum(nil))
}

// loadCppFuncForm loads a function-form C++ taskfile end to end. Fast
// path: a cached discover payload next to a built exe reloads with zero
// parse, zero exec and zero rebuild. Slow path: parse with the user's
// clang, validate, generate the wrapper, compile it through the build
// template ({src} = the wrapper, not the user file), discover once
// through the built binary, and cache the discover output beside the
// exe as <cppfuncCacheKey>.tasks.json.
func loadCppFuncForm(path, rtName string, spec RuntimeSpec, src []byte) (*File, error) {
	base, err := cacheBaseDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(base, "hire", rtName)
	key := cppfuncCacheKey(src, spec.Build)
	tasksFile := filepath.Join(dir, key+".tasks.json")
	exe := filepath.Join(dir, key+".exe")

	// Fast path: both cache artifacts present and the payload still
	// validates - anything off falls through to a full reload.
	if data, rerr := os.ReadFile(tasksFile); rerr == nil {
		if st, serr := os.Stat(exe); serr == nil && !st.IsDir() {
			if f, ferr := fileFromDiscoverJSON(path, data, "", exe, nil); ferr == nil {
				return f, nil
			}
		}
	}

	parser := resolveCppParser(spec)
	if parser == "" {
		return nil, fmt.Errorf("%s: this function-form C++ taskfile needs clang for parsing, and no clang++/clang is on PATH - install clang, point runtimes.cpp at parser = \"clang++\" in config.lua, or write the legacy hire.hpp contract form (docs/contract.md)", path)
	}
	if perr := ensureParserFound(parser); perr != nil {
		return nil, fmt.Errorf("%s: configured %v", path, perr)
	}
	tasks, err := parseFuncForm(path, parser)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("%s: no task functions found - top-level non-static functions are tasks; static and namespace-nested functions are helpers", path)
	}
	if verr := validateFuncTasks(path, tasks); verr != nil {
		return nil, verr
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	wrapper, err := genWrapper(path, dir, tasks)
	if err != nil {
		return nil, err
	}
	if _, err := buildIfMissing(exe, wrapper, spec.Build, path); err != nil {
		return nil, err
	}
	stdout, stderr, err := runDiscover(exe, nil)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, discoverErr(path, fmt.Sprintf("__hire__ discover failed (exit %d)", ee.ExitCode()), stderr)
		}
		return nil, fmt.Errorf("%s: __hire__ discover: %v", path, err)
	}
	f, err := fileFromDiscoverJSON(path, stdout, stderr, exe, nil)
	if err != nil {
		return nil, err
	}
	// Cache only after validation, so a poisoned or partial payload can
	// never poison the next load.
	if werr := os.WriteFile(tasksFile, stdout, 0o644); werr != nil {
		return nil, werr
	}
	return f, nil
}
