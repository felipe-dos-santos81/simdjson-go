package ondemand_test

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"

	"simdjson-go/internal/jsonerr"
	"simdjson-go/ondemand"
)

// The C++ oracle (scripts/ondemand-oracle) ran each case's script through
// C++ simdjson ondemand and recorded the output; runScript must reproduce
// it. runScript mirrors oracle.cpp's interpreter line for line.

type oracleCase struct {
	Doc    string   `json:"doc,omitempty"`  // hex
	File   string   `json:"file,omitempty"` // relative to the repository root
	Script []string `json:"script"`
	Out    string   `json:"out"`
}

func TestOracle(t *testing.T) {
	f, err := os.Open("../testdata/ondemand/oracle.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<26)
	var p ondemand.Parser
	n, failed := 0, 0
	for sc.Scan() {
		var c oracleCase
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		in, err := c.input()
		if err != nil {
			t.Fatal(err)
		}
		n++
		if got := runScript(&p, in, c.Script); got != c.Out {
			failed++
			if failed <= 20 {
				t.Errorf("case %d %s %q %q:\n got  %.300s\n want %.300s", n, c.File, abbrev(in), c.Script, got, c.Out)
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if failed > 0 {
		t.Errorf("%d of %d cases differ from C++", failed, n)
	}
	t.Logf("%d cases", n)
}

func abbrev(b []byte) string {
	if len(b) > 80 {
		return string(b[:80]) + "…"
	}
	return string(b)
}

func (c oracleCase) input() ([]byte, error) {
	if c.File != "" {
		return os.ReadFile("../" + c.File)
	}
	return hex.DecodeString(c.Doc)
}

// cppCode is each error's C++ error_code value (include/simdjson/error.h).
var cppCode = map[error]int{
	jsonerr.ErrCapacity: 1, jsonerr.ErrTape: 3, jsonerr.ErrDepth: 4, jsonerr.ErrString: 5,
	jsonerr.ErrTAtom: 6, jsonerr.ErrFAtom: 7, jsonerr.ErrNAtom: 8, jsonerr.ErrNumber: 9,
	jsonerr.ErrBigInt: 10, jsonerr.ErrUTF8: 11, jsonerr.ErrEmpty: 13, jsonerr.ErrUnescapedChars: 14,
	jsonerr.ErrUnclosedString: 15, jsonerr.ErrIncorrectType: 17, jsonerr.ErrNumberOutOfRange: 18,
	jsonerr.ErrIndexOutOfBounds: 19, jsonerr.ErrNoSuchField: 20, jsonerr.ErrInvalidJSONPointer: 22,
	jsonerr.ErrOutOfOrderIteration: 26, jsonerr.ErrIncompleteArrayOrObject: 28,
	jsonerr.ErrScalarDocumentAsValue: 29, jsonerr.ErrTrailingContent: 31,
}

func errToken(err error) string {
	for e, code := range cppCode {
		if errors.Is(err, e) {
			return "!" + strconv.Itoa(code)
		}
	}
	return "!?" + err.Error()
}

func esc(s []byte) string {
	var b strings.Builder
	for _, c := range s {
		if c > 0x20 && c < 0x7f && c != '\\' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "\\x%02x", c)
		}
	}
	return b.String()
}

func f64(f float64) string { return fmt.Sprintf("d%016x", math.Float64bits(f)) }

// reader is what ondemand.Document and ondemand.Value have in common.
type reader interface {
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

func walk(v reader, out *strings.Builder) error {
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
		out.WriteByte('{')
		for f, err := range o.All() {
			if err != nil {
				return err
			}
			out.WriteString(esc(f.RawKey()))
			out.WriteByte(':')
			if err := walk(f.Value(), out); err != nil {
				return err
			}
			out.WriteByte(',')
		}
		out.WriteByte('}')
	case ondemand.TypeArray:
		a, err := v.Array()
		if err != nil {
			return err
		}
		out.WriteByte('[')
		for e, err := range a.All() {
			if err != nil {
				return err
			}
			if err := walk(e, out); err != nil {
				return err
			}
			out.WriteByte(',')
		}
		out.WriteByte(']')
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
			fmt.Fprintf(out, "i%d", x)
		case ondemand.Uint64:
			x, err := v.Uint64()
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "u%d", x)
		case ondemand.Float64:
			x, err := v.Float64()
			if err != nil {
				return err
			}
			out.WriteString(f64(x))
		case ondemand.BigInt:
			r, err := v.Raw()
			if err != nil {
				return err
			}
			out.WriteString("B" + esc([]byte(strings.TrimRight(string(r), " \t\n\r"))))
		}
	case ondemand.TypeString:
		s, err := v.String()
		if err != nil {
			return err
		}
		out.WriteString("s" + esc([]byte(s)))
	case ondemand.TypeBool:
		b, err := v.Bool()
		if err != nil {
			return err
		}
		out.WriteString(strconv.FormatBool(b))
	case ondemand.TypeNull:
		n, err := v.IsNull()
		if err != nil {
			return err
		}
		out.WriteString(map[bool]string{true: "null", false: "notnull"}[n])
	default:
		out.WriteString("?")
	}
	return nil
}

