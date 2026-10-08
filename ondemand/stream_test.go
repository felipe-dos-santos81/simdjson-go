package ondemand_test

import (
	"errors"
	"fmt"
	"iter"
	"math/rand/v2"
	"strings"
	"testing"
	"testing/synctest"

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

// TestIterateManyAbandoned: after a read that abandons a document (a fatal
// error), the next step skips from the document's root, as for an unread
// document, and the stream goes on to the next record.
func TestIterateManyAbandoned(t *testing.T) {
	cases := []struct{ in, want string }{
		{"{\"a\":1}\n{\"a\":}\n{\"a\":3}", `0:{"a":1} 8:{"a":}:dead 15:{"a":3}`},
		{"[{\"a\":}, 1, [2, 3], {\"x\":1}]\n7", `0:[{"a":}, 1, [2, 3], {"x":1}]:dead 29:7`},
	}
	var p ondemand.Parser
	for _, c := range cases {
		for _, f := range []ondemand.Format{ondemand.NewlineDelimited, ondemand.Whitespace} {
			for _, bs := range []int{0, 64} {
				p.BatchSize = bs
				var got []string
				for doc, err := range p.IterateMany([]byte(c.in), f) {
					if err != nil {
						got = append(got, itemErr(err))
						continue
					}
					item := fmt.Sprintf("%d:%s", doc.Offset(), doc.Source())
					if walk(doc, new(strings.Builder)) != nil && ondemand.Abandoned(doc) {
						item += ":dead"
					}
					got = append(got, item)
				}
				if s := strings.Join(got, " "); s != c.want {
					t.Errorf("%q, format %d, BatchSize %d:\n got  %s\n want %s", c.in, f, bs, s, c.want)
				}
			}
		}
	}
}

// TestIterateManyNoLeak is the root package's TestStreamNoLeak for IterateMany.
func TestIterateManyNoLeak(t *testing.T) {
	in := []byte(strings.Repeat(`{"a":[1,2,3],"b":"xyz"}`+"\n", 400))
	for stop := range 30 {
		synctest.Test(t, func(t *testing.T) {
			p := ondemand.Parser{BatchSize: 64}
			n := 0
			for range p.IterateMany(in, ondemand.Whitespace) {
				if n++; n > stop*13 {
					break
				}
			}
		})
	}
}

// TestIterateManyPull2 is the root package's TestStreamPull2 for IterateMany.
func TestIterateManyPull2(t *testing.T) {
	var a, b strings.Builder
	for i := range 200 {
		fmt.Fprintf(&a, "[%d]\n", i)
		fmt.Fprintf(&b, "{\"b\":%d}\n", i)
	}
	synctest.Test(t, func(t *testing.T) {
		p := ondemand.Parser{BatchSize: 64}
		nextA, stopA := iter.Pull2(p.IterateMany([]byte(a.String()), ondemand.Whitespace))
		defer stopA()
		if doc, err, ok := nextA(); !ok || err != nil || string(doc.Source()) != "[0]" {
			t.Fatalf("A: %v %v", err, ok)
		}
		nextB, stopB := iter.Pull2(p.IterateMany([]byte(b.String()), ondemand.Whitespace))
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
		if _, err, ok := nextA(); ok && !errors.Is(err, simdjson.ErrOutOfOrderIteration) {
			t.Fatalf("A after B: %v", err)
		}
	})
}

// TestIterateManyReadPastRoot: a read can run past the root's end by its
// brackets, as far as C++'s one batch holds, whatever the batch size: on a
// malformed root ("[}" leaves the array open for On-Demand), or out of
// order (an empty root array started again, as C++'s release build allows,
// reads on from its end).
func TestIterateManyReadPastRoot(t *testing.T) {
	rest := " " + strings.Repeat("[1] ", 40) + "} 0"
	misuse := "[]," + strings.Repeat("1 ", 60) + "1]] 0"
	for _, c := range []struct {
		in   string
		read func(*ondemand.Document) string
		want string // at BatchSize 0
	}{
		{"[}" + rest, func(doc *ondemand.Document) string {
			a, err := doc.Array()
			if err != nil {
				return err.Error()
			}
			raw, err := a.Raw()
			if err != nil {
				return err.Error()
			}
			return string(raw)
		}, "0:[}:[}" + rest[:len(rest)-1] + fmt.Sprintf(" %d:0:%s", len(rest)+1, simdjson.ErrIncorrectType)},
		{misuse, func(doc *ondemand.Document) string {
			if _, err := doc.Raw(); err != nil {
				return err.Error()
			}
			a, err := doc.Array()
			if err != nil {
				return err.Error()
			}
			raw, err := a.Raw()
			if err != nil {
				return err.Error()
			}
			return string(raw)
		}, "0:[]:" + misuse[:len(misuse)-1] + fmt.Sprintf(" %d:0:%s", len(misuse)-1, simdjson.ErrIncorrectType)},
	} {
		var p ondemand.Parser
		var want string
		for _, bs := range []int{0, 64} {
			p.BatchSize = bs
			var got []string
			for doc, err := range p.IterateMany([]byte(c.in), ondemand.Whitespace) {
				if err != nil {
					got = append(got, itemErr(err))
					continue
				}
				got = append(got, fmt.Sprintf("%d:%s:%s", doc.Offset(), doc.Source(), c.read(doc)))
			}
			s := strings.Join(got, " ")
			if bs == 0 {
				want = s
				if s != c.want {
					t.Errorf("%.10q: got  %s\n want %s", c.in, s, c.want)
				}
			} else if s != want {
				t.Errorf("%.10q, BatchSize %d:\n got  %s\n want %s", c.in, bs, s, want)
			}
		}
	}
}

// TestStreamStage1Formats: stage 1 errors in the comma and RS formats, and
// in the dropped tail, are reported where spec §4.1 says, by both APIs.
func TestStreamStage1Formats(t *testing.T) {
	var dp simdjson.Parser
	var op ondemand.Parser
	for _, c := range []struct {
		in   string
		f    simdjson.Format
		want string
	}{
		{"{\"a\":\"x\"},{\"b\":\"\xff\"},{\"c\":1}", simdjson.CommaDelimited, `0:{"a":"x"} !10 utf8`},
		{"\x1e{\"a\":1}\n\x1e{\"b\":\"\x01\"}\n\x1e2", simdjson.JSONSequence, `1:{"a":1} !10 ctrl`},
		{"[1] [2 \"\xff", simdjson.Whitespace, "0:[1] !4 utf8"},       // in the dropped tail
		{"1,2,[3,\"\xff", simdjson.CommaDelimited, "0:1 2:2 !4 utf8"}, // ditto
	} {
		for _, bs := range []int{0, 64} {
			dp.BatchSize, op.BatchSize = bs, bs
			if got := domItems(&dp, []byte(c.in), c.f); got != c.want {
				t.Errorf("DOM %q BatchSize %d: %s, want %s", c.in, bs, got, c.want)
			}
			if got := odItems(&op, []byte(c.in), c.f); got != c.want {
				t.Errorf("On-Demand %q BatchSize %d: %s, want %s", c.in, bs, got, c.want)
			}
		}
	}
}

// TestIterateManyStaleDocument reads the previous document while the stream
// yields its final error, after the reader compacted its indices: it must
// report ErrOutOfOrderIteration, never panic.
func TestIterateManyStaleDocument(t *testing.T) {
	for _, in := range []string{
		strings.Repeat("0 ", 12) + "[1,1] [\"\xff\",1,2,3] 7 8",
		strings.Repeat("[1] ", 29) + "{\"a\":[ 7 7 \"\xff\" [2]",
	} {
		for _, bs := range []int{0, 64} {
			p := ondemand.Parser{BatchSize: bs}
			var prev *ondemand.Document
			sawErr := false
			for doc, err := range p.IterateMany([]byte(in), ondemand.Whitespace) {
				if err != nil {
					sawErr = true
					if src := prev.Source(); src != nil {
						t.Errorf("%q BatchSize %d: stale Source = %q", in, bs, src)
					}
					if _, err := prev.Type(); !errors.Is(err, simdjson.ErrOutOfOrderIteration) {
						t.Errorf("%q BatchSize %d: stale Type: %v", in, bs, err)
					}
					continue
				}
				prev = doc
			}
			if !sawErr {
				t.Errorf("%q BatchSize %d: no error item", in, bs)
			}
		}
	}
}
