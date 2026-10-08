package ondemand

import (
	"cmp"
	"iter"

	"simdjson-go/internal/jsonerr"
	"simdjson-go/internal/stream"
)

// Format says how the documents of a stream are separated (C++ stream_format).
type Format = stream.Format

const (
	// Whitespace: documents separated by optional whitespace, as NDJSON and
	// concatenated JSON (C++ whitespace_delimited).
	Whitespace = stream.Whitespace
	// NewlineDelimited: as Whitespace, but the rest of a document not read
	// to its end is skipped to the next newline without being validated.
	NewlineDelimited = stream.NewlineDelimited
	// JSONSequence: RFC 7464 records, each starting with RS (0x1E); the rest
	// of a document not read to its end is skipped to the next RS.
	JSONSequence = stream.JSONSequence
	// CommaDelimited: documents separated by commas, as {...},{...}.
	CommaDelimited = stream.CommaDelimited
	// CommaDelimitedArray: the elements of one top-level array, [{...},{...}].
	CommaDelimitedArray = stream.CommaDelimitedArray
)

// StreamError ends a stream: Err, one of the simdjson Err* values, happened
// in the document, or the dropped tail, that starts at byte Offset of the input.
type StreamError = jsonerr.StreamError

// IterateMany reads the documents in b one at a time, as C++
// parser::iterate_many, and yields each On-Demand; f says how they are
// separated. The yielded *Document is the same for every document; it and
// everything read from it are valid until the loop's next step or the next
// Iterate or IterateMany on p, and b must not change until the loop ends.
// Read errors belong to the read and do not end the stream: the next step
// skips the rest of the document, as C++ does. An error ending the stream
// is always the last item: a *StreamError with the failing document's
// offset (ErrTrailingContent when b ends with an incomplete document,
// which C++ drops silently). Empty input yields nothing. Stage 1 runs
// BatchSize bytes at a time; the batch size never changes what is yielded.
// Starting another stream on p ends this one with ErrOutOfOrderIteration.
func (p *Parser) IterateMany(b []byte, f Format) iter.Seq2[*Document, error] {
	return func(yield func(*Document, error) bool) {
		fail := func(off int, err error) { yield(nil, &StreamError{Offset: off, Err: err}) }
		if uint64(len(b)) > maxSize {
			fail(0, jsonerr.ErrCapacity)
			return
		}
		// The byte after C++'s input: its padding, or, as C++ strips the
		// brackets by moving its pointers, the array's own ']'.
		var pad byte
		if f == CommaDelimitedArray {
			pad = ']'
		}
		buf, base, f, ok := stream.Input(b, f)
		if !ok {
			fail(0, jsonerr.ErrTape)
			return
		}
		p.streams++
		gen, r, d := p.streams, &p.stream, &p.doc
		r.Reset(buf, f, p.BatchSize)
		defer func() {
			if p.streams == gen { // else the stream that reset r owns it
				r.Release()
			}
		}()
		var delim byte // C++ document_delimiter
		switch f {
		case NewlineDelimited:
			delim = '\n'
		case JSONSequence:
			delim = stream.RS
		}
		for pos := 0; ; {
			pos = r.Compact(pos)
			for !r.Done && r.Decided() <= pos {
				r.Load()
			}
			if pos >= r.Decided() {
				if r.Drop >= 0 {
					fail(base+r.Drop, cmp.Or(r.Stage1Err(len(buf)), jsonerr.ErrTrailingContent))
				}
				return
			}
			// The root value ends before end. Reads look up to reachAhead
			// indices past the cursor, so those must be final (or the view
			// grows, which a read of the root never needs), and the bytes
			// checked.
			end, _ := r.Skip(pos, 1)
			for !r.Done && (r.Decided() <= end+reachAhead || r.Checked() < r.End(r.Decided())) {
				r.Load()
			}
			start := int(r.Idx[pos])
			if err := r.Stage1Err(r.End(end)); err != nil {
				fail(base+start, err)
				return
			}
			d.view(r, pos, end, base+start, f == CommaDelimited, pad)
			mine, nextOff := d.gen, base+r.End(end)
			if !yield(d, nil) {
				return
			}
			if p.streams != gen { // another stream on p reset the reader
				fail(nextOff, jsonerr.ErrOutOfOrderIteration)
				return
			}
			// C++ next_document, from the reader's cursor, or from the root
			// if an Iterate on p replaced the document or a read abandoned
			// it (C++ is undefined there; a root read to its end also has
			// depth 0, but no error).
			cur, depth := pos, 1
			if d.gen == mine && (d.depth > 0 || d.err == nil) {
				cur, depth = pos+d.pos, d.depth
			}
			if d.gen == mine {
				// The document's step is over; Compact may move the indices
				// it views, so later reads report ErrOutOfOrderIteration.
				d.kill()
			}
			if delim != 0 && depth > 0 {
				if i, ok := r.SkipTo(cur, delim); ok {
					// The bytes jumped over are a region of their own for stage 1.
					for !r.Done && r.Checked() < r.End(i) {
						r.Load()
					}
					if err := r.Stage1Err(r.End(i)); err != nil {
						fail(base+r.End(end), err)
						return
					}
					pos = i
					continue
				}
			}
			i, ok := r.Skip(cur, depth)
			if !ok {
				fail(base+start, jsonerr.ErrIncompleteArrayOrObject)
				return
			}
			pos = i
		}
	}
}

