package simdjson

import (
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"testing"
)

func TestMarshalIndentMatchesV2(t *testing.T) {
	values := []any{
		map[string]any{"b": []any{1, "x", map[string]any{}}, "a": []any{}, "c": map[string]any{"z": true}},
		[]string{"q\"[{,:}]"}, 1, "s", nil, Outer{A: 1},
	}
	for _, ind := range [][2]string{{" ", "\t"}, {"", "  "}, {"", ""}, {"\t ", " \t"}} {
		for _, v := range values {
			want, errWant := jsonv2.Marshal(v, jsonv2.Deterministic(true), jsontext.WithIndentPrefix(ind[0]), jsontext.WithIndent(ind[1]))
			got, err := MarshalIndent(v, ind[0], ind[1], Deterministic(true))
			if errClass(err) != errClass(errWant) || string(got) != string(want) {
				t.Errorf("MarshalIndent(%#v, %q, %q) = %q, %v; v2 %q, %v", v, ind[0], ind[1], got, err, want, errWant)
			}
		}
	}
	// v2 panics on such options; MarshalIndent returns an error instead.
	for _, ind := range [][2]string{{"x", " "}, {"", "-"}, {"\n", ""}} {
		if got, err := MarshalIndent(1, ind[0], ind[1]); err == nil {
			t.Errorf("MarshalIndent(1, %q, %q) = %q, want an error", ind[0], ind[1], got)
		}
	}
}
