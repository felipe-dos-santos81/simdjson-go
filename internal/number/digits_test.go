package number

import (
	"strings"
	"testing"
)

// TestDigits checks Digits against the digit-by-digit loop for every run
// length around the 8-byte steps, with and without bytes after the run.
func TestDigits(t *testing.T) {
	for n := 0; n <= 40; n++ {
		run := strings.Repeat("9876543210", 4)[:n]
		for _, tail := range []string{"", "e5", ",", "]       "} {
			s := []byte("x" + run + tail)
			p, m := 1, uint64(7)
			for p < len(s) && IsDigit(s[p]) {
				m = 10*m + uint64(s[p]-'0')
				p++
			}
			if gp, gm := Digits(s, 1, 7); gp != p || gm != m {
				t.Errorf("Digits(%q): %d, %d; want %d, %d", s, gp, gm, p, m)
			}
		}
	}
}