func read(v reader, op string) (string, error) {
	switch op {
	case "int64":
		x, err := v.Int64()
		return "i" + strconv.FormatInt(x, 10), err
	case "uint64":
		x, err := v.Uint64()
		return "u" + strconv.FormatUint(x, 10), err
	case "double":
		x, err := v.Float64()
		return f64(x), err
	case "bool":
		x, err := v.Bool()
		return strconv.FormatBool(x), err
	case "null":
		x, err := v.IsNull()
		return map[bool]string{true: "null", false: "notnull"}[x], err
	case "string":
		x, err := v.String()
		return "s" + esc([]byte(x)), err
	case "type":
		x, err := v.Type()
		return x.String(), err
	case "ntype":
		x, err := v.NumberType()
		if err != nil {
			return "", err
		}
		return x.String(), nil
	case "raw":
		x, err := v.Raw()
		return "r" + esc(x), err
	case "walk":
		var b strings.Builder
		err := walk(v, &b)
		return b.String(), err
	}
	return "", fmt.Errorf("unknown op %q", op)
}

type slotKind int

const (
	kDoc slotKind = iota
	kValue
	kObject
	kArray
)

type slot struct {
	k slotKind
	v ondemand.Value
	o ondemand.Object
	a ondemand.Array
}

func runScript(p *ondemand.Parser, in []byte, script []string) string {
	var toks []string
	doc, err := p.Iterate(in)
	if err == nil {
		err = runOps(doc, script, &toks)
	}
	if err != nil {
		toks = append(toks, errToken(err))
	}
	out := strings.Join(toks, " ")
	if len(out) > 2000 {
		h := fnv.New64a()
		h.Write([]byte(out))
		out = fmt.Sprintf("#%016x:%d", h.Sum64(), len(out))
	}
	return out
}

