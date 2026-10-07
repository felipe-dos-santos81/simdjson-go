package simdjson

import (
	"encoding/binary"
	"math"
	"strconv"
	"unsafe"
)

func isDigit(c byte) bool { return c-'0' < 10 }

// number parses the number at buf[off] and appends it to the tape. Port of
// parse_number (include/simdjson/generic/numberparsing.h). Floats are
// converted by strconv.ParseFloat (Eisel-Lemire) once the JSON grammar is
// checked; it fails only on overflow to ±Inf, which C++ also rejects.
func (b *builder) number(off int) error {
	buf := b.buf
	p := off
	neg := p < len(buf) && buf[p] == '-' // off == len(buf) past the last structural
	if neg {
		p++
	}
	start := p
	var i uint64
	for p < len(buf) && isDigit(buf[p]) {
		i = 10*i + uint64(buf[p]-'0') // may wrap; the digit count decides below
		p++
	}
	digits := p - start
	if digits == 0 || (buf[start] == '0' && digits > 1) {
		return ErrNumber
	}
	isFloat := false
	if p < len(buf) && buf[p] == '.' {
		isFloat = true
		p++
		frac := p
		for p < len(buf) && isDigit(buf[p]) {
			p++
		}
		if p == frac {
			return ErrNumber
		}
	}
	if p < len(buf) && (buf[p] == 'e' || buf[p] == 'E') {
		isFloat = true
		p++
		if p < len(buf) && (buf[p] == '-' || buf[p] == '+') {
			p++
		}
		exp := p
		for p < len(buf) && isDigit(buf[p]) {
			p++
		}
		if p == exp {
			return ErrNumber
		}
	}
	if isFloat {
		f, err := strconv.ParseFloat(unsafe.String(&buf[off], p-off), 64)
		if err != nil || !terminates(buf, p) {
			return ErrNumber
		}
		b.tape = append(b.tape, word(tagDouble, 0), math.Float64bits(f))
		return nil
	}

	// Integers. As in C++, a too-long integer is reported before its terminator is checked.
	longest := 20
	if neg {
		longest = 19
	}
	if digits > longest ||
		digits == longest && neg && i > math.MaxInt64+1 ||
		digits == longest && !neg && (buf[start] != '1' || i <= math.MaxInt64) { // wrapped
		return b.bigInt(off, p)
	}
	if !terminates(buf, p) {
		return ErrNumber
	}
	switch {
	case neg:
		b.tape = append(b.tape, word(tagInt64, 0), -i) // two's complement; -2^63 included
	case i > math.MaxInt64:
		b.tape = append(b.tape, word(tagUint64, 0), i)
	default:
		b.tape = append(b.tape, word(tagInt64, 0), i)
	}
	return nil
}

// bigInt handles the integer buf[off:p] outside int64/uint64: ErrBigInt, or
// with BigIntAsString its raw digits stored like a string under tagBigInt.
func (b *builder) bigInt(off, p int) error {
	if !b.bigIntAsString {
		return ErrBigInt
	}
	if !terminates(b.buf, p) {
		return ErrNumber
	}
	start := len(b.strs)
	b.strs = binary.LittleEndian.AppendUint32(b.strs, uint32(p-off))
	b.strs = append(b.strs, b.buf[off:p]...)
	b.strs = append(b.strs, 0)
	b.tape = append(b.tape, word(tagBigInt, uint64(start)))
	return nil
}
