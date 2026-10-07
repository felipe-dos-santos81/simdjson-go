package simdjson

// Ports of C++ simdjson tests/dom/unpadded_tests.cpp, and of the basictests.cpp
// and document_tests.cpp cases not covered by the other test files. Expected
// errors that the C++ tests leave implicit come from C++ simdjson v5.0.2.

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// unpaddedTrailers are placed after the document, inside the slice's capacity.
// Reading any of them would close a string, extend a number or an atom, close
// a container or complete an escape, and so change the result.
var unpaddedTrailers = []string{
	strings.Repeat(`"]}`, 30),
	strings.Repeat("9", 90),
	`e` + strings.Repeat(`\u0041`, 15),
}

type parseOutcome struct{ json, err string }

func outcome(p *Parser, b []byte) (parseOutcome, *Document) {
	doc, err := p.Parse(b)
	if err != nil {
		return parseOutcome{err: err.Error()}, nil
	}
	return parseOutcome{json: string(doc.Root().AppendJSON(nil))}, doc
}

// checkUnpadded is the Go form of C++ check_matches, which requires
// parse (padded input) and parse_unpadded (an exact-size buffer) to agree.
// Go has a single Parse that takes unpadded input, so it parses in from an
// exact-capacity slice and from slices whose capacity continues with each of
// unpaddedTrailers; all must agree. It also checks the error against wantErr
// (nil for success) and returns the root of the exact-capacity parse, which
// stays valid until p's next Parse.
func checkUnpadded(t *testing.T, p *Parser, in string, wantErr error) (Element, bool) {
	t.Helper()
	exact := []byte(in)
	exact = exact[:len(in):len(in)]
	want, _ := outcome(p, exact)
	for _, tr := range unpaddedTrailers {
		buf := []byte(in + tr)
		if got, _ := outcome(p, buf[:len(in)]); got != want {
			t.Errorf("%.120q: exact-size %+v, with trailing %.6q %+v", in, want, tr, got)
		}
	}
	got, doc := outcome(p, exact)
	if wantErr == nil && got.err != "" || wantErr != nil && got.err != wantErr.Error() {
		t.Errorf("%.120q: err = %q, want %v", in, got.err, wantErr)
		return Element{}, false
	}
	if doc == nil {
		return Element{}, false
	}
	return doc.Root(), true
}

// checkUnpaddedJSON is checkUnpadded for a valid document whose minified
// output (C++ operator<<) is wantJSON.
func checkUnpaddedJSON(t *testing.T, p *Parser, in, wantJSON string) {
	t.Helper()
	if e, ok := checkUnpadded(t, p, in, nil); ok {
		if got := string(e.AppendJSON(nil)); got != wantJSON {
			t.Errorf("%.120q: AppendJSON = %.120q, want %.120q", in, got, wantJSON)
		}
	}
}

// C++ tests/dom/unpadded_tests.cpp: numbers
func TestUnpaddedNumbers(t *testing.T) {
	tests := []struct {
		// want is the C++ output, except that floats are printed as spec §6
		// says (strconv 'g'): C++ prints 1e10 as 10000000000.0. "" means
		// BIGINT_ERROR.
		in, want string
	}{
		{"0", "0"}, {"-0", "0"}, {"1", "1"}, {"-1", "-1"}, {"123", "123"}, {"-123", "-123"},
		{"3.14", "3.14"}, {"-3.14", "-3.14"}, {"1e10", "1e+10"}, {"1E10", "1e+10"},
		{"1e-10", "1e-10"}, {"1.5e+3", "1500.0"}, {"0.0", "0.0"}, {"0.1", "0.1"},
		{"3.141592653589793", "3.141592653589793"},
		{"123456789012345678", "123456789012345678"},
		{"9223372036854775807", "9223372036854775807"},
		{"-9223372036854775808", "-9223372036854775808"},
		{"18446744073709551615", "18446744073709551615"},
		{"1.7976931348623157e308", "1.7976931348623157e+308"},
		{"100000000000000000000", ""},
	}
	var p Parser
	for _, tt := range tests {
		for _, wrap := range [][2]string{{"", ""}, {"[", "]"}, {`{"k":`, "}"}, {"[", ",1]"}, {"[1,", "]"}} {
			in := wrap[0] + tt.in + wrap[1]
			if tt.want == "" {
				checkUnpadded(t, &p, in, ErrBigInt)
			} else {
				checkUnpaddedJSON(t, &p, in, wrap[0]+tt.want+wrap[1])
			}
		}
	}
}

