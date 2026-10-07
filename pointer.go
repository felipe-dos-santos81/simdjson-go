package simdjson

import (
	"math"
	"strings"
)

// AtPointer returns the value at the RFC 6901 JSON Pointer ptr, relative to
// e. Port of the C++ DOM at_pointer methods, including their error codes.
func (e Element) AtPointer(ptr string) (Element, error) {
	switch e.tag() {
	case '{':
		return Object{e}.atPointer(ptr)
	case '[':
		return Array{e}.atPointer(ptr)
	}
	switch {
	case ptr == "":
		return e, nil
	case pointerWellFormed(ptr): // descending into a scalar (simdjson issue 2154)
		return Element{}, ErrNoSuchField
	}
	return Element{}, ErrInvalidJSONPointer
}

func (o Object) atPointer(ptr string) (Element, error) {
	if ptr == "" {
		return o.e, nil
	}
	if ptr[0] != '/' {
		return Element{}, ErrInvalidJSONPointer
	}
	rest := ptr[1:]
	key := rest
	slash := strings.IndexByte(rest, '/')
	if slash >= 0 {
		key = rest[:slash]
	}
	if strings.IndexByte(key, '~') >= 0 {
		var ok bool
		if key, ok = unescapePointerToken(key); !ok {
			return Element{}, ErrInvalidJSONPointer
		}
	}
	child, err := o.Get(key)
	if err != nil || slash < 0 {
		return child, err
	}
	return child.AtPointer(rest[slash:])
}

func (a Array) atPointer(ptr string) (Element, error) {
	if ptr == "" {
		return a.e, nil
	}
	if ptr[0] != '/' {
		return Element{}, ErrInvalidJSONPointer
	}
	ptr = ptr[1:]
	if ptr == "-" { // the position after the last element
		return Element{}, ErrIndexOutOfBounds
	}
	// parse_json_pointer_array_index
	var index uint64
	n := 0
	for ; n < len(ptr) && ptr[n] != '/'; n++ {
		d := ptr[n] - '0'
		if d > 9 {
			return Element{}, ErrIncorrectType
		}
		if n > 0 && ptr[0] == '0' {
			return Element{}, ErrInvalidJSONPointer // leading zero
		}
		if index > (math.MaxUint64-uint64(d))/10 {
			return Element{}, ErrIndexOutOfBounds
		}
		index = index*10 + uint64(d)
	}
	if n == 0 {
		return Element{}, ErrInvalidJSONPointer
	}
	if index > math.MaxInt {
		return Element{}, ErrIndexOutOfBounds
	}
	child, err := a.At(int(index))
	if err != nil || n == len(ptr) {
		return child, err
	}
	return child.AtPointer(ptr[n:])
}

// unescapePointerToken replaces ~0 with ~ and ~1 with /. Any other use of ~ is invalid.
func unescapePointerToken(s string) (string, bool) {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '~' {
			b = append(b, s[i])
			continue
		}
		if i+1 == len(s) {
			return "", false
		}
		switch s[i+1] {
		case '0':
			b = append(b, '~')
		case '1':
			b = append(b, '/')
		default:
			return "", false
		}
		i++
	}
	return string(b), true
}

// pointerWellFormed is C++ is_pointer_well_formed: a leading '/' and a valid
// first ~ escape. ptr must be non-empty.
func pointerWellFormed(ptr string) bool {
	if ptr[0] != '/' {
		return false
	}
	i := strings.IndexByte(ptr, '~')
	return i < 0 || i+1 < len(ptr) && (ptr[i+1] == '0' || ptr[i+1] == '1')
}
