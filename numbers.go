package simdjson

import (
	"encoding/binary"
	"math"
	"strconv"
	"unsafe"

	"simdjson-go/internal/number"
)

// maxExp10 bounds the decimal exponents number passes to
// number.DecimalToFloat64: past ±maxExp10 every 19-digit significand
// underflows to ±0 (below -345) or overflows (above 310), so larger
// exponents need not be kept exactly.
const maxExp10 = 400

// number parses the number at buf[off] and appends it to the tape. Port of
// parse_number (include/simdjson/generic/numberparsing.h). While checking the
// JSON grammar it collects a float's significand (up to 19 significant digits)
// and decimal exponent for number.DecimalToFloat64; a float whose dropped digits are
// not all zeros falls back to strconv.ParseFloat. Both fail only on overflow to
// ±Inf, which C++ also rejects.
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
		for p < len(buf) && number.IsDigit(buf[p]) {
			if sig < 19 {
				mant = 10*mant + uint64(buf[p]-'0')
				exp10--
				if mant != 0 {
					sig++
				}
			} else if buf[p] != '0' {
				trunc = true // a dropped zero does not change the value
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
		for p < len(buf) && number.IsDigit(buf[p]) {
			// Saturate above maxSize+maxExp10: leading fraction zeros can lower
			// exp10 by up to maxSize, and an exponent must still cancel them.
			if e <= maxSize+maxExp10 {
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
		var f float64
		var ok bool
		if trunc {
			var err error
			f, err = strconv.ParseFloat(unsafe.String(&buf[off], p-off), 64)
			ok = err == nil
		} else {
			// Clamping to ±maxExp10 keeps the result (see maxExp10) and the
			// int64 exponent within int on 32-bit builds.
			f, ok = number.DecimalToFloat64(mant, int(max(-maxExp10, min(maxExp10, exp10))), neg)
		}
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
