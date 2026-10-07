// The string cache is adapted from the Go standard library
// (src/encoding/json/v2/intern.go), Copyright 2022 The Go Authors, under the
// BSD-style license in LICENSE-GO.

package simdjson

import (
	"encoding/binary"
	"math/bits"
)

// stringCache remembers recently decoded short strings, so repeated values
// (enum-like fields, map keys) are allocated once per Parser.
type stringCache = [256]string

// makeString returns the string form of b, reusing an equal string from c.
func makeString(c *stringCache, b []byte) string {
	const minCachedLen, maxCachedLen = 2, 256 // one-byte strings are interned by the runtime
	if len(b) < minCachedLen || len(b) > maxCachedLen {
		return string(b)
	}
	// Hash a fixed-width prefix and suffix, so hashing is constant time.
	var h uint32
	switch {
	case len(b) >= 8:
		lo := binary.LittleEndian.Uint64(b[:8])
		hi := binary.LittleEndian.Uint64(b[len(b)-8:])
		h = hash64(uint32(lo), uint32(lo>>32)) ^ hash64(uint32(hi), uint32(hi>>32))
	case len(b) >= 4:
		h = hash64(binary.LittleEndian.Uint32(b[:4]), binary.LittleEndian.Uint32(b[len(b)-4:]))
	default:
		h = hash64(uint32(binary.LittleEndian.Uint16(b[:2])), uint32(binary.LittleEndian.Uint16(b[len(b)-2:])))
	}
	i := h % uint32(len(*c))
	if s := (*c)[i]; s == string(b) {
		return s
	}
	s := string(b)
	(*c)[i] = s
	return s
}

// hash64 is XXH32 of an 8-byte input without the final avalanche step.
func hash64(lo, hi uint32) uint32 {
	const prime3, prime4, prime5 = 0xc2b2ae3d, 0x27d4eb2f, 0x165667b1
	h := prime5 + uint32(8)
	h += lo * prime3
	h = bits.RotateLeft32(h, 17) * prime4
	h += hi * prime3
	return bits.RotateLeft32(h, 17) * prime4
}
