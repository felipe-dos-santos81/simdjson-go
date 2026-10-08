package simdjson

import (
	"encoding/binary"
	"math"

	"simdjson-go/internal/number"
)

// number parses the number at buf[off] and appends it to the tape. Port of
// parse_number (include/simdjson/generic/numberparsing.h). While checking the
// JSON grammar it collects a float's digits (eight at a time in the
// fraction, as C++) and decimal exponent for number.DecimalToFloat64; a float
// with more than 19 significant digits falls back to strconv.ParseFloat. Both
// fail only on overflow to ±Inf, which C++ also rejects.
func (b *builder) number(off int) error {
	buf := b.buf
	p := off
	neg := p < len(buf) && buf[p] == '-' // off == len(buf) past the last structural
	if neg {
		p++
	}
	start := p
	var i uint64
	for p < len(buf) && number.IsDigit(buf[p]) {
		i = 10*i + uint64(buf[p]-'0') // may wrap; the digit count decides below
		p++
	}
	digits := p - start
	if digits == 0 || (buf[start] == '0' && digits > 1) {
		return ErrNumber
	}
	// A float is mant * 10**exp10, where mant holds every digit; it is exact
	// unless there are more than 19 significant digits (checked below).
	mant, nDigits, exp10 := i, digits, int64(0)
	isFloat := false
	if p < len(buf) && buf[p] == '.' {
		isFloat = true
		p++
		frac := p
		p, mant = number.Digits(buf, p, mant)
		if p == frac {
			return ErrNumber
		}
		nDigits += p - frac
		exp10 = int64(frac - p)
	}
	if p < len(buf) && (buf[p] == 'e' || buf[p] == 'E') {
		isFloat = true
		p++
		expNeg := p < len(buf) && buf[p] == '-'
		if p < len(buf) && (buf[p] == '-' || buf[p] == '+') {
			p++
		}
		expStart, e := p, int64(0)
		for p < len(buf) && number.IsDigit(buf[p]) {
			// Saturate above maxSize+number.MaxExp10: leading fraction zeros can lower
			// exp10 by up to maxSize, and an exponent must still cancel them.
			if e <= maxSize+number.MaxExp10 {
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
		if !number.Terminates(buf, p) {
			return ErrNumber
		}
		trunc := nDigits > 19 && number.SignificantDigits(buf[start:p]) > 19
		f, ok := number.ToFloat64(buf[off:p], mant, exp10, neg, trunc)
		if !ok {
			if !b.binding {
				return ErrNumber
			}
			f = math.Inf(1) // v2 rejects the overflow only when it decodes the value
			if neg {
				f = -f
			}
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
	if !number.Terminates(buf, p) {
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
	if !number.Terminates(b.buf, p) {
		return ErrNumber
	}
	start := len(b.strs)
	b.strs = binary.LittleEndian.AppendUint32(b.strs, uint32(p-off))
	b.strs = append(b.strs, b.buf[off:p]...)
	b.strs = append(b.strs, 0)
	b.tape = append(b.tape, word(tagBigInt, uint64(start)))
	return nil
}
