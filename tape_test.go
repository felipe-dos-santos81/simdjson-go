package simdjson

import (
	"math"
	"testing"
)

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

// The tape holds at most len(b)+3 words (the densest documents reach it) and
// stores tape indices in 32 bits, which is what bounds maxSize.
func TestTapeBound(t *testing.T) {
	var p Parser
	for _, s := range []string{`0`, `-1.5`, `[0]`, `[0,0,0]`, `{"":0}`, `[[],{},0,""]`, `"x"`, `true`} {
		doc, err := p.Parse([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		if n := len(doc.tape); n > len(s)+3 {
			t.Errorf("%s: %d tape words, want at most %d", s, n, len(s)+3)
		}
	}
	for _, s := range []string{`0`, `[0,0,0]`} { // the bound is reached
		doc, _ := p.Parse([]byte(s))
		if len(doc.tape) != len(s)+3 {
			t.Errorf("%s: %d tape words, want %d", s, len(doc.tape), len(s)+3)
		}
	}
	if maxSize+3 > math.MaxUint32 {
		t.Errorf("maxSize %#x lets tape indices overflow 32 bits", uint64(maxSize))
	}
}

// Stage 2 writes a container's opening and closing bytes as its tape tags.
func TestBracketsAreTags(t *testing.T) {
	if tagStartArray != '[' || tagEndArray != ']' || tagStartObject != '{' || tagEndObject != '}' {
		t.Fatal("bracket bytes and tape tags differ")
	}
}