func runOps(doc *ondemand.Document, script []string, toks *[]string) error {
	st := []slot{{k: kDoc}}
	for _, line := range script {
		op, arg, _ := strings.Cut(line, " ")
		top := &st[len(st)-1]
		switch op {
		case "get", "find":
			var v ondemand.Value
			var err error
			ordered := op == "find"
			switch top.k {
			case kDoc:
				if ordered {
					v, err = doc.FindNext(arg)
				} else {
					v, err = doc.Get(arg)
				}
			case kValue:
				if ordered {
					v, err = top.v.FindNext(arg)
				} else {
					v, err = top.v.Get(arg)
				}
			case kObject:
				if ordered {
					v, err = top.o.FindNext(arg)
				} else {
					v, err = top.o.Get(arg)
				}
			default:
				return jsonerr.ErrIncorrectType
			}
			if err != nil {
				return err
			}
			st = append(st, slot{k: kValue, v: v})
		case "obj":
			var o ondemand.Object
			var err error
			switch top.k {
			case kDoc:
				o, err = doc.Object()
			case kValue:
				o, err = top.v.Object()
			default:
				return jsonerr.ErrIncorrectType
			}
			if err != nil {
				return err
			}
			if top.k == kDoc {
				st = append(st, slot{k: kObject, o: o})
			} else {
				*top = slot{k: kObject, o: o}
			}
		case "arr":
			var a ondemand.Array
			var err error
			switch top.k {
			case kDoc:
				a, err = doc.Array()
			case kValue:
				a, err = top.v.Array()
			default:
				return jsonerr.ErrIncorrectType
			}
			if err != nil {
				return err
			}
			if top.k == kDoc {
				st = append(st, slot{k: kArray, a: a})
			} else {
				*top = slot{k: kArray, a: a}
			}
		case "val":
			v, err := doc.Value()
			if err != nil {
				return err
			}
			st = append(st, slot{k: kValue, v: v})
		case "at":
			if top.k != kArray {
				return jsonerr.ErrIncorrectType
			}
			i, _ := strconv.Atoi(arg)
			v, err := top.a.At(i)
			if err != nil {
				return err
			}
			st = append(st, slot{k: kValue, v: v})
		case "ptr":
			var v ondemand.Value
			var err error
			switch top.k {
			case kDoc:
				v, err = doc.AtPointer(arg)
			case kValue:
				v, err = top.v.AtPointer(arg)
			case kObject:
				v, err = top.o.AtPointer(arg)
			case kArray:
				v, err = top.a.AtPointer(arg)
			}
			if err != nil {
				return err
			}
			st = append(st, slot{k: kValue, v: v})
		case "pop":
			if len(st) > 1 {
				st = st[:len(st)-1]
			}
		case "count":
			var n int
			var err error
			switch top.k {
			case kObject:
				n, err = top.o.Count()
			case kArray:
				n, err = top.a.Count()
			default:
				return jsonerr.ErrIncorrectType
			}
			if err != nil {
				return err
			}
			*toks = append(*toks, "n"+strconv.Itoa(n))
		case "reset":
			var err error
			switch top.k {
			case kObject:
				err = top.o.Reset()
			case kArray:
				err = top.a.Reset()
			default:
				return jsonerr.ErrIncorrectType
			}
			if err != nil {
				return err
			}
			*toks = append(*toks, "ok")
		case "rewind":
			doc.Rewind()
			st = st[:1]
		case "keys":
			if top.k != kObject {
				return jsonerr.ErrIncorrectType
			}
			var b strings.Builder
			for f, err := range top.o.All() {
				if err != nil {
					return err
				}
				b.WriteString(esc(f.RawKey()) + ",")
			}
			*toks = append(*toks, "k"+b.String())
			st = st[:len(st)-1]
		case "each":
			if top.k != kArray {
				return jsonerr.ErrIncorrectType
			}
			n := 0
			for _, err := range top.a.All() {
				if err != nil {
					return err
				}
				n++
			}
			*toks = append(*toks, "e"+strconv.Itoa(n))
			st = st[:len(st)-1]
		default:
			var s string
			var err error
			switch {
			case top.k == kObject && op == "raw":
				var r []byte
				r, err = top.o.Raw()
				s = "r" + esc(r)
			case top.k == kArray && op == "raw":
				var r []byte
				r, err = top.a.Raw()
				s = "r" + esc(r)
			case top.k == kDoc:
				s, err = read(doc, op)
			case top.k == kValue:
				s, err = read(top.v, op)
			default:
				err = jsonerr.ErrIncorrectType
			}
			if err != nil {
				return err
			}
			*toks = append(*toks, s)
			if len(st) > 1 {
				st = st[:len(st)-1]
			}
		}
	}
	return nil
}
