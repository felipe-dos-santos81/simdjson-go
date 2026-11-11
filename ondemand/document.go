package ondemand

import (
	"simdjson-go/internal/jsonerr"
	"simdjson-go/internal/number"
	"simdjson-go/internal/stream"
)

// The document's getters read its root value. As in C++, a scalar root
// must be the whole document: content after it is ErrTrailingContent.

// root is C++ get_root_value_iterator.
func (d *Document) root() valueIter { return valueIter{d, 1, 0} }

func (d *Document) atRoot() bool { return d.pos == 0 }

// Root lengths C++ copies a root number into before parsing it. They limit
// the number's own text: the white space, commas and RS after it do not
// count (valueIter.rootScalar). A number of exactly maxRootFloat bytes is
// followed by NUL in C++'s buffer, so Float64 fails on it.
const (
	maxRootInt   = 21       // 20 digits and a sign
	maxRootFloat = 1074 + 9 // the longest float C++ accepts
)

// rootScalar reads a root scalar with parse, as C++ get_root_* does.
func rootScalar[T any](d *Document, n int, parse func([]byte, byte) (T, error)) (T, error) {
	var zero T
	it := d.root()
	s, pad, ok := it.rootScalar(n)
	if !ok {
		return zero, jsonerr.ErrNumber
	}
	x, err := parse(s, pad)
	if err != nil {
		return zero, err
	}
	if it.trailing() {
		return zero, jsonerr.ErrTrailingContent
	}
	it.advanceScalar()
	return x, nil
}

// Int64 is Value.Int64 for a scalar document.
func (d *Document) Int64() (int64, error) {
	if err := d.check(); err != nil {
		return 0, err
	}
	return rootScalar(d, maxRootInt, parseInteger)
}

// Uint64 is Value.Uint64 for a scalar document.
func (d *Document) Uint64() (uint64, error) {
	if err := d.check(); err != nil {
		return 0, err
	}
	return rootScalar(d, maxRootInt, parseUnsigned)
}

// Float64 is Value.Float64 for a scalar document.
func (d *Document) Float64() (float64, error) {
	if err := d.check(); err != nil {
		return 0, err
	}
	return rootScalar(d, maxRootFloat, parseDouble)
}

// NumberType is Value.NumberType for a scalar document.
func (d *Document) NumberType() (NumberType, error) {
	if err := d.check(); err != nil {
		return 0, err
	}
	it := d.root()
	s, pad, ok := it.rootScalar(maxRootFloat)
	if !ok { // C++ checks the whole token, white space and all
		if !checkIfInteger(d.rootText()) {
			return 0, jsonerr.ErrNumber
		}
		if it.trailing() {
			return 0, jsonerr.ErrTrailingContent
		}
		return BigInt, nil
	}
	t, err := numberType(s, pad)
	if err == nil && it.trailing() {
		return 0, jsonerr.ErrTrailingContent
	}
	return t, err
}

// Bool is Value.Bool for a scalar document.
func (d *Document) Bool() (bool, error) {
	if err := d.check(); err != nil {
		return false, err
	}
	it := d.root()
	s := d.rootText()
	isTrue, isFalse := rootLiteral(s, "true"), rootLiteral(s, "false")
	if !isTrue && !isFalse {
		return false, jsonerr.ErrIncorrectType
	}
	if it.trailing() {
		return false, jsonerr.ErrTrailingContent
	}
	it.advanceScalar()
	return isTrue, nil
}

// rootText is what C++ copies for a root scalar: from it to the next
// structural character (C++ peek_root_length).
func (d *Document) rootText() []byte {
	off := int(d.idx[0])
	return d.buf[off : off+d.rootTokenLen(0)]
}

// rootLiteral is C++ get_root_bool/is_root_null's test: s is lit, or lit
// and a terminator. Value's parseBool and IsNull read a padded copy
// instead; C++ has the two variants too.
func rootLiteral(s []byte, lit string) bool {
	n := len(lit)
	return len(s) >= n && string(s[:n]) == lit && (len(s) == n || number.IsStructuralOrSpace[s[n]])
}

// IsNull is Value.IsNull for a scalar document.
func (d *Document) IsNull() (bool, error) {
	if err := d.check(); err != nil {
		return false, err
	}
	it := d.root()
	isNull := rootLiteral(d.rootText(), "null")
	switch {
	case isNull:
		if it.trailing() {
			return false, jsonerr.ErrTrailingContent
		}
		it.advanceScalar()
	case d.peekAt(0) == 'n':
		return false, jsonerr.ErrIncorrectType
	}
	return isNull, nil
}

// StringBytes is Value.StringBytes for a scalar document.
func (d *Document) StringBytes() ([]byte, error) {
	if err := d.check(); err != nil {
		return nil, err
	}
	it := d.root()
	if d.peekAt(0) != '"' {
		return nil, jsonerr.ErrIncorrectType
	}
	if it.trailing() {
		return nil, jsonerr.ErrTrailingContent
	}
	it.advanceScalar()
	return d.unescape(it.scalarStart())
}

