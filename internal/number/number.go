// Package number holds what stage 2 and package ondemand share for parsing
// JSON numbers: the decimal-to-float64 conversion and the terminator rules.
package number

import (
	"strconv"
	"unsafe"
)

// IsStructuralOrSpace is C++ structural_or_whitespace: the bytes that may
// follow a number or literal.
var IsStructuralOrSpace = [256]bool{
	' ': true, '\t': true, '\n': true, '\r': true,
	',': true, ':': true, '[': true, ']': true, '{': true, '}': true,
}

// Terminates reports whether a number or literal ending at buf[p] is properly
// terminated: by the end of the input, whitespace or a structural character.
func Terminates(buf []byte, p int) bool { return p == len(buf) || IsStructuralOrSpace[buf[p]] }

// IsDigit reports whether c is an ASCII digit.
func IsDigit(c byte) bool { return c-'0' < 10 }

// MaxExp10 bounds the decimal exponents ToFloat64 passes to
// DecimalToFloat64: past ±MaxExp10 every 19-digit significand underflows to
// ±0 (below -345) or overflows (above 310).
const MaxExp10 = 400

// ToFloat64 converts the float written as text, whose significand digits,
// read in order into mant, make its value mant * 10**exp10, negated if neg;
// if trunc (more than 19 significant digits, so mant may have wrapped), text
// is parsed instead, as C++ does. Callers test trunc as
// nDigits > 19 && SignificantDigits(digits) > 19, inline: slicing digits
// for every float costs canada.json 1%. ok is false if the result rounds to ±Inf.
func ToFloat64(text []byte, mant uint64, exp10 int64, neg, trunc bool) (f float64, ok bool) {
	if trunc {
		f, err := strconv.ParseFloat(unsafe.String(unsafe.SliceData(text), len(text)), 64)
		return f, err == nil
	}
	// Clamping to ±MaxExp10 keeps the result (see MaxExp10) and the int64
	// exponent within int on 32-bit builds.
	return DecimalToFloat64(mant, int(max(-MaxExp10, min(MaxExp10, exp10))), neg)
}
