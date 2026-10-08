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
		buf, base, f, ok := stream.Input(b, f)
		if !ok {
			fail(0, jsonerr.ErrTape)
			return
		}
		p.streams++
		gen, r, d := p.streams, &p.stream, &p.doc
		r.Reset(buf, f, p.BatchSize)
		var delim byte // C++ document_delimiter
		switch f {
		case NewlineDelimited:
			delim = '\n'
		case JSONSequence:
			delim = 0x1E
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
			// The root value ends before end. Reads peek up to two indices
			// past its last, so those must be final, and its bytes checked.
			end, _ := r.Skip(pos, 1)
			for !r.Done && (r.Decided() <= end+1 || r.Checked() < r.End(r.Decided())) {
				r.Load()
			}
			start := int(r.Idx[pos])
			if err := r.Stage1Err(r.End(end)); err != nil {
				fail(base+start, err)
				return
			}
			d.view(r, pos, end, base+start, f == CommaDelimited)
			mine, nextOff := d.gen, base+r.End(end)
			if !yield(d, nil) {
				return
			}
			if p.streams != gen { // another stream on p reset the reader
				fail(nextOff, jsonerr.ErrOutOfOrderIteration)
				return
			}
			// C++ next_document, from the reader's cursor, or from the root
			// if an Iterate on p replaced the document.
			cur, depth := pos, 1
			if d.gen == mine {
				cur, depth = pos+d.pos, d.depth
			}
			if delim != 0 && depth > 0 {
				if i, ok := r.SkipTo(cur, delim); ok {
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
// document, over indices that run past it.
func (d *Document) view(r *stream.Reader, pos, end, off int, comma bool) {
	*d = Document{
		buf:    r.Buf,
		idx:    r.Idx[pos:],
		n:      r.Decided() - pos,
		end:    end - pos,
		depth:  1,
		gen:    d.gen + 1,
		strs:   d.strs[:0],
		starts: d.starts[:0],
		stream: true,
		comma:  comma,
		off:    off,
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
	// C++ can end one byte into its padding; Go stops at the input's end.
	end := min(int(d.idx[min(i, len(d.idx)-1)])+1, len(d.buf))
	return d.buf[start:end:end]
}
