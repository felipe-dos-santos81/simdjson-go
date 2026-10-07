package simdjson

// Ports of C++ simdjson tests/dom/unpadded_tests.cpp, and of the basictests.cpp
// and document_tests.cpp cases not covered by the other test files. Expected
// errors that the C++ tests leave implicit come from C++ simdjson v5.0.2.

import (
	"fmt"
	"math"
	"math/big"
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
	var p Parser
	for _, name := range []string{
		"twitter.json", "twitter_timeline.json", "canada.json", "mesh.json", "apache_builds.json",
		"gsoc-2018.json", "repeat.json", "small/demo.json", "small/smalldemo.json",
		"small/adversarial.json", "small/flatadversarial.json", "small/truenull.json",
	} {
		checkUnpadded(t, &p, string(readTestdata(t, "jsonexamples", name)), nil)
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
	type errCase struct {
		in   string
		want error
	}
	tests := []errCase{
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
			tests = append(tests, errCase{in, ErrUnclosedString})
		}
	}
	var p Parser
	for _, tt := range tests {
		checkUnpadded(t, &p, tt.in, tt.want)
	}
}

// Not ported from unpadded_tests.cpp: nan_inf_at_end needs the
// SIMDJSON_ENABLE_NAN_INF build option, which this port does not have.

// C++ basictests.cpp issue2213 (repeated parses) is covered by TestParserReuse.

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

// powerOfTen returns 10^i correctly rounded to float64 (0 below 1e-323),
// computed with math/big so the expected value does not come from strconv,
// the code under test. C++ testing_power_of_ten is a hand-written table.
func powerOfTen(i int) float64 {
	r, _ := new(big.Rat).SetString("1e" + strconv.Itoa(i))
	f, _ := r.Float64()
	return f
}

// C++ tests/dom/basictests.cpp: powers_of_ten, negative_powers_of_ten and
// signed_zero_underflow_exponent (the same inputs as powers_of_ten, without
// the sign check).
func TestPowersOfTen(t *testing.T) {
	var p Parser
	for _, sign := range []float64{1, -1} {
		for i := -1000; i <= 308; i++ {
			want := math.Copysign(powerOfTen(i), sign)
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
	data := readTestdata(t, "jsonexamples", "twitter.json")
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

// Covered elsewhere, so not repeated here (C++ tests/dom/document_tests.cpp):
// lots_of_brackets by TestMaxDepth, issue938 by TestParserReuse and
// TestUnpaddedRealFiles, count_array_example and count_object_example by
// TestArray and TestObject.

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
