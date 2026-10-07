package simdjson

import (
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"
)

// errClass is what Unmarshal and Marshal must agree with v2 on: whether an
// error occurred, its type, and the exported sentinel it wraps.
func errClass(err error) string {
	var sy *jsontext.SyntacticError
	var se *jsonv2.SemanticError
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, jsontext.ErrDuplicateName):
		return "syntactic:duplicate"
	case errors.As(err, &sy):
		return "syntactic"
	case errors.Is(err, jsonv2.ErrUnknownName):
		return "semantic:unknown"
	case errors.As(err, &se):
		return "semantic"
	}
	return fmt.Sprintf("other(%T)", err)
}

// v2Options maps our options to v2's.
func v2Options(opts []Option) []jsonv2.Options {
	o := makeOptions(opts)
	return []jsonv2.Options{
		jsonv2.MatchCaseInsensitiveNames(o.caseInsensitive),
		jsonv2.FormatNilSliceAsNull(o.nilSliceAsNull),
		jsonv2.FormatNilMapAsNull(o.nilMapAsNull),
		jsonv2.Deterministic(o.deterministic),
		jsonv2.RejectUnknownMembers(o.rejectUnknown),
	}
}

// sameUnmarshal reports how Unmarshal differs from v2's for input in and a
// fresh value of type t, or "".
func sameUnmarshal(in []byte, t reflect.Type, opts ...Option) string {
	want := reflect.New(t)
	errWant := jsonv2.Unmarshal(in, want.Interface(), v2Options(opts)...)
	got := reflect.New(t)
	errGot := Unmarshal(in, got.Interface(), opts...)
	cw, cg := errClass(errWant), errClass(errGot)
	// Documented deviation: invalid input is always a SyntacticError here,
	// while v2 may first report a semantic error it meets earlier.
	if cg == "syntactic" && errWant != nil && !jsontext.Value(in).IsValid() {
		return ""
	}
	if cw != cg {
		return fmt.Sprintf("%v: error %s (%v), v2 %s (%v)", t, cg, errGot, cw, errWant)
	}
	if errWant == nil && !reflect.DeepEqual(got.Elem().Interface(), want.Elem().Interface()) {
		return fmt.Sprintf("%v: got %#v, v2 %#v", t, got.Elem().Interface(), want.Elem().Interface())
	}
	return ""
}

// sameMarshal reports how Marshal differs from v2's for v, or "".
func sameMarshal(v any, opts ...Option) string {
	want, errWant := jsonv2.Marshal(v, v2Options(opts)...)
	got, errGot := Marshal(v, opts...)
	if cw, cg := errClass(errWant), errClass(errGot); cw != cg {
		return fmt.Sprintf("%T: error %s (%v), v2 %s (%v)", v, cg, errGot, cw, errWant)
	}
	if errWant == nil && string(got) != string(want) {
		return fmt.Sprintf("%T: got %s, v2 %s", v, got, want)
	}
	return ""
}

type text string

func (t *text) UnmarshalText(b []byte) error {
	if string(b) == "bad" {
		return errors.New("bad text")
	}
	*t = text("T:" + string(b))
	return nil
}
func (t text) MarshalText() ([]byte, error) { return []byte("T:" + string(t)), nil }

type rawJSON struct{ Got string }

func (r *rawJSON) UnmarshalJSON(b []byte) error { r.Got = string(b); return nil }
func (r rawJSON) MarshalJSON() ([]byte, error)  { return []byte(r.Got), nil }

type Inner struct {
	X int `json:"x"`
	Y string
}

type Outer struct {
	A     int               `json:"a"`
	B     string            `json:"b,omitempty"`
	C     []int             `json:"c,omitzero"`
	D     *Inner            `json:"d"`
	E     map[string]Inner  `json:"e"`
	F     any               `json:"f"`
	G     float64           `json:"g,string"`
	H     int64             `json:"h,string"`
	I     time.Time         `json:"i,omitzero"`
	J     bool              `json:"j,case:ignore"`
	K     text              `json:"k"`
	L     jsonv1.RawMessage `json:"l"`
	Inner                   // promoted: x, Y
	skip  int
}

