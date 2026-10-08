package stage1

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"
	"unicode/utf8"
)

// streamInput builds JSON-like bytes, mostly valid UTF-8, with a few bad bytes.
func streamInput(r *rand.Rand, n int) []byte {
	good := []string{"{", "}", "[", "]", ":", ",", " ", "\n", `"`, `\`, `\"`, "a", "1", "é", "€", "😀"}
	bad := []string{"\x01", "\xff", "\xe2\x82", "\xc3"}
	var b []byte
	for len(b) < n {
		if r.IntN(80) == 0 {
			b = append(b, bad[r.IntN(len(bad))]...)
		} else {
			b = append(b, good[r.IntN(len(good))]...)
		}
	}
	return b
}

// refBadUTF8 and refBadCtrl locate what stage 1 rejects, the slow way.
func refBadUTF8(b []byte) int {
	for i := 0; i < len(b); {
		r, n := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && n == 1 {
			return i
		}
		i += n
	}
	return math.MaxInt
}

func refBadCtrl(b []byte) int {
	// A backslash escapes the next byte inside and outside strings, as in C++.
	in, esc := false, false
	for i, c := range b {
		escaped := esc
		esc = c == '\\' && !escaped
		if in && c < 0x20 {
			return i
		}
		if c == '"' && !escaped {
			in = !in
		}
	}
	return math.MaxInt
}

// checkWindows scans buf in windows of win bytes and in one call, and
// compares both with each other and with the slow references.
func checkWindows(t *testing.T, buf []byte, win int) {
	t.Helper()
	var one, many Stream
	one.Reset(buf)
	want := one.Next(len(buf), nil)
	many.Reset(buf)
	var got []uint32
	for end := 0; end < len(buf); {
		end = min(end+win, len(buf))
		got = many.Next(end, got)
	}
	if !slices.Equal(got, want) || many.Unclosed() != one.Unclosed() ||
		many.BadCtrl != one.BadCtrl || many.BadUTF8 != one.BadUTF8 {
		t.Fatalf("window %d, %q: windows (%v %v %d %d) != one pass (%v %v %d %d)", win, buf,
			got, many.Unclosed(), many.BadCtrl, many.BadUTF8, want, one.Unclosed(), one.BadCtrl, one.BadUTF8)
	}
	if b := refBadUTF8(buf); one.BadUTF8 != b {
		t.Fatalf("%q: BadUTF8 = %d, want %d", buf, one.BadUTF8, b)
	}
	if c := refBadCtrl(buf); one.BadCtrl != c {
		t.Fatalf("%q: BadCtrl = %d, want %d", buf, one.BadCtrl, c)
	}
}

func TestStreamWindows(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for n := range 3000 {
		checkWindows(t, streamInput(r, r.IntN(700)), 64*(1+n%5))
	}
}

func FuzzStream(f *testing.F) {
	f.Add([]byte(`{"a":"é😀"} [1,2]`), uint8(0))
	f.Fuzz(func(t *testing.T, buf []byte, win uint8) {
		checkWindows(t, buf, 64*(1+int(win)%8))
	})
}
