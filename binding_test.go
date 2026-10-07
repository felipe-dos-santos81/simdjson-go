package simdjson

import (
	"errors"
	"math"
	"strings"
	"testing"
)

// TestBindingMode checks what stage 2 records for Unmarshal: input offsets,
// the first repeated name of each object, ±Inf for overflowing floats, and
// empty containers counted toward the depth limit.
func TestBindingMode(t *testing.T) {
	in := `{"a":1,"b":{"c":1,"c":2,"c":3},"a":-3e400,"d":[],"e":{}}`
	p := Parser{MaxDepth: 10, binding: true}
	doc, err := p.Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, i := range doc.dups {
		k := Element{doc, int(i)}
		names = append(names, string(k.rawString()))
		if off := int(doc.offs[i]); in[off] != '"' || !strings.HasPrefix(in[off+1:], names[len(names)-1]) {
			t.Errorf("offs[%d] = %d, not at name %q", i, off, names[len(names)-1])
		}
	}
	if got := strings.Join(names, ","); got != "c,a" {
		t.Errorf("dups name %s, want c,a (inner object first)", got)
	}
	inner := Element{doc, int(doc.dups[0])}
	if off := int(doc.offs[inner.i]); off != strings.Index(in, `"c":2`) {
		t.Errorf("first repeated c at offset %d, want %d", off, strings.Index(in, `"c":2`))
	}
	last := Element{doc, int(doc.dups[1]) + 1}
	if f, err := last.Float64(); err != nil || !math.IsInf(f, -1) {
		t.Errorf("-3e400 = %v, %v; want -Inf", f, err)
	}
	if off := int(doc.offs[last.i]); in[off:off+7] != "-3e400," {
		t.Errorf("offs of -3e400 = %d", off)
	}

	// The same input outside binding mode: an error, and nothing recorded.
	plain := Parser{MaxDepth: 10}
	if _, err := plain.Parse([]byte(in)); !errors.Is(err, ErrNumber) {
		t.Errorf("plain Parse = %v, want ErrNumber", err)
	}
	if doc, err := plain.Parse([]byte(`{"a":1,"a":2}`)); err != nil || doc.dups != nil || doc.offs != nil {
		t.Errorf("plain Parse recorded dups %v, offs %v (err %v)", doc.dups, doc.offs, err)
	}

	// Empty containers count toward the depth only in binding mode.
	for _, tc := range []struct {
		binding bool
		in      string
		want    error
	}{
		{false, `[[]]`, nil}, {true, `[[]]`, ErrDepth}, {true, `[]`, nil}, {true, `[{}]`, ErrDepth},
	} {
		p := Parser{MaxDepth: 2, binding: tc.binding}
		if _, err := p.Parse([]byte(tc.in)); !errors.Is(err, tc.want) {
			t.Errorf("binding=%v Parse(%s) = %v, want %v", tc.binding, tc.in, err, tc.want)
		}
	}
}
