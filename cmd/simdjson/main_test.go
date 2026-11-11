package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRun covers each command once, each exit status, and both input sources.
func TestRun(t *testing.T) {
	file := filepath.Join(t.TempDir(), "in.json")
	if err := os.WriteFile(file, []byte(`{"user": {"ids": [1, 2, 3]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		args  string
		stdin string
		code  int
		out   string
	}{
		{"validate", `{"a":[1,2]}`, 0, ""},
		{"validate", `{"a":`, 1, ""},
		{"minify", "{ \"a\" : [1, 2] }\n", 0, `{"a":[1,2]}` + "\n"},
		{"get /a/1", `{"a":[1,{"b":null}]}`, 0, `{"b":null}` + "\n"},
		{"get /missing", `{"a":1}`, 1, ""},
		{"get /a", `{"a":`, 1, ""},
		{"get /user/ids/2 " + file, "", 0, "3\n"},
		{"validate " + file + ".absent", "", 1, ""},
		{"version", "", 0, version + "\n"},
		{"", "", 2, ""},
		{"get", "", 2, ""},
		{"frobnicate", "", 2, ""},
		{"validate a b", "", 2, ""},
	} {
		var out, errOut bytes.Buffer
		code := run(strings.Fields(c.args), strings.NewReader(c.stdin), &out, &errOut)
		if code != c.code || out.String() != c.out {
			t.Errorf("simdjson %s: exit %d, stdout %q; want exit %d, stdout %q", c.args, code, out.String(), c.code, c.out)
		}
		if (code != 0) != (errOut.Len() > 0) {
			t.Errorf("simdjson %s: exit %d with stderr %q", c.args, code, errOut.String())
		}
	}

	var out, errOut bytes.Buffer
	if code := run([]string{"help"}, nil, &out, &errOut); code != 0 || !strings.Contains(out.String(), "Usage:") || errOut.Len() > 0 {
		t.Errorf("simdjson help: exit %d, stdout %q, stderr %q", code, out.String(), errOut.String())
	}
}
