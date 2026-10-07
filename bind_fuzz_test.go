package simdjson

import (
	jsonv2 "encoding/json/v2"
	"testing"
)

// FuzzUnmarshal checks that Unmarshal agrees with encoding/json/v2, in
// values and error classes, for every type of the differential suite.
func FuzzUnmarshal(f *testing.F) {
	for _, in := range diffInputs {
		f.Add([]byte(in), uint8(0))
	}
	f.Fuzz(func(t *testing.T, in []byte, which uint8) {
		typ := diffTypes[int(which)%len(diffTypes)]
		for _, opts := range [][]Option{nil, {MatchCaseInsensitiveNames(true)}, {RejectUnknownMembers(true)}} {
			if msg := sameUnmarshal(in, typ, opts...); msg != "" {
				t.Fatalf("Unmarshal(%q): %s", in, msg)
			}
		}
	})
}

// FuzzMarshal checks that Marshal agrees with encoding/json/v2 on values
// that v2 decodes from the fuzzed input.
func FuzzMarshal(f *testing.F) {
	for _, in := range diffInputs {
		f.Add([]byte(in))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		var a any
		var o Outer
		for _, v := range []any{&a, &o} {
			if jsonv2.Unmarshal(in, v) != nil {
				continue
			}
			// Deterministic always: Go map order is random in both encoders.
			for _, opts := range [][]Option{{Deterministic(true)}, {Deterministic(true), FormatNilSliceAsNull(true), FormatNilMapAsNull(true)}} {
				if msg := sameMarshal(v, opts...); msg != "" {
					t.Fatalf("Marshal(from %q): %s", in, msg)
				}
			}
		}
	})
}

// TestConcurrentBinding runs Unmarshal and Marshal from many goroutines on
// fresh types, so the codec cache and pools are exercised under -race.
func TestConcurrentBinding(t *testing.T) {
	data := readTestdata(t, "jsonexamples", "twitter.json")
	type fresh struct {
		Statuses []struct {
			ID   uint64 `json:"id"`
			User struct {
				Name string `json:"name"`
			} `json:"user"`
		} `json:"statuses"`
	}
	done := make(chan error)
	for range 8 {
		go func() {
			var err error
			for range 20 {
				var v fresh
				if err = Unmarshal(data, &v); err != nil {
					break
				}
				if _, err = Marshal(&v); err != nil {
					break
				}
			}
			done <- err
		}()
	}
	for range 8 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}