// C++ tests/dom/unpadded_tests.cpp: number_at_end_sweep
func TestUnpaddedNumberAtEndSweep(t *testing.T) {
	var p Parser
	for frac := 1; frac <= 3*64+5; frac++ { // 3*SIMDJSON_PADDING+5
		num := "0." + strings.Repeat("9", frac)
		want, _ := strconv.ParseFloat(num, 64)
		root, ok := checkUnpadded(t, &p, num, nil)
		if !ok {
			continue
		}
		if got, err := root.Float64(); err != nil || got != want {
			t.Errorf("%s = %v, %v; want %v", num, got, err, want)
		}
		js := string(root.AppendJSON(nil))
		checkUnpaddedJSON(t, &p, "["+num+"]", "["+js+"]")
		checkUnpaddedJSON(t, &p, `{"k":`+num+"}", `{"k":`+js+"}")
		checkUnpaddedJSON(t, &p, "[123456789,"+num+"]", "[123456789,"+js+"]")
		bignum := strings.Repeat("7", frac)
		if frac <= 19 {
			checkUnpaddedJSON(t, &p, "[1,"+bignum+"]", "[1,"+bignum+"]")
		} else {
			checkUnpadded(t, &p, "[1,"+bignum+"]", ErrBigInt)
		}
	}
}

// C++ tests/dom/unpadded_tests.cpp: malformed_atoms_at_end
func TestUnpaddedMalformedAtomsAtEnd(t *testing.T) {
	atomErr := map[byte]error{'n': ErrNAtom, 't': ErrTAtom, 'f': ErrFAtom}
	var p Parser
	for _, tok := range []string{
		"n", "nu", "nul", "nulx", "nall", "t", "tr", "tru", "trux", "ture",
		"f", "fa", "fal", "fals", "falx", "fale", "true", "false", "null",
	} {
		var want error
		if tok != "true" && tok != "false" && tok != "null" {
			want = atomErr[tok[0]]
		}
		for _, in := range []string{tok, "[" + tok + "]", `{"k":` + tok + "}", "[" + tok + ",1]", "[" + strings.Repeat(" ", 64) + tok + "]"} {
			checkUnpadded(t, &p, in, want)
		}
	}
}

// xorshift64, the C++ rng of unpadded_tests.cpp.
type xorshift struct{ s uint64 }

func (r *xorshift) next() uint64 {
	r.s ^= r.s << 13
	r.s ^= r.s >> 7
	r.s ^= r.s << 17
	return r.s
}

func (r *xorshift) below(n uint32) uint32 { return uint32(r.next() % uint64(n)) }

func (r *xorshift) genString(b *strings.Builder) {
	b.WriteByte('"')
	for n, i := r.below(24), uint32(0); i < n; i++ {
		switch r.below(9) {
		case 0:
			b.WriteString(`\n`)
		case 1:
			b.WriteString(`\t`)
		case 2:
			b.WriteString(`\"`)
		case 3:
			b.WriteString(`\\`)
		case 4:
			b.WriteString(`\/`)
		case 5:
			b.WriteString(`\u00e9`)
		case 6:
			b.WriteString("\xC3\xA9")
		case 7:
			b.WriteString("\xF0\x9F\x98\x80")
		default:
			b.WriteByte(byte('a' + r.below(26)))
		}
	}
	b.WriteByte('"')
}

func (r *xorshift) genNumber(b *strings.Builder) {
	switch r.below(6) {
	case 0:
		fmt.Fprint(b, r.below(1000000))
	case 1:
		fmt.Fprint(b, "-", r.below(1000000))
	case 2:
		fmt.Fprint(b, r.below(1000))
		fmt.Fprint(b, ".", r.below(1000))
	case 3:
		fmt.Fprint(b, r.below(100))
		fmt.Fprint(b, "e", int(r.below(20))-10)
	case 4:
		b.WriteString("0")
	default:
		fmt.Fprint(b, r.next())
	}
}

func (r *xorshift) genValue(b *strings.Builder, depth int) {
	var choice uint32
	if depth <= 0 {
		choice = 3 + r.below(4)
	} else {
		choice = r.below(7)
	}
	switch choice {
	case 0:
		b.WriteByte('{')
		for n, i := r.below(5), uint32(0); i < n; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			r.genString(b)
			b.WriteByte(':')
			r.genValue(b, depth-1)
		}
		b.WriteByte('}')
	case 1:
		b.WriteByte('[')
		for n, i := r.below(6), uint32(0); i < n; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			r.genValue(b, depth-1)
		}
		b.WriteByte(']')
	case 2:
		r.genString(b)
	case 3:
		r.genNumber(b)
	case 4:
		b.WriteString("true")
	case 5:
		b.WriteString("false")
	default:
		b.WriteString("null")
	}
}

