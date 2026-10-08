package str

import (
	"strings"
	"testing"
)

func TestIndexQuoteOrBackslash(t *testing.T) {
	for n := range 40 {
		for _, c := range []byte{'"', '\\'} {
			s := []byte(strings.Repeat("x", n) + string(c) + "yy")
			if got := IndexQuoteOrBackslash(s); got != n {
				t.Fatalf("IndexQuoteOrBackslash(%q) = %d, want %d", s, got, n)
			}
		}
		if got := IndexQuoteOrBackslash([]byte(strings.Repeat("x", n))); got != -1 {
			t.Fatalf("no match, len %d: got %d", n, got)
		}
	}
}
