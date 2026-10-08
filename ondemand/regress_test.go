package ondemand_test

import (
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
		var p ondemand.Parser
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
