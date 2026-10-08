// Package stream splits a buffer holding many JSON documents into
// documents, as C++ simdjson's document_stream does when the whole buffer
// is one batch, while running stage 1 one window at a time.
//
// Positions are indices into Reader.Idx; offsets are into Reader.Buf.
package stream

import (
	"bytes"
	"math"

	"simdjson-go/internal/jsonerr"
	"simdjson-go/internal/stage1"
)

// Format says how the documents of a stream are separated (C++ stream_format).
type Format int

const (
	Whitespace          Format = iota // whitespace_delimited
	NewlineDelimited                  // newline_delimited
	JSONSequence                      // json_sequence (RFC 7464)
	CommaDelimited                    // comma_delimited
	CommaDelimitedArray               // comma_delimited_array
)

// DefaultBatchSize is C++'s DEFAULT_BATCH_SIZE.
const DefaultBatchSize = 1000000

const rs = 0x1E // RFC 7464 record separator

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// isCSpace is C's isspace.
func isCSpace(c byte) bool { return isSpace(c) || c == '\v' || c == '\f' }

func isOperator(c byte) bool {
	return c == '{' || c == '}' || c == '[' || c == ']' || c == ':' || c == ','
}

// Input returns the bytes C++'s parse_many and iterate_many stream (dom
// and ondemand parser-inl.h): b without a leading UTF-8 BOM and, for
// CommaDelimitedArray, without the brackets and the whitespace outside
// them, read as CommaDelimited. base is where they start in b. ok is false
// when CommaDelimitedArray input is not an array (C++ TAPE_ERROR).
func Input(b []byte, f Format) (buf []byte, base int, g Format, ok bool) {
	if bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}) {
		b, base = b[3:], 3
	}
	if f != CommaDelimitedArray {
		return b, base, f, true
	}
	i, j := 0, len(b)
	for i < j && isSpace(b[i]) {
		i++
	}
	if i == j || b[i] != '[' {
		return nil, 0, f, false
	}
	i++
	for j > i && isSpace(b[j-1]) {
		j--
	}
	if j == i || b[j-1] != ']' {
		return nil, 0, f, false
	}
	return b[i : j-1], base + i, CommaDelimited, true
}

// window is one run of stage 1: the structural indices of Buf up to end,
// and what stage 1 had found by then.
type window struct {
	idx              []uint32
	end              int
	badCtrl, badUTF8 int
	unclosed         bool
}

// Reader indexes a stream window by window. Idx holds the structural
// indices after the format's filter. They are final below Decided; once
// Done, Idx[:N] are the documents' and Idx[N:N+3] are C++'s sentinels
// (len(Buf), where the dropped tail starts or len(Buf), 0).
type Reader struct {
	Buf  []byte
	Idx  []uint32
	Done bool // all of Buf is indexed and trimmed
	N    int  // once Done: how many indices the documents have
	Drop int  // once Done: where the dropped tail starts in Buf, or -1

	format  Format
	batch   int
	scanned int // Buf[:scanned] is indexed
	clean   int // Buf[:clean] is checked by stage 1, apart from badCtrl and badUTF8
	badCtrl int // first control character inside a string, or math.MaxInt
	badUTF8 int // first invalid UTF-8 byte, or math.MaxInt

	cand    int    // position of the last boundary candidate, or -1
	prev    int    // character of the last index added, or -1
	held    uint32 // the last raw index: added once the next one arrives
	hasHeld bool
	depth   int  // CommaDelimited: bracket depth
	rs      bool // JSONSequence: an RS was seen
	insert  int  // JSONSequence: a value start to add before the next index, or -1
	skipTo  int  // JSONSequence: raw indices below it are inside an RS run

	st   stage1.Stream
	wins [2]window
}

// Reset starts reading buf in format f (not CommaDelimitedArray: see
// Input), batch bytes of stage 1 at a time (0 means DefaultBatchSize;
// rounded up to a multiple of 64).
func (r *Reader) Reset(buf []byte, f Format, batch int) {
	if batch <= 0 {
		batch = DefaultBatchSize
	}
	batch = (min(batch, 1<<30) + 63) &^ 63
	*r = Reader{
		Buf: buf, Idx: r.Idx[:0], Drop: -1, format: f, batch: batch,
		badCtrl: math.MaxInt, badUTF8: math.MaxInt, cand: -1, prev: -1, insert: -1,
		wins: r.wins,
	}
	r.st.Reset(buf)
	if len(buf) == 0 {
		r.Done = true // C++: EMPTY, no documents
		r.Idx = append(r.Idx, 0, 0, 0)
	}
}

