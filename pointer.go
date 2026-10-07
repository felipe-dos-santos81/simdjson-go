package simdjson

import (
	"math"
	"strings"
)

// AtPointer returns the value at the RFC 6901 JSON Pointer ptr, relative to
// e. Port of the C++ DOM at_pointer methods, including their error codes.
func (e Element) AtPointer(ptr string) (Element, error) {
	for ptr != "" {
		if ptr[0] != '/' {
			return Element{}, ErrInvalidJSONPointer
		}
		token, rest := ptr[1:], ""
		if i := strings.IndexByte(token, '/'); i >= 0 {
			token, rest = token[:i], token[i:]
		}
		var err error
		switch e.tag() {
		case tagStartObject:
			e, err = Object{e}.pointerChild(token)
		case tagStartArray:
			if token == "-" && rest == "" { // the position after the last element
				return Element{}, ErrIndexOutOfBounds
			}
			e, err = Array{e}.pointerChild(token)
		default:
			if pointerWellFormed(ptr) { // descending into a scalar (simdjson issue 2154)
				return Element{}, ErrNoSuchField
			}
			return Element{}, ErrInvalidJSONPointer
		}
		if err != nil {
			return Element{}, err
		}
		ptr = rest
	}
	return e, nil
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
// parse_json_pointer_array_index).
func (a Array) pointerChild(token string) (Element, error) {
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
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '~' {
			b.WriteByte(s[i])
			continue
		}
		if i+1 == len(s) {
			return "", false
		}
		switch s[i+1] {
		case '0':
			b.WriteByte('~')
		case '1':
			b.WriteByte('/')
		default:
			return "", false
		}
		i++
	}
	return b.String(), true
}

// pointerWellFormed is C++ is_pointer_well_formed for a pointer already known
// to start with '/': its first ~ escape must be valid.
func pointerWellFormed(ptr string) bool {
	i := strings.IndexByte(ptr, '~')
	return i < 0 || i+1 < len(ptr) && (ptr[i+1] == '0' || ptr[i+1] == '1')
}
