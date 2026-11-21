// Command fakeinterp is a test double for the __hire__ contract (see
// internal/adapter/exec_test.go). It plays both an interpreter (the
// python/node role) and a compiled taskfile binary, so contract tests
// run on machines without external toolchains. FAKE_MODE selects the
// error-path behaviors; by default discover lists echo/boom, run echo
// writes HIRE_ARGS to the HIRE_WRITE file, and run boom exits 3.
package main

import (
	"fmt"
	"os"
)

func main() {
	i := 1
	for i < len(os.Args) && os.Args[i] != "__hire__" {
		i++
	}
	if i >= len(os.Args) {
		fmt.Fprintln(os.Stderr, "fakeinterp: missing __hire__ verb")
		os.Exit(2)
	}
	verb, task := "", ""
	if i+1 < len(os.Args) {
		verb = os.Args[i+1]
	}
	if i+2 < len(os.Args) {
		task = os.Args[i+2]
	}
	switch os.Getenv("FAKE_MODE") {
	case "garbage":
		fmt.Println("this is {{{ not json")
		return
	case "fail":
		fmt.Fprintln(os.Stderr, "fakeinterp: discover exploded")
		os.Exit(7)
	case "reserved":
		fmt.Println(`{"tasks":[{"name":"__secret","desc":"bad name"}]}`)
		return
	case "shellarg":
		fmt.Println(`{"tasks":[{"name":"t","args":{"shell":{"type":"string"}}}]}`)
		return
	}
	switch {
	case verb == "discover":
		fmt.Println(`{"tasks":[` +
			`{"name":"echo","desc":"writes HIRE_ARGS to HIRE_WRITE","args":{"msg":{"type":"string","required":false,"desc":"a message"},"force":{"type":"bool","required":false,"desc":"a flag"}}},` +
			`{"name":"boom","desc":"always fails"}` +
			`]}`)
	case verb == "run" && task == "echo":
		f, err := os.Create(os.Getenv("HIRE_WRITE"))
		if err != nil {
			os.Exit(4)
		}
		defer f.Close()
		f.WriteString(os.Getenv("HIRE_ARGS"))
	case verb == "run" && task == "boom":
		fmt.Fprintln(os.Stderr, "fakeinterp: boom by design")
		os.Exit(3)
	default:
		fmt.Fprintln(os.Stderr, "fakeinterp: unknown verb/task")
		os.Exit(2)
	}
}
