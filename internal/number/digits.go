package number

// SignificantDigits counts the digits of a number's significand from the
// first nonzero one (num holds the digits and maybe a '.' and exponent).
func SignificantDigits(num []byte) int {
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

// IsEightDigits is C++ is_made_of_eight_digits_fast: v (8 bytes of input)
// holds 8 ASCII digits.
func IsEightDigits(v uint64) bool {
	return (v&0xF0F0F0F0F0F0F0F0)|(((v+0x0606060606060606)&0xF0F0F0F0F0F0F0F0)>>4) == 0x3333333333333333
}

// ParseEightDigits is C++ parse_eight_digits_unrolled: the value of 8
// digits, given as 8 bytes of input minus '0' each (byte 0 is the most
// significant digit).
func ParseEightDigits(v uint64) uint64 {
	const mask, mul1, mul2 = 0x000000FF000000FF, 0x000F424000000064, 0x0000271000000001
	v = v*10 + v>>8
	return ((v&mask)*mul1 + (v>>16&mask)*mul2) >> 32
}

// Pow10Uint64[n] is 10**n.
var Pow10Uint64 = [9]uint64{1, 10, 100, 1000, 10000, 100000, 1000000, 10000000, 100000000}
