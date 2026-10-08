package ondemand_test

import (
	"errors"
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