// C++ tests/dom/unpadded_tests.cpp: random_documents
func TestUnpaddedRandomDocuments(t *testing.T) {
	var p Parser
	for seed := uint64(1); seed <= 5000; seed++ {
		r := xorshift{seed * 0x100000001b3}
		var b strings.Builder
		r.genValue(&b, 4)
		if e, ok := checkUnpadded(t, &p, b.String(), nil); ok {
			if d := sameAsStdlib(e, []byte(b.String())); d != "" {
				t.Errorf("seed %d %q: differs from encoding/json: %s", seed, b.String(), d)
			}
		}
	}
}

// C++ tests/dom/unpadded_tests.cpp: string_at_end_sweep
func TestUnpaddedStringAtEndSweep(t *testing.T) {
	var p Parser
	for n := 0; n <= 3*64+5; n++ {
		obj := `{"k":"` + strings.Repeat("a", n) + `"}`
		checkUnpaddedJSON(t, &p, obj, obj)
		root := `"` + strings.Repeat("b", n) + `"`
		checkUnpaddedJSON(t, &p, root, root)
	}
}

// C++ tests/dom/unpadded_tests.cpp: escapes_at_end
func TestUnpaddedEscapesAtEnd(t *testing.T) {
	tests := []struct {
		tail, out string // out is the tail as C++ prints it; "" means STRING_ERROR
	}{
		{`\n`, `\n`}, {`\t`, `\t`}, {`\"`, `\"`}, {`\\`, `\\`}, {`\/`, `/`},
		{`\b`, `\b`}, {`\f`, `\f`}, {`\r`, `\r`},
		{"\xC3\xA9", "\xC3\xA9"}, {"\xF0\x9F\x98\x80", "\xF0\x9F\x98\x80"},
		// A lone high surrogate: the C++ test calls it valid, but C++ v5.0.2
		// (padded and unpadded alike) rejects it with STRING_ERROR.
		{`\uD800`, ""},
	}
	var p Parser
	for _, tt := range tests {
		for pad := 0; pad <= 2*64; pad++ {
			x, y := strings.Repeat("x", pad), strings.Repeat("y", pad)
			obj, root := `{"k":"`+x+tt.tail+`"}`, `"`+y+tt.tail+`"`
			if tt.out == "" {
				checkUnpadded(t, &p, obj, ErrString)
				checkUnpadded(t, &p, root, ErrString)
				continue
			}
			checkUnpaddedJSON(t, &p, obj, `{"k":"`+x+tt.out+`"}`)
			checkUnpaddedJSON(t, &p, root, `"`+y+tt.out+`"`)
		}
	}
}

// C++ tests/dom/unpadded_tests.cpp: surrogate_pair_deep_lookahead_at_chunk_boundary
func TestUnpaddedSurrogatePairDeepLookahead(t *testing.T) {
	var p Parser
	// A high surrogate followed by a truncated \u, the backslash at offset 63.
	checkUnpadded(t, &p, `"`+strings.Repeat("a", 63)+`\uD800\u"`, ErrString)
	checkUnpadded(t, &p, `{"k":"`+strings.Repeat("b", 63)+`\uD800\u"}`, ErrString)
}

// C++ tests/dom/unpadded_tests.cpp: assorted_documents
func TestUnpaddedAssortedDocuments(t *testing.T) {
	tests := []struct{ in, want string }{
		{"{}", "{}"}, {"[]", "[]"}, {"true", "true"}, {"false", "false"}, {"null", "null"},
		{"0", "0"}, {"-1", "-1"}, {"123", "123"}, {"3.14", "3.14"}, {"1e10", "1e+10"}, // C++ prints 10000000000.0
		{`""`, `""`}, {`"a"`, `"a"`}, {`"hello world"`, `"hello world"`}, {"1", "1"},
		{"[1]", "[1]"}, {"[1,2,3]", "[1,2,3]"},
		{`{"a":1,"b":2,"c":[true,false,null],"d":{"x":"y"}}`, `{"a":1,"b":2,"c":[true,false,null],"d":{"x":"y"}}`},
		{`[{"k":"v"},{"k2":"v2"},123,4.5,"end"]`, `[{"k":"v"},{"k2":"v2"},123,4.5,"end"]`},
		{"{\"unicode\":\"\xC3\xA9\xC3\xA8\xC3\xAA\",\"emoji\":\"\xF0\x9F\x98\x80\"}", "{\"unicode\":\"\xC3\xA9\xC3\xA8\xC3\xAA\",\"emoji\":\"\xF0\x9F\x98\x80\"}"},
		{`  {  "spaced" :  "value"  }  `, `{"spaced":"value"}`},
		{`{"nested":{"deep":{"deeper":{"value":42}}}}`, `{"nested":{"deep":{"deeper":{"value":42}}}}`},
		{"\"\xEF\xBB\xBF\"", "\"\xEF\xBB\xBF\""}, // a string holding a BOM, not a leading BOM
		{"\xEF\xBB\xBF[1,2,3]", "[1,2,3]"},       // leading BOM
	}
	var p Parser
	for _, tt := range tests {
		checkUnpaddedJSON(t, &p, tt.in, tt.want)
	}
}

