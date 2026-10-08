package ondemand_test

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"simdjson-go"
	"simdjson-go/ondemand"
)

// must returns x, panicking on err (for tests and benchmarks).
func must[T any](x T, err error) T {
	if err != nil {
		panic(err)
	}
	return x
}

// walkInfo records what a walk saw that C++'s On-Demand/DOM differences
// depend on.
type walkInfo struct {
	// badBigInt: a BigInt the DOM rejects: an integer too long for 64 bits
	// with a leading zero, or a bare "-" (a root past the 1083-byte buffer,
	// which C++'s check_if_integer accepts).
	badBigInt bool
}

// fullWalk reads the whole document with On-Demand into a canonical text,
// and checks nothing follows it. Any error means the document is invalid.
func fullWalk(p *ondemand.Parser, in []byte) (string, walkInfo, error) {
	doc, err := p.Iterate(in)
	if err != nil {
		return "", walkInfo{}, err
	}
	var b strings.Builder
	var info walkInfo
	if err := odWalk(doc, &b, &info); err != nil {
		return "", walkInfo{}, err
	}
	if !doc.AtEnd() {
		return "", walkInfo{}, errors.New("trailing content")
	}
	return b.String(), info, nil
}

type odReader interface {
	Type() (ondemand.Type, error)
	NumberType() (ondemand.NumberType, error)
	Int64() (int64, error)
	Uint64() (uint64, error)
	Float64() (float64, error)
	Bool() (bool, error)
	IsNull() (bool, error)
	String() (string, error)
	Raw() ([]byte, error)
	Object() (ondemand.Object, error)
	Array() (ondemand.Array, error)
}

func odWalk(v odReader, b *strings.Builder, info *walkInfo) error {
	t, err := v.Type()
	if err != nil {
		return err
	}
	switch t {
	case ondemand.TypeObject:
		o, err := v.Object()
		if err != nil {
			return err
		}
		b.WriteByte('{')
		for f, err := range o.All() {
			if err != nil {
				return err
			}
			k, err := f.Key()
			if err != nil {
				return err
			}
			fmt.Fprintf(b, "%q:", k)
			if err := odWalk(f.Value(), b, info); err != nil {
				return err
			}
			b.WriteByte(',')
		}
		b.WriteByte('}')
	case ondemand.TypeArray:
		a, err := v.Array()
		if err != nil {
			return err
		}
		b.WriteByte('[')
		for e, err := range a.All() {
			if err != nil {
				return err
			}
			if err := odWalk(e, b, info); err != nil {
				return err
			}
			b.WriteByte(',')
		}
		b.WriteByte(']')
	case ondemand.TypeNumber:
		nt, err := v.NumberType()
		if err != nil {
			return err
		}
		switch nt {
		case ondemand.Int64:
			x, err := v.Int64()
			if err != nil {
				return err
			}
			fmt.Fprintf(b, "i%d", x)
		case ondemand.Uint64:
			x, err := v.Uint64()
			if err != nil {
				return err
			}
			fmt.Fprintf(b, "u%d", x)
		case ondemand.Float64:
			x, err := v.Float64()
			if err != nil {
				return err
			}
			fmt.Fprintf(b, "d%x", math.Float64bits(x))
		case ondemand.BigInt:
			r, err := v.Raw()
			if err != nil {
				return err
			}
			trimmed := strings.TrimRight(string(r), " \t\n\r")
			fmt.Fprintf(b, "B%s", trimmed)
			digits := strings.TrimPrefix(trimmed, "-")
			if digits == "" || len(digits) > 1 && digits[0] == '0' {
				info.badBigInt = true
			}
		}
	case ondemand.TypeString:
		s, err := v.String()
		if err != nil {
			return err
		}
		fmt.Fprintf(b, "%q", s)
	case ondemand.TypeBool:
		x, err := v.Bool()
		if err != nil {
			return err
		}
		fmt.Fprint(b, x)
	case ondemand.TypeNull:
		x, err := v.IsNull()
		if err != nil {
			return err
		}
		if !x {
			return errors.New("not null")
		}
		b.WriteString("null")
	default:
		return errors.New("not a value")
	}
	return nil
}

// domWalk is fullWalk's canonical text through Parse and the DOM.
func domWalk(p *simdjson.Parser, in []byte) (string, error) {
	doc, err := p.Parse(in)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	var walk func(e simdjson.Element) error
	walk = func(e simdjson.Element) error {
		switch e.Type() {
		case simdjson.TypeObject:
			o, _ := e.Object()
			b.WriteByte('{')
			for k, v := range o.All() {
				fmt.Fprintf(&b, "%q:", k)
				if err := walk(v); err != nil {
					return err
				}
				b.WriteByte(',')
			}
			b.WriteByte('}')
		case simdjson.TypeArray:
			a, _ := e.Array()
			b.WriteByte('[')
			for _, v := range a.All() {
				if err := walk(v); err != nil {
					return err
				}
				b.WriteByte(',')
			}
			b.WriteByte(']')
		case simdjson.TypeInt64:
			x, _ := e.Int64()
			fmt.Fprintf(&b, "i%d", x)
		case simdjson.TypeUint64:
			x, _ := e.Uint64()
			fmt.Fprintf(&b, "u%d", x)
		case simdjson.TypeFloat64:
			x, _ := e.Float64()
			fmt.Fprintf(&b, "d%x", math.Float64bits(x))
		case simdjson.TypeBigInt:
			x, _ := e.BigInt()
			fmt.Fprintf(&b, "B%s", x)
		case simdjson.TypeString:
			x, _ := e.StringValue()
			fmt.Fprintf(&b, "%q", x)
		case simdjson.TypeBool:
			x, _ := e.Bool()
			fmt.Fprint(&b, x)
		case simdjson.TypeNull:
			b.WriteString("null")
		}
		return nil
	}
	err = walk(doc.Root())
	return b.String(), err
}

