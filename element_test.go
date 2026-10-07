package simdjson

import (
	"errors"
	"math"
	"strings"
	"testing"
)

// mustParse parses s with a fresh Parser and returns the root, failing the test on error.
func mustParse(t *testing.T, s string) Element {
	t.Helper()
	var p Parser
	doc, err := p.Parse([]byte(s))
	if err != nil {
		t.Fatalf("Parse(%.80q): %v", s, err)
	}
	return doc.Root()
}

// A negative index must be rejected without walking the tape: this array's
// tape is deliberately truncated, so a walk would panic.
func TestArrayAtNegativeDoesNotWalk(t *testing.T) {
	doc := &Document{tape: []uint64{'r' << 56, '['<<56 | 1000}}
	_, err := Array{Element{doc, 1}}.At(-1)
	checkErr(t, "At(-1)", err, ErrIndexOutOfBounds)
}

func mustAt(t *testing.T, a Array, i int) Element {
	t.Helper()
	e, err := a.At(i)
	if err != nil {
		t.Fatalf("At(%d): %v", i, err)
	}
	return e
}

// checkErr fails unless err matches want (nil means success).
func checkErr(t *testing.T, name string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) || (want == nil) != (err == nil) {
		t.Errorf("%s: err = %v, want %v", name, err, want)
	}
}

func errOf[T any](_ T, err error) error { return err }

