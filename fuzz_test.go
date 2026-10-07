package simdjson

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

var fuzzSeeds = []string{
	`{"a":[1,2.5,"x",true,null,{}]}`, `[]`, `"\u00e9\ud83d\ude00"`, `-0`, `1e400`, "\xEF\xBB\xBF{}",
	`[1,]`, `{"a":1,"a":2}`, `"\ud800"`, `18446744073709551616`, `[[[[1]]]]`, "\"\x01\"",
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
		if diff := sameAsStdlib(doc.Root(), in); diff != "" {
			t.Fatalf("Parse(%q) differs from encoding/json: %s", in, diff)
		}
		out := doc.Root().AppendJSON(nil)
		var q Parser
		again, err := q.Parse(out)
		if err != nil || !bytes.Equal(again.Root().AppendJSON(nil), out) {
			t.Fatalf("AppendJSON(%q) = %q does not round-trip (%v)", in, out, err)
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
		min, err := Minify(nil, in)
		if err != nil {
			t.Fatalf("Minify(%q): %v", in, err)
		}
		m, err := q.Parse(min)
		if err != nil || !bytes.Equal(m.Root().AppendJSON(nil), doc.Root().AppendJSON(nil)) {
			t.Fatalf("Minify(%q) = %q changed the document (%v)", in, min, err)
		}
	})
}

func oracleAccepts(in []byte) bool {
	in = bytes.TrimPrefix(in, bom)
	if !json.Valid(in) || !utf8.Valid(in) || !validSurrogates(in) {
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(in))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return false
	}
	return depth(v) <= 1023 && numbersFit(v)
}

// depth counts nested non-empty arrays/objects (empty ones do not open a
// scope in simdjson).
func depth(v any) int {
	d := 0
	switch v := v.(type) {
	case []any:
		for _, c := range v {
			d = max(d, depth(c))
		}
		if len(v) > 0 {
			d++
		}
	case map[string]any:
		for _, c := range v {
			d = max(d, depth(c))
		}
		if len(v) > 0 {
			d++
		}
	}
	return d
}

func numbersFit(v any) bool {
	switch v := v.(type) {
	case []any:
		for _, c := range v {
			if !numbersFit(c) {
				return false
			}
		}
	case map[string]any:
		for _, c := range v {
			if !numbersFit(c) {
				return false
			}
		}
	case json.Number:
		s := string(v)
		if strings.ContainsAny(s, ".eE") {
			_, err := strconv.ParseFloat(s, 64)
			return err == nil
		}
		_, errI := strconv.ParseInt(s, 10, 64)
		_, errU := strconv.ParseUint(s, 10, 64)
		return errI == nil || errU == nil
	}
	return true
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
