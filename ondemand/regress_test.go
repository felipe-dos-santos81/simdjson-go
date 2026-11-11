package ondemand_test

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	simdjson "simdjson-go"
	"simdjson-go/ondemand"
)

// calls runs every Document method that returns an error; each must report
// ErrOutOfOrderIteration on a dead Document, and none may panic.
func deadCalls(d *ondemand.Document) map[string]error {
	m := map[string]error{}
	rec := func(name string, err error) { m[name] = err }
	_, err := d.Get("a")
	rec("Get", err)
	_, err = d.FindNext("a")
	rec("FindNext", err)
	_, err = d.Object()
	rec("Object", err)
	_, err = d.Array()
	rec("Array", err)
	_, err = d.Value()
	rec("Value", err)
	_, err = d.Type()
	rec("Type", err)
	_, err = d.NumberType()
	rec("NumberType", err)
	_, err = d.Int64()
	rec("Int64", err)
	_, err = d.Uint64()
	rec("Uint64", err)
	_, err = d.Float64()
	rec("Float64", err)
	_, err = d.Bool()
	rec("Bool", err)
	_, err = d.IsNull()
	rec("IsNull", err)
	_, err = d.StringBytes()
	rec("StringBytes", err)
	_, err = d.String()
	rec("String", err)
	_, err = d.Raw()
	rec("Raw", err)
	_, err = d.AtPointer("/a")
	rec("AtPointer", err)
	_, err = d.AtPointer("")
	rec("AtPointer(empty)", err)
	return m
}

func checkDead(t *testing.T, d *ondemand.Document) {
	t.Helper()
	for name, err := range deadCalls(d) {
		if !errors.Is(err, simdjson.ErrOutOfOrderIteration) {
			t.Errorf("%s on a dead document: got %v, want ErrOutOfOrderIteration", name, err)
		}
	}
	d.Rewind()
	if d.AtEnd() {
		t.Error("AtEnd on a dead document is true")
	}
}

func TestMisuseRegressions(t *testing.T) {
	t.Run("wraparound loop terminates", func(t *testing.T) {
		done := make(chan error, 1)
		go func() {
			var p ondemand.Parser
			d, err := p.Iterate([]byte(`[{"c":[]]},2]`))
			if err != nil {
				done <- err
				return
			}
			a, err := d.Array()
			if err != nil {
				done <- err
				return
			}
			v, err := a.At(0)
			if err != nil {
				done <- err
				return
			}
			if _, err := v.Object(); err != nil {
				done <- err
				return
			}
			_, err = d.Get("")
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Error("want an error")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Get spins forever")
		}
	})

	t.Run("String out of range", func(t *testing.T) {
		if s := ondemand.Type(99).String(); s != "Type(99)" {
			t.Errorf("Type(99).String() = %q", s)
		}
		if s := ondemand.NumberType(9).String(); s != "NumberType(9)" {
			t.Errorf("NumberType(9).String() = %q", s)
		}
	})

	t.Run("failed Iterate", func(t *testing.T) {
		var p ondemand.Parser
		d, err := p.Iterate([]byte(`[1,2,3,4,5,6,7,8]`))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Iterate([]byte("[\"\xff")); err == nil {
			t.Fatal("want a stage 1 error")
		}
		checkDead(t, d)
	})

	t.Run("zero Document", func(t *testing.T) {
		checkDead(t, new(ondemand.Document))
	})

	t.Run("Array.At on a bad handle", func(t *testing.T) {
		if _, err := (ondemand.Array{}).At(3); !errors.Is(err, simdjson.ErrOutOfOrderIteration) {
			t.Errorf("zero Array: got %v", err)
		}
		var p ondemand.Parser
		d, err := p.Iterate([]byte(`[1,2,3,4,5]`))
		if err != nil {
			t.Fatal(err)
		}
		a, err := d.Array()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Iterate([]byte(`[1,2,3,4,5]`)); err != nil {
			t.Fatal(err)
		}
		if _, err := a.At(3); !errors.Is(err, simdjson.ErrOutOfOrderIteration) {
			t.Errorf("stale Array: got %v", err)
		}
	})
}