// field returns e[key] for an object e, failing the test otherwise.
func field(t *testing.T, e Element, key string) Element {
	t.Helper()
	o, err := e.Object()
	if err != nil {
		t.Fatal(err)
	}
	v, err := o.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestGetters(t *testing.T) {
	a, _ := mustParse(t, `[-1, 1, 18446744073709551615, 1.5, "s", true, false, null, [], {}]`).Array()
	el := func(i int) Element { return mustAt(t, a, i) }

	types := []Type{TypeInt64, TypeInt64, TypeUint64, TypeFloat64, TypeString, TypeBool, TypeBool, TypeNull, TypeArray, TypeObject}
	for i, typ := range types {
		if got := el(i).Type(); got != typ {
			t.Errorf("element %d: type %v, want %v", i, got, typ)
		}
	}
	// Numeric conversions follow C++ element-inl.h.
	i, err := el(0).Int64()
	checkErr(t, "Int64(-1)", err, nil)
	checkErr(t, "Uint64(-1)", errOf(el(0).Uint64()), ErrNumberOutOfRange)
	u, err := el(1).Uint64()
	checkErr(t, "Uint64(1)", err, nil)
	checkErr(t, "Int64(MaxUint64)", errOf(el(2).Int64()), ErrNumberOutOfRange)
	f0, err := el(0).Float64()
	checkErr(t, "Float64(-1)", err, nil)
	f2, err := el(2).Float64()
	checkErr(t, "Float64(MaxUint64)", err, nil)
	checkErr(t, "Int64(1.5)", errOf(el(3).Int64()), ErrIncorrectType)
	checkErr(t, "Uint64(1.5)", errOf(el(3).Uint64()), ErrIncorrectType)
	if i != -1 || u != 1 || f0 != -1 || f2 != math.MaxUint64 {
		t.Errorf("values: %d %d %v %v", i, u, f0, f2)
	}
	if s, err := el(4).StringValue(); err != nil || s != "s" {
		t.Errorf("StringValue = %q, %v", s, err)
	}
	if b, err := el(4).StringBytes(); err != nil || string(b) != "s" {
		t.Errorf("StringBytes = %q, %v", b, err)
	}
	if b, err := el(5).Bool(); err != nil || !b {
		t.Errorf("Bool(true) = %v, %v", b, err)
	}
	if b, err := el(6).Bool(); err != nil || b {
		t.Errorf("Bool(false) = %v, %v", b, err)
	}
	if !el(7).IsNull() || el(6).IsNull() {
		t.Error("IsNull")
	}
	checkErr(t, "Bool(string)", errOf(el(4).Bool()), ErrIncorrectType)
	checkErr(t, "StringValue(true)", errOf(el(5).StringValue()), ErrIncorrectType)
	checkErr(t, "Array(string)", errOf(el(4).Array()), ErrIncorrectType)
	checkErr(t, "Object(array)", errOf(el(8).Object()), ErrIncorrectType)
	checkErr(t, "BigInt(string)", errOf(el(4).BigInt()), ErrIncorrectType)
}

func TestIntegerGetters(t *testing.T) { // C++ tests/dom/integer_tests.cpp
	tests := []struct {
		in           string
		i64          int64
		i64Err       error
		u64          uint64
		u64Err       error
		wantUnsigned bool
	}{
		{"9223372036854775807", math.MaxInt64, nil, math.MaxInt64, nil, false},
		{"-9223372036854775808", math.MinInt64, nil, 0, ErrNumberOutOfRange, false},
		{"9223372036854775808", 0, ErrNumberOutOfRange, 1 << 63, nil, true},
		{"18446744073709551615", 0, ErrNumberOutOfRange, math.MaxUint64, nil, true},
		{"0", 0, nil, 0, nil, false},
	}
	for _, tt := range tests {
		v := field(t, mustParse(t, `{"key": `+tt.in+`}`), "key")
		i, err := v.Int64()
		checkErr(t, tt.in+" Int64", err, tt.i64Err)
		u, uerr := v.Uint64()
		checkErr(t, tt.in+" Uint64", uerr, tt.u64Err)
		if i != tt.i64 || u != tt.u64 || (v.Type() == TypeUint64) != tt.wantUnsigned {
			t.Errorf("%s: %d %d %v", tt.in, i, u, v.Type())
		}
	}
}

func TestBigIntGetters(t *testing.T) { // C++ big_integer_tests type_checks
	p := Parser{BigIntAsString: true}
	doc, err := p.Parse([]byte(`{"val":123456789012345678901}`))
	if err != nil {
		t.Fatal(err)
	}
	v := field(t, doc.Root(), "val")
	if v.Type() != TypeBigInt {
		t.Fatalf("type %v", v.Type())
	}
	if d, err := v.BigInt(); err != nil || d != "123456789012345678901" {
		t.Errorf("BigInt() = %q, %v", d, err)
	}
	for name, err := range map[string]error{
		"Int64": errOf(v.Int64()), "Uint64": errOf(v.Uint64()), "Float64": errOf(v.Float64()),
		"StringValue": errOf(v.StringValue()), "Bool": errOf(v.Bool()),
	} {
		checkErr(t, name, err, ErrIncorrectType)
	}
}

func TestArray(t *testing.T) {
	a, _ := mustParse(t, `[1,[2,3],{"a":[4]},"x",5.5]`).Array()
	if a.Len() != 5 {
		t.Errorf("Len = %d", a.Len())
	}
	var types []Type
	for i, v := range a.All() {
		if i == 4 {
			break // stopping early must end the iteration
		}
		types = append(types, v.Type())
	}
	if len(types) != 4 || types[1] != TypeArray || types[2] != TypeObject || types[3] != TypeString {
		t.Errorf("All types = %v", types)
	}
	if v := mustAt(t, a, 4); v.Type() != TypeFloat64 {
		t.Errorf("At(4) type %v", v.Type())
	}
	for _, i := range []int{-1, 5} {
		checkErr(t, "At out of range", errOf(a.At(i)), ErrIndexOutOfBounds)
	}
	empty, _ := mustParse(t, `[]`).Array()
	if empty.Len() != 0 {
		t.Error("empty Len")
	}
	for range empty.All() {
		t.Error("empty array yielded an element")
	}
}

func TestObject(t *testing.T) {
	o, _ := mustParse(t, `{"1":1,"2":{"x":[]},"3":1,"1":"dup","k\u00e9y":true}`).Object()
	if o.Len() != 5 {
		t.Errorf("Len = %d", o.Len())
	}
	var keys []string
	for k := range o.All() {
		keys = append(keys, k)
	}
	if strings.Join(keys, ",") != "1,2,3,1,k\u00e9y" {
		t.Errorf("keys = %q", keys)
	}
	if v, err := o.Get("1"); err != nil || v.Type() != TypeInt64 { // the first duplicate wins, as in C++
		t.Errorf("Get(1) = %v, %v", v.Type(), err)
	}
	if v, err := o.Get("k\u00e9y"); err != nil || v.Type() != TypeBool { // keys compare unescaped
		t.Errorf("Get(k\u00e9y) = %v, %v", v.Type(), err)
	}
	checkErr(t, "Get(nope)", errOf(o.Get("nope")), ErrNoSuchField)
}

func TestZeroElement(t *testing.T) {
	// The zero values returned next to an error must not panic when used.
	o, _ := mustParse(t, `{}`).Object()
	v, err := o.Get("missing")
	checkErr(t, "Get", err, ErrNoSuchField)
	if v.Type() != 0 || v.IsNull() {
		t.Errorf("zero Element: type %v, IsNull %v", v.Type(), v.IsNull())
	}
	checkErr(t, "Int64", errOf(v.Int64()), ErrIncorrectType)
	checkErr(t, "StringValue", errOf(v.StringValue()), ErrIncorrectType)
	var a Array
	if a.Len() != 0 {
		t.Error("zero Array Len")
	}
	for range a.All() {
		t.Error("zero Array yielded an element")
	}
	var obj Object
	checkErr(t, "zero Object Get", errOf(obj.Get("x")), ErrNoSuchField)
}

func TestCountSaturation(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates ~34 MB")
	}
	const n = 0xFFFFFF + 2 // more elements than the 24-bit tape count holds
	a, _ := mustParse(t, "["+strings.Repeat("0,", n-1)+"0]").Array()
	if a.Len() != n {
		t.Errorf("array Len = %d, want %d", a.Len(), n)
	}
	o, _ := mustParse(t, "{"+strings.Repeat(`"":0,`, n-1)+`"":0}`).Object()
	if o.Len() != n {
		t.Errorf("object Len = %d, want %d", o.Len(), n)
	}
}
