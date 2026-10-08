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
func (d *Document) Int64() (int64, error) { return rootScalar(d, maxRootInt, parseInteger) }

// Uint64 is Value.Uint64 for a scalar document.
func (d *Document) Uint64() (uint64, error) { return rootScalar(d, maxRootInt, parseUnsigned) }

// Float64 is Value.Float64 for a scalar document.
func (d *Document) Float64() (float64, error) { return rootScalar(d, maxRootFloat, parseDouble) }

// NumberType is Value.NumberType for a scalar document.
func (d *Document) NumberType() (NumberType, error) {
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
	it := d.root()
	off, l := it.scalarStart(), d.rootTokenLen(0)
	s := d.buf[off : off+l]
	isTrue := l >= 4 && string(s[:4]) == "true" && (l == 4 || number.IsStructuralOrSpace[s[4]])
	// As in C++, "false" is checked on its first four bytes only.
	isFalse := l >= 5 && string(s[:4]) == "fals" && (l == 5 || number.IsStructuralOrSpace[s[5]])
	if !isTrue && !isFalse {
		return false, jsonerr.ErrIncorrectType
	}
	if it.trailing() {
		return false, jsonerr.ErrTrailingContent
	}
	it.advanceScalar()
	return isTrue, nil
}

// IsNull is Value.IsNull for a scalar document.
func (d *Document) IsNull() (bool, error) {
	it := d.root()
	off, l := it.scalarStart(), d.rootTokenLen(0)
	s := d.buf[off : off+l]
	isNull := l >= 4 && string(s[:4]) == "null" && (l == 4 || number.IsStructuralOrSpace[s[4]])
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
	s := string(b)
	d.strs = d.strs[:mark]
	return s, err
}

// Type returns the root value's JSON type.
func (d *Document) Type() (Type, error) { return d.root().typ(), nil }

// Object returns the root as an object, ready to be read.
func (d *Document) Object() (Object, error) {
	it := d.root()
	if _, err := it.startRootObject(); err != nil {
		return Object{}, err
	}
	return Object{it, d.gen}, nil
}

// Array returns the root as an array, ready to be read.
func (d *Document) Array() (Array, error) {
	it := d.root()
	if _, err := it.startRootArray(); err != nil {
		return Array{}, err
	}
	return Array{it, d.gen}, nil
}

// Value returns the root array or object as a Value, to be read with
// Value's methods. A scalar root is ErrScalarDocumentAsValue: read it with
// the document's own getters.
func (d *Document) Value() (Value, error) {
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
	return Object{d.root(), d.gen}, nil
}

// Get is Object.Get on the root object.
func (d *Document) Get(name string) (Value, error) {
	o, err := d.object()
	if err != nil {
		return Value{}, err
	}
	return o.Get(name)
}

// FindNext is Object.FindNext on the root object.
func (d *Document) FindNext(name string) (Value, error) {
	o, err := d.object()
	if err != nil {
		return Value{}, err
	}
	return o.FindNext(name)
}

// Raw returns the whole document's JSON text from its first character to
// the next structural character after the root value, consuming it.
func (d *Document) Raw() ([]byte, error) {
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
	d.Rewind()
	if ptr == "" {
		return d.Value()
	}
	switch d.root().typ() {
	case TypeArray:
		a, err := d.Array()
		if err != nil {
			return Value{}, err
		}
		return a.AtPointer(ptr)
	case TypeObject:
		o, err := d.Object()
		if err != nil {
			return Value{}, err
		}
		return o.AtPointer(ptr)
	}
	return Value{}, jsonerr.ErrInvalidJSONPointer
}
