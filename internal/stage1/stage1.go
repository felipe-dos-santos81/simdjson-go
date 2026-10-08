// Package stage1 finds the structural characters of a JSON document.
// It is a port of simdjson's src/generic/stage1 (json_escape_scanner.h,
// json_string_scanner.h, json_scanner.h, json_structural_indexer.h).
package stage1

import (
	"errors"
	"math"
	"math/bits"
)

// Errors returned by Index, in the order simdjson checks them.
var (
	ErrUnclosedString = errors.New("simdjson: unclosed string")                       // UNCLOSED_STRING
	ErrUnescapedChars = errors.New("simdjson: unescaped control character in string") // UNESCAPED_CHARS
	ErrEmpty          = errors.New("simdjson: no JSON found")                         // EMPTY
	ErrUTF8           = errors.New("simdjson: invalid UTF-8")                         // UTF8_ERROR
)

// scanner carries state from one 64-byte block to the next.
type scanner struct {
	nextIsEscaped uint64 // 1 if the first byte of the next block is escaped
	prevInString  uint64 // all ones if the previous block ended inside a string
	prevScalar    uint64 // 1 if the previous block ended with a non-quote scalar byte
	unescaped     uint64 // accumulated control characters found inside strings
}

// next consumes one block and returns its structural characters (operators and
// the first byte of every scalar, including opening quotes) and its in-string mask
// (bytes inside strings, including the opening but not the closing quote).
func (s *scanner) next(m masks) (structurals, inString uint64) {
	// json_escape_scanner: which bytes are escaped by a backslash.
	var escaped uint64
	if m.backslash == 0 {
		escaped = s.nextIsEscaped
		s.nextIsEscaped = 0
	} else {
		const oddBits = 0xAAAAAAAAAAAAAAAA
		potential := m.backslash &^ s.nextIsEscaped
		escapeAndTerminal := ((potential<<1 | oddBits) - potential) ^ oddBits
		escaped = escapeAndTerminal ^ (m.backslash | s.nextIsEscaped)
		s.nextIsEscaped = (escapeAndTerminal & m.backslash) >> 63
	}

	// json_string_scanner: real quotes and the bytes between them.
	quote := m.quote &^ escaped
	inString = prefixXor(quote) ^ s.prevInString
	s.prevInString = uint64(int64(inString) >> 63)
	s.unescaped |= m.ctrl & inString

	// json_scanner: a scalar starts where a non-quote scalar byte does not precede it.
	scalar := ^(m.op | m.ws)
	nonQuoteScalar := scalar &^ quote
	followsNonQuoteScalar := nonQuoteScalar<<1 | s.prevScalar
	s.prevScalar = nonQuoteScalar >> 63
	scalarStart := scalar &^ followsNonQuoteScalar
	stringTail := inString ^ quote
	return (m.op | scalarStart) &^ stringTail, inString
}

// prefixXor sets bit i to the XOR of bits 0..i (6-step shift cascade).
func prefixXor(x uint64) uint64 {
	x ^= x << 1
	x ^= x << 2
	x ^= x << 4
	x ^= x << 8
	x ^= x << 16
	x ^= x << 32
	return x
}

// block returns the 64-byte block at src[off:] and the number of real bytes in
// it. A final partial block is copied into tail and padded with spaces.
func block(src []byte, off int, tail *[64]byte) (*[64]byte, int) {
	if len(src)-off >= 64 {
		return (*[64]byte)(src[off : off+64]), 64
	}
	n := copy(tail[:], src[off:])
	for i := n; i < 64; i++ {
		tail[i] = ' '
	}
	return tail, n
}

// Index appends to idx[:0] the offset of every structural character of buf and
// returns it. Errors are checked in simdjson's order: unclosed string, control
// character inside a string, empty document, invalid UTF-8.
func Index(buf []byte, idx []uint32) ([]uint32, error) {
	idx = idx[:0]
	if len(buf) == 0 {
		return idx, ErrEmpty
	}
	if cap(idx) == 0 {
		// The jsonexamples corpus has 3 to 44 bytes per structural: most
		// documents never grow this, dense numeric arrays grow it once or
		// twice, and it reserves an eighth of the one-per-byte worst case.
		idx = make([]uint32, 0, len(buf)/8+64)
	}
	var st Stream
	st.Reset(buf)
	idx = st.Next(len(buf), idx)
	switch {
	case st.Unclosed():
		return idx, ErrUnclosedString
	case st.BadCtrl != math.MaxInt:
		return idx, ErrUnescapedChars
	case len(idx) == 0:
		return idx, ErrEmpty
	case st.BadUTF8 != math.MaxInt:
		return idx, ErrUTF8
	}
	return idx, nil
}

// Minify appends src to dst with all whitespace outside strings removed.
// Like C++ simdjson::minify it does not validate the document; its only
// error is ErrUnclosedString.
func Minify(dst, src []byte) ([]byte, error) {
	var (
		s    scanner
		tail [64]byte
	)
	for off := 0; off < len(src); off += 64 {
		blk, n := block(src, off, &tail)
		m := classify(blk)
		_, inString := s.next(m)
		keep := ^(m.ws &^ inString)
		if n < 64 {
			keep &= 1<<n - 1
		}
		for keep != 0 { // copy each run of kept bytes
			i := bits.TrailingZeros64(keep)
			run := bits.TrailingZeros64(^(keep >> i))
			dst = append(dst, blk[i:i+run]...)
			keep &^= (1<<run - 1) << i
		}
	}
	if s.prevInString != 0 {
		return dst, ErrUnclosedString
	}
	return dst, nil
}
