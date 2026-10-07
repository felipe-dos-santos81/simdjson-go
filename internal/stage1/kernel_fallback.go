//go:build !arm64 || !goexperiment.simd || purego

package stage1

import "unicode/utf8"

func classify(b *[64]byte) masks { return classifyGeneric(b) }

// utf8Checker validates the whole input at the end with the standard library.
type utf8Checker struct{}

func (*utf8Checker) next(*[64]byte) {}

func (*utf8Checker) valid(buf []byte) bool { return utf8.Valid(buf) }
