package simdjson

import (
	"encoding"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"reflect"
)

// addMethods layers a type's JSON and text methods over the default codec,
// in v2's order of precedence (encoding/json/v2/arshal_methods.go), and
// reports whether it added any. Methods are only attached to value types,
// so they are never called on a nil pointer or interface.
func addMethods(c *codec) bool {
	t := c.typ
	if t.Kind() == reflect.Pointer || t.Kind() == reflect.Interface {
		return false
	}
	added := false

	// Encoding: MarshalJSON > AppendText > MarshalText > default.
	// MarshalerTo cannot be called without a jsontext.Encoder.
	if _, ok := implements(t, textMarshalerType); ok {
		added = true
		c.enc = func(s *encodeState, v reflect.Value, mode uint8) error {
			b, err := v.Addr().Interface().(encoding.TextMarshaler).MarshalText()
			if err != nil {
				return s.methodErr(t, err)
			}
			return s.appendQuoted(t, b)
		}
	}
	if _, ok := implements(t, textAppenderType); ok {
		added = true
		c.enc = func(s *encodeState, v reflect.Value, mode uint8) error {
			b, err := v.Addr().Interface().(encoding.TextAppender).AppendText(s.scratch[:0])
			s.scratch = b[:0]
			if err != nil {
				return s.methodErr(t, err)
			}
			return s.appendQuoted(t, b)
		}
	}
	if _, ok := implements(t, jsonMarshalerType); ok {
		added = true
		c.enc = func(s *encodeState, v reflect.Value, mode uint8) error {
			b, err := v.Addr().Interface().(jsonv2.Marshaler).MarshalJSON()
			if err != nil {
				return s.methodErr(t, err)
			}
			if mode&modeName != 0 && !isQuoted(b) {
				return nonStringName(t)
			}
			if err := s.appendCanonical(b); err != nil {
				return &jsonv2.SemanticError{GoType: t, Err: err}
			}
			return nil
		}
	} else if _, ok := implements(t, jsonMarshalerToType); ok {
		added = true
		c.enc = func(s *encodeState, v reflect.Value, mode uint8) error {
			return &jsonv2.SemanticError{GoType: t, Err: errUnsupportedMethods}
		}
	}

	// Decoding: UnmarshalJSON > UnmarshalText > default.
	if _, ok := implements(t, textUnmarshalerType); ok {
		added = true
		c.dec = func(d *decodeState, e Element, v reflect.Value, mode uint8) error {
			switch e.tag() {
			case tagNull:
				v.SetZero()
				return nil
			case tagString:
			default:
				return d.valueErr(e, t, errNonStringValue)
			}
			if err := v.Addr().Interface().(encoding.TextUnmarshaler).UnmarshalText(e.rawString()); err != nil {
				return d.methodErr(e, t, err)
			}
			return nil
		}
	}
	if _, ok := implements(t, jsonUnmarshalerType); ok {
		added = true
		c.dec = func(d *decodeState, e Element, v reflect.Value, mode uint8) error {
			if err := d.checkDups(e); err != nil { // v2 validates the value it passes
				return err
			}
			if err := v.Addr().Interface().(jsonv2.Unmarshaler).UnmarshalJSON(d.raw(e)); err != nil {
				return d.methodErr(e, t, err)
			}
			return nil
		}
	} else if _, ok := implements(t, jsonUnmarshalerFromType); ok {
		added = true
		c.dec = func(d *decodeState, e Element, v reflect.Value, mode uint8) error {
			return d.semErr(e, t, errUnsupportedMethods)
		}
	}
	return added
}

// methodErr wraps an error from a user's unmarshal method in a
// SemanticError, unless it already is one or a SyntacticError.
func (d *decodeState) methodErr(e Element, t reflect.Type, err error) error {
	var se *jsonv2.SemanticError
	var sy *jsontext.SyntacticError
	if errors.As(err, &se) || errors.As(err, &sy) {
		return err
	}
	return &jsonv2.SemanticError{JSONPointer: d.pointer(e), JSONKind: kind(e), GoType: t, Err: err}
}

// isQuoted reports whether the JSON value b (possibly with surrounding
// whitespace) is a string.
func isQuoted(b []byte) bool {
	for _, c := range b {
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		case '"':
			return true
		}
		return false
	}
	return false
}
