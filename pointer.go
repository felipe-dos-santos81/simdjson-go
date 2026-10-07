package simdjson

import (
	"math"
	"strings"
)

// AtPointer returns the value at the RFC 6901 JSON Pointer ptr, relative to
// e. Port of the C++ DOM at_pointer methods, including their error codes.
func (e Element) AtPointer(ptr string) (Element, error) {
	if ptr == "" {
		return e, nil
	}
	if ptr[0] != '/' {
		return Element{}, ErrInvalidJSONPointer
	}
	token, rest := ptr[1:], ""
	if i := strings.IndexByte(token, '/'); i >= 0 {
		token, rest = token[:i], token[i:]
	}
	var child Element
	var err error
	switch e.tag() {
	case tagStartObject:
		child, err = Object{e}.pointerChild(token)
	case tagStartArray:
		child, err = Array{e}.pointerChild(token, rest == "")
	default:
		if pointerWellFormed(ptr) { // descending into a scalar (simdjson issue 2154)
			return Element{}, ErrNoSuchField
		}
		return Element{}, ErrInvalidJSONPointer
	}
	if err != nil || rest == "" {
		return child, err
	}
	return child.AtPointer(rest)
}

// pointerChild returns the field named by the (still ~-escaped) pointer token.
func (o Object) pointerChild(token string) (Element, error) {
	if strings.IndexByte(token, '~') >= 0 {
		var ok bool
		if token, ok = unescapePointerToken(token); !ok {
			return Element{}, ErrInvalidJSONPointer
		}
	}
	return o.Get(token)
}

// pointerChild returns the element at the pointer token's index (C++
// parse_json_pointer_array_index). last reports whether the token ends the pointer.
func (a Array) pointerChild(token string, last bool) (Element, error) {
	if token == "-" && last { // the position after the last element
		return Element{}, ErrIndexOutOfBounds
	}
	if token == "" {
		return Element{}, ErrInvalidJSONPointer
	}
	var index uint64
	for n := 0; n < len(token); n++ {
		d := token[n] - '0'
		if d > 9 {
			return Element{}, ErrIncorrectType
		}
		if n > 0 && token[0] == '0' {
			return Element{}, ErrInvalidJSONPointer // leading zero
		}
		if index > (math.MaxUint64-uint64(d))/10 {
			return Element{}, ErrIndexOutOfBounds // C++ wraps around instead (see the spec, §9)
		}
		index = index*10 + uint64(d)
	}
	if index > math.MaxInt {
		return Element{}, ErrIndexOutOfBounds
	}
	return a.At(int(index))
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
