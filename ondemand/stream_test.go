package ondemand_test

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"simdjson-go"
	"simdjson-go/ondemand"
)

var errNames = map[error]string{
	simdjson.ErrTrailingContent: "trailing", simdjson.ErrTape: "tape", simdjson.ErrUTF8: "utf8",
	simdjson.ErrUnescapedChars: "ctrl", simdjson.ErrOutOfOrderIteration: "order",
	simdjson.ErrCapacity: "capacity", simdjson.ErrIncompleteArrayOrObject: "incomplete",
}

func itemErr(err error) string {
	var se *ondemand.StreamError
	if !errors.As(err, &se) {
		return "not a *StreamError: " + err.Error()
	}
	name := se.Err.Error()
	for e, n := range errNames {
		if errors.Is(err, e) {
			name = n
		}
	}
	return fmt.Sprintf("!%d %s", se.Offset, name)
}

// odItems lists IterateMany's items as "offset:source" without reading the
// documents, and an error as "!offset name".
func odItems(p *ondemand.Parser, in []byte, f ondemand.Format) string {
	var out []string
	for doc, err := range p.IterateMany(in, f) {
		if err != nil {
			out = append(out, itemErr(err))
			continue
		}
		out = append(out, fmt.Sprintf("%d:%s", doc.Offset(), doc.Source()))
	}
	return strings.Join(out, " ")
}

// domItems is odItems for ParseMany.
func domItems(p *simdjson.Parser, in []byte, f simdjson.Format) string {
	var out []string
	for doc, err := range p.ParseMany(in, f) {
		if err != nil {
			out = append(out, itemErr(err))
			continue
		}
		out = append(out, fmt.Sprintf("%d:%s", doc.Offset(), doc.Source()))
	}
	return strings.Join(out, " ")
}

// manyCases is a copy of the root package's: both APIs yield the same.
var manyCases = []struct {
	in   string
	f    ondemand.Format
	want string
}{
	{"1 2 34", ondemand.Whitespace, "0:1 2:2 4:34"},
	{`[1,23] "lone string" {"key":"unfinished value}`, ondemand.Whitespace, `0:[1,23] 7:"lone string" !21 trailing`},
	{" 1111 }", ondemand.Whitespace, "!1 trailing"},
	{`{"a":1},{"b":`, ondemand.CommaDelimited, `0:{"a":1} !8 trailing`},
	{`1,2,"abc`, ondemand.CommaDelimited, "0:1 2:2 !4 trailing"},
	{"\x1e1\n\x1e2\n\x1e \"abc", ondemand.JSONSequence, "1:1 4:2 !8 trailing"},
	{"\x1e\x1e1\n\x1e\x1e\x1e2\n\x1e", ondemand.JSONSequence, "2:1 7:2"},
	{`,1,,2,,"x",,`, ondemand.CommaDelimited, `1:1 4:2 7:"x"`},
	{"\ufeff[1] [2]", ondemand.Whitespace, "3:[1] 7:[2]"},
	{`  [ {"a":1} , 2 ]  `, ondemand.CommaDelimitedArray, `4:{"a":1} 14:2`},
	{"[]", ondemand.CommaDelimitedArray, ""},
	{"{}", ondemand.CommaDelimitedArray, "!0 tape"},
	{"1 2 \xff 3", ondemand.Whitespace, "0:1 2:2 !4 utf8"},
	{"{\"a\":\"\x01\"} 1", ondemand.Whitespace, "!0 ctrl"},
	{"[1] [2", ondemand.Whitespace, "0:[1] !4 trailing"},
	{"1 2 \xc3", ondemand.Whitespace, "0:1 2:2 !4 utf8"},
	{"", ondemand.Whitespace, ""},
	{" \n ", ondemand.NewlineDelimited, ""},
	{"{\"a\":1}\r\n{\"a\":2}\r\n", ondemand.NewlineDelimited, `0:{"a":1} 9:{"a":2}`},
	{"{\"a\":1}\r\n{\"a\":2}", ondemand.NewlineDelimited, `0:{"a":1} 9:{"a":2}`},
	{"true  {  ", ondemand.Whitespace, "0:true !6 trailing"},
}

