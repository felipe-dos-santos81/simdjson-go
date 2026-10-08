package simdjson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

var fuzzSeeds = []string{
	`{"a":[1,2.5,"x",true,null,{}]}`, `[]`, `"\u00e9\ud83d\ude00"`, `-0`, `1e400`, "\xEF\xBB\xBF{}",
	`[1,]`, `{"a":1,"a":2}`, `{"a":1e400,"a":1}`, `"\ud800"`, `18446744073709551616`, `[[[[1]]]]`, "\"\x01\"",
}

// FuzzParse checks Parse against an oracle built from encoding/json: a document
// is accepted exactly when encoding/json accepts it, it is valid UTF-8, it nests
// at most 1023 non-empty containers, its numbers fit int64/uint64/finite
// float64, and its \u escapes form valid surrogate pairs. Accepted documents must
// decode to the same tree and round-trip through AppendJSON.
func FuzzParse(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		var p Parser
		doc, err := p.Parse(in)
		if want := oracleAccepts(in); want != (err == nil) {
			t.Fatalf("Parse(%q): err = %v, oracle accepts = %v", in, err, want)
		}
		if err != nil {
			return
		}
		if n := tapeWords(len(bytes.TrimPrefix(in, bom)), len(p.indices)); len(doc.tape) > n { // a larger tape would allocate
			t.Fatalf("Parse(%q): %d tape words, bound %d", in, len(doc.tape), n)
		}
		if diff := sameAsStdlib(doc.Root(), in); diff != "" {
			t.Fatalf("Parse(%q) differs from encoding/json: %s", in, diff)
		}
		var q Parser
		if diff := roundTripDiff(&q, doc.Root().AppendJSON(nil), in); diff != "" {
			t.Fatalf("Parse(%q): %s", in, diff)
		}
	})
}

// FuzzMinify checks that minifying an accepted document does not change it.
func FuzzMinify(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		var p, q Parser
		doc, err := p.Parse(in)
		if err != nil {
			return
		}
		if diff := minifyDiff(&q, doc.Root().AppendJSON(nil), in); diff != "" {
			t.Fatalf("Minify(%q): %s", in, diff)
		}
	})
}

func oracleAccepts(in []byte) bool {
	in = bytes.TrimPrefix(in, bom)
	if !json.Valid(in) || !utf8.Valid(in) || !validSurrogates(in) {
		return false
	}
	// Walk the tokens rather than a decoded tree: a map keeps only the last of
	// duplicate keys, which would hide the depth and numbers of the others.
	dec := json.NewDecoder(bytes.NewReader(in))
	dec.UseNumber()
	var open []bool // per open array/object: whether it is non-empty
	depth := 0      // open non-empty containers (empty ones do not open a scope in simdjson)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return true
		}
		if err != nil {
			return false
		}
		switch tok := tok.(type) {
		case json.Delim:
			if tok == '[' || tok == '{' {
				nonEmpty := dec.More()
				open = append(open, nonEmpty)
				if nonEmpty {
					if depth++; depth > defaultMaxDepth-1 {
						return false
					}
				}
				continue
			}
			if open[len(open)-1] {
				depth--
			}
			open = open[:len(open)-1]
		case json.Number:
			if !numberFits(string(tok)) {
				return false
			}
		}
	}
}

// numberFits reports whether simdjson accepts the number: integers must fit
// int64 or uint64, floats must not overflow to ±Inf.
func numberFits(s string) bool {
	if strings.ContainsAny(s, ".eE") {
		_, err := strconv.ParseFloat(s, 64)
		return err == nil
	}
	_, errI := strconv.ParseInt(s, 10, 64)
	_, errU := strconv.ParseUint(s, 10, 64)
	return errI == nil || errU == nil
}

// validSurrogates reports whether every \u escape in the (syntactically valid)
// document is a BMP code point or a high surrogate followed by a low one.
// encoding/json silently replaces bad surrogates with U+FFFD; simdjson rejects them.
func validSurrogates(in []byte) bool {
	inString := false
	for i := 0; i < len(in); i++ {
		switch c := in[i]; {
		case c == '"':
			inString = !inString
		case c == '\\' && inString:
			if in[i+1] != 'u' {
				i++ // skip the escaped byte
				continue
			}
			cp, _ := strconv.ParseUint(string(in[i+2:i+6]), 16, 32)
			i += 5
			switch {
			case cp >= 0xD800 && cp < 0xDC00:
				if i+6 >= len(in) || in[i+1] != '\\' || in[i+2] != 'u' {
					return false
				}
				low, _ := strconv.ParseUint(string(in[i+3:i+7]), 16, 32)
				if low < 0xDC00 || low > 0xDFFF {
					return false
				}
				i += 6
			case cp >= 0xDC00 && cp <= 0xDFFF:
				return false
			}
		}
	}
	return true
}

// manyFull lists a stream's items with each document's JSON.
func manyFull(p *Parser, in []byte, f Format) string {
	var out []string
	for doc, err := range p.ParseMany(in, f) {
		if err != nil {
			out = append(out, "!"+err.Error())
			continue
		}
		out = append(out, fmt.Sprintf("%d:%q:%s", doc.Offset(), doc.Source(), doc.Root().AppendJSON(nil)))
	}
	return strings.Join(out, " ")
}

// FuzzParseMany checks that no batch size changes what ParseMany yields,
// and that every document equals Parse of its source when that succeeds.
func FuzzParseMany(f *testing.F) {
	for _, s := range []string{"1 2 34", `[1,23] "x" {"k":"v}`, `{"a":1},{"b":`, "\x1e1\n\x1e2",
		`[{"a":1},2]`, "1 2 \xff 3", "[1] [2", `{"a":[1,{"b":"\u00e9"}]}` + "\n" + `{"a":2}`} {
		f.Add([]byte(s), uint8(0), uint8(0))
	}
	f.Fuzz(func(t *testing.T, in []byte, format, batch uint8) {
		fm := Format(format % 5)
		var p Parser
		want := manyFull(&p, in, fm) // one window for any fuzz-sized input
		p.BatchSize = 64 * (1 + int(batch)%8)
		if got := manyFull(&p, in, fm); got != want {
			t.Fatalf("BatchSize %d, %q:\n got  %s\n want %s", p.BatchSize, in, got, want)
		}
		var q Parser
		for doc, err := range p.ParseMany(in, fm) {
			if err != nil {
				break
			}
			d2, err := q.Parse(doc.Source())
			if err != nil {
				continue
			}
			if a, b := doc.Root().AppendJSON(nil), d2.Root().AppendJSON(nil); !bytes.Equal(a, b) {
				t.Fatalf("%q: document %q is %s, Parse gives %s", in, doc.Source(), a, b)
			}
		}
	})
}
