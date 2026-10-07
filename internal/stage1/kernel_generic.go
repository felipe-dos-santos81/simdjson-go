package stage1

// masks classifies the 64 bytes of a block: bit i describes byte i.
type masks struct {
	backslash uint64 // '\\'
	quote     uint64 // '"'
	ws        uint64 // ' ', '\t', '\n', '\r'
	op        uint64 // '{', '}', '[', ']', ':', ','
	ctrl      uint64 // bytes < 0x20
}

// Byte classes used by classifyGeneric; bit k of classTable[c] is class k.
const (
	classBackslash = 1 << iota
	classQuote
	classWS
	classOp
	classCtrl
)

var classTable = func() (t [256]uint8) {
	for c := range 0x20 {
		t[c] |= classCtrl
	}
	t['\\'] |= classBackslash
	t['"'] |= classQuote
	for _, c := range []byte(" \t\n\r") {
		t[c] |= classWS
	}
	for _, c := range []byte("{}[]:,") {
		t[c] |= classOp
	}
	return t
}()

// classifyGeneric is the portable classify. It is the reference the SIMD
// kernel is tested against, so it is compiled on every platform.
func classifyGeneric(b *[64]byte) masks {
	var m masks
	for i := 0; i < 64; i += 8 {
		// Gather 8 class bytes into one word, then move bit k of each byte
		// into an 8-bit mask with a multiply.
		var w uint64
		for j := 7; j >= 0; j-- {
			w = w<<8 | uint64(classTable[b[i+j]])
		}
		m.backslash |= gather(w) << i
		m.quote |= gather(w>>1) << i
		m.ws |= gather(w>>2) << i
		m.op |= gather(w>>3) << i
		m.ctrl |= gather(w>>4) << i
	}
	return m
}

// gather returns bit 0 of each byte of w as an 8-bit mask (byte j → bit j).
func gather(w uint64) uint64 {
	return (w & 0x0101010101010101) * 0x0102040810204080 >> 56
}