var bom = []byte{0xEF, 0xBB, 0xBF}

// knownDifference reports C++'s known differences between On-Demand and
// the DOM when one fails and the other does not. On-Demand alone fails on
// an exponent of more than 19 digits and by the root-buffer rule below;
// On-Demand alone succeeds on a root "falsX" (any fifth byte, read as
// false) and on an integer too long for 64 bits that it does not validate
// (read with Raw; see walkInfo.badBigInt).
func knownDifference(in []byte, info walkInfo, errOD, errDOM error) bool {
	if errOD != nil && errDOM == nil {
		return exponentTooLong(in) || rootBufferDifference(in)
	}
	root := bytes.TrimLeft(bytes.TrimPrefix(in, bom), " \t\n\r")
	return errOD == nil && errDOM != nil &&
		(bytes.HasPrefix(root, []byte("fals")) && !bytes.HasPrefix(root, []byte("false")) || info.badBigInt)
}

// rootBufferDifference reports C++'s root-buffer rule: C++ copies a root
// number and its trailing whitespace into a fixed buffer (21 bytes for an
// integer, 1083 for any number). Longer, reading it as an integer or float
// fails, and NumberType classifies it as BigInt, read raw (the oracle
// records "0" followed by 1083 or more spaces as ntype bigint, walk B0).
func rootBufferDifference(in []byte) bool {
	root := bytes.TrimLeft(bytes.TrimPrefix(in, bom), " \t\n\r")
	num := bytes.TrimRight(root, " \t\n\r")
	return len(root) > 21 && rootInteger.Match(num) ||
		len(root) > 1083 && rootNumber.Match(num)
}

// rootInteger and rootNumber match a document that is a single integer or
// number. rootNumber also matches a bare "-": past the 1083-byte buffer,
// C++'s check_if_integer accepts it as a big integer (oracle: "-" followed
// by 1083 spaces walks as B-).
var (
	rootInteger = regexp.MustCompile(`^-?[0-9]+$`)
	rootNumber  = regexp.MustCompile(`^-?[0-9]*(\.[0-9]+)?([eE][-+]?[0-9]+)?$`)
)

func exponentTooLong(in []byte) bool {
	n := 0
	for i := 0; i < len(in); i++ {
		switch c := in[i]; {
		case c == '"': // a string: its contents are not numbers
			for i++; i < len(in) && in[i] != '"'; i++ {
				if in[i] == '\\' {
					i++
				}
			}
			n = 0
		case c >= '0' && c <= '9':
			n++
		case c == 'e' || c == 'E':
			n = 0
			for i+1 < len(in) && (in[i+1] == '+' || in[i+1] == '-') {
				i++
			}
			for i+1 < len(in) && in[i+1] >= '0' && in[i+1] <= '9' {
				i++
				n++
			}
			if n > 19 {
				return true
			}
		default:
			n = 0
		}
	}
	return false
}

// agree checks that a full On-Demand walk gives what the DOM gives.
func agree(t *testing.T, od *ondemand.Parser, dom *simdjson.Parser, in []byte) {
	t.Helper()
	got, info, errOD := fullWalk(od, in)
	want, errDOM := domWalk(dom, in)
	switch {
	case (errOD == nil) != (errDOM == nil):
		if knownDifference(in, info, errOD, errDOM) {
			return
		}
		t.Errorf("%.80q: On-Demand err %v, DOM err %v", in, errOD, errDOM)
	case errOD == nil && got != want:
		// both succeed: only the root-buffer rule changes a value
		if rootBufferDifference(in) {
			return
		}
		t.Errorf("%.80q:\n On-Demand %.300s\n DOM       %.300s", in, got, want)
	}
}

func newDOMParser() *simdjson.Parser {
	return &simdjson.Parser{MaxDepth: math.MaxInt32, BigIntAsString: true}
}

// TestAgreesWithDOM walks every corpus file both ways.
func TestAgreesWithDOM(t *testing.T) {
	var files []string
	for _, dir := range []string{"jsonexamples", "jsonchecker"} {
		m, err := filepath.Glob(filepath.Join("../testdata", dir, "*.json"))
		if err != nil || len(m) == 0 {
			t.Fatalf("no corpus in testdata/%s: run `make testdata`", dir)
		}
		files = append(files, m...)
	}
	var od ondemand.Parser
	dom := newDOMParser()
	for _, f := range files {
		in, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		agree(t, &od, dom, in)
	}
}
