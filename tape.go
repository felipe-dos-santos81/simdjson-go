package simdjson

// Type is the type of a JSON value. Its values are the tape tags of C++
// simdjson (doc/tape.md); false is stored as 'f' but reported as TypeBool.
type Type byte

// The values of Type, as returned by Element.Type.
const (
	TypeArray   Type = tagStartArray
	TypeObject  Type = tagStartObject
	TypeInt64   Type = tagInt64
	TypeUint64  Type = tagUint64
	TypeFloat64 Type = tagDouble
	TypeString  Type = tagString
	TypeBool    Type = tagTrue
	TypeNull    Type = tagNull
	TypeBigInt  Type = tagBigInt
)

// String returns the C++ element_type name.
func (t Type) String() string {
	switch t {
	case TypeArray:
		return "array"
	case TypeObject:
		return "object"
	case TypeInt64:
		return "int64_t"
	case TypeUint64:
		return "uint64_t"
	case TypeFloat64:
		return "double"
	case TypeString:
		return "string"
	case TypeBool:
		return "bool"
	case TypeNull:
		return "null"
	case TypeBigInt:
		return "bigint"
	}
	return "unknown"
}

// Tape tags (C++ doc/tape.md).
const (
	tagRoot        = 'r'
	tagStartArray  = '['
	tagEndArray    = ']'
	tagStartObject = '{'
	tagEndObject   = '}'
	tagString      = '"'
	tagInt64       = 'l'
	tagUint64      = 'u'
	tagDouble      = 'd'
	tagTrue        = 't'
	tagFalse       = 'f'
	tagNull        = 'n'
	tagBigInt      = 'Z'
)

// word builds a tape word: an 8-bit tag above a 56-bit payload.
func word(tag byte, payload uint64) uint64 { return uint64(tag)<<56 | payload }
