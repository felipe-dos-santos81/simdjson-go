# simdjson-go Data Binding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `Unmarshal`, `Marshal`, `MarshalAppend` and `MarshalIndent` to package `simdjson`. They must behave like `encoding/json/v2` under its default options, and decode on the existing simdjson tape.

**Architecture:**
- An unexported binding mode in stage 2 records what v2's rules need: input offsets and repeated object names. It also turns overflowing floats into ±Inf and counts empty containers toward the depth limit.
- Per-type decoders and encoders over `reflect.Value` are built once and cached. They follow v2's struct-field rules, method precedence and output format.
- Every behaviour is checked against the installed `encoding/json/v2`, in a differential suite and in fuzzers.

**Tech Stack:** Go 1.27 standard library only (`encoding/json/v2` and `encoding/json/jsontext` serve as the test oracle and supply the error types).

**Spec:** `docs/superpowers/specs/2026-10-07-simdjson-go-binding-design.md`. Read it with this plan. Where they disagree, the spec wins.

**Provenance:** every code block below is copied from a prototype that passed the whole plan:
- the differential suite;
- fuzzing against v2, 60 s per target;
- the race test;
- the C++ oracle (72,718 cases, 0 failures);
- `make check`.

The tasks were also replayed in order on a copy of `main`. Each built and passed its tests on its own, and the final tree matched the prototype byte for byte. Transcribe the blocks exactly. Apply each `diff` block with `git apply` from the repo root: save it to a file outside the repo, run `git apply --check FILE`, then `git apply FILE`.

## Global Constraints

- Go 1.27. Standard library only, in the library and its tests; no cgo.
- `encoding/json/v2` from the installed toolchain is the oracle, called in-process. Never loosen `sameUnmarshal`/`sameMarshal` to make a test pass.
- The one allowed difference from v2: on input that is not valid JSON, we always report a `*jsontext.SyntacticError`, where v2 may report a `*json.SemanticError` it meets first.
- Error types are v2's: `*jsontext.SyntacticError` and `*json.SemanticError`, wrapping `jsontext.ErrDuplicateName` and `json.ErrUnknownName` where v2 does.
- Speed (NEON build, Apple M3 Max):
  - `Unmarshal` into typed structs is at least 1.4× faster than v2 on `twitter.json`, 1.1× on `citm_catalog.json` and 1.5× on `canada.json`.
  - `Marshal` is no slower than v2 on all three files.
  - Plain `Parser.Parse` is at most 3% slower than before, in geometric mean over `BenchmarkParse`.
- The non-binding tape stays as C++ simdjson defines it. Every binding-only step in stage 2 sits behind `if b.binding`.
- Files adapted from Go source say so in a header and are covered by `LICENSE-GO`.
- Work on a branch, never on `main`. Commit messages end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- `make check` must pass before each commit.

## Review Focus

Each case below is pinned by a test in Task 2's `bind_test.go`:

- **Names written with escapes.** `{"a":5}` fills field `a`, and `{"a":1,"a":2}` is a duplicate. Names are compared after unescaping, as v2 does. Pinned by the `diffInputs` entries.
- **Objects with more than eight names.** Above eight names, the duplicate check switches to a hash table, and a repeat far from its first occurrence must still be found. Pinned by `wideObject` in `TestDeepAndWide`.
- **Decoding into values that already hold data.** Slices with spare capacity and stale elements, maps with entries, set pointers and fields, and interfaces holding values must all end up exactly as v2 leaves them. Pinned by `TestUnmarshalIntoExisting`.
- **Self-referential types.** A struct that contains slices, pointers and maps of itself must build its codecs without deadlock or infinite recursion. Pinned by `TestRecursiveTypes`.
- **Back-to-back calls on pooled parsers.** Duplicates, offsets or ±Inf state from one document must not leak into the next. Pinned by `TestUnmarshalReusesParsers`.

---

## File Structure