type Conflict struct {
	A  int `json:"a"`
	A2 int `json:"A"`
}

var diffTypes = []reflect.Type{
	reflect.TypeFor[bool](), reflect.TypeFor[string](),
	reflect.TypeFor[int8](), reflect.TypeFor[int](), reflect.TypeFor[int64](),
	reflect.TypeFor[uint8](), reflect.TypeFor[uint](), reflect.TypeFor[uint64](),
	reflect.TypeFor[float32](), reflect.TypeFor[float64](),
	reflect.TypeFor[[]byte](), reflect.TypeFor[[4]byte](),
	reflect.TypeFor[[]int](), reflect.TypeFor[[2]int](), reflect.TypeFor[[]any](),
	reflect.TypeFor[map[string]int](), reflect.TypeFor[map[int]string](), reflect.TypeFor[map[uint8]any](),
	reflect.TypeFor[map[float64]int](), reflect.TypeFor[map[bool]int](), reflect.TypeFor[map[string]any](),
	reflect.TypeFor[map[text]int](),
	reflect.TypeFor[*int](), reflect.TypeFor[**string](), reflect.TypeFor[any](), reflect.TypeFor[fmt.Stringer](),
	reflect.TypeFor[time.Time](), reflect.TypeFor[time.Duration](), reflect.TypeFor[netip.Addr](),
	reflect.TypeFor[text](), reflect.TypeFor[rawJSON](), reflect.TypeFor[jsonv1.RawMessage](),
	reflect.TypeFor[Inner](), reflect.TypeFor[Outer](), reflect.TypeFor[*Outer](), reflect.TypeFor[Conflict](),
	reflect.TypeFor[[]Outer](), reflect.TypeFor[chan int](), reflect.TypeFor[complex128](),
}

var diffInputs = []string{
	`null`, `true`, `false`, `0`, `-0`, `1`, `-1`, `127`, `128`, `255`, `256`, `-128`, `-129`,
	`1.5`, `1e2`, `1E400`, `-1e400`, `1e-400`, `0.1`, `3.4028235e38`, `3.5e38`, `16777217`,
	`9223372036854775807`, `9223372036854775808`, `-9223372036854775808`, `-9223372036854775809`,
	`18446744073709551615`, `18446744073709551616`, `123456789012345678901234567890`, `-123456789012345678901234567890`,
	`""`, `"x"`, `"1"`, `"-1"`, `"-0"`, `"01"`, `"1.5"`, `"1e2"`, `" 1"`, `"null"`, `"true"`,
	`"AQID"`, `"AQIDBA=="`, `"AQIDBA"`, `"AQ\nID"`, `"bad"`, `"é"`, `"1.2.3.4"`, `"::1"`,
	`"2026-10-07T12:00:00Z"`, `"2026-10-07T12:00:00.5+02:00"`, `"2026-10-07T12:00:00+25:00"`, `"2026-10-07T1:00:00Z"`,
	`[]`, `[1]`, `[1,2]`, `[1,2,3]`, `[[1],[2]]`, `[null]`, `[{"q":1,"q":2}]`,
	`{}`, `{"a":1}`, `{"A":1}`, `{"a":1,"A":2}`, `{"a":1,"a":2}`, `{"zz":1,"zz":2}`, `{"zz":{"x":1,"x":2}}`,
	`{"1":"a","2":"b"}`, `{"1":"a","01":"b"}`, `{"0":"a","-0":"b"}`, `{"1.5":1,"1.50":2}`, `{"true":1}`,
	`{"x":1,"Y":"y"}`, `{"x":1,"x":2}`, `{"y":"a","Y":"b"}`, `{"j":true}`, `{"J":true}`, `{"_J-":true}`,
	`{"g":"1.5","h":"-7"}`, `{"g":1.5}`, `{"h":"07"}`, `{"h":"1e2"}`, `{"g":"-0"}`,
	`{"d":{"x":3},"e":{"k":{"x":4}},"f":[1,"s",{"t":null}]}`, `{"d":null,"c":[]}`,
	`{"k":"hi","l":{ "raw" : [1,  2] }}`, `{"l":{"a":1,"a":2}}`, `{"i":"2026-10-07T12:00:00Z"}`,
	`{"b":"s","b":"t"}`, `{"a":"wrong"}`, `{"a":1, "unknown":{"deep":[1,{"k":1,"k":2}]}}`,
	` {"b" : [1, 2] , "a":3 } `, `{"\u0061":5}`, `{"a":1,"\u0061":2}`, `{"x\"y":1,"x\u0022y":2}`,
	`{`, `[1,]`, `"\ud800"`, "\xEF\xBB\xBF{}", `{} x`, `[1 2]`, `tru`, `"\x01"`, `01`, `1.`, `{"a" 1}`,
}

