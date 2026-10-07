// Struct field rules, tag parsing and name folding are adapted from the Go
// standard library (src/encoding/json/v2/fields.go and fold.go), Copyright
// 2020-2021 The Go Authors, under the BSD-style license in LICENSE-GO.

package simdjson

import (
	"cmp"
	"encoding"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// A codec decodes and encodes one Go type. Codecs are built on first use and
// cached; a codec is in the cache before its functions are built, so
// recursive types resolve to themselves.
type codec struct {
	typ        reflect.Type
	once       sync.Once
	dec        decodeFunc
	enc        encodeFunc
	nonDefault bool // the type has marshal or unmarshal methods (or is a time type)

	planOnce sync.Once // struct types: the field plan, shared by dec and enc
	plan     structFields
	planErr  *jsonv2.SemanticError
}

// fields returns the field plan of struct type c.typ, built on first use.
func (c *codec) fields() (*structFields, *jsonv2.SemanticError) {
	c.planOnce.Do(func() { c.plan, c.planErr = makeStructFields(c.typ) })
	return &c.plan, c.planErr
}

// uniqueKeys reports whether a map whose key codec is key gives each key
// exactly one JSON name (see uniqueKeyKind).
func uniqueKeys(key *codec) bool { return !key.nonDefault && uniqueKeyKind(key.typ.Kind()) }

// decodeFunc decodes e into the addressable v.
type decodeFunc func(d *decodeState, e Element, v reflect.Value, mode uint8) error

// encodeFunc appends v (addressable) to s.buf.
type encodeFunc func(s *encodeState, v reflect.Value, mode uint8) error

// Modes of decodeFunc and encodeFunc.
const (
	modeStringTag uint8 = 1 << iota // the field has the `string` tag option
	modeName                        // the value is an object name (a map key)
)

var codecs sync.Map // reflect.Type → *codec

func codecFor(t reflect.Type) *codec {
	if c, ok := codecs.Load(t); ok {
		return c.(*codec)
	}
	c, _ := codecs.LoadOrStore(t, &codec{typ: t})
	return c.(*codec)
}

func (c *codec) init() {
	c.once.Do(func() {
		c.dec, c.enc = makeDefaultDecoder(c.typ), makeDefaultEncoder(c.typ)
		c.nonDefault = addMethods(c)
		if c.typ == timeTimeType || c.typ == timeDurationType {
			c.dec, c.enc = makeTimeDecoder(c.typ), makeTimeEncoder(c.typ)
			c.nonDefault = true
		}
	})
}

func (c *codec) decode(d *decodeState, e Element, v reflect.Value, mode uint8) error {
	c.init()
	return c.dec(d, e, v, mode)
}

func (c *codec) encode(s *encodeState, v reflect.Value, mode uint8) error {
	c.init()
	return c.enc(s, v, mode)
}

var (
	jsonMarshalerType       = reflect.TypeFor[jsonv2.Marshaler]()
	jsonMarshalerToType     = reflect.TypeFor[jsonv2.MarshalerTo]()
	jsonUnmarshalerType     = reflect.TypeFor[jsonv2.Unmarshaler]()
	jsonUnmarshalerFromType = reflect.TypeFor[jsonv2.UnmarshalerFrom]()
	textAppenderType        = reflect.TypeFor[encoding.TextAppender]()
	textMarshalerType       = reflect.TypeFor[encoding.TextMarshaler]()
	textUnmarshalerType     = reflect.TypeFor[encoding.TextUnmarshaler]()
	isZeroerType            = reflect.TypeFor[interface{ IsZero() bool }]()
	jsontextValueType       = reflect.TypeFor[jsontext.Value]()
	timeTimeType            = reflect.TypeFor[time.Time]()
	timeDurationType        = reflect.TypeFor[time.Duration]()

	allMarshalerTypes   = []reflect.Type{jsonMarshalerToType, jsonMarshalerType, textAppenderType, textMarshalerType}
	allUnmarshalerTypes = []reflect.Type{jsonUnmarshalerFromType, jsonUnmarshalerType, textUnmarshalerType}
	allMethodTypes      = append(slices.Clip(allMarshalerTypes), allUnmarshalerTypes...)
)

// implements reports whether t or *t implements iface, and whether only *t does.
func implements(t, iface reflect.Type) (needAddr, ok bool) {
	switch {
	case t.Implements(iface):
		return false, true
	case reflect.PointerTo(t).Implements(iface):
		return true, true
	}
	return false, false
}

func implementsAny(t reflect.Type, ifaces ...reflect.Type) bool {
	for _, iface := range ifaces {
		if _, ok := implements(t, iface); ok {
			return true
		}
	}
	return false
}

// errUnsupportedMethods is returned for types whose only JSON methods are the
// jsontext-based MarshalerTo/UnmarshalerFrom, which this package cannot call.
var errUnsupportedMethods = errors.New("MarshalJSONTo and UnmarshalJSONFrom methods are not supported")

type isZeroer interface{ IsZero() bool }

// structFields is the list of JSON-representable fields of a struct type.
type structFields struct {
	flattened    []structField // depth-first order
	byFoldedName map[string][]*structField
	foldable     bool             // some field is tagged `case:ignore`
	byLen        [][]*structField // byLen[n]: the fields whose name is n bytes long
}

// lookup returns the field named exactly name, or nil. Indexing by length
// first rejects most unknown names without comparing bytes.
func (fs *structFields) lookup(name []byte) *structField {
	if len(name) >= len(fs.byLen) {
		return nil
	}
	for _, f := range fs.byLen[len(name)] {
		if f.name == string(name) {
			return f
		}
	}
	return nil
}

func (fs *structFields) lookupByFoldedName(name []byte) []*structField {
	return fs.byFoldedName[string(foldName(name))]
}

type structField struct {
	id      int   // breadth-first ID, used for duplicate detection
	index   []int // according to reflect.Value.FieldByIndex
	typ     reflect.Type
	codec   *codec
	isZero  func(reflect.Value) bool
	isEmpty func(reflect.Value) bool
	fieldOptions
}

var errNoExportedFields = errors.New("Go struct has no exported fields")

// makeStructFields is v2's makeStructFields (fields.go) without embedded
// fallbacks: an embedded Go map or jsontext.Value is reported as unsupported.
func makeStructFields(root reflect.Type) (fs structFields, serr *jsonv2.SemanticError) {
	orErrorf := func(serr *jsonv2.SemanticError, t reflect.Type, f string, a ...any) *jsonv2.SemanticError {
		return cmp.Or(serr, &jsonv2.SemanticError{GoType: t, Err: fmt.Errorf(f, a...)})
	}

	// Breadth-first search, so that len(f.index) increases monotonically.
	type queueEntry struct {
		typ           reflect.Type
		index         []int
		visitChildren bool // whether to visit embedded fields of this struct
	}
	queue := []queueEntry{{root, nil, true}}
	seen := map[reflect.Type]bool{root: true}
	var allFields []structField
	for qi := 0; qi < len(queue); qi++ {
		qe := queue[qi]
		t := qe.typ
		namesIndex := make(map[string]int) // field index per JSON name in this struct
		var hasAnyJSONTag, hasAnyJSONField bool
		for i := range t.NumField() {
			sf := t.Field(i)
			_, hasTag := sf.Tag.Lookup("json")
			hasAnyJSONTag = hasAnyJSONTag || hasTag
			options, ignored, err := parseFieldOptions(sf)
			if err != nil {
				serr = cmp.Or(serr, &jsonv2.SemanticError{GoType: t, Err: err})
			}
			if ignored {
				continue
			}
			hasAnyJSONField = true
			f := structField{
				index:        append(append(make([]int, 0, len(qe.index)+1), qe.index...), i),
				typ:          sf.Type,
				fieldOptions: options,
			}
			if sf.Anonymous && !f.hasName {
				if indirectType(f.typ).Kind() != reflect.Struct {
					serr = orErrorf(serr, t, "embedded Go struct field %s of non-struct type must be explicitly given a JSON name", sf.Name)
				} else {
					f.embed = true // implied by Go embedding without an explicit name
				}
			}

			var handleEmbed, handleField func()
			handleEmbed = func() {
				if f.fieldOptions != (fieldOptions{name: f.name, quotedName: f.quotedName, embed: true}) {
					serr = orErrorf(serr, t, "Go struct field %s cannot have any options other than `embed` specified", sf.Name)
					if f.hasName {
						handleField()
						return // invalid embedded field; treat as regular field
					}
					f.fieldOptions = fieldOptions{name: f.name, quotedName: f.quotedName, embed: f.embed}
				}
				tf := indirectType(f.typ)
				if implementsAny(tf, allMethodTypes...) && tf != jsontextValueType {
					serr = orErrorf(serr, t, "embedded Go struct field %s of type %s must not implement marshal or unmarshal methods", sf.Name, tf)
				}
				if tf.Kind() == reflect.Struct {
					if qe.visitChildren {
						queue = append(queue, queueEntry{tf, f.index, !seen[tf]})
					}
					seen[tf] = true
					return
				} else if !sf.IsExported() {
					serr = orErrorf(serr, t, "embedded Go struct field %s is not exported", sf.Name)
					return
				}
				// v2 accepts an embedded Go map or jsontext.Value as a fallback
				// for unknown names; this package does not support it.
				serr = orErrorf(serr, t, "embedded Go struct field %s of type %s is not supported (only Go structs may be embedded)", sf.Name, tf)
			}
			handleField = func() {
				if !sf.IsExported() {
					tf := indirectType(f.typ)
					if !(sf.Anonymous && tf.Kind() == reflect.Struct) {
						serr = orErrorf(serr, t, "Go struct field %s is not exported", sf.Name)
						return
					}
					if implementsAny(tf, allMethodTypes...) || (f.omitzero && implementsAny(tf, isZeroerType)) {
						serr = orErrorf(serr, t, "Go struct field %s is not exported for method calls", sf.Name)
						return
					}
				}
				switch {
				case sf.Type.Kind() == reflect.Interface && sf.Type.Implements(isZeroerType):
					f.isZero = func(v reflect.Value) bool {
						return v.IsNil() || (v.Elem().Kind() == reflect.Pointer && v.Elem().IsNil()) || v.Interface().(isZeroer).IsZero()
					}
				case sf.Type.Kind() == reflect.Pointer && sf.Type.Implements(isZeroerType):
					f.isZero = func(v reflect.Value) bool { return v.IsNil() || v.Interface().(isZeroer).IsZero() }
				case sf.Type.Implements(isZeroerType):
					f.isZero = func(v reflect.Value) bool { return v.Interface().(isZeroer).IsZero() }
				case reflect.PointerTo(sf.Type).Implements(isZeroerType):
					f.isZero = func(v reflect.Value) bool { return v.Addr().Interface().(isZeroer).IsZero() }
				}
				switch sf.Type.Kind() {
				case reflect.String, reflect.Map, reflect.Array, reflect.Slice:
					f.isEmpty = func(v reflect.Value) bool { return v.Len() == 0 }
				case reflect.Pointer, reflect.Interface:
					f.isEmpty = func(v reflect.Value) bool { return v.IsNil() }
				}
				if j, ok := namesIndex[f.name]; ok {
					serr = orErrorf(serr, t, "Go struct fields %s and %s conflict over JSON object name %q", t.Field(j).Name, sf.Name, f.name)
				}
				namesIndex[f.name] = i
				f.id = len(allFields)
				f.codec = codecFor(sf.Type)
				allFields = append(allFields, f)
				if f.format != "" {
					serr = orErrorf(serr, t, "Go struct field %s has `format` tag option, which is not supported", sf.Name)
				}
			}
			if f.embed {
				handleEmbed()
			} else {
				handleField()
			}
		}
		// Refuse a struct with fields but none JSON-representable and no
		// `json` tags (e.g. the errors.New type), as v2 does.
		if t.NumField() > 0 && !hasAnyJSONTag && !hasAnyJSONField {
			serr = cmp.Or(serr, &jsonv2.SemanticError{GoType: t, Err: errNoExportedFields})
		}
	}

	// Keep the dominant field per name: the one alone at the shallowest depth,
	// or uniquely tagged with a JSON name there.
	flattened := allFields[:0]
	slices.SortStableFunc(allFields, func(x, y structField) int {
		return cmp.Or(
			strings.Compare(x.name, y.name),
			cmp.Compare(len(x.index), len(y.index)),
			boolsCompare(!x.hasName, !y.hasName))
	})
	for len(allFields) > 0 {
		n := 1
		for n < len(allFields) && allFields[n-1].name == allFields[n].name {
			n++
		}
		if n == 1 || len(allFields[0].index) != len(allFields[1].index) || allFields[0].hasName != allFields[1].hasName {
			flattened = append(flattened, allFields[0])
		}
		allFields = allFields[n:]
	}
	slices.SortFunc(flattened, func(x, y structField) int { return cmp.Compare(x.id, y.id) })
	for i := range flattened {
		flattened[i].id = i
	}
	slices.SortFunc(flattened, func(x, y structField) int { return slices.Compare(x.index, y.index) })

	fs = structFields{
		flattened:    flattened,
		byFoldedName: make(map[string][]*structField, len(flattened)),
	}
	for i, f := range fs.flattened {
		fs.foldable = fs.foldable || f.casing == caseIgnore
		folded := string(foldName([]byte(f.name)))
		fs.byFoldedName[folded] = append(fs.byFoldedName[folded], &fs.flattened[i])
	}
	for i := range fs.flattened {
		f := &fs.flattened[i]
		for len(fs.byLen) <= len(f.name) {
			fs.byLen = append(fs.byLen, nil)
		}
		fs.byLen[len(f.name)] = append(fs.byLen[len(f.name)], f)
	}
	for folded, fields := range fs.byFoldedName {
		if len(fields) > 1 {
			// Conflicting case-insensitive names take breadth-first precedence.
			slices.SortFunc(fields, func(x, y *structField) int { return cmp.Compare(x.id, y.id) })
			fs.byFoldedName[folded] = fields
		}
	}
	return fs, serr
}

// indirectType unwraps one unnamed pointer level, as Go embedding allows.
func indirectType(t reflect.Type) reflect.Type {
	if t.Kind() == reflect.Pointer && t.Name() == "" {
		t = t.Elem()
	}
	return t
}

// matchFoldedName reports whether name matches f case-insensitively under
// the field's `case:` option or MatchCaseInsensitiveNames. It assumes
// foldName(f.name) == foldName(name).
func (f *structField) matchFoldedName(caseInsensitive bool) bool {
	return f.casing == caseIgnore || (caseInsensitive && f.casing != caseStrict)
}

const (
	caseIgnore = 1
	caseStrict = 2
)

type fieldOptions struct {
	name       string
	quotedName string // quoted per RFC 8785, section 3.2.2.2
	hasName    bool
	casing     int8 // 0, caseIgnore or caseStrict
	embed      bool
	omitzero   bool
	omitempty  bool
	string     bool
	format     string
}

// parseFieldOptions parses the `json` tag of a struct field (v2 fields.go).
func parseFieldOptions(sf reflect.StructField) (out fieldOptions, ignored bool, err error) {
	tag, hasTag := sf.Tag.Lookup("json")
	if tag == "-" {
		return fieldOptions{}, true, nil
	}
	if !sf.IsExported() && !sf.Anonymous {
		if hasTag {
			err = cmp.Or(err, fmt.Errorf("unexported Go struct field %s cannot have non-ignored `json:%q` tag", sf.Name, tag))
		}
		return fieldOptions{}, true, err
	}

	out.name = sf.Name
	if len(tag) > 0 && !strings.HasPrefix(tag, ",") {
		n := len(tag) - len(strings.TrimLeftFunc(tag, func(r rune) bool {
			return !strings.ContainsRune(",\\'\"`", r) // reserve comma, backslash, and quotes
		}))
		name := tag[:n]
		var err2 error
		if !strings.HasPrefix(tag[n:], ",") && len(name) != len(tag) {
			name, n, err2 = consumeTagOption(tag)
			if err2 != nil {
				err = cmp.Or(err, fmt.Errorf("Go struct field %s has malformed `json` tag: %v", sf.Name, err2))
			}
		}
		if !utf8.ValidString(name) {
			err = cmp.Or(err, fmt.Errorf("Go struct field %s has JSON object name %q with invalid UTF-8", sf.Name, name))
			name = string([]rune(name))
		}
		if err2 == nil {
			out.hasName = true
			out.name = name
		}
		tag = tag[n:]
	}
	out.quotedName = string(appendQuote(nil, out.name))

	var wasFormat bool
	seenOpts := make(map[string]bool)
	for len(tag) > 0 {
		if tag[0] != ',' {
			err = cmp.Or(err, fmt.Errorf("Go struct field %s has malformed `json` tag: invalid character %q before next option (expecting ',')", sf.Name, tag[0]))
		} else {
			tag = tag[len(","):]
			if len(tag) == 0 {
				err = cmp.Or(err, fmt.Errorf("Go struct field %s has malformed `json` tag: invalid trailing ',' character", sf.Name))
				break
			}
		}
		opt, n, err2 := consumeTagOption(tag)
		if err2 != nil {
			err = cmp.Or(err, fmt.Errorf("Go struct field %s has malformed `json` tag: %v", sf.Name, err2))
		}
		rawOpt := tag[:n]
		tag = tag[n:]
		switch {
		case wasFormat:
			err = cmp.Or(err, fmt.Errorf("Go struct field %s has `format` tag option that was not specified last", sf.Name))
		case strings.HasPrefix(rawOpt, "'") && strings.TrimFunc(opt, isLetterOrDigit) == "":
			err = cmp.Or(err, fmt.Errorf("Go struct field %s has unnecessarily quoted appearance of `%s` tag option; specify `%s` instead", sf.Name, rawOpt, opt))
		}
		switch opt {
		case "case":
			if !strings.HasPrefix(tag, ":") {
				err = cmp.Or(err, fmt.Errorf("Go struct field %s is missing value for `case` tag option; specify `case:ignore` or `case:strict` instead", sf.Name))
				break
			}
			tag = tag[len(":"):]
			opt, n, err2 := consumeTagOption(tag)
			if err2 != nil {
				err = cmp.Or(err, fmt.Errorf("Go struct field %s has malformed value for `case` tag option: %v", sf.Name, err2))
				break
			}
			rawOpt := tag[:n]
			tag = tag[n:]
			if strings.HasPrefix(rawOpt, "'") {
				err = cmp.Or(err, fmt.Errorf("Go struct field %s has unnecessarily quoted appearance of `case:%s` tag option; specify `case:%s` instead", sf.Name, rawOpt, opt))
			}
			switch opt {
			case "ignore":
				out.casing |= caseIgnore
			case "strict":
				out.casing |= caseStrict
			default:
				err = cmp.Or(err, fmt.Errorf("Go struct field %s has unknown `case:%s` tag value", sf.Name, rawOpt))
			}
		case "embed":
			out.embed = true
		case "omitzero":
			out.omitzero = true
		case "omitempty":
			out.omitempty = true
		case "string":
			out.string = true
		case "format":
			if !strings.HasPrefix(tag, ":") {
				err = cmp.Or(err, fmt.Errorf("Go struct field %s is missing value for `format` tag option", sf.Name))
				break
			}
			tag = tag[len(":"):]
			opt, n, err2 := consumeQuotedTagOption(tag)
			if err2 != nil {
				err = cmp.Or(err, fmt.Errorf("Go struct field %s has malformed value for `format` tag option: %v", sf.Name, err2))
				break
			} else if opt == "" {
				err = cmp.Or(err, fmt.Errorf("Go struct field %s cannot have empty value for `format` tag option", sf.Name))
				break
			}
			tag = tag[n:]
			out.format = opt
			wasFormat = true
		default:
			// Reject keys that resemble a supported option ("omitEmpty", "omit_empty").
			switch norm := strings.ReplaceAll(strings.ToLower(opt), "_", ""); norm {
			case "case", "embed", "omitzero", "omitempty", "string", "format":
				err = cmp.Or(err, fmt.Errorf("Go struct field %s has invalid appearance of `%s` tag option; specify `%s` instead", sf.Name, opt, norm))
			}
			// Anything else is ignored, as in v2.
		}
		switch {
		case out.casing == caseIgnore|caseStrict:
			err = cmp.Or(err, fmt.Errorf("Go struct field %s cannot have both `case:ignore` and `case:strict` tag options", sf.Name))
		case seenOpts[opt]:
			err = cmp.Or(err, fmt.Errorf("Go struct field %s has duplicate appearance of `%s` tag option", sf.Name, rawOpt))
		}
		seenOpts[opt] = true
	}
	return out, false, err
}

// consumeTagOption consumes a Go identifier option; an invalid option
// returns everything up to the next comma and an error.
func consumeTagOption(in string) (string, int, error) {
	i := strings.IndexByte(in, ',')
	if i < 0 {
		i = len(in)
	}
	switch r, _ := utf8.DecodeRuneInString(in); {
	case r == '_' || unicode.IsLetter(r):
		n := len(in) - len(strings.TrimLeftFunc(in, isLetterOrDigit))
		return in[:n], n, nil
	case len(in) == 0:
		return in[:i], i, io.ErrUnexpectedEOF
	default:
		return in[:i], i, fmt.Errorf("invalid character %q at start of option (expecting Unicode letter)", r)
	}
}

// consumeQuotedTagOption is consumeTagOption that also accepts a
// single-quoted string, as the `format` option does in v2.
func consumeQuotedTagOption(in string) (string, int, error) {
	i := strings.IndexByte(in, ',')
	if i < 0 {
		i = len(in)
	}
	switch r, _ := utf8.DecodeRuneInString(in); {
	case r == '_' || unicode.IsLetter(r):
		n := len(in) - len(strings.TrimLeftFunc(in, isLetterOrDigit))
		return in[:n], n, nil
	case r == '\'':
		var inEscape bool
		b := []byte{'"'}
		n := len(`'`)
		for len(in) > n {
			r, rn := utf8.DecodeRuneInString(in[n:])
			switch {
			case inEscape:
				if r == '\'' {
					b = b[:len(b)-1] // `\'` => `'`
				}
				inEscape = false
			case r == '\\':
				inEscape = true
			case r == '"':
				b = append(b, '\\') // `"` => `\"`
			case r == '\'':
				b = append(b, '"')
				n += len(`'`)
				out, err := strconv.Unquote(string(b))
				if err != nil {
					return in[:i], i, fmt.Errorf("invalid single-quoted string: %s", in[:n])
				}
				return out, n, nil
			}
			b = append(b, in[n:][:rn]...)
			n += rn
		}
		if n > 10 {
			n = 10
		}
		return in[:i], i, fmt.Errorf("single-quoted string not terminated: %s...", in[:n])
	case len(in) == 0:
		return in[:i], i, io.ErrUnexpectedEOF
	default:
		return in[:i], i, fmt.Errorf("invalid character %q at start of option (expecting Unicode letter or single quote)", r)
	}
}

func isLetterOrDigit(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r)
}

// boolsCompare orders false before true.
func boolsCompare(x, y bool) int {
	switch {
	case !x && y:
		return -1
	case x && !y:
		return +1
	}
	return 0
}

// foldName folds a name for case-insensitive matching that also ignores
// dashes and underscores (v2 fold.go).
func foldName(in []byte) []byte {
	var arr [32]byte
	return appendFoldedName(arr[:0], in)
}

func appendFoldedName(out, in []byte) []byte {
	for i := 0; i < len(in); {
		if c := in[i]; c < utf8.RuneSelf {
			if c != '_' && c != '-' {
				if 'a' <= c && c <= 'z' {
					c -= 'a' - 'A'
				}
				out = append(out, c)
			}
			i++
			continue
		}
		r, n := utf8.DecodeRune(in[i:])
		out = utf8.AppendRune(out, foldRune(r))
		i += n
	}
	return out
}

// foldRune returns the same rune for every rune in a fold set.
func foldRune(r rune) rune {
	for {
		r2 := unicode.SimpleFold(r)
		if r2 <= r {
			return r2
		}
		r = r2
	}
}
