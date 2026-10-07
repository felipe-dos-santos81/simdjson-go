package simdjson

// Type is the type of a JSON value. Its values are the tape tags of C++
// simdjson (doc/tape.md); false is stored as 'f' but reported as TypeBool.
type Type byte

const (
	TypeArray   Type = '['
	TypeObject  Type = '{'
	TypeInt64   Type = 'l'
	TypeUint64  Type = 'u'
	TypeFloat64 Type = 'd'
	TypeString  Type = '"'
	TypeBool    Type = 't'
	TypeNull    Type = 'n'
	TypeBigInt  Type = 'Z'
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

// Tape tags that are not value types.
const (
	tagRoot      = 'r'
	tagEndArray  = ']'
	tagEndObject = '}'
	tagFalse     = 'f'
)

// word builds a tape word: an 8-bit tag above a 56-bit payload.
func word(tag byte, payload uint64) uint64 { return uint64(tag)<<56 | payload }