func TestUnmarshalMatchesV2(t *testing.T) {
	optSets := [][]Option{nil, {MatchCaseInsensitiveNames(true)}, {RejectUnknownMembers(true)}}
	for _, opts := range optSets {
		for _, typ := range diffTypes {
			for _, in := range diffInputs {
				if d := sameUnmarshal([]byte(in), typ, opts...); d != "" {
					t.Errorf("Unmarshal(%q) opts=%d: %s", in, len(opts), d)
				}
			}
		}
	}
}

func TestDeepAndWide(t *testing.T) {
	for _, in := range []string{
		strings.Repeat("[", 10000) + strings.Repeat("]", 10000),
		strings.Repeat("[", 10001) + strings.Repeat("]", 10001),
		strings.Repeat("[", 10000) + "1" + strings.Repeat("]", 10000),
		strings.Repeat(`{"a":`, 9999) + "{}" + strings.Repeat("}", 9999),
		wideObject(100, ""), wideObject(100, `,"k3":0`), wideObject(1000, `,"k999":0`),
	} {
		if d := sameUnmarshal([]byte(in), reflect.TypeFor[any]()); d != "" {
			t.Errorf("depth %d: %s", len(in), d[:min(len(d), 300)])
		}
	}
}

// wideObject is an object with n names k0, k1, …, followed by extra.
func wideObject(n int, extra string) string {
	var b strings.Builder
	b.WriteString("{")
	for i := range n {
		fmt.Fprintf(&b, `"k%d":%d,`, i, i)
	}
	b.WriteString(`"end":0` + extra + "}")
	return b.String()
}

// TestUnmarshalIntoExisting decodes into values that already hold data:
// slices with spare capacity, maps with entries, set pointers and fields.
func TestUnmarshalIntoExisting(t *testing.T) {
	fill := func() []any {
		n := 7
		return []any{
			&[]int{9, 9, 9, 9}, ptr(make([]int, 1, 8)), &map[string]int{"a": 1, "z": 26},
			&map[string]Inner{"k": {X: 1, Y: "keep"}}, ptr(&n), &Outer{A: 5, B: "keep", D: &Inner{Y: "keep"}},
			ptr(any(map[string]any{"old": true})), ptr(any(&Inner{Y: "keep"})), &[2]int{7, 7},
		}
	}
	for _, in := range []string{`[1,2]`, `[]`, `null`, `{"a":2,"b":3}`, `{"k":{"x":2}}`, `3`, `{"x":1}`, `{"d":{"x":2}}`, `[1]`} {
		got, want := fill(), fill()
		for i := range got {
			errGot := Unmarshal([]byte(in), got[i])
			errWant := jsonv2.Unmarshal([]byte(in), want[i])
			if errClass(errGot) != errClass(errWant) || !reflect.DeepEqual(got[i], want[i]) {
				t.Errorf("Unmarshal(%s) into %T: got %v %+v, v2 %v %+v", in, got[i], errGot, got[i], errWant, want[i])
			}
		}
	}
}

func ptr[T any](v T) *T { return &v }

type node struct {
	Name string          `json:"name"`
	Kids []node          `json:"kids,omitempty"`
	Next *node           `json:"next,omitempty"`
	Tags map[string]node `json:"tags,omitempty"`
}

// TestRecursiveTypes builds codecs for a type that refers to itself.
func TestRecursiveTypes(t *testing.T) {
	in := `{"name":"a","kids":[{"name":"b","next":{"name":"c"}}],"tags":{"t":{"name":"d","kids":[]}}}`
	if d := sameUnmarshal([]byte(in), reflect.TypeFor[node]()); d != "" {
		t.Error(d)
	}
	var n node
	if err := Unmarshal([]byte(in), &n); err != nil {
		t.Fatal(err)
	}
	if d := sameMarshal(n, Deterministic(true)); d != "" {
		t.Error(d)
	}
}

