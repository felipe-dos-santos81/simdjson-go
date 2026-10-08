package ondemand_test

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"simdjson-go"
	"simdjson-go/internal/jsonerr"
	"simdjson-go/internal/stream"
	"simdjson-go/ondemand"
)

// The C++ oracle (scripts/ondemand-oracle) ran each stream case through
// parse_many or iterate_many in one batch; streamOut must print the same.

type streamCase struct {
	Doc    string   `json:"doc,omitempty"`
	File   string   `json:"file,omitempty"`
	Script []string `json:"script"`
	Stream struct {
		API    string `json:"api"`
		Format string `json:"format"`
	} `json:"stream"`
	Out string `json:"out"`
}

var formats = map[string]simdjson.Format{
	"whitespace": simdjson.Whitespace, "newline": simdjson.NewlineDelimited,
	"sequence": simdjson.JSONSequence, "comma": simdjson.CommaDelimited, "array": simdjson.CommaDelimitedArray,
}

// walkDOM prints a DOM value as oracle.cpp's walk_dom does.
func walkDOM(e simdjson.Element, out *strings.Builder) {
	switch e.Type() {
	case simdjson.TypeArray:
		a, _ := e.Array()
		out.WriteByte('[')
		for _, x := range a.All() {
			walkDOM(x, out)
			out.WriteByte(',')
		}
		out.WriteByte(']')
	case simdjson.TypeObject:
		o, _ := e.Object()
		out.WriteByte('{')
		for k, v := range o.AllBytes() {
			out.WriteString(esc(k))
			out.WriteByte(':')
			walkDOM(v, out)
			out.WriteByte(',')
		}
		out.WriteByte('}')
	case simdjson.TypeInt64:
		n, _ := e.Int64()
		fmt.Fprintf(out, "i%d", n)
	case simdjson.TypeUint64:
		n, _ := e.Uint64()
		fmt.Fprintf(out, "u%d", n)
	case simdjson.TypeFloat64:
		x, _ := e.Float64()
		out.WriteString(f64(x))
	case simdjson.TypeString:
		s, _ := e.StringBytes()
		out.WriteString("s" + esc(s))
	case simdjson.TypeBool:
		if b, _ := e.Bool(); b {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case simdjson.TypeNull:
		out.WriteString("null")
	default:
		out.WriteByte('?')
	}
}

// streamOut runs a stream through the DOM or On-Demand and prints it as
// oracle.cpp does, translating Go's offsets and final ErrTrailingContent
// to C++'s current_index and truncated_bytes. Like oracle.cpp, it stops
// after a read that abandoned its document: C++ cannot go on from there.
func streamOut(dp *simdjson.Parser, op *ondemand.Parser, api string, in []byte, f simdjson.Format, script []string) string {
	buf, base, _, ok := stream.Input(in, f)
	var toks []string
	end := func(err error) string {
		var se *jsonerr.StreamError
		switch {
		case !ok: // C++ fails before streaming
			toks = append(toks, errToken(err))
		case errors.As(err, &se) && errors.Is(err, jsonerr.ErrTrailingContent):
			toks = append(toks, "~"+strconv.Itoa(len(buf)-(se.Offset-base)))
		case errors.As(err, &se):
			toks = append(toks, "@"+strconv.Itoa(se.Offset-base), errToken(err))
		default:
			toks = append(toks, "not a *StreamError: "+err.Error())
		}
		return joinOut(toks)
	}
	if api == "dom" {
		for doc, err := range dp.ParseMany(in, f) {
			if err != nil {
				return end(err)
			}
			var w strings.Builder
			walkDOM(doc.Root(), &w)
			toks = append(toks, "@"+strconv.Itoa(doc.Offset()-base), "h"+hex.EncodeToString(doc.Source()), "w"+w.String())
		}
	} else {
		for doc, err := range op.IterateMany(in, f) {
			if err != nil {
				return end(err)
			}
			toks = append(toks, "@"+strconv.Itoa(doc.Offset()-base), "h"+hex.EncodeToString(doc.Source()))
			if err := runOps(doc, script, &toks); err != nil {
				toks = append(toks, "x"+errToken(err)[1:])
				if ondemand.Abandoned(doc) {
					return joinOut(append(toks, "dead"))
				}
			}
		}
	}
	toks = append(toks, "~0")
	return joinOut(toks)
}

func TestStreamOracle(t *testing.T) {
	f, err := os.Open("../testdata/stream/oracle.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<26)
	var dp simdjson.Parser
	var op ondemand.Parser
	n, skipped, pastEndSkipped, failed := 0, 0, 0, 0
	for sc.Scan() {
		var c streamCase
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		in, err := oracleCase{Doc: c.Doc, File: c.File}.input()
		if err != nil {
			t.Fatal(err)
		}
		n++
		f := formats[c.Stream.Format]
		// Stage 1 errors are reported where they are (spec §4.1), and C++
		// trims a cut character at the end: covered by TestStreamStage1Errors.
		if buf, _, _, _ := stream.Input(in, f); !utf8.Valid(buf) || strings.HasPrefix(c.Out, "@0 !14") {
			skipped++
			continue
		}
		got := streamOut(&dp, &op, c.Stream.API, in, f, c.Script)
		if c.Stream.API == "dom" && f == simdjson.CommaDelimitedArray {
			buf, _, _, _ := stream.Input(in, f)
			if want, ok := pastEnd(c.Out, len(buf)); ok {
				// Compare up to the document that closed on the array's ']';
				// Go then ends the stream: one ~n, or one @n !code.
				pastEndSkipped++
				c.Out = want
				if rest, ok := strings.CutPrefix(got, want+" "); ok && streamEnd(rest) {
					got = want
				}
			}
		}
		if got != c.Out {
			failed++
			if failed <= 20 {
				t.Errorf("case %d %s %s %s %q %q:\n got  %.300s\n want %.300s", n, c.File, c.Stream.API,
					c.Stream.Format, abbrev(in), c.Script, got, c.Out)
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if failed > 0 {
		t.Errorf("%d of %d cases differ", failed, n)
	}
	t.Logf("%d cases, %d skipped (stage 1 errors), %d compared only up to C++ reading past its end", n, skipped, pastEndSkipped)
}

// streamEnd reports whether out is just the end of a stream: "~n" or "@n !code".
func streamEnd(out string) bool {
	toks := strings.Fields(out)
	return len(toks) == 1 && toks[0][0] == '~' || len(toks) == 2 && toks[0][0] == '@' && toks[1][0] == '!'
}

// pastEnd recognizes C++ DOM output for a CommaDelimitedArray stream whose
// document reached the end of the array's contents (n bytes), closed on the
// array's own ']' (its source ends one byte past them), and was followed by
// more: C++ then goes on past its end-of-input sentinel, which Go does not
// port (spec §4.3). It returns the output up to that document.
func pastEnd(out string, n int) (string, bool) {
	toks := strings.Fields(out)
	for i := 0; i+3 < len(toks); i++ {
		if toks[i][0] != '@' || toks[i+1][0] != 'h' {
			continue
		}
		at, err := strconv.Atoi(toks[i][1:])
		if err == nil && at+(len(toks[i+1])-1)/2 == n+1 && toks[i+3][0] == '@' {
			return strings.Join(toks[:i+3], " "), true
		}
	}
	return "", false
}

// FuzzIterateMany checks that no batch size changes what either stream
// yields, with an arbitrary script of reads run on every On-Demand document.
func FuzzIterateMany(f *testing.F) {
	for _, s := range []string{"1 2 34", `[1,23] "x" {"k":"v}`, `{"a":1},{"b":`, "\x1e1\n\x1e2",
		`[{"a":1},2]`, "[1,23 [1,23]", "{\"a\":1,\n\"b\":2}\n[3]"} {
		f.Add([]byte(s), uint8(0), uint8(0), []byte{0, 26})
	}
	f.Fuzz(func(t *testing.T, in []byte, format, batch uint8, ops []byte) {
		fm := simdjson.Format(format % 5)
		script := make([]string, 0, 8)
		for _, b := range ops[:min(len(ops), 8)] {
			script = append(script, fuzzOps[int(b)%len(fuzzOps)])
		}
		var dp simdjson.Parser
		var op ondemand.Parser
		for _, api := range []string{"dom", "ondemand"} {
			dp.BatchSize, op.BatchSize = 0, 0
			want := streamOut(&dp, &op, api, in, fm, script)
			dp.BatchSize, op.BatchSize = 64*(1+int(batch)%8), 64*(1+int(batch)%8)
			if got := streamOut(&dp, &op, api, in, fm, script); got != want {
				t.Fatalf("%s, BatchSize %d, %q %q:\n got  %s\n want %s", api, dp.BatchSize, in, script, got, want)
			}
		}
	})
}
