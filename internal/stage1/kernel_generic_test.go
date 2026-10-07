package stage1

import "testing"

func TestGather(t *testing.T) {
	for m := range 256 {
		var w uint64
		for j := range 8 {
			if m&(1<<j) != 0 {
				w |= 0xFF << (8 * j) // every bit set: only bit 0 of each byte may count
			}
		}
		if got := gather(w); got != uint64(m) {
			t.Fatalf("gather(%#x) = %#x, want %#x", w, got, m)
		}
	}
}

func TestClassifyGeneric(t *testing.T) {
	var b [64]byte
	copy(b[:], "{\"a\\\": [1,\t2]}\n\x01")
	for i := 16; i < 64; i++ {
		b[i] = 'x'
	}
	m := classifyGeneric(&b)
	// {"a\": [1,<tab>2]}<lf><0x01>
	// 0 12 345 6789 0 1 2 3  4   5   6
	want := masks{
		backslash: 1 << 3,
		quote:     1<<1 | 1<<4,
		ws:        1<<6 | 1<<10 | 1<<14,
		op:        1<<0 | 1<<5 | 1<<7 | 1<<9 | 1<<12 | 1<<13,
		ctrl:      1<<10 | 1<<14 | 1<<15,
	}
	if m != want {
		t.Fatalf("classifyGeneric = %+v\nwant              %+v", m, want)
	}
}
