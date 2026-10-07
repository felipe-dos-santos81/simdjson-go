// Package simdjson is a Go port of the simdjson DOM parser
// (https://github.com/simdjson/simdjson).
package simdjson

import (
	"bytes"
	"slices"

	"simdjson-go/internal/stage1"
)

const (
	defaultMaxDepth = 1024       // C++ DEFAULT_MAX_DEPTH
	maxSize         = 0xFFFFFFFF // C++ SIMDJSON_MAXSIZE_BYTES
	// maxMaxDepth keeps AppendJSON's and AtPointer's recursion well inside
	// Go's 1 GB stack limit.
	maxMaxDepth = 1 << 20
)

var bom = []byte{0xEF, 0xBB, 0xBF}

// Document is a parsed JSON document in C++ simdjson's tape format
// (doc/tape.md). It is owned by the Parser that produced it.
type Document struct {
	tape    []uint64
	strings []byte
}

// Parser parses JSON documents. The zero value is ready to use. A Parser
// reuses its buffers across calls and must not be used by more than one
// goroutine at a time.
type Parser struct {
	// MaxDepth limits nesting: at most MaxDepth-1 non-empty arrays/objects
	// may be nested (empty ones do not count, as in C++). 0 or negative means
	// 1024; values above 1<<20 are treated as 1<<20.
	MaxDepth int
	// BigIntAsString stores integers that fit neither int64 nor uint64 as
	// TypeBigInt (their raw digits) instead of failing with ErrBigInt.
	BigIntAsString bool

	indices []uint32
	stack   []scope
	doc     Document
}

// Parse parses b. The returned Document is valid until the next call to
// p.Parse. b is neither retained nor modified, and needs no padding.
// A leading UTF-8 byte-order mark is skipped, as in C++.
func (p *Parser) Parse(b []byte) (*Document, error) {
	if uint64(len(b)) > maxSize {
		return nil, ErrCapacity
	}
	b = bytes.TrimPrefix(b, bom)
	var err error
	if p.indices, err = stage1.Index(b, p.indices); err != nil {
		return nil, err
	}
	bd := builder{
		buf:            b,
		idx:            p.indices,
		tape:           slices.Grow(p.doc.tape[:0], len(b)+3),
		strs:           slices.Grow(p.doc.strings[:0], 5*len(b)/3+64),
		stack:          p.stack[:0],
		maxDepth:       p.MaxDepth,
		bigIntAsString: p.BigIntAsString,
	}
	if bd.maxDepth <= 0 {
		bd.maxDepth = defaultMaxDepth
	}
	bd.maxDepth = min(bd.maxDepth, maxMaxDepth)
	err = bd.walk()
	p.doc.tape, p.doc.strings, p.stack = bd.tape, bd.strs, bd.stack
	if err != nil {
		return nil, err
	}
	return &p.doc, nil
}