func TestIterateMany(t *testing.T) {
	var p ondemand.Parser
	for _, c := range manyCases {
		if got := odItems(&p, []byte(c.in), c.f); got != c.want {
			t.Errorf("%q (format %d):\n got  %s\n want %s", c.in, c.f, got, c.want)
		}
	}
	// On-Demand only: the unbalanced document is yielded; skipping it fails.
	if got := odItems(&p, []byte("[1,23 [1,23]"), ondemand.Whitespace); got != "0:[1,23 [1,23] !0 incomplete" {
		t.Errorf("[1,23 [1,23]: %s", got)
	}
}

// A document still open at the end of a CommaDelimitedArray's contents reads
// the array's own ']' there, as C++ (its sentinel structural_indexes[n] = len
// points at the byte after its input, which is that ']').
func TestIterateManyArrayEnd(t *testing.T) {
	var p ondemand.Parser
	var got []string
	for doc, err := range p.IterateMany([]byte("[[1 2]]"), ondemand.CommaDelimitedArray) {
		if err != nil {
			got = append(got, itemErr(err))
			continue
		}
		var b strings.Builder
		if err := walk(doc, &b); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%d:%s:%s", doc.Offset(), doc.Source(), b.String()))
	}
	if s := strings.Join(got, " "); s != "1:[1 2]]:[i1,] !4 trailing" {
		t.Errorf("got %s", s)
	}
	if got := odItems(&p, []byte(`[{},nul,[1 2]]`), ondemand.CommaDelimitedArray); got != "1:{} 4:nul 8:[1 2]] !8 incomplete" {
		t.Errorf("unread: %s", got)
	}
}

func TestIterateManyReads(t *testing.T) {
	var p ondemand.Parser
	var got []string
	for doc, err := range p.IterateMany([]byte("{\"a\":1,\"b\":2}\n{\"a\":3}\n7"), ondemand.NewlineDelimited) {
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		if err := walk(doc, &b); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%d:%s:%v", doc.Offset(), b.String(), doc.AtEnd()))
	}
	if s := strings.Join(got, " "); s != "0:{a:i1,b:i2,}:true 14:{a:i3,}:true 22:i7:true" {
		t.Errorf("got %s", s)
	}
	// Partly read documents: the rest is skipped to the next newline.
	got = got[:0]
	for doc, err := range p.IterateMany([]byte("{\"a\":1,\"b\":2}\n{\"a\":3,\"b\":4}"), ondemand.NewlineDelimited) {
		if err != nil {
			t.Fatal(err)
		}
		v := must(doc.FindNext("a"))
		got = append(got, fmt.Sprint(must(v.Int64()), doc.AtEnd()))
	}
	if s := strings.Join(got, " "); s != "1 false 3 false" {
		t.Errorf("got %s", s)
	}
}

func TestIterateManyJumpedBytes(t *testing.T) {
	var p ondemand.Parser
	for _, c := range []struct{ in, want string }{
		{"1 \xff\n", "0:1 !2 utf8"},
		{"[1] [2 \xff]\n3", "0:[1] !4 utf8"},
	} {
		if got := odItems(&p, []byte(c.in), ondemand.NewlineDelimited); got != c.want {
			t.Errorf("%q unread: got %s, want %s", c.in, got, c.want)
		}
	}
	// Read, the same.
	var out []string
	for doc, err := range p.IterateMany([]byte("1 \xff\n"), ondemand.NewlineDelimited) {
		if err != nil {
			out = append(out, itemErr(err))
			continue
		}
		_ = must(doc.Int64())
		out = append(out, fmt.Sprintf("%d:%s", doc.Offset(), doc.Source()))
	}
	if s := strings.Join(out, " "); s != "0:1 !2 utf8" {
		t.Errorf("read: got %s", s)
	}
}

