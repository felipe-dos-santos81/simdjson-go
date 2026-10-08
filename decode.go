// parseUint is adapted from the Go standard library
// (src/encoding/json/internal/jsonwire/decode.go), Copyright 2023 The Go
// Authors, under the BSD-style license in LICENSE-GO.

package simdjson

import (
	"bytes"
	"encoding/base64"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"sync"

	"simdjson-go/internal/number"
)

// Unmarshal decodes the JSON document data into the Go value v points to,
// with the semantics of encoding/json/v2's Unmarshal under its default options
// (changed by opts). Malformed JSON gives a *jsontext.SyntacticError wrapping
// this package's Err* value; a document that does not fit v gives a
// *json.SemanticError (encoding/json/v2). After an error the contents of v
// are unspecified.
func Unmarshal(data []byte, v any, opts ...Option) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return &jsonv2.SemanticError{GoType: reflect.TypeOf(v), Err: errNonNilPointer}
	}
	if bytes.HasPrefix(data, bom) { // v2 does not skip a byte-order mark
		return &jsontext.SyntacticError{Err: ErrTape}
	}
	b := getBinder(maxDepth)
	defer putBinder(b)
	doc, err := b.p.Parse(data)
	if err != nil {
		return &jsontext.SyntacticError{Err: err}
	}
	d := decodeState{doc: doc, buf: data, opts: makeOptions(opts), strs: &b.strs}
	return codecFor(rv.Type().Elem()).decode(&d, doc.Root(), rv.Elem(), 0)
}

var errNonNilPointer = errors.New("value must be passed as a non-nil pointer reference")

// binder is a Parser in binding mode with its string cache.
type binder struct {
	p    Parser
	strs stringCache
}

var binders = sync.Pool{New: func() any {
	return &binder{p: Parser{BigIntAsString: true, binding: true}}
}}

// getBinder takes a binder from the pool, set to allow nesting levels of
// arrays and objects, empty ones included, as v2 counts them (MaxDepth also
// counts the root value).
func getBinder(levels int) *binder {
	b := binders.Get().(*binder)
	b.p.MaxDepth = levels + 1
	return b
}

// putBinder returns b to the pool unless it grew large, so one big document
// does not pin its buffers for the life of the process.
func putBinder(b *binder) {
	if cap(b.p.doc.tape) > 1<<20 || cap(b.p.doc.strings) > 8<<20 {
		return
	}
	binders.Put(b)
}

// decodeState is the state of one Unmarshal call.
type decodeState struct {
	doc  *Document
	buf  []byte // the input, for the raw text of values (Document.offs)
	opts options
	strs *stringCache
}

// raw returns the input text of e (a value or an object name).
func (d *decodeState) raw(e Element) []byte {
	start := int(d.doc.offs[e.i])
	switch e.tag() {
	case tagStartArray, tagStartObject:
		return d.buf[start : int(d.doc.offs[e.next()-1])+1]
	case tagString:
		return d.buf[start : start+stringEnd(d.buf[start:])]
	case tagTrue, tagNull:
		return d.buf[start : start+4]
	case tagFalse:
		return d.buf[start : start+5]
	}
	n := start // a number
	for n < len(d.buf) && !number.IsStructuralOrSpace[d.buf[n]] {
		n++
	}
	return d.buf[start:n]
}