// C++ tests/dom/unpadded_tests.cpp: real_files
func TestUnpaddedRealFiles(t *testing.T) {
	dir := testdataDir(t, "jsonexamples")
	var p Parser
	for _, name := range []string{
		"twitter.json", "twitter_timeline.json", "canada.json", "mesh.json", "apache_builds.json",
		"gsoc-2018.json", "repeat.json", "small/demo.json", "small/smalldemo.json",
		"small/adversarial.json", "small/flatadversarial.json", "small/truenull.json",
	} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		checkUnpadded(t, &p, string(data), nil)
	}
}

// C++ tests/dom/unpadded_tests.cpp: api_overloads. The string_view, pointer and
// parse_into_document overloads have no Go equivalent; only the lookups and
// the parser reuse are ported.
func TestUnpaddedAPI(t *testing.T) {
	const in = `{"hello":"world","n":42}`
	var p Parser
	for range 2 {
		e, ok := checkUnpadded(t, &p, in, nil)
		if !ok {
			return
		}
		if n, err := field(t, e, "n").Int64(); err != nil || n != 42 {
			t.Errorf("n = %d, %v", n, err)
		}
		if s, err := field(t, e, "hello").StringValue(); err != nil || s != "world" {
			t.Errorf("hello = %q, %v", s, err)
		}
	}
}

// C++ tests/dom/unpadded_tests.cpp: degenerate_inputs, truncated_nested_containers
// (issue 2815) and truncated_utf8_at_end.
func TestUnpaddedTruncated(t *testing.T) {
	tests := []struct {
		in   string
		want error
	}{
		// degenerate_inputs
		{"", ErrEmpty}, {" ", ErrEmpty}, {"   ", ErrEmpty}, {"\t\n", ErrEmpty},
		{"{", ErrTape}, {"[", ErrTape}, {`"`, ErrUnclosedString}, {`"abc`, ErrUnclosedString},
		{"[1,", ErrTape}, {`{"a":`, ErrTape}, {"tru", ErrTAtom}, {"nul", ErrNAtom},
		{"fals", ErrFAtom}, {"12.", ErrNumber}, {"-", ErrNumber},
		{`["unterminated`, ErrUnclosedString}, {"\x00", ErrTape},
		// truncated_nested_containers
		{"[[]", ErrTape}, {`{"a":{}`, ErrTape}, {`{"a":{ }`, ErrTape},
	}
	// truncated_utf8_at_end: a multi-byte sequence cut off by the end of input.
	for _, tail := range []string{"\xc3", "\xe0", "\xe0\xa0", "\xf0", "\xf0\x90", "\xf0\x90\x80"} {
		for _, in := range []string{`["a` + tail, `"a` + tail, `{"a":"b` + tail} {
			tests = append(tests, struct {
				in   string
				want error
			}{in, ErrUnclosedString})
		}
	}
	var p Parser
	for _, tt := range tests {
		checkUnpadded(t, &p, tt.in, tt.want)
	}
}

// Not ported from unpadded_tests.cpp: nan_inf_at_end needs the
// SIMDJSON_ENABLE_NAN_INF build option, which this port does not have.

// C++ tests/dom/basictests.cpp: issue2213
func TestIssue2213(t *testing.T) {
	for range 15 {
		mustParse(t, `[1,2,3,"4", {"a": 5}]`)
	}
}

// parseFloatDoc parses in and returns it as a float64 (C++ get<double>).
func parseFloatDoc(t *testing.T, p *Parser, in string) (float64, bool) {
	t.Helper()
	doc, err := p.Parse([]byte(in))
	if err != nil {
		t.Errorf("Parse(%.80q): %v", in, err)
		return 0, false
	}
	f, err := doc.Root().Float64()
	if err != nil {
		t.Errorf("%.80q: Float64: %v", in, err)
		return 0, false
	}
	return f, true
}

// C++ tests/dom/basictests.cpp: powers_of_two
func TestPowersOfTwo(t *testing.T) {
	var p Parser
	for i := -1075; i < 1024; i++ { // 2^-1075 rounds to zero
		want := math.Ldexp(1, i)
		in := fmt.Sprintf("%.16e", want) // C++ "%.*e" with max_digits10-1
		if got, ok := parseFloatDoc(t, &p, in); ok && got != want {
			t.Errorf("%s = %g, want %g", in, got, want)
		}
	}
}

