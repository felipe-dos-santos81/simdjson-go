package ondemand

import (
	"bytes"

	"simdjson-go/internal/jsonerr"
	"simdjson-go/internal/stage1"
	"simdjson-go/internal/str"
	"simdjson-go/internal/stream"
)

// Parser reads documents On-Demand. The zero value is ready to use; reuse
// a Parser across documents so its buffers are reused. A Parser is not safe
// for concurrent use.
type Parser struct {
	// BatchSize is how many bytes IterateMany runs stage 1 over at a time:
	// 0 means 1,000,000; above 1 GiB it means 1 GiB; it is rounded up to a
	// multiple of 64. It changes speed and memory, never results.
	BatchSize int

	doc     Document
	idx     []uint32      // Iterate's indices, apart from the stream's
	stream  stream.Reader // IterateMany's indices and state
	streams uint32        // counts IterateMany calls, to detect a second stream on p
}

var bom = []byte{0xEF, 0xBB, 0xBF}

// maxSize is the largest input: structural indices are 32 bits wide.
const maxSize = 0xFFFFFFFF - 3

// Iterate runs stage 1 over b (validating UTF-8 and finding the structural
// characters) and returns the document, ready to be read. Values are
// parsed and validated only as they are read. b is not copied or modified
// and needs no padding; it must not change until the next Iterate. A
// leading UTF-8 byte-order mark is skipped.
//
// The Document, every handle obtained from it and every slice returned by
// Raw and RawKey are valid until the next Iterate on p, or the next step of
// an IterateMany loop on p; slices from StringBytes until then or the next
// Rewind. The returned *Document is the same for every call on p.
func (p *Parser) Iterate(b []byte) (*Document, error) {
	b = bytes.TrimPrefix(b, bom)
	d := &p.doc
	if uint64(len(b)) > maxSize {
		d.kill()
		return nil, jsonerr.ErrCapacity
	}
	idx, err := stage1.Index(b, p.idx[:0])
	if err != nil {
		p.idx = idx
		d.kill()
		return nil, err
	}
	n := len(idx)
	// As in C++: two entries pointing at the end of the input (peeking
	// there sees 0, like C++'s padding), then a 0.
	idx = append(idx, uint32(len(b)), uint32(len(b)), 0)
	p.idx = idx
	*d = Document{
		buf:    b,
		idx:    idx,
		n:      n,
		end:    n,
		depth:  1,
		gen:    d.gen + 1,
		strs:   d.strs[:0],
		starts: d.starts[:0],
	}
	return d, nil
}

// kill turns d into a dead document (n == 0) after a failed Iterate,
// keeping only the reusable buffers; gen advances so handles into the
// previous document go stale.
func (d *Document) kill() {
	*d = Document{gen: d.gen + 1, strs: d.strs[:0], starts: d.starts[:0]}
}

// check rejects a dead document: one that never was iterated, or whose last
// Iterate failed. A live document has n > 0 (stage 1 rejects empty input).
func (d *Document) check() error {
	if d.n == 0 {
		return jsonerr.ErrOutOfOrderIteration
	}
	return nil
}

// Document is a JSON document being read On-Demand: a cursor over the
// structural characters stage 1 found. It is also the document's root value.
type Document struct {
	buf    []byte
	idx    []uint32 // structural indices, then len(buf), len(buf), 0
	n      int      // number of structural indices
	pos    int      // the cursor: an index into idx
	depth  int      // depth of the cursor (the root value is at depth 1)
	err    error    // a fatal error; reads fail with it from then on
	gen    uint32   // incremented by every Iterate, to detect stale handles
	strs   []byte   // unescaped strings, until the next Iterate or Rewind
	starts []int    // starts[depth]: start of the container open at depth (misuse checks)
	end    int      // where the root value ends (AtEnd); n for Iterate
	stream bool     // a stream document (C++ _streaming): no root checks of what follows
	comma  bool     // from a CommaDelimited stream (Source trims commas)
	off    int      // IterateMany: where the document starts in the input
	pad    byte     // the byte at len(buf): C++'s padding, 0, or CommaDelimitedArray's ']'
	// IterateMany, before its input is all indexed: idx is rd.Idx[rpos:],
	// final below n, and reach extends it (stream.go).
	rd     *stream.Reader
	rpos   int
	repoch uint32
}

// peekAt returns the structural character at token position i (pad at the
// end of the input, 0 past it, as C++'s padding reads).
func (d *Document) peekAt(i int) byte {
	idx, buf := d.idx, d.buf
	if uint(i) < uint(len(idx)) {
		if off := uint(idx[i]); off < uint(len(buf)) {
			return buf[off]
		} else if off == uint(len(buf)) {
			return d.pad
		}
	}
	return 0
}

func (d *Document) peek() byte { return d.peekAt(d.pos) }

// advance returns the structural character at the cursor and moves past it.
func (d *Document) advance() byte {
	c := d.peekAt(d.pos)
	if d.pos < len(d.idx) {
		d.pos++
	}
	return c
}

// exhausted reports whether the cursor is past the last structural (C++
// json_iterator::at_end).
func (d *Document) exhausted() bool { return d.pos == d.n }

