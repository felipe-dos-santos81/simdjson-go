package stage1

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestPrefixXor(t *testing.T) {
	if got := prefixXor(1<<2 | 1<<5); got != 0b11100 {
		t.Fatalf("prefixXor = %b", got)
	}
}

func TestIndex(t *testing.T) {
	tests := []struct {
		in   string
		want []uint32
	}{
		{`1`, []uint32{0}},
		{` true `, []uint32{1}},
		{`{"a":1}`, []uint32{0, 1, 4, 5, 6}},
		{`["x\"y",-1.5e3]`, []uint32{0, 1, 7, 8, 14}},
		{`[ "\\" , null ]`, []uint32{0, 2, 7, 9, 14}},
		{`"a"true`, []uint32{0, 3}}, // a scalar right after a string still starts a token
		// A string crossing the 64-byte block boundary, then an escaped backslash at the boundary.
		{`["` + strings.Repeat("x", 61) + `\\",2]`, []uint32{0, 1, 66, 67, 68}},
	}
	for _, tt := range tests {
		got, err := Index([]byte(tt.in), nil)
		if err != nil || !slices.Equal(got, tt.want) {
			t.Errorf("Index(%q) = %v, %v; want %v", tt.in, got, err, tt.want)
		}
	}
}

func TestIndexErrors(t *testing.T) {
	tests := []struct {
		in   string
		want error
	}{
		{"", ErrEmpty},
		{" \t\r\n ", ErrEmpty},
		{`"abc`, ErrUnclosedString},
		{`["a\"]`, ErrUnclosedString},
		{"\"a\x01\"", ErrUnescapedChars},
		{"[\"\x1f\"]", ErrUnescapedChars},
		{"\"\xff\"", ErrUTF8},
		{"[\"\xc3\"]", ErrUTF8},              // truncated 2-byte sequence
		{"\"\xed\xa0\x80\"", ErrUTF8},        // encoded surrogate
		{"\"\xc0\xaf\"", ErrUTF8},            // overlong
		{"\"abc\xe2\x82", ErrUnclosedString}, // unclosed wins over UTF-8
	}
	for _, tt := range tests {
		if _, err := Index([]byte(tt.in), nil); !errors.Is(err, tt.want) {
			t.Errorf("Index(%q) error = %v, want %v", tt.in, err, tt.want)
		}
	}
}

func TestMinify(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{" { \"a b\" : [ 1 , 2 ] } \n", `{"a b":[1,2]}`},
		{`"  \"  "  x  `, `"  \"  "x`},
		{strings.Repeat(" ", 70) + "[ 1 ]", "[1]"},
		{`[ "` + strings.Repeat(" ", 100) + `" ]`, `["` + strings.Repeat(" ", 100) + `"]`},
	}
	for _, tt := range tests {
		got, err := Minify(nil, []byte(tt.in))
		if err != nil || string(got) != tt.want {
			t.Errorf("Minify(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
	if _, err := Minify(nil, []byte(`["abc`)); !errors.Is(err, ErrUnclosedString) {
		t.Errorf("Minify unclosed: err = %v", err)
	}
}

func TestIndexBadUTF8(t *testing.T) {
	// Invalid sequences from C++ tests/unicode_tests.cpp, with and without a
	// long ASCII prefix so that they also start mid-block.
	bad := []string{
		"\xc3\x28", "\xa0\xa1", "\xe2\x28\xa1", "\xe2\x82\x28", "\xf0\x28\x8c\xbc", "\xf0\x90\x28\xbc",
		"\xf0\x28\x8c\x28", "\xc0\x9f", "\xf5\xff\xff\xff", "\xed\xa0\x81", "\xf8\x90\x80\x80\x80",
		"123456789012345\xed", "123456789012345\xf1", "123456789012345\xc2", "\xC2\x7F", "\xce",
		"\xce\xba\xe1", "\xce\xba\xe1\xbd", "\xce\xba\xe1\xbd\xb9\xcf", "\xce\xba\xe1\xbd\xb9\xcf\x83\xce",
		"\xce\xba\xe1\xbd\xb9\xcf\x83\xce\xbc\xce", "\xdf", "\xef\xbf", "\x80", "\x91\x85\x95\x9e", "\x6c\x02\x8e\x18",
	}
	for _, s := range bad {
		for _, in := range []string{s, strings.Repeat("a", 62) + s} {
			if _, err := Index([]byte(in), nil); !errors.Is(err, ErrUTF8) {
				t.Errorf("Index(%q) error = %v, want ErrUTF8", in, err)
			}
		}
	}
}
