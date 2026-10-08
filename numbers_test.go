package simdjson

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
)

func TestIntegers(t *testing.T) {
	tests := []struct{ in, want string }{
		{"0", "l 0"},
		{"-0", "l 0"},
		{"42", "l 42"},
		{"-1", "l -1"},
		{"9223372036854775807", "l 9223372036854775807"},
		{"-9223372036854775808", "l -9223372036854775808"},
		{"9223372036854775808", "u 9223372036854775808"},
		{"9999999999999999999", "u 9999999999999999999"},
		{"18446744073709551615", "u 18446744073709551615"},
	}
	for _, tt := range tests {
		if got := rootValue(t, tt.in); got != tt.want {
			t.Errorf("%s: %q, want %q", tt.in, got, tt.want)
		}
		var p Parser
		if got := parseTape(t, &p, `{"key": `+tt.in+`}`)[3]; got != tt.want {
			t.Errorf("{key:%s}: %q, want %q", tt.in, got, tt.want)
		}
	}
	for i := -1024; i < 1024; i++ { // C++ basictests small_integers
		if got := rootValue(t, strconv.Itoa(i)); got != "l "+strconv.Itoa(i) {
			t.Fatalf("%d: %q", i, got)
		}
	}
}

func TestBigInt(t *testing.T) {
	var p Parser
	for _, in := range []string{`{"val":123456789012345678901}`, `{"val":18446744073709551616}`} {
		checkErr(t, "default "+in, errOf(p.Parse([]byte(in))), ErrBigInt)
	}
	p.BigIntAsString = true
	for _, digits := range []string{"123456789012345678901", "-12345678901234567890", "18446744073709551616", "99999999999999999999"} {
		if got := parseTape(t, &p, `{"val":`+digits+`}`)[3]; got != "Z "+digits {
			t.Errorf("%s: %q", digits, got)
		}
	}
	if got := parseTape(t, &p, `[1, 123456789012345678901, 3]`)[1]; got != "[ 8 n=3" { // tape r [ l . Z l . ] r: "[" points past "]"
		t.Errorf("array with big int: %q", got)
	}
	for _, in := range []string{`{"val":123456789012345678901x}`, `{"val":-123456789012345678901x}`, "123456789012345678901x"} {
		checkErr(t, in, errOf(p.Parse([]byte(in))), ErrNumber)
	}
}

func TestFloats(t *testing.T) {
	tests := []struct {
		in   string
		want float64
	}{
		// C++ basictests ground_truth
		{"2.2250738585072013e-308", 0x1p-1022},
		{"-92666518056446206563E3", -0x1.39f764644154dp+76},
		{"-42823146028335318693e-128", -0x1.0176daa6cdaafp-360},
		{"90054602635948575728E72", 0x1.61ab4ea9cb6c3p+305},
		{"1.00000000000000188558920870223463870174566020691753515394643550663070558368373221972569761144603605635692374830246134201063722058e-309", 0x0.0b8157268fdafp-1022},
		{"0e9999999999999999999999999999", 0},
		{"-2402844368454405395.2", -0x1.0ac4f1c7422e7p+61},
		// C++ basictests nines
		{"9999999999999999999e0", 9999999999999999999.0},
		{"9999999999999999999.0", 9999999999999999999.0},
		{"999999999999999999.9", 999999999999999999.9},
		{"9.999999999999999999", 9.999999999999999999},
		{"0.09999999999999999999", 0.09999999999999999999},
		// C++ issues 2017 and 2570
		{"0.8825149536132812", 0.8825149536132812},
		{"44.411101", 44.411101},
		{"8.908021", 8.908021},
		// limits
		{"1.7976931348623157e308", math.MaxFloat64},
		{"4.9e-324", 5e-324},
		{"1e-400", 0},
		{"1E+2", 100},
		{"-0.0", math.Copysign(0, -1)},
		{"-1e-400", math.Copysign(0, -1)},
	}
	for _, tt := range tests {
		want := "d " + strconv.FormatFloat(tt.want, 'g', -1, 64)
		if got := rootValue(t, tt.in); got != want {
			t.Errorf("%s: %q, want %q", tt.in, got, want)
		}
		var p Parser
		if got := parseTape(t, &p, "["+tt.in+"]")[2]; got != want {
			t.Errorf("[%s]: %q, want %q", tt.in, got, want)
		}
	}
}

func TestNumberErrors(t *testing.T) {
	for _, in := range []string{"1e400", "-1e400", "1e99999999999999999999", "1.", "1.e5", "1e", "1e+", "01", "-", "-a", "1.5.2", "1ee5", "2x"} {
		var p Parser
		checkErr(t, in, errOf(p.Parse([]byte(in))), ErrNumber)
	}
}

// TestFractionDigits: the fraction is read eight digits at a time, so check
// every length around the 8-byte steps, at the end of the input and before
// an exponent or a terminator, against strconv.
func TestFractionDigits(t *testing.T) {
	var p Parser
	for n := 1; n <= 40; n++ {
		frac := "9876543210987654321098765432109876543210"[:n]
		for _, form := range []string{"1.%s", "1.%se5", "[1.%s]", "[1.%s,2]"} {
			in := fmt.Sprintf(form, frac)
			doc, err := p.Parse([]byte(in))
			if err != nil {
				t.Fatalf("%s: %v", in, err)
			}
			e := doc.Root()
			if a, err := e.Array(); err == nil {
				e, _ = a.At(0)
			}
			got, _ := e.Float64()
			num := strings.Trim(in, "[]")
			num, _, _ = strings.Cut(num, ",")
			if want, _ := strconv.ParseFloat(num, 64); got != want {
				t.Errorf("%s: %v, want %v", in, got, want)
			}
		}
	}
}