func TestIterateManyBatchSizes(t *testing.T) {
	var sb strings.Builder
	for i := range 3000 {
		fmt.Fprintf(&sb, "{\"id\":%d,\"s\":\"ü\\n\"}\n", i)
	}
	sb.WriteString("[" + strings.Repeat("1,", 20000) + "1]\n")
	in := []byte(sb.String())
	var p ondemand.Parser
	want := odItems(&p, in, ondemand.Whitespace)
	var dp simdjson.Parser
	if dom := domItems(&dp, in, simdjson.Whitespace); dom != want {
		t.Fatal("IterateMany and ParseMany differ")
	}
	for _, bs := range []int{64, 100, 4096} {
		p.BatchSize = bs
		if got := odItems(&p, in, ondemand.Whitespace); got != want {
			t.Fatalf("BatchSize %d differs", bs)
		}
	}
	// A root starting with a closer: C++'s Source walk runs to the batch end.
	probe := []byte("] ] " + strings.Repeat("1 ", 200))
	p.BatchSize = 0
	want = odItems(&p, probe, ondemand.Whitespace)
	for _, bs := range []int{64, 128} {
		p.BatchSize = bs
		if got := odItems(&p, probe, ondemand.Whitespace); got != want {
			t.Fatalf("closer root, BatchSize %d differs:\n got  %.60s\n want %.60s", bs, got, want)
		}
	}
}

func TestIterateManyLifecycle(t *testing.T) {
	in := []byte("[1] [2] [3]")
	var p ondemand.Parser
	for range p.IterateMany(in, ondemand.Whitespace) {
		break
	}
	if got := odItems(&p, in, ondemand.Whitespace); got != "0:[1] 4:[2] 8:[3]" {
		t.Fatalf("after a break: %s", got)
	}
	var got []string // Iterate inside the loop: the stream carries on from the root
	for doc, err := range p.IterateMany(in, ondemand.Whitespace) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(doc.Source()))
		if _, err := p.Iterate([]byte(`{"x":1}`)); err != nil {
			t.Fatal(err)
		}
	}
	if s := strings.Join(got, " "); s != "[1] [2] [3]" {
		t.Fatalf("with Iterate inside: %s", s)
	}
	var errs []error
	for _, err := range p.IterateMany(in, ondemand.Whitespace) {
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for range p.IterateMany(in, ondemand.Whitespace) {
		}
	}
	if len(errs) != 1 || !errors.Is(errs[0], simdjson.ErrOutOfOrderIteration) {
		t.Fatalf("nested stream: %v", errs)
	}
}

// TestStreamStage1Errors: one bad byte in a valid stream ends it at the
// document holding it, after the clean stream's earlier documents, whatever
// the batch size (spec §4.1).
func TestStreamStage1Errors(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	docs := []string{`{"a":"xyz","b":[1,2]}`, `["pq","rs"]`, `"str"`, `{"k":{"m":"vw"}}`}
	var dp simdjson.Parser
	var op ondemand.Parser
	for range 300 {
		parts := make([]string, 2+r.IntN(30))
		for i := range parts {
			parts[i] = docs[r.IntN(len(docs))]
		}
		clean := []byte(strings.Join(parts, "\n"))
		k := r.IntN(len(parts))
		bad, name := "\xff", "utf8"
		if r.IntN(2) == 0 {
			bad, name = "\x01", "ctrl"
		}
		q := strings.IndexByte(parts[k], '"') + 1 // inside the first string
		parts[k] = parts[k][:q] + bad + parts[k][q:]
		in := []byte(strings.Join(parts, "\n"))
		off := len(strings.Join(parts[:k], "\n"))
		if k > 0 {
			off++
		}
		want := strings.Fields(domItems(&dp, clean, simdjson.Whitespace))[:k]
		want = append(want, fmt.Sprintf("!%d %s", off, name))
		for _, bs := range []int{64, 128, 0} {
			dp.BatchSize, op.BatchSize = bs, bs
			if got := domItems(&dp, in, simdjson.Whitespace); got != strings.Join(want, " ") {
				t.Fatalf("DOM, BatchSize %d, %q:\n got  %s\n want %s", bs, in, got, strings.Join(want, " "))
			}
			if got := odItems(&op, in, ondemand.Whitespace); got != strings.Join(want, " ") {
				t.Fatalf("On-Demand, BatchSize %d, %q:\n got  %s\n want %s", bs, in, got, strings.Join(want, " "))
			}
		}
		dp.BatchSize, op.BatchSize = 0, 0
	}
}