// TestUnmarshalReusesParsers runs Unmarshal calls back to back, so pooled parsers are
// reused: state from one document must not leak into the next.
func TestUnmarshalReusesParsers(t *testing.T) {
	for range 3 {
		for _, in := range []string{`{"a":1,"a":2}`, `{"a":1}`, wideObject(50, `,"k1":0`), wideObject(50, ""), `[1e400]`, `[1]`} {
			if d := sameUnmarshal([]byte(in), reflect.TypeFor[any]()); d != "" {
				t.Errorf("%.40s: %s", in, d)
			}
		}
	}
}

// TestManyObjectsWithDuplicates decodes an object with many members that each
// repeat a name inside: finding the outer object's own duplicate must stay
// linear.
func TestManyObjectsWithDuplicates(t *testing.T) {
	var b strings.Builder
	b.WriteString("{")
	for i := range 100000 {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"k%d":{"a":1,"a":2}`, i)
	}
	b.WriteString("}")
	in := []byte(b.String())
	start := time.Now()
	var v any
	err := Unmarshal(in, &v)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Unmarshal of %d bytes took %v", len(in), elapsed)
	}
	if errClass(err) != "syntactic:duplicate" {
		t.Errorf("err = %v, want a duplicate-name error", err)
	}
	if d := sameUnmarshal([]byte(`[{"a":1},{"b":{"c":1,"c":2}}]`), reflect.TypeFor[[]map[string]any]()); d != "" {
		t.Error(d)
	}
}

func TestMarshalMatchesV2(t *testing.T) {
	ts := time.Date(2026, 10, 7, 12, 0, 0, 500, time.FixedZone("x", 2*3600))
	in := Inner{X: 1, Y: "y"}
	values := []any{
		nil, true, false, 0, -1, int8(-128), uint64(18446744073709551615), 1.0, 100.0, 0.1, 1e20, 1e21, 1e-6, 1e-7,
		float32(0.1), float32(1e21), 5e-324, -0.0, 123456789.125,
		"", "x", "q\"b\\s/ <>&   \x01 \t é😀", "bad \xff",
		[]byte(nil), []byte{}, []byte{1, 2, 3}, [4]byte{1}, []int(nil), []int{}, []int{1, 2}, [2]int{3, 4},
		map[string]int(nil), map[string]int{}, map[string]int{"b": 2, "a": 1, "é": 3}, map[int]string{2: "b", -1: "a", 10: "c"},
		map[float64]int{1.5: 1, 2: 2}, map[bool]int{true: 1}, map[text]int{"x": 1, "y": 2},
		map[string]any{"z": []any{1, "s", nil, map[string]any{}}, "a": nil},
		&in, in, Outer{A: 1, Inner: in}, Outer{B: "s", C: []int{}, G: 1.5, H: -7, I: ts, K: "k", L: jsonv1.RawMessage(`{ "r" : [1,  2.50] }`)},
		[]Outer{{}}, Conflict{A: 1, A2: 2}, ts, time.Time{}, time.Second, netip.MustParseAddr("::1"),
		text("t"), rawJSON{`{"a" : 1}`}, rawJSON{`{"a":1,"a":2}`}, rawJSON{`[1, 2.50, 1e2]`}, rawJSON{`bad`},
		make(chan int), complex(1, 2), math.Inf(1), new(any), struct{}{},
		struct {
			F float64 `json:",omitempty"`
			P *int    `json:",omitempty"`
			S []int   `json:",omitempty"`
			M rawJSON `json:",omitempty"`
		}{M: rawJSON{`""`}},
	}
	for _, opts := range [][]Option{{Deterministic(true)}, {Deterministic(true), FormatNilSliceAsNull(true), FormatNilMapAsNull(true)}} {
		for _, v := range values {
			if d := sameMarshal(v, opts...); d != "" {
				t.Errorf("Marshal(%#v): %s", v, d)
			}
		}
	}
}
