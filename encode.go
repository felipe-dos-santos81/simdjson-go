// String quoting and float formatting are adapted from the Go standard
// library (src/encoding/json/internal/jsonwire/encode.go), Copyright 2023 The
// Go Authors, under the BSD-style license in LICENSE-GO.

package simdjson

import (
	"bytes"
	"encoding/base64"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// Marshal returns the JSON encoding of v, with the semantics of
// encoding/json/v2's Marshal under its default options (changed by opts).
func Marshal(v any, opts ...Option) ([]byte, error) {
	bp, _ := bufPool.Get().(*[]byte)
	if bp == nil {
		bp = new([]byte)
	}
	b, err := MarshalAppend((*bp)[:0], v, opts...)
	var out []byte
	if err == nil {
		out = bytes.Clone(b) // exactly sized, like v2's
	}
	if cap(b) <= maxPooledBuf {
		*bp = b
		bufPool.Put(bp)
	}
	return out, err
}

// bufPool holds Marshal's scratch buffers (*[]byte); larger ones are dropped.
var bufPool sync.Pool

const maxPooledBuf = 8 << 20

// MarshalAppend appends the JSON encoding of v to dst. On error it returns dst
// unchanged.
func MarshalAppend(dst []byte, v any, opts ...Option) ([]byte, error) {
	s := encodeState{buf: dst, opts: makeOptions(opts)}
	if v == nil {
		return append(dst, "null"...), nil
	}
	rv := reflect.ValueOf(v)
	av := reflect.New(rv.Type()).Elem() // addressable, so pointer-receiver methods run
	av.Set(rv)
	if err := codecFor(av.Type()).encode(&s, av, 0); err != nil {
		return dst, err
	}
	return s.buf, nil
}

// encodeState is the state of one Marshal call.
type encodeState struct {
	buf     []byte
	opts    options
	scratch []byte // for AppendText
	depth   int    // open arrays and objects
	seen    map[any]struct{}
}

// maxDepth and startCycleCheck are v2's nesting limit and the depth after
// which it starts looking for pointer cycles.
const (
	maxDepth        = 10000
	startCycleCheck = 1000
)

func (s *encodeState) semErr(t reflect.Type, err error) error {
	return &jsonv2.SemanticError{GoType: t, Err: err}
}

// methodErr wraps an error from a user's marshal method in a SemanticError,
// unless it already is one.
func (s *encodeState) methodErr(t reflect.Type, err error) error {
	if _, ok := err.(*jsonv2.SemanticError); ok {
		return err
	}
	return s.semErr(t, err)
}

// open counts a new array or object, enforcing v2's depth limit.
func (s *encodeState) open() error {
	if s.depth++; s.depth > maxDepth {
		return &jsontext.SyntacticError{Err: ErrDepth}
	}
	return nil
}

// visit records pointer-like value v past the cycle-check depth; it fails if
// v is already being encoded.
func (s *encodeState) visit(t reflect.Type, v reflect.Value) (leave func(), err error) {
	if s.depth < startCycleCheck {
		return func() {}, nil
	}
	if s.seen == nil {
		s.seen = make(map[any]struct{})
	}
	type ptrKey struct {
		p   uintptr
		t   reflect.Type
		len int
	}
	k := ptrKey{v.Pointer(), t, 0}
	if v.Kind() == reflect.Slice {
		k.len = v.Len()
	}
	if _, ok := s.seen[k]; ok {
		return nil, s.semErr(t, fmt.Errorf("encountered a cycle via %v", t))
	}
	s.seen[k] = struct{}{}
	return func() { delete(s.seen, k) }, nil
}

// appendQuoted appends text from a text method as a JSON string.
func (s *encodeState) appendQuoted(t reflect.Type, b []byte) error {
	var ok bool
	if s.buf, ok = appendQuoteBytes(s.buf, b); !ok {
		return s.semErr(t, &jsontext.SyntacticError{Err: ErrUTF8})
	}
	return nil
}

// appendQuote quotes s as v2 does by default (minimal escaping; invalid
// UTF-8 is replaced by U+FFFD). Use appendQuoteBytes to detect invalid UTF-8.
func appendQuote(dst []byte, s string) []byte {
	dst, _ = appendQuoteBytes(dst, []byte(s))
	return dst
}

// appendQuoteBytes is v2's jsonwire.AppendQuote without the HTML and JS
// escaping options: only '"', '\\' and bytes below 0x20 are escaped. It
// reports false if src is not valid UTF-8.
func appendQuoteBytes(dst, src []byte) ([]byte, bool) {
	ok := true
	dst = append(dst, '"')
	i := 0
	for n := 0; n < len(src); {
		c := src[n]
		if c < utf8.RuneSelf {
			n++
			if c >= 0x20 && c != '"' && c != '\\' {
				continue
			}
			dst = append(dst, src[i:n-1]...)
			switch c {
			case '"', '\\':
				dst = append(dst, '\\', c)
			case '\b':
				dst = append(dst, `\b`...)
			case '\f':
				dst = append(dst, `\f`...)
			case '\n':
				dst = append(dst, `\n`...)
			case '\r':
				dst = append(dst, `\r`...)
			case '\t':
				dst = append(dst, `\t`...)
			default:
				const hex = "0123456789abcdef"
				dst = append(dst, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xF])
			}
			i = n
			continue
		}
		r, rn := utf8.DecodeRune(src[n:])
		n += rn
		if r == utf8.RuneError && rn == 1 {
			ok = false
			dst = append(dst, src[i:n-rn]...)
			dst = append(dst, "�"...)
			i = n
		}
	}
	dst = append(dst, src[i:]...)
	return append(dst, '"'), ok
}

