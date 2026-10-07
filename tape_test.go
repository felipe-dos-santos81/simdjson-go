package simdjson

import "testing"

func TestTypeString(t *testing.T) {
	want := map[Type]string{
		TypeArray: "array", TypeObject: "object", TypeInt64: "int64_t", TypeUint64: "uint64_t",
		TypeFloat64: "double", TypeString: "string", TypeBool: "bool", TypeNull: "null", TypeBigInt: "bigint",
		Type(0): "unknown",
	}
	for typ, name := range want {
		if typ.String() != name {
			t.Errorf("%q: %q, want %q", byte(typ), typ.String(), name)
		}
	}
}
