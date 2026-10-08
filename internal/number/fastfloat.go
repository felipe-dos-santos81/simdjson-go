// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE-GO file.

// Decimal to float64 conversion by fast unrounded scaling, adapted from the Go
// standard library (src/internal/strconv/uscale.go: parseFloat64 and the
// helpers it uses); see https://research.swtch.com/fp. Callers have already
// scanned the digits, so it calls this instead of strconv.ParseFloat, which
// would scan them again.
//
// Renamed from upstream for readability: bool2 → btoi, pmHiLo → pow10Entry,
// unmin → minUnrounded. Everything else keeps its upstream name and shape so
// the code can be diffed against uscale.go.

package number

import (
	"math"
	"math/bits"
)

// DecimalToFloat64 rounds d * 10**p to the nearest float64, negated if neg.
// d can have at most 19 digits. ok is false if the result rounds to ±Inf.
func DecimalToFloat64(d uint64, p int, neg bool) (f float64, ok bool) {
	sign := btoi[uint64](neg) << 63
	switch {
	case d == 0, p < -345: // zero, or d < 1e19 underflows to ±0
		return math.Float64frombits(sign), true
	case p > 310: // d ≥ 1 overflows
		return 0, false
	}
	b := bits.Len64(d)
	lp := log2Pow10(p)
	e := min(1074, 53-b-lp)
	var pre scaler
	prescale(&pre, e-(64-b), p, lp)
	if pre.s >= 64 {
		return math.Float64frombits(sign), true
	}
	u := uscale(d<<(64-b), &pre)

	// This block is branch-free code for:
	//	if u.round() >= 1<<53 {
	//		u = u.rsh(1)
	//		e = e - 1
	//	}
	s := btoi[int](u >= minUnrounded(1<<53))
	u = u>>s | u&1
	e = e - s

	return pack64(sign|u.round(), -e)
}

// btoi converts b to an integer: 1 for true, 0 for false.
func btoi[T ~int | ~uint64](b bool) T {
	if b {
		return 1
	}
	return 0
}

// pack64 takes m, e and returns f = m * 2**e, or ok false if it is ±Inf.
// It assumes a 53-bit mantissa m and an exponent in range for it.
func pack64(m uint64, e int) (float64, bool) {
	if m&(1<<52) == 0 {
		return math.Float64frombits(m), true
	}
	if e >= 0x7FF-1075 {
		return 0, false
	}
	return math.Float64frombits(m&^(1<<52) | uint64(1075+e)<<52), true
}

// An unrounded represents an unrounded value: the value times 4, with the
// low bit set if any discarded bits were non-zero.
type unrounded uint64

func (u unrounded) round() uint64 { return uint64((u + 1 + (u>>2)&1) >> 2) }

// minUnrounded returns the minimum unrounded that rounds to x.
func minUnrounded(x uint64) unrounded { return unrounded(x<<2 - 2) }

// log2Pow10(x) returns ⌊log₂ 10**x⌋ = ⌊x * log₂ 10⌋.
func log2Pow10(x int) int {
	// log₂ 10 ≈ 3.32192809489 ≈ 108853 / 2^15
	return (x * 108853) >> 15
}

// A pow10Entry is a 128-bit power-of-ten mantissa, stored as hi<<64 - lo.
type pow10Entry struct {
	hi uint64
	lo uint64
}

// A scaler holds derived scaling constants for a given e, p pair.
type scaler struct {
	pmHi uint64
	pmLo uint64
	s    int
}

// prescale sets the scaling constants for e, p; lp must be log2Pow10(p).
func prescale(pre *scaler, e, p, lp int) {
	pre.pmHi = pow10Tab[p-pow10Min].hi
	pre.pmLo = pow10Tab[p-pow10Min].lo
	pre.s = -(e + lp + 3)
}

// uscale returns unround(x * 2**e * 10**p) for the e, p given to prescale.
// x must be left-justified (high bit set) and c.s must be below 64.
func uscale(x uint64, c *scaler) unrounded {
	hi, mid := bits.Mul64(x, c.pmHi)
	s := c.s & 63 // make shifts cheaper
	if hi>>s<<s != hi {
		return unrounded(hi>>s | 1)
	}
	mid2, _ := bits.Mul64(x, c.pmLo)
	hi -= btoi[uint64](mid < mid2)
	return unrounded(hi>>s | btoi[uint64](mid-mid2 > 1))
}
