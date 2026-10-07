package simdjson

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// dumpTape renders a tape one entry per value, in the notation of
// doc/tape.md: numbers show their value, strings their unescaped bytes,
// containers the index after their end and their element count.
func dumpTape(d *Document) []string {
	var out []string
	for i := 0; i < len(d.tape); i++ {
		w := d.tape[i]
		tag, payload := byte(w>>56), w&(1<<56-1)
		switch tag {
		case 'l':
			i++
			out = append(out, "l "+strconv.FormatInt(int64(d.tape[i]), 10))
		case 'u':
			i++
			out = append(out, "u "+strconv.FormatUint(d.tape[i], 10))
		case 'd':
			i++
			out = append(out, "d "+strconv.FormatFloat(math.Float64frombits(d.tape[i]), 'g', -1, 64))
		case '"', 'Z':
			n := uint64(binary.LittleEndian.Uint32(d.strings[payload:]))
			s := string(d.strings[payload+4 : payload+4+n])
			if d.strings[payload+4+n] != 0 {
				s += " (missing NUL)"
			}
			if tag == 'Z' {
				out = append(out, "Z "+s)
			} else {
				out = append(out, strconv.Quote(s))
			}
		case '{', '[':
			out = append(out, fmt.Sprintf("%c %d n=%d", tag, uint32(payload), payload>>32))
		default:
			out = append(out, fmt.Sprintf("%c %d", tag, payload))
		}
	}
	return out
}

// parseTape parses in with p and returns dumpTape of the result.
func parseTape(t *testing.T, p *Parser, in string) []string {
	t.Helper()
	doc, err := p.Parse([]byte(in))
	if err != nil {
		t.Fatalf("Parse(%.80q): %v", in, err)
	}
	return dumpTape(doc)
}

// rootValue returns the tape entry of a scalar document's value.
func rootValue(t *testing.T, in string) string {
	t.Helper()
	var p Parser
	return parseTape(t, &p, in)[1]
}

func TestTapeLayout(t *testing.T) {
	// The example of doc/tape.md; the comments give the tape index.
	const in = `{"Image":{"Width":800,"Height":600,"Title":"View from 15th Floor",
		"Thumbnail":{"Url":"http://www.example.com/image/481989943","Height":125,"Width":100},
		"Animated":false,"IDs":[116,943,234,38793]}}`
	want := []string{
		"r 39", "{ 38 n=1", `"Image"`, "{ 37 n=6", // 0-3
		`"Width"`, "l 800", `"Height"`, "l 600", // 4, 5-6, 7, 8-9
		`"Title"`, `"View from 15th Floor"`, `"Thumbnail"`, "{ 23 n=3", // 10-13
		`"Url"`, `"http://www.example.com/image/481989943"`, `"Height"`, "l 125", `"Width"`, "l 100", // 14-21
		"} 13", `"Animated"`, "f 0", `"IDs"`, "[ 36 n=4", // 22-26
		"l 116", "l 943", "l 234", "l 38793", // 27-34
		"] 26", "} 3", "} 1", "r 0", // 35-38
	}
	var p Parser
	if got := parseTape(t, &p, in); !slices.Equal(got, want) {
		t.Fatalf("tape:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if got := parseTape(t, &p, `[[],{},true,null]`); !slices.Equal(got, []string{
		"r 10", "[ 9 n=4", "[ 4 n=0", "] 2", "{ 6 n=0", "} 4", "t 0", "n 0", "] 1", "r 0",
	}) {
		t.Errorf("empty containers: %q", got)
	}
}