// tokenLen is C++ peek_length: the distance to the next structural index.
// At the end of a stream, C++'s sentinels can make it negative (the dropped
// tail starts before len(buf)), which C++ reads as huge: Go reads 0.
func (d *Document) tokenLen(i int) int { return max(int(d.idx[i+1])-int(d.idx[i]), 0) }

// rootTokenLen is C++ peek_root_length.
func (d *Document) rootTokenLen(i int) int {
	a, b := int(d.idx[i+1])-int(d.idx[i]), int(d.idx[i+2])-int(d.idx[i])
	if b > a {
		return a
	}
	return b
}

// fail records a fatal error (C++ report_error).
func (d *Document) fail(err error) error {
	d.err = err
	return err
}

// abandon stops all further iteration after a fatal error.
func (d *Document) abandon() {
	d.depth = 0
}

func (d *Document) setStart(depth, pos int) {
	for len(d.starts) <= depth {
		d.starts = append(d.starts, -1)
	}
	d.starts[depth] = pos
}

func (d *Document) start(depth int) int {
	if depth < len(d.starts) {
		return d.starts[depth]
	}
	return -1
}

// reach is called by every step that moves the cursor, which then reads
// and moves at most a few indices on: a stream document's view is first
// extended to the whole input if those are not all final (more, in
// stream.go).
func (d *Document) reach() {
	if d.rd != nil && d.pos+reachAhead >= d.n {
		d.more()
	}
}

// reachAhead is how far reach looks; IterateMany indexes that far past a
// root before yielding it, so that reading the root never extends the view.
const reachAhead = 4

// skipChild is C++ json_iterator::skip_child: it skips the rest of the
// value the cursor is in, until the cursor is back at parentDepth.
func (d *Document) skipChild(parentDepth int) error {
	if d.depth <= parentDepth {
		return nil
	}
	d.reach()
	switch d.advance() {
	case '[', '{', ':':
	case ',':
	case ']', '}':
		d.depth--
		if d.depth <= parentDepth {
			return nil
		}
	case '"':
		if !d.exhausted() && d.peek() == ':' {
			d.advance() // a key: eat the ':'
			break
		}
		fallthrough
	default:
		d.depth--
		if d.depth <= parentDepth {
			return nil
		}
	}
	buf, depth, pos := d.buf, d.depth, d.pos
	for {
		idx := d.idx[:d.n] // every index below n is inside the input
		for ; pos < len(idx); pos++ {
			switch buf[idx[pos]] {
			case '[', '{':
				depth++
			case ']', '}':
				depth--
				if depth <= parentDepth {
					d.pos, d.depth = pos+1, depth
					d.reach() // it may have walked far: what follows reads on
					return nil
				}
			}
		}
		if !d.more() {
			break
		}
	}
	d.pos, d.depth = d.n, depth
	return d.fail(jsonerr.ErrIncompleteArrayOrObject)
}

// unescape unescapes the string whose opening quote is at buf[off] (C++
// raw_json_string::unescape). A string without escapes is returned as a
// slice of the input; one with escapes is unescaped into d.strs.
func (d *Document) unescape(off int) ([]byte, error) {
	s := d.buf[off+1:]
	if n := str.IndexQuoteOrBackslash(s); n >= 0 && s[n] == '"' {
		return s[:n:n], nil
	}
	start := len(d.strs)
	out, ok := str.AppendUnescaped(d.strs, d.buf, off+1)
	if !ok {
		d.strs = out[:start]
		return nil, jsonerr.ErrString
	}
	d.strs = out
	return out[start:len(out):len(out)], nil
}

// rawText returns the input from offset start to the cursor's structural
// (C++ raw_json's end point), or ErrOutOfOrderIteration if the cursor is not
// after start (a handle used out of order).
func (d *Document) rawText(start int) ([]byte, error) {
	if d.pos >= len(d.idx) {
		return nil, jsonerr.ErrOutOfOrderIteration
	}
	end := int(d.idx[d.pos])
	if d.pos > d.n || end < start {
		return nil, jsonerr.ErrOutOfOrderIteration
	}
	return d.buf[start:end], nil
}

// AtEnd reports whether the whole document has been read (C++ at_end). As
// in C++, reading a root array or object does not check what follows it:
// call AtEnd after reading to reject trailing content such as "[1] [2]".
// For a document from IterateMany it reports whether the root value has
// been read to its end.
func (d *Document) AtEnd() bool { return d.n > 0 && d.pos == d.end }

// copyString returns b as a string and gives back the buffer space it used
// (from mark): a copy does not keep the buffer, as in C++.
func (d *Document) copyString(mark int, b []byte) string {
	s := string(b)
	d.strs = d.strs[:mark]
	return s
}

// Rewind moves the cursor back to the start of the document, so it can be
// read again (C++ document::rewind). Slices from StringBytes read before
// the Rewind may be overwritten. As C++ json_iterator::rewind, it keeps a
// fatal error, which iteration, Count and Reset report again.
func (d *Document) Rewind() {
	if d.n == 0 {
		return
	}
	d.pos = 0
	d.depth = 1
	d.strs = d.strs[:0]
}
