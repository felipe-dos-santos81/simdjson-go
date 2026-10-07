package simdjson

import (
	"errors"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
)

// decimalToFloat64 must agree bit for bit with strconv.ParseFloat, including
// signed zeros, subnormals, rounding ties and overflow.
func TestDecimalToFloat64(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	check := func(d uint64, p int, neg bool) {
		s := strconv.FormatUint(d, 10) + "e" + strconv.Itoa(p)
		if neg {
			s = "-" + s
		}
		want, err := strconv.ParseFloat(s, 64)
		got, ok := decimalToFloat64(d, p, neg)
		if wantOK := !errors.Is(err, strconv.ErrRange); ok != wantOK || ok && math.Float64bits(got) != math.Float64bits(want) {
			t.Fatalf("%s: got %v (ok %v), want %v (%v)", s, got, ok, want, err)
		}
	}
	for _, d := range []uint64{0, 1, 2, 5, 9007199254740993, 1<<53 + 1, 9999999999999999999, math.MaxUint64 / 10} {
		for p := -400; p <= 400; p++ {
			check(d, p, false)
			check(d, p, true)
		}
	}
	for range 1_000_000 {
		d := r.Uint64N(uint64(math.Pow10(1 + r.IntN(19))))
		check(d, r.IntN(700)-360, r.IntN(2) == 0)
	}
}

// Through the number scanner: leading zeros, long fractions (the strconv
// fallback), exponent forms and boundaries must match strconv.ParseFloat.
func TestFloatScanning(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	digits := func(n int) string {
		var b strings.Builder
		for range n {
			b.WriteByte(byte('0' + r.IntN(10)))
		}
		return b.String()
	}
	inputs := []string{
		"0.0", "-0.0", "0e5", "1e308", "1.7976931348623157e308", "1.7976931348623159e308",
		"4.9e-324", "2.4703282292062327e-324", "2.4703282292062328e-324", "1e-400", "-1e-400",
		"0.000000000000000000000000000001", "123456789012345678901234567890e-10",
		"1." + strings.Repeat("0", 40), "9007199254740993." + strings.Repeat("0", 30), // dropped zeros
		"0." + strings.Repeat("0", 5000) + "1e5001", // a zero run cancelled by the exponent
		"9007199254740993.0", "0.1", "3.141592653589793238462643383279",
		"1" + strings.Repeat("0", 400) + "e-400", "0." + strings.Repeat("0", 400) + "1e400",
	}
	for range 200_000 {
		in := digits(1 + r.IntN(25))
		if len(in) > 1 && in[0] == '0' {
			in = "1" + in[1:]
		}
		frac, exp := r.IntN(2) == 0, r.IntN(2) == 0
		if !frac && !exp { // a float needs a fraction or an exponent
			frac = true
		}
		if frac {
			in += "." + digits(1+r.IntN(25))
		}
		if exp {
			in += "e" + []string{"", "+", "-"}[r.IntN(3)] + strconv.Itoa(r.IntN(400))
		}
		if r.IntN(2) == 0 {
			in = "-" + in
		}
		inputs = append(inputs, in)
	}
	var p Parser
	for _, in := range inputs {
		want, err := strconv.ParseFloat(in, 64)
		doc, perr := p.Parse([]byte(in))
		if err != nil {
			if !errors.Is(perr, ErrNumber) {
				t.Fatalf("%s: err = %v, want ErrNumber (strconv: %v)", in, perr, err)
			}
			continue
		}
		if perr != nil {
			t.Fatalf("%s: %v", in, perr)
		}
		got, _ := doc.Root().Float64()
		if math.Float64bits(got) != math.Float64bits(want) {
			t.Fatalf("%s: got %v, want %v", in, got, want)
		}
	}
}
