package simdjson

import (
	"errors"
	"fmt"
	"iter"
	"strings"
	"testing"
	"testing/synctest"
)

var errNames = map[error]string{
	ErrTrailingContent: "trailing", ErrTape: "tape", ErrUTF8: "utf8", ErrUnescapedChars: "ctrl",
	ErrOutOfOrderIteration: "order", ErrCapacity: "capacity", ErrIncompleteArrayOrObject: "incomplete",
	ErrNumber: "number", ErrTAtom: "tatom",
}

// manyItems lists a stream's items as "offset:source", and an error as
// "!offset name". Anything after an error shows up too.
func manyItems(p *Parser, in []byte, f Format) string {
	var out []string
	for doc, err := range p.ParseMany(in, f) {
		if err != nil {
			var se *StreamError
			if !errors.As(err, &se) {
				return "not a *StreamError: " + err.Error()
			}
			name := se.Err.Error()
			for e, n := range errNames {
				if errors.Is(err, e) {
					name = n
				}
			}
			out = append(out, fmt.Sprintf("!%d %s", se.Offset, name))
			continue
		}
		out = append(out, fmt.Sprintf("%d:%s", doc.Offset(), doc.Source()))
	}
	return strings.Join(out, " ")
}

// manyCases hold for ParseMany and IterateMany alike (ondemand/stream_test.go
// has a copy). Most come from C++'s document_stream tests.
var manyCases = []struct {
	in   string
	f    Format
	want string
}{
	{"1 2 34", Whitespace, "0:1 2:2 4:34"},
	{`[1,23] "lone string" {"key":"unfinished value}`, Whitespace, `0:[1,23] 7:"lone string" !21 trailing`},
	{" 1111 }", Whitespace, "!1 trailing"},
	{`{"a":1},{"b":`, CommaDelimited, `0:{"a":1} !8 trailing`},
	{`1,2,"abc`, CommaDelimited, "0:1 2:2 !4 trailing"},
	{"\x1e1\n\x1e2\n\x1e \"abc", JSONSequence, "1:1 4:2 !8 trailing"},
	{"\x1e\x1e1\n\x1e\x1e\x1e2\n\x1e", JSONSequence, "2:1 7:2"},
	{`,1,,2,,"x",,`, CommaDelimited, `1:1 4:2 7:"x"`},
	{"\ufeff[1] [2]", Whitespace, "3:[1] 7:[2]"},
	{`  [ {"a":1} , 2 ]  `, CommaDelimitedArray, `4:{"a":1} 14:2`},
	{"[]", CommaDelimitedArray, ""},
	{"{}", CommaDelimitedArray, "!0 tape"},
	{"1 2 \xff 3", Whitespace, "0:1 2:2 !4 utf8"},
	{"{\"a\":\"\x01\"} 1", Whitespace, "!0 ctrl"},
	{"[1] [2", Whitespace, "0:[1] !4 trailing"},
	{"1 2 \xc3", Whitespace, "0:1 2:2 !4 utf8"},
	{"", Whitespace, ""},
	{" \n ", NewlineDelimited, ""},
	{"{\"a\":1}\r\n{\"a\":2}\r\n", NewlineDelimited, `0:{"a":1} 9:{"a":2}`},
	{"{\"a\":1}\r\n{\"a\":2}", NewlineDelimited, `0:{"a":1} 9:{"a":2}`},
	{"true  {  ", Whitespace, "0:true !6 trailing"},
}

func TestParseMany(t *testing.T) {
	var p Parser
	for _, c := range manyCases {
		if got := manyItems(&p, []byte(c.in), c.f); got != c.want {
			t.Errorf("%q (format %d):\n got  %s\n want %s", c.in, c.f, got, c.want)
		}
	}
	// DOM only: stage 2 fails on the first document, which On-Demand yields.
	if got := manyItems(&p, []byte("[1,23 [1,23]"), Whitespace); got != "!0 tape" {
		t.Errorf("[1,23 [1,23]: %s", got)
	}
	// A JSONSequence record is kept unbalanced, so a number or literal in a
	// container can end the input: it meets C++'s 0 padding (stage 2 visit_number).
	for in, want := range map[string]string{
		"\x1e[1,23": "!1 number", "\x1e[true": "!1 tatom", "\x1e{\"a\":1.5": "!1 number", "\x1e23": "1:23",
	} {
		if got := manyItems(&p, []byte(in), JSONSequence); got != want {
			t.Errorf("%q: got %s, want %s", in, got, want)
		}
	}
	// A document still open at the end of a CommaDelimitedArray's contents
	// reads the array's own ']' there, as C++ (its sentinel
	// structural_indexes[n] = len points at the byte after its input). C++
	// then parses on past that sentinel; Go stops (spec §4.3).
	var items []string
	for doc, err := range p.ParseMany([]byte("[[1 2]]"), CommaDelimitedArray) {
		if err != nil {
			items = append(items, err.Error())
			continue
		}
		items = append(items, fmt.Sprintf("%d:%s:%s", doc.Offset(), doc.Source(), doc.Root().AppendJSON(nil)))
	}
	if got := strings.Join(items, " "); got != "1:[1 2]]:[1] "+(&StreamError{Offset: 4, Err: ErrTrailingContent}).Error() {
		t.Errorf("[[1 2]]: %s", got)
	}
	// The same when the document's last index is decided in an earlier
	// window than the one showing that what follows is dropped.
	long := "[[[][" + strings.Repeat("0", 100) + "]"
	for _, bs := range []int{0, 64} {
		p.BatchSize = bs
		if got := manyItems(&p, []byte(long), CommaDelimitedArray); got != "1:"+long[1:]+" !4 trailing" {
			t.Errorf("BatchSize %d, %q: %s", bs, long, got)
		}
	}
	p.BatchSize = 0
	doc, err := p.Parse([]byte(`[1]`))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Offset() != 0 || doc.Source() != nil {
		t.Errorf("Parse: %d %q", doc.Offset(), doc.Source())
	}
}

