package ondemand

import (
	"iter"
	"strings"

	"simdjson-go/internal/jsonerr"
)

// Object is a JSON object being read.
type Object struct {
	it  valueIter
	gen uint32
}

func (o Object) check() error {
	if o.it.d == nil || o.gen != o.it.d.gen {
		return jsonerr.ErrOutOfOrderIteration
	}
	return nil
}

// Get returns the value of the field named name, searching forward from the
// cursor and then wrapping around to the fields before it, so every field
// is examined at most once (C++ operator[] / find_field_unordered). Names
// are compared as written: escapes in a name are not decoded, as in C++. A
// missing field is ErrNoSuchField.
func (o Object) Get(name string) (Value, error) {
	if err := o.check(); err != nil {
		return Value{}, err
	}
	found, err := o.it.findFieldUnorderedRaw(name)
	if err != nil {
		return Value{}, err
	}
	if !found {
		return Value{}, jsonerr.ErrNoSuchField
	}
	return newValue(o.it.child()), nil
}

// FindNext returns the value of the field named name, searching only
// forward from the cursor (C++ find_field). Reading fields in document
// order with FindNext reads the object once.
func (o Object) FindNext(name string) (Value, error) {
	if err := o.check(); err != nil {
		return Value{}, err
	}
	found, err := o.it.findFieldRaw(name)
	if err != nil {
		return Value{}, err
	}
	if !found {
		return Value{}, jsonerr.ErrNoSuchField
	}
	return newValue(o.it.child()), nil
}

// All iterates over the object's fields in order. Values not read in the
// loop body are skipped. An error ends the iteration.
func (o Object) All() iter.Seq2[Field, error] {
	return func(yield func(Field, error) bool) {
		if err := o.check(); err != nil {
			yield(Field{}, err)
			return
		}
		it := o.it
		if !it.isAtIteratorStart() {
			yield(Field{}, jsonerr.ErrOutOfOrderIteration)
			return
		}
		for it.isOpen() {
			if err := it.d.err; err != nil {
				it.d.abandon()
				yield(Field{}, err)
				return
			}
			key := it.d.pos
			if _, err := it.fieldKey(); err != nil {
				it.d.abandon()
				yield(Field{}, err)
				return
			}
			if err := it.fieldValue(); err != nil {
				it.d.abandon()
				yield(Field{}, err)
				return
			}
			if !yield(Field{key: key, value: newValue(it.child())}, nil) {
				return
			}
			if !it.isOpen() {
				return
			}
			if it.skipChild() != nil {
				continue // the error is reported at the top of the loop
			}
			if _, err := it.hasNextField(); err != nil {
				continue
			}
		}
	}
}

// Count returns the number of fields, then rewinds the object to its first
// field (C++ count_fields).
func (o Object) Count() (int, error) {
	n := 0
	for _, err := range o.All() {
		if err != nil {
			return 0, err
		}
		n++
	}
	if o.it.d.err != nil {
		return 0, o.it.d.err
	}
	_, _ = o.it.resetObject()
	return n, nil
}

// Reset moves the cursor back to the object's first field (C++ reset).
func (o Object) Reset() error {
	if err := o.check(); err != nil {
		return err
	}
	_, err := o.it.resetObject()
	return err
}

// Raw returns the object's JSON text (C++ raw_json), consuming the object.
func (o Object) Raw() ([]byte, error) {
	if err := o.check(); err != nil {
		return nil, err
	}
	it := o.it
	start := it.startOff()
	if it.isAtKey() {
		if _, err := it.fieldKey(); err != nil {
			it.d.abandon()
			return nil, err
		}
		if err := it.fieldValue(); err != nil {
			it.d.abandon()
			return nil, err
		}
	}
	if err := it.d.skipChild(it.depth - 1); err != nil {
		it.d.abandon()
		return nil, err
	}
	return it.d.rawText(start)
}

// AtPointer returns the value at the RFC 6901 JSON Pointer ptr below the
// object, looking fields up with FindNext (as C++ does).
func (o Object) AtPointer(ptr string) (Value, error) {
	if err := o.check(); err != nil {
		return Value{}, err
	}
	v, rest, err := o.step(ptr)
	if err != nil || rest == "" {
		return v, err
	}
	return atPointer(v, rest)
}

// step follows the first segment of ptr (C++ object::at_pointer without its
// recursion) and returns the child and the rest of ptr, from its '/'.
func (o Object) step(ptr string) (Value, string, error) {
	if ptr == "" || ptr[0] != '/' {
		return Value{}, "", jsonerr.ErrInvalidJSONPointer
	}
	key, rest := ptr[1:], ""
	if i := strings.IndexByte(key, '/'); i >= 0 {
		key, rest = key[:i], key[i:]
	}
	if strings.IndexByte(key, '~') >= 0 {
		var b strings.Builder
		for i := 0; i < len(key); i++ {
			if key[i] != '~' {
				b.WriteByte(key[i])
				continue
			}
			if i+1 == len(key) {
				return Value{}, "", jsonerr.ErrInvalidJSONPointer
			}
			switch key[i+1] {
			case '0':
				b.WriteByte('~')
			case '1':
				b.WriteByte('/')
			default:
				return Value{}, "", jsonerr.ErrInvalidJSONPointer
			}
			i++
		}
		key = b.String()
	}
	v, err := o.FindNext(key)
	return v, rest, err
}

// Field is a field of an object being iterated: its name and value.
type Field struct {
	key   int // token position of the name
	value Value
}

// Value returns the field's value.
func (f Field) Value() Value { return f.value }

// RawKey returns the field's name as written, escapes included, without
// the quotes. It is valid until the next Iterate.
func (f Field) RawKey() []byte {
	v := f.value
	if v.check() != nil {
		return nil
	}
	d := v.it.d
	start := int(d.idx[f.key]) + 1
	end := int(d.idx[f.key+1]) // the ':' after the name
	for end > start && d.buf[end-1] != '"' {
		end--
	}
	return d.buf[start : end-1]
}

// Key returns the field's name, unescaped, as a copy.
func (f Field) Key() (string, error) {
	v := f.value
	if err := v.check(); err != nil {
		return "", err
	}
	mark := len(v.it.d.strs)
	b, err := v.it.d.unescape(int(v.it.d.idx[f.key]))
	s := string(b)
	v.it.d.strs = v.it.d.strs[:mark]
	return s, err
}