// Load indexes the next window. It reports false once there is nothing left.
func (r *Reader) Load() bool {
	if r.Done {
		return false
	}
	w := &r.wins[0]
	r.scan(w, min(r.scanned+r.batch, len(r.Buf)))
	r.take(w)
	return true
}

// scan runs stage 1 up to end into w.
func (r *Reader) scan(w *window, end int) {
	w.idx = r.st.Next(end, w.idx[:0])
	w.end, w.badCtrl, w.badUTF8, w.unclosed = end, r.st.BadCtrl, r.st.BadUTF8, r.st.Unclosed()
}

// take adds a scanned window, holding back its last index (C++ drops a
// trailing unclosed string's quote, which only the end of the input shows).
func (r *Reader) take(w *window) {
	r.scanned, r.badCtrl, r.badUTF8 = w.end, w.badCtrl, w.badUTF8
	r.clean = max(w.end-3, 0) // a character cut at the window's end is checked with the next
	for _, i := range w.idx {
		if r.hasHeld {
			r.push(r.held)
		}
		r.held, r.hasHeld = i, true
	}
	if w.end == len(r.Buf) {
		r.clean = w.end
		r.finish(w.unclosed)
	}
}

// push runs one raw structural index through the format's filter (C++
// filter_comma_delimited and find_next_document_index_json_sequence, in
// find_next_document_index.h).
func (r *Reader) push(i uint32) {
	c := r.Buf[i]
	switch r.format {
	case CommaDelimited:
		switch c {
		case '{', '[':
			r.depth++
		case '}', ']':
			r.depth--
		case ',':
			if r.depth == 0 {
				return // a document separator
			}
		}
	case JSONSequence:
		if int(i) < r.skipTo {
			return // inside an RS run
		}
		if r.insert >= 0 {
			if int(i) != r.insert { // stage 1 missed the value start after the RS
				r.add(uint32(r.insert))
			}
			r.insert = -1
		}
		if c == rs {
			r.rs = true
			v := int(i) + 1
			for v < len(r.Buf) && (isSpace(r.Buf[v]) || r.Buf[v] == rs) {
				v++
			}
			r.skipTo = v
			if v < len(r.Buf) && !isOperator(r.Buf[v]) {
				r.insert = v
			}
			return
		}
	}
	r.add(i)
}

// add appends a filtered index and tracks the last boundary candidate (C++
// find_next_document_index): a structural that is not a closer, ':' or ','
// and does not follow '{', '[', ':' or ','.
func (r *Reader) add(i uint32) {
	c := r.Buf[i]
	if r.prev >= 0 && c != '}' && c != ']' && c != ':' && c != ',' {
		switch r.prev {
		case '{', '[', ':', ',':
		default:
			r.cand = len(r.Idx)
		}
	}
	r.prev = int(c)
	r.Idx = append(r.Idx, i)
}

// finish ends the input as C++'s final window does (json_structural_indexer.h
// finish, streaming_final and its json_sequence and comma variants): drop a
// trailing unclosed string, keep what find_next_document_index keeps, and
// write the sentinels.
func (r *Reader) finish(unclosed bool) {
	r.Done = true
	scanLen := len(r.Buf)
	if r.hasHeld {
		r.hasHeld = false
		if unclosed {
			scanLen = int(r.held)
		} else {
			r.push(r.held)
		}
	}
	if r.insert >= 0 && r.insert < scanLen {
		r.add(uint32(r.insert))
	}
	r.insert = -1
	n := len(r.Idx)
	r.N = n
	if r.format != JSONSequence || !r.rs { // with an RS, every record is kept
		r.N = r.keep()
	}
	switch {
	case r.N < n:
		r.Drop = int(r.Idx[r.N])
	case scanLen < len(r.Buf):
		r.Drop = scanLen
	}
	end := uint32(len(r.Buf))
	next := end
	if r.Drop >= 0 {
		next = uint32(r.Drop)
	}
	r.Idx = append(r.Idx[:r.N], end, next, 0)
}

// keep is find_next_document_index on the final window: all indices if the
// brackets from the last boundary candidate on balance, otherwise those
// before it (none when there is no candidate).
func (r *Reader) keep() int {
	from := max(r.cand, 0)
	arr, obj := 0, 0
	for _, i := range r.Idx[from:] {
		switch r.Buf[i] {
		case '[':
			arr++
		case ']':
			arr--
		case '{':
			obj++
		case '}':
			obj--
		}
	}
	if arr == 0 && obj == 0 {
		return len(r.Idx)
	}
	return from
}

// Decided returns how many leading indices are final. C++ trims only from
// the last boundary candidate of the input on, so every index before the
// latest candidate seen is final.
func (r *Reader) Decided() int {
	if r.Done {
		return r.N
	}
	return max(r.cand, 0)
}

