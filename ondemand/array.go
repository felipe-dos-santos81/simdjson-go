package ondemand

import (
	"iter"
	"math"
	"strings"

	"simdjson-go/internal/jsonerr"
)

// Array is a JSON array being read.
type Array struct {
	it  valueIter
	gen uint32
}

func (a Array) check() error {
	if a.it.d == nil || a.gen != a.it.d.gen {
		return jsonerr.ErrOutOfOrderIteration
	}
	return nil
}

// All iterates over the array's elements in order. Elements not read in
// the loop body are skipped. An error ends the iteration.
func (a Array) All() iter.Seq2[Value, error] {
	return func(yield func(Value, error) bool) {
		if err := a.check(); err != nil {
			yield(Value{}, err)
			return
		}
		it := a.it
		if !it.isAtIteratorStart() {
			yield(Value{}, jsonerr.ErrOutOfOrderIteration)
			return
		}
		for it.isOpen() {
			if err := it.d.err; err != nil {
				it.d.abandon()
				yield(Value{}, err)
				return
			}
			if !yield(newValue(it.child()), nil) {
				return
			}
			if it.d.err != nil {
				continue
			}
			if it.skipChild() != nil {
				continue
			}
			if _, err := it.hasNextElement(); err != nil {
				continue
			}
		}
	}
}

// At returns the element at index i, iterating to it (C++ at). As in C++,
// an error while iterating counts as an element: if it is not at index i,
// the result is ErrIndexOutOfBounds.
func (a Array) At(i int) (Value, error) {
	if err := a.check(); err != nil {
		return Value{}, err
	}
	n := 0
	for v, err := range a.All() {
		if n == i {
			return v, err
		}
		n++
	}
	return Value{}, jsonerr.ErrIndexOutOfBounds
}

// Count returns the number of elements, then rewinds the array to its first
// element (C++ count_elements).
func (a Array) Count() (int, error) {
	n := 0
	for _, err := range a.All() {
		if err != nil {
			return 0, err
		}
		n++
	}
	if a.it.d.err != nil {
		return 0, a.it.d.err
	}
	_, _ = a.it.resetArray()
	return n, nil
}

// Reset moves the cursor back to the array's first element (C++ reset).
func (a Array) Reset() error {
	if err := a.check(); err != nil {
		return err
	}
	_, err := a.it.resetArray()
	return err
}

// Raw returns the array's JSON text (C++ raw_json), consuming the array.
func (a Array) Raw() ([]byte, error) {
	if err := a.check(); err != nil {
		return nil, err
	}
	it := a.it
	start := it.startOff()
	if err := it.d.skipChild(it.depth - 1); err != nil {
		it.d.abandon()
		return nil, err
	}
	return it.d.rawText(start)
}

// AtPointer returns the value at the RFC 6901 JSON Pointer ptr below the
// array.
func (a Array) AtPointer(ptr string) (Value, error) {
	if err := a.check(); err != nil {
		return Value{}, err
	}
	if ptr == "" || ptr[0] != '/' {
		return Value{}, jsonerr.ErrInvalidJSONPointer
	}
	ptr = ptr[1:]
	if ptr == "-" {
		return Value{}, jsonerr.ErrIndexOutOfBounds
	}
	// C++ parse_json_pointer_array_index.
	tok, rest, more := strings.Cut(ptr, "/")
	index := uint64(0)
	for i := 0; i < len(tok); i++ {
		d := tok[i] - '0'
		if d > 9 {
			return Value{}, jsonerr.ErrIncorrectType
		}
		if i > 0 && tok[0] == '0' {
			return Value{}, jsonerr.ErrInvalidJSONPointer
		}
		if index > (math.MaxUint64-uint64(d))/10 {
			return Value{}, jsonerr.ErrIndexOutOfBounds
		}
		index = index*10 + uint64(d)
	}
	if tok == "" {
		return Value{}, jsonerr.ErrInvalidJSONPointer
	}
	if index > math.MaxInt {
		return Value{}, jsonerr.ErrIndexOutOfBounds
	}
	child, err := a.At(int(index))
	if err != nil || !more {
		return child, err
	}
	return child.AtPointer("/" + rest)
}
