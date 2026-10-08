package ondemand

import "simdjson-go/internal/jsonerr"

// valueIter is C++ value_iterator: a value of the document, known by the
// token position where it starts and its depth. Methods keep C++'s names
// (lowerCamelCase) so the port can be compared with value_iterator-inl.h;
// C++'s development checks (SIMDJSON_DEVELOPMENT_CHECKS) are always on.
type valueIter struct {
	d     *Document
	depth int
	start int
}

func (it valueIter) isAtStart() bool { return it.d.pos == it.start }
func (it valueIter) isOpen() bool    { return it.d.depth >= it.depth }
func (it valueIter) atFirstField() bool {
	return it.d.pos == it.start+1
}
func (it valueIter) isAtIteratorStart() bool {
	delta := it.d.pos - it.start
	return delta == 1 || delta == 2
}
func (it valueIter) isAtKey() bool {
	return it.depth == it.d.depth && it.d.peek() == '"'
}
func (it valueIter) peekStart() byte { return it.d.peekAt(it.start) }
func (it valueIter) child() valueIter {
	return valueIter{it.d, it.depth + 1, it.d.pos}
}
func (it valueIter) skipChild() error { return it.d.skipChild(it.depth) }

// startOff is the input offset where the value starts.
func (it valueIter) startOff() int { return int(it.d.idx[it.start]) }

// startContainer checks that the value is the container opened by c and
// steps inside it.
func (it valueIter) startContainer(c byte) error {
	if !it.isAtStart() {
		if !it.isAtIteratorStart() {
			return jsonerr.ErrOutOfOrderIteration
		}
		if it.peekStart() != c {
			return jsonerr.ErrIncorrectType
		}
		return nil
	}
	if it.d.peek() != c {
		return jsonerr.ErrIncorrectType
	}
	it.d.advance()
	return nil
}

// endContainer leaves the container. (C++ checks for a missing parent ']'
// or '}' here only with SIMDJSON_CHECK_EOF, which release builds leave off:
// reading past the end then sees 0 and fails as a TAPE_ERROR.)
func (it valueIter) endContainer() error {
	it.d.depth = it.depth - 1
	return nil
}

// --- objects ---

func (it valueIter) startObject() (bool, error) {
	if err := it.startContainer('{'); err != nil {
		return false, err
	}
	return it.startedObject()
}

func (it valueIter) startRootObject() (bool, error) {
	if err := it.startContainer('{'); err != nil {
		return false, err
	}
	if err := it.checkRootContainer('}'); err != nil {
		return false, err
	}
	return it.startedObject()
}

func (it valueIter) startedObject() (bool, error) {
	it.d.setStart(it.depth, it.start)
	if it.d.peek() == '}' {
		it.d.advance()
		if err := it.endContainer(); err != nil {
			return false, err
		}
		return false, nil
	}
	return true, nil
}

// checkRootContainer is C++ check_root_object/check_root_array: the last
// structural must close the root. (C++ also checks the brackets balance,
// but only for truncated stream documents, which Iterate never makes.)
func (it valueIter) checkRootContainer(end byte) error {
	d := it.d
	if d.peekAt(d.n-1) != end {
		d.abandon()
		return d.fail(jsonerr.ErrIncompleteArrayOrObject)
	}
	return nil
}

func (it valueIter) hasNextField() (bool, error) {
	switch it.d.advance() {
	case '}':
		if err := it.endContainer(); err != nil {
			return false, err
		}
		return false, nil
	case ',':
		return true, nil
	}
	return false, it.d.fail(jsonerr.ErrTape)
}

// fieldKey returns the input offset of the key's opening quote.
func (it valueIter) fieldKey() (int, error) {
	k := it.d.pos
	if it.d.advance() != '"' {
		return 0, it.d.fail(jsonerr.ErrTape)
	}
	return int(it.d.idx[k]), nil
}