// testingPowerOfTen holds 1e-323 ... 1e308 as Go constants, which the compiler
// rounds exactly. C++ testing_power_of_ten starts at 1e-307 and uses std::pow
// below that.
var testingPowerOfTen = [...]float64{
	1e-323, 1e-322, 1e-321, 1e-320, 1e-319, 1e-318, 1e-317, 1e-316, 1e-315, 1e-314,
	1e-313, 1e-312, 1e-311, 1e-310, 1e-309, 1e-308, 1e-307, 1e-306, 1e-305, 1e-304,
	1e-303, 1e-302, 1e-301, 1e-300, 1e-299, 1e-298, 1e-297, 1e-296, 1e-295, 1e-294,
	1e-293, 1e-292, 1e-291, 1e-290, 1e-289, 1e-288, 1e-287, 1e-286, 1e-285, 1e-284,
	1e-283, 1e-282, 1e-281, 1e-280, 1e-279, 1e-278, 1e-277, 1e-276, 1e-275, 1e-274,
	1e-273, 1e-272, 1e-271, 1e-270, 1e-269, 1e-268, 1e-267, 1e-266, 1e-265, 1e-264,
	1e-263, 1e-262, 1e-261, 1e-260, 1e-259, 1e-258, 1e-257, 1e-256, 1e-255, 1e-254,
	1e-253, 1e-252, 1e-251, 1e-250, 1e-249, 1e-248, 1e-247, 1e-246, 1e-245, 1e-244,
	1e-243, 1e-242, 1e-241, 1e-240, 1e-239, 1e-238, 1e-237, 1e-236, 1e-235, 1e-234,
	1e-233, 1e-232, 1e-231, 1e-230, 1e-229, 1e-228, 1e-227, 1e-226, 1e-225, 1e-224,
	1e-223, 1e-222, 1e-221, 1e-220, 1e-219, 1e-218, 1e-217, 1e-216, 1e-215, 1e-214,
	1e-213, 1e-212, 1e-211, 1e-210, 1e-209, 1e-208, 1e-207, 1e-206, 1e-205, 1e-204,
	1e-203, 1e-202, 1e-201, 1e-200, 1e-199, 1e-198, 1e-197, 1e-196, 1e-195, 1e-194,
	1e-193, 1e-192, 1e-191, 1e-190, 1e-189, 1e-188, 1e-187, 1e-186, 1e-185, 1e-184,
	1e-183, 1e-182, 1e-181, 1e-180, 1e-179, 1e-178, 1e-177, 1e-176, 1e-175, 1e-174,
	1e-173, 1e-172, 1e-171, 1e-170, 1e-169, 1e-168, 1e-167, 1e-166, 1e-165, 1e-164,
	1e-163, 1e-162, 1e-161, 1e-160, 1e-159, 1e-158, 1e-157, 1e-156, 1e-155, 1e-154,
	1e-153, 1e-152, 1e-151, 1e-150, 1e-149, 1e-148, 1e-147, 1e-146, 1e-145, 1e-144,
	1e-143, 1e-142, 1e-141, 1e-140, 1e-139, 1e-138, 1e-137, 1e-136, 1e-135, 1e-134,
	1e-133, 1e-132, 1e-131, 1e-130, 1e-129, 1e-128, 1e-127, 1e-126, 1e-125, 1e-124,
	1e-123, 1e-122, 1e-121, 1e-120, 1e-119, 1e-118, 1e-117, 1e-116, 1e-115, 1e-114,
	1e-113, 1e-112, 1e-111, 1e-110, 1e-109, 1e-108, 1e-107, 1e-106, 1e-105, 1e-104,
	1e-103, 1e-102, 1e-101, 1e-100, 1e-99, 1e-98, 1e-97, 1e-96, 1e-95, 1e-94,
	1e-93, 1e-92, 1e-91, 1e-90, 1e-89, 1e-88, 1e-87, 1e-86, 1e-85, 1e-84,
	1e-83, 1e-82, 1e-81, 1e-80, 1e-79, 1e-78, 1e-77, 1e-76, 1e-75, 1e-74,
	1e-73, 1e-72, 1e-71, 1e-70, 1e-69, 1e-68, 1e-67, 1e-66, 1e-65, 1e-64,
	1e-63, 1e-62, 1e-61, 1e-60, 1e-59, 1e-58, 1e-57, 1e-56, 1e-55, 1e-54,
	1e-53, 1e-52, 1e-51, 1e-50, 1e-49, 1e-48, 1e-47, 1e-46, 1e-45, 1e-44,
	1e-43, 1e-42, 1e-41, 1e-40, 1e-39, 1e-38, 1e-37, 1e-36, 1e-35, 1e-34,
	1e-33, 1e-32, 1e-31, 1e-30, 1e-29, 1e-28, 1e-27, 1e-26, 1e-25, 1e-24,
	1e-23, 1e-22, 1e-21, 1e-20, 1e-19, 1e-18, 1e-17, 1e-16, 1e-15, 1e-14,
	1e-13, 1e-12, 1e-11, 1e-10, 1e-9, 1e-8, 1e-7, 1e-6, 1e-5, 1e-4,
	1e-3, 1e-2, 1e-1, 1e0, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6,
	1e7, 1e8, 1e9, 1e10, 1e11, 1e12, 1e13, 1e14, 1e15, 1e16,
	1e17, 1e18, 1e19, 1e20, 1e21, 1e22, 1e23, 1e24, 1e25, 1e26,
	1e27, 1e28, 1e29, 1e30, 1e31, 1e32, 1e33, 1e34, 1e35, 1e36,
	1e37, 1e38, 1e39, 1e40, 1e41, 1e42, 1e43, 1e44, 1e45, 1e46,
	1e47, 1e48, 1e49, 1e50, 1e51, 1e52, 1e53, 1e54, 1e55, 1e56,
	1e57, 1e58, 1e59, 1e60, 1e61, 1e62, 1e63, 1e64, 1e65, 1e66,
	1e67, 1e68, 1e69, 1e70, 1e71, 1e72, 1e73, 1e74, 1e75, 1e76,
	1e77, 1e78, 1e79, 1e80, 1e81, 1e82, 1e83, 1e84, 1e85, 1e86,
	1e87, 1e88, 1e89, 1e90, 1e91, 1e92, 1e93, 1e94, 1e95, 1e96,
	1e97, 1e98, 1e99, 1e100, 1e101, 1e102, 1e103, 1e104, 1e105, 1e106,
	1e107, 1e108, 1e109, 1e110, 1e111, 1e112, 1e113, 1e114, 1e115, 1e116,
	1e117, 1e118, 1e119, 1e120, 1e121, 1e122, 1e123, 1e124, 1e125, 1e126,
	1e127, 1e128, 1e129, 1e130, 1e131, 1e132, 1e133, 1e134, 1e135, 1e136,
	1e137, 1e138, 1e139, 1e140, 1e141, 1e142, 1e143, 1e144, 1e145, 1e146,
	1e147, 1e148, 1e149, 1e150, 1e151, 1e152, 1e153, 1e154, 1e155, 1e156,
	1e157, 1e158, 1e159, 1e160, 1e161, 1e162, 1e163, 1e164, 1e165, 1e166,
	1e167, 1e168, 1e169, 1e170, 1e171, 1e172, 1e173, 1e174, 1e175, 1e176,
	1e177, 1e178, 1e179, 1e180, 1e181, 1e182, 1e183, 1e184, 1e185, 1e186,
	1e187, 1e188, 1e189, 1e190, 1e191, 1e192, 1e193, 1e194, 1e195, 1e196,
	1e197, 1e198, 1e199, 1e200, 1e201, 1e202, 1e203, 1e204, 1e205, 1e206,
	1e207, 1e208, 1e209, 1e210, 1e211, 1e212, 1e213, 1e214, 1e215, 1e216,
	1e217, 1e218, 1e219, 1e220, 1e221, 1e222, 1e223, 1e224, 1e225, 1e226,
	1e227, 1e228, 1e229, 1e230, 1e231, 1e232, 1e233, 1e234, 1e235, 1e236,
	1e237, 1e238, 1e239, 1e240, 1e241, 1e242, 1e243, 1e244, 1e245, 1e246,
	1e247, 1e248, 1e249, 1e250, 1e251, 1e252, 1e253, 1e254, 1e255, 1e256,
	1e257, 1e258, 1e259, 1e260, 1e261, 1e262, 1e263, 1e264, 1e265, 1e266,
	1e267, 1e268, 1e269, 1e270, 1e271, 1e272, 1e273, 1e274, 1e275, 1e276,
	1e277, 1e278, 1e279, 1e280, 1e281, 1e282, 1e283, 1e284, 1e285, 1e286,
	1e287, 1e288, 1e289, 1e290, 1e291, 1e292, 1e293, 1e294, 1e295, 1e296,
	1e297, 1e298, 1e299, 1e300, 1e301, 1e302, 1e303, 1e304, 1e305, 1e306,
	1e307, 1e308,
}

