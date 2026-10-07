package simdjson

import (
	"strconv"
	"testing"
)

// Fixtures and cases from C++ tests/dom/pointercheck.cpp.
const pointerJSON = `{"/~01abc":[0,{"\\\" 0":["value0","value1"]}],"0":"0 ok","01":"01 ok","":"empty ok","arr":[]}`

const pointerRFCJSON = `{"foo":["bar","baz"],"":0,"a/b":1,"c%d":2,"e^f":3,"g|h":4,"i\\j":5,"k\"l":6," ":7,"m~n":8}`

func TestAtPointer(t *testing.T) {
	tests := []struct {
		doc, ptr, want string
		err            error
	}{
		{pointerRFCJSON, "", pointerRFCJSON, nil},
		{pointerRFCJSON, "/foo", `["bar","baz"]`, nil},
		{pointerRFCJSON, "/foo/0", `"bar"`, nil},
		{pointerRFCJSON, "/", "0", nil},
		{pointerRFCJSON, "/a~1b", "1", nil},
		{pointerRFCJSON, "/c%d", "2", nil},
		{pointerRFCJSON, "/e^f", "3", nil},
		{pointerRFCJSON, "/g|h", "4", nil},
		{pointerRFCJSON, `/i\j`, "5", nil},
		{pointerRFCJSON, `/k"l`, "6", nil},
		{pointerRFCJSON, "/ ", "7", nil},
		{pointerRFCJSON, "/m~0n", "8", nil},
		{pointerJSON, "/~1~001abc", `[0,{"\\\" 0":["value0","value1"]}]`, nil},
		{pointerJSON, "/~1~001abc/1", `{"\\\" 0":["value0","value1"]}`, nil},
		{pointerJSON, `/~1~001abc/1/\" 0`, `["value0","value1"]`, nil},
		{pointerJSON, `/~1~001abc/1/\" 0/0`, `"value0"`, nil},
		{pointerJSON, `/~1~001abc/1/\" 0/1`, `"value1"`, nil},
		{pointerJSON, `/~1~001abc/1/\" 0/2`, "", ErrIndexOutOfBounds},
		{pointerJSON, "/arr", "[]", nil},
		{pointerJSON, "/arr/0", "", ErrIndexOutOfBounds},
		{pointerJSON, "~1~001abc", "", ErrInvalidJSONPointer},
		{pointerJSON, "/0", `"0 ok"`, nil},
		{pointerJSON, "/01", `"01 ok"`, nil},
		{pointerJSON, "/~01abc", "", ErrNoSuchField},
		{pointerJSON, "/~1~001abc/01", "", ErrInvalidJSONPointer},
		{pointerJSON, "/~1~001abc/", "", ErrInvalidJSONPointer},
		{pointerJSON, "/~1~001abc/18446744073709551616", "", ErrIndexOutOfBounds},
		{pointerJSON, "/~1~001abc/-", "", ErrIndexOutOfBounds},
		{`{"key":"value","array":[0,1,2]}`, "/array/not_a_num", "", ErrIncorrectType},
		{`{"key":"value","array":[0,1,2]}`, "/array/9", "", ErrIndexOutOfBounds},
		{`{"key":"value","array":[0,1,2]}`, "/no_such_key", "", ErrNoSuchField},
		// issue 2154: descending into a scalar
		{`{"obj":{"s":"42","n":42,"f":4.2}}`, "/obj/X/42", "", ErrNoSuchField},
		{`{"obj":{"s":"42","n":42,"f":4.2}}`, "/obj/s/42", "", ErrNoSuchField},
		{`{"obj":{"s":"42","n":42,"f":4.2}}`, "/obj/f/4~", "", ErrInvalidJSONPointer},
		{`{"obj":{"s":"42","n":42,"f":4.2}}`, "/obj/f/~", "", ErrInvalidJSONPointer},
		{`{"obj":{"s":"42","n":42,"f":4.2}}`, "/obj/f/~1", "", ErrNoSuchField},
		{`"just a string"`, "", `"just a string"`, nil},
	}
	for _, tt := range tests {
		e, err := mustParse(t, tt.doc).AtPointer(tt.ptr)
		if !checkErr(t, strconv.Quote(tt.ptr), err, tt.err) {
			continue
		}
		if err == nil {
			if got := string(e.AppendJSON(nil)); got != tt.want {
				t.Errorf("%q = %s, want %s", tt.ptr, got, tt.want)
			}
		}
	}
	// Pointers compose: at_pointer("/array").at_pointer("/0") (C++ modern_support).
	arr, _ := mustParse(t, `{"key":"value","array":[0,1,2]}`).AtPointer("/array")
	if v, err := arr.AtPointer("/0"); err != nil || string(v.AppendJSON(nil)) != "0" {
		t.Errorf("/array then /0 = %v", err)
	}
}
