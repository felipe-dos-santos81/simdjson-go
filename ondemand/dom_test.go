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

// fullWalk reads the whole document with On-Demand into a canonical text,
// and checks nothing follows it. Any error means the document is invalid.
func fullWalk(p *ondemand.Parser, in []byte) (string, error) {
	doc, err := p.Iterate(in)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := odWalk(doc, &b); err != nil {
		return "", err
	}
	if !doc.AtEnd() {
		return "", errors.New("trailing content")
	}
	return b.String(), nil
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

func odWalk(v odReader, b *strings.Builder) error {
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
			if err := odWalk(f.Value(), b); err != nil {
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
			if err := odWalk(e, b); err != nil {
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
			fmt.Fprintf(b, "B%s", strings.TrimRight(string(r), " \t\n\r"))
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

// knownDifference reports C++'s known differences between On-Demand and
// the DOM, given the input and On-Demand's walk (empty if it failed):
// On-Demand rejects an exponent of more than 19 digits, reads a root
// "falsX" (any fifth byte) as false, does not validate an integer too long
// for 64 bits (read with Raw), such as one with leading zeros, and rejects
// a root number whose text and trailing whitespace exceed the buffer C++
// copies them to: 21 bytes for an integer, 1083 for any number.
func knownDifference(in []byte, walk string) bool {
	root := bytes.TrimLeft(in, " \t\n\r")
	num := bytes.TrimRight(root, " \t\n\r")
	return exponentTooLong(in) ||
		bytes.HasPrefix(root, []byte("fals")) && !bytes.HasPrefix(root, []byte("false")) ||
		badBigInt.MatchString(walk) ||
		len(root) > 21 && rootInteger.Match(num) ||
		len(root) > 1083 && rootNumber.Match(num)
}

// rootInteger and rootNumber match a document that is a single integer or
// number.
var (
	rootInteger = regexp.MustCompile(`^-?[0-9]+$`)
	rootNumber  = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?([eE][-+]?[0-9]+)?$`)
)

// badBigInt matches, in a walk, a big integer the DOM would reject.
var badBigInt = regexp.MustCompile(`B-?0[0-9]`)

func exponentTooLong(in []byte) bool {
	n := 0
	for i := 0; i < len(in); i++ {
		switch c := in[i]; {
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
	got, errOD := fullWalk(od, in)
	want, errDOM := domWalk(dom, in)
	switch {
	case (errOD == nil) != (errDOM == nil):
		if knownDifference(in, got) {
			return
		}
		t.Errorf("%.80q: On-Demand err %v, DOM err %v", in, errOD, errDOM)
	case errOD == nil && got != want:
		if knownDifference(in, got) {
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