// C++ tests/dom/basictests.cpp: powers_of_ten, negative_powers_of_ten and
// signed_zero_underflow_exponent (the same inputs as powers_of_ten, without
// the sign check).
func TestPowersOfTen(t *testing.T) {
	var p Parser
	for _, sign := range []float64{1, -1} {
		for i := -1000; i <= 308; i++ {
			want := 0.0 // 1e-324 and below round to zero
			if i >= -323 {
				want = testingPowerOfTen[i+323]
			}
			want = math.Copysign(want, sign)
			in := fmt.Sprintf("1e%d", i)
			if sign < 0 {
				in = "-" + in
			}
			if got, ok := parseFloatDoc(t, &p, in); ok && (got != want || math.Signbit(got) != math.Signbit(want)) {
				t.Errorf("%s = %g, want %g", in, got, want)
			}
		}
	}
}

// C++ tests/dom/basictests.cpp: truncated_borderline and specific_tests
// (basic_test_64bit). The "-0" case needs SIMDJSON_MINUS_ZERO_AS_FLOAT and is
// not ported; basic_test_64bit's fallback to the C library strtod is not
// needed, the Go compiler rounds the expected constants exactly.
func TestSpecificFloats(t *testing.T) {
	tests := []struct {
		in   string
		want float64
	}{
		{"9007199254740993.0" + strings.Repeat("0", 1000), 9007199254740992},
		{"-1e-999", math.Copysign(0, -1)},
		{"-2402844368454405395.2", -2402844368454405395.2},
		{"4503599627370496.5", 4503599627370496.5},
		{"4503599627475352.5", 4503599627475352.5},
		{"4503599627475353.5", 4503599627475353.5},
		{"2251799813685248.25", 2251799813685248.25},
		{"1125899906842624.125", 1125899906842624.125},
		{"1125899906842901.875", 1125899906842901.875},
		{"2251799813685803.75", 2251799813685803.75},
		{"4503599627370497.5", 4503599627370497.5},
		{"45035996.273704995", 45035996.273704995},
		{"45035996.273704985", 45035996.273704985},
		{"0.000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000044501477170144022721148195934182639518696390927032912960468522194496444440421538910330590478162701758282983178260792422137401728773891892910553144148156412434867599762821265346585071045737627442980259622449029037796981144446145705102663115100318287949527959668236039986479250965780342141637013812613333119898765515451440315261253813266652951306000184917766328660755595837392240989947807556594098101021612198814605258742579179000071675999344145086087205681577915435923018910334964869420614052182892431445797605163650903606514140377217442262561590244668525767372446430075513332450079650686719491377688478005309963967709758965844137894433796621993967316936280457084866613206797017728916080020698679408551343728867675409720757232455434770912461317493580281734466552734375", 0.000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000044501477170144022721148195934182639518696390927032912960468522194496444440421538910330590478162701758282983178260792422137401728773891892910553144148156412434867599762821265346585071045737627442980259622449029037796981144446145705102663115100318287949527959668236039986479250965780342141637013812613333119898765515451440315261253813266652951306000184917766328660755595837392240989947807556594098101021612198814605258742579179000071675999344145086087205681577915435923018910334964869420614052182892431445797605163650903606514140377217442262561590244668525767372446430075513332450079650686719491377688478005309963967709758965844137894433796621993967316936280457084866613206797017728916080020698679408551343728867675409720757232455434770912461317493580281734466552734375},
		{"0.000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000022250738585072008890245868760858598876504231122409594654935248025624400092282356951787758888037591552642309780950434312085877387158357291821993020294379224223559819827501242041788969571311791082261043971979604000454897391938079198936081525613113376149842043271751033627391549782731594143828136275113838604094249464942286316695429105080201815926642134996606517803095075913058719846423906068637102005108723282784678843631944515866135041223479014792369585208321597621066375401613736583044193603714778355306682834535634005074073040135602968046375918583163124224521599262546494300836851861719422417646455137135420132217031370496583210154654068035397417906022589503023501937519773030945763173210852507299305089761582519159720757232455434770912461317493580281734466552734375", 0.000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000022250738585072008890245868760858598876504231122409594654935248025624400092282356951787758888037591552642309780950434312085877387158357291821993020294379224223559819827501242041788969571311791082261043971979604000454897391938079198936081525613113376149842043271751033627391549782731594143828136275113838604094249464942286316695429105080201815926642134996606517803095075913058719846423906068637102005108723282784678843631944515866135041223479014792369585208321597621066375401613736583044193603714778355306682834535634005074073040135602968046375918583163124224521599262546494300836851861719422417646455137135420132217031370496583210154654068035397417906022589503023501937519773030945763173210852507299305089761582519159720757232455434770912461317493580281734466552734375},
	}
	var p Parser
	for _, tt := range tests {
		if got, ok := parseFloatDoc(t, &p, tt.in); ok && (got != tt.want || math.Signbit(got) != math.Signbit(tt.want)) {
			t.Errorf("%.60s = %x, want %x", tt.in, got, tt.want)
		}
	}
}