func TestParseErrors(t *testing.T) {
	// Expected errors were produced by C++ simdjson v5.0.2 (dom::parser).
	tests := []struct {
		in   string
		want error
	}{
		{"", ErrEmpty},
		{" ", ErrEmpty},
		{"\t\n", ErrEmpty},
		{"{", ErrTape},
		{"[", ErrTape},
		{"\"", ErrUnclosedString},
		{"\"abc", ErrUnclosedString},
		{"[1,", ErrTape},
		{"{\"a\":", ErrTape},
		{"tru", ErrTAtom},
		{"nul", ErrNAtom},
		{"fals", ErrFAtom},
		{"12.", ErrNumber},
		{"-", ErrNumber},
		{"\x00", ErrTape},
		{"[[]", ErrTape},
		{"{\"a\":{}", ErrTape},
		{"1 2", ErrTape},
		{"[] []", ErrTape},
		{"{}x", ErrTape},
		{"[1]]", ErrTape},
		{"[1 2]", ErrTape},
		{"{\"a\" 1}", ErrTape},
		{"{\"a\":1 \"b\":2}", ErrTape},
		{"{1:2}", ErrTape},
		{"[,1]", ErrNumber}, // C++ sends every byte below '0' in a container to the number parser
		{"[1,]", ErrTape},
		{"{\"a\":1,}", ErrTape},
		{"{\"a\"}", ErrTape},
		{"]", ErrTape},
		{"\"a\"x", ErrTape},
		{"\"a\"true", ErrTape},
		{"truefalse", ErrTAtom},
		{"[true false]", ErrTape},
		{"01", ErrNumber},
		{"-01", ErrNumber},
		{"1.", ErrNumber},
		{".1", ErrTape},
		{"1e", ErrNumber},
		{"+1", ErrTape},
		{"--1", ErrNumber},
		{"0x10", ErrNumber},
		{"NaN", ErrTape},
		{"[NaN]", ErrTape},
		{"[Infinity]", ErrTape},
		{"1e400", ErrNumber},
		{"1x", ErrNumber},
		{"[1x]", ErrNumber},
		{"1\x00", ErrNumber},
		{"trux", ErrTAtom},
		{"ture", ErrTAtom},
		{"falx", ErrFAtom},
		{"nulx", ErrNAtom},
		{"[truex]", ErrTAtom},
		{"\"\\uD800\"", ErrString},
		{"\"\\uDC00\"", ErrString},
		{"\"\\uD800\\u0041\"", ErrString},
		{"\"\\u12G4\"", ErrString},
		{"\"\\x\"", ErrString},
		{"\"\x01\"", ErrUnescapedChars},
		{"\"\xff\"", ErrUTF8},
		{"\"\xc3\"", ErrUTF8},
		{"\"\xed\xa0\x80\"", ErrUTF8},
		{"[\f]", ErrNumber},
		{"\"\\", ErrUnclosedString},
		{"\"\\\"", ErrUnclosedString},
		{"18446744073709551616", ErrBigInt},
		{"-9223372036854775809", ErrBigInt},
		{"123456789012345678901x", ErrBigInt}, // too long is reported before the bad terminator
		{"[123456789012345678901x]", ErrBigInt},
		{"[7,7,7,7,6,7,7,7,6,7,7,6,[7,7,7,7,6,7,7,7,6,7,7,6,7,7,7,7,7,7,6", ErrTape}, // C++ issue 345
	}
	for _, tt := range tests {
		var p Parser
		doc, err := p.Parse([]byte(tt.in))
		if !errors.Is(err, tt.want) || doc != nil {
			t.Errorf("Parse(%q) = %v, %v; want nil, %v", tt.in, doc, err, tt.want)
		}
	}
}

func nest(n int, inner string) []byte {
	return []byte(strings.Repeat("[", n) + inner + strings.Repeat("]", n))
}

func TestMaxDepth(t *testing.T) {
	// Only non-empty arrays/objects count: an empty one is written without
	// opening a scope (C++ visit_empty_array), so 1024 brackets ending in []
	// are accepted while 1024 levels around a value are not.
	var p Parser
	for _, tt := range []struct {
		in   []byte
		want error
	}{
		{nest(1023, "1"), nil},
		{nest(1024, "1"), ErrDepth},
		{nest(1023, "[]"), nil},
		{nest(1024, "[]"), ErrDepth},
		{[]byte(strings.Repeat(`{"a":`, 1024) + "1" + strings.Repeat("}", 1024)), ErrDepth},
	} {
		if _, err := p.Parse(tt.in); !errors.Is(err, tt.want) || (tt.want == nil) != (err == nil) {
			t.Errorf("depth case %.20q...: err = %v, want %v", tt.in, err, tt.want)
		}
	}
	p.MaxDepth = 3
	if _, err := p.Parse([]byte(`{"a":[1]}`)); err != nil {
		t.Errorf("MaxDepth 3, depth 2: %v", err)
	}
	if _, err := p.Parse([]byte(`[[[1]]]`)); !errors.Is(err, ErrDepth) {
		t.Errorf("MaxDepth 3, depth 3: err = %v, want ErrDepth", err)
	}
}