// stringEnd returns the length of the JSON string at the start of b.
func stringEnd(b []byte) int {
	for i := 1; i < len(b); i++ {
		switch b[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return len(b)
}

// kind is the jsontext.Kind of e.
func kind(e Element) jsontext.Kind {
	switch t := e.tag(); t {
	case tagInt64, tagUint64, tagDouble, tagBigInt:
		return '0'
	case tagFalse:
		return 'f'
	default:
		return jsontext.Kind(t)
	}
}

// semErr reports that e cannot be decoded into Go type t.
func (d *decodeState) semErr(e Element, t reflect.Type, err error) error {
	se := d.semanticErr(e, t)
	se.Err = err
	return se
}

// semanticErr is a SemanticError locating e, for decoding into Go type t.
func (d *decodeState) semanticErr(e Element, t reflect.Type) *jsonv2.SemanticError {
	return &jsonv2.SemanticError{JSONPointer: d.pointer(e), JSONKind: kind(e), GoType: t}
}

// valueErr is semErr for decoders that, in v2, read the whole value before
// checking it (strings, numbers, bytes, time.Time, text and JSON methods): a
// duplicate name inside the value is reported instead.
func (d *decodeState) valueErr(e Element, t reflect.Type, err error) error {
	if e.tag() == tagStartArray || e.tag() == tagStartObject {
		if dup := d.checkDups(e); dup != nil {
			return dup
		}
	}
	return d.semErr(e, t, err)
}

// dupErr reports a duplicate object name at name element k.
func (d *decodeState) dupErr(k Element) error {
	return &jsontext.SyntacticError{JSONPointer: d.pointer(k), Err: jsontext.ErrDuplicateName}
}

// pointer returns the JSON Pointer of the value or name at tape index
// target.i, found by descending from the root. It runs only on errors.
func (d *decodeState) pointer(target Element) jsontext.Pointer {
	var p []byte
	e := d.doc.Root()
	for e.i != target.i {
		switch e.tag() {
		case tagStartArray:
			i, end := e.span()
			k := 0
			for i < end && !(target.i >= i && target.i < (Element{d.doc, i}).next()) {
				i = Element{d.doc, i}.next()
				k++
			}
			p = strconv.AppendInt(append(p, '/'), int64(k), 10)
			e = Element{d.doc, i}
		case tagStartObject:
			i, end := e.span()
			for i < end {
				v := Element{d.doc, i + 1}
				if target.i == i || target.i >= v.i && target.i < v.next() {
					break
				}
				i = v.next()
			}
			p = append(p, '/')
			for _, c := range (Element{d.doc, i}).rawString() { // RFC 6901 escaping
				switch c {
				case '~':
					p = append(p, "~0"...)
				case '/':
					p = append(p, "~1"...)
				default:
					p = append(p, c)
				}
			}
			if target.i == i {
				return jsontext.Pointer(p)
			}
			e = Element{d.doc, i + 1}
		default:
			return jsontext.Pointer(p)
		}
	}
	return jsontext.Pointer(p)
}

// checkDups returns an error if any object within e repeats a name: v2
// checks every object it reads, including skipped values. Stage 2 recorded
// the first repeated name of each object (Document.dups).
func (d *decodeState) checkDups(e Element) error {
	if len(d.doc.dups) == 0 {
		return nil
	}
	if k, ok := slices.BinarySearch(d.doc.dups, uint32(e.i)); ok || k < len(d.doc.dups) && int(d.doc.dups[k]) < e.next() {
		return d.dupErr(Element{d.doc, int(d.doc.dups[k])})
	}
	return nil
}

// firstDup returns the tape index of the first repeated name of object o
// itself (not of objects nested in it), or -1.
func (d *decodeState) firstDup(o Element) int {
	if len(d.doc.dups) == 0 {
		return -1
	}
	k, _ := slices.BinarySearch(d.doc.dups, uint32(o.i))
	end := o.next()
	if k == len(d.doc.dups) || int(d.doc.dups[k]) >= end {
		return -1 // no repeated name anywhere inside o
	}
	for name := range (Object{o}).keyIndices() {
		for k < len(d.doc.dups) && int(d.doc.dups[k]) < name {
			k++
		}
		if k == len(d.doc.dups) || int(d.doc.dups[k]) >= end {
			return -1
		}
		if int(d.doc.dups[k]) == name {
			return name
		}
	}
	return -1
}

var (
	errInvalidStringTag = errors.New("invalid use of `string` tag option")
	errNonStringValue   = errors.New("JSON value must be string type")
	errArrayUnderflow   = errors.New("too few array elements")
	errArrayOverflow    = errors.New("too many array elements")
	errNilInterface     = errors.New("cannot derive concrete type for nil interface with finite type set")
	errNilField         = errors.New("cannot set embedded pointer to unexported struct type")
	errAmbiguousName    = errors.New("ambiguous object name")
	errNoDefault        = errors.New("no default representation")
)

func makeDefaultDecoder(t reflect.Type) decodeFunc {
	switch t.Kind() {
	case reflect.Bool:
		return decodeBool
	case reflect.String:
		return decodeString
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return decodeInt
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return decodeUint
	case reflect.Float32, reflect.Float64:
		return decodeFloat
	case reflect.Map:
		return makeMapDecoder(t)
	case reflect.Struct:
		return makeStructDecoder(t)
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 && t.Elem().PkgPath() == "" {
			return decodeBytes
		}
		return makeSliceDecoder(t)
	case reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 && t.Elem().PkgPath() == "" {
			return decodeBytes
		}
		return makeArrayDecoder(t)
	case reflect.Pointer:
		return makePointerDecoder(t)
	case reflect.Interface:
		return makeInterfaceDecoder(t)
	}
	return func(d *decodeState, e Element, v reflect.Value, _ uint8) error {
		return d.semErr(e, v.Type(), nil) // complex, chan, func, …: no JSON form, even for null
	}
}

func decodeBool(d *decodeState, e Element, v reflect.Value, mode uint8) error {
	if mode&modeStringTag != 0 {
		return d.semErr(e, v.Type(), errInvalidStringTag)
	}
	switch e.tag() {
	case tagNull, tagFalse:
		v.SetBool(false)
	case tagTrue:
		v.SetBool(true)
	default:
		return d.semErr(e, v.Type(), nil)
	}
	return nil
}

func decodeString(d *decodeState, e Element, v reflect.Value, mode uint8) error {
	if mode&modeStringTag != 0 {
		return d.semErr(e, v.Type(), errInvalidStringTag)
	}
	switch e.tag() {
	case tagNull:
		v.SetString("")
	case tagString:
		v.SetString(makeString(d.strs, e.rawString()))
	default:
		return d.valueErr(e, v.Type(), nil)
	}
	return nil
}

// stringified reports whether e holds a number in the form mode requires:
// a plain number, or (for the `string` tag option and object names) a
// string whose contents s are then the number's text.
func stringified(e Element, mode uint8) (s []byte, quoted, ok bool) {
	switch e.tag() {
	case tagString:
		if mode != 0 {
			return e.rawString(), true, true
		}
	case tagInt64, tagUint64, tagDouble, tagBigInt:
		return nil, false, mode == 0
	}
	return nil, false, false
}

func decodeInt(d *decodeState, e Element, v reflect.Value, mode uint8) error {
	if e.tag() == tagNull {
		v.SetInt(0)
		return nil
	}
	s, quoted, ok := stringified(e, mode)
	if !ok {
		return d.valueErr(e, v.Type(), nil)
	}
	bits := v.Type().Bits()
	var neg bool
	var n uint64
	if quoted {
		neg = len(s) > 0 && s[0] == '-'
		if neg {
			s = s[1:]
		}
		var good bool
		if n, good = parseUint(s); !good && n != math.MaxUint64 {
			return d.valueErr(e, v.Type(), strconv.ErrSyntax)
		}
	} else {
		switch e.tag() {
		case tagInt64:
			i := int64(e.value())
			neg, n = i < 0, uint64(i)
			if neg {
				n = -n
			}
		case tagUint64:
			n = e.value()
		case tagBigInt:
			n = math.MaxUint64 // overflow
		default: // a float: JSON fractions and exponents do not parse as integers
			return d.valueErr(e, v.Type(), strconv.ErrSyntax)
		}
	}
	if limit := uint64(1) << (bits - 1); neg && n > limit || !neg && n > limit-1 {
		return d.valueErr(e, v.Type(), strconv.ErrRange)
	}
	if neg {
		v.SetInt(int64(-n))
	} else {
		v.SetInt(int64(n))
	}
	return nil
}

func decodeUint(d *decodeState, e Element, v reflect.Value, mode uint8) error {
	if e.tag() == tagNull {
		v.SetUint(0)
		return nil
	}
	s, quoted, ok := stringified(e, mode)
	if !ok {
		return d.valueErr(e, v.Type(), nil)
	}
	var n uint64
	switch {
	case quoted:
		var good bool
		if n, good = parseUint(s); !good {
			if n != math.MaxUint64 {
				return d.valueErr(e, v.Type(), strconv.ErrSyntax)
			}
			return d.valueErr(e, v.Type(), strconv.ErrRange)
		}
	case e.tag() == tagInt64:
		i := int64(e.value())
		if i < 0 || i == 0 && d.raw(e)[0] == '-' { // "-0" does not parse as unsigned
			return d.valueErr(e, v.Type(), strconv.ErrSyntax)
		}
		n = uint64(i)
	case e.tag() == tagUint64:
		n = e.value()
	case e.tag() == tagBigInt:
		if e.rawString()[0] == '-' {
			return d.valueErr(e, v.Type(), strconv.ErrSyntax)
		}
		return d.valueErr(e, v.Type(), strconv.ErrRange)
	default:
		return d.valueErr(e, v.Type(), strconv.ErrSyntax)
	}
	if bits := v.Type().Bits(); bits < 64 && n > 1<<bits-1 {
		return d.valueErr(e, v.Type(), strconv.ErrRange)
	}
	v.SetUint(n)
	return nil
}

// parseUint is v2's jsonwire.ParseUint: a decimal without sign, leading
// zeros or other characters. On overflow it returns (math.MaxUint64, false).
func parseUint(b []byte) (v uint64, ok bool) {
	var n int
	for ; len(b) > n && '0' <= b[n] && b[n] <= '9'; n++ {
		v = 10*v + uint64(b[n]-'0')
	}
	switch {
	case n == 0 || len(b) != n || (b[0] == '0' && string(b) != "0"):
		return 0, false
	case n >= 20 && (b[0] != '1' || v < 1e19 || n > 20):
		return math.MaxUint64, false
	}
	return v, true
}

func decodeFloat(d *decodeState, e Element, v reflect.Value, mode uint8) error {
	if e.tag() == tagNull {
		v.SetFloat(0)
		return nil
	}
	s, quoted, ok := stringified(e, mode)
	if !ok {
		return d.valueErr(e, v.Type(), nil)
	}
	bits := v.Type().Bits()
	if quoted && !isNumber(s) {
		return d.valueErr(e, v.Type(), strconv.ErrSyntax)
	}
	f, err := d.number(e, s, quoted, bits)
	if err != nil {
		return d.valueErr(e, v.Type(), err)
	}
	v.SetFloat(f)
	return nil
}

// number converts number e (or the quoted number text s) to a float of the
// given size, as strconv.ParseFloat would from the text. It returns
// strconv.ErrRange on overflow.
func (d *decodeState) number(e Element, s []byte, quoted bool, bits int) (float64, error) {
	var f float64
	switch {
	case quoted || bits == 32: // float32 rounds once, from the text
		if !quoted {
			s = d.raw(e)
		}
		var err error
		if f, err = strconv.ParseFloat(string(s), bits); err != nil {
			return f, strconv.ErrRange // the text is a valid JSON number
		}
		return f, nil
	case e.tag() == tagDouble:
		f = math.Float64frombits(e.value())
	case e.tag() == tagInt64:
		f = float64(int64(e.value()))
		if f == 0 && d.raw(e)[0] == '-' {
			f = math.Copysign(0, -1)
		}
	case e.tag() == tagUint64:
		f = float64(e.value())
	default: // tagBigInt
		f, _ = strconv.ParseFloat(string(e.rawString()), 64)
	}
	if math.IsInf(f, 0) {
		return f, strconv.ErrRange
	}
	return f, nil
}

// isNumber reports whether b is exactly one JSON number.
func isNumber(b []byte) bool {
	i := 0
	if i < len(b) && b[i] == '-' {
		i++
	}
	switch {
	case i < len(b) && b[i] == '0':
		i++
	case i < len(b) && '1' <= b[i] && b[i] <= '9':
		for i < len(b) && number.IsDigit(b[i]) {
			i++
		}
	default:
		return false
	}
	if i < len(b) && b[i] == '.' {
		i++
		if i == len(b) || !number.IsDigit(b[i]) {
			return false
		}
		for i < len(b) && number.IsDigit(b[i]) {
			i++
		}
	}
	if i < len(b) && (b[i] == 'e' || b[i] == 'E') {
		i++
		if i < len(b) && (b[i] == '+' || b[i] == '-') {
			i++
		}
		if i == len(b) || !number.IsDigit(b[i]) {
			return false
		}
		for i < len(b) && number.IsDigit(b[i]) {
			i++
		}
	}
	return i == len(b)
}

// decodeBytes decodes base64 (RFC 4648 §4, padding required) into []byte or [N]byte.
func decodeBytes(d *decodeState, e Element, v reflect.Value, mode uint8) error {
	if mode&modeStringTag != 0 {
		return d.semErr(e, v.Type(), errInvalidStringTag)
	}
	switch e.tag() {
	case tagNull:
		v.SetZero()
		return nil
	case tagString:
	default:
		return d.valueErr(e, v.Type(), nil)
	}
	s := e.rawString()
	var dst []byte
	if v.Kind() == reflect.Slice {
		dst = v.Bytes()[:0]
	}
	b, err := base64.StdEncoding.AppendDecode(dst, s)
	if err != nil {
		return d.valueErr(e, v.Type(), err)
	}
	if len(s) != base64.StdEncoding.EncodedLen(len(b)) { // base64 skips '\r' and '\n'; RFC 4648 does not
		i := bytes.IndexAny(s, "\r\n")
		return d.valueErr(e, v.Type(), fmt.Errorf("illegal character %q at offset %d", s[i], i))
	}
	if v.Kind() == reflect.Array {
		dst := v.Bytes()
		clear(dst[copy(dst, b):])
		if len(b) != len(dst) {
			return d.valueErr(e, v.Type(), fmt.Errorf("decoded length of %d mismatches array length of %d", len(b), len(dst)))
		}
		return nil
	}
	if b == nil {
		b = []byte{}
	}
	v.SetBytes(b)
	return nil
}

func makeSliceDecoder(t reflect.Type) decodeFunc {
	elem := codecFor(t.Elem())
	empty := reflect.MakeSlice(t, 0, 0)
	return func(d *decodeState, e Element, v reflect.Value, mode uint8) error {
		if mode&modeStringTag != 0 {
			return d.semErr(e, t, errInvalidStringTag)
		}
		switch e.tag() {
		case tagNull:
			v.SetZero()
			return nil
		case tagStartArray:
		default:
			return d.semErr(e, t, nil)
		}
		n := e.length(1)
		if n == 0 {
			v.Set(empty)
			return nil
		}
		if v.Cap() < n {
			// A new zeroed array, as MakeSlice, without boxing the header.
			// Grow from nil: growing the old slice would copy its elements.
			v.SetZero()
			v.Grow(n)
			v.SetLen(n)
		} else {
			v.SetLen(n)
			for i := range n {
				v.Index(i).SetZero()
			}
		}
		i := 0
		for c := range e.items() {
			if err := elem.decode(d, c, v.Index(i), 0); err != nil {
				return err
			}
			i++
		}
		return nil
	}
}

func makeArrayDecoder(t reflect.Type) decodeFunc {
	elem := codecFor(t.Elem())
	n := t.Len()
	return func(d *decodeState, e Element, v reflect.Value, mode uint8) error {
		if mode&modeStringTag != 0 {
			return d.semErr(e, t, errInvalidStringTag)
		}
		switch e.tag() {
		case tagNull:
			v.SetZero()
			return nil
		case tagStartArray:
		default:
			return d.semErr(e, t, nil)
		}
		i := 0
		var lengthErr error
		for c := range e.items() {
			if i >= n {
				if err := d.checkDups(c); err != nil { // skipped, as in v2
					return err
				}
				lengthErr = errArrayOverflow
				continue
			}
			ev := v.Index(i)
			ev.SetZero()
			if err := elem.decode(d, c, ev, 0); err != nil {
				return err
			}
			i++
		}
		for ; i < n; i++ {
			v.Index(i).SetZero()
			lengthErr = errArrayUnderflow
		}
		if lengthErr != nil {
			return &jsonv2.SemanticError{JSONPointer: d.pointer(e), JSONKind: '[', GoType: t, Err: lengthErr}
		}
		return nil
	}
}

func makePointerDecoder(t reflect.Type) decodeFunc {
	elem := codecFor(t.Elem())
	return func(d *decodeState, e Element, v reflect.Value, mode uint8) error {
		if e.tag() == tagNull {
			v.SetZero()
			return nil
		}
		if v.IsNil() {
			v.Set(reflect.New(t.Elem()))
		}
		return elem.decode(d, e, v.Elem(), mode)
	}
}

var anyType = reflect.TypeFor[any]()

func makeInterfaceDecoder(t reflect.Type) decodeFunc {
	isAny := t == anyType || anyType.Implements(t)
	return func(d *decodeState, e Element, v reflect.Value, mode uint8) error {
		if mode&modeStringTag != 0 {
			return d.semErr(e, t, errInvalidStringTag)
		}
		if e.tag() == tagNull {
			v.SetZero()
			return nil
		}
		if v.IsNil() {
			if !isAny {
				return d.semErr(e, t, errNilInterface)
			}
			x, err := d.decodeAny(e)
			if x != nil {
				v.Set(reflect.ValueOf(x))
			}
			return err
		}
		// Decode into a copy of the existing value, then store it back.
		cv := reflect.New(v.Elem().Type()).Elem()
		cv.Set(v.Elem())
		err := codecFor(cv.Type()).decode(d, e, cv, mode&modeName)
		v.Set(cv)
		return err
	}
}

var float64Type = reflect.TypeFor[float64]()

// decodeAny decodes e into the Go value v2 chooses for a nil any: bool,
// string, float64, map[string]any or []any.
func (d *decodeState) decodeAny(e Element) (any, error) {
	switch e.tag() {
	case tagNull:
		return nil, nil
	case tagTrue:
		return true, nil
	case tagFalse:
		return false, nil
	case tagString:
		return makeString(d.strs, e.rawString()), nil
	case tagStartArray:
		a := []any{}
		for c := range e.items() {
			x, err := d.decodeAny(c)
			a = append(a, x)
			if err != nil {
				return a, err
			}
		}
		return a, nil
	case tagStartObject:
		m := make(map[string]any, e.length(2))
		dup := d.firstDup(e)
		for k, c := range (Object{e}).AllBytes() {
			if c.i-1 == dup {
				return m, d.dupErr(Element{d.doc, dup})
			}
			name := makeString(d.strs, k)
			x, err := d.decodeAny(c)
			m[name] = x
			if err != nil {
				return m, err
			}
		}
		return m, nil
	}
	f, err := d.number(e, nil, false, 64)
	if err != nil {
		return nil, &jsonv2.SemanticError{JSONPointer: d.pointer(e), JSONKind: '0', GoType: float64Type, Err: err}
	}
	return f, nil
}

func makeMapDecoder(t reflect.Type) decodeFunc {
	key, val := codecFor(t.Key()), codecFor(t.Elem())
	emptyStruct := reflect.TypeFor[struct{}]()
	return func(d *decodeState, e Element, v reflect.Value, mode uint8) error {
		if mode&modeStringTag != 0 {
			return d.semErr(e, t, errInvalidStringTag)
		}
		switch e.tag() {
		case tagNull:
			v.SetZero()
			return nil
		case tagStartObject:
		default:
			return d.semErr(e, t, nil)
		}
		if v.IsNil() {
			v.Set(reflect.MakeMap(t))
		}
		key.init()
		// Keys of a kind with a unique representation are duplicates when
		// they decode to a key already present (so "0" and "-0" collide as
		// integers); other keys are compared as names, as in v2.
		unique := uniqueKeys(key)
		var seen reflect.Value // keys from the input, if v had entries before
		if v.Len() > 0 {
			seen = reflect.MakeMap(reflect.MapOf(t.Key(), emptyStruct))
		}
		k := reflect.New(t.Key()).Elem()
		x := reflect.New(t.Elem()).Elem()
		dup := -1
		if !unique {
			dup = d.firstDup(e)
		}
		first, end := e.span()
		for i := first; i < end; {
			ke, ve := Element{d.doc, i}, Element{d.doc, i + 1}
			i = ve.next()
			if ke.i == dup {
				return d.dupErr(ke)
			}
			k.SetZero()
			if err := key.decode(d, ke, k, modeName); err != nil {
				return err
			}
			if k.Kind() == reflect.Interface && !k.IsNil() && !k.Elem().Type().Comparable() {
				return d.semErr(ke, t, fmt.Errorf("invalid incomparable key type %v", k.Elem().Type()))
			}
			if old := v.MapIndex(k); old.IsValid() {
				if !seen.IsValid() || seen.MapIndex(k).IsValid() {
					return d.dupErr(ke)
				}
				x.Set(old)
			} else {
				x.SetZero()
			}
			err := val.decode(d, ve, x, 0)
			v.SetMapIndex(k, x)
			if seen.IsValid() {
				seen.SetMapIndex(k, reflect.Zero(emptyStruct))
			}
			if err != nil {
				return err
			}
		}
		return nil
	}
}

// uniqueKeyKind reports whether every JSON name of a map key of kind k
// decodes to a different Go value and back (v2 mapKeyWithUniqueRepresentation).
func uniqueKeyKind(k reflect.Kind) bool {
	switch k {
	case reflect.Bool, reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	}
	return false
}

func makeStructDecoder(t reflect.Type) decodeFunc {
	c := codecFor(t)
	return func(d *decodeState, e Element, v reflect.Value, mode uint8) error {
		if mode&modeStringTag != 0 {
			return d.semErr(e, t, errInvalidStringTag)
		}
		switch e.tag() {
		case tagNull:
			v.SetZero()
			return nil
		case tagStartObject:
		default:
			return d.semErr(e, t, nil)
		}
		fields, errFs := c.fields()
		if errFs != nil {
			return &jsonv2.SemanticError{JSONPointer: d.pointer(e), JSONKind: '{', GoType: errFs.GoType, Err: errFs.Err}
		}
		var seenIDs bitSet
		var err error
		dup := d.firstDup(e)
		fold := d.opts.caseInsensitive || fields.foldable
		first, end := e.span()
		for i := first; i < end; {
			ke, ve := Element{d.doc, i}, Element{d.doc, i + 1}
			i = ve.next()
			if ke.i == dup {
				return d.dupErr(ke)
			}
			name := ke.rawString()
			f := fields.lookup(name)
			if f == nil && fold {
				matches := 0
				for _, f2 := range fields.lookupByFoldedName(name) {
					if f2.matchFoldedName(d.opts.caseInsensitive) {
						if f == nil {
							f = f2 // breadth-first order
						}
						matches++
					}
				}
				if matches > 1 {
					return d.semErr(ke, t, errAmbiguousName)
				}
			}
			if f == nil { // an unknown name; repeats were caught by dup above
				if d.opts.rejectUnknown {
					return &jsonv2.SemanticError{JSONPointer: d.pointer(ke), JSONKind: '"', GoType: t, Err: jsonv2.ErrUnknownName}
				}
				if err := d.checkDups(ve); err != nil { // skipped, as in v2
					return err
				}
				continue
			}
			if !seenIDs.insert(f.id) {
				return d.dupErr(ke)
			}
			var fv reflect.Value
			if len(f.index) == 1 {
				fv = v.Field(f.index[0])
			} else if fv, err = fieldByIndexAlloc(v, f.index); err != nil {
				return d.semErr(ve, t, err)
			}
			var fmode uint8
			if f.string {
				fmode = modeStringTag
			}
			if err := f.codec.decode(d, ve, fv, fmode); err != nil {
				return err
			}
		}
		return nil
	}
}

// fieldByIndexAlloc is v.FieldByIndex that allocates nil embedded pointers.
func fieldByIndexAlloc(v reflect.Value, index []int) (reflect.Value, error) {
	for n, i := range index {
		if n > 0 && v.Kind() == reflect.Pointer {
			if v.IsNil() {
				if !v.CanSet() {
					return reflect.Value{}, errNilField
				}
				v.Set(reflect.New(v.Type().Elem()))
			}
			v = v.Elem()
		}
		v = v.Field(i)
	}
	return v, nil
}

// bitSet is a set of small non-negative integers.
type bitSet struct {
	lo uint64
	hi []uint64
}

// insert adds i and reports whether it was not already present.
func (s *bitSet) insert(i int) bool {
	if i < 64 {
		had := s.lo&(1<<i) != 0
		s.lo |= 1 << i
		return !had
	}
	w := i/64 - 1
	for len(s.hi) <= w {
		s.hi = append(s.hi, 0)
	}
	had := s.hi[w]&(1<<(i%64)) != 0
	s.hi[w] |= 1 << (i % 64)
	return !had
}