// fieldHead is fieldKey followed by fieldValue: it steps over a field's key
// and ':' and returns the key's offset. The common case, two well-formed
// tokens, is checked in one go; anything else takes the two steps, so errors
// and the cursor after them are as before.
func (it valueIter) fieldHead() (int, error) {
	d := it.d
	if k := d.pos; uint(k)+1 < uint(len(d.idx)) {
		idx, buf := d.idx, d.buf
		o, c := uint(idx[k]), uint(idx[k+1])
		if o < uint(len(buf)) && c < uint(len(buf)) && buf[o] == '"' && buf[c] == ':' {
			d.pos = k + 2
			d.depth = it.depth + 1
			return int(o), nil
		}
	}
	off, err := it.fieldKey()
	if err != nil {
		return 0, err
	}
	return off, it.fieldValue()
}

func (it valueIter) fieldValue() error {
	if it.d.advance() != ':' {
		return it.d.fail(jsonerr.ErrTape)
	}
	it.d.depth = it.depth + 1
	return nil
}

// keyEquals is C++ raw_json_string::unsafe_is_equal: the raw key (as
// written, escapes included) at buf[off+1:] equals key, followed by a quote.
func (it valueIter) keyEquals(off int, key string) bool {
	b := it.d.buf
	end := off + 1 + len(key)
	if end >= len(b) || b[end] != '"' {
		return false
	}
	if len(key) > 8 {
		return string(b[off+1:end]) == key
	}
	seg := b[off+1 : end] // short keys: a loop beats the memequal call
	for i := range seg {
		if seg[i] != key[i] {
			return false
		}
	}
	return true
}

func (it valueIter) findFieldRaw(key string) (bool, error) {
	var hasValue bool
	var err error
	switch {
	case it.atFirstField():
		hasValue = true
	case !it.isOpen():
		if it.d.depth < it.depth-1 {
			return false, jsonerr.ErrOutOfOrderIteration
		}
		return false, nil
	default:
		if err = it.skipChild(); err != nil {
			it.d.abandon()
			return false, err
		}
		if hasValue, err = it.hasNextField(); err != nil {
			it.d.abandon()
			return false, err
		}
		if it.d.start(it.depth) != it.start {
			return false, jsonerr.ErrOutOfOrderIteration
		}
	}
	for hasValue {
		off, err := it.fieldHead()
		if err != nil {
			it.d.abandon()
			return false, err
		}
		if it.keyEquals(off, key) {
			return true, nil
		}
		if err := it.skipChild(); err != nil {
			return false, err
		}
		if hasValue, err = it.hasNextField(); err != nil {
			it.d.abandon()
			return false, err
		}
	}
	return false, nil
}

func (it valueIter) findFieldUnorderedRaw(key string) (bool, error) {
	var hasValue bool
	var err error
	searchStart := it.d.pos
	atFirst := it.atFirstField()
	switch {
	case atFirst:
		hasValue = true
	case !it.isOpen():
		if it.d.depth < it.depth-1 {
			return false, jsonerr.ErrOutOfOrderIteration
		}
		if hasValue, err = it.resetObject(); err != nil {
			return false, err
		}
		atFirst = true
	default:
		if err = it.skipChild(); err != nil {
			it.d.abandon()
			return false, err
		}
		searchStart = it.d.pos
		if hasValue, err = it.hasNextField(); err != nil {
			it.d.abandon()
			return false, err
		}
		if it.d.start(it.depth) != it.start {
			return false, jsonerr.ErrOutOfOrderIteration
		}
	}
	for hasValue {
		off, err := it.fieldKey()
		if err != nil {
			it.d.abandon()
			return false, err
		}
		if err := it.fieldValue(); err != nil {
			it.d.abandon()
			return false, err
		}
		if it.keyEquals(off, key) {
			return true, nil
		}
		if err := it.skipChild(); err != nil {
			return false, err
		}
		if hasValue, err = it.hasNextField(); err != nil {
			it.d.abandon()
			return false, err
		}
	}
	if atFirst {
		return false, nil
	}
	// Wrap around: search from the first field up to where we started. C++
	// assumes the fields before searchStart were read without error, so none
	// of these steps can fail; Go checks, so that misuse (a handle used out
	// of order over malformed input) ends in an error instead of a loop.
	if _, err := it.resetObject(); err != nil {
		return false, err
	}
	for {
		off, err := it.fieldKey()
		if err != nil {
			it.d.abandon()
			return false, err
		}
		if err := it.fieldValue(); err != nil {
			it.d.abandon()
			return false, err
		}
		if it.keyEquals(off, key) {
			return true, nil
		}
		if err := it.skipChild(); err != nil {
			return false, err
		}
		if it.d.pos == searchStart {
			return false, nil
		}
		if it.d.pos > searchStart {
			return false, jsonerr.ErrOutOfOrderIteration
		}
		if _, err := it.hasNextField(); err != nil {
			it.d.abandon()
			return false, err
		}
	}
}

