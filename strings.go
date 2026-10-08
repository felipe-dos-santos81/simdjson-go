package simdjson

import (
	"encoding/binary"

	"simdjson-go/internal/str"
)

// str unescapes the string whose opening quote is at buf[off] into the
// string buffer (4-byte little-endian length, bytes, NUL) and appends its
// tape word. Port of src/generic/stage2/stringparsing.h.
func (b *builder) str(off int) error {
	start := len(b.strs)
	b.strs = append(b.strs, 0, 0, 0, 0) // length, patched below
	var ok bool
	if b.strs, ok = str.AppendUnescaped(b.strs, b.buf, off+1); !ok {
		return ErrString
	}
	binary.LittleEndian.PutUint32(b.strs[start:], uint32(len(b.strs)-start-4))
	b.strs = append(b.strs, 0)
	b.tape = append(b.tape, word(tagString, uint64(start)))
	return nil
}
