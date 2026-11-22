package argparse

import (
	"strings"
	"testing"

	lua "github.com/yuin/gopher-lua"
)

func TestParseHeuristic(t *testing.T) {
	p, err := Parse([]string{"--tag", "v0.1.2", "--force", "file.txt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Flags["tag"] != "v0.1.2" {
		t.Errorf("tag = %v", p.Flags["tag"])
	}
	if p.Flags["force"] != "file.txt" {
		t.Errorf("force = %v, want file.txt (heuristic consumes next non-dash token)", p.Flags["force"])
	}
	if len(p.Positional) != 0 {
		t.Errorf("Positional = %v, want empty", p.Positional)
	}
}

func TestParseHeuristicBoolThenSeparator(t *testing.T) {
	p, err := Parse([]string{"--tag", "v0.1.2", "--force", "--", "file.txt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Flags["force"] != true {
		t.Errorf("force = %v, want true", p.Flags["force"])
	}
	if len(p.Positional) != 1 || p.Positional[0] != "file.txt" {
		t.Errorf("Positional = %v, want [file.txt]", p.Positional)
	}
}

func TestParseEqualsForm(t *testing.T) {
	p, err := Parse([]string{"--tag=v0.2.0"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Flags["tag"] != "v0.2.0" {
		t.Errorf("tag = %v", p.Flags["tag"])
	}
}

func TestParseDoubleDashSeparator(t *testing.T) {
	p, err := Parse([]string{"--force", "--", "--tag", "x"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Flags["force"] != true {
		t.Errorf("force = %v", p.Flags["force"])
	}
	if len(p.Positional) != 2 || p.Positional[0] != "--tag" || p.Positional[1] != "x" {
		t.Errorf("Positional = %v", p.Positional)
	}
}

func TestParseStrictStringFlag(t *testing.T) {
	specs := map[string]Spec{"tag": {Type: "string"}}
	p, err := Parse([]string{"--tag", "v1"}, specs)
	if err != nil {
		t.Fatal(err)
	}
	if p.Flags["tag"] != "v1" {
		t.Errorf("tag = %v", p.Flags["tag"])
	}
}

func TestParseStrictStringFlagMissingValue(t *testing.T) {
	specs := map[string]Spec{"tag": {Type: "string"}}
	_, err := Parse([]string{"--tag"}, specs)
	if err == nil || !strings.Contains(err.Error(), "requires a value") {
		t.Errorf("expected 'requires a value' error, got %v", err)
	}
}

func TestParseStrictBoolFlag(t *testing.T) {
	specs := map[string]Spec{"force": {Type: "bool"}}
	p, err := Parse([]string{"--force"}, specs)
	if err != nil {
		t.Fatal(err)
	}
	if p.Flags["force"] != true {
		t.Errorf("force = %v", p.Flags["force"])
	}
}

func TestParseStrictBoolFlagEquals(t *testing.T) {
	specs := map[string]Spec{"force": {Type: "bool"}}
	p, err := Parse([]string{"--force=false"}, specs)
	if err != nil {
		t.Fatal(err)
	}
	if p.Flags["force"] != false {
		t.Errorf("force = %v", p.Flags["force"])
	}
}

func TestParseStrictUnknownFlag(t *testing.T) {
	specs := map[string]Spec{"tag": {Type: "string"}}
	_, err := Parse([]string{"--unknown"}, specs)
	if err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("expected 'unknown flag' error, got %v", err)
	}
	if !strings.Contains(err.Error(), "allowed") || !strings.Contains(err.Error(), "--tag") {
		t.Errorf("error should list allowed flags, got %q", err.Error())
	}
}

func TestParseRequiredMissing(t *testing.T) {
	specs := map[string]Spec{"tag": {Type: "string", Required: true, Desc: "version tag"}}
	_, err := Parse([]string{}, specs)
	if err == nil || !strings.Contains(err.Error(), "missing required") {
		t.Errorf("expected 'missing required' error, got %v", err)
	}
}

func TestParseStrictNumFlag(t *testing.T) {
	specs := map[string]Spec{"port": {Type: "num"}}
	p, err := Parse([]string{"--port", "8080"}, specs)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := p.Flags["port"].(float64); !ok || got != 8080 {
		t.Errorf("port = %v (%T), want float64 8080", p.Flags["port"], p.Flags["port"])
	}
}

func TestParseStrictNumFlagEquals(t *testing.T) {
	specs := map[string]Spec{"port": {Type: "num"}}
	p, err := Parse([]string{"--port=8080.5"}, specs)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := p.Flags["port"].(float64); !ok || got != 8080.5 {
		t.Errorf("port = %v (%T), want float64 8080.5", p.Flags["port"], p.Flags["port"])
	}
}

func TestParseStrictNumFlagInvalid(t *testing.T) {
	specs := map[string]Spec{"port": {Type: "num", Desc: "listen port"}}
	_, err := Parse([]string{"--port", "abc"}, specs)
	if err == nil || !strings.Contains(err.Error(), "want num") || !strings.Contains(err.Error(), "port") {
		t.Errorf("expected 'want num' error naming the flag, got %v", err)
	}
	_, err = Parse([]string{"--port=abc"}, specs)
	if err == nil || !strings.Contains(err.Error(), "want num") {
		t.Errorf("equals form should fail the same way, got %v", err)
	}
}

func TestParseStrictNumFlagMissingValue(t *testing.T) {
	specs := map[string]Spec{"port": {Type: "num"}}
	_, err := Parse([]string{"--port"}, specs)
	if err == nil || !strings.Contains(err.Error(), "requires a value") {
		t.Errorf("expected 'requires a value' error, got %v", err)
	}
}

func TestParseNumRequiredMissing(t *testing.T) {
	specs := map[string]Spec{"port": {Type: "num", Required: true, Desc: "listen port"}}
	_, err := Parse([]string{}, specs)
	if err == nil || !strings.Contains(err.Error(), "missing required") {
		t.Errorf("expected 'missing required' error, got %v", err)
	}
}

func TestToLuaTableNum(t *testing.T) {
	L := lua.NewState()
	defer L.Close()
	p := &Parsed{Flags: map[string]any{"port": 8080.0}}
	tbl := ToLuaTable(L, p)
	if got, ok := tbl.RawGetString("port").(lua.LNumber); !ok || float64(got) != 8080 {
		t.Errorf("port = %v (%T), want lua.LNumber 8080", tbl.RawGetString("port"), tbl.RawGetString("port"))
	}
	// The Lua-side view the brief pins: numbers must arrive as number,
	// not as a stringified value.
	L.SetGlobal("args", tbl)
	if err := L.DoString(`if type(args.port) ~= "number" then error("port is " .. type(args.port)) end`); err != nil {
		t.Errorf("Lua-side type check: %v", err)
	}
}

func TestToLuaTable(t *testing.T) {
	L := lua.NewState()
	defer L.Close()
	p := &Parsed{
		Flags:      map[string]any{"tag": "v1", "force": true},
		Positional: []string{"a", "b"},
	}
	tbl := ToLuaTable(L, p)
	if tbl.RawGetString("tag").String() != "v1" {
		t.Errorf("tag = %v", tbl.RawGetString("tag"))
	}
	if tbl.RawGetString("force") != lua.LTrue {
		t.Errorf("force = %v", tbl.RawGetString("force"))
	}
	pos, ok := tbl.RawGetString("_").(*lua.LTable)
	if !ok {
		t.Fatal("positional _ is not a table")
	}
	if pos.RawGetInt(1).String() != "a" || pos.RawGetInt(2).String() != "b" {
		t.Errorf("positional = %v %v", pos.RawGetInt(1), pos.RawGetInt(2))
	}
}
