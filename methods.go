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

	// Encoding: MarshalJSONTo > MarshalJSON > AppendText > MarshalText >
	// default. MarshalJSONTo needs a jsontext.Encoder, so a type that has it
	// is unsupported: v2 would call it, and anything else would differ.
	if _, ok := implements(t, textMarshalerType); ok {
		added = true
		c.enc = func(s *encodeState, v reflect.Value, mode uint8) error {
			b, err := v.Addr().Interface().(encoding.TextMarshaler).MarshalText()
			if err != nil {
				return textMethodErr(&jsonv2.SemanticError{GoType: t}, err, "MarshalText", false)
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
				return textMethodErr(&jsonv2.SemanticError{GoType: t}, err, "AppendText", false)
			}
			return s.appendQuoted(t, b)
		}
	}
	if _, ok := implements(t, jsonMarshalerType); ok {
		added = true
		c.enc = func(s *encodeState, v reflect.Value, mode uint8) error {
			b, err := v.Addr().Interface().(jsonv2.Marshaler).MarshalJSON()
			if err != nil {
				return jsonMethodErr(&jsonv2.SemanticError{GoType: t}, err, "MarshalJSON")
			}
			if mode&modeName != 0 && !isQuoted(b) {
				return nonStringName(t)
			}
			if err := s.appendCanonical(b); err != nil {
				return &jsonv2.SemanticError{GoType: t, Err: err}
			}
			return nil
		}
	}
	if _, ok := implements(t, jsonMarshalerToType); ok {
		added = true
		c.enc = func(s *encodeState, v reflect.Value, mode uint8) error {
			return &jsonv2.SemanticError{GoType: t, Err: errUnsupportedMethods}
		}
	}

	// Decoding: UnmarshalJSONFrom > UnmarshalJSON > UnmarshalText > default,
	// with UnmarshalJSONFrom unsupported as MarshalJSONTo is.
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
				return textMethodErr(d.semanticErr(e, t), err, "UnmarshalText", true)
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
			raw := d.raw(e)
			// The full slice expression keeps a method that appends to its
			// argument from writing into the caller's input.
			if err := v.Addr().Interface().(jsonv2.Unmarshaler).UnmarshalJSON(raw[:len(raw):len(raw)]); err != nil {
				return jsonMethodErr(d.semanticErr(e, t), err, "UnmarshalJSON")
			}
			return nil
		}
	}
	if _, ok := implements(t, jsonUnmarshalerFromType); ok {
		added = true
		c.dec = func(d *decodeState, e Element, v reflect.Value, mode uint8) error {
			return d.semErr(e, t, errUnsupportedMethods)
		}
	}
	return added
}

// methodErr replaces errors.ErrUnsupported from a user's method, as v2 does:
// v2 reserves it for its own use.
func methodErr(err error, method string) error {
	if errors.Is(err, errors.ErrUnsupported) {
		return errors.New(method + " method may not return errors.ErrUnsupported")
	}
	return err
}

// textMethodErr is v2's wrapping of an error from a text method: a
// *SemanticError (or, when decoding, a *SyntacticError) passes through as is;
// anything else becomes outer's Err.
func textMethodErr(outer *jsonv2.SemanticError, err error, method string, decoding bool) error {
	err = methodErr(err, method)
	switch err.(type) {
	case *jsonv2.SemanticError:
		return err
	case *jsontext.SyntacticError:
		if decoding {
			return err
		}
	}
	outer.Err = err
	return outer
}

// jsonMethodErr is v2's wrapping of an error from MarshalJSON or
// UnmarshalJSON: always wrapped in outer, except that a *SemanticError the
// method returned is merged into it, its JSONPointer taken as relative.
func jsonMethodErr(outer *jsonv2.SemanticError, err error, method string) error {
	err = methodErr(err, method)
	if inner, ok := err.(*jsonv2.SemanticError); ok {
		merged := *inner
		merged.JSONPointer = outer.JSONPointer + inner.JSONPointer
		return &merged
	}
	outer.Err = err
	return outer
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
