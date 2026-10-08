package ondemand

import (
	"encoding/binary"
	"math"

	"simdjson-go/internal/jsonerr"
	"simdjson-go/internal/number"
)

// The parsers below port C++ simdjson's On-Demand number parsing
// (include/simdjson/generic/numberparsing.h: parse_unsigned, parse_integer,
// parse_double, get_number_type and check_if_integer). Each reads the
// number at the start of s; bytes past the end of s read as pad: 0 for a
// value inside the document (C++'s padding), ' ' for a root scalar (C++
// copies those to a space-padded buffer).

func at(s []byte, i int, pad byte) byte {
	if i < len(s) {
		return s[i]
	}
	return pad
}

// finisher is C++ integer_string_finisher: what may follow an integer.
func finisher(c byte) error {
	switch c {
	case ' ', '\t', '\n', '\r', ',', ':', '[', ']', '{', '}':
		return nil
	case '.', 'e', 'E':
		return jsonerr.ErrIncorrectType
	}
	return jsonerr.ErrNumber
}

func digits(s []byte, i int, pad byte) (n uint64, end int) {
	for number.IsDigit(at(s, i, pad)) {
		n = 10*n + uint64(s[i]-'0') // may wrap; the digit count decides
		i++
	}
	return n, i
}

func parseUnsigned(s []byte, pad byte) (uint64, error) {
	n, p := digits(s, 0, pad)
	if p == 0 || p > 20 {
		return 0, jsonerr.ErrIncorrectType
	}
	if s[0] == '0' && p > 1 {
		return 0, jsonerr.ErrNumber
	}
	if err := finisher(at(s, p, pad)); err != nil {
		return 0, err
	}
	if p == 20 && (s[0] != '1' || n <= math.MaxInt64) {
		return 0, jsonerr.ErrIncorrectType
	}
	return n, nil
}

func parseInteger(s []byte, pad byte) (int64, error) {
	neg := at(s, 0, pad) == '-'
	start := 0
	if neg {
		start = 1
	}
	n, p := digits(s, start, pad)
	if p == start || p-start > 19 {
		return 0, jsonerr.ErrIncorrectType
	}
	if s[start] == '0' && p-start > 1 {
		return 0, jsonerr.ErrNumber
	}
	if err := finisher(at(s, p, pad)); err != nil {
		return 0, err
	}
	if neg {
		if n > math.MaxInt64+1 {
			return 0, jsonerr.ErrIncorrectType
		}
		return int64(-n), nil
	}
	if n > math.MaxInt64 {
		return 0, jsonerr.ErrIncorrectType
	}
	return int64(n), nil
}

// parseDouble follows C++ parse_double's grammar and errors; the value is
// the correctly rounded float of the text, as C++'s is.
func parseDouble(s []byte, pad byte) (float64, error) {
	p := 0
	neg := at(s, 0, pad) == '-'
	if neg {
		p = 1
	}
	intStart := p
	// mant accumulates every digit (it may wrap); with at most 19 digits
	// it is exact.
	var mant uint64
	for p < len(s) && s[p]-'0' < 10 {
		mant = 10*mant + uint64(s[p]-'0')
		p++
	}
	if p == intStart {
		return 0, jsonerr.ErrIncorrectType
	}
	if s[intStart] == '0' && p-intStart > 1 {
		return 0, jsonerr.ErrNumber
	}
	nDigits := p - intStart
	var exp10 int64
	if at(s, p, pad) == '.' {
		p++
		fracStart := p
		for p+8 <= len(s) && isEightDigits(s[p:]) {
			mant = mant*100000000 + parseEightDigits(s[p:])
			p += 8
		}
		for p < len(s) && s[p]-'0' < 10 {
			mant = 10*mant + uint64(s[p]-'0')
			p++
		}
		if p == fracStart {
			return 0, jsonerr.ErrNumber
		}
		nDigits += p - fracStart
		exp10 = int64(fracStart - p)
	}
	if c := at(s, p, pad); c == 'e' || c == 'E' {
		p++
		expNeg := at(s, p, pad) == '-'
		if expNeg || at(s, p, pad) == '+' {
			p++
		}
		expStart := p
		var e int64
		for p < len(s) && s[p]-'0' < 10 {
			if e <= 1<<32 { // saturate: past ±400 the value is 0 or ±Inf anyway
				e = 10*e + int64(s[p]-'0')
			}
			p++
		}
		if p == expStart || p-expStart > 19 {
			return 0, jsonerr.ErrNumber
		}
		if expNeg {
			e = -e
		}
		exp10 += e
	}
	if !number.IsStructuralOrSpace[at(s, p, pad)] {
		return 0, jsonerr.ErrNumber
	}
	f, ok := number.ToFloat64(s[:p], mant, exp10, neg, nDigits > 19 && significantDigits(s[intStart:p]) > 19)
	if !ok {
		return 0, jsonerr.ErrNumber
	}
	return f, nil
}

// significantDigits counts the digits of a number's significand from the
// first nonzero one (num holds the digits and maybe a '.' and exponent).
func significantDigits(num []byte) int {
	n, started := 0, false
	for _, c := range num {
		switch {
		case c == '.':
		case c-'0' >= 10:
			return n
		case started || c != '0':
			started = true
			n++
		}
	}
	return n
}

// isEightDigits is C++ is_made_of_eight_digits_fast: b starts with 8 ASCII digits.
func isEightDigits(b []byte) bool {
	v := binary.LittleEndian.Uint64(b)
	return (v&0xF0F0F0F0F0F0F0F0)|(((v+0x0606060606060606)&0xF0F0F0F0F0F0F0F0)>>4) == 0x3333333333333333
}

// parseEightDigits is C++ parse_eight_digits_unrolled: the value of the 8
// ASCII digits b starts with.
func parseEightDigits(b []byte) uint64 {
	const mask, mul1, mul2 = 0x000000FF000000FF, 0x000F424000000064, 0x0000271000000001
	v := binary.LittleEndian.Uint64(b) - 0x3030303030303030
	v = v*10 + v>>8
	return ((v&mask)*mul1 + (v>>16&mask)*mul2) >> 32
}

// numberType is C++ get_number_type: it classifies without validating
// beyond the leading digits.
func numberType(s []byte, pad byte) (NumberType, error) {
	neg := at(s, 0, pad) == '-'
	p := 0
	if neg {
		p = 1
	}
	start := p
	for number.IsDigit(at(s, p, pad)) {
		p++
	}
	n := p - start
	if n == 0 {
		return 0, jsonerr.ErrNumber
	}
	if !number.IsStructuralOrSpace[at(s, p, pad)] {
		return Float64, nil
	}
	d := string(s[start:p])
	const minBig = "9223372036854775808" // 2**63
	switch {
	case n > 20:
		return BigInt, nil
	case neg:
		if n > 19 || n == 19 && d > minBig {
			return BigInt, nil
		}
		return Int64, nil
	case n == 20 && d >= "18446744073709551616": // 2**64
		return BigInt, nil
	case n == 20 || n == 19 && d >= minBig:
		return Uint64, nil
	}
	return Int64, nil
}

// checkIfInteger is C++ check_if_integer, for root numbers too long to copy.
func checkIfInteger(s []byte) bool {
	p := 0
	if at(s, 0, 0) == '-' {
		p = 1
	}
	if p == len(s) {
		return false
	}
	if s[p] == '0' {
		p++
		return p == len(s) || number.IsStructuralOrSpace[s[p]]
	}
	for p < len(s) && number.IsDigit(s[p]) {
		p++
	}
	return p == len(s) || number.IsStructuralOrSpace[s[p]]
}
