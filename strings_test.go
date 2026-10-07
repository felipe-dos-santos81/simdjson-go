package simdjson

import (
	"strconv"
	"strings"
	"testing"
)

func TestStrings(t *testing.T) {
	tests := []struct{ in, want string }{
		{`""`, ""},
		{`"abc"`, "abc"},
		{`"\"\\\/\b\f\n\r\t"`, "\"\\/\b\f\n\r\t"},
		{`"\u0000"`, "\x00"},
		{`"\u0041\u00e9\u20ac"`, "Aé€"},
		{`"\ud83d\ude00"`, "😀"},
		{`"\ud83d\ude00x"`, "😀x"},
		{"\"é€😀\"", "é€😀"},
	}
	for _, tt := range tests {
		want := strconv.Quote(tt.want)
		if got := rootValue(t, tt.in); got != want {
			t.Errorf("%s: %s, want %s", tt.in, got, want)
		}
		var p Parser
		if got := parseTape(t, &p, `{`+tt.in+`:1}`)[2]; got != want { // keys are unescaped too
			t.Errorf("key %s: %s, want %s", tt.in, got, want)
		}
	}
}

func TestStringsAcrossBlocks(t *testing.T) {
	// Escapes and multi-byte characters at every offset around the 64-byte
	// stage 1 block boundary.
	for pad := 50; pad < 80; pad++ {
		x := strings.Repeat("x", pad)
		for _, tt := range []struct{ esc, want string }{
			{`\"`, `"`}, {`\\`, `\`}, {`\n`, "\n"}, {`\u00e9`, "é"}, {`\ud83d\ude00`, "😀"}, {"é", "é"}, {"😀", "😀"},
		} {
			want := strconv.Quote(x + tt.want + "!")
			if got := rootValue(t, `"`+x+tt.esc+`!"`); got != want {
				t.Fatalf("pad %d %s: %s, want %s", pad, tt.esc, got, want)
			}
		}
	}
}

func TestIndexQuoteOrBackslash(t *testing.T) {
	for n := range 40 {
		for _, c := range []byte{'"', '\\'} {
			s := []byte(strings.Repeat("x", n) + string(c) + "yy")
			if got := indexQuoteOrBackslash(s); got != n {
				t.Fatalf("indexQuoteOrBackslash(%q) = %d, want %d", s, got, n)
			}
		}
		if got := indexQuoteOrBackslash([]byte(strings.Repeat("x", n))); got != -1 {
			t.Fatalf("no match, len %d: got %d", n, got)
		}
	}
}
