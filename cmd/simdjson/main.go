// Command simdjson validates, minifies and queries JSON with simdjson-go.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"simdjson-go"
)

// version is set by the release build: -ldflags "-X main.version=1.2.3".
var version = "dev"

const usage = `simdjson %s: validate, minify and query JSON.

Usage:
  simdjson validate [file]        exit 0 if the input is valid JSON
  simdjson minify [file]          print the input without white space (not validated)
  simdjson get <pointer> [file]   print the value at an RFC 6901 JSON Pointer, minified
  simdjson version                print the version
  simdjson help                   print this text

The input is one JSON document, read from file or, without one, from standard
input. A pointer names a field or an array index at each step: /user/ids/0.

Exit status: 0 on success; 1 for invalid JSON, a pointer that finds nothing or
a file that cannot be read; 2 for a usage error.

https://github.com/felipe-dos-santos81/simdjson-go
`

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	misuse := func() int {
		fmt.Fprintf(stderr, usage, version)
		return 2
	}
	if len(args) == 0 {
		return misuse()
	}
	cmd, args := args[0], args[1:]
	var ptr string
	switch cmd {
	case "version":
		fmt.Fprintln(stdout, version)
		return 0
	case "help", "-h", "--help":
		fmt.Fprintf(stdout, usage, version)
		return 0
	case "get":
		if len(args) == 0 {
			return misuse()
		}
		ptr, args = args[0], args[1:]
	case "validate", "minify":
	default:
		return misuse()
	}
	if len(args) > 1 {
		return misuse()
	}

	out, err := process(cmd, ptr, args, stdin)
	if err == nil && cmd != "validate" {
		_, err = stdout.Write(append(out, '\n'))
	}
	if err != nil {
		// The library's errors already start with "simdjson: ".
		fmt.Fprintln(stderr, "simdjson:", strings.TrimPrefix(err.Error(), "simdjson: "))
		return 1
	}
	return 0
}

// process reads the input named by args (a file, or stdin) and returns what
// cmd prints for it.
func process(cmd, ptr string, args []string, stdin io.Reader) ([]byte, error) {
	var in []byte
	var err error
	if len(args) == 1 {
		in, err = os.ReadFile(args[0])
	} else {
		in, err = io.ReadAll(stdin)
	}
	if err != nil {
		return nil, err
	}
	if cmd == "minify" {
		return simdjson.Minify(nil, in)
	}
	var p simdjson.Parser
	doc, err := p.Parse(in)
	if err != nil || cmd == "validate" {
		return nil, err
	}
	e, err := doc.Root().AtPointer(ptr)
	if err != nil {
		return nil, err
	}
	return e.AppendJSON(nil), nil
}
