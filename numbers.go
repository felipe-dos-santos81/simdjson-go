package simdjson

import (
	"encoding/binary"
	"math"
	"math/bits"

	"simdjson-go/internal/number"
)

// number parses the number at buf[off] and appends it to the tape. Port of
// parse_number (include/simdjson/generic/numberparsing.h). While checking the
// JSON grammar it collects a float's digits (eight at a time in the
// fraction, as C++) and decimal exponent for number.DecimalToFloat64; a float
// with more than 19 significant digits falls back to strconv.ParseFloat. Both
// fail only on overflow to ±Inf, which C++ also rejects.
// Measured slower (2026-10-08): number.Digits for the integer part, and the
// truncation test anywhere but inline here.
func (b *builder) number(off int) error {
	buf := b.buf
	p := off
	neg := p < len(buf) && buf[p] == '-' // off == len(buf) past the last structural
	if neg {
		p++
	}
	// mant collects every digit and may wrap: an integer is checked by its
	// digit count, a float is mant * 10**exp10, exact unless it has more than
	// 19 significant digits (checked below).
	start := p
	var mant uint64
	for p < len(buf) && number.IsDigit(buf[p]) {
		mant = 10*mant + uint64(buf[p]-'0')
		p++
	}
	nDigits, exp10 := p-start, int64(0)
	if nDigits == 0 || (buf[start] == '0' && nDigits > 1) {
		return ErrNumber
	}
	isFloat := false
	if p < len(buf) && buf[p] == '.' {
		isFloat = true
		p++
		frac := p
		// The fraction, eight digits at a time where it can (C++
		// parse_number); mant may wrap. The same loop is in
		// ondemand.parseDouble; keep the two in step. A shared function
		// was 4% slower on canada.json.
		tail := false
		for p+8 <= len(buf) {
			v := binary.LittleEndian.Uint64(buf[p : p+8])
			if !number.IsEightDigits(v) {
				// Fewer than 8 digits left: take them all at once. The
				// value is what the digit-by-digit loop gives, wrapping
				// included.
				t := v ^ 0x3030303030303030
				n := bits.TrailingZeros64(((t+0x7676767676767676)|t)&0x8080808080808080) >> 3
				mant = mant*number.Pow10Uint64[n] + number.ParseEightDigits(t<<(64-8*n)) // n == 0 shifts by 64: 0
				p += n
				tail = true
				break
			}
			mant = mant*100000000 + number.ParseEightDigits(v-0x3030303030303030)
			p += 8
		}
		for !tail && p < len(buf) && number.IsDigit(buf[p]) { // within 8 bytes of the end
			mant = 10*mant + uint64(buf[p]-'0')
			p++
		}
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
			// Saturate above maxSize+number.MaxExp10: the fraction's digits
			// lower exp10 by up to maxSize, and an exponent must still cancel them.
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
		if !b.terminates(p) {
			return ErrNumber
		}
		f, ok := number.ToFloat64(buf[off:p], mant, exp10, neg, nDigits > 19 && number.SignificantDigits(buf[start:p]) > 19)
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
	if nDigits > longest ||
		nDigits == longest && neg && mant > math.MaxInt64+1 ||
		nDigits == longest && !neg && (buf[start] != '1' || mant <= math.MaxInt64) { // wrapped
		return b.bigInt(off, p)
	}
	if !b.terminates(p) {
		return ErrNumber
	}
	switch {
	case neg:
		b.tape = append(b.tape, word(tagInt64, 0), -mant) // two's complement; -2^63 included
	case mant > math.MaxInt64:
		b.tape = append(b.tape, word(tagUint64, 0), mant)
	default:
		b.tape = append(b.tape, word(tagInt64, 0), mant)
	}
	return nil
}

// bigInt handles the integer buf[off:p] outside int64/uint64: ErrBigInt, or
// with BigIntAsString its raw digits stored like a string under tagBigInt.
func (b *builder) bigInt(off, p int) error {
	if !b.bigIntAsString {
		return ErrBigInt
	}
	if !b.terminates(p) {
		return ErrNumber
	}
	start := len(b.strs)
	b.strs = binary.LittleEndian.AppendUint32(b.strs, uint32(p-off))
	b.strs = append(b.strs, b.buf[off:p]...)
	b.strs = append(b.strs, 0)
	b.tape = append(b.tape, word(tagBigInt, uint64(start)))
	return nil
}
