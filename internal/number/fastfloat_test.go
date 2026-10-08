package number

import (
	"errors"
	"math"
	"math/rand/v2"
	"strconv"
	"testing"
)

// DecimalToFloat64 must agree bit for bit with strconv.ParseFloat, including
// signed zeros, subnormals, rounding ties and overflow.
func TestDecimalToFloat64(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	check := func(d uint64, p int, neg bool) {
		s := strconv.FormatUint(d, 10) + "e" + strconv.Itoa(p)
		if neg {
			s = "-" + s
		}
		want, err := strconv.ParseFloat(s, 64)
		got, ok := DecimalToFloat64(d, p, neg)
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
