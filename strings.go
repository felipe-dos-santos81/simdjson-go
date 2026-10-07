package simdjson

import (
	"encoding/binary"
	"unicode/utf8"
)

// str unescapes the string whose opening quote is at buf[off] into the
// string buffer (4-byte little-endian length, bytes, NUL) and appends its
// tape word. Port of src/generic/stage2/stringparsing.h.
func (b *builder) str(off int) error {
	start := len(b.strs)
	b.strs = append(b.strs, 0, 0, 0, 0) // length, patched below
	var ok bool
	if b.strs, ok = appendUnescaped(b.strs, b.buf, off+1); !ok {
		return ErrString
	}
	binary.LittleEndian.PutUint32(b.strs[start:], uint32(len(b.strs)-start-4))
	b.strs = append(b.strs, 0)
	b.tape = append(b.tape, word(tagString, uint64(start)))
	return nil
}

var escapeMap = [256]byte{
	'"': '"', '\\': '\\', '/': '/',
	'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t',
}

// appendUnescaped appends the unescaped string starting at src[i] (just after
// the opening quote) up to its closing quote. It reports false for an invalid
// escape or a missing closing quote.
func appendUnescaped(dst, src []byte, i int) ([]byte, bool) {
	for {
		n := indexQuoteOrBackslash(src[i:])
		if n < 0 {
			return dst, false
		}
		dst = append(dst, src[i:i+n]...)
		i += n
		if src[i] == '"' {
			return dst, true
		}
		if i+1 >= len(src) {
			return dst, false
		}
		if src[i+1] == 'u' {
			var ok bool
			if dst, i, ok = appendCodePoint(dst, src, i); !ok {
				return dst, false
			}
			continue
		}
		r := escapeMap[src[i+1]]
		if r == 0 {
			return dst, false
		}
		dst = append(dst, r)
		i += 2
	}
}

// indexQuoteOrBackslash returns the index of the first '"' or '\\' in s, or
// -1. It skips 8 bytes at a time with the SWAR has-zero-byte test.
func indexQuoteOrBackslash(s []byte) int {
	const lo, hi = 0x0101010101010101, 0x8080808080808080
	i := 0
	for ; i+8 <= len(s); i += 8 {
		w := binary.LittleEndian.Uint64(s[i:])
		q, bs := w^('"'*lo), w^('\\'*lo)
		if ((q-lo)&^q|(bs-lo)&^bs)&hi != 0 {
			break
		}
	}
	for ; i < len(s); i++ {
		if s[i] == '"' || s[i] == '\\' {
			return i
		}
	}
	return -1
}

// appendCodePoint decodes the \uXXXX escape at src[i] (combining a surrogate
// pair), appends it as UTF-8 and returns the index after the escape. Lone or
// malformed surrogates and bad hex digits are errors, as in C++.
func appendCodePoint(dst, src []byte, i int) ([]byte, int, bool) {
	cp, ok := hex4(src, i+2)
	if !ok {
		return dst, i, false
	}
	i += 6
	switch {
	case cp >= 0xD800 && cp < 0xDC00:
		if i+1 >= len(src) || src[i] != '\\' || src[i+1] != 'u' {
			return dst, i, false
		}
		low, ok := hex4(src, i+2)
		if !ok || low < 0xDC00 || low > 0xDFFF {
			return dst, i, false
		}
		cp = ((cp-0xD800)<<10 | (low - 0xDC00)) + 0x10000
		i += 6
	case cp >= 0xDC00 && cp <= 0xDFFF:
		return dst, i, false
	}
	return utf8.AppendRune(dst, rune(cp)), i, true
}

func hex4(src []byte, i int) (uint32, bool) {
	if i+4 > len(src) {
		return 0, false
	}
	var v uint32
	for _, c := range src[i : i+4] {
		switch {
		case '0' <= c && c <= '9':
			c -= '0'
		case 'a' <= c && c <= 'f':
			c -= 'a' - 10
		case 'A' <= c && c <= 'F':
			c -= 'A' - 10
		default:
			return 0, false
		}
		v = v<<4 | uint32(c)
	}
	return v, true
}