// TestAtPointerDeep follows a 100,000-segment pointer: AtPointer must loop,
// not recurse or copy the pointer per segment.
func TestAtPointerDeep(t *testing.T) {
	const depth = 100_000
	for _, c := range []struct{ open, close, seg string }{
		{`{"a":`, `}`, "/a"},
		{`[`, `]`, "/0"},
	} {
		doc := strings.Repeat(c.open, depth) + "7" + strings.Repeat(c.close, depth)
		ptr := strings.Repeat(c.seg, depth)
		p := ondemand.Parser{MaxDepth: depth + 1}
		d, err := p.Iterate([]byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		v, err := d.AtPointer(ptr)
		if err != nil {
			t.Fatalf("%s: %v", c.seg, err)
		}
		if el := time.Since(start); el > time.Second {
			t.Errorf("%s: AtPointer took %v", c.seg, el)
		}
		if n, err := v.Int64(); n != 7 || err != nil {
			t.Errorf("%s: leaf = %d, %v; want 7", c.seg, n, err)
		}
	}
}

// TestMaxDepth: which reads Parser.MaxDepth refuses, and the document they
// leave. The expected values come from C++ v5.1.1 built with
// SIMDJSON_DEVELOPMENT_CHECKS (oracle.cpp, with parser.allocate(len,
// MaxDepth)): the recorded oracle is the release build, which has no limit.
func TestMaxDepth(t *testing.T) {
	arr := func(n int, in string) string { return strings.Repeat("[", n) + in + strings.Repeat("]", n) }
	obj := func(n int, in string) string { return strings.Repeat(`{"a":`, n) + in + strings.Repeat("}", n) }
	ptr := func(seg string, n int) string { return strings.Repeat(seg, n) }
	for _, c := range []struct {
		max    int
		in     string
		script []string
		want   string
	}{
		// The root is at depth 1: an array or object starting at depth
		// MaxDepth fails.
		{16, arr(15, "1"), []string{"pint " + ptr("/0", 15)}, "i1"},
		{16, arr(16, "1"), []string{"pint " + ptr("/0", 16)}, "!4"},
		{16, arr(17, "1"), []string{"pint " + ptr("/0", 17)}, "!4"},
		{16, obj(15, "1"), []string{"pint " + ptr("/a", 15)}, "i1"},
		{16, obj(16, "1"), []string{"pint " + ptr("/a", 16)}, "!4"},
		// Empty ones too.
		{16, arr(14, "[]"), []string{"ptr " + ptr("/0", 14), "arr", "count"}, "n0"},
		{16, arr(15, "[]"), []string{"ptr " + ptr("/0", 15), "arr", "count"}, "!4"},
		{16, arr(14, "{}"), []string{"ptr " + ptr("/0", 14), "obj", "count"}, "n0"},
		{16, arr(15, "{}"), []string{"ptr " + ptr("/0", 15), "obj", "count"}, "!4"},
		// A value skipped without being started does not count.
		{16, "[" + arr(20, "1") + ",2]", []string{"arr", "at 1", "int64"}, "i2"},
		// 0 means 1024.
		{0, arr(1023, "1"), []string{"pint " + ptr("/0", 1023)}, "i1"},
		{0, arr(1024, "1"), []string{"pint " + ptr("/0", 1024)}, "!4"},
		// The error is kept: iterating after a Rewind reports it and
		// abandons the document (the error counts as element 0, then
		// AtPointer reports it again).
		{16, "[" + arr(20, "1") + ",2]", []string{"pint " + ptr("/0", 16), "pint /1", "pint /1"}, "!4 !19 !4"},
		// The error alone does not abandon the document, even once Raw has
		// taken its cursor to depth 0.
		{16, `{"a":` + arr(20, "1") + `,"b":2}`, []string{"pint /a" + ptr("/0", 15), "raw", "pint /b"},
			`!4 r{"a":` + arr(20, "1") + ` i2`},
	} {
		p := ondemand.Parser{MaxDepth: c.max}
		if got := runScript(&p, []byte(c.in), c.script); got != c.want {
			t.Errorf("MaxDepth %d, %.40q %.40q: got %.80s, want %.80s", c.max, c.in, c.script, got, c.want)
		}
	}

	// A stream document obeys the limit, and the error ends the stream there.
	p := ondemand.Parser{MaxDepth: 16}
	deep := arr(16, "1")
	want := "@0 h5b315d [i1,] @4 h" + hex.EncodeToString([]byte(deep)) + " x4 @4 !4"
	if got := streamOut(nil, &p, "ondemand", []byte("[1]\n"+deep+"\n[2]\n"), simdjson.Whitespace, []string{"walk"}); got != want {
		t.Errorf("stream: got %s, want %s", got, want)
	}
}

// TestNoAllocs: NumberType on a long integer and AtPointer without '~'
// escapes do not allocate.
func TestNoAllocs(t *testing.T) {
	var p ondemand.Parser
	d, err := p.Iterate([]byte(`{"a":[0,{"b":` + strings.Repeat("9", 100) + `}]}`))
	if err != nil {
		t.Fatal(err)
	}
	n := testing.AllocsPerRun(100, func() {
		v, err := d.AtPointer("/a/1/b")
		if err != nil {
			t.Fatal(err)
		}
		if nt, err := v.NumberType(); nt != ondemand.BigInt || err != nil {
			t.Fatalf("NumberType = %v, %v", nt, err)
		}
	})
	if n != 0 {
		t.Errorf("%v allocs per run", n)
	}
}

// TestResumeWrongType: looking a field up in an array that was already
// started is ErrIncorrectType and leaves the document readable (C++
// resumes it as an object without checking and then misreads).
func TestResumeWrongType(t *testing.T) {
	var p ondemand.Parser
	doc := must(p.Iterate([]byte(`["a",1]`)))
	must(doc.Array())
	if _, err := doc.Get("a"); !errors.Is(err, simdjson.ErrIncorrectType) {
		t.Fatalf("Document.Get after Array: %v, want ErrIncorrectType", err)
	}
	if n, err := must(doc.Array()).Count(); err != nil || n != 2 {
		t.Fatalf("Count after the failed Get: %d, %v", n, err)
	}

	doc = must(p.Iterate([]byte(`{"v":[1]}`)))
	v := must(doc.Get("v"))
	must(v.Array())
	for _, f := range []func(string) (ondemand.Value, error){v.Get, v.FindNext} {
		if _, err := f("a"); !errors.Is(err, simdjson.ErrIncorrectType) {
			t.Fatalf("Value lookup after Array: %v, want ErrIncorrectType", err)
		}
	}
	doc.Rewind()
	if n, err := must(must(doc.Get("v")).Array()).Count(); err != nil || n != 1 {
		t.Fatalf("Count after the failed lookups and Rewind: %d, %v", n, err)
	}
}
