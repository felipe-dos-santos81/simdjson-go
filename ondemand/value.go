package ondemand

import (
	"strconv"

	"simdjson-go/internal/jsonerr"
	"simdjson-go/internal/number"
)

// Type is the JSON type of a value (C++ json_type).
type Type byte

const (
	TypeUnknown Type = iota // not the start of a JSON value
	TypeArray
	TypeObject
	TypeNumber
	TypeString
	TypeBool
	TypeNull
)

func (t Type) String() string {
	if t > TypeNull {
		return "Type(" + strconv.Itoa(int(t)) + ")"
	}
	return [...]string{"unknown", "array", "object", "number", "string", "bool", "null"}[t]
}

// NumberType is the kind of a JSON number (C++ number_type).
type NumberType byte

const (
	Int64   NumberType = iota + 1 // fits int64
	Uint64                        // above math.MaxInt64, fits uint64
	Float64                       // has a fraction or an exponent
	BigInt                        // an integer outside 64 bits (read it with Raw)
)

func (t NumberType) String() string {
	if t > BigInt {
		return "NumberType(" + strconv.Itoa(int(t)) + ")"
	}
	return [...]string{"", "int64", "uint64", "float64", "bigint"}[t]
}

// Value is a value inside a document, read at most once: getting it as an
// object or array moves the document's cursor through it. A Value holding a
// scalar may also be read after the cursor has moved past it.
type Value struct{ handle }

// handle is what Value, Object and Array hold: a position in the document
// and the document's generation when it was made.
type handle struct {
	it  valueIter
	gen uint32
}

func newHandle(it valueIter) handle { return handle{it, it.d.gen} }

func newValue(it valueIter) Value { return Value{newHandle(it)} }

// check rejects a zero handle and one from before the document's last Iterate.
func (h handle) check() error {
	if h.it.d == nil || h.gen != h.it.d.gen {
		return jsonerr.ErrOutOfOrderIteration
	}
	return nil
}

func (v Value) scalar() []byte { return v.it.d.buf[v.it.scalarStart():] }

// Type returns the value's JSON type, judged by its first character.
func (v Value) Type() (Type, error) {
	if err := v.check(); err != nil {
		return 0, err
	}
	return v.it.typ(), nil
}

// NumberType classifies a number from its digits, without converting it.
func (v Value) NumberType() (NumberType, error) {
	if err := v.check(); err != nil {
		return 0, err
	}
	return numberType(v.scalar(), 0)
}

// Int64 returns an integer that fits int64. Fractions, exponents and
// integers outside int64 are ErrIncorrectType.
func (v Value) Int64() (int64, error) {
	if err := v.check(); err != nil {
		return 0, err
	}
	n, err := parseInteger(v.scalar(), 0)
	if err == nil {
		v.it.advanceScalar()
	}
	return n, err
}

// Uint64 returns a non-negative integer that fits uint64.
func (v Value) Uint64() (uint64, error) {
	if err := v.check(); err != nil {
		return 0, err
	}
	n, err := parseUnsigned(v.scalar(), 0)
	if err == nil {
		v.it.advanceScalar()
	}
	return n, err
}

// Float64 returns any number as the nearest float64.
func (v Value) Float64() (float64, error) {
	if err := v.check(); err != nil {
		return 0, err
	}
	f, err := parseDouble(v.scalar(), 0)
	if err == nil {
		v.it.advanceScalar()
	}
	return f, err
}

// Bool returns true or false.
func (v Value) Bool() (bool, error) {
	if err := v.check(); err != nil {
		return false, err
	}
	b, err := parseBool(v.scalar(), 0)
	if err == nil {
		v.it.advanceScalar()
	}
	return b, err
}

// IsNull reports whether the value is null (moving past it if so).
func (v Value) IsNull() (bool, error) {
	if err := v.check(); err != nil {
		return false, err
	}
	s := v.scalar()
	isNull := hasPrefix4(s, "null", 0) && number.IsStructuralOrSpace[at(s, 4, 0)]
	if !isNull && at(s, 0, 0) == 'n' {
		return false, jsonerr.ErrIncorrectType
	}
	if isNull {
		v.it.advanceScalar()
	}
	return isNull, nil
}

// StringBytes returns the unescaped string. It is valid until the next
// Iterate or Rewind.
func (v Value) StringBytes() ([]byte, error) {
	if err := v.check(); err != nil {
		return nil, err
	}
	if v.it.d.peekAt(v.it.start) != '"' {
		return nil, jsonerr.ErrIncorrectType
	}
	v.it.advanceScalar()
	return v.it.d.unescape(v.it.scalarStart())
}