// appendFloat is v2's jsonwire.AppendFloat: the shortest representation that
// round-trips at the given bit size, in exponent form below 1e-6 and from
// 1e21 on, with "e-09" shortened to "e-9".
func appendFloat(dst []byte, f float64, bits int) []byte {
	if bits == 32 {
		f = float64(float32(f))
	}
	abs := math.Abs(f)
	fmt := byte('f')
	if abs != 0 {
		if bits == 64 && (abs < 1e-6 || abs >= 1e21) ||
			bits == 32 && (float32(abs) < 1e-6 || float32(abs) >= 1e21) {
			fmt = 'e'
		}
	}
	dst = strconv.AppendFloat(dst, f, fmt, -1, bits)
	if fmt == 'e' {
		n := len(dst)
		if n >= 4 && dst[n-4] == 'e' && dst[n-3] == '-' && dst[n-2] == '0' {
			dst[n-2] = dst[n-1]
			dst = dst[:n-1]
		}
	}
	return dst
}

func makeDefaultEncoder(t reflect.Type) encodeFunc {
	switch t.Kind() {
	case reflect.Bool:
		return func(s *encodeState, v reflect.Value, mode uint8) error {
			if mode&modeStringTag != 0 {
				return s.semErr(t, errInvalidStringTag)
			}
			s.buf = strconv.AppendBool(s.buf, v.Bool())
			return nil
		}
	case reflect.String:
		return func(s *encodeState, v reflect.Value, mode uint8) error {
			if mode&modeStringTag != 0 {
				return s.semErr(t, errInvalidStringTag)
			}
			var ok bool
			if s.buf, ok = appendQuoteBytes(s.buf, []byte(v.String())); !ok {
				return &jsontext.SyntacticError{Err: ErrUTF8}
			}
			return nil
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return func(s *encodeState, v reflect.Value, mode uint8) error {
			s.appendNumber(mode, func(b []byte) []byte { return strconv.AppendInt(b, v.Int(), 10) })
			return nil
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return func(s *encodeState, v reflect.Value, mode uint8) error {
			s.appendNumber(mode, func(b []byte) []byte { return strconv.AppendUint(b, v.Uint(), 10) })
			return nil
		}
	case reflect.Float32, reflect.Float64:
		bits := t.Bits()
		return func(s *encodeState, v reflect.Value, mode uint8) error {
			f := v.Float()
			if math.IsNaN(f) || math.IsInf(f, 0) {
				return s.semErr(t, fmt.Errorf("unsupported value: %v", f))
			}
			s.appendNumber(mode, func(b []byte) []byte { return appendFloat(b, f, bits) })
			return nil
		}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 && t.Elem().PkgPath() == "" {
			return func(s *encodeState, v reflect.Value, mode uint8) error {
				if mode&modeStringTag != 0 {
					return s.semErr(t, errInvalidStringTag)
				}
				if s.opts.nilSliceAsNull && v.Kind() == reflect.Slice && v.IsNil() {
					s.buf = append(s.buf, "null"...)
					return nil
				}
				s.buf = append(s.buf, '"')
				s.buf = base64.StdEncoding.AppendEncode(s.buf, v.Bytes())
				s.buf = append(s.buf, '"')
				return nil
			}
		}
		return makeSequenceEncoder(t)
	case reflect.Map:
		return makeMapEncoder(t)
	case reflect.Struct:
		return makeStructEncoder(t)
	case reflect.Pointer:
		elem := codecFor(t.Elem())
		return func(s *encodeState, v reflect.Value, mode uint8) error {
			if v.IsNil() {
				s.buf = append(s.buf, "null"...)
				return nil
			}
			leave, err := s.visit(t, v)
			if err != nil {
				return err
			}
			defer leave()
			return elem.encode(s, v.Elem(), mode)
		}
	case reflect.Interface:
		return func(s *encodeState, v reflect.Value, mode uint8) error {
			if mode&modeStringTag != 0 {
				return s.semErr(t, errInvalidStringTag)
			}
			if v.IsNil() {
				s.buf = append(s.buf, "null"...)
				return nil
			}
			cv := reflect.New(v.Elem().Type()).Elem() // addressable copy
			cv.Set(v.Elem())
			return codecFor(cv.Type()).encode(s, cv, mode&modeName)
		}
	}
	return func(s *encodeState, v reflect.Value, mode uint8) error {
		return s.semErr(t, nil) // complex, chan, func, …: no JSON form
	}
}

// appendNumber appends a number, quoted for the `string` tag option and
// for object names.
func (s *encodeState) appendNumber(mode uint8, f func([]byte) []byte) {
	if mode == 0 {
		s.buf = f(s.buf)
		return
	}
	s.buf = append(s.buf, '"')
	s.buf = f(s.buf)
	s.buf = append(s.buf, '"')
}

func makeSequenceEncoder(t reflect.Type) encodeFunc {
	elem := codecFor(t.Elem())
	return func(s *encodeState, v reflect.Value, mode uint8) error {
		if mode&modeStringTag != 0 {
			return s.semErr(t, errInvalidStringTag)
		}
		if mode&modeName != 0 {
			return nonStringName(t)
		}
		if v.Kind() == reflect.Slice {
			if v.Len() == 0 {
				if s.opts.nilSliceAsNull && v.IsNil() {
					s.buf = append(s.buf, "null"...)
				} else {
					s.buf = append(s.buf, "[]"...)
				}
				return nil
			}
			leave, err := s.visit(t, v)
			if err != nil {
				return err
			}
			defer leave()
		}
		if err := s.open(); err != nil {
			return err
		}
		s.buf = append(s.buf, '[')
		for i := range v.Len() {
			if i > 0 {
				s.buf = append(s.buf, ',')
			}
			if err := elem.encode(s, v.Index(i), 0); err != nil {
				return err
			}
		}
		s.buf = append(s.buf, ']')
		s.depth--
		return nil
	}
}

func makeMapEncoder(t reflect.Type) encodeFunc {
	key, val := codecFor(t.Key()), codecFor(t.Elem())
	return func(s *encodeState, v reflect.Value, mode uint8) error {
		if mode&modeStringTag != 0 {
			return s.semErr(t, errInvalidStringTag)
		}
		if mode&modeName != 0 {
			return nonStringName(t)
		}
		n := v.Len()
		if n == 0 {
			if s.opts.nilMapAsNull && v.IsNil() {
				s.buf = append(s.buf, "null"...)
			} else {
				s.buf = append(s.buf, "{}"...)
			}
			return nil
		}
		leave, err := s.visit(t, v)
		if err != nil {
			return err
		}
		defer leave()
		if err := s.open(); err != nil {
			return err
		}
		key.init()
		unique := !key.nonDefault && uniqueKeyKind(t.Key().Kind())
		k := reflect.New(t.Key()).Elem()
		x := reflect.New(t.Elem()).Elem()
		s.buf = append(s.buf, '{')
		if !s.opts.deterministic || n == 1 {
			var names map[string]struct{}
			if !unique {
				names = make(map[string]struct{}, n)
			}
			first := true
			for it := v.MapRange(); it.Next(); {
				if !first {
					s.buf = append(s.buf, ',')
				}
				first = false
				k.SetIterKey(it)
				start := len(s.buf)
				if err := s.encodeName(t, key, k); err != nil {
					return err
				}
				if names != nil {
					name := unquote(s.buf[start:])
					if _, dup := names[name]; dup {
						return &jsontext.SyntacticError{Err: jsontext.ErrDuplicateName}
					}
					names[name] = struct{}{}
				}
				s.buf = append(s.buf, ':')
				x.SetIterValue(it)
				if err := val.encode(s, x, 0); err != nil {
					return err
				}
			}
		} else {
			// Sort the entries by unquoted name, as v2's Deterministic does.
			type entry struct {
				name, quoted string
				val          reflect.Value
			}
			entries := make([]entry, 0, n)
			for it := v.MapRange(); it.Next(); {
				k.SetIterKey(it)
				start := len(s.buf)
				if err := s.encodeName(t, key, k); err != nil {
					return err
				}
				quoted := string(s.buf[start:])
				s.buf = s.buf[:start]
				ev := reflect.New(t.Elem()).Elem()
				ev.SetIterValue(it)
				entries = append(entries, entry{unquote([]byte(quoted)), quoted, ev})
			}
			slices.SortFunc(entries, func(a, b entry) int { return strings.Compare(a.name, b.name) })
			for i, en := range entries {
				if i > 0 {
					s.buf = append(s.buf, ',')
					if !unique && en.name == entries[i-1].name {
						return &jsontext.SyntacticError{Err: jsontext.ErrDuplicateName}
					}
				}
				s.buf = append(s.buf, en.quoted...)
				s.buf = append(s.buf, ':')
				if err := val.encode(s, en.val, 0); err != nil {
					return err
				}
			}
		}
		s.buf = append(s.buf, '}')
		s.depth--
		return nil
	}
}

// encodeName appends map key k as an object name, which must encode as a
// JSON string.
func (s *encodeState) encodeName(t reflect.Type, key *codec, k reflect.Value) error {
	start := len(s.buf)
	if err := key.encode(s, k, modeName); err != nil {
		return err
	}
	if len(s.buf) == start || s.buf[start] != '"' {
		return nonStringName(t.Key())
	}
	return nil
}

// nonStringName is v2's error for a map key that does not encode as a JSON
// string: a SemanticError wrapping a SyntacticError.
func nonStringName(t reflect.Type) error {
	return &jsonv2.SemanticError{GoType: t, Err: &jsontext.SyntacticError{Err: jsontext.ErrNonStringName}}
}

// unquote returns the value of a JSON string this package produced.
func unquote(q []byte) string {
	if bytes.IndexByte(q, '\\') < 0 {
		return string(q[1 : len(q)-1])
	}
	b, _ := appendUnescaped(nil, q, 1)
	return string(b)
}

func makeStructEncoder(t reflect.Type) encodeFunc {
	var (
		once   sync.Once
		fields structFields
		errFs  *jsonv2.SemanticError
	)
	return func(s *encodeState, v reflect.Value, mode uint8) error {
		if mode&modeStringTag != 0 {
			return s.semErr(t, errInvalidStringTag)
		}
		if mode&modeName != 0 {
			return nonStringName(t)
		}
		once.Do(func() { fields, errFs = makeStructFields(t) })
		if errFs != nil {
			return errFs
		}
		if err := s.open(); err != nil {
			return err
		}
		s.buf = append(s.buf, '{')
		first := true
		for i := range fields.flattened {
			f := &fields.flattened[i]
			fv, ok := fieldByIndex(v, f.index)
			if !ok {
				continue // a nil embedded pointer
			}
			if f.omitzero && (f.isZero == nil && fv.IsZero() || f.isZero != nil && f.isZero(fv)) {
				continue
			}
			f.cod.init()
			if f.omitempty && !f.cod.nonDefault && f.isEmpty != nil && f.isEmpty(fv) {
				continue
			}
			start := len(s.buf)
			if !first {
				s.buf = append(s.buf, ',')
			}
			s.buf = append(s.buf, f.quotedName...)
			s.buf = append(s.buf, ':')
			valStart := len(s.buf)
			var fmode uint8
			if f.string {
				fmode = modeStringTag
			}
			if err := f.cod.encode(s, fv, fmode); err != nil {
				return err
			}
			if f.omitempty && isEmptyJSON(s.buf[valStart:]) {
				s.buf = s.buf[:start] // the value encoded as an empty JSON value
				continue
			}
			first = false
		}
		s.buf = append(s.buf, '}')
		s.depth--
		return nil
	}
}

// isEmptyJSON reports whether b is null, "", {} or [].
func isEmptyJSON(b []byte) bool {
	switch string(b) {
	case "null", `""`, "{}", "[]":
		return true
	}
	return false
}

// fieldByIndex is v.FieldByIndex that reports false at a nil embedded pointer.
func fieldByIndex(v reflect.Value, index []int) (reflect.Value, bool) {
	for n, i := range index {
		if n > 0 && v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return reflect.Value{}, false
			}
			v = v.Elem()
		}
		v = v.Field(i)
	}
	return v, true
}

// appendCanonical appends the JSON value b from a MarshalJSON method in v2's
// form: whitespace removed, strings re-quoted minimally, numbers as written.
// Invalid JSON and duplicate object names are errors.
func (s *encodeState) appendCanonical(b []byte) error {
	if bytes.HasPrefix(b, bom) {
		return &jsontext.SyntacticError{Err: ErrTape}
	}
	bd := binders.Get().(*binder)
	defer putBinder(bd)
	doc, err := bd.p.Parse(b)
	if err != nil {
		return &jsontext.SyntacticError{Err: err}
	}
	d := decodeState{doc: doc, buf: b}
	if err := d.checkDups(doc.Root()); err != nil {
		return err
	}
	s.buf = doc.Root().appendJSON(s.buf, d.raw)
	return nil
}
