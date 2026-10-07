package simdjson

import (
	"encoding/binary"
	"iter"
	"math"
)

// Root returns the document's top-level value.
func (d *Document) Root() Element { return Element{doc: d, i: 1} }

// Element is a JSON value inside a Document. It is a small value type,
// valid until the next Parse on the Document's Parser.
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
	case '[', '{':
		return int(uint32(e.doc.tape[e.i])) // opening word points past the closing one
	case 'l', 'u', 'd':
		return e.i + 2
	}
	return e.i + 1
}

// rawString returns the bytes of a '"' or 'Z' element in the string buffer.
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

func (e Element) Array() (Array, error) {
	if e.tag() != '[' {
		return Array{}, ErrIncorrectType
	}
	return Array{e}, nil
}

func (e Element) Object() (Object, error) {
	if e.tag() != '{' {
		return Object{}, ErrIncorrectType
	}
	return Object{e}, nil
}

// StringValue returns a copy of a string value.
func (e Element) StringValue() (string, error) {
	b, err := e.StringBytes()
	return string(b), err
}

// StringBytes returns a string value without copying. The bytes are valid
// until the next Parse and must not be modified.
func (e Element) StringBytes() ([]byte, error) {
	if e.tag() != '"' {
		return nil, ErrIncorrectType
	}
	return e.rawString(), nil
}

// BigInt returns the raw digits of a TypeBigInt value (see Parser.BigIntAsString).
func (e Element) BigInt() (string, error) {
	if e.tag() != 'Z' {
		return "", ErrIncorrectType
	}
	return string(e.rawString()), nil
}

func (e Element) Int64() (int64, error) {
	switch e.tag() {
	case 'l':
		return int64(e.value()), nil
	case 'u':
		if v := e.value(); v <= math.MaxInt64 {
			return int64(v), nil
		}
		return 0, ErrNumberOutOfRange
	}
	return 0, ErrIncorrectType
}

func (e Element) Uint64() (uint64, error) {
	switch e.tag() {
	case 'u':
		return e.value(), nil
	case 'l':
		if v := int64(e.value()); v >= 0 {
			return uint64(v), nil
		}
		return 0, ErrNumberOutOfRange
	}
	return 0, ErrIncorrectType
}

// Float64 returns a number as float64; integers are converted.
func (e Element) Float64() (float64, error) {
	switch e.tag() {
	case 'd':
		return math.Float64frombits(e.value()), nil
	case 'l':
		return float64(int64(e.value())), nil
	case 'u':
		return float64(e.value()), nil
	}
	return 0, ErrIncorrectType
}

func (e Element) Bool() (bool, error) {
	switch e.tag() {
	case 't':
		return true, nil
	case tagFalse:
		return false, nil
	}
	return false, ErrIncorrectType
}

func (e Element) IsNull() bool { return e.tag() == 'n' }

// count returns the element count stored in an opening word, saturated at 2^24-1.
func (e Element) count() int {
	if e.doc == nil {
		return 0
	}
	return int(e.doc.tape[e.i] >> 32 & 0xFFFFFF)
}

// Array is a JSON array.
type Array struct{ e Element }

// Len returns the number of elements.
func (a Array) Len() int {
	if n := a.e.count(); n < 0xFFFFFF {
		return n
	}
	n := 0
	for range a.All() {
		n++
	}
	return n
}

// All iterates over the elements and their indices.
func (a Array) All() iter.Seq2[int, Element] {
	return func(yield func(int, Element) bool) {
		end := a.e.next() - 1 // the closing ']'
		for i, k := a.e.i+1, 0; i < end; k++ {
			v := Element{a.e.doc, i}
			if !yield(k, v) {
				return
			}
			i = v.next()
		}
	}
}

// At returns element i, walking the tape (O(i)).
func (a Array) At(i int) (Element, error) {
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
func (o Object) Len() int {
	if n := o.e.count(); n < 0xFFFFFF {
		return n
	}
	n := 0
	for range o.fields() {
		n++
	}
	return n
}

// fields iterates over the unescaped keys (not copied) and values.
func (o Object) fields() iter.Seq2[[]byte, Element] {
	return func(yield func([]byte, Element) bool) {
		end := o.e.next() - 1 // the closing '}'
		for i := o.e.i + 1; i < end; {
			k, v := Element{o.e.doc, i}, Element{o.e.doc, i + 1}
			if !yield(k.rawString(), v) {
				return
			}
			i = v.next()
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