// C++ tests/dom/basictests.cpp: twitter_count, twitter_default_profile and
// twitter_image_sizes (the *_exception variants repeat them with exceptions).
func TestTwitterQueries(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(testdataDir(t, "jsonexamples"), "twitter.json"))
	if err != nil {
		t.Fatal(err)
	}
	var p Parser
	doc, err := p.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	root := doc.Root()
	if n, err := field(t, field(t, root, "search_metadata"), "count").Uint64(); err != nil || n != 100 {
		t.Errorf("search_metadata.count = %d, %v; want 100", n, err)
	}
	tweets, err := field(t, root, "statuses").Array()
	if err != nil {
		t.Fatal(err)
	}
	defaultUsers := map[string]bool{}
	type size struct{ w, h uint64 }
	imageSizes := map[size]bool{}
	for _, tweet := range tweets.All() {
		user := field(t, tweet, "user")
		if b, err := field(t, user, "default_profile").Bool(); err != nil {
			t.Fatal(err)
		} else if b {
			name, err := field(t, user, "screen_name").StringValue()
			if err != nil {
				t.Fatal(err)
			}
			defaultUsers[name] = true
		}
		media, err := tweet.AtPointer("/entities/media")
		if err != nil {
			continue // C++ skips tweets without entities.media
		}
		images, err := media.Array()
		if err != nil {
			t.Fatal(err)
		}
		for _, image := range images.All() {
			sizes, err := field(t, image, "sizes").Object()
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range sizes.All() {
				w, errW := field(t, s, "w").Uint64()
				h, errH := field(t, s, "h").Uint64()
				if errW != nil || errH != nil {
					t.Fatal(errW, errH)
				}
				imageSizes[size{w, h}] = true
			}
		}
	}
	if len(defaultUsers) != 86 {
		t.Errorf("%d users with a default profile, want 86", len(defaultUsers))
	}
	if len(imageSizes) != 15 {
		t.Errorf("%d image sizes, want 15", len(imageSizes))
	}
}

