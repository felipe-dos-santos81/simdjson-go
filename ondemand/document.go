package ondemand

import (
	"simdjson-go/internal/jsonerr"
	"simdjson-go/internal/number"
)

// The document's getters read its root value. As in C++, a scalar root
// must be the whole document: content after it is ErrTrailingContent.

// root is C++ get_root_value_iterator.
func (d *Document) root() valueIter { return valueIter{d, 1, 0} }

func (d *Document) atRoot() bool { return d.pos == 0 }

// Root lengths C++ copies a root scalar into before parsing it.
const (
	maxRootInt   = 21       // 20 digits and a sign
	maxRootFloat = 1074 + 9 // the longest float C++ accepts
)

// rootScalar reads a root scalar with parse, as C++ get_root_* does.
func rootScalar[T any](d *Document, n int, parse func([]byte, byte) (T, error)) (T, error) {
	var zero T
	it := d.root()
	s, ok := it.rootScalar(n)
	if !ok {
		return zero, jsonerr.ErrNumber
	}
	x, err := parse(s, ' ')
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
	s, ok := it.rootScalar(maxRootFloat)
	if !ok {
		off := it.scalarStart()
		if !checkIfInteger(d.buf[off : off+d.rootTokenLen(0)]) {
			return 0, jsonerr.ErrNumber
		}
		if it.trailing() {
			return 0, jsonerr.ErrTrailingContent
		}
		return BigInt, nil
	}
	t, err := numberType(s, ' ')
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
	isTrue := rootLiteral(s, "true", 4)
	isFalse := rootLiteral(s, "fals", 5) // as in C++, only "fals" is compared
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

// rootLiteral is C++ get_root_bool/is_root_null's test: s starts with the
// four bytes of lit and has n bytes, or more after a terminator at s[n].
func rootLiteral(s []byte, lit string, n int) bool {
	return len(s) >= n && string(s[:4]) == lit && (len(s) == n || number.IsStructuralOrSpace[s[n]])
}

// IsNull is Value.IsNull for a scalar document.
func (d *Document) IsNull() (bool, error) {
	if err := d.check(); err != nil {
		return false, err
	}
	it := d.root()
	isNull := rootLiteral(d.rootText(), "null", 4)
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
	return d.copyString(mark, b, err)
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
	if d.peekAt(0) != '{' { // Go: C++ resumes without checking and then misreads
		return Object{}, jsonerr.ErrIncorrectType
	}
	return Object{newHandle(d.root())}, nil
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

// Raw returns the whole document's JSON text from its first character to
// the next structural character after the root value, consuming it.
func (d *Document) Raw() ([]byte, error) {
	if err := d.check(); err != nil {
		return nil, err
	}
	if !d.atRoot() {
		return nil, jsonerr.ErrOutOfOrderIteration
	}
	start := int(d.idx[0])
	switch d.root().typ() {
	case TypeArray, TypeObject:
		if err := d.skipChild(0); err != nil {
			d.abandon()
			return nil, err
		}
	default:
		d.advance()
	}
	return d.rawText(start)
}

// AtPointer rewinds the document and returns the value at the RFC 6901 JSON
// Pointer ptr, looking fields up in order. An empty pointer is the root.
func (d *Document) AtPointer(ptr string) (Value, error) {
	if err := d.check(); err != nil {
		return Value{}, err
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
