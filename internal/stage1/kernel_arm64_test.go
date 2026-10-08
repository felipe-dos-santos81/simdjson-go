//go:build arm64 && goexperiment.simd && !purego

package stage1

import (
	"math/rand/v2"
	"testing"
	"unicode/utf8"
)

func TestClassifyMatchesGeneric(t *testing.T) {
	var b [64]byte
	for start := 0; start < 256; start += 64 { // every byte value at every position class
		for i := range b {
			b[i] = byte(start + i)
		}
		checkClassify(t, &b)
	}
	r := rand.New(rand.NewPCG(1, 2))
	for range 10000 {
		for i := range b {
			b[i] = byte(r.Uint32())
		}
		checkClassify(t, &b)
	}
}

func FuzzClassify(f *testing.F) {
	f.Add([]byte(`{"a":[1,2,"x\"y"]}`))
	f.Fuzz(func(t *testing.T, in []byte) {
		var b [64]byte
		copy(b[:], in)
		checkClassify(t, &b)
	})
}

func checkClassify(t *testing.T, b *[64]byte) {
	t.Helper()
	if got, want := classify(b), classifyGeneric(b); got != want {
		t.Fatalf("classify(%q)\n got %+v\nwant %+v", b[:], got, want)
	}
}

// neonValid runs the NEON checker over buf exactly as Index does.
func neonValid(buf []byte) bool {
	var (
		u    utf8Checker
		tail [64]byte
	)
	for off := 0; off < len(buf); off += 64 {
		blk, _ := block(buf, off, &tail)
		u.next(blk)
	}
	return u.validWindow(buf, 0, len(buf))
}

func TestUTF8MatchesStdlib(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	pieces := [][]byte{
		[]byte("a"), []byte("é"), []byte("€"), []byte("😀"), // valid 1..4 byte
		{0x80}, {0xc0, 0xaf}, {0xed, 0xa0, 0x80}, {0xf4, 0x90, 0x80, 0x80}, {0xff}, // invalid
		{0xe2, 0x82}, {0xf0, 0x9f}, // truncated
	}
	for range 20000 {
		var buf []byte
		for range r.IntN(200) {
			p := pieces[r.IntN(len(pieces))]
			if r.IntN(8) != 0 {
				p = pieces[r.IntN(4)] // mostly valid input
			}
			buf = append(buf, p...)
		}
		if got, want := neonValid(buf), utf8.Valid(buf); got != want {
			t.Fatalf("neonValid(%x) = %v, want %v", buf, got, want)
		}
	}
}

func FuzzUTF8(f *testing.F) {
	f.Add([]byte("héllo €😀"))
	f.Fuzz(func(t *testing.T, buf []byte) {
		if got, want := neonValid(buf), utf8.Valid(buf); got != want {
			t.Fatalf("neonValid(%x) = %v, want %v", buf, got, want)
		}
	})
}
