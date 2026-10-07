package simdjson

import (
	"encoding/binary"
	"iter"
	"math"
)

// Root returns the document's top-level value.
func (d *Document) Root() Element { return Element{doc: d, i: 1} }

// Element is a JSON value inside a Document. It is a small value type.
// Like the Document, every Element, Array, Object and string bytes obtained
// from it is valid only until the next call to the Parser's Parse, including
// a call that fails; using one afterwards may return wrong values or panic.
type Element struct {
	doc *Document
	i   int // tape index
}

// tag returns the tape tag of e, or 0 for the zero Element (returned with errors).
func (e Element) tag() byte {
	if e.doc == nil {
		return 0
	}
	return byte(e.doc.tape[e.i] >> 56)
}

func (e Element) value() uint64 { return e.doc.tape[e.i+1] } // second word of a number

// next returns the tape index just past e.
func (e Element) next() int {
	switch e.tag() {
	case tagStartArray, tagStartObject:
		return int(uint32(e.doc.tape[e.i])) // opening word points past the closing one
	case tagInt64, tagUint64, tagDouble:
		return e.i + 2
	}
	return e.i + 1
}

// rawString returns the bytes of a string or big-integer element in the string buffer.
func (e Element) rawString() []byte {
	off := e.doc.tape[e.i] & (1<<56 - 1)
	n := uint64(binary.LittleEndian.Uint32(e.doc.strings[off:]))
	return e.doc.strings[off+4 : off+4+n : off+4+n]
}

// Type returns the type of e.
func (e Element) Type() Type {
	if t := e.tag(); t != tagFalse {
		return Type(t)
	}
	return TypeBool
}

// Array returns e as an Array, or ErrIncorrectType if it is not an array.
func (e Element) Array() (Array, error) {
	if e.tag() != tagStartArray {
		return Array{}, ErrIncorrectType
	}
	return Array{e}, nil
}

// Object returns e as an Object, or ErrIncorrectType if it is not an object.
func (e Element) Object() (Object, error) {
	if e.tag() != tagStartObject {
		return Object{}, ErrIncorrectType
	}
	return Object{e}, nil
}

// StringValue returns a copy of a string value.
func (e Element) StringValue() (string, error) {
	b, err := e.StringBytes()
	return string(b), err
}

// StringBytes returns a string value without copying. The bytes alias the
// parser's buffer: they are valid until the next Parse and must not be modified.
func (e Element) StringBytes() ([]byte, error) {
	if e.tag() != tagString {
		return nil, ErrIncorrectType
	}
	return e.rawString(), nil
}

// BigInt returns the raw digits of a TypeBigInt value (see Parser.BigIntAsString).
func (e Element) BigInt() (string, error) {
	if e.tag() != tagBigInt {
		return "", ErrIncorrectType
	}
	return string(e.rawString()), nil
}

// Int64 returns an integer as int64: an int64 as is, a uint64 if it is at
// most math.MaxInt64 (else ErrNumberOutOfRange). Other types give ErrIncorrectType.
func (e Element) Int64() (int64, error) {
	switch e.tag() {
	case tagInt64:
		return int64(e.value()), nil
	case tagUint64:
		if v := e.value(); v <= math.MaxInt64 {
			return int64(v), nil
		}
		return 0, ErrNumberOutOfRange
	}
	return 0, ErrIncorrectType
}

// Uint64 returns an integer as uint64: a uint64 as is, an int64 if it is not
// negative (else ErrNumberOutOfRange). Other types give ErrIncorrectType.
func (e Element) Uint64() (uint64, error) {
	switch e.tag() {
	case tagUint64:
		return e.value(), nil
	case tagInt64:
		if v := int64(e.value()); v >= 0 {
			return uint64(v), nil
		}
		return 0, ErrNumberOutOfRange
	}
	return 0, ErrIncorrectType
}

// Float64 returns a number (int64, uint64 or float64) as float64; other types
// give ErrIncorrectType.
func (e Element) Float64() (float64, error) {
	switch e.tag() {
	case tagDouble:
		return math.Float64frombits(e.value()), nil
	case tagInt64:
		return float64(int64(e.value())), nil
	case tagUint64:
		return float64(e.value()), nil
	}
	return 0, ErrIncorrectType
}

// Bool returns a boolean value, or ErrIncorrectType if e is not a boolean.
func (e Element) Bool() (bool, error) {
	switch e.tag() {
	case tagTrue:
		return true, nil
	case tagFalse:
		return false, nil
	}
	return false, ErrIncorrectType
}

// IsNull reports whether e is JSON null.
func (e Element) IsNull() bool { return e.tag() == tagNull }

// items iterates over the tape entries inside an array or object: its
// elements, or its keys and values alternating.
func (e Element) items() iter.Seq[Element] {
	return func(yield func(Element) bool) {
		end := e.next() - 1 // the closing ']' or '}'
		for i := e.i + 1; i < end; {
			v := Element{e.doc, i}
			if !yield(v) {
				return
			}
			i = v.next()
		}
	}
}

// length returns the number of elements (perItem 1) or fields (perItem 2,
// a key and a value). The count stored in the opening word saturates at
// 2^24-1; past that the tape is walked.
func (e Element) length(perItem int) int {
	if e.doc == nil {
		return 0
	}
	if n := int(e.doc.tape[e.i] >> 32 & 0xFFFFFF); n < 0xFFFFFF {
		return n
	}
	n := 0
	for range e.items() {
		n++
	}
	return n / perItem
}

// Array is a JSON array.
type Array struct{ e Element }

// Len returns the number of elements.
func (a Array) Len() int { return a.e.length(1) }

// All iterates over the elements and their indices.
func (a Array) All() iter.Seq2[int, Element] {
	return func(yield func(int, Element) bool) {
		k := 0
		for v := range a.e.items() {
			if !yield(k, v) {
				return
			}
			k++
		}
	}
}

// At returns element i, walking the tape (O(i)).
func (a Array) At(i int) (Element, error) {
	if uint(i) >= uint(a.Len()) { // also rejects negative i, without walking
		return Element{}, ErrIndexOutOfBounds
	}
	for k, v := range a.All() {
		if k == i {
			return v, nil
		}
	}
	return Element{}, ErrIndexOutOfBounds
}

// Object is a JSON object.
type Object struct{ e Element }

// Len returns the number of fields.
func (o Object) Len() int { return o.e.length(2) }

// fields iterates over the unescaped keys (not copied) and values.
func (o Object) fields() iter.Seq2[[]byte, Element] {
	return func(yield func([]byte, Element) bool) {
		var key []byte
		isKey := true
		for v := range o.e.items() {
			if isKey {
				key = v.rawString()
			} else if !yield(key, v) {
				return
			}
			isKey = !isKey
		}
	}
}

// All iterates over the fields in document order. Keys are copies.
func (o Object) All() iter.Seq2[string, Element] {
	return func(yield func(string, Element) bool) {
		for k, v := range o.fields() {
			if !yield(string(k), v) {
				return
			}
		}
	}
}

// Get returns the value of the first field whose unescaped key equals key.
func (o Object) Get(key string) (Element, error) {
	for k, v := range o.fields() {
		if string(k) == key {
			return v, nil
		}
	}
	return Element{}, ErrNoSuchField
}
