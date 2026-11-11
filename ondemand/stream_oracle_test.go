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
// to C++'s current_index and truncated_bytes.
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
			}
			if ondemand.Abandoned(doc) {
				toks = append(toks, "dead")
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
	n, skipped, fatalCut, failed := 0, 0, 0, 0
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
		if want, ok := cutFatalEnd(c.Out); ok {
			fatalCut++
			c.Out = want
			if strings.HasPrefix(got, want+" ") {
				got = want
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
	t.Logf("%d cases, %d skipped (stage 1 errors), %d compared only up to C++ ending the stream after a fatal read error", n, skipped, fatalCut)
}

// cutFatalEnd recognizes C++ output whose stream ends at its last document
// because a read on it met a fatal error: "@n h… … dead @n !code", or, when
// the error did not abandon the document (skip_child's), "@n h… … xcode @n
// !code". Go yields the documents after it instead (streams spec §4.3, a
// documented difference). It returns the output up to that document's last
// token, "dead" included.
func cutFatalEnd(out string) (string, bool) {
	toks := strings.Fields(out)
	k := len(toks)
	if k < 4 || toks[k-2][0] != '@' || toks[k-1][0] != '!' {
		return "", false
	}
	if last := toks[k-3]; last != "dead" {
		if last[0] != 'x' && last[0] != '!' || last[1:] != toks[k-1][1:] {
			return "", false
		}
		i := k - 3
		for i >= 0 && toks[i][0] != '@' {
			i--
		}
		if i < 0 || toks[i] != toks[k-2] { // the error is not the last document's
			return "", false
		}
	}
	return strings.Join(toks[:k-2], " "), true
}

// FuzzIterateMany checks that no batch size changes what either stream
// yields, with an arbitrary script of reads run on every On-Demand document,
// also past a document a read abandoned.
func FuzzIterateMany(f *testing.F) {
	for _, s := range []string{"1 2 34", `[1,23] "x" {"k":"v}`, `{"a":1},{"b":`, "\x1e1\n\x1e2",
		`[{"a":1},2]`, "[1,23 [1,23]", "{\"a\":1,\n\"b\":2}\n[3]", "{\"a\":1}\n{\"a\":}\n{\"a\":3}"} {
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
