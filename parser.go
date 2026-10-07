// Package simdjson is a Go port of the simdjson DOM parser
// (https://github.com/simdjson/simdjson).
package simdjson

import (
	"bytes"
	"slices"

	"simdjson-go/internal/stage1"
)

const (
	defaultMaxDepth = 1024 // C++ DEFAULT_MAX_DEPTH
	// maxSize is the largest input whose tape (at most len(b)+3 words) keeps
	// every tape index within 32 bits. C++ allows 0xFFFFFFFF bytes and then
	// truncates indices for the densest documents.
	maxSize = 0xFFFFFFFF - 3
)

var bom = []byte{0xEF, 0xBB, 0xBF}

// tapeWords bounds the tape of an n-byte document with s structural
// characters: at most 2 words per structural (numbers) plus the 2 root words,
// and never more than n+3 (C++'s bound, which commas and colons keep tighter).
func tapeWords(n, s int) int { return min(n+3, 2*s+2) }

// Document is a parsed JSON document in C++ simdjson's tape format
// (doc/tape.md). It is owned by the Parser that produced it and, with every
// Element, Array, Object and string bytes obtained from it, is valid until
// the next call to that Parser's Parse, including a call that fails; using
// them afterwards may return wrong values or panic. A Document and its
// Elements may be read from multiple goroutines concurrently as long as no
// Parse runs on its Parser.
type Document struct {
	tape    []uint64
	strings []byte
	offs    []uint32 // binding mode only: input offset per tape index (see builder.mark)
	dups    []uint32 // binding mode only: sorted tape indices of each object's first repeated name
}

// Parser parses JSON documents. The zero value is ready to use. A Parser
// reuses its buffers across calls and must not be used by more than one
// goroutine at a time.
type Parser struct {
	// MaxDepth limits nesting: at most MaxDepth-1 non-empty arrays/objects
	// may be nested (empty ones do not count, as in C++). 0 or negative means
	// 1024.
	MaxDepth int
	// BigIntAsString stores integers that fit neither int64 nor uint64 as
	// TypeBigInt (their raw digits) instead of failing with ErrBigInt.
	BigIntAsString bool

	// binding selects the encoding/json/v2 behaviour Unmarshal needs: input
	// offsets (Document.offs) and repeated object names (Document.dups) are
	// recorded, floats that overflow parse as ±Inf, and empty arrays and
	// objects count toward MaxDepth.
	binding bool

	indices []uint32
	stack   []scope
	doc     Document
	keys    []uint32 // binding mode scratch (builder.checkNames)
	seen    []uint32 // binding mode scratch (builder.checkNames)
}

// Parse parses b. The returned Document is valid until the next call to
// p.Parse. b is neither retained nor modified, and needs no padding.
// A leading UTF-8 byte-order mark is skipped, as in C++. It returns
// ErrCapacity if len(b) > 0xFFFFFFFC, or another Err* value if b is not valid JSON.
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
		tape:           slices.Grow(p.doc.tape[:0], tapeWords(len(b), len(p.indices))),
		strs:           slices.Grow(p.doc.strings[:0], 5*len(b)/3+64),
		stack:          p.stack[:0],
		maxDepth:       p.MaxDepth,
		bigIntAsString: p.BigIntAsString,
		binding:        p.binding,
	}
	if p.binding {
		bd.offs = slices.Grow(p.doc.offs[:0], cap(bd.tape))[:cap(bd.tape)]
		bd.dups = p.doc.dups[:0]
		bd.keys, bd.seen = p.keys[:0], p.seen
	}
	if bd.maxDepth <= 0 {
		bd.maxDepth = defaultMaxDepth
	}
	err = bd.walk()
	p.doc.tape, p.doc.strings, p.stack = bd.tape, bd.strs, bd.stack
	if p.binding {
		p.doc.offs = bd.offs[:len(bd.tape)]
		slices.Sort(bd.dups) // objects close inner first
		p.doc.dups = bd.dups
		p.keys, p.seen = bd.keys, bd.seen
	}
	if err != nil {
		return nil, err
	}
	return &p.doc, nil
}
