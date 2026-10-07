package simdjson

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestAppendJSON(t *testing.T) {
	tests := []struct{ in, want string }{
		{` [ 1 , -2 , 18446744073709551615 ] `, `[1,-2,18446744073709551615]`},
		{`{"a" : "x\"y\\z\/\u0001\u001f\b\f\n\r\t" }`, `{"a":"x\"y\\z/\u0001\u001f\b\f\n\r\t"}`},
		{`[1.0, 0.1, -0.0, 1e21, 1e-7, 1E+2, 2.5]`, `[1.0,0.1,-0.0,1e+21,1e-07,100.0,2.5]`},
		{`[true,false,null,{},[]]`, `[true,false,null,{},[]]`},
		{`"\u00e9\ud83d\ude00"`, `"é😀"`},
		// C++ document_tests stable_test: minified input round-trips exactly.
		{`{"Image":{"Width":800,"Height":600,"Title":"View from 15th Floor","Thumbnail":{"Url":"http://www.example.com/image/481989943","Height":125,"Width":100},"Animated":false,"IDs":[116,943.3,234,38793]}}`,
			`{"Image":{"Width":800,"Height":600,"Title":"View from 15th Floor","Thumbnail":{"Url":"http://www.example.com/image/481989943","Height":125,"Width":100},"Animated":false,"IDs":[116,943.3,234,38793]}}`},
	}
	for _, tt := range tests {
		if got := string(mustParse(t, tt.in).AppendJSON(nil)); got != tt.want {
			t.Errorf("AppendJSON(%s)\n got %s\nwant %s", tt.in, got, tt.want)
		}
		// Round trip: the output parses to the same output (types included).
		if again := string(mustParse(t, tt.want).AppendJSON(nil)); again != tt.want {
			t.Errorf("round trip of %s gave %s", tt.want, again)
		}
	}
}

func TestMarshalJSON(t *testing.T) {
	e := field(t, mustParse(t, `{"a":[1,2]}`), "a")
	out, err := json.Marshal(map[string]any{"v": e})
	if err != nil || string(out) != `{"v":[1,2]}` {
		t.Errorf("json.Marshal = %s, %v", out, err)
	}
}

func TestMinify(t *testing.T) {
	got, err := Minify([]byte("x:"), []byte(" { \"a b\" : [ 1 , 2 ] } \n"))
	if err != nil || string(got) != `x:{"a b":[1,2]}` {
		t.Errorf("Minify = %q, %v", got, err)
	}
	if _, err := Minify(nil, []byte(`["abc`)); !errors.Is(err, ErrUnclosedString) {
		t.Errorf("Minify unclosed: err = %v", err)
	}
}
