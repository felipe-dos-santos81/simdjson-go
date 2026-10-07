package simdjson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// testdataDir returns testdata/<sub>, failing (never skipping) when the
// corpora have not been fetched.
func testdataDir(t testing.TB, sub string) string {
	t.Helper()
	dir := filepath.Join("testdata", sub)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("%s missing: run scripts/fetch-testdata.sh", dir)
	}
	return dir
}

// readTestdata returns the contents of testdata/<sub>/<name>.
func readTestdata(t testing.TB, sub, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(testdataDir(t, sub), name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// checkCorpus parses every testdata/<sub>/*.json: names starting with ok must
// parse, names starting with bad must fail, others (and EXCLUDE files) are ignored.
func checkCorpus(t *testing.T, sub, ok, bad string) {
	files, err := filepath.Glob(filepath.Join(testdataDir(t, sub), "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no files")
	}
	var p Parser
	for _, f := range files {
		name := filepath.Base(f)
		if strings.Contains(name, "EXCLUDE") {
			continue
		}
		_, err := p.Parse(readTestdata(t, sub, name))
		switch {
		case strings.HasPrefix(name, ok) && err != nil:
			t.Errorf("%s: %v", name, err)
		case strings.HasPrefix(name, bad) && err == nil:
			t.Errorf("%s: parsed, want an error", name)
		}
	}
}

// C++ tests/dom/jsoncheck.cpp
func TestJSONChecker(t *testing.T) { checkCorpus(t, "jsonchecker", "pass", "fail") }

// C++ tests/dom/minefieldcheck.cpp (JSONTestSuite): y_ must parse, n_ must fail, i_ is ignored.
func TestMinefield(t *testing.T) { checkCorpus(t, "jsonchecker/minefield", "y_", "n_") }

// TestExamples parses every jsonexamples file and checks it against
// encoding/json, the AppendJSON round trip and the Minify round trip
// (C++ numberparsingcheck, stringparsingcheck and minify_tests).
func TestExamples(t *testing.T) {
	var files []string
	if err := filepath.WalkDir(testdataDir(t, "jsonexamples"), func(path string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(path, ".json") {
			files = append(files, path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no files")
	}
	var p, q Parser
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := p.Parse(data)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if diff := sameAsStdlib(doc.Root(), data); diff != "" {
			t.Errorf("%s: differs from encoding/json: %s", f, diff)
		}
		if diff := roundTripDiff(&q, doc.Root(), data); diff != "" {
			t.Errorf("%s: %s", f, diff)
		}
		if diff := minifyDiff(&q, doc.Root(), data); diff != "" {
			t.Errorf("%s: %s", f, diff)
		}
	}
}

// roundTripDiff reports how Parse(AppendJSON(e)) differs from data (spec §6:
// it must be an equal tree, and stable), or "". q is a scratch Parser.
func roundTripDiff(q *Parser, e Element, data []byte) string {
	out := e.AppendJSON(nil)
	again, err := q.Parse(out)
	switch {
	case err != nil:
		return fmt.Sprintf("AppendJSON output %.80q does not parse: %v", out, err)
	case !bytes.Equal(again.Root().AppendJSON(nil), out):
		return fmt.Sprintf("AppendJSON output %.80q is not stable", out)
	}
	if diff := sameAsStdlib(again.Root(), data); diff != "" {
		return "AppendJSON changed the value: " + diff
	}
	return ""
}

// minifyDiff reports how Minify(data) parses differently from e, or "".
func minifyDiff(q *Parser, e Element, data []byte) string {
	min, err := Minify(nil, data)
	if err != nil {
		return fmt.Sprintf("Minify: %v", err)
	}
	m, err := q.Parse(min)
	if err != nil || !bytes.Equal(m.Root().AppendJSON(nil), e.AppendJSON(nil)) {
		return fmt.Sprintf("Minify output %.80q changed the document (%v)", min, err)
	}
	return ""
}

// sameAsStdlib decodes data with encoding/json (UseNumber) and returns a
// description of the first difference from e, or "".
func sameAsStdlib(e Element, data []byte) string {
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, bom)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "encoding/json: " + err.Error()
	}
	return sameTree(e, v)
}

func sameTree(e Element, v any) string {
	switch v := v.(type) {
	case map[string]any:
		o, err := e.Object()
		if err != nil {
			return "want object, have " + e.Type().String()
		}
		last := map[string]Element{} // encoding/json keeps the last duplicate key
		for k, ev := range o.All() {
			last[k] = ev
		}
		if len(last) != len(v) {
			return "field count"
		}
		for k, ev := range last {
			if _, ok := v[k]; !ok {
				return "unexpected key " + strconv.Quote(k)
			}
			if d := sameTree(ev, v[k]); d != "" {
				return strconv.Quote(k) + ": " + d
			}
		}
	case []any:
		a, err := e.Array()
		if err != nil {
			return "want array, have " + e.Type().String()
		}
		if a.Len() != len(v) {
			return "array length"
		}
		for i, ev := range a.All() {
			if d := sameTree(ev, v[i]); d != "" {
				return strconv.Itoa(i) + ": " + d
			}
		}
	case string:
		if s, err := e.StringValue(); err != nil || s != v {
			return "string " + strconv.Quote(v)
		}
	case bool:
		if b, err := e.Bool(); err != nil || b != v {
			return "bool"
		}
	case nil:
		if !e.IsNull() {
			return "want null"
		}
	case json.Number:
		var ok bool
		switch e.Type() {
		case TypeInt64:
			got, _ := e.Int64()
			want, err := strconv.ParseInt(string(v), 10, 64)
			ok = err == nil && got == want
		case TypeUint64:
			got, _ := e.Uint64()
			want, err := strconv.ParseUint(string(v), 10, 64)
			ok = err == nil && got == want
		case TypeFloat64:
			got, _ := e.Float64()
			want, err := strconv.ParseFloat(string(v), 64)
			ok = err == nil && math.Float64bits(got) == math.Float64bits(want)
		}
		if !ok {
			return "number " + string(v) + " parsed as " + string(e.AppendJSON(nil))
		}
	}
	return ""
}