// C++ tests/dom/document_tests.cpp: lots_of_brackets
func TestLotsOfBrackets(t *testing.T) {
	mustParse(t, strings.Repeat("[", 200)+strings.Repeat("]", 200))
}

// C++ tests/dom/document_tests.cpp: skyprophet_test
func TestSkyprophet(t *testing.T) {
	const n = 100
	var data []string
	for i := range n {
		gender := "female"
		if i%2 == 1 {
			gender = "male"
		}
		data = append(data, fmt.Sprintf(`{"id": %d, "name": "name%d", "gender": "%s", "school": {"id": %d, "name": "school%d"}}`,
			i, i, gender, i%10, i%10))
	}
	for i := range n {
		data = append(data, fmt.Sprintf(`{"counter": %f, "array": [%t]}`, float64(i)*3.1416, i%2 == 1))
	}
	for i := range n {
		data = append(data, fmt.Sprintf(`{"number": %e}`, float64(i)*10000.31321321))
	}
	data = append(data, "true", "false", "null", "0.1")
	var p Parser
	for _, rec := range data {
		for range 2 {
			if _, err := p.Parse([]byte(rec)); err != nil {
				t.Errorf("Parse(%q): %v", rec, err)
			}
		}
	}
}

// C++ tests/dom/document_tests.cpp: issue938. C++ prints each element and type;
// only success is checked.
func TestIssue938(t *testing.T) {
	var p1 Parser
	for _, s := range []string{"[true,false]", "[1,2,3,null]", `{"yay":"json!"}`} {
		if _, err := p1.Parse([]byte(s)); err != nil {
			t.Errorf("Parse(%q): %v", s, err)
		}
	}
	dir := testdataDir(t, "jsonexamples")
	files := []string{
		"small/adversarial.json", "small/flatadversarial.json", "small/demo.json",
		"twitter_timeline.json", "repeat.json", "small/smalldemo.json", "small/truenull.json",
	}
	var p3 Parser
	for _, reuse := range []bool{false, true} {
		for _, name := range files {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			p := &p3
			if !reuse {
				p = new(Parser)
			}
			if _, err := p.Parse(data); err != nil {
				t.Errorf("%s (reused parser %v): %v", name, reuse, err)
			}
		}
	}
}

// C++ tests/dom/document_tests.cpp: count_array_example and
// count_object_example. Their iterator comparisons (<, ==) have no Go
// equivalent and are not ported.
func TestCountExamples(t *testing.T) {
	a, err := mustParse(t, "[1,2,3]").Array()
	if err != nil {
		t.Fatal(err)
	}
	if a.Len() != 3 {
		t.Errorf("array Len = %d, want 3", a.Len())
	}
	o, err := mustParse(t, `{"1":1,"2":1,"3":1}`).Object()
	if err != nil {
		t.Fatal(err)
	}
	if o.Len() != 3 {
		t.Errorf("object Len = %d, want 3", o.Len())
	}
}