// String returns the unescaped string as a copy.
func (v Value) String() (string, error) {
	if err := v.check(); err != nil {
		return "", err
	}
	mark := len(v.it.d.strs)
	b, err := v.StringBytes()
	return v.it.d.copyString(mark, b, err)
}

// Raw returns the value's JSON text. For a scalar it runs to the next
// structural character and the cursor does not move (C++ raw_json_token);
// for an array or object it is the whole container, which Raw consumes (C++
// raw_json). It is valid until the next Iterate.
func (v Value) Raw() ([]byte, error) {
	if err := v.check(); err != nil {
		return nil, err
	}
	switch v.it.typ() {
	case TypeArray:
		a, err := v.Array()
		if err != nil {
			return nil, err
		}
		return a.Raw()
	case TypeObject:
		o, err := v.Object()
		if err != nil {
			return nil, err
		}
		return o.Raw()
	}
	off := v.it.scalarStart()
	return v.it.d.buf[off : off+v.it.d.tokenLen(v.it.start)], nil
}

// Object returns the value as an object, ready to be read.
func (v Value) Object() (Object, error) {
	if err := v.check(); err != nil {
		return Object{}, err
	}
	if _, err := v.it.startObject(); err != nil {
		return Object{}, err
	}
	return Object{v.handle}, nil
}

// Array returns the value as an array, ready to be read.
func (v Value) Array() (Array, error) {
	if err := v.check(); err != nil {
		return Array{}, err
	}
	if _, err := v.it.startArray(); err != nil {
		return Array{}, err
	}
	return Array{v.handle}, nil
}

// object starts the object, or resumes it if it was started (C++
// start_or_resume_object). Resuming a value that is not an object is
// ErrIncorrectType (Go: C++ resumes without checking and then misreads).
func (v Value) object() (Object, error) {
	if err := v.check(); err != nil {
		return Object{}, err
	}
	if v.it.isAtStart() {
		if _, err := v.it.startObject(); err != nil {
			return Object{}, err
		}
	} else if v.it.peekStart() != '{' {
		return Object{}, jsonerr.ErrIncorrectType
	}
	return Object{v.handle}, nil
}

// Get is Object.Get on the value.
func (v Value) Get(name string) (Value, error) {
	o, err := v.object()
	if err != nil {
		return Value{}, err
	}
	return o.find(name, false)
}

// FindNext is Object.FindNext on the value.
func (v Value) FindNext(name string) (Value, error) {
	o, err := v.object()
	if err != nil {
		return Value{}, err
	}
	return o.find(name, true)
}

// AtPointer returns the value at the RFC 6901 JSON Pointer ptr below v,
// looking fields up in order (Object.FindNext). As in C++, an empty pointer
// is ErrInvalidJSONPointer (Document.AtPointer("") is the root).
func (v Value) AtPointer(ptr string) (Value, error) {
	if err := v.check(); err != nil {
		return Value{}, err
	}
	return atPointer(v, ptr)
}

// atPointer is C++ value::at_pointer: it follows ptr one segment at a time,
// in a loop where C++ recurses, so a deep pointer costs no stack.
func atPointer(v Value, ptr string) (Value, error) {
	for {
		var err error
		switch v.it.typ() {
		case TypeArray:
			var a Array
			if a, err = v.Array(); err != nil {
				return Value{}, err
			}
			v, ptr, err = a.step(ptr)
		case TypeObject:
			var o Object
			if o, err = v.Object(); err != nil {
				return Value{}, err
			}
			v, ptr, err = o.step(ptr)
		default:
			if pointerWellFormed(ptr) {
				return Value{}, jsonerr.ErrNoSuchField
			}
			return Value{}, jsonerr.ErrInvalidJSONPointer
		}
		if err != nil || ptr == "" {
			return v, err
		}
	}
}

// pointerWellFormed is C++ is_pointer_well_formed.
func pointerWellFormed(p string) bool {
	if p == "" || p[0] != '/' {
		return false
	}
	for i := 0; i < len(p); i++ {
		if p[i] == '~' {
			return i+1 < len(p) && (p[i+1] == '0' || p[i+1] == '1')
		}
	}
	return true
}

func hasPrefix4(s []byte, lit string, pad byte) bool {
	for i := range 4 {
		if at(s, i, pad) != lit[i] {
			return false
		}
	}
	return true
}

// parseBool is C++ parse_bool.
func parseBool(s []byte, pad byte) (bool, error) {
	notTrue := !hasPrefix4(s, "true", pad)
	notFalse := !hasPrefix4(s, "fals", pad) || at(s, 4, pad) != 'e'
	end := 4
	if notTrue {
		end = 5
	}
	if notTrue && notFalse || !number.IsStructuralOrSpace[at(s, end, pad)] {
		return false, jsonerr.ErrIncorrectType
	}
	return !notTrue, nil
}