// String is Value.String for a scalar document.
func (d *Document) String() (string, error) {
	mark := len(d.strs)
	b, err := d.StringBytes()
	return d.copyString(mark, b), err
}

// Type returns the root value's JSON type.
func (d *Document) Type() (Type, error) {
	if err := d.check(); err != nil {
		return 0, err
	}
	return d.root().typ(), nil
}

// Object returns the root as an object, ready to be read.
func (d *Document) Object() (Object, error) {
	if err := d.check(); err != nil {
		return Object{}, err
	}
	it := d.root()
	if _, err := it.startRootObject(); err != nil {
		return Object{}, err
	}
	return Object{newHandle(it)}, nil
}

// Array returns the root as an array, ready to be read.
func (d *Document) Array() (Array, error) {
	if err := d.check(); err != nil {
		return Array{}, err
	}
	it := d.root()
	if _, err := it.startRootArray(); err != nil {
		return Array{}, err
	}
	return Array{newHandle(it)}, nil
}

// Value returns the root array or object as a Value, to be read with
// Value's methods. A scalar root is ErrScalarDocumentAsValue: read it with
// the document's own getters.
func (d *Document) Value() (Value, error) {
	if err := d.check(); err != nil {
		return Value{}, err
	}
	if !d.atRoot() {
		return Value{}, jsonerr.ErrOutOfOrderIteration
	}
	switch d.peek() {
	case '[':
		if err := d.root().checkRootContainer(']'); err != nil {
			return Value{}, err
		}
	case '{':
		if err := d.root().checkRootContainer('}'); err != nil {
			return Value{}, err
		}
	default:
		return Value{}, jsonerr.ErrScalarDocumentAsValue
	}
	return newValue(d.root()), nil
}

// object starts the root object, or resumes it (C++ start_or_resume_object).
func (d *Document) object() (Object, error) {
	if d.atRoot() {
		return d.Object()
	}
	it := d.root()
	if err := it.startOrResumeObject(); err != nil { // resumes: not at the start
		return Object{}, err
	}
	return Object{newHandle(it)}, nil
}

// Get is Object.Get on the root object.
func (d *Document) Get(name string) (Value, error) {
	if err := d.check(); err != nil {
		return Value{}, err
	}
	o, err := d.object()
	if err != nil {
		return Value{}, err
	}
	return o.Get(name)
}

// FindNext is Object.FindNext on the root object.
func (d *Document) FindNext(name string) (Value, error) {
	if err := d.check(); err != nil {
		return Value{}, err
	}
	o, err := d.object()
	if err != nil {
		return Value{}, err
	}
	return o.FindNext(name)
}

// Raw returns the root value's JSON text from its first byte to its last,
// consuming it (C++ document::raw_json): an array or object ends on its
// closing bracket, a scalar at the end of its token. As in C++ it is not
// order-checked: wherever the cursor is, it reads the rest of the root, or
// nothing if the root was read to its end, and returns the root's text.
func (d *Document) Raw() ([]byte, error) {
	if err := d.check(); err != nil {
		return nil, err
	}
	start := int(d.idx[0])
	switch d.root().typ() {
	case TypeArray, TypeObject:
		if err := d.skipChild(0); err != nil {
			d.abandon()
			return nil, err
		}
		return d.rawText(start)
	}
	if d.depth > 0 { // C++ consume: step over the scalar, unless a read did
		d.advance()
		d.depth = 0
	}
	return d.scalarText(start), nil
}

// scalarText is the end of C++ document::raw_json for a scalar root at
// offset start: a string ends at its closing quote; anything else at the
// first white space, structural character, quote, RS or NUL. Neither runs
// past the cursor's structural.
func (d *Document) scalarText(start int) []byte {
	next := len(d.buf)
	if d.pos < len(d.idx) {
		next = min(int(d.idx[d.pos]), next)
	}
	next = max(next, start) // C++ is undefined below start
	buf, end := d.buf, start+1
	if buf[start] == '"' {
		for end < next {
			c := buf[end]
			end++
			if c == '\\' {
				end++
			} else if c == '"' {
				break
			}
		}
		end = min(end, next)
	} else {
		for end < next && !number.IsStructuralOrSpace[buf[end]] && buf[end] != '"' && buf[end] != stream.RS && buf[end] != 0 {
			end++
		}
	}
	return buf[start:end]
}

// AtPointer rewinds the document and returns the value at the RFC 6901 JSON
// Pointer ptr, looking fields up in order. An empty pointer is the root.
// A document that a fatal error abandoned cannot be rewound: every later
// call returns that error (C++ v5.1.1).
func (d *Document) AtPointer(ptr string) (Value, error) {
	if err := d.check(); err != nil {
		return Value{}, err
	}
	if d.abandoned() {
		return Value{}, d.err
	}
	d.Rewind()
	if ptr == "" {
		return d.Value()
	}
	switch d.root().typ() {
	case TypeArray, TypeObject:
		v, err := d.Value() // checks the root container, as C++'s get_array/get_object do
		if err != nil {
			return Value{}, err
		}
		return atPointer(v, ptr)
	}
	return Value{}, jsonerr.ErrInvalidJSONPointer
}