// Checked returns how many leading bytes of Buf stage 1 has checked.
func (r *Reader) Checked() int { return r.clean }

// End returns the offset where the document after position i starts:
// Idx[i], or, once Done and i >= N, where the dropped tail starts (or
// len(Buf)). It is where a document ending before i stops holding bytes.
func (r *Reader) End(i int) int {
	if r.Done && i >= r.N {
		if r.Drop >= 0 {
			return r.Drop
		}
		return len(r.Buf)
	}
	return int(r.Idx[i])
}

// Stage1Err returns the stage 1 error of the bytes below end, or nil,
// checking unescaped characters before UTF-8 as C++ does. end must not
// exceed Checked.
func (r *Reader) Stage1Err(end int) error {
	switch {
	case r.badCtrl < end:
		return jsonerr.ErrUnescapedChars
	case r.badUTF8 < end:
		return jsonerr.ErrUTF8
	}
	return nil
}

// Compact drops the indices before pos (never past Decided) once they are
// at least half of Idx, so the copying stays linear, and returns pos's new
// position.
func (r *Reader) Compact(pos int) int {
	k := min(pos, r.Decided())
	if k == 0 || 2*k < len(r.Idx) {
		return pos
	}
	r.Idx = r.Idx[:copy(r.Idx, r.Idx[k:])]
	r.cand = max(r.cand-k, -1)
	if r.Done {
		r.N -= k
	}
	return pos - k
}

// exhausted reports whether position i is past the last document index
// (C++ json_iterator::at_end), loading windows until that is known.
func (r *Reader) exhausted(i int) bool {
	for !r.Done && i >= r.Decided() {
		r.Load()
	}
	return r.Done && i >= r.N
}

// char returns the character at position i, settled first; 0 past the
// input, like C++'s padding.
func (r *Reader) char(i int) byte {
	r.exhausted(i)
	if i < len(r.Idx) {
		if off := int(r.Idx[i]); off < len(r.Buf) {
			return r.Buf[off]
		}
	}
	return 0
}

// Skip is C++ json_iterator::skip_child(0) (ondemand's Document.skipChild)
// over the whole stream: from the token at pos, at the given depth, it
// skips to the end of the value, loading windows as needed. It returns the
// position after that, or N and false if the brackets never balance.
func (r *Reader) Skip(pos, depth int) (int, bool) {
	if depth <= 0 {
		return pos, true
	}
	c := r.char(pos)
	if pos < len(r.Idx) {
		pos++
	}
	switch c {
	case '[', '{', ':', ',':
	case ']', '}':
		if depth--; depth <= 0 {
			return pos, true
		}
	case '"':
		if !r.exhausted(pos) && r.char(pos) == ':' {
			pos++ // a key: eat the ':'
			break
		}
		fallthrough
	default:
		if depth--; depth <= 0 {
			return pos, true
		}
	}
	for ; !r.exhausted(pos); pos++ {
		switch r.Buf[r.Idx[pos]] {
		case '[', '{':
			depth++
		case ']', '}':
			if depth--; depth <= 0 {
				return pos + 1, true
			}
		}
	}
	return r.N, false
}

// SkipTo is C++ document_stream::skip_to_delimiter: the position of the
// first index at or after the first delim byte from the token at pos (N if
// none), loading windows as needed; false if there is no such byte.
func (r *Reader) SkipTo(pos int, delim byte) (int, bool) {
	if r.exhausted(pos) {
		return pos, false
	}
	k := bytes.IndexByte(r.Buf[r.Idx[pos]:], delim)
	if k < 0 {
		return pos, false
	}
	boundary := r.Idx[pos] + uint32(k)
	for ; !r.exhausted(pos); pos++ {
		if r.Idx[pos] >= boundary {
			break
		}
	}
	return pos, true
}

// ScalarSource is the scalar case of C++ document_stream::iterator::source:
// the token at start, bounded by the next document at next, without
// trailing whitespace, NUL and RS (and, for CommaDelimited, commas).
func ScalarSource(buf []byte, start, next int, comma bool) []byte {
	s := buf[start:]
	n := next - start
	tok := 0
	if s[0] == '"' {
		tok = 1
		for tok < n {
			c := s[tok]
			tok++
			if c == '\\' {
				tok++
			} else if c == '"' {
				break
			}
		}
	} else {
		for tok < n {
			if c := s[tok]; isCSpace(c) || c == ',' || c == '{' || c == '[' || c == 0 || c == rs {
				break
			}
			tok++
		}
	}
	if tok > 0 && tok < n {
		n = tok
	}
	for n > 1 && (isCSpace(s[n-1]) || s[n-1] == 0 || s[n-1] == rs || comma && s[n-1] == ',') {
		n--
	}
	return s[:n:n]
}
