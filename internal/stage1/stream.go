package stage1

import (
	"math"
	"math/bits"
	"unicode/utf8"
)

// Stream is Index run window by window over one buffer: the state carried
// from one 64-byte block to the next is kept between calls, so the indices
// of any sequence of windows equal one Index over the whole buffer. It also
// locates the first byte stage 1 rejects, which Index only detects.
type Stream struct {
	buf []byte
	off int // buf[:off] is scanned
	s   scanner
	u   utf8Checker
	// BadCtrl and BadUTF8 are the offsets of the first control character
	// inside a string and of the first byte of invalid UTF-8 found so far,
	// or math.MaxInt.
	BadCtrl, BadUTF8 int
}

// Reset starts a scan of buf.
func (st *Stream) Reset(buf []byte) {
	*st = Stream{buf: buf, BadCtrl: math.MaxInt, BadUTF8: math.MaxInt}
}

// Next scans buf[off:end], end being a multiple of 64 past the previous
// end or len(buf), and appends the offsets of its structural characters to
// idx. Once end reaches len(buf), every bad byte has been found; before
// that, those below end-3 have (a character cut at end is checked with the
// next window).
func (st *Stream) Next(end int, idx []uint32) []uint32 {
	lo, saved := st.off, st.s
	var tail [64]byte
	for off := lo; off < end; off += 64 {
		blk, _ := block(st.buf, off, &tail)
		st.u.next(blk)
		structurals, _ := st.s.next(classify(blk))
		for structurals != 0 { // ponytail: plain loop; unroll if BenchmarkIndex shows it hot
			idx = append(idx, uint32(off+bits.TrailingZeros64(structurals)))
			structurals &= structurals - 1
		}
	}
	st.off = end
	if st.BadCtrl == math.MaxInt && st.s.unescaped != 0 {
		st.BadCtrl = firstCtrl(st.buf, lo, end, saved)
	}
	if st.BadUTF8 == math.MaxInt && !st.u.validWindow(st.buf, lo, end) {
		// NEON reports a character cut at the previous window's end only
		// now, so look from up to a block back.
		st.BadUTF8 = firstBadUTF8(st.buf, runeStart(st.buf, max(lo-64, 0)), end)
	}
	return idx
}

// Unclosed reports whether the bytes scanned so far end inside a string.
func (st *Stream) Unclosed() bool { return st.s.prevInString != 0 }

// firstCtrl rescans buf[lo:end] from scanner state s and returns the offset
// of the first control character inside a string (error path only).
func firstCtrl(buf []byte, lo, end int, s scanner) int {
	var tail [64]byte
	for off := lo; off < end; off += 64 {
		blk, _ := block(buf, off, &tail)
		m := classify(blk)
		_, inString := s.next(m)
		if bad := m.ctrl & inString; bad != 0 {
			return off + bits.TrailingZeros64(bad)
		}
	}
	return math.MaxInt
}

// firstBadUTF8 returns the offset of the first invalid UTF-8 byte at or
// after from (which starts a character) and before end, or math.MaxInt. A
// character that starts before end may run past it.
func firstBadUTF8(buf []byte, from, end int) int {
	for i := from; i < end; {
		r, n := utf8.DecodeRune(buf[i:])
		if r == utf8.RuneError && n == 1 {
			return i
		}
		i += n
	}
	return math.MaxInt
}

// runeStart backs i up to the first byte of the UTF-8 character holding it
// (at most 3 bytes back).
func runeStart(buf []byte, i int) int {
	for k := 0; k < 3 && i > 0 && i < len(buf) && buf[i]&0xC0 == 0x80; k++ {
		i--
	}
	return i
}