// view points d at the stream document starting at r.Idx[pos], whose root
// value ends before position end: C++'s json_iterator re-anchored at the
// document, over indices that run past it. Until the input is all indexed,
// those are final up to the decided point only; a read that would go past
// it (on a malformed root, or out of order) first indexes the rest, as C++
// reads on to the end of its one batch (reach, more).
func (d *Document) view(r *stream.Reader, pos, end, off int, comma bool, pad byte) {
	rd := r
	if r.Done {
		rd = nil
	}
	*d = Document{
		buf:    r.Buf,
		idx:    r.Idx[pos:],
		n:      r.Decided() - pos,
		rd:     rd,
		rpos:   pos,
		repoch: r.Epoch(),
		end:    end - pos,
		depth:  1,
		gen:    d.gen + 1,
		strs:   d.strs[:0],
		starts: d.starts[:0],
		stream: true,
		comma:  comma,
		off:    off,
		pad:    pad,
	}
}

// Offset returns where a document from IterateMany starts in its input (0
// for Iterate).
func (d *Document) Offset() int { return d.off }

// Source returns a document from IterateMany as it appears in its input,
// from its first character to the end of its root value whatever has been
// read (C++ document_stream::iterator::source); nil for Iterate.
func (d *Document) Source() []byte {
	if !d.stream {
		return nil
	}
	start, depth := int(d.idx[0]), 1
	switch d.peekAt(0) {
	case '{', '[':
	case '}', ']':
		depth--
	default:
		return stream.ScalarSource(d.buf, start, int(d.idx[1]), d.comma)
	}
	i := 1
	for ; i <= d.n; i++ {
		if i == d.n {
			d.more() // C++'s walk reaches the end of its one batch
		}
		switch d.peekAt(i) {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		}
		if depth == 0 {
			break
		}
	}
	// C++ can end one byte past its input: into its padding, where Go stops
	// at the input's end, or on the array's ']', which b still holds.
	limit := len(d.buf)
	if d.pad != 0 {
		limit++
	}
	end := min(int(d.idx[min(i, len(d.idx)-1)])+1, limit)
	return d.buf[start:end:end]
}

// more extends a stream document's view to the whole input, loading every
// window, and reports whether it grew. A view whose stream has ended or
// been reset by another cannot grow.
func (d *Document) more() bool {
	r := d.rd
	if r == nil || r.Epoch() != d.repoch {
		return false
	}
	for r.Load() {
	}
	d.rd, d.idx, d.n = nil, r.Idx[d.rpos:], r.N-d.rpos
	return true
}
