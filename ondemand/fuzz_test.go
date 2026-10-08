package ondemand_test

import (
	"errors"
	"testing"

	"simdjson-go"
	"simdjson-go/ondemand"
)

// fuzzOps are the script steps FuzzOnDemand picks from (see oracle_test.go).
var fuzzOps = []string{
	"get a", "get b", "find a", "find b", "obj", "arr", "val", "at 0", "at 1", "ptr /a", "ptr /0/a",
	"pop", "int64", "uint64", "double", "bool", "null", "string", "type", "ntype", "raw", "count",
	"reset", "rewind", "keys", "each", "walk",
}

// FuzzOnDemand reads arbitrary input two ways: a full walk must agree with
// the DOM, and an arbitrary script of reads (including out-of-order ones)
// must never panic.
func FuzzOnDemand(f *testing.F) {
	for _, s := range []string{
		`{"a":[1,{"a":2}],"b":"x"}`, `[1,2.5,"s",true,null,{}]`, `{"a":1,"a":2}`, `"x"`, `-0`,
		`[1,]`, `{"a":}`, `[[[]]]`, `{"a":{"b":1}`, `18446744073709551616`, `{"a":1}`, `1 2`,
	} {
		f.Add([]byte(s), []byte{0, 12, 4, 26})
	}
	f.Fuzz(func(t *testing.T, in, ops []byte) {
		var od ondemand.Parser
		agree(t, &od, newDOMParser(), in)
		script := make([]string, 0, len(ops))
		for _, b := range ops {
			script = append(script, fuzzOps[int(b)%len(fuzzOps)])
		}
		runScript(&od, in, script) // must not panic
	})
}

// TestMisuse checks that reading out of order or after the next Iterate
// is reported, never a panic or stale data.
func TestMisuse(t *testing.T) {
	var p ondemand.Parser
	ooo := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, simdjson.ErrOutOfOrderIteration) {
			t.Errorf("%s: err = %v, want ErrOutOfOrderIteration", name, err)
		}
	}

	// A container whose turn has passed.
	doc := must(p.Iterate([]byte(`[{"a":1},{"b":2}]`)))
	arr := must(doc.Array())
	var first ondemand.Value
	for v, err := range arr.All() {
		if err != nil {
			t.Fatal(err)
		}
		if first == (ondemand.Value{}) {
			first = v
		}
	}
	_, err := first.Object()
	ooo("object after the loop moved past it", err)

	// Iterating an array a second time.
	for _, err := range arr.All() {
		ooo("second iteration", err)
	}

	// A field kept from an earlier step of the loop.
	doc = must(p.Iterate([]byte(`{"x":{"y":1},"z":2}`)))
	var kept ondemand.Field
	for f, err := range must(doc.Object()).All() {
		if err != nil {
			t.Fatal(err)
		}
		if k, _ := f.Key(); k == "x" {
			kept = f
		}
	}
	_, err = kept.Value().Object()
	ooo("stale field", err)

	// Any handle after the next Iterate.
	doc = must(p.Iterate([]byte(`{"a":[1,2]}`)))
	v := must(doc.Get("a"))
	_ = must(p.Iterate([]byte(`{"a":[1,2]}`)))
	_, err = v.Array()
	ooo("after Iterate", err)
	_, err = v.Int64()
	ooo("scalar after Iterate", err)

	// Zero handles.
	var zero ondemand.Value
	_, err = zero.Int64()
	ooo("zero Value", err)
	_, err = ondemand.Object{}.Get("a")
	ooo("zero Object", err)
	for _, err := range (ondemand.Array{}).All() {
		ooo("zero Array", err)
	}
	if (ondemand.Field{}).RawKey() != nil {
		t.Error("zero Field has a key")
	}

	// A scalar may be read after the cursor has moved past it (C++ allows it).
	doc = must(p.Iterate([]byte(`{"n":42,"s":"x","o":{}}`)))
	n := must(doc.Get("n"))
	_ = must(must(doc.Get("o")).Object())
	if got, err := n.Int64(); got != 42 || err != nil {
		t.Errorf("saved scalar = %d, %v; want 42", got, err)
	}
}
