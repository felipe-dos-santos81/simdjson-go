package simdjson

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
	// NewlineDelimited is Whitespace for ParseMany; ondemand.IterateMany
	// skips the rest of a document not read to its end to the next newline.
	NewlineDelimited = stream.NewlineDelimited
	// JSONSequence: RFC 7464 records, each starting with RS (0x1E).
	JSONSequence = stream.JSONSequence
	// CommaDelimited: documents separated by commas, as {...},{...}.
	CommaDelimited = stream.CommaDelimited
	// CommaDelimitedArray: the elements of one top-level array, [{...},{...}].
	CommaDelimitedArray = stream.CommaDelimitedArray
)

// StreamError ends a stream: Err, one of the Err* values, happened in the
// document, or the dropped tail, that starts at byte Offset of the input.
type StreamError = jsonerr.StreamError

// ParseMany parses the documents in b one at a time, as C++
// parser::parse_many, and yields them in order; f says how they are
// separated. Each yielded *Document, and everything read from it, is valid
// until the loop's next step or the next Parse or ParseMany on p; b must
// not change until the loop ends. An error is always the last item: a
// *StreamError with the failing document's offset, wrapping an Err* value
// (ErrTrailingContent when b ends with an incomplete document, which C++
// drops silently). Empty input yields nothing. A leading UTF-8 BOM is
// skipped. Stage 1 runs BatchSize bytes at a time; the batch size never
// changes what is yielded. Starting another stream on p ends this one with
// ErrOutOfOrderIteration.
func (p *Parser) ParseMany(b []byte, f Format) iter.Seq2[*Document, error] {
	return func(yield func(*Document, error) bool) {
		fail := func(off int, err error) { yield(nil, &StreamError{Offset: off, Err: err}) }
		if uint64(len(b)) > maxSize {
			fail(0, ErrCapacity)
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
			fail(0, ErrTape)
			return
		}
		p.streams++
		gen, r := p.streams, &p.stream
		r.Reset(buf, f, p.BatchSize)
		defer func() {
			if p.streams == gen { // else the stream that reset r owns it
				r.Release()
			}
		}()
		for pos := 0; ; {
			pos = r.Compact(pos)
			// Wait until the document at pos, up to the next boundary
			// candidate, is final and checked by stage 1.
			for !r.Done && (r.Decided() <= pos || r.Checked() < r.End(r.Decided())) {
				r.Load()
			}
			lim := r.Decided()
			if pos >= lim {
				if r.Drop >= 0 {
					fail(base+r.Drop, cmp.Or(r.Stage1Err(len(buf)), ErrTrailingContent))
				}
				return
			}
			start := int(r.Idx[pos])
			// A document still open at the end of the input reads pad there
			// (C++'s sentinel structural_indexes[n] = len); it then ends the
			// stream, where C++ goes on past its sentinel.
			end := byte(0)
			if r.Done {
				end = pad
			}
			n, err := p.build(buf, r.Idx[pos:lim], r.End(lim)-start, true, end)
			if err != nil && end != pad {
				// A failed walk may have read 0 at lim. Had lim started the
				// dropped tail, it would have read pad (only a
				// CommaDelimitedArray has one): find out.
				for !r.Done && r.Decided() == lim {
					r.Load()
				}
				if r.Done && r.N == lim {
					n, err = p.build(buf, r.Idx[pos:lim], r.End(lim)-start, true, pad)
				}
			}
			if err != nil {
				// A stage 1 error among the bytes the document holds (by a
				// bracket count, as stage 2 stopped early) comes first.
				end, _ := r.Skip(pos, 1)
				for !r.Done && r.Checked() < r.End(end) {
					r.Load()
				}
				fail(base+start, cmp.Or(r.Stage1Err(r.End(end)), err))
				return
			}
			next := pos + n
			if err := r.Stage1Err(r.End(next)); err != nil {
				fail(base+start, err)
				return
			}
			p.doc.off, p.doc.src = base+start, domSource(buf, r.Idx, pos, next, f == CommaDelimited)
			nextOff := base + r.End(next)
			if !yield(&p.doc, nil) {
				return
			}
			if p.streams != gen { // another stream on p reset the reader
				fail(nextOff, ErrOutOfOrderIteration)
				return
			}
			pos = next
		}
	}
}

// domSource is C++ dom document_stream::iterator::source for the document
// at idx[pos] whose stage 2 stopped before idx[next].
func domSource(buf []byte, idx []uint32, pos, next int, comma bool) []byte {
	start := int(idx[pos])
	if c := buf[start]; c == '[' || c == '{' {
		end := int(idx[next-1]) + 1
		return buf[start:end:end]
	}
	return stream.ScalarSource(buf, start, int(idx[next]), comma)
}
