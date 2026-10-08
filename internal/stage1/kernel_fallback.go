//go:build !arm64 || !goexperiment.simd || purego

package stage1

import "unicode/utf8"

func classify(b *[64]byte) masks { return classifyGeneric(b) }

// utf8Checker validates each window with the standard library; a character
// cut at a window's end is validated with the next window.
type utf8Checker struct{}

func (*utf8Checker) next(*[64]byte) {}

func (*utf8Checker) validWindow(buf []byte, lo, end int) bool {
	if end < len(buf) {
		end = runeStart(buf, end)
	}
	return utf8.Valid(buf[runeStart(buf, lo):end])
}
