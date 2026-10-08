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

// maxExp10 bounds the decimal exponents ToFloat64 passes to
// DecimalToFloat64: past ±maxExp10 every 19-digit significand underflows to
// ±0 (below -345) or overflows (above 310).
const maxExp10 = 400

// ToFloat64 converts the float written as text, whose significand (its
// first 19 significant digits, or all of them) is mant and whose value is
// mant * 10**exp10, negated if neg; trunc reports that significant digits
// were dropped from mant, and then text is parsed instead. ok is false if
// the result rounds to ±Inf.
func ToFloat64(text []byte, mant uint64, exp10 int64, neg, trunc bool) (f float64, ok bool) {
	if trunc {
		f, err := strconv.ParseFloat(unsafe.String(unsafe.SliceData(text), len(text)), 64)
		return f, err == nil
	}
	// Clamping to ±maxExp10 keeps the result (see maxExp10) and the int64
	// exponent within int on 32-bit builds.
	return DecimalToFloat64(mant, int(max(-maxExp10, min(maxExp10, exp10))), neg)
}