| File | Task | Responsibility |
|---|---|---|
| `parser.go`, `stage2.go`, `numbers.go` (modify) | 1 | Binding mode: `Parser.binding`, `Document.offs`, `Document.dups`, ±Inf floats, empty containers count toward depth |
| `binding_test.go` | 1 | Unit test of binding mode |
| `options.go` | 2 | `Option` and its five constructors |
| `typeplan.go` | 2 | Codec cache, method detection, tag parsing, struct field plans (adapted from v2's `fields.go`, `fold.go`) |
| `strcache.go` | 2 | String cache (adapted from v2's `intern.go`) |
| `decode.go` | 2 | `Unmarshal`, the pooled `binder`, default decoders, duplicate and error helpers |
| `encode.go` | 2 | `Marshal`, `MarshalAppend`, default encoders, v2 float format and quoting |
| `methods.go` | 2 | `MarshalJSON`/`UnmarshalJSON`/text-method codecs |
| `time.go` | 2 | `time.Time`, `time.Duration` (adapted from v2's `arshal_time.go`) |
| `serialize.go` (modify) | 2 | `appendJSON` hook that writes numbers verbatim |
| `bind_test.go` | 2 | Differential suite against v2 |
| `indent.go`, `indent_test.go` | 3 | `MarshalIndent` |
| `bind_fuzz_test.go` | 4 | `FuzzUnmarshal`, `FuzzMarshal`, concurrency test |
| `Makefile` (modify) | 4 | `test-race` target, part of `check` |
| `bind_bench_test.go` | 5 | Benchmarks beside v2's, typed corpus structs |
| `README.md`, `AGENTS.md`, `NOTICE` (modify) | 5 | Documentation and attribution |

---

### Task 1: Binding mode in stage 2

Stage 2 gains an unexported mode for `Unmarshal`. Plain `Parse` (`binding` false) must produce exactly the tape it does today.

**Files:**
- Modify: `parser.go`, `stage2.go`, `numbers.go`
- Test: `binding_test.go`

**Interfaces:**
- Consumes: the existing `Parser`, `Document`, `builder`, `Element{doc, i}`, `Element.rawString()`, `Element.Float64()`, `ErrDepth`, `ErrNumber`.
- Produces (used by Task 2):
  - `Parser.binding bool`.
  - `Document.offs []uint32`: the input offset of each value, name and closing-bracket tape word.
  - `Document.dups []uint32`: sorted tape indices of each object's first repeated name.
  - In binding mode, overflowing floats are ±Inf (tag `d`), and empty `[]`/`{}` count toward `MaxDepth`.

- [ ] **Step 1: Record the parsing baseline** (spec §1 criterion 4), before changing anything:

```sh
make bench neon=1 bench='BenchmarkParse/' count=6 out=/tmp/parse-before.txt
```

- [ ] **Step 2: Write the failing test**

```go
package simdjson

import (
	"errors"
	"math"
	"strings"
	"testing"
)

// TestBindingMode checks what stage 2 records for Unmarshal: input offsets,
// the first repeated name of each object, ±Inf for overflowing floats, and
// empty containers counted toward the depth limit.
func TestBindingMode(t *testing.T) {
	in := `{"a":1,"b":{"c":1,"c":2,"c":3},"a":-3e400,"d":[],"e":{}}`
	p := Parser{MaxDepth: 10, binding: true}
	doc, err := p.Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, i := range doc.dups {
		k := Element{doc, int(i)}
		names = append(names, string(k.rawString()))
		if off := int(doc.offs[i]); in[off] != '"' || !strings.HasPrefix(in[off+1:], names[len(names)-1]) {
			t.Errorf("offs[%d] = %d, not at name %q", i, off, names[len(names)-1])
		}
	}
	if got := strings.Join(names, ","); got != "c,a" {
		t.Errorf("dups name %s, want c,a (inner object first)", got)
	}
	inner := Element{doc, int(doc.dups[0])}
	if off := int(doc.offs[inner.i]); off != strings.Index(in, `"c":2`) {
		t.Errorf("first repeated c at offset %d, want %d", off, strings.Index(in, `"c":2`))
	}
	last := Element{doc, int(doc.dups[1]) + 1}
	if f, err := last.Float64(); err != nil || !math.IsInf(f, -1) {
		t.Errorf("-3e400 = %v, %v; want -Inf", f, err)
	}
	if off := int(doc.offs[last.i]); in[off:off+7] != "-3e400," {
		t.Errorf("offs of -3e400 = %d", off)
	}

	// The same input outside binding mode: an error, and nothing recorded.
	plain := Parser{MaxDepth: 10}
	if _, err := plain.Parse([]byte(in)); !errors.Is(err, ErrNumber) {
		t.Errorf("plain Parse = %v, want ErrNumber", err)
	}
	if doc, err := plain.Parse([]byte(`{"a":1,"a":2}`)); err != nil || doc.dups != nil || doc.offs != nil {
		t.Errorf("plain Parse recorded dups %v, offs %v (err %v)", doc.dups, doc.offs, err)
	}

	// Empty containers count toward the depth only in binding mode.
	for _, tc := range []struct {
		binding bool
		in      string
		want    error
	}{
		{false, `[[]]`, nil}, {true, `[[]]`, ErrDepth}, {true, `[]`, nil}, {true, `[{}]`, ErrDepth},
	} {
		p := Parser{MaxDepth: 2, binding: tc.binding}
		if _, err := p.Parse([]byte(tc.in)); !errors.Is(err, tc.want) {
			t.Errorf("binding=%v Parse(%s) = %v, want %v", tc.binding, tc.in, err, tc.want)
		}
	}
}
```

- [ ] **Step 3: Run it to make sure it fails**

Run: `go test -run TestBindingMode .`
Expected: build failure, `unknown field binding in struct literal of type Parser`.

- [ ] **Step 4: Implement binding mode**

```diff
--- a/parser.go
+++ b/parser.go
@@ -34,6 +34,8 @@
 type Document struct {
 	tape    []uint64
 	strings []byte
+	offs    []uint32 // binding mode only: input offset per tape index (see builder.mark)
+	dups    []uint32 // binding mode only: sorted tape indices of each object's first repeated name
 }
 
 // Parser parses JSON documents. The zero value is ready to use. A Parser
@@ -48,9 +50,17 @@
 	// TypeBigInt (their raw digits) instead of failing with ErrBigInt.
 	BigIntAsString bool
 
+	// binding selects the encoding/json/v2 behaviour Unmarshal needs: input
+	// offsets (Document.offs) and repeated object names (Document.dups) are
+	// recorded, floats that overflow parse as ±Inf, and empty arrays and
+	// objects count toward MaxDepth.
+	binding bool
+
 	indices []uint32
 	stack   []scope
 	doc     Document
+	keys    []nameKey // binding mode scratch (builder.checkNames)
+	seen    []uint32  // binding mode scratch (builder.checkNames)
 }
 
 // Parse parses b. The returned Document is valid until the next call to
@@ -74,12 +84,24 @@
 		stack:          p.stack[:0],
 		maxDepth:       p.MaxDepth,
 		bigIntAsString: p.BigIntAsString,
+		binding:        p.binding,
+	}
+	if p.binding {
+		bd.offs = slices.Grow(p.doc.offs[:0], cap(bd.tape))[:cap(bd.tape)]
+		bd.dups = p.doc.dups[:0]
+		bd.keys, bd.seen = p.keys[:0], p.seen
 	}
 	if bd.maxDepth <= 0 {
 		bd.maxDepth = defaultMaxDepth
 	}
 	err = bd.walk()
 	p.doc.tape, p.doc.strings, p.stack = bd.tape, bd.strs, bd.stack
+	if p.binding {
+		p.doc.offs = bd.offs[:len(bd.tape)]
+		slices.Sort(bd.dups) // objects close inner first
+		p.doc.dups = bd.dups
+		p.keys, p.seen = bd.keys, bd.seen
+	}
 	if err != nil {
 		return nil, err
 	}
```

```diff
--- a/stage2.go
+++ b/stage2.go
@@ -1,9 +1,12 @@
 package simdjson
 
+import "encoding/binary"
+
 // scope is an open array or object.
 type scope struct {
 	tapeIndex uint32 // tape index of the opening word, written when the scope closes
 	count     uint32 // number of elements (arrays) or fields (objects)
+	keyStart  uint32 // binding mode: where this object's names start in builder.keys
 	open      byte   // '[' or '{', which is also the scope's tape tag
 }
 
@@ -19,6 +22,28 @@
 	stack          []scope
 	maxDepth       int
 	bigIntAsString bool
+	binding        bool      // see Parser.binding
+	offs           []uint32  // binding mode: offs[i] is the input offset behind tape word i
+	dups           []uint32  // binding mode: first repeated name per object (see checkNames)
+	keys           []nameKey // binding mode: names of the open objects, innermost last
+	seen           []uint32  // scratch for checkNames: hash table of keys indices + 1
+}
+
+// nameKey is an object name seen in binding mode: its tape index and hash.
+type nameKey struct {
+	i    uint32
+	hash uint64
+}
+
+// mark records, in binding mode, that the tape word written next starts at
+// input offset off. Only value, key and closing-bracket words are marked.
+func (b *builder) mark(off int) { b.markAt(len(b.tape), off) }
+
+func (b *builder) markAt(i, off int) {
+	if i >= len(b.offs) {
+		b.offs = append(b.offs, make([]uint32, i+1-len(b.offs)+64)...)
+	}
+	b.offs[i] = uint32(off)
 }
 
 // isStructuralOrSpace is C++ structural_or_whitespace: the bytes that may
@@ -71,9 +96,15 @@
 		return ErrTape
 	}
 	b.stack[len(b.stack)-1].count++
+	if b.binding {
+		b.mark(off)
+	}
 	if err = b.str(off); err != nil {
 		return err
 	}
+	if b.binding {
+		b.addKey()
+	}
 
 objectField:
 	if c, _ = b.advance(); c != ':' {
@@ -83,17 +114,27 @@
 	goto value
 
 objectContinue:
-	switch c, _ = b.advance(); c {
+	switch c, off = b.advance(); c {
 	case ',':
 		b.stack[len(b.stack)-1].count++
 		if c, off = b.advance(); c != '"' {
 			return ErrTape
 		}
+		if b.binding {
+			b.mark(off)
+		}
 		if err = b.str(off); err != nil {
 			return err
 		}
+		if b.binding {
+			b.addKey()
+		}
 		goto objectField
 	case '}':
+		if b.binding {
+			b.mark(off)
+			b.checkNames(b.stack[len(b.stack)-1].keyStart)
+		}
 		b.endContainer()
 		goto valueEnd
 	}
@@ -106,8 +147,14 @@
 	c, off = b.advance()
 
 value: // c, off start a value: the root, an object field's value or an array element
+	if b.binding {
+		b.mark(off)
+	}
 	switch c {
 	case '{', '[':
+		if b.binding && len(b.stack)+1 >= b.maxDepth { // v2 counts empty containers too
+			return ErrDepth
+		}
 		if b.empty(c) {
 			goto valueEnd
 		}
@@ -133,11 +180,14 @@
 	goto objectContinue
 
 arrayContinue:
-	switch c, _ = b.advance(); c {
+	switch c, off = b.advance(); c {
 	case ',':
 		b.stack[len(b.stack)-1].count++
 		goto arrayValue
 	case ']':
+		if b.binding {
+			b.mark(off)
+		}
 		b.endContainer()
 		goto valueEnd
 	}
@@ -157,7 +207,7 @@
 	if len(b.stack)+1 >= b.maxDepth {
 		return ErrDepth
 	}
-	b.stack = append(b.stack, scope{tapeIndex: uint32(len(b.tape)), open: open})
+	b.stack = append(b.stack, scope{tapeIndex: uint32(len(b.tape)), keyStart: uint32(len(b.keys)), open: open})
 	b.tape = append(b.tape, 0) // written by endContainer
 	return nil
 }
@@ -170,7 +220,10 @@
 	if b.peek() != end {
 		return false
 	}
-	b.advance()
+	_, closeOff := b.advance()
+	if b.binding {
+		b.markAt(len(b.tape)+1, closeOff)
+	}
 	i := uint64(len(b.tape))
 	b.tape = append(b.tape, word(c, i+2), word(end, i))
 	return true
@@ -234,3 +287,85 @@
 }
 
 func terminates(buf []byte, p int) bool { return p == len(buf) || isStructuralOrSpace[buf[p]] }
+
+// addKey records the name just written to the tape (binding mode).
+func (b *builder) addKey() {
+	i := uint32(len(b.tape) - 1)
+	b.keys = append(b.keys, nameKey{i, nameHash(b.name(i))})
+}
+
+// checkNames records, in binding mode, the first name of the closing object
+// (whose names start at b.keys[start]) that repeats an earlier one:
+// encoding/json/v2 rejects duplicate names in every object. Names are
+// compared by hash first: pairwise in small objects, through a hash table in
+// larger ones.
+func (b *builder) checkNames(start uint32) {
+	keys := b.keys[start:]
+	b.keys = b.keys[:start]
+	if len(keys) <= 8 {
+		for j := 1; j < len(keys); j++ {
+			for _, k := range keys[:j] {
+				if k.hash == keys[j].hash && string(b.name(k.i)) == string(b.name(keys[j].i)) {
+					b.dups = append(b.dups, keys[j].i)
+					return
+				}
+			}
+		}
+		return
+	}
+	size := 32
+	for size < 2*len(keys) {
+		size *= 2
+	}
+	if cap(b.seen) < size {
+		b.seen = make([]uint32, size)
+	}
+	seen := b.seen[:size]
+	clear(seen)
+	for j, k := range keys {
+		h := int(k.hash) & (size - 1)
+		for seen[h] != 0 {
+			if prev := keys[seen[h]-1]; prev.hash == k.hash && string(b.name(prev.i)) == string(b.name(k.i)) {
+				b.dups = append(b.dups, k.i)
+				return
+			}
+			h = (h + 1) & (size - 1)
+		}
+		seen[h] = uint32(j + 1)
+	}
+}
+
+// nameHash is a cheap hash of an object name for checkNames, which compares
+// the names themselves on a match: its length and first and last 8 bytes.
+func nameHash(s []byte) uint64 {
+	h := uint64(len(s)) * 0x9E3779B97F4A7C15
+	if len(s) >= 8 {
+		h ^= binary.LittleEndian.Uint64(s)
+		h *= 0xBF58476D1CE4E5B9
+		h ^= binary.LittleEndian.Uint64(s[len(s)-8:])
+	} else {
+		for _, c := range s {
+			h = h<<8 | uint64(c)
+		}
+	}
+	h *= 0x94D049BB133111EB
+	return h ^ h>>31
+}
+
+// name returns the unescaped name of the string word at tape index i.
+func (b *builder) name(i uint32) []byte {
+	off := b.tape[i] & (1<<56 - 1)
+	n := uint64(binary.LittleEndian.Uint32(b.strs[off:]))
+	return b.strs[off+4 : off+4+n]
+}
+
+// skipValue returns the tape index after the (complete) value at index i.
+func (b *builder) skipValue(i int) int {
+	switch w := b.tape[i]; byte(w >> 56) {
+	case tagStartArray, tagStartObject:
+		return int(uint32(w))
+	case tagInt64, tagUint64, tagDouble:
+		return i + 2
+	}
+	return i + 1
+}
```

```diff
--- a/numbers.go
+++ b/numbers.go
@@ -104,7 +104,13 @@
 			f, ok = decimalToFloat64(mant, int(max(-maxExp10, min(maxExp10, exp10))), neg)
 		}
 		if !ok {
-			return ErrNumber
+			if !b.binding {
+				return ErrNumber
+			}
+			f = math.Inf(1) // v2 rejects the overflow only when it decodes the value
+			if neg {
+				f = -f
+			}
 		}
 		b.tape = append(b.tape, word(tagDouble, 0), math.Float64bits(f))
 		return nil
```

- [ ] **Step 5: Run the test, then the parser regressions**

```sh
go test -count=1 -run TestBindingMode -v .   # PASS
make check                                   # all builds pass: the plain tape is unchanged
make fuzz target=FuzzParse time=60s          # no failures
make fuzz target=FuzzParse time=60s neon=1   # no failures
```

- [ ] **Step 6: Check the cost to plain parsing**

```sh
make bench neon=1 bench='BenchmarkParse/' count=6 out=/tmp/parse-after.txt
make benchstat old=/tmp/parse-before.txt new=/tmp/parse-after.txt
```

Expected: geomean of sec/op at most +3%. The prototype measured +2.6%. Run on an idle machine; a load average above the core count inflates both sides.

- [ ] **Step 7: Commit**

```sh
git add parser.go stage2.go numbers.go binding_test.go
git commit -m "feat: binding mode in stage 2 (offsets, duplicate names, ±Inf, v2 depth)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Unmarshal and Marshal

The codec core. Decoders and encoders share the per-type cache, method detection and struct field plans, so they land together. The test is the differential suite against v2.

**Files:**
- Create: `options.go`, `typeplan.go`, `strcache.go`, `decode.go`, `encode.go`, `methods.go`, `time.go`
- Modify: `serialize.go`
- Test: `bind_test.go`

**Interfaces:**
- Consumes: Task 1's `Parser.binding`, `Document.offs`, `Document.dups`; the DOM's `Element` (`tag`, `value`, `span`, `next`, `items`, `length`, `rawString`), `Object.AllBytes`; `bom` and the sentinel errors.
- Produces:
  - `func Unmarshal(data []byte, v any, opts ...Option) error`
  - `func Marshal(v any, opts ...Option) ([]byte, error)`
  - `func MarshalAppend(dst []byte, v any, opts ...Option) ([]byte, error)`
  - `type Option func(*options)`, plus `MatchCaseInsensitiveNames`, `FormatNilSliceAsNull`, `FormatNilMapAsNull`, `Deterministic` and `RejectUnknownMembers` (each `func(v bool) Option`)
  - `makeOptions(opts []Option) options`
  - Test helpers used by Tasks 3–5: `errClass(error) string`, `sameUnmarshal(in []byte, t reflect.Type, opts ...Option) string`, `sameMarshal(v any, opts ...Option) string`, `diffTypes []reflect.Type`, `diffInputs []string`, the types `Inner` and `Outer`.

- [ ] **Step 1: Write the failing test**

```go
package simdjson

import (
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"
)

// errClass is what Unmarshal and Marshal must agree with v2 on: whether an
// error occurred, its type, and the exported sentinel it wraps.
func errClass(err error) string {
	var sy *jsontext.SyntacticError
	var se *jsonv2.SemanticError
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, jsontext.ErrDuplicateName):
		return "syntactic:duplicate"
	case errors.As(err, &sy):
		return "syntactic"
	case errors.Is(err, jsonv2.ErrUnknownName):
		return "semantic:unknown"
	case errors.As(err, &se):
		return "semantic"
	}
	return fmt.Sprintf("other(%T)", err)
}

// v2Options maps our options to v2's.
func v2Options(opts []Option) []jsonv2.Options {
	o := makeOptions(opts)
	return []jsonv2.Options{
		jsonv2.MatchCaseInsensitiveNames(o.caseInsensitive),
		jsonv2.FormatNilSliceAsNull(o.nilSliceAsNull),
		jsonv2.FormatNilMapAsNull(o.nilMapAsNull),
		jsonv2.Deterministic(o.deterministic),
		jsonv2.RejectUnknownMembers(o.rejectUnknown),
	}
}

// sameUnmarshal reports how Unmarshal differs from v2's for input in and a
// fresh value of type t, or "".
func sameUnmarshal(in []byte, t reflect.Type, opts ...Option) string {
	want := reflect.New(t)
	errWant := jsonv2.Unmarshal(in, want.Interface(), v2Options(opts)...)
	got := reflect.New(t)
	errGot := Unmarshal(in, got.Interface(), opts...)
	cw, cg := errClass(errWant), errClass(errGot)
	// Documented deviation: invalid input is always a SyntacticError here,
	// while v2 may first report a semantic error it meets earlier.
	if cg == "syntactic" && errWant != nil && !jsontext.Value(in).IsValid() {
		return ""
	}
	if cw != cg {
		return fmt.Sprintf("%v: error %s (%v), v2 %s (%v)", t, cg, errGot, cw, errWant)
	}
	if errWant == nil && !reflect.DeepEqual(got.Elem().Interface(), want.Elem().Interface()) {
		return fmt.Sprintf("%v: got %#v, v2 %#v", t, got.Elem().Interface(), want.Elem().Interface())
	}
	return ""
}

// sameMarshal reports how Marshal differs from v2's for v, or "".
func sameMarshal(v any, opts ...Option) string {
	want, errWant := jsonv2.Marshal(v, v2Options(opts)...)
	got, errGot := Marshal(v, opts...)
	if cw, cg := errClass(errWant), errClass(errGot); cw != cg {
		return fmt.Sprintf("%T: error %s (%v), v2 %s (%v)", v, cg, errGot, cw, errWant)
	}
	if errWant == nil && string(got) != string(want) {
		return fmt.Sprintf("%T: got %s, v2 %s", v, got, want)
	}
	return ""
}

type text string

func (t *text) UnmarshalText(b []byte) error {
	if string(b) == "bad" {
		return errors.New("bad text")
	}
	*t = text("T:" + string(b))
	return nil
}
func (t text) MarshalText() ([]byte, error) { return []byte("T:" + string(t)), nil }

type rawJSON struct{ Got string }

func (r *rawJSON) UnmarshalJSON(b []byte) error { r.Got = string(b); return nil }
func (r rawJSON) MarshalJSON() ([]byte, error)  { return []byte(r.Got), nil }

type Inner struct {
	X int `json:"x"`
	Y string
}

type Outer struct {
	A     int               `json:"a"`
	B     string            `json:"b,omitempty"`
	C     []int             `json:"c,omitzero"`
	D     *Inner            `json:"d"`
	E     map[string]Inner  `json:"e"`
	F     any               `json:"f"`
	G     float64           `json:"g,string"`
	H     int64             `json:"h,string"`
	I     time.Time         `json:"i,omitzero"`
	J     bool              `json:"j,case:ignore"`
	K     text              `json:"k"`
	L     jsonv1.RawMessage `json:"l"`
	Inner                   // promoted: x, Y
	skip  int
}

type Conflict struct {
	A  int `json:"a"`
	A2 int `json:"A"`
}

var diffTypes = []reflect.Type{
	reflect.TypeFor[bool](), reflect.TypeFor[string](),
	reflect.TypeFor[int8](), reflect.TypeFor[int](), reflect.TypeFor[int64](),
	reflect.TypeFor[uint8](), reflect.TypeFor[uint](), reflect.TypeFor[uint64](),
	reflect.TypeFor[float32](), reflect.TypeFor[float64](),
	reflect.TypeFor[[]byte](), reflect.TypeFor[[4]byte](),
	reflect.TypeFor[[]int](), reflect.TypeFor[[2]int](), reflect.TypeFor[[]any](),
	reflect.TypeFor[map[string]int](), reflect.TypeFor[map[int]string](), reflect.TypeFor[map[uint8]any](),
	reflect.TypeFor[map[float64]int](), reflect.TypeFor[map[bool]int](), reflect.TypeFor[map[string]any](),
	reflect.TypeFor[map[text]int](),
	reflect.TypeFor[*int](), reflect.TypeFor[**string](), reflect.TypeFor[any](), reflect.TypeFor[fmt.Stringer](),
	reflect.TypeFor[time.Time](), reflect.TypeFor[time.Duration](), reflect.TypeFor[netip.Addr](),
	reflect.TypeFor[text](), reflect.TypeFor[rawJSON](), reflect.TypeFor[jsonv1.RawMessage](),
	reflect.TypeFor[Inner](), reflect.TypeFor[Outer](), reflect.TypeFor[*Outer](), reflect.TypeFor[Conflict](),
	reflect.TypeFor[[]Outer](), reflect.TypeFor[chan int](), reflect.TypeFor[complex128](),
}

var diffInputs = []string{
	`null`, `true`, `false`, `0`, `-0`, `1`, `-1`, `127`, `128`, `255`, `256`, `-128`, `-129`,
	`1.5`, `1e2`, `1E400`, `-1e400`, `1e-400`, `0.1`, `3.4028235e38`, `3.5e38`, `16777217`,
	`9223372036854775807`, `9223372036854775808`, `-9223372036854775808`, `-9223372036854775809`,
	`18446744073709551615`, `18446744073709551616`, `123456789012345678901234567890`, `-123456789012345678901234567890`,
	`""`, `"x"`, `"1"`, `"-1"`, `"-0"`, `"01"`, `"1.5"`, `"1e2"`, `" 1"`, `"null"`, `"true"`,
	`"AQID"`, `"AQIDBA=="`, `"AQIDBA"`, `"AQ\nID"`, `"bad"`, `"é"`, `"1.2.3.4"`, `"::1"`,
	`"2026-10-07T12:00:00Z"`, `"2026-10-07T12:00:00.5+02:00"`, `"2026-10-07T12:00:00+25:00"`, `"2026-10-07T1:00:00Z"`,
	`[]`, `[1]`, `[1,2]`, `[1,2,3]`, `[[1],[2]]`, `[null]`, `[{"q":1,"q":2}]`,
	`{}`, `{"a":1}`, `{"A":1}`, `{"a":1,"A":2}`, `{"a":1,"a":2}`, `{"zz":1,"zz":2}`, `{"zz":{"x":1,"x":2}}`,
	`{"1":"a","2":"b"}`, `{"1":"a","01":"b"}`, `{"0":"a","-0":"b"}`, `{"1.5":1,"1.50":2}`, `{"true":1}`,
	`{"x":1,"Y":"y"}`, `{"x":1,"x":2}`, `{"y":"a","Y":"b"}`, `{"j":true}`, `{"J":true}`, `{"_J-":true}`,
	`{"g":"1.5","h":"-7"}`, `{"g":1.5}`, `{"h":"07"}`, `{"h":"1e2"}`, `{"g":"-0"}`,
	`{"d":{"x":3},"e":{"k":{"x":4}},"f":[1,"s",{"t":null}]}`, `{"d":null,"c":[]}`,
	`{"k":"hi","l":{ "raw" : [1,  2] }}`, `{"l":{"a":1,"a":2}}`, `{"i":"2026-10-07T12:00:00Z"}`,
	`{"b":"s","b":"t"}`, `{"a":"wrong"}`, `{"a":1, "unknown":{"deep":[1,{"k":1,"k":2}]}}`,
	` {"b" : [1, 2] , "a":3 } `, `{"\u0061":5}`, `{"a":1,"\u0061":2}`, `{"x\"y":1,"x\u0022y":2}`,
	`{`, `[1,]`, `"\ud800"`, "\xEF\xBB\xBF{}", `{} x`, `[1 2]`, `tru`, `"\x01"`, `01`, `1.`, `{"a" 1}`,
}

func TestUnmarshalMatchesV2(t *testing.T) {
	optSets := [][]Option{nil, {MatchCaseInsensitiveNames(true)}, {RejectUnknownMembers(true)}}
	for _, opts := range optSets {
		for _, typ := range diffTypes {
			for _, in := range diffInputs {
				if d := sameUnmarshal([]byte(in), typ, opts...); d != "" {
					t.Errorf("Unmarshal(%q) opts=%d: %s", in, len(opts), d)
				}
			}
		}
	}
}

func TestDeepAndWide(t *testing.T) {
	for _, in := range []string{
		strings.Repeat("[", 10000) + strings.Repeat("]", 10000),
		strings.Repeat("[", 10001) + strings.Repeat("]", 10001),
		strings.Repeat("[", 10000) + "1" + strings.Repeat("]", 10000),
		strings.Repeat(`{"a":`, 9999) + "{}" + strings.Repeat("}", 9999),
		wideObject(100, ""), wideObject(100, `,"k3":0`), wideObject(1000, `,"k999":0`),
	} {
		if d := sameUnmarshal([]byte(in), reflect.TypeFor[any]()); d != "" {
			t.Errorf("depth %d: %s", len(in), d[:min(len(d), 300)])
		}
	}
}

// wideObject is an object with n names k0, k1, …, followed by extra.
func wideObject(n int, extra string) string {
	var b strings.Builder
	b.WriteString("{")
	for i := range n {
		fmt.Fprintf(&b, `"k%d":%d,`, i, i)
	}
	b.WriteString(`"end":0` + extra + "}")
	return b.String()
}

// TestUnmarshalIntoExisting decodes into values that already hold data:
// slices with spare capacity, maps with entries, set pointers and fields.
func TestUnmarshalIntoExisting(t *testing.T) {
	fill := func() []any {
		n := 7
		return []any{
			&[]int{9, 9, 9, 9}, ptr(make([]int, 1, 8)), &map[string]int{"a": 1, "z": 26},
			&map[string]Inner{"k": {X: 1, Y: "keep"}}, ptr(&n), &Outer{A: 5, B: "keep", D: &Inner{Y: "keep"}},
			ptr(any(map[string]any{"old": true})), ptr(any(&Inner{Y: "keep"})), &[2]int{7, 7},
		}
	}
	for _, in := range []string{`[1,2]`, `[]`, `null`, `{"a":2,"b":3}`, `{"k":{"x":2}}`, `3`, `{"x":1}`, `{"d":{"x":2}}`, `[1]`} {
		got, want := fill(), fill()
		for i := range got {
			errGot := Unmarshal([]byte(in), got[i])
			errWant := jsonv2.Unmarshal([]byte(in), want[i])
			if errClass(errGot) != errClass(errWant) || !reflect.DeepEqual(got[i], want[i]) {
				t.Errorf("Unmarshal(%s) into %T: got %v %+v, v2 %v %+v", in, got[i], errGot, got[i], errWant, want[i])
			}
		}
	}
}

func ptr[T any](v T) *T { return &v }

type node struct {
	Name string          `json:"name"`
	Kids []node          `json:"kids,omitempty"`
	Next *node           `json:"next,omitempty"`
	Tags map[string]node `json:"tags,omitempty"`
}

// TestRecursiveTypes builds codecs for a type that refers to itself.
func TestRecursiveTypes(t *testing.T) {
	in := `{"name":"a","kids":[{"name":"b","next":{"name":"c"}}],"tags":{"t":{"name":"d","kids":[]}}}`
	if d := sameUnmarshal([]byte(in), reflect.TypeFor[node]()); d != "" {
		t.Error(d)
	}
	var n node
	if err := Unmarshal([]byte(in), &n); err != nil {
		t.Fatal(err)
	}
	if d := sameMarshal(n, Deterministic(true)); d != "" {
		t.Error(d)
	}
}

// TestUnmarshalReusesParsers runs Unmarshal calls back to back, so pooled parsers are
// reused: state from one document must not leak into the next.
func TestUnmarshalReusesParsers(t *testing.T) {
	for range 3 {
		for _, in := range []string{`{"a":1,"a":2}`, `{"a":1}`, wideObject(50, `,"k1":0`), wideObject(50, ""), `[1e400]`, `[1]`} {
			if d := sameUnmarshal([]byte(in), reflect.TypeFor[any]()); d != "" {
				t.Errorf("%.40s: %s", in, d)
			}
		}
	}
}

func TestMarshalMatchesV2(t *testing.T) {
	ts := time.Date(2026, 10, 7, 12, 0, 0, 500, time.FixedZone("x", 2*3600))
	in := Inner{X: 1, Y: "y"}
	values := []any{
		nil, true, false, 0, -1, int8(-128), uint64(18446744073709551615), 1.0, 100.0, 0.1, 1e20, 1e21, 1e-6, 1e-7,
		float32(0.1), float32(1e21), 5e-324, -0.0, 123456789.125,
		"", "x", "q\"b\\s/ <>&   \x01 \t é😀", "bad \xff",
		[]byte(nil), []byte{}, []byte{1, 2, 3}, [4]byte{1}, []int(nil), []int{}, []int{1, 2}, [2]int{3, 4},
		map[string]int(nil), map[string]int{}, map[string]int{"b": 2, "a": 1, "é": 3}, map[int]string{2: "b", -1: "a", 10: "c"},
		map[float64]int{1.5: 1, 2: 2}, map[bool]int{true: 1}, map[text]int{"x": 1, "y": 2},
		map[string]any{"z": []any{1, "s", nil, map[string]any{}}, "a": nil},
		&in, in, Outer{A: 1, Inner: in}, Outer{B: "s", C: []int{}, G: 1.5, H: -7, I: ts, K: "k", L: jsonv1.RawMessage(`{ "r" : [1,  2.50] }`)},
		[]Outer{{}}, Conflict{A: 1, A2: 2}, ts, time.Time{}, time.Second, netip.MustParseAddr("::1"),
		text("t"), rawJSON{`{"a" : 1}`}, rawJSON{`{"a":1,"a":2}`}, rawJSON{`[1, 2.50, 1e2]`}, rawJSON{`bad`},
		make(chan int), complex(1, 2), math.Inf(1), new(any), struct{}{},
		struct {
			F float64 `json:",omitempty"`
			P *int    `json:",omitempty"`
			S []int   `json:",omitempty"`
			M rawJSON `json:",omitempty"`
		}{M: rawJSON{`""`}},
	}
	for _, opts := range [][]Option{{Deterministic(true)}, {Deterministic(true), FormatNilSliceAsNull(true), FormatNilMapAsNull(true)}} {
		for _, v := range values {
			if d := sameMarshal(v, opts...); d != "" {
				t.Errorf("Marshal(%#v): %s", v, d)
			}
		}
	}
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test -run MatchesV2 .`
Expected: build failure, `undefined: Unmarshal` (and `Option`, `Marshal`, …).

- [ ] **Step 3: Options, struct field plans and the string cache**

```go
package simdjson

// Option configures Unmarshal, Marshal, MarshalAppend and MarshalIndent.
// The defaults are those of encoding/json/v2; each option turns on the v2
// option of the same name, which restores a behaviour of encoding/json (v1).
type Option func(*options)

type options struct {
	caseInsensitive bool
	nilSliceAsNull  bool
	nilMapAsNull    bool
	deterministic   bool
	rejectUnknown   bool
}

func makeOptions(opts []Option) options {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// MatchCaseInsensitiveNames matches JSON object names to struct fields
// ignoring case, dashes and underscores. A field tagged `case:strict` or
// `case:ignore` keeps its own rule.
func MatchCaseInsensitiveNames(v bool) Option { return func(o *options) { o.caseInsensitive = v } }

// FormatNilSliceAsNull marshals a nil slice as null instead of [].
func FormatNilSliceAsNull(v bool) Option { return func(o *options) { o.nilSliceAsNull = v } }

// FormatNilMapAsNull marshals a nil map as null instead of {}.
func FormatNilMapAsNull(v bool) Option { return func(o *options) { o.nilMapAsNull = v } }

// Deterministic marshals map entries sorted by their JSON names.
func Deterministic(v bool) Option { return func(o *options) { o.deterministic = v } }

// RejectUnknownMembers makes an object name that matches no struct field an error.
func RejectUnknownMembers(v bool) Option { return func(o *options) { o.rejectUnknown = v } }
```

```go
// Struct field rules, tag parsing and name folding are adapted from the Go
// standard library (src/encoding/json/v2/fields.go and fold.go), Copyright
// 2020-2021 The Go Authors, under the BSD-style license in LICENSE-GO.

package simdjson

import (
	"cmp"
	"encoding"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// A codec decodes and encodes one Go type. Codecs are built on first use and
// cached; a codec is in the cache before its functions are built, so
// recursive types resolve to themselves.
type codec struct {
	typ        reflect.Type
	once       sync.Once
	dec        decodeFunc
	enc        encodeFunc
	nonDefault bool // the type has marshal or unmarshal methods (or is a time type)
}

// decodeFunc decodes e into the addressable v.
type decodeFunc func(d *decodeState, e Element, v reflect.Value, mode uint8) error

// encodeFunc appends v (addressable) to s.buf.
type encodeFunc func(s *encodeState, v reflect.Value, mode uint8) error

// Modes of decodeFunc and encodeFunc.
const (
	modeStringTag uint8 = 1 << iota // the field has the `string` tag option
	modeName                        // the value is an object name (a map key)
)

var codecs sync.Map // reflect.Type → *codec

func codecFor(t reflect.Type) *codec {
	if c, ok := codecs.Load(t); ok {
		return c.(*codec)
	}
	c, _ := codecs.LoadOrStore(t, &codec{typ: t})
	return c.(*codec)
}

func (c *codec) init() {
	c.once.Do(func() {
		c.dec, c.enc = makeDefaultDecoder(c.typ), makeDefaultEncoder(c.typ)
		c.nonDefault = addMethods(c)
		if c.typ == timeTimeType || c.typ == timeDurationType {
			c.dec, c.enc = makeTimeDecoder(c.typ), makeTimeEncoder(c.typ)
			c.nonDefault = true
		}
	})
}

func (c *codec) decode(d *decodeState, e Element, v reflect.Value, mode uint8) error {
	c.init()
	return c.dec(d, e, v, mode)
}

func (c *codec) encode(s *encodeState, v reflect.Value, mode uint8) error {
	c.init()
	return c.enc(s, v, mode)
}

var (
	jsonMarshalerType       = reflect.TypeFor[jsonv2.Marshaler]()
	jsonMarshalerToType     = reflect.TypeFor[jsonv2.MarshalerTo]()
	jsonUnmarshalerType     = reflect.TypeFor[jsonv2.Unmarshaler]()
	jsonUnmarshalerFromType = reflect.TypeFor[jsonv2.UnmarshalerFrom]()
	textAppenderType        = reflect.TypeFor[encoding.TextAppender]()
	textMarshalerType       = reflect.TypeFor[encoding.TextMarshaler]()
	textUnmarshalerType     = reflect.TypeFor[encoding.TextUnmarshaler]()
	isZeroerType            = reflect.TypeFor[interface{ IsZero() bool }]()
	jsontextValueType       = reflect.TypeFor[jsontext.Value]()
	timeTimeType            = reflect.TypeFor[time.Time]()
	timeDurationType        = reflect.TypeFor[time.Duration]()

	allMarshalerTypes   = []reflect.Type{jsonMarshalerToType, jsonMarshalerType, textAppenderType, textMarshalerType}
	allUnmarshalerTypes = []reflect.Type{jsonUnmarshalerFromType, jsonUnmarshalerType, textUnmarshalerType}
	allMethodTypes      = append(slices.Clip(allMarshalerTypes), allUnmarshalerTypes...)
)

// implements reports whether t or *t implements iface, and whether only *t does.
func implements(t, iface reflect.Type) (needAddr, ok bool) {
	switch {
	case t.Implements(iface):
		return false, true
	case reflect.PointerTo(t).Implements(iface):
		return true, true
	}
	return false, false
}

func implementsAny(t reflect.Type, ifaces ...reflect.Type) bool {
	for _, iface := range ifaces {
		if _, ok := implements(t, iface); ok {
			return true
		}
	}
	return false
}

// errUnsupportedMethods is returned for types whose only JSON methods are the
// jsontext-based MarshalerTo/UnmarshalerFrom, which this package cannot call.
var errUnsupportedMethods = errors.New("MarshalJSONTo and UnmarshalJSONFrom methods are not supported")

type isZeroer interface{ IsZero() bool }

// structFields is the list of JSON-representable fields of a struct type.
type structFields struct {
	flattened    []structField // depth-first order
	byActualName map[string]*structField
	byFoldedName map[string][]*structField
	foldable     bool             // some field is tagged `case:ignore`
	byLen        [][]*structField // byLen[n]: the fields whose name is n bytes long
}

// lookup returns the field named exactly name, or nil. Indexing by length
// first rejects most unknown names without comparing bytes.
func (fs *structFields) lookup(name []byte) *structField {
	if len(name) >= len(fs.byLen) {
		return nil
	}
	for _, f := range fs.byLen[len(name)] {
		if f.name == string(name) {
			return f
		}
	}
	return nil
}

func (fs *structFields) lookupByFoldedName(name []byte) []*structField {
	return fs.byFoldedName[string(foldName(name))]
}

type structField struct {
	id      int   // breadth-first ID, used for duplicate detection
	index   []int // according to reflect.Value.FieldByIndex
	typ     reflect.Type
	cod     *codec
	isZero  func(reflect.Value) bool
	isEmpty func(reflect.Value) bool
	fieldOptions
}

var errNoExportedFields = errors.New("Go struct has no exported fields")

// makeStructFields is v2's makeStructFields (fields.go) without embedded
// fallbacks: an embedded Go map or jsontext.Value is reported as unsupported.
func makeStructFields(root reflect.Type) (fs structFields, serr *jsonv2.SemanticError) {
	orErrorf := func(serr *jsonv2.SemanticError, t reflect.Type, f string, a ...any) *jsonv2.SemanticError {
		return cmp.Or(serr, &jsonv2.SemanticError{GoType: t, Err: fmt.Errorf(f, a...)})
	}

	// Breadth-first search, so that len(f.index) increases monotonically.
	type queueEntry struct {
		typ           reflect.Type
		index         []int
		visitChildren bool // whether to visit embedded fields of this struct
	}
	queue := []queueEntry{{root, nil, true}}
	seen := map[reflect.Type]bool{root: true}
	var allFields []structField
	for qi := 0; qi < len(queue); qi++ {
		qe := queue[qi]
		t := qe.typ
		namesIndex := make(map[string]int) // field index per JSON name in this struct
		var hasAnyJSONTag, hasAnyJSONField bool
		for i := range t.NumField() {
			sf := t.Field(i)
			_, hasTag := sf.Tag.Lookup("json")
			hasAnyJSONTag = hasAnyJSONTag || hasTag
			options, ignored, err := parseFieldOptions(sf)
			if err != nil {
				serr = cmp.Or(serr, &jsonv2.SemanticError{GoType: t, Err: err})
			}
			if ignored {
				continue
			}
			hasAnyJSONField = true
			f := structField{
				index:        append(append(make([]int, 0, len(qe.index)+1), qe.index...), i),
				typ:          sf.Type,
				fieldOptions: options,
			}
			if sf.Anonymous && !f.hasName {
				if indirectType(f.typ).Kind() != reflect.Struct {
					serr = orErrorf(serr, t, "embedded Go struct field %s of non-struct type must be explicitly given a JSON name", sf.Name)
				} else {
					f.embed = true // implied by Go embedding without an explicit name
				}
			}

			var handleEmbed, handleField func()
			handleEmbed = func() {
				if f.fieldOptions != (fieldOptions{name: f.name, quotedName: f.quotedName, embed: true}) {
					serr = orErrorf(serr, t, "Go struct field %s cannot have any options other than `embed` specified", sf.Name)
					if f.hasName {
						handleField()
						return // invalid embedded field; treat as regular field
					}
					f.fieldOptions = fieldOptions{name: f.name, quotedName: f.quotedName, embed: f.embed}
				}
				tf := indirectType(f.typ)
				if implementsAny(tf, allMethodTypes...) && tf != jsontextValueType {
					serr = orErrorf(serr, t, "embedded Go struct field %s of type %s must not implement marshal or unmarshal methods", sf.Name, tf)
				}
				if tf.Kind() == reflect.Struct {
					if qe.visitChildren {
						queue = append(queue, queueEntry{tf, f.index, !seen[tf]})
					}
					seen[tf] = true
					return
				} else if !sf.IsExported() {
					serr = orErrorf(serr, t, "embedded Go struct field %s is not exported", sf.Name)
					return
				}
				// v2 accepts an embedded Go map or jsontext.Value as a fallback
				// for unknown names; this package does not support it.
				serr = orErrorf(serr, t, "embedded Go struct field %s of type %s is not supported (only Go structs may be embedded)", sf.Name, tf)
			}
			handleField = func() {
				if !sf.IsExported() {
					tf := indirectType(f.typ)
					if !(sf.Anonymous && tf.Kind() == reflect.Struct) {
						serr = orErrorf(serr, t, "Go struct field %s is not exported", sf.Name)
						return
					}
					if implementsAny(tf, allMethodTypes...) || (f.omitzero && implementsAny(tf, isZeroerType)) {
						serr = orErrorf(serr, t, "Go struct field %s is not exported for method calls", sf.Name)
						return
					}
				}
				switch {
				case sf.Type.Kind() == reflect.Interface && sf.Type.Implements(isZeroerType):
					f.isZero = func(v reflect.Value) bool {
						return v.IsNil() || (v.Elem().Kind() == reflect.Pointer && v.Elem().IsNil()) || v.Interface().(isZeroer).IsZero()
					}
				case sf.Type.Kind() == reflect.Pointer && sf.Type.Implements(isZeroerType):
					f.isZero = func(v reflect.Value) bool { return v.IsNil() || v.Interface().(isZeroer).IsZero() }
				case sf.Type.Implements(isZeroerType):
					f.isZero = func(v reflect.Value) bool { return v.Interface().(isZeroer).IsZero() }
				case reflect.PointerTo(sf.Type).Implements(isZeroerType):
					f.isZero = func(v reflect.Value) bool { return v.Addr().Interface().(isZeroer).IsZero() }
				}
				switch sf.Type.Kind() {
				case reflect.String, reflect.Map, reflect.Array, reflect.Slice:
					f.isEmpty = func(v reflect.Value) bool { return v.Len() == 0 }
				case reflect.Pointer, reflect.Interface:
					f.isEmpty = func(v reflect.Value) bool { return v.IsNil() }
				}
				if j, ok := namesIndex[f.name]; ok {
					serr = orErrorf(serr, t, "Go struct fields %s and %s conflict over JSON object name %q", t.Field(j).Name, sf.Name, f.name)
				}
				namesIndex[f.name] = i
				f.id = len(allFields)
				f.cod = codecFor(sf.Type)
				allFields = append(allFields, f)
				if f.format != "" {
					serr = orErrorf(serr, t, "Go struct field %s has `format` tag option, which is not supported", sf.Name)
				}
			}
			if f.embed {
				handleEmbed()
			} else {
				handleField()
			}
		}
		// Refuse a struct with fields but none JSON-representable and no
		// `json` tags (e.g. the errors.New type), as v2 does.
		if t.NumField() > 0 && !hasAnyJSONTag && !hasAnyJSONField {
			serr = cmp.Or(serr, &jsonv2.SemanticError{GoType: t, Err: errNoExportedFields})
		}
	}

	// Keep the dominant field per name: the one alone at the shallowest depth,
	// or uniquely tagged with a JSON name there.
	flattened := allFields[:0]
	slices.SortStableFunc(allFields, func(x, y structField) int {
		return cmp.Or(
			strings.Compare(x.name, y.name),
			cmp.Compare(len(x.index), len(y.index)),
			boolsCompare(!x.hasName, !y.hasName))
	})
	for len(allFields) > 0 {
		n := 1
		for n < len(allFields) && allFields[n-1].name == allFields[n].name {
			n++
		}
		if n == 1 || len(allFields[0].index) != len(allFields[1].index) || allFields[0].hasName != allFields[1].hasName {
			flattened = append(flattened, allFields[0])
		}
		allFields = allFields[n:]
	}
	slices.SortFunc(flattened, func(x, y structField) int { return cmp.Compare(x.id, y.id) })
	for i := range flattened {
		flattened[i].id = i
	}
	slices.SortFunc(flattened, func(x, y structField) int { return slices.Compare(x.index, y.index) })

	fs = structFields{
		flattened:    flattened,
		byActualName: make(map[string]*structField, len(flattened)),
		byFoldedName: make(map[string][]*structField, len(flattened)),
	}
	for i, f := range fs.flattened {
		fs.foldable = fs.foldable || f.casing == caseIgnore
		folded := string(foldName([]byte(f.name)))
		fs.byActualName[f.name] = &fs.flattened[i]
		fs.byFoldedName[folded] = append(fs.byFoldedName[folded], &fs.flattened[i])
	}
	for i := range fs.flattened {
		f := &fs.flattened[i]
		for len(fs.byLen) <= len(f.name) {
			fs.byLen = append(fs.byLen, nil)
		}
		fs.byLen[len(f.name)] = append(fs.byLen[len(f.name)], f)
	}
	for folded, fields := range fs.byFoldedName {
		if len(fields) > 1 {
			// Conflicting case-insensitive names take breadth-first precedence.
			slices.SortFunc(fields, func(x, y *structField) int { return cmp.Compare(x.id, y.id) })
			fs.byFoldedName[folded] = fields
		}
	}
	return fs, serr
}

// indirectType unwraps one unnamed pointer level, as Go embedding allows.
func indirectType(t reflect.Type) reflect.Type {
	if t.Kind() == reflect.Pointer && t.Name() == "" {
		t = t.Elem()
	}
	return t
}

// matchFoldedName reports whether name matches f case-insensitively under
// the field's `case:` option or MatchCaseInsensitiveNames. It assumes
// foldName(f.name) == foldName(name).
func (f *structField) matchFoldedName(caseInsensitive bool) bool {
	return f.casing == caseIgnore || (caseInsensitive && f.casing != caseStrict)
}

const (
	caseIgnore = 1
	caseStrict = 2
)

type fieldOptions struct {
	name       string
	quotedName string // quoted per RFC 8785, section 3.2.2.2
	hasName    bool
	casing     int8 // 0, caseIgnore or caseStrict
	embed      bool
	omitzero   bool
	omitempty  bool
	string     bool
	format     string
}

// parseFieldOptions parses the `json` tag of a struct field (v2 fields.go).
func parseFieldOptions(sf reflect.StructField) (out fieldOptions, ignored bool, err error) {
	tag, hasTag := sf.Tag.Lookup("json")
	if tag == "-" {
		return fieldOptions{}, true, nil
	}
	if !sf.IsExported() && !sf.Anonymous {
		if hasTag {
			err = cmp.Or(err, fmt.Errorf("unexported Go struct field %s cannot have non-ignored `json:%q` tag", sf.Name, tag))
		}
		return fieldOptions{}, true, err
	}

	out.name = sf.Name
	if len(tag) > 0 && !strings.HasPrefix(tag, ",") {
		n := len(tag) - len(strings.TrimLeftFunc(tag, func(r rune) bool {
			return !strings.ContainsRune(",\\'\"`", r) // reserve comma, backslash, and quotes
		}))
		name := tag[:n]
		var err2 error
		if !strings.HasPrefix(tag[n:], ",") && len(name) != len(tag) {
			name, n, err2 = consumeTagOption(tag)
			if err2 != nil {
				err = cmp.Or(err, fmt.Errorf("Go struct field %s has malformed `json` tag: %v", sf.Name, err2))
			}
		}
		if !utf8.ValidString(name) {
			err = cmp.Or(err, fmt.Errorf("Go struct field %s has JSON object name %q with invalid UTF-8", sf.Name, name))
			name = string([]rune(name))
		}
		if err2 == nil {
			out.hasName = true
			out.name = name
		}
		tag = tag[n:]
	}
	out.quotedName = string(appendQuote(nil, out.name))

	var wasFormat bool
	seenOpts := make(map[string]bool)
	for len(tag) > 0 {
		if tag[0] != ',' {
			err = cmp.Or(err, fmt.Errorf("Go struct field %s has malformed `json` tag: invalid character %q before next option (expecting ',')", sf.Name, tag[0]))
		} else {
			tag = tag[len(","):]
			if len(tag) == 0 {
				err = cmp.Or(err, fmt.Errorf("Go struct field %s has malformed `json` tag: invalid trailing ',' character", sf.Name))
				break
			}
		}
		opt, n, err2 := consumeTagOption(tag)
		if err2 != nil {
			err = cmp.Or(err, fmt.Errorf("Go struct field %s has malformed `json` tag: %v", sf.Name, err2))
		}
		rawOpt := tag[:n]
		tag = tag[n:]
		switch {
		case wasFormat:
			err = cmp.Or(err, fmt.Errorf("Go struct field %s has `format` tag option that was not specified last", sf.Name))
		case strings.HasPrefix(rawOpt, "'") && strings.TrimFunc(opt, isLetterOrDigit) == "":
			err = cmp.Or(err, fmt.Errorf("Go struct field %s has unnecessarily quoted appearance of `%s` tag option; specify `%s` instead", sf.Name, rawOpt, opt))
		}
		switch opt {
		case "case":
			if !strings.HasPrefix(tag, ":") {
				err = cmp.Or(err, fmt.Errorf("Go struct field %s is missing value for `case` tag option; specify `case:ignore` or `case:strict` instead", sf.Name))
				break
			}
			tag = tag[len(":"):]
			opt, n, err2 := consumeTagOption(tag)
			if err2 != nil {
				err = cmp.Or(err, fmt.Errorf("Go struct field %s has malformed value for `case` tag option: %v", sf.Name, err2))
				break
			}
			rawOpt := tag[:n]
			tag = tag[n:]
			if strings.HasPrefix(rawOpt, "'") {
				err = cmp.Or(err, fmt.Errorf("Go struct field %s has unnecessarily quoted appearance of `case:%s` tag option; specify `case:%s` instead", sf.Name, rawOpt, opt))
			}
			switch opt {
			case "ignore":
				out.casing |= caseIgnore
			case "strict":
				out.casing |= caseStrict
			default:
				err = cmp.Or(err, fmt.Errorf("Go struct field %s has unknown `case:%s` tag value", sf.Name, rawOpt))
			}
		case "embed":
			out.embed = true
		case "omitzero":
			out.omitzero = true
		case "omitempty":
			out.omitempty = true
		case "string":
			out.string = true
		case "format":
			if !strings.HasPrefix(tag, ":") {
				err = cmp.Or(err, fmt.Errorf("Go struct field %s is missing value for `format` tag option", sf.Name))
				break
			}
			tag = tag[len(":"):]
			opt, n, err2 := consumeQuotedTagOption(tag)
			if err2 != nil {
				err = cmp.Or(err, fmt.Errorf("Go struct field %s has malformed value for `format` tag option: %v", sf.Name, err2))
				break
			} else if opt == "" {
				err = cmp.Or(err, fmt.Errorf("Go struct field %s cannot have empty value for `format` tag option", sf.Name))
				break
			}
			tag = tag[n:]
			out.format = opt
			wasFormat = true
		default:
			// Reject keys that resemble a supported option ("omitEmpty", "omit_empty").
			switch norm := strings.ReplaceAll(strings.ToLower(opt), "_", ""); norm {
			case "case", "embed", "omitzero", "omitempty", "string", "format":
				err = cmp.Or(err, fmt.Errorf("Go struct field %s has invalid appearance of `%s` tag option; specify `%s` instead", sf.Name, opt, norm))
			}
			// Anything else is ignored, as in v2.
		}
		switch {
		case out.casing == caseIgnore|caseStrict:
			err = cmp.Or(err, fmt.Errorf("Go struct field %s cannot have both `case:ignore` and `case:strict` tag options", sf.Name))
		case seenOpts[opt]:
			err = cmp.Or(err, fmt.Errorf("Go struct field %s has duplicate appearance of `%s` tag option", sf.Name, rawOpt))
		}
		seenOpts[opt] = true
	}
	return out, false, err
}

// consumeTagOption consumes a Go identifier option; an invalid option
// returns everything up to the next comma and an error.
func consumeTagOption(in string) (string, int, error) {
	i := strings.IndexByte(in, ',')
	if i < 0 {
		i = len(in)
	}
	switch r, _ := utf8.DecodeRuneInString(in); {
	case r == '_' || unicode.IsLetter(r):
		n := len(in) - len(strings.TrimLeftFunc(in, isLetterOrDigit))
		return in[:n], n, nil
	case len(in) == 0:
		return in[:i], i, io.ErrUnexpectedEOF
	default:
		return in[:i], i, fmt.Errorf("invalid character %q at start of option (expecting Unicode letter)", r)
	}
}

// consumeQuotedTagOption is consumeTagOption that also accepts a
// single-quoted string, as the `format` option does in v2.
func consumeQuotedTagOption(in string) (string, int, error) {
	i := strings.IndexByte(in, ',')
	if i < 0 {
		i = len(in)
	}
	switch r, _ := utf8.DecodeRuneInString(in); {
	case r == '_' || unicode.IsLetter(r):
		n := len(in) - len(strings.TrimLeftFunc(in, isLetterOrDigit))
		return in[:n], n, nil
	case r == '\'':
		var inEscape bool
		b := []byte{'"'}
		n := len(`'`)
		for len(in) > n {
			r, rn := utf8.DecodeRuneInString(in[n:])
			switch {
			case inEscape:
				if r == '\'' {
					b = b[:len(b)-1] // `\'` => `'`
				}
				inEscape = false
			case r == '\\':
				inEscape = true
			case r == '"':
				b = append(b, '\\') // `"` => `\"`
			case r == '\'':
				b = append(b, '"')
				n += len(`'`)
				out, err := strconv.Unquote(string(b))
				if err != nil {
					return in[:i], i, fmt.Errorf("invalid single-quoted string: %s", in[:n])
				}
				return out, n, nil
			}
			b = append(b, in[n:][:rn]...)
			n += rn
		}
		if n > 10 {
			n = 10
		}
		return in[:i], i, fmt.Errorf("single-quoted string not terminated: %s...", in[:n])
	case len(in) == 0:
		return in[:i], i, io.ErrUnexpectedEOF
	default:
		return in[:i], i, fmt.Errorf("invalid character %q at start of option (expecting Unicode letter or single quote)", r)
	}
}

func isLetterOrDigit(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r)
}

// boolsCompare orders false before true.
func boolsCompare(x, y bool) int {
	switch {
	case !x && y:
		return -1
	case x && !y:
		return +1
	}
	return 0
}

// foldName folds a name for case-insensitive matching that also ignores
// dashes and underscores (v2 fold.go).
func foldName(in []byte) []byte {
	var arr [32]byte
	return appendFoldedName(arr[:0], in)
}

func appendFoldedName(out, in []byte) []byte {
	for i := 0; i < len(in); {
		if c := in[i]; c < utf8.RuneSelf {
			if c != '_' && c != '-' {
				if 'a' <= c && c <= 'z' {
					c -= 'a' - 'A'
				}
				out = append(out, c)
			}
			i++
			continue
		}
		r, n := utf8.DecodeRune(in[i:])
		out = utf8.AppendRune(out, foldRune(r))
		i += n
	}
	return out
}

// foldRune returns the same rune for every rune in a fold set.
func foldRune(r rune) rune {
	for {
		r2 := unicode.SimpleFold(r)
		if r2 <= r {
			return r2
		}
		r = r2
	}
}
```

```go
// The string cache is adapted from the Go standard library
// (src/encoding/json/v2/intern.go), Copyright 2022 The Go Authors, under the
// BSD-style license in LICENSE-GO.

package simdjson

import (
	"encoding/binary"
	"math/bits"
)

// stringCache remembers recently decoded short strings, so repeated values
// (enum-like fields, map keys) are allocated once per Parser.
type stringCache = [256]string

// makeString returns the string form of b, reusing an equal string from c.
func makeString(c *stringCache, b []byte) string {
	const minCachedLen, maxCachedLen = 2, 256 // one-byte strings are interned by the runtime
	if len(b) < minCachedLen || len(b) > maxCachedLen {
		return string(b)
	}
	// Hash a fixed-width prefix and suffix, so hashing is constant time.
	var h uint32
	switch {
	case len(b) >= 8:
		lo := binary.LittleEndian.Uint64(b[:8])
		hi := binary.LittleEndian.Uint64(b[len(b)-8:])
		h = hash64(uint32(lo), uint32(lo>>32)) ^ hash64(uint32(hi), uint32(hi>>32))
	case len(b) >= 4:
		h = hash64(binary.LittleEndian.Uint32(b[:4]), binary.LittleEndian.Uint32(b[len(b)-4:]))
	default:
		h = hash64(uint32(binary.LittleEndian.Uint16(b[:2])), uint32(binary.LittleEndian.Uint16(b[len(b)-2:])))
	}
	i := h % uint32(len(*c))
	if s := (*c)[i]; s == string(b) {
		return s
	}
	s := string(b)
	(*c)[i] = s
	return s
}

// hash64 is XXH32 of an 8-byte input without the final avalanche step.
func hash64(lo, hi uint32) uint32 {
	const prime3, prime4, prime5 = 0xc2b2ae3d, 0x27d4eb2f, 0x165667b1
	h := prime5 + uint32(8)
	h += lo * prime3
	h = bits.RotateLeft32(h, 17) * prime4
	h += hi * prime3
	return bits.RotateLeft32(h, 17) * prime4
}
```

- [ ] **Step 4: Decoding**

```go
// parseUint is adapted from the Go standard library
// (src/encoding/json/internal/jsonwire/decode.go), Copyright 2023 The Go
// Authors, under the BSD-style license in LICENSE-GO.

package simdjson

import (
	"bytes"
	"encoding/base64"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"sync"
)

// Unmarshal decodes the JSON document data into the Go value v points to,
// with the semantics of encoding/json/v2's Unmarshal under its default options
// (changed by opts). Malformed JSON gives a *jsontext.SyntacticError wrapping
// this package's Err* value; a document that does not fit v gives a
// *json.SemanticError (encoding/json/v2). After an error the contents of v
// are unspecified.
func Unmarshal(data []byte, v any, opts ...Option) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return &jsonv2.SemanticError{GoType: reflect.TypeOf(v), Err: errNonNilPointer}
	}
	if bytes.HasPrefix(data, bom) { // v2 does not skip a byte-order mark
		return &jsontext.SyntacticError{Err: ErrTape}
	}
	b := binders.Get().(*binder)
	defer putBinder(b)
	doc, err := b.p.Parse(data)
	if err != nil {
		return &jsontext.SyntacticError{Err: err}
	}
	d := decodeState{doc: doc, buf: data, opts: makeOptions(opts), strs: &b.strs}
	return codecFor(rv.Type().Elem()).decode(&d, doc.Root(), rv.Elem(), 0)
}

var errNonNilPointer = errors.New("value must be passed as a non-nil pointer reference")

// binder is a Parser in binding mode with its string cache. v2 limits
// nesting to 10000 levels, counting empty arrays and objects, as binding
// mode does (MaxDepth counts the root too).
type binder struct {
	p    Parser
	strs stringCache
}

var binders = sync.Pool{New: func() any {
	return &binder{p: Parser{MaxDepth: 10001, BigIntAsString: true, binding: true}}
}}

// putBinder returns b to the pool unless it grew large, so one big document
// does not pin its buffers for the life of the process.
func putBinder(b *binder) {
	if cap(b.p.doc.tape) > 1<<20 || cap(b.p.doc.strings) > 8<<20 {
		return
	}
	binders.Put(b)
}

// decodeState is the state of one Unmarshal call.
type decodeState struct {
	doc  *Document
	buf  []byte // the input, for the raw text of values (Document.offs)
	opts options
	strs *stringCache
}

// raw returns the input text of e (a value or an object name).
func (d *decodeState) raw(e Element) []byte {
	start := int(d.doc.offs[e.i])
	switch e.tag() {
	case tagStartArray, tagStartObject:
		return d.buf[start : int(d.doc.offs[e.next()-1])+1]
	case tagString:
		return d.buf[start : start+stringEnd(d.buf[start:])]
	case tagTrue, tagNull:
		return d.buf[start : start+4]
	case tagFalse:
		return d.buf[start : start+5]
	}
	n := start // a number
	for n < len(d.buf) && !isStructuralOrSpace[d.buf[n]] {
		n++
	}
	return d.buf[start:n]
}

// stringEnd returns the length of the JSON string at the start of b.
func stringEnd(b []byte) int {
	for i := 1; i < len(b); i++ {
		switch b[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return len(b)
}

// kind is the jsontext.Kind of e.
func kind(e Element) jsontext.Kind {
	switch t := e.tag(); t {
	case tagInt64, tagUint64, tagDouble, tagBigInt:
		return '0'
	case tagFalse:
		return 'f'
	default:
		return jsontext.Kind(t)
	}
}

// semErr reports that e cannot be decoded into Go type t.
func (d *decodeState) semErr(e Element, t reflect.Type, err error) error {
	return &jsonv2.SemanticError{JSONPointer: d.pointer(e), JSONKind: kind(e), GoType: t, Err: err}
}

// valueErr is semErr for decoders that, in v2, read the whole value before
// checking it (strings, numbers, bytes, time.Time, text and JSON methods): a
// duplicate name inside the value is reported instead.
func (d *decodeState) valueErr(e Element, t reflect.Type, err error) error {
	if e.tag() == tagStartArray || e.tag() == tagStartObject {
		if dup := d.checkDups(e); dup != nil {
			return dup
		}
	}
	return d.semErr(e, t, err)
}

// dupErr reports a duplicate object name at name element k.
func (d *decodeState) dupErr(k Element) error {
	return &jsontext.SyntacticError{JSONPointer: d.pointer(k), Err: jsontext.ErrDuplicateName}
}

// pointer returns the JSON Pointer of the value or name at tape index
// target.i, found by descending from the root. It runs only on errors.
func (d *decodeState) pointer(target Element) jsontext.Pointer {
	var p []byte
	e := d.doc.Root()
	for e.i != target.i {
		switch e.tag() {
		case tagStartArray:
			i, end := e.span()
			k := 0
			for i < end && !(target.i >= i && target.i < (Element{d.doc, i}).next()) {
				i = Element{d.doc, i}.next()
				k++
			}
			p = strconv.AppendInt(append(p, '/'), int64(k), 10)
			e = Element{d.doc, i}
		case tagStartObject:
			i, end := e.span()
			for i < end {
				v := Element{d.doc, i + 1}
				if target.i == i || target.i >= v.i && target.i < v.next() {
					break
				}
				i = v.next()
			}
			p = append(p, '/')
			for _, c := range (Element{d.doc, i}).rawString() { // RFC 6901 escaping
				switch c {
				case '~':
					p = append(p, "~0"...)
				case '/':
					p = append(p, "~1"...)
				default:
					p = append(p, c)
				}
			}
			if target.i == i {
				return jsontext.Pointer(p)
			}
			e = Element{d.doc, i + 1}
		default:
			return jsontext.Pointer(p)
		}
	}
	return jsontext.Pointer(p)
}

// checkDups returns an error if any object within e repeats a name: v2
// checks every object it reads, including skipped values. Stage 2 recorded
// the first repeated name of each object (Document.dups).
func (d *decodeState) checkDups(e Element) error {
	if len(d.doc.dups) == 0 {
		return nil
	}
	if k, ok := slices.BinarySearch(d.doc.dups, uint32(e.i)); ok || k < len(d.doc.dups) && int(d.doc.dups[k]) < e.next() {
		return d.dupErr(Element{d.doc, int(d.doc.dups[k])})
	}
	return nil
}

// firstDup returns the tape index of the first repeated name of object o
// itself (not of objects nested in it), or -1.
func (d *decodeState) firstDup(o Element) int {
	if len(d.doc.dups) == 0 {
		return -1
	}
	k, _ := slices.BinarySearch(d.doc.dups, uint32(o.i))
	end := o.next()
	for ; k < len(d.doc.dups) && int(d.doc.dups[k]) < end; k++ {
		for name := range (Object{o}).keyIndices() {
			if name == int(d.doc.dups[k]) {
				return name
			}
		}
	}
	return -1
}

// keyIndices iterates over the tape indices of an object's names.
func (o Object) keyIndices() func(func(int) bool) {
	return func(yield func(int) bool) {
		i, end := o.e.span()
		for i < end {
			if !yield(i) {
				return
			}
			i = Element{o.e.doc, i + 1}.next()
		}
	}
}

var (
	errInvalidStringTag = errors.New("invalid use of `string` tag option")
	errNonStringValue   = errors.New("JSON value must be string type")
	errArrayUnderflow   = errors.New("too few array elements")
	errArrayOverflow    = errors.New("too many array elements")
	errNilInterface     = errors.New("cannot derive concrete type for nil interface with finite type set")
	errNilField         = errors.New("cannot set embedded pointer to unexported struct type")
	errAmbiguousName    = errors.New("ambiguous object name")
	errNoDefault        = errors.New("no default representation")
)

func makeDefaultDecoder(t reflect.Type) decodeFunc {
	switch t.Kind() {
	case reflect.Bool:
		return decodeBool
	case reflect.String:
		return decodeString
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return decodeInt
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return decodeUint
	case reflect.Float32, reflect.Float64:
		return decodeFloat
	case reflect.Map:
		return makeMapDecoder(t)
	case reflect.Struct:
		return makeStructDecoder(t)
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 && t.Elem().PkgPath() == "" {
			return decodeBytes
		}
		return makeSliceDecoder(t)
	case reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 && t.Elem().PkgPath() == "" {
			return decodeBytes
		}
		return makeArrayDecoder(t)
	case reflect.Pointer:
		return makePointerDecoder(t)
	case reflect.Interface:
		return makeInterfaceDecoder(t)
	}
	return func(d *decodeState, e Element, v reflect.Value, _ uint8) error {
		return d.semErr(e, v.Type(), nil) // complex, chan, func, …: no JSON form, even for null
	}
}

func decodeBool(d *decodeState, e Element, v reflect.Value, mode uint8) error {
	if mode&modeStringTag != 0 {
		return d.semErr(e, v.Type(), errInvalidStringTag)
	}
	switch e.tag() {
	case tagNull, tagFalse:
		v.SetBool(false)
	case tagTrue:
		v.SetBool(true)
	default:
		return d.semErr(e, v.Type(), nil)
	}
	return nil
}

func decodeString(d *decodeState, e Element, v reflect.Value, mode uint8) error {
	if mode&modeStringTag != 0 {
		return d.semErr(e, v.Type(), errInvalidStringTag)
	}
	switch e.tag() {
	case tagNull:
		v.SetString("")
	case tagString:
		v.SetString(makeString(d.strs, e.rawString()))
	default:
		return d.valueErr(e, v.Type(), nil)
	}
	return nil
}

// stringified reports whether e holds a number in the form mode requires:
// a plain number, or (for the `string` tag option and object names) a
// string whose contents s are then the number's text.
func stringified(e Element, mode uint8) (s []byte, quoted, ok bool) {
	switch e.tag() {
	case tagString:
		if mode != 0 {
			return e.rawString(), true, true
		}
	case tagInt64, tagUint64, tagDouble, tagBigInt:
		return nil, false, mode == 0
	}
	return nil, false, false
}

func decodeInt(d *decodeState, e Element, v reflect.Value, mode uint8) error {
	if e.tag() == tagNull {
		v.SetInt(0)
		return nil
	}
	s, quoted, ok := stringified(e, mode)
	if !ok {
		return d.valueErr(e, v.Type(), nil)
	}
	bits := v.Type().Bits()
	var neg bool
	var n uint64
	if quoted {
		neg = len(s) > 0 && s[0] == '-'
		if neg {
			s = s[1:]
		}
		var good bool
		if n, good = parseUint(s); !good && n != math.MaxUint64 {
			return d.valueErr(e, v.Type(), strconv.ErrSyntax)
		}
	} else {
		switch e.tag() {
		case tagInt64:
			i := int64(e.value())
			neg, n = i < 0, uint64(i)
			if neg {
				n = -n
			}
		case tagUint64:
			n = e.value()
		case tagBigInt:
			n = math.MaxUint64 // overflow
		default: // a float: JSON fractions and exponents do not parse as integers
			return d.valueErr(e, v.Type(), strconv.ErrSyntax)
		}
	}
	if limit := uint64(1) << (bits - 1); neg && n > limit || !neg && n > limit-1 {
		return d.valueErr(e, v.Type(), strconv.ErrRange)
	}
	if neg {
		v.SetInt(int64(-n))
	} else {
		v.SetInt(int64(n))
	}
	return nil
}

func decodeUint(d *decodeState, e Element, v reflect.Value, mode uint8) error {
	if e.tag() == tagNull {
		v.SetUint(0)
		return nil
	}
	s, quoted, ok := stringified(e, mode)
	if !ok {
		return d.valueErr(e, v.Type(), nil)
	}
	var n uint64
	switch {
	case quoted:
		var good bool
		if n, good = parseUint(s); !good {
			if n != math.MaxUint64 {
				return d.valueErr(e, v.Type(), strconv.ErrSyntax)
			}
			return d.valueErr(e, v.Type(), strconv.ErrRange)
		}
	case e.tag() == tagInt64:
		i := int64(e.value())
		if i < 0 || i == 0 && d.raw(e)[0] == '-' { // "-0" does not parse as unsigned
			return d.valueErr(e, v.Type(), strconv.ErrSyntax)
		}
		n = uint64(i)
	case e.tag() == tagUint64:
		n = e.value()
	case e.tag() == tagBigInt:
		if e.rawString()[0] == '-' {
			return d.valueErr(e, v.Type(), strconv.ErrSyntax)
		}
		return d.valueErr(e, v.Type(), strconv.ErrRange)
	default:
		return d.valueErr(e, v.Type(), strconv.ErrSyntax)
	}
	if bits := v.Type().Bits(); bits < 64 && n > 1<<bits-1 {
		return d.valueErr(e, v.Type(), strconv.ErrRange)
	}
	v.SetUint(n)
	return nil
}

// parseUint is v2's jsonwire.ParseUint: a decimal without sign, leading
// zeros or other characters. On overflow it returns (math.MaxUint64, false).
func parseUint(b []byte) (v uint64, ok bool) {
	var n int
	for ; len(b) > n && '0' <= b[n] && b[n] <= '9'; n++ {
		v = 10*v + uint64(b[n]-'0')
	}
	switch {
	case n == 0 || len(b) != n || (b[0] == '0' && string(b) != "0"):
		return 0, false
	case n >= 20 && (b[0] != '1' || v < 1e19 || n > 20):
		return math.MaxUint64, false
	}
	return v, true
}

func decodeFloat(d *decodeState, e Element, v reflect.Value, mode uint8) error {
	if e.tag() == tagNull {
		v.SetFloat(0)
		return nil
	}
	s, quoted, ok := stringified(e, mode)
	if !ok {
		return d.valueErr(e, v.Type(), nil)
	}
	bits := v.Type().Bits()
	if quoted && !isNumber(s) {
		return d.valueErr(e, v.Type(), strconv.ErrSyntax)
	}
	f, err := d.number(e, s, quoted, bits)
	if err != nil {
		return d.valueErr(e, v.Type(), err)
	}
	v.SetFloat(f)
	return nil
}

// number converts number e (or the quoted number text s) to a float of the
// given size, as strconv.ParseFloat would from the text. It returns
// strconv.ErrRange on overflow.
func (d *decodeState) number(e Element, s []byte, quoted bool, bits int) (float64, error) {
	var f float64
	switch {
	case quoted || bits == 32: // float32 rounds once, from the text
		if !quoted {
			s = d.raw(e)
		}
		var err error
		if f, err = strconv.ParseFloat(string(s), bits); err != nil {
			return f, strconv.ErrRange // the text is a valid JSON number
		}
		return f, nil
	case e.tag() == tagDouble:
		f = math.Float64frombits(e.value())
	case e.tag() == tagInt64:
		f = float64(int64(e.value()))
		if f == 0 && d.raw(e)[0] == '-' {
			f = math.Copysign(0, -1)
		}
	case e.tag() == tagUint64:
		f = float64(e.value())
	default: // tagBigInt
		f, _ = strconv.ParseFloat(string(e.rawString()), 64)
	}
	if math.IsInf(f, 0) {
		return f, strconv.ErrRange
	}
	return f, nil
}

// isNumber reports whether b is exactly one JSON number.
func isNumber(b []byte) bool {
	i := 0
	if i < len(b) && b[i] == '-' {
		i++
	}
	switch {
	case i < len(b) && b[i] == '0':
		i++
	case i < len(b) && '1' <= b[i] && b[i] <= '9':
		for i < len(b) && isDigit(b[i]) {
			i++
		}
	default:
		return false
	}
	if i < len(b) && b[i] == '.' {
		i++
		if i == len(b) || !isDigit(b[i]) {
			return false
		}
		for i < len(b) && isDigit(b[i]) {
			i++
		}
	}
	if i < len(b) && (b[i] == 'e' || b[i] == 'E') {
		i++
		if i < len(b) && (b[i] == '+' || b[i] == '-') {
			i++
		}
		if i == len(b) || !isDigit(b[i]) {
			return false
		}
		for i < len(b) && isDigit(b[i]) {
			i++
		}
	}
	return i == len(b)
}

// decodeBytes decodes base64 (RFC 4648 §4, padding required) into []byte or [N]byte.
func decodeBytes(d *decodeState, e Element, v reflect.Value, mode uint8) error {
	if mode&modeStringTag != 0 {
		return d.semErr(e, v.Type(), errInvalidStringTag)
	}
	switch e.tag() {
	case tagNull:
		v.SetZero()
		return nil
	case tagString:
	default:
		return d.valueErr(e, v.Type(), nil)
	}
	s := e.rawString()
	var dst []byte
	if v.Kind() == reflect.Slice {
		dst = v.Bytes()[:0]
	}
	b, err := base64.StdEncoding.AppendDecode(dst, s)
	if err != nil {
		return d.valueErr(e, v.Type(), err)
	}
	if len(s) != base64.StdEncoding.EncodedLen(len(b)) { // base64 skips '\r' and '\n'; RFC 4648 does not
		i := bytes.IndexAny(s, "\r\n")
		return d.valueErr(e, v.Type(), fmt.Errorf("illegal character %q at offset %d", s[i], i))
	}
	if v.Kind() == reflect.Array {
		dst := v.Bytes()
		clear(dst[copy(dst, b):])
		if len(b) != len(dst) {
			return d.valueErr(e, v.Type(), fmt.Errorf("decoded length of %d mismatches array length of %d", len(b), len(dst)))
		}
		return nil
	}
	if b == nil {
		b = []byte{}
	}
	v.SetBytes(b)
	return nil
}

func makeSliceDecoder(t reflect.Type) decodeFunc {
	elem := codecFor(t.Elem())
	empty := reflect.MakeSlice(t, 0, 0)
	return func(d *decodeState, e Element, v reflect.Value, mode uint8) error {
		if mode&modeStringTag != 0 {
			return d.semErr(e, t, errInvalidStringTag)
		}
		switch e.tag() {
		case tagNull:
			v.SetZero()
			return nil
		case tagStartArray:
		default:
			return d.semErr(e, t, nil)
		}
		n := e.length(1)
		if n == 0 {
			v.Set(empty)
			return nil
		}
		if v.Cap() < n {
			v.Set(reflect.MakeSlice(t, n, n))
		} else {
			v.SetLen(n)
			for i := range n {
				v.Index(i).SetZero()
			}
		}
		i := 0
		for c := range e.items() {
			if err := elem.decode(d, c, v.Index(i), 0); err != nil {
				return err
			}
			i++
		}
		return nil
	}
}

func makeArrayDecoder(t reflect.Type) decodeFunc {
	elem := codecFor(t.Elem())
	n := t.Len()
	return func(d *decodeState, e Element, v reflect.Value, mode uint8) error {
		if mode&modeStringTag != 0 {
			return d.semErr(e, t, errInvalidStringTag)
		}
		switch e.tag() {
		case tagNull:
			v.SetZero()
			return nil
		case tagStartArray:
		default:
			return d.semErr(e, t, nil)
		}
		i := 0
		var lengthErr error
		for c := range e.items() {
			if i >= n {
				if err := d.checkDups(c); err != nil { // skipped, as in v2
					return err
				}
				lengthErr = errArrayOverflow
				continue
			}
			ev := v.Index(i)
			ev.SetZero()
			if err := elem.decode(d, c, ev, 0); err != nil {
				return err
			}
			i++
		}
		for ; i < n; i++ {
			v.Index(i).SetZero()
			lengthErr = errArrayUnderflow
		}
		if lengthErr != nil {
			return &jsonv2.SemanticError{JSONPointer: d.pointer(e), JSONKind: '[', GoType: t, Err: lengthErr}
		}
		return nil
	}
}

func makePointerDecoder(t reflect.Type) decodeFunc {
	elem := codecFor(t.Elem())
	return func(d *decodeState, e Element, v reflect.Value, mode uint8) error {
		if e.tag() == tagNull {
			v.SetZero()
			return nil
		}
		if v.IsNil() {
			v.Set(reflect.New(t.Elem()))
		}
		return elem.decode(d, e, v.Elem(), mode)
	}
}

var anyType = reflect.TypeFor[any]()

func makeInterfaceDecoder(t reflect.Type) decodeFunc {
	isAny := t == anyType || anyType.Implements(t)
	return func(d *decodeState, e Element, v reflect.Value, mode uint8) error {
		if mode&modeStringTag != 0 {
			return d.semErr(e, t, errInvalidStringTag)
		}
		if e.tag() == tagNull {
			v.SetZero()
			return nil
		}
		if v.IsNil() {
			if !isAny {
				return d.semErr(e, t, errNilInterface)
			}
			x, err := d.decodeAny(e)
			if x != nil {
				v.Set(reflect.ValueOf(x))
			}
			return err
		}
		// Decode into a copy of the existing value, then store it back.
		cv := reflect.New(v.Elem().Type()).Elem()
		cv.Set(v.Elem())
		err := codecFor(cv.Type()).decode(d, e, cv, mode&modeName)
		v.Set(cv)
		return err
	}
}

var float64Type = reflect.TypeFor[float64]()

// decodeAny decodes e into the Go value v2 chooses for a nil any: bool,
// string, float64, map[string]any or []any.
func (d *decodeState) decodeAny(e Element) (any, error) {
	switch e.tag() {
	case tagNull:
		return nil, nil
	case tagTrue:
		return true, nil
	case tagFalse:
		return false, nil
	case tagString:
		return makeString(d.strs, e.rawString()), nil
	case tagStartArray:
		a := []any{}
		for c := range e.items() {
			x, err := d.decodeAny(c)
			a = append(a, x)
			if err != nil {
				return a, err
			}
		}
		return a, nil
	case tagStartObject:
		m := make(map[string]any, e.length(2))
		dup := d.firstDup(e)
		for k, c := range (Object{e}).AllBytes() {
			if c.i-1 == dup {
				return m, d.dupErr(Element{d.doc, dup})
			}
			name := makeString(d.strs, k)
			x, err := d.decodeAny(c)
			m[name] = x
			if err != nil {
				return m, err
			}
		}
		return m, nil
	}
	f, err := d.number(e, nil, false, 64)
	if err != nil {
		return nil, &jsonv2.SemanticError{JSONPointer: d.pointer(e), JSONKind: '0', GoType: float64Type, Err: err}
	}
	return f, nil
}

func makeMapDecoder(t reflect.Type) decodeFunc {
	key, val := codecFor(t.Key()), codecFor(t.Elem())
	emptyStruct := reflect.TypeFor[struct{}]()
	return func(d *decodeState, e Element, v reflect.Value, mode uint8) error {
		if mode&modeStringTag != 0 {
			return d.semErr(e, t, errInvalidStringTag)
		}
		switch e.tag() {
		case tagNull:
			v.SetZero()
			return nil
		case tagStartObject:
		default:
			return d.semErr(e, t, nil)
		}
		if v.IsNil() {
			v.Set(reflect.MakeMap(t))
		}
		key.init()
		// Keys of a kind with a unique representation are duplicates when
		// they decode to a key already present (so "0" and "-0" collide as
		// integers); other keys are compared as names, as in v2.
		unique := !key.nonDefault && uniqueKeyKind(t.Key().Kind())
		var seen reflect.Value // keys from the input, if v had entries before
		if v.Len() > 0 {
			seen = reflect.MakeMap(reflect.MapOf(t.Key(), emptyStruct))
		}
		k := reflect.New(t.Key()).Elem()
		x := reflect.New(t.Elem()).Elem()
		dup := -1
		if !unique {
			dup = d.firstDup(e)
		}
		first, end := e.span()
		for i := first; i < end; {
			ke, ve := Element{d.doc, i}, Element{d.doc, i + 1}
			i = ve.next()
			if ke.i == dup {
				return d.dupErr(ke)
			}
			k.SetZero()
			if err := key.decode(d, ke, k, modeName); err != nil {
				return err
			}
			if k.Kind() == reflect.Interface && !k.IsNil() && !k.Elem().Type().Comparable() {
				return d.semErr(ke, t, fmt.Errorf("invalid incomparable key type %v", k.Elem().Type()))
			}
			if old := v.MapIndex(k); old.IsValid() {
				if !seen.IsValid() || seen.MapIndex(k).IsValid() {
					return d.dupErr(ke)
				}
				x.Set(old)
			} else {
				x.SetZero()
			}
			err := val.decode(d, ve, x, 0)
			v.SetMapIndex(k, x)
			if seen.IsValid() {
				seen.SetMapIndex(k, reflect.Zero(emptyStruct))
			}
			if err != nil {
				return err
			}
		}
		return nil
	}
}

// uniqueKeyKind reports whether every JSON name of a map key of kind k
// decodes to a different Go value and back (v2 mapKeyWithUniqueRepresentation).
func uniqueKeyKind(k reflect.Kind) bool {
	switch k {
	case reflect.Bool, reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	}
	return false
}

func makeStructDecoder(t reflect.Type) decodeFunc {
	var (
		once   sync.Once
		fields structFields
		errFs  *jsonv2.SemanticError
	)
	return func(d *decodeState, e Element, v reflect.Value, mode uint8) error {
		if mode&modeStringTag != 0 {
			return d.semErr(e, t, errInvalidStringTag)
		}
		switch e.tag() {
		case tagNull:
			v.SetZero()
			return nil
		case tagStartObject:
		default:
			return d.semErr(e, t, nil)
		}
		once.Do(func() { fields, errFs = makeStructFields(t) })
		if errFs != nil {
			return &jsonv2.SemanticError{JSONPointer: d.pointer(e), JSONKind: '{', GoType: errFs.GoType, Err: errFs.Err}
		}
		var seenIDs bitSet
		var err error
		dup := d.firstDup(e)
		fold := d.opts.caseInsensitive || fields.foldable
		first, end := e.span()
		for i := first; i < end; {
			ke, ve := Element{d.doc, i}, Element{d.doc, i + 1}
			i = ve.next()
			if ke.i == dup {
				return d.dupErr(ke)
			}
			name := ke.rawString()
			f := fields.lookup(name)
			if f == nil && fold {
				matches := 0
				for _, f2 := range fields.lookupByFoldedName(name) {
					if f2.matchFoldedName(d.opts.caseInsensitive) {
						if f == nil {
							f = f2 // breadth-first order
						}
						matches++
					}
				}
				if matches > 1 {
					return d.semErr(ke, t, errAmbiguousName)
				}
			}
			if f == nil { // an unknown name; repeats were caught by dup above
				if d.opts.rejectUnknown {
					return &jsonv2.SemanticError{JSONPointer: d.pointer(ke), JSONKind: '"', GoType: t, Err: jsonv2.ErrUnknownName}
				}
				if err := d.checkDups(ve); err != nil { // skipped, as in v2
					return err
				}
				continue
			}
			if !seenIDs.insert(f.id) {
				return d.dupErr(ke)
			}
			var fv reflect.Value
			if len(f.index) == 1 {
				fv = v.Field(f.index[0])
			} else if fv, err = fieldByIndexAlloc(v, f.index); err != nil {
				return d.semErr(ve, t, err)
			}
			var fmode uint8
			if f.string {
				fmode = modeStringTag
			}
			if err := f.cod.decode(d, ve, fv, fmode); err != nil {
				return err
			}
		}
		return nil
	}
}

// fieldByIndexAlloc is v.FieldByIndex that allocates nil embedded pointers.
func fieldByIndexAlloc(v reflect.Value, index []int) (reflect.Value, error) {
	for n, i := range index {
		if n > 0 && v.Kind() == reflect.Pointer {
			if v.IsNil() {
				if !v.CanSet() {
					return reflect.Value{}, errNilField
				}
				v.Set(reflect.New(v.Type().Elem()))
			}
			v = v.Elem()
		}
		v = v.Field(i)
	}
	return v, nil
}

// bitSet is a set of small non-negative integers.
type bitSet struct {
	lo uint64
	hi []uint64
}

// insert adds i and reports whether it was not already present.
func (s *bitSet) insert(i int) bool {
	if i < 64 {
		had := s.lo&(1<<i) != 0
		s.lo |= 1 << i
		return !had
	}
	w := i/64 - 1
	for len(s.hi) <= w {
		s.hi = append(s.hi, 0)
	}
	had := s.hi[w]&(1<<(i%64)) != 0
	s.hi[w] |= 1 << (i % 64)
	return !had
}
```

- [ ] **Step 5: Encoding, with the verbatim-number hook in serialize.go**

```go
// String quoting and float formatting are adapted from the Go standard
// library (src/encoding/json/internal/jsonwire/encode.go), Copyright 2023 The
// Go Authors, under the BSD-style license in LICENSE-GO.

package simdjson

import (
	"bytes"
	"encoding/base64"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// Marshal returns the JSON encoding of v, with the semantics of
// encoding/json/v2's Marshal under its default options (changed by opts).
func Marshal(v any, opts ...Option) ([]byte, error) {
	bp, _ := bufPool.Get().(*[]byte)
	if bp == nil {
		bp = new([]byte)
	}
	b, err := MarshalAppend((*bp)[:0], v, opts...)
	var out []byte
	if err == nil {
		out = bytes.Clone(b) // exactly sized, like v2's
	}
	if cap(b) <= maxPooledBuf {
		*bp = b
		bufPool.Put(bp)
	}
	return out, err
}

// bufPool holds Marshal's scratch buffers (*[]byte); larger ones are dropped.
var bufPool sync.Pool

const maxPooledBuf = 8 << 20

// MarshalAppend appends the JSON encoding of v to dst. On error it returns dst
// unchanged.
func MarshalAppend(dst []byte, v any, opts ...Option) ([]byte, error) {
	s := encodeState{buf: dst, opts: makeOptions(opts)}
	if v == nil {
		return append(dst, "null"...), nil
	}
	rv := reflect.ValueOf(v)
	av := reflect.New(rv.Type()).Elem() // addressable, so pointer-receiver methods run
	av.Set(rv)
	if err := codecFor(av.Type()).encode(&s, av, 0); err != nil {
		return dst, err
	}
	return s.buf, nil
}

// encodeState is the state of one Marshal call.
type encodeState struct {
	buf     []byte
	opts    options
	scratch []byte // for AppendText
	depth   int    // open arrays and objects
	seen    map[any]struct{}
}

// maxDepth and startCycleCheck are v2's nesting limit and the depth after
// which it starts looking for pointer cycles.
const (
	maxDepth        = 10000
	startCycleCheck = 1000
)

func (s *encodeState) semErr(t reflect.Type, err error) error {
	return &jsonv2.SemanticError{GoType: t, Err: err}
}

// methodErr wraps an error from a user's marshal method in a SemanticError,
// unless it already is one.
func (s *encodeState) methodErr(t reflect.Type, err error) error {
	if _, ok := err.(*jsonv2.SemanticError); ok {
		return err
	}
	return s.semErr(t, err)
}

// open counts a new array or object, enforcing v2's depth limit.
func (s *encodeState) open() error {
	if s.depth++; s.depth > maxDepth {
		return &jsontext.SyntacticError{Err: ErrDepth}
	}
	return nil
}

// visit records pointer-like value v past the cycle-check depth; it fails if
// v is already being encoded.
func (s *encodeState) visit(t reflect.Type, v reflect.Value) (leave func(), err error) {
	if s.depth < startCycleCheck {
		return func() {}, nil
	}
	if s.seen == nil {
		s.seen = make(map[any]struct{})
	}
	type ptrKey struct {
		p   uintptr
		t   reflect.Type
		len int
	}
	k := ptrKey{v.Pointer(), t, 0}
	if v.Kind() == reflect.Slice {
		k.len = v.Len()
	}
	if _, ok := s.seen[k]; ok {
		return nil, s.semErr(t, fmt.Errorf("encountered a cycle via %v", t))
	}
	s.seen[k] = struct{}{}
	return func() { delete(s.seen, k) }, nil
}

// appendQuoted appends text from a text method as a JSON string.
func (s *encodeState) appendQuoted(t reflect.Type, b []byte) error {
	var ok bool
	if s.buf, ok = appendQuoteBytes(s.buf, b); !ok {
		return s.semErr(t, &jsontext.SyntacticError{Err: ErrUTF8})
	}
	return nil
}

// appendQuote quotes s as v2 does by default (minimal escaping; invalid
// UTF-8 is replaced by U+FFFD). Use appendQuoteBytes to detect invalid UTF-8.
func appendQuote(dst []byte, s string) []byte {
	dst, _ = appendQuoteBytes(dst, []byte(s))
	return dst
}

// appendQuoteBytes is v2's jsonwire.AppendQuote without the HTML and JS
// escaping options: only '"', '\\' and bytes below 0x20 are escaped. It
// reports false if src is not valid UTF-8.
func appendQuoteBytes(dst, src []byte) ([]byte, bool) {
	ok := true
	dst = append(dst, '"')
	i := 0
	for n := 0; n < len(src); {
		c := src[n]
		if c < utf8.RuneSelf {
			n++
			if c >= 0x20 && c != '"' && c != '\\' {
				continue
			}
			dst = append(dst, src[i:n-1]...)
			switch c {
			case '"', '\\':
				dst = append(dst, '\\', c)
			case '\b':
				dst = append(dst, `\b`...)
			case '\f':
				dst = append(dst, `\f`...)
			case '\n':
				dst = append(dst, `\n`...)
			case '\r':
				dst = append(dst, `\r`...)
			case '\t':
				dst = append(dst, `\t`...)
			default:
				const hex = "0123456789abcdef"
				dst = append(dst, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xF])
			}
			i = n
			continue
		}
		r, rn := utf8.DecodeRune(src[n:])
		n += rn
		if r == utf8.RuneError && rn == 1 {
			ok = false
			dst = append(dst, src[i:n-rn]...)
			dst = append(dst, "�"...)
			i = n
		}
	}
	dst = append(dst, src[i:]...)
	return append(dst, '"'), ok
}

// appendFloat is v2's jsonwire.AppendFloat: the shortest representation that
// round-trips at the given bit size, in exponent form below 1e-6 and from
// 1e21 on, with "e-09" shortened to "e-9".
func appendFloat(dst []byte, f float64, bits int) []byte {
	if bits == 32 {
		f = float64(float32(f))
	}
	abs := math.Abs(f)
	fmt := byte('f')
	if abs != 0 {
		if bits == 64 && (abs < 1e-6 || abs >= 1e21) ||
			bits == 32 && (float32(abs) < 1e-6 || float32(abs) >= 1e21) {
			fmt = 'e'
		}
	}
	dst = strconv.AppendFloat(dst, f, fmt, -1, bits)
	if fmt == 'e' {
		n := len(dst)
		if n >= 4 && dst[n-4] == 'e' && dst[n-3] == '-' && dst[n-2] == '0' {
			dst[n-2] = dst[n-1]
			dst = dst[:n-1]
		}
	}
	return dst
}

func makeDefaultEncoder(t reflect.Type) encodeFunc {
	switch t.Kind() {
	case reflect.Bool:
		return func(s *encodeState, v reflect.Value, mode uint8) error {
			if mode&modeStringTag != 0 {
				return s.semErr(t, errInvalidStringTag)
			}
			s.buf = strconv.AppendBool(s.buf, v.Bool())
			return nil
		}
	case reflect.String:
		return func(s *encodeState, v reflect.Value, mode uint8) error {
			if mode&modeStringTag != 0 {
				return s.semErr(t, errInvalidStringTag)
			}
			var ok bool
			if s.buf, ok = appendQuoteBytes(s.buf, []byte(v.String())); !ok {
				return &jsontext.SyntacticError{Err: ErrUTF8}
			}
			return nil
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return func(s *encodeState, v reflect.Value, mode uint8) error {
			s.appendNumber(mode, func(b []byte) []byte { return strconv.AppendInt(b, v.Int(), 10) })
			return nil
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return func(s *encodeState, v reflect.Value, mode uint8) error {
			s.appendNumber(mode, func(b []byte) []byte { return strconv.AppendUint(b, v.Uint(), 10) })
			return nil
		}
	case reflect.Float32, reflect.Float64:
		bits := t.Bits()
		return func(s *encodeState, v reflect.Value, mode uint8) error {
			f := v.Float()
			if math.IsNaN(f) || math.IsInf(f, 0) {
				return s.semErr(t, fmt.Errorf("unsupported value: %v", f))
			}
			s.appendNumber(mode, func(b []byte) []byte { return appendFloat(b, f, bits) })
			return nil
		}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 && t.Elem().PkgPath() == "" {
			return func(s *encodeState, v reflect.Value, mode uint8) error {
				if mode&modeStringTag != 0 {
					return s.semErr(t, errInvalidStringTag)
				}
				if s.opts.nilSliceAsNull && v.Kind() == reflect.Slice && v.IsNil() {
					s.buf = append(s.buf, "null"...)
					return nil
				}
				s.buf = append(s.buf, '"')
				s.buf = base64.StdEncoding.AppendEncode(s.buf, v.Bytes())
				s.buf = append(s.buf, '"')
				return nil
			}
		}
		return makeSequenceEncoder(t)
	case reflect.Map:
		return makeMapEncoder(t)
	case reflect.Struct:
		return makeStructEncoder(t)
	case reflect.Pointer:
		elem := codecFor(t.Elem())
		return func(s *encodeState, v reflect.Value, mode uint8) error {
			if v.IsNil() {
				s.buf = append(s.buf, "null"...)
				return nil
			}
			leave, err := s.visit(t, v)
			if err != nil {
				return err
			}
			defer leave()
			return elem.encode(s, v.Elem(), mode)
		}
	case reflect.Interface:
		return func(s *encodeState, v reflect.Value, mode uint8) error {
			if mode&modeStringTag != 0 {
				return s.semErr(t, errInvalidStringTag)
			}
			if v.IsNil() {
				s.buf = append(s.buf, "null"...)
				return nil
			}
			cv := reflect.New(v.Elem().Type()).Elem() // addressable copy
			cv.Set(v.Elem())
			return codecFor(cv.Type()).encode(s, cv, mode&modeName)
		}
	}
	return func(s *encodeState, v reflect.Value, mode uint8) error {
		return s.semErr(t, nil) // complex, chan, func, …: no JSON form
	}
}

// appendNumber appends a number, quoted for the `string` tag option and
// for object names.
func (s *encodeState) appendNumber(mode uint8, f func([]byte) []byte) {
	if mode == 0 {
		s.buf = f(s.buf)
		return
	}
	s.buf = append(s.buf, '"')
	s.buf = f(s.buf)
	s.buf = append(s.buf, '"')
}

func makeSequenceEncoder(t reflect.Type) encodeFunc {
	elem := codecFor(t.Elem())
	return func(s *encodeState, v reflect.Value, mode uint8) error {
		if mode&modeStringTag != 0 {
			return s.semErr(t, errInvalidStringTag)
		}
		if mode&modeName != 0 {
			return nonStringName(t)
		}
		if v.Kind() == reflect.Slice {
			if v.Len() == 0 {
				if s.opts.nilSliceAsNull && v.IsNil() {
					s.buf = append(s.buf, "null"...)
				} else {
					s.buf = append(s.buf, "[]"...)
				}
				return nil
			}
			leave, err := s.visit(t, v)
			if err != nil {
				return err
			}
			defer leave()
		}
		if err := s.open(); err != nil {
			return err
		}
		s.buf = append(s.buf, '[')
		for i := range v.Len() {
			if i > 0 {
				s.buf = append(s.buf, ',')
			}
			if err := elem.encode(s, v.Index(i), 0); err != nil {
				return err
			}
		}
		s.buf = append(s.buf, ']')
		s.depth--
		return nil
	}
}

func makeMapEncoder(t reflect.Type) encodeFunc {
	key, val := codecFor(t.Key()), codecFor(t.Elem())
	return func(s *encodeState, v reflect.Value, mode uint8) error {
		if mode&modeStringTag != 0 {
			return s.semErr(t, errInvalidStringTag)
		}
		if mode&modeName != 0 {
			return nonStringName(t)
		}
		n := v.Len()
		if n == 0 {
			if s.opts.nilMapAsNull && v.IsNil() {
				s.buf = append(s.buf, "null"...)
			} else {
				s.buf = append(s.buf, "{}"...)
			}
			return nil
		}
		leave, err := s.visit(t, v)
		if err != nil {
			return err
		}
		defer leave()
		if err := s.open(); err != nil {
			return err
		}
		key.init()
		unique := !key.nonDefault && uniqueKeyKind(t.Key().Kind())
		k := reflect.New(t.Key()).Elem()
		x := reflect.New(t.Elem()).Elem()
		s.buf = append(s.buf, '{')
		if !s.opts.deterministic || n == 1 {
			var names map[string]struct{}
			if !unique {
				names = make(map[string]struct{}, n)
			}
			first := true
			for it := v.MapRange(); it.Next(); {
				if !first {
					s.buf = append(s.buf, ',')
				}
				first = false
				k.SetIterKey(it)
				start := len(s.buf)
				if err := s.encodeName(t, key, k); err != nil {
					return err
				}
				if names != nil {
					name := unquote(s.buf[start:])
					if _, dup := names[name]; dup {
						return &jsontext.SyntacticError{Err: jsontext.ErrDuplicateName}
					}
					names[name] = struct{}{}
				}
				s.buf = append(s.buf, ':')
				x.SetIterValue(it)
				if err := val.encode(s, x, 0); err != nil {
					return err
				}
			}
		} else {
			// Sort the entries by unquoted name, as v2's Deterministic does.
			type entry struct {
				name, quoted string
				val          reflect.Value
			}
			entries := make([]entry, 0, n)
			for it := v.MapRange(); it.Next(); {
				k.SetIterKey(it)
				start := len(s.buf)
				if err := s.encodeName(t, key, k); err != nil {
					return err
				}
				quoted := string(s.buf[start:])
				s.buf = s.buf[:start]
				ev := reflect.New(t.Elem()).Elem()
				ev.SetIterValue(it)
				entries = append(entries, entry{unquote([]byte(quoted)), quoted, ev})
			}
			slices.SortFunc(entries, func(a, b entry) int { return strings.Compare(a.name, b.name) })
			for i, en := range entries {
				if i > 0 {
					s.buf = append(s.buf, ',')
					if !unique && en.name == entries[i-1].name {
						return &jsontext.SyntacticError{Err: jsontext.ErrDuplicateName}
					}
				}
				s.buf = append(s.buf, en.quoted...)
				s.buf = append(s.buf, ':')
				if err := val.encode(s, en.val, 0); err != nil {
					return err
				}
			}
		}
		s.buf = append(s.buf, '}')
		s.depth--
		return nil
	}
}

// encodeName appends map key k as an object name, which must encode as a
// JSON string.
func (s *encodeState) encodeName(t reflect.Type, key *codec, k reflect.Value) error {
	start := len(s.buf)
	if err := key.encode(s, k, modeName); err != nil {
		return err
	}
	if len(s.buf) == start || s.buf[start] != '"' {
		return nonStringName(t.Key())
	}
	return nil
}

// nonStringName is v2's error for a map key that does not encode as a JSON
// string: a SemanticError wrapping a SyntacticError.
func nonStringName(t reflect.Type) error {
	return &jsonv2.SemanticError{GoType: t, Err: &jsontext.SyntacticError{Err: jsontext.ErrNonStringName}}
}

// unquote returns the value of a JSON string this package produced.
func unquote(q []byte) string {
	if bytes.IndexByte(q, '\\') < 0 {
		return string(q[1 : len(q)-1])
	}
	b, _ := appendUnescaped(nil, q, 1)
	return string(b)
}

func makeStructEncoder(t reflect.Type) encodeFunc {
	var (
		once   sync.Once
		fields structFields
		errFs  *jsonv2.SemanticError
	)
	return func(s *encodeState, v reflect.Value, mode uint8) error {
		if mode&modeStringTag != 0 {
			return s.semErr(t, errInvalidStringTag)
		}
		if mode&modeName != 0 {
			return nonStringName(t)
		}
		once.Do(func() { fields, errFs = makeStructFields(t) })
		if errFs != nil {
			return errFs
		}
		if err := s.open(); err != nil {
			return err
		}
		s.buf = append(s.buf, '{')
		first := true
		for i := range fields.flattened {
			f := &fields.flattened[i]
			fv, ok := fieldByIndex(v, f.index)
			if !ok {
				continue // a nil embedded pointer
			}
			if f.omitzero && (f.isZero == nil && fv.IsZero() || f.isZero != nil && f.isZero(fv)) {
				continue
			}
			f.cod.init()
			if f.omitempty && !f.cod.nonDefault && f.isEmpty != nil && f.isEmpty(fv) {
				continue
			}
			start := len(s.buf)
			if !first {
				s.buf = append(s.buf, ',')
			}
			s.buf = append(s.buf, f.quotedName...)
			s.buf = append(s.buf, ':')
			valStart := len(s.buf)
			var fmode uint8
			if f.string {
				fmode = modeStringTag
			}
			if err := f.cod.encode(s, fv, fmode); err != nil {
				return err
			}
			if f.omitempty && isEmptyJSON(s.buf[valStart:]) {
				s.buf = s.buf[:start] // the value encoded as an empty JSON value
				continue
			}
			first = false
		}
		s.buf = append(s.buf, '}')
		s.depth--
		return nil
	}
}

// isEmptyJSON reports whether b is null, "", {} or [].
func isEmptyJSON(b []byte) bool {
	switch string(b) {
	case "null", `""`, "{}", "[]":
		return true
	}
	return false
}

// fieldByIndex is v.FieldByIndex that reports false at a nil embedded pointer.
func fieldByIndex(v reflect.Value, index []int) (reflect.Value, bool) {
	for n, i := range index {
		if n > 0 && v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return reflect.Value{}, false
			}
			v = v.Elem()
		}
		v = v.Field(i)
	}
	return v, true
}

// appendCanonical appends the JSON value b from a MarshalJSON method in v2's
// form: whitespace removed, strings re-quoted minimally, numbers as written.
// Invalid JSON and duplicate object names are errors.
func (s *encodeState) appendCanonical(b []byte) error {
	if bytes.HasPrefix(b, bom) {
		return &jsontext.SyntacticError{Err: ErrTape}
	}
	bd := binders.Get().(*binder)
	defer putBinder(bd)
	doc, err := bd.p.Parse(b)
	if err != nil {
		return &jsontext.SyntacticError{Err: err}
	}
	d := decodeState{doc: doc, buf: b}
	if err := d.checkDups(doc.Root()); err != nil {
		return err
	}
	s.buf = doc.Root().appendJSON(s.buf, d.raw)
	return nil
}
```

```diff
--- a/serialize.go
+++ b/serialize.go
@@ -11,7 +11,11 @@
 // AppendJSON appends e as minified JSON to dst. Parsing the output yields an
 // equal tree, including element types. It walks the tape in order without
 // recursion, so nesting depth is limited only by memory.
-func (e Element) AppendJSON(dst []byte) []byte {
+func (e Element) AppendJSON(dst []byte) []byte { return e.appendJSON(dst, nil) }
+
+// appendJSON is AppendJSON; if numberText is not nil, numbers are written as
+// the text it returns instead of being reformatted.
+func (e Element) appendJSON(dst []byte, numberText func(Element) []byte) []byte {
 	type open struct {
 		object   bool
 		nonEmpty bool // an element or field was written: the next needs a comma
@@ -46,7 +50,11 @@
 			i++
 			continue
 		}
-		dst = v.appendScalar(dst)
+		if numberText != nil && (tag == tagInt64 || tag == tagUint64 || tag == tagDouble || tag == tagBigInt) {
+			dst = append(dst, numberText(v)...)
+		} else {
+			dst = v.appendScalar(dst)
+		}
 		i = v.next()
 	}
 	return dst
```

- [ ] **Step 6: Methods and time**

```go
package simdjson

import (
	"encoding"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"reflect"
)

// addMethods layers a type's JSON and text methods over the default codec,
// in v2's order of precedence (encoding/json/v2/arshal_methods.go), and
// reports whether it added any. Methods are only attached to value types,
// so they are never called on a nil pointer or interface.
func addMethods(c *codec) bool {
	t := c.typ
	if t.Kind() == reflect.Pointer || t.Kind() == reflect.Interface {
		return false
	}
	added := false

	// Encoding: MarshalJSON > AppendText > MarshalText > default.
	// MarshalerTo cannot be called without a jsontext.Encoder.
	if _, ok := implements(t, textMarshalerType); ok {
		added = true
		c.enc = func(s *encodeState, v reflect.Value, mode uint8) error {
			b, err := v.Addr().Interface().(encoding.TextMarshaler).MarshalText()
			if err != nil {
				return s.methodErr(t, err)
			}
			return s.appendQuoted(t, b)
		}
	}
	if _, ok := implements(t, textAppenderType); ok {
		added = true
		c.enc = func(s *encodeState, v reflect.Value, mode uint8) error {
			b, err := v.Addr().Interface().(encoding.TextAppender).AppendText(s.scratch[:0])
			s.scratch = b[:0]
			if err != nil {
				return s.methodErr(t, err)
			}
			return s.appendQuoted(t, b)
		}
	}
	if _, ok := implements(t, jsonMarshalerType); ok {
		added = true
		c.enc = func(s *encodeState, v reflect.Value, mode uint8) error {
			b, err := v.Addr().Interface().(jsonv2.Marshaler).MarshalJSON()
			if err != nil {
				return s.methodErr(t, err)
			}
			if mode&modeName != 0 && !isQuoted(b) {
				return nonStringName(t)
			}
			if err := s.appendCanonical(b); err != nil {
				return &jsonv2.SemanticError{GoType: t, Err: err}
			}
			return nil
		}
	} else if _, ok := implements(t, jsonMarshalerToType); ok {
		added = true
		c.enc = func(s *encodeState, v reflect.Value, mode uint8) error {
			return &jsonv2.SemanticError{GoType: t, Err: errUnsupportedMethods}
		}
	}

	// Decoding: UnmarshalJSON > UnmarshalText > default.
	if _, ok := implements(t, textUnmarshalerType); ok {
		added = true
		c.dec = func(d *decodeState, e Element, v reflect.Value, mode uint8) error {
			switch e.tag() {
			case tagNull:
				v.SetZero()
				return nil
			case tagString:
			default:
				return d.valueErr(e, t, errNonStringValue)
			}
			if err := v.Addr().Interface().(encoding.TextUnmarshaler).UnmarshalText(e.rawString()); err != nil {
				return d.methodErr(e, t, err)
			}
			return nil
		}
	}
	if _, ok := implements(t, jsonUnmarshalerType); ok {
		added = true
		c.dec = func(d *decodeState, e Element, v reflect.Value, mode uint8) error {
			if err := d.checkDups(e); err != nil { // v2 validates the value it passes
				return err
			}
			if err := v.Addr().Interface().(jsonv2.Unmarshaler).UnmarshalJSON(d.raw(e)); err != nil {
				return d.methodErr(e, t, err)
			}
			return nil
		}
	} else if _, ok := implements(t, jsonUnmarshalerFromType); ok {
		added = true
		c.dec = func(d *decodeState, e Element, v reflect.Value, mode uint8) error {
			return d.semErr(e, t, errUnsupportedMethods)
		}
	}
	return added
}

// methodErr wraps an error from a user's unmarshal method in a
// SemanticError, unless it already is one or a SyntacticError.
func (d *decodeState) methodErr(e Element, t reflect.Type, err error) error {
	var se *jsonv2.SemanticError
	var sy *jsontext.SyntacticError
	if errors.As(err, &se) || errors.As(err, &sy) {
		return err
	}
	return &jsonv2.SemanticError{JSONPointer: d.pointer(e), JSONKind: kind(e), GoType: t, Err: err}
}

// isQuoted reports whether the JSON value b (possibly with surrounding
// whitespace) is a string.
func isQuoted(b []byte) bool {
	for _, c := range b {
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		case '"':
			return true
		}
		return false
	}
	return false
}
```

```go
// time.Time handling is adapted from the Go standard library
// (src/encoding/json/v2/arshal_time.go), Copyright 2023 The Go Authors,
// under the BSD-style license in LICENSE-GO.

package simdjson

import (
	"errors"
	"reflect"
	"time"
)

// makeTimeDecoder handles time.Time (an RFC 3339 string, checked as strictly
// as v2 does) and time.Duration (no default JSON form in v2: always an error).
func makeTimeDecoder(t reflect.Type) decodeFunc {
	if t == timeDurationType {
		return func(d *decodeState, e Element, v reflect.Value, mode uint8) error {
			return d.semErr(e, t, errNoDefault)
		}
	}
	return func(d *decodeState, e Element, v reflect.Value, mode uint8) error {
		if mode&modeStringTag != 0 {
			return d.semErr(e, t, errInvalidStringTag)
		}
		switch e.tag() {
		case tagNull:
			v.SetZero()
			return nil
		case tagString:
		default:
			return d.valueErr(e, t, nil)
		}
		tt := v.Addr().Interface().(*time.Time)
		if err := unmarshalRFC3339(tt, e.rawString()); err != nil {
			return d.semErr(e, t, err)
		}
		return nil
	}
}

// unmarshalRFC3339 parses b into tt, rejecting what RFC 3339 forbids but
// time.Time.UnmarshalText accepts (v2 timeArshaler.unmarshal).
func unmarshalRFC3339(tt *time.Time, b []byte) error {
	var u time.Time
	if err := u.UnmarshalText(b); err != nil {
		return err
	}
	newParseError := func(layoutElem, valueElem, message string) error {
		return &time.ParseError{Layout: time.RFC3339, Value: string(b), LayoutElem: layoutElem, ValueElem: valueElem, Message: message}
	}
	switch {
	case b[len("2006-01-02T")+1] == ':': // hour must be two digits
		return newParseError("15", string(b[len("2006-01-02T"):][:1]), "")
	case b[len("2006-01-02T15:04:05")] == ',': // sub-second separator must be a period
		return newParseError(".", ",", "")
	case b[len(b)-1] != 'Z':
		switch {
		case parseDec2(b[len(b)-len("07:00"):]) >= 24:
			return newParseError("Z07:00", string(b[len(b)-len("Z07:00"):]), ": timezone hour out of range")
		case parseDec2(b[len(b)-len("00"):]) >= 60:
			return newParseError("Z07:00", string(b[len(b)-len("Z07:00"):]), ": timezone minute out of range")
		}
	}
	*tt = u
	return nil
}

func parseDec2(b []byte) byte {
	if len(b) < 2 {
		return 0
	}
	return 10*(b[0]-'0') + (b[1] - '0')
}

// makeTimeEncoder writes time.Time as an RFC 3339 string with nanoseconds,
// rejecting years and zone offsets RFC 3339 cannot express; time.Duration
// has no default JSON form in v2.
func makeTimeEncoder(t reflect.Type) encodeFunc {
	if t == timeDurationType {
		return func(s *encodeState, v reflect.Value, mode uint8) error {
			return s.semErr(t, errNoDefault)
		}
	}
	return func(s *encodeState, v reflect.Value, mode uint8) error {
		if mode&modeStringTag != 0 {
			return s.semErr(t, errInvalidStringTag)
		}
		tt := v.Interface().(time.Time)
		s.buf = append(s.buf, '"')
		n0 := len(s.buf)
		s.buf = tt.AppendFormat(s.buf, time.RFC3339Nano)
		switch b := s.buf[n0:]; {
		case b[len("9999")] != '-': // year must be exactly 4 digits wide
			return s.semErr(t, errors.New("year outside of range [0,9999]"))
		case b[len(b)-1] != 'Z':
			c := b[len(b)-len("Z07:00")]
			if ('0' <= c && c <= '9') || parseDec2(b[len(b)-len("07:00"):]) >= 24 {
				return s.semErr(t, errors.New("timezone hour outside of range [0,23]"))
			}
		}
		s.buf = append(s.buf, '"')
		return nil
	}
}
```

- [ ] **Step 7: Run the tests**

```sh
go vet .
go test -count=1 -run 'MatchesV2|DeepAndWide|IntoExisting|Recursive|ReusesParsers' -v . 2>&1 | grep -E '^(--- |ok|FAIL)'
```

Expected: every test PASSes. A failure prints the input, the target type and both results. Fix the codec to match v2, not the test.

- [ ] **Step 8: Run the full matrix and commit**

```sh
make check
git add options.go typeplan.go strcache.go decode.go encode.go methods.go time.go serialize.go bind_test.go
git commit -m "feat: Unmarshal and Marshal with encoding/json/v2 semantics

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: MarshalIndent

**Files:**
- Create: `indent.go`
- Test: `indent_test.go`

**Interfaces:**
- Consumes: `Marshal`, `Option`, `Deterministic`, `errClass`, `Outer` (Task 2).
- Produces: `func MarshalIndent(v any, prefix, indent string, opts ...Option) ([]byte, error)`.

- [ ] **Step 1: Write the failing test**

```go
package simdjson

import (
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"testing"
)

func TestMarshalIndentMatchesV2(t *testing.T) {
	values := []any{
		map[string]any{"b": []any{1, "x", map[string]any{}}, "a": []any{}, "c": map[string]any{"z": true}},
		[]string{"q\"[{,:}]"}, 1, "s", nil, Outer{A: 1},
	}
	for _, ind := range [][2]string{{" ", "\t"}, {"", "  "}, {"", ""}, {"\t ", " \t"}} {
		for _, v := range values {
			want, errWant := jsonv2.Marshal(v, jsonv2.Deterministic(true), jsontext.WithIndentPrefix(ind[0]), jsontext.WithIndent(ind[1]))
			got, err := MarshalIndent(v, ind[0], ind[1], Deterministic(true))
			if errClass(err) != errClass(errWant) || string(got) != string(want) {
				t.Errorf("MarshalIndent(%#v, %q, %q) = %q, %v; v2 %q, %v", v, ind[0], ind[1], got, err, want, errWant)
			}
		}
	}
	// v2 panics on such options; MarshalIndent returns an error instead.
	for _, ind := range [][2]string{{"x", " "}, {"", "-"}, {"\n", ""}} {
		if got, err := MarshalIndent(1, ind[0], ind[1]); err == nil {
			t.Errorf("MarshalIndent(1, %q, %q) = %q, want an error", ind[0], ind[1], got)
		}
	}
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test -run MarshalIndent .`
Expected: build failure, `undefined: MarshalIndent`.

- [ ] **Step 3: Implement**

```go
package simdjson

import (
	"errors"
	"strings"
)

// MarshalIndent is Marshal with each array element and object member on its
// own line, starting with prefix and indented by one copy of indent per
// nesting level, as encoding/json/v2 does with jsontext.WithIndentPrefix and
// jsontext.WithIndent. Empty arrays and objects stay on one line. Prefix and
// indent may contain only spaces and tabs: anything else is an error (v2
// panics on such options).
func MarshalIndent(v any, prefix, indent string, opts ...Option) ([]byte, error) {
	if strings.Trim(prefix, " \t") != "" || strings.Trim(indent, " \t") != "" {
		return nil, errBadIndent
	}
	b, err := Marshal(v, opts...)
	if err != nil {
		return nil, err
	}
	return appendIndented(make([]byte, 0, len(b)*2), b, prefix, indent), nil
}

var errBadIndent = errors.New("simdjson: indent prefix and indent may contain only spaces and tabs")

// appendIndented re-indents the compact, valid JSON src (as produced by Marshal).
func appendIndented(dst, src []byte, prefix, indent string) []byte {
	depth := 0
	newline := func() {
		dst = append(dst, '\n')
		dst = append(dst, prefix...)
		for range depth {
			dst = append(dst, indent...)
		}
	}
	for i := 0; i < len(src); i++ {
		switch c := src[i]; c {
		case '"':
			n := stringEnd(src[i:])
			dst = append(dst, src[i:i+n]...)
			i += n - 1
		case '{', '[':
			if i+1 < len(src) && (src[i+1] == '}' || src[i+1] == ']') {
				dst = append(dst, c, src[i+1]) // empty: {} or []
				i++
				continue
			}
			dst = append(dst, c)
			depth++
			newline()
		case '}', ']':
			depth--
			newline()
			dst = append(dst, c)
		case ',':
			dst = append(dst, ',')
			newline()
		case ':':
			dst = append(dst, ':', ' ')
		default:
			dst = append(dst, c)
		}
	}
	return dst
}
```

- [ ] **Step 4: Run the test**

Run: `go test -count=1 -run MarshalIndent -v .`
Expected: PASS.

- [ ] **Step 5: Commit**

```sh
make check
git add indent.go indent_test.go
git commit -m "feat: MarshalIndent matching v2's indented output

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Fuzzing, concurrency and the race detector

**Files:**
- Create: `bind_fuzz_test.go`
- Modify: `Makefile`

**Interfaces:**
- Consumes: `diffInputs`, `diffTypes`, `sameUnmarshal`, `sameMarshal`, `Outer` (Task 2); `readTestdata` (`corpus_test.go`).
- Produces: `FuzzUnmarshal`, `FuzzMarshal`, `TestConcurrentBinding`, and the `make test-race` target, which `make check` runs.

- [ ] **Step 1: Write the fuzz targets and the concurrency test**

```go
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
```

- [ ] **Step 2: Run the seed corpora and the concurrency test**

Run: `go test -count=1 -run 'FuzzUnmarshal|FuzzMarshal|ConcurrentBinding' -v . 2>&1 | grep -E '^(--- |ok|FAIL)'`
Expected: PASS.

- [ ] **Step 3: Add the race run to `make check`**

```diff
--- a/Makefile
+++ b/Makefile
@@ -17,7 +17,7 @@
 FUZZ_PKG  = $(if $(filter $(target),$(NEON_FUZZ)),./internal/stage1/,.)
 
 .PHONY: help testdata clean fmt vet check \
-        test test-neon test-purego test-amd64 \
+        test test-neon test-purego test-amd64 test-race \
         fuzz bench benchstat
 
 # ── Setup ────────────────────────────────────────────────────────────────────
@@ -48,7 +48,7 @@
 	GOOS=linux GOARCH=386 $(GO) vet ./...
 	GOOS=wasip1 GOARCH=wasm $(GO) vet ./...
 
-check: fmt vet test test-purego test-neon test-amd64 ## Full build matrix (spec §8.4); run before committing
+check: fmt vet test test-purego test-neon test-amd64 test-race ## Full build matrix (spec §8.4); run before committing
 
 # ── Tests ────────────────────────────────────────────────────────────────────
 
@@ -64,6 +64,9 @@
 test-amd64: testdata ## Test as amd64, via Rosetta 2 on Apple silicon [short=1]
 	GOARCH=amd64 $(GO) test $(if $(short),-short) ./...
 
+test-race: testdata ## Test with the race detector (short mode)
+	$(GO) test -race -short ./...
+
 # ── Fuzzing and benchmarks ───────────────────────────────────────────────────
 
 fuzz: ## Fuzz one target [target=FuzzParse time=60s neon=1]
```

- [ ] **Step 4: Fuzz for 10 minutes per target** (spec §8)

```sh
make fuzz target=FuzzUnmarshal time=10m
make fuzz target=FuzzMarshal time=10m
```

Expected: no failures. If a fuzzer fails, add its input to `diffInputs` in `bind_test.go`, then fix the codec until the differential suite passes again.

- [ ] **Step 5: Commit**

```sh
make check   # now ends with test-race
git add bind_fuzz_test.go Makefile
git commit -m "test: fuzz Unmarshal and Marshal against v2; race test in make check

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Benchmarks, documentation and acceptance

**Files:**
- Create: `bind_bench_test.go`
- Modify: `README.md`, `AGENTS.md`, `NOTICE`

**Interfaces:**
- Consumes: `Unmarshal`, `Marshal` (Task 2); `Parser.binding` (Task 1); `readTestdata` (`corpus_test.go`).
- Produces: `BenchmarkUnmarshal`, `BenchmarkV2Unmarshal`, `BenchmarkMarshal`, `BenchmarkV2Marshal`, `BenchmarkParseMode`, `TestBenchTypesMatchV2`.

- [ ] **Step 1: Write the benchmarks and the check that keeps them honest**

```go
package simdjson

import (
	jsonv2 "encoding/json/v2"
	"testing"
)

// Typed subsets of the example files, as a service would declare them.
type benchUser struct {
	ID         uint64 `json:"id"`
	Name       string `json:"name"`
	ScreenName string `json:"screen_name"`
	Followers  int    `json:"followers_count"`
	Verified   bool   `json:"verified"`
}
type benchStatus struct {
	ID        uint64    `json:"id"`
	Text      string    `json:"text"`
	CreatedAt string    `json:"created_at"`
	User      benchUser `json:"user"`
	Retweets  int       `json:"retweet_count"`
	Favorites int       `json:"favorite_count"`
	Lang      string    `json:"lang"`
}
type benchTwitter struct {
	Statuses []benchStatus `json:"statuses"`
}

type benchCitm struct {
	AreaNames                map[string]string `json:"areaNames"`
	AudienceSubCategoryNames map[string]string `json:"audienceSubCategoryNames"`
	Events                   map[string]struct {
		ID          int64   `json:"id"`
		Name        string  `json:"name"`
		Description *string `json:"description"`
		Logo        *string `json:"logo"`
		SubTopicIDs []int64 `json:"subTopicIds"`
		TopicIDs    []int64 `json:"topicIds"`
	} `json:"events"`
	Performances []struct {
		EventID int64 `json:"eventId"`
		ID      int64 `json:"id"`
		Prices  []struct {
			Amount                int64 `json:"amount"`
			AudienceSubCategoryID int64 `json:"audienceSubCategoryId"`
			SeatCategoryID        int64 `json:"seatCategoryId"`
		} `json:"prices"`
		SeatCategories []struct {
			Areas []struct {
				AreaID   int64   `json:"areaId"`
				BlockIDs []int64 `json:"blockIds"`
			} `json:"areas"`
			SeatCategoryID int64 `json:"seatCategoryId"`
		} `json:"seatCategories"`
		Start     int64  `json:"start"`
		VenueCode string `json:"venueCode"`
	} `json:"performances"`
	SeatCategoryNames map[string]string  `json:"seatCategoryNames"`
	SubTopicNames     map[string]string  `json:"subTopicNames"`
	TopicNames        map[string]string  `json:"topicNames"`
	TopicSubTopics    map[string][]int64 `json:"topicSubTopics"`
	VenueNames        map[string]string  `json:"venueNames"`
}

type benchCanada struct {
	Type     string `json:"type"`
	Features []struct {
		Type       string `json:"type"`
		Properties struct {
			Name string `json:"name"`
		} `json:"properties"`
		Geometry struct {
			Type        string         `json:"type"`
			Coordinates [][][2]float64 `json:"coordinates"`
		} `json:"geometry"`
	} `json:"features"`
}

func benchBind[T any](b *testing.B, file string, unmarshal func([]byte, any) error) {
	data := readTestdata(b, "jsonexamples", file)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		var v T
		if err := unmarshal(data, &v); err != nil {
			b.Fatal(err)
		}
	}
}

func ours(data []byte, v any) error { return Unmarshal(data, v) }
func v2(data []byte, v any) error   { return jsonv2.Unmarshal(data, v) }

func BenchmarkUnmarshal(b *testing.B) {
	b.Run("twitter", func(b *testing.B) { benchBind[benchTwitter](b, "twitter.json", ours) })
	b.Run("citm", func(b *testing.B) { benchBind[benchCitm](b, "citm_catalog.json", ours) })
	b.Run("canada", func(b *testing.B) { benchBind[benchCanada](b, "canada.json", ours) })
}

func BenchmarkV2Unmarshal(b *testing.B) {
	b.Run("twitter", func(b *testing.B) { benchBind[benchTwitter](b, "twitter.json", v2) })
	b.Run("citm", func(b *testing.B) { benchBind[benchCitm](b, "citm_catalog.json", v2) })
	b.Run("canada", func(b *testing.B) { benchBind[benchCanada](b, "canada.json", v2) })
}

// TestBenchTypesMatchV2 keeps the benchmarks honest: both decode the same.
func TestBenchTypesMatchV2(t *testing.T) {
	check := func(file string, a, b any) {
		data := readTestdata(t, "jsonexamples", file)
		if err := Unmarshal(data, a); err != nil {
			t.Fatal(file, err)
		}
		if err := jsonv2.Unmarshal(data, b); err != nil {
			t.Fatal(file, err)
		}
		x, _ := jsonv2.Marshal(a, jsonv2.Deterministic(true))
		y, _ := jsonv2.Marshal(b, jsonv2.Deterministic(true))
		if string(x) != string(y) {
			t.Errorf("%s: decoded values differ", file)
		}
	}
	check("twitter.json", new(benchTwitter), new(benchTwitter))
	check("citm_catalog.json", new(benchCitm), new(benchCitm))
	check("canada.json", new(benchCanada), new(benchCanada))
}

func benchMarshal[T any](b *testing.B, file string, marshal func(any) ([]byte, error)) {
	var v T
	if err := jsonv2.Unmarshal(readTestdata(b, "jsonexamples", file), &v); err != nil {
		b.Fatal(err)
	}
	out, _ := marshal(&v)
	b.SetBytes(int64(len(out)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := marshal(&v); err != nil {
			b.Fatal(err)
		}
	}
}

func oursM(v any) ([]byte, error) { return Marshal(v) }
func v2M(v any) ([]byte, error)   { return jsonv2.Marshal(v) }

func BenchmarkMarshal(b *testing.B) {
	b.Run("twitter", func(b *testing.B) { benchMarshal[benchTwitter](b, "twitter.json", oursM) })
	b.Run("citm", func(b *testing.B) { benchMarshal[benchCitm](b, "citm_catalog.json", oursM) })
	b.Run("canada", func(b *testing.B) { benchMarshal[benchCanada](b, "canada.json", oursM) })
}

func BenchmarkV2Marshal(b *testing.B) {
	b.Run("twitter", func(b *testing.B) { benchMarshal[benchTwitter](b, "twitter.json", v2M) })
	b.Run("citm", func(b *testing.B) { benchMarshal[benchCitm](b, "citm_catalog.json", v2M) })
	b.Run("canada", func(b *testing.B) { benchMarshal[benchCanada](b, "canada.json", v2M) })
}

// BenchmarkParseMode shows what binding mode adds to parsing.
func BenchmarkParseMode(b *testing.B) {
	data := readTestdata(b, "jsonexamples", "twitter.json")
	for _, binding := range []bool{false, true} {
		b.Run(map[bool]string{false: "plain", true: "binding"}[binding], func(b *testing.B) {
			p := Parser{MaxDepth: 10001, BigIntAsString: true, binding: binding}
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if _, err := p.Parse(data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
```

- [ ] **Step 2: Check that the benchmark types decode as v2 does**

Run: `go test -count=1 -run TestBenchTypesMatchV2 -v .`
Expected: PASS.

- [ ] **Step 3: Measure the speed criteria** (on an idle machine)

```sh
make bench neon=1 bench='Unmarshal/|Marshal/' count=6 out=/tmp/bind.txt
```

Expected, from the time/op of each pair:

| | Must be |
|---|---|
| `BenchmarkV2Unmarshal/twitter` ÷ `BenchmarkUnmarshal/twitter` | ≥ 1.4 (prototype: 1.46) |
| `BenchmarkV2Unmarshal/citm` ÷ `BenchmarkUnmarshal/citm` | ≥ 1.1 (prototype: 1.12) |
| `BenchmarkV2Unmarshal/canada` ÷ `BenchmarkUnmarshal/canada` | ≥ 1.5 (prototype: 1.71) |
| `BenchmarkV2Marshal/X` ÷ `BenchmarkMarshal/X`, for each file | ≥ 1.0 (prototype: 1.2, 1.5, 1.03) |

If a ratio falls short, profile first (`-cpuprofile`), then fix the code; do not relax the threshold.

- [ ] **Step 4: Update the documentation**

````diff
--- a/README.md
+++ b/README.md
@@ -1,6 +1,6 @@
 # simdjson-go
 
-A pure-Go port of [simdjson](https://github.com/simdjson/simdjson)'s parser and DOM API. It uses no cgo and no dependencies outside the standard library. On arm64 an optional NEON kernel speeds up the scan for structural characters.
+A pure-Go port of [simdjson](https://github.com/simdjson/simdjson)'s parser and DOM API, with `Unmarshal` and `Marshal` that behave like `encoding/json/v2`. It uses no cgo and no dependencies outside the standard library. On arm64 an optional NEON kernel speeds up the scan for structural characters.
 
 C++ simdjson v5.0.2 is the reference: this port uses the same tape format, error codes and edge-case behaviour, and was checked against the C++ parser on the simdjson-data corpora.
 
@@ -40,6 +40,23 @@
 - **`Minify(dst, src)`** removes whitespace without parsing.
 - **Errors** are sentinel values (`ErrTape`, `ErrNumber`, `ErrDepth`, …), one per C++ error code. Check them with `errors.Is`.
 
+### Data binding
+
+```go
+type User struct {
+	Name string  `json:"name"`
+	IDs  []int64 `json:"ids,omitempty"`
+}
+
+var u User
+err := simdjson.Unmarshal(data, &u)
+out, err := simdjson.Marshal(&u)
+```
+
+`Unmarshal`, `Marshal`, `MarshalAppend` and `MarshalIndent` have the semantics of `encoding/json/v2` under its default options: exact name matching, duplicate names rejected, invalid UTF-8 rejected, `[]`/`{}` for nil slices and maps. Errors are v2's `*jsontext.SyntacticError` and `*json.SemanticError`. Struct tags use v2's syntax (`omitempty`, `omitzero`, `string`, `case:ignore`, `embed`; `format:` is not supported), and `MarshalJSON`/`UnmarshalJSON`/text methods are honoured. Options turn on the v1 behaviours services rely on: `MatchCaseInsensitiveNames`, `FormatNilSliceAsNull`, `FormatNilMapAsNull`, `Deterministic`, `RejectUnknownMembers`.
+
+One difference: invalid JSON is always a `SyntacticError`, because the whole input is validated before decoding, where v2 may first report a semantic error it meets earlier.
+
 **Lifetime:** a `Document` and everything read from it stay valid until the next `Parse` on the same `Parser`, including a call that fails. A `Parser` is not safe for concurrent use. A `Document` can be read from several goroutines as long as no `Parse` runs at the same time.
 
 ## Builds
@@ -64,6 +81,13 @@
 
 `Parse` makes no allocations once the `Parser` has grown its buffers. The NEON structural indexer runs at about 2.1 GB/s.
 
+Typed `Unmarshal` and `Marshal` against `encoding/json/v2` (NEON, structs of `bind_bench_test.go`):
+
+| | twitter.json | citm_catalog.json | canada.json |
+|---|---|---|---|
+| `Unmarshal` | 1.46× | 1.12× | 1.71× |
+| `Marshal` | 1.2× | 1.5× | 1.03× |
+
 ## Development
 
 ```sh
@@ -81,8 +105,8 @@
 
 ## Status
 
-This is sub-project 1 of 5: the core and the DOM. Planned next are streams (`ParseMany`), On-Demand, and data binding (`Unmarshal`/`Marshal`). There are no x86 SIMD kernels; amd64 uses the portable Go code.
+Done: the core and the DOM (sub-project 1) and data binding (sub-project 4). Planned next are streams (`ParseMany`) and On-Demand. There are no x86 SIMD kernels; amd64 uses the portable Go code.
 
 ## License
 
-Apache-2.0 or MIT, at your option, the same as C++ simdjson. See [`LICENSE`](LICENSE), [`LICENSE-MIT`](LICENSE-MIT) and [`NOTICE`](NOTICE). The float conversion is adapted from the Go standard library under [`LICENSE-GO`](LICENSE-GO).
+Apache-2.0 or MIT, at your option, the same as C++ simdjson. See [`LICENSE`](LICENSE), [`LICENSE-MIT`](LICENSE-MIT) and [`NOTICE`](NOTICE). The float conversion and parts of the data binding are adapted from the Go standard library under [`LICENSE-GO`](LICENSE-GO).
````

```diff
--- a/AGENTS.md
+++ b/AGENTS.md
@@ -6,7 +6,8 @@
 
 - **C++ simdjson v5.0.2 is the oracle.** The tape format, the error returned for each bad input, and the edge cases must match C++. Some of those cases look like bugs but are intended, so don't "fix" them: the design spec lists them (§5.5, §9). Change parsing behaviour only together with evidence from C++.
 - **Go 1.27, standard library only.** No cgo, no third-party modules in the library or its tests. Dev tooling run through `go run` (the pinned `benchstat` in the Makefile) is the only exception.
-- **The spec is the authority.** Read [`docs/superpowers/specs/2026-10-06-simdjson-go-core-design.md`](docs/superpowers/specs/2026-10-06-simdjson-go-core-design.md) before changing behaviour. If the code and the spec disagree, fix one of them in the same change.
+- **`encoding/json/v2` is the oracle for data binding.** `Unmarshal` and `Marshal` must agree with the installed v2 on values, output bytes and error classes; the one allowed difference is listed in the binding spec (§1, §6).
+- **The spec is the authority.** Read [`docs/superpowers/specs/2026-10-06-simdjson-go-core-design.md`](docs/superpowers/specs/2026-10-06-simdjson-go-core-design.md) (parser, DOM) or [`docs/superpowers/specs/2026-10-07-simdjson-go-binding-design.md`](docs/superpowers/specs/2026-10-07-simdjson-go-binding-design.md) (data binding) before changing behaviour. If the code and the spec disagree, fix one of them in the same change.
 
 ## Layout
 
@@ -20,7 +21,10 @@
 | `fastfloat.go`, `pow10tab.go` | Decimal→float64 conversion adapted from Go's `internal/strconv` (BSD, `LICENSE-GO`); regenerate the table with `go generate` (`pow10gen.go`) |
 | `parser.go`, `tape.go`, `errors.go` | `Parser`, `Document`, tape tags, sentinel errors |
 | `element.go`, `pointer.go`, `serialize.go` | DOM API, JSON Pointer, `AppendJSON` and `Minify` |
-| `*_test.go` | Unit, corpus (`corpus_test.go`), fuzz (`fuzz_test.go`) and benchmark tests |
+| `options.go`, `typeplan.go` | Data binding: options, the per-type codec cache, struct field plans (adapted from v2's `fields.go`) |
+| `decode.go`, `encode.go`, `indent.go` | `Unmarshal`, `Marshal`/`MarshalAppend`, `MarshalIndent` |
+| `methods.go`, `time.go`, `strcache.go` | `MarshalJSON`/`UnmarshalJSON`/text methods, `time.Time`, the string cache |
+| `*_test.go` | Unit, corpus (`corpus_test.go`), fuzz (`fuzz_test.go`) and benchmark tests; data binding against v2 in `bind_test.go`, `bind_fuzz_test.go`, `bind_bench_test.go` |
 | `scripts/fetch-testdata.sh` | Downloads the pinned corpora (`make testdata`) |
 
 ## Commands
@@ -40,6 +44,7 @@
 - Run `make check`. It runs gofmt, vet (including linux/386 and wasip1/wasm) and the tests on pure Go, `-tags purego`, NEON and amd64 (under Rosetta).
 - **A stage 1 kernel change** must keep the NEON and portable kernels bit-identical. Run `make fuzz target=FuzzClassify` and `make fuzz target=FuzzUTF8`.
 - **A parsing change** needs `make fuzz target=FuzzParse` (both builds) and the corpus tests passing.
+- **A data binding change** needs `make fuzz target=FuzzUnmarshal` and `make fuzz target=FuzzMarshal`. If they fail, fix the binding: do not loosen `sameUnmarshal`/`sameMarshal` in `bind_test.go`.
 - **If a fuzzer fails, investigate the parser first.** Do not loosen the oracle in `fuzz_test.go` to make it pass. Each adjustment the oracle makes to `encoding/json` models a documented difference from C++.
 - **Never skip the corpus tests.** They `t.Fatal` when `testdata/` is missing, and that's deliberate.
 - `Parse` must not read past `len(b)`, must not keep or modify `b`, and must not allocate once the `Parser` has grown its buffers. `BenchmarkParse` reports allocs/op.
@@ -48,5 +53,6 @@
 
 - **Literal `\u` escapes in tests.** Test inputs such as `` `"é"` `` must keep the backslash. Some editors and tools decode them to `é`, which silently weakens the test. Check with `grep -c 'u00e9'`.
 - **`archsimd` shift direction.** `x.ConcatShiftBytesRight(y, n)` treats `y` as the low half. The byte k positions before `in[i]` is `in.ConcatShiftBytesRight(prev, 16-k)[i]`.
-- **Depth.** Empty `[]` and `{}` don't count toward `MaxDepth`, as in C++. Nothing recurses per nesting level (`AppendJSON` and `AtPointer` walk the tape iteratively); keep it that way so a raised `MaxDepth` cannot overflow the stack.
+- **Depth.** Empty `[]` and `{}` don't count toward `MaxDepth`, as in C++ (but they do in binding mode, as in v2). The parser and DOM never recurse per nesting level (`AppendJSON` and `AtPointer` walk the tape iteratively); keep it that way so a raised `MaxDepth` cannot overflow the stack. The binding codecs do recurse, as v2's do, bounded by v2's depth limit of 10,000.
+- **Binding mode.** `Parser.binding` (set only by `Unmarshal`) makes stage 2 record input offsets and repeated names. Keep every binding-only step behind `if b.binding`, so plain `Parse` and its tape stay as C++ defines them.
 - **Tape counts.** A container's element count saturates at 0xFFFFFF (`saturated`), and `Len()` then walks the tape. Read the count field only through `Element.exactCount`, which says whether it is exact.
```

```diff
--- a/NOTICE
+++ b/NOTICE
@@ -7,3 +7,8 @@
 fastfloat.go, pow10gen.go and pow10tab.go are adapted from the Go standard
 library (src/internal/strconv), Copyright 2025-2026 The Go Authors, available
 under the BSD-style license in LICENSE-GO.
+
+Parts of typeplan.go, decode.go, encode.go, time.go and strcache.go are
+adapted from the Go standard library (src/encoding/json/v2 and
+src/encoding/json/internal/jsonwire), Copyright 2020-2023 The Go Authors,
+available under the BSD-style license in LICENSE-GO.
```

- [ ] **Step 5: Final acceptance and commit**

```sh
make check
git add bind_bench_test.go README.md AGENTS.md NOTICE
git commit -m "docs: data binding in README and AGENTS; benchmarks against v2

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Then re-read spec §1 and confirm each criterion against this run:
1. The differential suite and the fuzzers pass.
2. and 3. The speed table in Step 3 holds.
4. Task 1 Step 6 met the parsing bound.
5. `make check` passes, including `test-race`.
