package ondemand

import (
	"bytes"

	"simdjson-go/internal/jsonerr"
	"simdjson-go/internal/stage1"
	"simdjson-go/internal/str"
)

// Parser reads documents On-Demand. The zero value is ready to use; reuse
// a Parser across documents so its buffers are reused. A Parser is not safe
// for concurrent use.
type Parser struct {
	doc Document
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
// Raw and RawKey are valid until the next Iterate on p; slices from
// StringBytes until the next Iterate or Rewind. The returned *Document is
// the same for every call on p.
func (p *Parser) Iterate(b []byte) (*Document, error) {
	b = bytes.TrimPrefix(b, bom)
	d := &p.doc
	if uint64(len(b)) > maxSize {
		d.kill(d.idx)
		return nil, jsonerr.ErrCapacity
	}
	idx, err := stage1.Index(b, d.idx[:0])
	if err != nil {
		d.kill(idx)
		return nil, err
	}
	n := len(idx)
	// As in C++: two entries pointing at the end of the input (peeking
	// there sees 0, like C++'s padding), then a 0.
	idx = append(idx, uint32(len(b)), uint32(len(b)), 0)
	*d = Document{
		buf:    b,
		idx:    idx,
		n:      n,
		depth:  1,
		gen:    d.gen + 1,
		strs:   d.strs[:0],
		starts: d.starts[:0],
	}
	return d, nil
}

// kill turns d into a dead document (n == 0) after a failed Iterate, keeping
// only the reusable buffers; gen advances so handles into the previous
// document go stale.
func (d *Document) kill(idx []uint32) {
	*d = Document{idx: idx, gen: d.gen + 1, strs: d.strs[:0], starts: d.starts[:0]}
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
}

// peekAt returns the structural character at token position i (0 past the
// end, as C++'s padding reads).
func (d *Document) peekAt(i int) byte {
	idx, buf := d.idx, d.buf
	if uint(i) < uint(len(idx)) {
		if off := uint(idx[i]); off < uint(len(buf)) {
			return buf[off]
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
func (d *Document) tokenLen(i int) int { return int(d.idx[i+1]) - int(d.idx[i]) }

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

// skipChild is C++ json_iterator::skip_child: it skips the rest of the
// value the cursor is in, until the cursor is back at parentDepth.
func (d *Document) skipChild(parentDepth int) error {
	if d.depth <= parentDepth {
		return nil
	}
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
	// Every index below n is inside the input.
	idx, buf, depth := d.idx[:d.n], d.buf, d.depth
	for pos := d.pos; pos < len(idx); {
		c := buf[idx[pos]]
		pos++
		switch c {
		case '[', '{':
			depth++
		case ']', '}':
			depth--
			if depth <= parentDepth {
				d.pos, d.depth = pos, depth
				return nil
			}
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
func (d *Document) AtEnd() bool { return d.n > 0 && d.pos == d.n }

// copyString returns b as a string and gives back the buffer space it used
// (from mark): a copy does not keep the buffer, as in C++.
func (d *Document) copyString(mark int, b []byte, err error) (string, error) {
	s := string(b)
	d.strs = d.strs[:mark]
	return s, err
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