// --- arrays ---

func (it valueIter) startArray() (bool, error) {
	if err := it.startContainer('['); err != nil {
		return false, err
	}
	return it.startedArray()
}

func (it valueIter) startRootArray() (bool, error) {
	if err := it.startContainer('['); err != nil {
		return false, err
	}
	if err := it.checkRootContainer(']'); err != nil {
		return false, err
	}
	return it.startedArray()
}

func (it valueIter) startedArray() (bool, error) {
	if it.d.peek() == ']' {
		it.d.advance()
		if err := it.endContainer(); err != nil {
			return false, err
		}
		return false, nil
	}
	it.d.depth = it.depth + 1
	it.d.setStart(it.depth, it.start)
	return true, nil
}

func (it valueIter) hasNextElement() (bool, error) {
	switch it.d.advance() {
	case ']':
		if err := it.endContainer(); err != nil {
			return false, err
		}
		return false, nil
	case ',':
		it.d.depth = it.depth + 1
		return true, nil
	}
	return false, it.d.fail(jsonerr.ErrTape)
}

// --- resetting ---

func (it valueIter) moveAtContainerStart() {
	it.d.depth = it.depth
	it.d.pos = it.start + 1
}

func (it valueIter) resetArray() (bool, error) {
	if it.d.err != nil {
		return false, it.d.err
	}
	it.moveAtContainerStart()
	return it.startedArray()
}

func (it valueIter) resetObject() (bool, error) {
	if it.d.err != nil {
		return false, it.d.err
	}
	it.moveAtContainerStart()
	return it.startedObject()
}

// --- scalars ---

// scalarStart returns the input offset of a scalar value: C++ peek_scalar,
// which reads at the value's own position whether or not the cursor is
// there (so scalar values may be read later).
func (it valueIter) scalarStart() int { return int(it.d.idx[it.start]) }

// advanceScalar moves the cursor past a scalar that was just read, if the
// cursor is at it.
func (it valueIter) advanceScalar() {
	if !it.isAtStart() {
		return
	}
	if d := it.d; d.pos < len(d.idx) { // advance without reading the token
		d.pos++
	}
	it.d.depth = it.depth - 1
}

// rootScalar returns the bytes C++ copies to its temporary buffer for a
// root scalar (peek_root_length bytes), or ok false if they exceed n.
func (it valueIter) rootScalar(n int) (s []byte, ok bool) {
	off := it.scalarStart()
	l := it.d.rootTokenLen(it.start)
	if l > n {
		return nil, false
	}
	return it.d.buf[off : off+l], true
}

// trailing reports C++ TRAILING_CONTENT: a root scalar followed by more.
func (it valueIter) trailing() bool { return it.d.n != 1 }

// typ is C++ value_iterator::type.
func (it valueIter) typ() Type {
	switch it.peekStart() {
	case '{':
		return TypeObject
	case '[':
		return TypeArray
	case '"':
		return TypeString
	case 'n':
		return TypeNull
	case 't', 'f':
		return TypeBool
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return TypeNumber
	}
	return TypeUnknown
}
