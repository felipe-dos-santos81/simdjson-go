// Package simdjson is a Go port of the simdjson DOM parser
// (https://github.com/simdjson/simdjson).
package simdjson

import (
	"bytes"
	"slices"

	"simdjson-go/internal/stage1"
	"simdjson-go/internal/stream"
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
	off     int      // ParseMany: where the document starts in the input
	src     []byte   // ParseMany: the document's source
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
	// BatchSize is how many bytes ParseMany runs stage 1 over at a time:
	// 0 means 1,000,000; above 1 GiB it means 1 GiB; it is rounded up to a
	// multiple of 64. It changes speed and memory, never results.
	BatchSize int

	// binding selects the encoding/json/v2 behaviour Unmarshal needs: input
	// offsets (Document.offs) and repeated object names (Document.dups) are
	// recorded, floats that overflow parse as ±Inf, and empty arrays and
	// objects count toward MaxDepth.
	binding bool

	indices []uint32
	stack   []scope
	doc     Document
	keys    []uint32      // binding mode scratch (builder.checkNames)
	seen    []uint32      // binding mode scratch (builder.checkNames)
	stream  stream.Reader // ParseMany's indices and state, apart from Parse's
	streams uint32        // counts ParseMany calls, to detect a second stream on p
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
	if _, err = p.build(b, p.indices, len(b), false, 0); err != nil {
		return nil, err
	}
	p.doc.off, p.doc.src = 0, nil
	return &p.doc, nil
}

// build runs stage 2 over idx into p.doc and returns how many indices it
// read. size bounds the bytes the tape and strings can need (a hint). In
// streaming mode it parses one document of a stream and stops after it.
func (p *Parser) build(buf []byte, idx []uint32, size int, streaming bool, pad byte) (int, error) {
	bd := builder{
		buf:            buf,
		idx:            idx,
		tape:           slices.Grow(p.doc.tape[:0], tapeWords(size, len(idx))),
		strs:           slices.Grow(p.doc.strings[:0], 5*size/3+64),
		stack:          p.stack[:0],
		maxDepth:       p.MaxDepth,
		bigIntAsString: p.BigIntAsString,
		binding:        p.binding,
		streaming:      streaming,
		pad:            pad,
	}
	if p.binding {
		bd.offs = slices.Grow(p.doc.offs[:0], cap(bd.tape))[:cap(bd.tape)]
		bd.dups = p.doc.dups[:0]
		bd.keys, bd.seen = p.keys[:0], p.seen
	}
	if bd.maxDepth <= 0 {
		bd.maxDepth = defaultMaxDepth
	}
	err := bd.walk()
	p.doc.tape, p.doc.strings, p.stack = bd.tape, bd.strs, bd.stack
	if p.binding {
		p.doc.offs = bd.offs[:len(bd.tape)]
		slices.Sort(bd.dups) // objects close inner first
		p.doc.dups = bd.dups
		p.keys, p.seen = bd.keys, bd.seen
	}
	return bd.pos, err
}

// Offset returns where a document from ParseMany starts in its input (0
// for Parse).
func (d *Document) Offset() int { return d.off }

// Source returns a document from ParseMany as it appears in its input (C++
// document_stream::iterator::source); nil for Parse.
func (d *Document) Source() []byte { return d.src }