func TestParseManyBatchSizes(t *testing.T) {
	var sb strings.Builder
	for i := range 3000 {
		fmt.Fprintf(&sb, "{\"id\":%d,\"s\":\"ü\\n\"}\n", i)
	}
	sb.WriteString("[" + strings.Repeat("1,", 20000) + "1]\n") // far larger than the batch
	in := []byte(sb.String())
	var p Parser
	want := manyItems(&p, in, Whitespace)
	if n := strings.Count(want, " ") + 1; n != 3001 || strings.Contains(want, "!") {
		t.Fatalf("%d items, error: %v", n, strings.Contains(want, "!"))
	}
	for _, bs := range []int{64, 100, 4096} {
		p.BatchSize = bs
		if got := manyItems(&p, in, Whitespace); got != want {
			t.Fatalf("BatchSize %d differs", bs)
		}
	}
}

func TestParseManyLifecycle(t *testing.T) {
	in := []byte("[1] [2] [3]")
	var p Parser
	seq := p.ParseMany(in, Whitespace)
	for range seq {
		break
	}
	if got := manyItems(&p, in, Whitespace); got != "0:[1] 4:[2] 8:[3]" {
		t.Fatalf("after a break: %s", got)
	}
	var again []string // ranging over the same Seq2 restarts
	for doc, err := range seq {
		if err != nil {
			t.Fatal(err)
		}
		again = append(again, string(doc.Source()))
	}
	if s := strings.Join(again, " "); s != "[1] [2] [3]" {
		t.Fatalf("second range: %s", s)
	}
	var got []string // Parse inside the loop only invalidates the current document
	for doc, err := range p.ParseMany(in, Whitespace) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(doc.Source()))
		if _, err := p.Parse([]byte(`{"x":1}`)); err != nil {
			t.Fatal(err)
		}
	}
	if s := strings.Join(got, " "); s != "[1] [2] [3]" {
		t.Fatalf("with Parse inside: %s", s)
	}
	var errs []error // a second stream on p ends the first
	for _, err := range p.ParseMany(in, Whitespace) {
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for range p.ParseMany(in, Whitespace) {
		}
	}
	if len(errs) != 1 || !errors.Is(errs[0], ErrOutOfOrderIteration) {
		t.Fatalf("nested stream: %v", errs)
	}
}

// TestStreamNoLeak breaks out of a pipelined stream at every document: the
// stage 1 goroutine must be gone when the loop ends (synctest fails on a
// goroutine left blocked in the bubble).
func TestStreamNoLeak(t *testing.T) {
	in := []byte(strings.Repeat(`{"a":[1,2,3],"b":"xyz"}`+"\n", 400))
	for stop := range 30 {
		synctest.Test(t, func(t *testing.T) {
			p := Parser{BatchSize: 64}
			n := 0
			for range p.ParseMany(in, Whitespace) {
				if n++; n > stop*13 {
					break
				}
			}
		})
	}
}

// TestStreamPull2 interleaves two pipelined streams on one Parser: B starts
// while A is suspended, A is stopped while B is live, and B must still
// yield every document; A then ends (synctest fails on a leaked goroutine).
func TestStreamPull2(t *testing.T) {
	var a, b strings.Builder
	for i := range 200 {
		fmt.Fprintf(&a, "[%d]\n", i)
		fmt.Fprintf(&b, "{\"b\":%d}\n", i)
	}
	synctest.Test(t, func(t *testing.T) {
		p := Parser{BatchSize: 64}
		nextA, stopA := iter.Pull2(p.ParseMany([]byte(a.String()), Whitespace))
		defer stopA()
		if doc, err, ok := nextA(); !ok || err != nil || string(doc.Source()) != "[0]" {
			t.Fatalf("A: %v %v", err, ok)
		}
		nextB, stopB := iter.Pull2(p.ParseMany([]byte(b.String()), Whitespace))
		defer stopB()
		for i := range 200 {
			doc, err, ok := nextB()
			if want := fmt.Sprintf(`{"b":%d}`, i); !ok || err != nil || string(doc.Source()) != want {
				t.Fatalf("B document %d: %v %v, want %s", i, err, ok, want)
			}
			if i == 0 {
				stopA()
			}
		}
		if _, _, ok := nextB(); ok {
			t.Fatal("B goes on past its end")
		}
		if _, err, ok := nextA(); ok && !errors.Is(err, ErrOutOfOrderIteration) {
			t.Fatalf("A after B: %v", err)
		}
		if p.stream.Buf != nil {
			t.Error("the Parser keeps B's input")
		}
	})
}