// A huge MaxDepth is clamped to 1<<20 so that AppendJSON's recursion cannot
// overflow Go's (fatal, unrecoverable) stack limit.
func TestMaxDepthCeiling(t *testing.T) {
	p := Parser{MaxDepth: math.MaxInt}
	in := nest(1<<20-1, "1")
	doc, err := p.Parse(in)
	if err != nil {
		t.Fatalf("depth 1<<20-1: %v", err)
	}
	if got := doc.Root().AppendJSON(nil); string(got) != string(in) {
		t.Errorf("AppendJSON round trip differs (len %d, want %d)", len(got), len(in))
	}
	if _, err := p.Parse(nest(1<<20, "1")); !errors.Is(err, ErrDepth) {
		t.Errorf("depth 1<<20: err = %v, want ErrDepth", err)
	}
}

func TestByteOrderMark(t *testing.T) {
	var p Parser
	const doc = `{"score":0.8825149536132812}`
	if got, want := parseTape(t, &p, "\xEF\xBB\xBF"+doc), parseTape(t, &p, doc); !slices.Equal(got, want) {
		t.Errorf("with BOM %q, without %q", got, want)
	}
	if _, err := p.Parse([]byte("\xEF\xBB\xBF")); !errors.Is(err, ErrEmpty) {
		t.Errorf("BOM only: err = %v, want ErrEmpty", err)
	}
}

func TestNoReadPastLength(t *testing.T) {
	// C++ errortests number_overrun_*: bytes after len(b) are never read, even
	// when they are in the slice's capacity.
	const filler = "22222222222222222222222222222222222222222222222222222222222222222"
	var p Parser
	buf := []byte("1" + filler + ",")
	doc, err := p.Parse(buf[:1])
	if err != nil {
		t.Fatal(err)
	}
	if got := dumpTape(doc); !slices.Equal(got, []string{"r 4", "l 1", "r 0"}) {
		t.Errorf("tape = %q", got)
	}
	for _, in := range []string{"[1" + filler + "]", `{"key":1` + filler + "}"} {
		b := []byte(in)
		n := strings.Index(in, "1") + 1
		if _, err := p.Parse(b[:n]); !errors.Is(err, ErrTape) {
			t.Errorf("Parse(%q) err = %v, want ErrTape", b[:n], err)
		}
	}
	brackets := []byte("[][[[[[[[[[[[[[[[[[") // C++ document_tests padded_with_open_bracket
	if _, err := p.Parse(brackets[:2]); err != nil {
		t.Errorf("[]: %v", err)
	}
	if _, err := p.Parse(brackets[2:4]); !errors.Is(err, ErrTape) {
		t.Errorf("[[: err = %v, want ErrTape", err)
	}
}

func TestParserReuse(t *testing.T) {
	var reused Parser
	for _, in := range []string{"[true,false]", `{"yay":"json!"}`, "[1,2,3,null]", `"x"`, "[1,", "[]"} {
		var fresh Parser
		_, wantErr := fresh.Parse([]byte(in))
		doc, err := reused.Parse([]byte(in))
		if (err == nil) != (wantErr == nil) {
			t.Fatalf("Parse(%q): err = %v, fresh parser err = %v", in, err, wantErr)
		}
		if err == nil {
			if got, want := dumpTape(doc), parseTape(t, &fresh, in); !slices.Equal(got, want) {
				t.Errorf("reused %q, fresh %q", got, want)
			}
		}
	}
}
