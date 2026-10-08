package stream

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// readAll indexes in completely, batch bytes at a time.
func readAll(in []byte, f Format, batch int) *Reader {
	var r Reader
	r.Reset(in, f, batch)
	for r.Load() {
	}
	return &r
}

// kept returns the first character of every kept index.
func kept(r *Reader) string {
	var b strings.Builder
	for _, i := range r.Idx[:r.N] {
		b.WriteByte(r.Buf[i])
	}
	return b.String()
}

// The C++ tests' inputs (tests/dom/document_stream_tests.cpp,
// tests/ondemand/ondemand_document_stream_tests.cpp), in one batch.
func TestTrim(t *testing.T) {
	for _, c := range []struct {
		in   string
		f    Format
		kept string
		drop int
	}{
		{" 1111 }", Whitespace, "", 1}, // issue1977
		{`[1,23] "lone string" {"key":"unfinished value}`, Whitespace, `[1,2]"`, 21}, // test_naked_iterators
		{`{"a":1},{"b":`, CommaDelimited, `{":1}`, 8},                                // truncated_bytes_filtered_formats
		{`1,2,"abc`, CommaDelimited, "12", 4},                                        // ditto
		{"\x1e1\n\x1e2\n\x1e \"abc", JSONSequence, "12", 8},                          // ditto
		{"\x1e\x1e1\n\x1e\x1e\x1e2\n\x1e", JSONSequence, "12", -1},                   // RS runs
		{`,1,,2,,"x",,`, CommaDelimited, `12"`, -1},                                  // comma runs
		{"true  {  ", Whitespace, "t", 6},                                            // issue2137
		{"1 2 34", Whitespace, "123", -1},                                            // issue2181
		{"\x1e\x1e \x1e", JSONSequence, "", -1},                                      // RS only
		{"[1,23 [1,23]", Whitespace, "[1,2[1,2]", -1},                                // balanced after the last candidate
		{"", Whitespace, "", -1},
	} {
		r := readAll([]byte(c.in), c.f, 0)
		if got := kept(r); got != c.kept || r.Drop != c.drop {
			t.Errorf("%q: kept %q, drop %d; want %q, %d", c.in, got, r.Drop, c.kept, c.drop)
		}
	}
}

func TestInput(t *testing.T) {
	for _, c := range []struct {
		in, buf string
		base    int
		ok      bool
	}{
		{"\ufeff[1]", "[1]", 3, true},
		{`  [ {"a":1} , 2 ]  `, ` {"a":1} , 2 `, 3, true},
		{"[]", "", 1, true},
		{"[ ]", " ", 1, true},
		{"[", "", 0, false},
		{"{}", "", 0, false},
		{"  ", "", 0, false},
	} {
		f := CommaDelimitedArray
		if c.in == "\ufeff[1]" {
			f = Whitespace
		}
		buf, base, _, ok := Input([]byte(c.in), f)
		if string(buf) != c.buf || base != c.base || ok != c.ok {
			t.Errorf("%q: %q %d %v, want %q %d %v", c.in, buf, base, ok, c.buf, c.base, c.ok)
		}
	}
}

// docs and seps build random streams, valid and not.
var (
	docs = []string{`1`, `-2.5`, `"a\"b"`, `true`, `null`, `[]`, `{}`, `[1,[2,{"a":"é"}]]`,
		`{"a":{"b":[1,2]},"c":"😀"}`, `[1,`, `{"a":`, `]`, `}`, `"open`, `[1 2]`, `,`, "\xff", "\"\x01\""}
	seps = []string{" ", "\n", "", ",", "\x1e", "\r\n", " , ", "\x1e\n"}
)

func randomStream(r *rand.Rand) []byte {
	var b []byte
	for range r.IntN(40) {
		b = append(b, seps[r.IntN(len(seps))]...)
		b = append(b, docs[r.IntN(len(docs))]...)
	}
	return b
}

// state is everything a consumer of a fully read Reader can see.
func state(r *Reader) string {
	return fmt.Sprint(r.Idx[:r.N+3], r.N, r.Drop, r.Stage1Err(len(r.Buf)), r.badCtrl, r.badUTF8)
}

func TestWindows(t *testing.T) {
	rnd := rand.New(rand.NewPCG(5, 6))
	for range 2000 {
		in := randomStream(rnd)
		for f := Whitespace; f <= CommaDelimited; f++ {
			want := state(readAll(in, f, 1<<30))
			for _, batch := range []int{1, 64, 128, 192} {
				if got := state(readAll(in, f, batch)); got != want {
					t.Fatalf("format %d, batch %d, %q:\n got  %s\n want %s", f, batch, in, got, want)
				}
			}
		}
	}
}

func TestSkip(t *testing.T) {
	for _, c := range []struct {
		in         string
		pos, depth int
		want       int
		ok         bool
	}{
		{`[1,[2]] 3`, 0, 1, 7, true},
		{`[1,[2]] 3`, 7, 1, 8, true},
		{`{"a":1} 2`, 0, 1, 5, true},
		{`{"a":1} 2`, 3, 2, 5, true}, // the cursor on a's value at depth 2, as after FindNext("a")
		{"[1,23 [1,23]", 0, 1, 9, false},
		{"1 2", 0, 0, 0, true},
	} {
		r := readAll([]byte(c.in), Whitespace, 0)
		if got, ok := r.Skip(c.pos, c.depth); got != c.want || ok != c.ok {
			t.Errorf("%q Skip(%d, %d) = %d, %v; want %d, %v", c.in, c.pos, c.depth, got, ok, c.want, c.ok)
		}
	}
	r := readAll([]byte("{\"a\":1,\n\"b\":2}\n[3]"), NewlineDelimited, 0)
	if got, ok := r.SkipTo(1, '\n'); got != 5 || !ok { // the "b" after the first newline
		t.Errorf("SkipTo = %d, %v; want 5, true", got, ok)
	}
}

// TestCompactBounded walks a stream of many tiny documents as ParseMany
// does and checks the reader's memory follows the window, not the input.
func TestCompactBounded(t *testing.T) {
	const n = 200000
	var r Reader
	r.Reset(bytes.Repeat([]byte("1\n"), n), Whitespace, 4096)
	docs := 0
	for pos := 0; ; {
		pos = r.Compact(pos)
		for !r.Done && r.Decided() <= pos {
			r.Load()
		}
		if pos >= r.Decided() {
			break
		}
		next, ok := r.Skip(pos, 1)
		if !ok {
			t.Fatal("unbalanced")
		}
		docs, pos = docs+1, next
		if cap(r.Idx) > 4*4096 {
			t.Fatalf("cap(Idx) = %d after %d documents", cap(r.Idx), docs)
		}
	}
	if docs != n || r.Drop != -1 {
		t.Fatalf("%d documents, drop %d", docs, r.Drop)
	}
}
