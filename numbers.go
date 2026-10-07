package simdjson

import (
	"encoding/binary"
	"math"
	"strconv"
	"unsafe"
)

func isDigit(c byte) bool { return c-'0' < 10 }

// number parses the number at buf[off] and appends it to the tape. Port of
// parse_number (include/simdjson/generic/numberparsing.h). While checking the
// JSON grammar it collects a float's significand (up to 19 significant digits)
// and decimal exponent for decimalToFloat64; longer significands fall back to
// strconv.ParseFloat. Both fail only on overflow to ±Inf, which C++ also rejects.
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
	// A float is mant * 10**exp10, where mant holds at most 19 significant
	// digits; trunc records that more were dropped.
	mant, sig, exp10, trunc := i, digits, int64(0), digits > 19
	if i == 0 {
		sig = 0 // the integer part is a single '0'
	}
	isFloat := false
	if p < len(buf) && buf[p] == '.' {
		isFloat = true
		p++
		frac := p
		for p < len(buf) && isDigit(buf[p]) {
			if sig < 19 {
				mant = 10*mant + uint64(buf[p]-'0')
				exp10--
				if mant != 0 {
					sig++
				}
			} else {
				trunc = true
			}
			p++
		}
		if p == frac {
			return ErrNumber
		}
	}
	if p < len(buf) && (buf[p] == 'e' || buf[p] == 'E') {
		isFloat = true
		p++
		expNeg := p < len(buf) && buf[p] == '-'
		if p < len(buf) && (buf[p] == '-' || buf[p] == '+') {
			p++
		}
		expStart, e := p, int64(0)
		for p < len(buf) && isDigit(buf[p]) {
			if e < 1e12 { // saturate: far beyond any float, and no int64 overflow
				e = 10*e + int64(buf[p]-'0')
			}
			p++
		}
		if p == expStart {
			return ErrNumber
		}
		if expNeg {
			e = -e
		}
		exp10 += e
	}
	if isFloat {
		if !terminates(buf, p) {
			return ErrNumber
		}
		var f float64
		var ok bool
		if trunc {
			var err error
			f, err = strconv.ParseFloat(unsafe.String(&buf[off], p-off), 64)
			ok = err == nil
		} else {
			// Past ±400 every 19-digit mant underflows or overflows anyway.
			f, ok = decimalToFloat64(mant, int(max(-400, min(400, exp10))), neg)
		}
		if !ok {
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
