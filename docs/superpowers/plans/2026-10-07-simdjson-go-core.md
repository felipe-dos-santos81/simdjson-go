# simdjson-go Core + DOM Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A pure-Go port of simdjson's parsing core and DOM API (stage 1, stage 2, tape, DOM, JSON Pointer, Minify, serialization), with an arm64 NEON kernel behind `GOEXPERIMENT=simd`.

**Architecture:** `internal/stage1` finds structural characters 64 bytes at a time through a two-function kernel (`classify`, `utf8Checker`) with a portable implementation and an arm64 NEON one. The root package `simdjson` walks those indices (stage 2), writes the C++ tape format, and exposes it through `Parser` → `Document` → `Element`/`Array`/`Object`.

**Tech Stack:** Go 1.27, standard library only (`simd/archsimd` with `GOEXPERIMENT=simd`), Go native fuzzing, `testing.B`.

**Spec:** `docs/superpowers/specs/2026-10-06-simdjson-go-core-design.md` (read it first).

**Status:** executed. This plan is the record of the original implementation; the code has since
changed through reviews (see `git log`), so where its code blocks differ from the repository,
the repository and the spec are current.

**How the code in this plan was checked:** every code block below was compiled and tested in a scratch copy of this module on Go 1.27.1. It passed on pure Go (arm64), the NEON build and pure Go amd64 under Rosetta 2. On 72,718 inputs (the corpora, hand-written edge cases, random and mutated documents), its parse result and error code matched C++ simdjson v5.0.2. It was fuzzed (about 10M `FuzzParse` and 13M `FuzzClassify` executions without a finding), and the task order below was dry-run one task at a time. Copy code exactly; if something fails, the plan is wrong, so stop and report rather than improvise.

## Global Constraints

- Go `1.27` (`go 1.27` in `go.mod`), module path `simdjson-go`, package name `simdjson`.
- No cgo and no third-party modules; standard library only (`simd/archsimd` exists only with `GOEXPERIMENT=simd`).
- SIMD code is arm64 NEON only, in files tagged `//go:build arm64 && goexperiment.simd && !purego`; every other build uses the pure-Go kernel (`//go:build !arm64 || !goexperiment.simd || purego` once Task 10 adds the NEON kernel).
- The tape format is identical to C++ `doc/tape.md`: an 8-bit tag above a 56-bit payload; tags `r [ { ] } " l u d t f n Z`; strings stored as a 4-byte little-endian length, the bytes and a `0x00`.
- Errors are sentinel values, one per reachable C++ `error_code`, matched with `errors.Is`; which error a bad input yields matches C++ simdjson v5.0.2.
- `Parse` never copies, retains, modifies or reads past `len(b)` of its input; no padding is required.
- `MaxDepth` 0 or negative means 1024; at most `MaxDepth−1` nested non-empty arrays/objects; empty `[]`/`{}` do not count.
- `len(b) > 0xFFFFFFFC` → `ErrCapacity` (keeps 32-bit tape indices; C++ allows 0xFFFFFFFF); a leading UTF-8 BOM (`EF BB BF`) is skipped.
- Test corpora come from `github.com/simdjson/simdjson-data` @ `351949906abde446f0314bf79606fb5d884f5be7` via `scripts/fetch-testdata.sh` into gitignored `testdata/`; corpus tests `t.Fatal` (never skip) when the data is missing.
- Ship `LICENSE` (Apache-2.0) and `LICENSE-MIT` copied from C++ simdjson, plus `NOTICE`.
- Every commit leaves `gofmt -l .` empty and `go vet ./...` plus `GOEXPERIMENT=simd go vet ./...` clean.
- Commit messages end with the line `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

1. **The zero `Element` left over from an ignored error** (`v, _ := obj.Get("missing")`, then `v.IsNull()`). It must not panic: `Type()` is 0 ("unknown"), getters return `ErrIncorrectType`, `IsNull` is false. Pinned by `TestZeroElement` (Task 4).
2. **Escapes and multi-byte UTF-8 straddling the 64-byte stage 1 block boundary.** They must unescape correctly in both builds. Pinned by the boundary case in `TestIndex` (Task 2) and `TestStringsAcrossBlocks` (Task 3).
3. **Input that is a sub-slice of a larger buffer whose capacity holds more JSON.** Bytes past `len(b)` must be ignored. Pinned by `TestNoReadPastLength` (Task 3).
4. **Arrays/objects with more than 2²⁴−1 elements.** `Len` must be exact although the tape count saturates. Pinned by `TestCountSaturation` (Task 4).
5. **Objects with duplicate keys.** `Get` returns the first match (as C++ does) and `All` yields every field. Pinned by `TestObject` (Task 4).

## File Structure

| File | Task | Responsibility |
|---|---|---|
| `go.mod`, `LICENSE`, `LICENSE-MIT`, `NOTICE` | 1 | module and licensing |
| `internal/stage1/kernel_generic.go` | 1 | `masks` type, portable `classifyGeneric` (reference for the NEON kernel) |
| `internal/stage1/stage1.go` | 2 | scanner (escapes, strings, scalars), `Index`, `Minify`, stage 1 errors |
| `internal/stage1/kernel_purego.go` | 2, 10 | `classify` and `utf8Checker` for non-NEON builds |
| `errors.go` | 3 | all sentinel errors |
| `tape.go` | 3 | `Type`, tape tags, `word` |
| `parser.go` | 3 | `Document`, `Parser`, `Parse` |
| `stage2.go` | 3 | `builder`: structural walk, scopes, atoms |
| `strings.go` | 3 | string unescaping into the string buffer |
| `numbers.go` | 3 | number grammar and conversion, big integers |
| `element.go` | 4 | `Element`, `Array`, `Object`, getters, iteration |
| `serialize.go` | 5 | `AppendJSON`, `MarshalJSON`, `Minify` |
| `pointer.go` | 6 | `AtPointer` (RFC 6901, C++ error codes) |
| `scripts/fetch-testdata.sh`, `corpus_test.go` | 7 | corpora download and corpus tests |
| `fuzz_test.go` | 8 | `FuzzParse`, `FuzzMinify` |
| `bench_test.go` | 9 | benchmarks against `encoding/json` |
| `internal/stage1/kernel_arm64.go` | 10 | NEON `classify` and lookup4 UTF-8 validation |
| `scripts/check.sh` | 11 | build matrix (spec §8.4) |

---

### Task 1: Module scaffold and portable byte classifier

**Files:**
- Create: `go.mod`, `LICENSE`, `LICENSE-MIT`, `NOTICE`
- Create: `internal/stage1/kernel_generic.go`
- Test: `internal/stage1/kernel_generic_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces (package `stage1`): `type masks struct{ backslash, quote, ws, op, ctrl uint64 }` (bit i describes byte i of a 64-byte block); `func classifyGeneric(b *[64]byte) masks`; `func gather(w uint64) uint64`.

- [ ] **Step 1: Create the module and license files**

```bash
cd ~/code/mine/simdjson-go
printf 'module simdjson-go\n\ngo 1.27\n' > go.mod
cp ~/code/theirs/simdjson/LICENSE LICENSE
cp ~/code/theirs/simdjson/LICENSE-MIT LICENSE-MIT
mkdir -p internal/stage1
```

Create `NOTICE`:

```text
simdjson-go

This is a Go port of simdjson (https://github.com/simdjson/simdjson),
Copyright 2018-2025 The simdjson authors, available under the Apache
License 2.0 (LICENSE) or the MIT License (LICENSE-MIT), at your option.
```

- [ ] **Step 2: Write the failing test**

`internal/stage1/kernel_generic_test.go`:

```go
package stage1

import "testing"

func TestGather(t *testing.T) {
	for m := range 256 {
		var w uint64
		for j := range 8 {
			if m&(1<<j) != 0 {
				w |= 0xFF << (8 * j) // every bit set: only bit 0 of each byte may count
			}
		}
		if got := gather(w); got != uint64(m) {
			t.Fatalf("gather(%#x) = %#x, want %#x", w, got, m)
		}
	}
}

func TestClassifyGeneric(t *testing.T) {
	var b [64]byte
	copy(b[:], "{\"a\\\": [1,\t2]}\n\x01")
	for i := 16; i < 64; i++ {
		b[i] = 'x'
	}
	m := classifyGeneric(&b)
	// {"a\": [1,<tab>2]}<lf><0x01>
	// 0 12 345 6789 0 1 2 3  4   5   6
	want := masks{
		backslash: 1 << 3,
		quote:     1<<1 | 1<<4,
		ws:        1<<6 | 1<<10 | 1<<14,
		op:        1<<0 | 1<<5 | 1<<7 | 1<<9 | 1<<12 | 1<<13,
		ctrl:      1<<10 | 1<<14 | 1<<15,
	}
	if m != want {
		t.Fatalf("classifyGeneric = %+v\nwant              %+v", m, want)
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/stage1/`
Expected: build failure, `undefined: gather` / `undefined: classifyGeneric` / `undefined: masks`.

- [ ] **Step 4: Implement the classifier**

`internal/stage1/kernel_generic.go`:

```go
package stage1

// masks classifies the 64 bytes of a block: bit i describes byte i.
type masks struct {
	backslash uint64 // '\\'
	quote     uint64 // '"'
	ws        uint64 // ' ', '\t', '\n', '\r'
	op        uint64 // '{', '}', '[', ']', ':', ','
	ctrl      uint64 // bytes < 0x20
}

// Byte classes used by classifyGeneric; bit k of classTable[c] is class k.
const (
	classBackslash = 1 << iota
	classQuote
	classWS
	classOp
	classCtrl
)

var classTable = func() (t [256]uint8) {
	for c := range 0x20 {
		t[c] |= classCtrl
	}
	t['\\'] |= classBackslash
	t['"'] |= classQuote
	for _, c := range []byte(" \t\n\r") {
		t[c] |= classWS
	}
	for _, c := range []byte("{}[]:,") {
		t[c] |= classOp
	}
	return t
}()

// classifyGeneric is the portable classify. It is the reference the SIMD
// kernel is tested against, so it is compiled on every platform.
func classifyGeneric(b *[64]byte) masks {
	var m masks
	for i := 0; i < 64; i += 8 {
		// Gather 8 class bytes into one word, then move bit k of each byte
		// into an 8-bit mask with a multiply.
		var w uint64
		for j := 7; j >= 0; j-- {
			w = w<<8 | uint64(classTable[b[i+j]])
		}
		m.backslash |= gather(w) << i
		m.quote |= gather(w>>1) << i
		m.ws |= gather(w>>2) << i
		m.op |= gather(w>>3) << i
		m.ctrl |= gather(w>>4) << i
	}
	return m
}

// gather returns bit 0 of each byte of w as an 8-bit mask (byte j → bit j).
func gather(w uint64) uint64 {
	return (w & 0x0101010101010101) * 0x0102040810204080 >> 56
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/stage1/ && go vet ./... && gofmt -l .`
Expected: `ok  simdjson-go/internal/stage1`, no vet output, no gofmt output.

- [ ] **Step 6: Commit**

```bash
git add go.mod LICENSE LICENSE-MIT NOTICE internal/stage1
git commit -m "feat(stage1): add module and portable byte classifier

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Stage 1 structural indexer and Minify

Port of `src/generic/stage1/json_escape_scanner.h`, `json_string_scanner.h`, `json_scanner.h` and `json_structural_indexer.h`. The final partial block is copied into a space-padded `[64]byte`, so no input padding is needed. Errors are checked in C++ order: unclosed string, control character inside a string, empty document, invalid UTF-8.

**Files:**
- Create: `internal/stage1/stage1.go`
- Create: `internal/stage1/kernel_purego.go` (**no build tag yet**: Task 10 adds it together with the NEON kernel, so `GOEXPERIMENT=simd` builds keep compiling until then)
- Test: `internal/stage1/stage1_test.go`

**Interfaces:**
- Consumes: `masks`, `classifyGeneric` (Task 1).
- Produces (package `stage1`):
  - `func Index(buf []byte, idx []uint32) ([]uint32, error)` returns the offsets of all structural characters, reusing `idx`'s storage.
  - `func Minify(dst, src []byte) ([]byte, error)`.
  - `var ErrUnclosedString, ErrUnescapedChars, ErrEmpty, ErrUTF8 error`.
  - Unexported, used by Task 10: `func classify(b *[64]byte) masks`; `type utf8Checker` with `next(*[64]byte)` and `valid([]byte) bool`; `func block(src []byte, off int, tail *[64]byte) (*[64]byte, int)`.

- [ ] **Step 1: Write the failing test**

`internal/stage1/stage1_test.go`:

```go
package stage1

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestPrefixXor(t *testing.T) {
	if got := prefixXor(1<<2 | 1<<5); got != 0b11100 {
		t.Fatalf("prefixXor = %b", got)
	}
}

func TestIndex(t *testing.T) {
	tests := []struct {
		in   string
		want []uint32
	}{
		{`1`, []uint32{0}},
		{` true `, []uint32{1}},
		{`{"a":1}`, []uint32{0, 1, 4, 5, 6}},
		{`["x\"y",-1.5e3]`, []uint32{0, 1, 7, 8, 14}},
		{`[ "\\" , null ]`, []uint32{0, 2, 7, 9, 14}},
		{`"a"true`, []uint32{0, 3}}, // a scalar right after a string still starts a token
		// A string crossing the 64-byte block boundary, then an escaped backslash at the boundary.
		{`["` + strings.Repeat("x", 61) + `\\",2]`, []uint32{0, 1, 66, 67, 68}},
	}
	for _, tt := range tests {
		got, err := Index([]byte(tt.in), nil)
		if err != nil || !slices.Equal(got, tt.want) {
			t.Errorf("Index(%q) = %v, %v; want %v", tt.in, got, err, tt.want)
		}
	}
}

func TestIndexErrors(t *testing.T) {
	tests := []struct {
		in   string
		want error
	}{
		{"", ErrEmpty},
		{" \t\r\n ", ErrEmpty},
		{`"abc`, ErrUnclosedString},
		{`["a\"]`, ErrUnclosedString},
		{"\"a\x01\"", ErrUnescapedChars},
		{"[\"\x1f\"]", ErrUnescapedChars},
		{"\"\xff\"", ErrUTF8},
		{"[\"\xc3\"]", ErrUTF8},              // truncated 2-byte sequence
		{"\"\xed\xa0\x80\"", ErrUTF8},        // encoded surrogate
		{"\"\xc0\xaf\"", ErrUTF8},            // overlong
		{"\"abc\xe2\x82", ErrUnclosedString}, // unclosed wins over UTF-8
	}
	for _, tt := range tests {
		if _, err := Index([]byte(tt.in), nil); !errors.Is(err, tt.want) {
			t.Errorf("Index(%q) error = %v, want %v", tt.in, err, tt.want)
		}
	}
}

func TestMinify(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{" { \"a b\" : [ 1 , 2 ] } \n", `{"a b":[1,2]}`},
		{`"  \"  "  x  `, `"  \"  "x`},
		{strings.Repeat(" ", 70) + "[ 1 ]", "[1]"},
		{`[ "` + strings.Repeat(" ", 100) + `" ]`, `["` + strings.Repeat(" ", 100) + `"]`},
	}
	for _, tt := range tests {
		got, err := Minify(nil, []byte(tt.in))
		if err != nil || string(got) != tt.want {
			t.Errorf("Minify(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
	if _, err := Minify(nil, []byte(`["abc`)); !errors.Is(err, ErrUnclosedString) {
		t.Errorf("Minify unclosed: err = %v", err)
	}
}

func TestIndexBadUTF8(t *testing.T) {
	// Invalid sequences from C++ tests/unicode_tests.cpp, with and without a
	// long ASCII prefix so that they also start mid-block.
	bad := []string{
		"\xc3\x28", "\xa0\xa1", "\xe2\x28\xa1", "\xe2\x82\x28", "\xf0\x28\x8c\xbc", "\xf0\x90\x28\xbc",
		"\xf0\x28\x8c\x28", "\xc0\x9f", "\xf5\xff\xff\xff", "\xed\xa0\x81", "\xf8\x90\x80\x80\x80",
		"123456789012345\xed", "123456789012345\xf1", "123456789012345\xc2", "\xC2\x7F", "\xce",
		"\xce\xba\xe1", "\xce\xba\xe1\xbd", "\xce\xba\xe1\xbd\xb9\xcf", "\xce\xba\xe1\xbd\xb9\xcf\x83\xce",
		"\xce\xba\xe1\xbd\xb9\xcf\x83\xce\xbc\xce", "\xdf", "\xef\xbf", "\x80", "\x91\x85\x95\x9e", "\x6c\x02\x8e\x18",
	}
	for _, s := range bad {
		for _, in := range []string{s, strings.Repeat("a", 62) + s} {
			if _, err := Index([]byte(in), nil); !errors.Is(err, ErrUTF8) {
				t.Errorf("Index(%q) error = %v, want ErrUTF8", in, err)
			}
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/stage1/`
Expected: build failure, `undefined: prefixXor`, `undefined: Index`, `undefined: Minify`, `undefined: ErrEmpty`, and so on.

- [ ] **Step 3: Implement the scanner, Index and Minify**

`internal/stage1/stage1.go`:

```go
// Package stage1 finds the structural characters of a JSON document.
// It is a port of simdjson's src/generic/stage1 (json_escape_scanner.h,
// json_string_scanner.h, json_scanner.h, json_structural_indexer.h).
package stage1

import (
	"errors"
	"math/bits"
)

// Errors returned by Index, in the order simdjson checks them.
var (
	ErrUnclosedString = errors.New("simdjson: unclosed string")                       // UNCLOSED_STRING
	ErrUnescapedChars = errors.New("simdjson: unescaped control character in string") // UNESCAPED_CHARS
	ErrEmpty          = errors.New("simdjson: no JSON found")                         // EMPTY
	ErrUTF8           = errors.New("simdjson: invalid UTF-8")                         // UTF8_ERROR
)

// scanner carries state from one 64-byte block to the next.
type scanner struct {
	nextIsEscaped uint64 // 1 if the first byte of the next block is escaped
	prevInString  uint64 // all ones if the previous block ended inside a string
	prevScalar    uint64 // 1 if the previous block ended with a non-quote scalar byte
	unescaped     uint64 // accumulated control characters found inside strings
}

// next consumes one block and returns its structural characters (operators and
// the first byte of every scalar, including opening quotes) and its in-string mask
// (bytes inside strings, including the opening but not the closing quote).
func (s *scanner) next(m masks) (structurals, inString uint64) {
	// json_escape_scanner: which bytes are escaped by a backslash.
	var escaped uint64
	if m.backslash == 0 {
		escaped = s.nextIsEscaped
		s.nextIsEscaped = 0
	} else {
		const oddBits = 0xAAAAAAAAAAAAAAAA
		potential := m.backslash &^ s.nextIsEscaped
		escapeAndTerminal := ((potential<<1 | oddBits) - potential) ^ oddBits
		escaped = escapeAndTerminal ^ (m.backslash | s.nextIsEscaped)
		s.nextIsEscaped = (escapeAndTerminal & m.backslash) >> 63
	}

	// json_string_scanner: real quotes and the bytes between them.
	quote := m.quote &^ escaped
	inString = prefixXor(quote) ^ s.prevInString
	s.prevInString = uint64(int64(inString) >> 63)
	s.unescaped |= m.ctrl & inString

	// json_scanner: a scalar starts where a non-quote scalar byte does not precede it.
	scalar := ^(m.op | m.ws)
	nonQuoteScalar := scalar &^ quote
	followsNonQuoteScalar := nonQuoteScalar<<1 | s.prevScalar
	s.prevScalar = nonQuoteScalar >> 63
	scalarStart := scalar &^ followsNonQuoteScalar
	stringTail := inString ^ quote
	return (m.op | scalarStart) &^ stringTail, inString
}

// prefixXor sets bit i to the XOR of bits 0..i (6-step shift cascade).
func prefixXor(x uint64) uint64 {
	x ^= x << 1
	x ^= x << 2
	x ^= x << 4
	x ^= x << 8
	x ^= x << 16
	x ^= x << 32
	return x
}

// block returns the 64-byte block at src[off:] and the number of real bytes in
// it. A final partial block is copied into tail and padded with spaces.
func block(src []byte, off int, tail *[64]byte) (*[64]byte, int) {
	if len(src)-off >= 64 {
		return (*[64]byte)(src[off : off+64]), 64
	}
	n := copy(tail[:], src[off:])
	for i := n; i < 64; i++ {
		tail[i] = ' '
	}
	return tail, n
}

// Index appends to idx[:0] the offset of every structural character of buf and
// returns it. Errors are checked in simdjson's order: unclosed string, control
// character inside a string, empty document, invalid UTF-8.
func Index(buf []byte, idx []uint32) ([]uint32, error) {
	idx = idx[:0]
	if len(buf) == 0 {
		return idx, ErrEmpty
	}
	if cap(idx) < len(buf) {
		idx = make([]uint32, 0, len(buf)) // at most one structural per byte
	}
	var (
		s    scanner
		u    utf8Checker
		tail [64]byte
	)
	for off := 0; off < len(buf); off += 64 {
		blk, _ := block(buf, off, &tail)
		u.next(blk)
		structurals, _ := s.next(classify(blk))
		for structurals != 0 { // ponytail: plain loop; unroll if BenchmarkIndex shows it hot
			idx = append(idx, uint32(off+bits.TrailingZeros64(structurals)))
			structurals &= structurals - 1
		}
	}
	switch {
	case s.prevInString != 0:
		return idx, ErrUnclosedString
	case s.unescaped != 0:
		return idx, ErrUnescapedChars
	case len(idx) == 0:
		return idx, ErrEmpty
	case !u.valid(buf):
		return idx, ErrUTF8
	}
	return idx, nil
}

// Minify appends src to dst with all whitespace outside strings removed.
// Like C++ simdjson::minify it does not validate the document; its only
// error is ErrUnclosedString.
func Minify(dst, src []byte) ([]byte, error) {
	var (
		s    scanner
		tail [64]byte
	)
	for off := 0; off < len(src); off += 64 {
		blk, n := block(src, off, &tail)
		m := classify(blk)
		_, inString := s.next(m)
		keep := ^(m.ws &^ inString)
		if n < 64 {
			keep &= 1<<n - 1
		}
		for keep != 0 { // copy each run of kept bytes
			i := bits.TrailingZeros64(keep)
			run := bits.TrailingZeros64(^(keep >> i))
			dst = append(dst, blk[i:i+run]...)
			keep &^= (1<<run - 1) << i
		}
	}
	if s.prevInString != 0 {
		return dst, ErrUnclosedString
	}
	return dst, nil
}
```

- [ ] **Step 4: Add the portable kernel glue (no build tag in this task)**

`internal/stage1/kernel_purego.go`:

```go
package stage1

import "unicode/utf8"

func classify(b *[64]byte) masks { return classifyGeneric(b) }

// utf8Checker validates the whole input at the end with the standard library.
type utf8Checker struct{}

func (*utf8Checker) next(*[64]byte) {}

func (*utf8Checker) valid(buf []byte) bool { return utf8.Valid(buf) }
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/stage1/ && GOEXPERIMENT=simd go test ./internal/stage1/ && go vet ./... && gofmt -l .`
Expected: `ok` twice, nothing else.

- [ ] **Step 6: Commit**

```bash
git add internal/stage1
git commit -m "feat(stage1): structural indexer, UTF-8 check and minifier

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: Stage 2 parser writing the C++ tape

Port of `src/generic/stage2/json_iterator.h` (the gotos mirror its labels), `tape_builder.h`, `stringparsing.h`, and `parse_number` from `include/simdjson/generic/numberparsing.h`. Floats go through `strconv.ParseFloat` after the JSON grammar is checked; it fails only when the value overflows to ±Inf, which is exactly what C++ rejects. Two behaviors in this task are confirmed against C++ and are easy to "fix" by mistake. First, a value inside a container that starts with a byte below `'0'` is `ErrNumber`. Second, an over-long integer is `ErrBigInt` even when a bad byte follows it.

**Files:**
- Create: `errors.go`, `tape.go`, `parser.go`, `stage2.go`, `strings.go`, `numbers.go`
- Test: `tape_test.go`, `parser_test.go`, `numbers_test.go`, `strings_test.go`

**Interfaces:**
- Consumes: `stage1.Index`, `stage1.ErrUnclosedString`, `stage1.ErrUnescapedChars`, `stage1.ErrEmpty`, `stage1.ErrUTF8` (Task 2).
- Produces (package `simdjson`):
  - Exported: `type Parser struct{ MaxDepth int; BigIntAsString bool; ... }`, `func (p *Parser) Parse(b []byte) (*Document, error)`, `type Document struct{ tape []uint64; strings []byte }`, and `type Type byte` with constants `TypeArray '['`, `TypeObject '{'`, `TypeInt64 'l'`, `TypeUint64 'u'`, `TypeFloat64 'd'`, `TypeString '"'`, `TypeBool 't'`, `TypeNull 'n'`, `TypeBigInt 'Z'` and `func (Type) String() string`.
  - Exported errors: `ErrCapacity`, `ErrTape`, `ErrDepth`, `ErrString`, `ErrTAtom`, `ErrFAtom`, `ErrNAtom`, `ErrNumber`, `ErrBigInt`, `ErrIncorrectType`, `ErrNumberOutOfRange`, `ErrIndexOutOfBounds`, `ErrNoSuchField`, `ErrInvalidJSONPointer`, `ErrUnclosedString`, `ErrUnescapedChars`, `ErrEmpty`, `ErrUTF8`.
  - Unexported, used by later tasks: `tagRoot`, `tagEndArray`, `tagEndObject`, `tagFalse`; `func word(tag byte, payload uint64) uint64`; `var bom []byte`; `isStructuralOrSpace [256]bool`; `func terminates(buf []byte, p int) bool`.
  - Test helpers used by later tasks: `dumpTape(*Document) []string`, `parseTape`, `rootValue` (all in `parser_test.go`).

- [ ] **Step 1: Write the failing tests**

`tape_test.go`:

```go
package simdjson

import "testing"

func TestTypeString(t *testing.T) {
	want := map[Type]string{
		TypeArray: "array", TypeObject: "object", TypeInt64: "int64_t", TypeUint64: "uint64_t",
		TypeFloat64: "double", TypeString: "string", TypeBool: "bool", TypeNull: "null", TypeBigInt: "bigint",
		Type(0): "unknown",
	}
	for typ, name := range want {
		if typ.String() != name {
			t.Errorf("%q: %q, want %q", byte(typ), typ.String(), name)
		}
	}
}
```

`parser_test.go` (the expected errors in `TestParseErrors` were produced by C++ simdjson v5.0.2):

```go
package simdjson

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// dumpTape renders a tape one entry per value, in the notation of
// doc/tape.md: numbers show their value, strings their unescaped bytes,
// containers the index after their end and their element count.
func dumpTape(d *Document) []string {
	var out []string
	for i := 0; i < len(d.tape); i++ {
		w := d.tape[i]
		tag, payload := byte(w>>56), w&(1<<56-1)
		switch tag {
		case 'l':
			i++
			out = append(out, "l "+strconv.FormatInt(int64(d.tape[i]), 10))
		case 'u':
			i++
			out = append(out, "u "+strconv.FormatUint(d.tape[i], 10))
		case 'd':
			i++
			out = append(out, "d "+strconv.FormatFloat(math.Float64frombits(d.tape[i]), 'g', -1, 64))
		case '"', 'Z':
			n := uint64(binary.LittleEndian.Uint32(d.strings[payload:]))
			s := string(d.strings[payload+4 : payload+4+n])
			if d.strings[payload+4+n] != 0 {
				s += " (missing NUL)"
			}
			if tag == 'Z' {
				out = append(out, "Z "+s)
			} else {
				out = append(out, strconv.Quote(s))
			}
		case '{', '[':
			out = append(out, fmt.Sprintf("%c %d n=%d", tag, uint32(payload), payload>>32))
		default:
			out = append(out, fmt.Sprintf("%c %d", tag, payload))
		}
	}
	return out
}

// parseTape parses in with p and returns dumpTape of the result.
func parseTape(t *testing.T, p *Parser, in string) []string {
	t.Helper()
	doc, err := p.Parse([]byte(in))
	if err != nil {
		t.Fatalf("Parse(%.80q): %v", in, err)
	}
	return dumpTape(doc)
}

// rootValue returns the tape entry of a scalar document's value.
func rootValue(t *testing.T, in string) string {
	t.Helper()
	var p Parser
	return parseTape(t, &p, in)[1]
}

func TestTapeLayout(t *testing.T) {
	// The example of doc/tape.md; the comments give the tape index.
	const in = `{"Image":{"Width":800,"Height":600,"Title":"View from 15th Floor",
		"Thumbnail":{"Url":"http://www.example.com/image/481989943","Height":125,"Width":100},
		"Animated":false,"IDs":[116,943,234,38793]}}`
	want := []string{
		"r 39", "{ 38 n=1", `"Image"`, "{ 37 n=6", // 0-3
		`"Width"`, "l 800", `"Height"`, "l 600", // 4, 5-6, 7, 8-9
		`"Title"`, `"View from 15th Floor"`, `"Thumbnail"`, "{ 23 n=3", // 10-13
		`"Url"`, `"http://www.example.com/image/481989943"`, `"Height"`, "l 125", `"Width"`, "l 100", // 14-21
		"} 13", `"Animated"`, "f 0", `"IDs"`, "[ 36 n=4", // 22-26
		"l 116", "l 943", "l 234", "l 38793", // 27-34
		"] 26", "} 3", "} 1", "r 0", // 35-38
	}
	var p Parser
	if got := parseTape(t, &p, in); !slices.Equal(got, want) {
		t.Fatalf("tape:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if got := parseTape(t, &p, `[[],{},true,null]`); !slices.Equal(got, []string{
		"r 10", "[ 9 n=4", "[ 4 n=0", "] 2", "{ 6 n=0", "} 4", "t 0", "n 0", "] 1", "r 0",
	}) {
		t.Errorf("empty containers: %q", got)
	}
}

func TestParseErrors(t *testing.T) {
	// Expected errors were produced by C++ simdjson v5.0.2 (dom::parser).
	tests := []struct {
		in   string
		want error
	}{
		{"", ErrEmpty},
		{" ", ErrEmpty},
		{"\t\n", ErrEmpty},
		{"{", ErrTape},
		{"[", ErrTape},
		{"\"", ErrUnclosedString},
		{"\"abc", ErrUnclosedString},
		{"[1,", ErrTape},
		{"{\"a\":", ErrTape},
		{"tru", ErrTAtom},
		{"nul", ErrNAtom},
		{"fals", ErrFAtom},
		{"12.", ErrNumber},
		{"-", ErrNumber},
		{"\x00", ErrTape},
		{"[[]", ErrTape},
		{"{\"a\":{}", ErrTape},
		{"1 2", ErrTape},
		{"[] []", ErrTape},
		{"{}x", ErrTape},
		{"[1]]", ErrTape},
		{"[1 2]", ErrTape},
		{"{\"a\" 1}", ErrTape},
		{"{\"a\":1 \"b\":2}", ErrTape},
		{"{1:2}", ErrTape},
		{"[,1]", ErrNumber}, // C++ sends every byte below '0' in a container to the number parser
		{"[1,]", ErrTape},
		{"{\"a\":1,}", ErrTape},
		{"{\"a\"}", ErrTape},
		{"]", ErrTape},
		{"\"a\"x", ErrTape},
		{"\"a\"true", ErrTape},
		{"truefalse", ErrTAtom},
		{"[true false]", ErrTape},
		{"01", ErrNumber},
		{"-01", ErrNumber},
		{"1.", ErrNumber},
		{".1", ErrTape},
		{"1e", ErrNumber},
		{"+1", ErrTape},
		{"--1", ErrNumber},
		{"0x10", ErrNumber},
		{"NaN", ErrTape},
		{"[NaN]", ErrTape},
		{"[Infinity]", ErrTape},
		{"1e400", ErrNumber},
		{"1x", ErrNumber},
		{"[1x]", ErrNumber},
		{"1\x00", ErrNumber},
		{"trux", ErrTAtom},
		{"ture", ErrTAtom},
		{"falx", ErrFAtom},
		{"nulx", ErrNAtom},
		{"[truex]", ErrTAtom},
		{"\"\\uD800\"", ErrString},
		{"\"\\uDC00\"", ErrString},
		{"\"\\uD800\\u0041\"", ErrString},
		{"\"\\u12G4\"", ErrString},
		{"\"\\x\"", ErrString},
		{"\"\x01\"", ErrUnescapedChars},
		{"\"\xff\"", ErrUTF8},
		{"\"\xc3\"", ErrUTF8},
		{"\"\xed\xa0\x80\"", ErrUTF8},
		{"[\f]", ErrNumber},
		{"\"\\", ErrUnclosedString},
		{"\"\\\"", ErrUnclosedString},
		{"18446744073709551616", ErrBigInt},
		{"-9223372036854775809", ErrBigInt},
		{"123456789012345678901x", ErrBigInt}, // too long is reported before the bad terminator
		{"[123456789012345678901x]", ErrBigInt},
		{"[7,7,7,7,6,7,7,7,6,7,7,6,[7,7,7,7,6,7,7,7,6,7,7,6,7,7,7,7,7,7,6", ErrTape}, // C++ issue 345
	}
	for _, tt := range tests {
		var p Parser
		doc, err := p.Parse([]byte(tt.in))
		if !errors.Is(err, tt.want) || doc != nil {
			t.Errorf("Parse(%q) = %v, %v; want nil, %v", tt.in, doc, err, tt.want)
		}
	}
}

func TestMaxDepth(t *testing.T) {
	// Only non-empty arrays/objects count: an empty one is written without
	// opening a scope (C++ visit_empty_array), so 1024 brackets ending in []
	// are accepted while 1024 levels around a value are not.
	nest := func(n int, inner string) []byte {
		return []byte(strings.Repeat("[", n) + inner + strings.Repeat("]", n))
	}
	var p Parser
	for _, tt := range []struct {
		in   []byte
		want error
	}{
		{nest(1023, "1"), nil},
		{nest(1024, "1"), ErrDepth},
		{nest(1023, "[]"), nil},
		{nest(1024, "[]"), ErrDepth},
		{[]byte(strings.Repeat(`{"a":`, 1024) + "1" + strings.Repeat("}", 1024)), ErrDepth},
	} {
		if _, err := p.Parse(tt.in); !errors.Is(err, tt.want) || (tt.want == nil) != (err == nil) {
			t.Errorf("depth case %.20q...: err = %v, want %v", tt.in, err, tt.want)
		}
	}
	p.MaxDepth = 3
	if _, err := p.Parse([]byte(`{"a":[1]}`)); err != nil {
		t.Errorf("MaxDepth 3, depth 2: %v", err)
	}
	if _, err := p.Parse([]byte(`[[[1]]]`)); !errors.Is(err, ErrDepth) {
		t.Errorf("MaxDepth 3, depth 3: err = %v, want ErrDepth", err)
	}
}

func TestByteOrderMark(t *testing.T) {
	var p Parser
	const doc = `{"score":0.8825149536132812}`
	if got, want := parseTape(t, &p, "\xEF\xBB\xBF"+doc), parseTape(t, &p, doc); !slices.Equal(got, want) {
		t.Errorf("with BOM %q, without %q", got, want)
	}
	if _, err := p.Parse([]byte("\xEF\xBB\xBF")); !errors.Is(err, ErrEmpty) {
		t.Errorf("BOM only: err = %v, want ErrEmpty", err)
	}
}

func TestNoReadPastLength(t *testing.T) {
	// C++ errortests number_overrun_*: bytes after len(b) are never read, even
	// when they are in the slice's capacity.
	const filler = "22222222222222222222222222222222222222222222222222222222222222222"
	var p Parser
	buf := []byte("1" + filler + ",")
	doc, err := p.Parse(buf[:1])
	if err != nil {
		t.Fatal(err)
	}
	if got := dumpTape(doc); !slices.Equal(got, []string{"r 4", "l 1", "r 0"}) {
		t.Errorf("tape = %q", got)
	}
	for _, in := range []string{"[1" + filler + "]", `{"key":1` + filler + "}"} {
		b := []byte(in)
		n := strings.Index(in, "1") + 1
		if _, err := p.Parse(b[:n]); !errors.Is(err, ErrTape) {
			t.Errorf("Parse(%q) err = %v, want ErrTape", b[:n], err)
		}
	}
	brackets := []byte("[][[[[[[[[[[[[[[[[[") // C++ document_tests padded_with_open_bracket
	if _, err := p.Parse(brackets[:2]); err != nil {
		t.Errorf("[]: %v", err)
	}
	if _, err := p.Parse(brackets[2:4]); !errors.Is(err, ErrTape) {
		t.Errorf("[[: err = %v, want ErrTape", err)
	}
}

func TestParserReuse(t *testing.T) {
	var reused Parser
	for _, in := range []string{"[true,false]", `{"yay":"json!"}`, "[1,2,3,null]", `"x"`, "[1,", "[]"} {
		var fresh Parser
		_, wantErr := fresh.Parse([]byte(in))
		doc, err := reused.Parse([]byte(in))
		if (err == nil) != (wantErr == nil) {
			t.Fatalf("Parse(%q): err = %v, fresh parser err = %v", in, err, wantErr)
		}
		if err == nil {
			if got, want := dumpTape(doc), parseTape(t, &fresh, in); !slices.Equal(got, want) {
				t.Errorf("reused %q, fresh %q", got, want)
			}
		}
	}
}
```

`numbers_test.go`:

```go
package simdjson

import (
	"errors"
	"math"
	"strconv"
	"testing"
)

func TestIntegers(t *testing.T) {
	tests := []struct{ in, want string }{
		{"0", "l 0"},
		{"-0", "l 0"},
		{"42", "l 42"},
		{"-1", "l -1"},
		{"9223372036854775807", "l 9223372036854775807"},
		{"-9223372036854775808", "l -9223372036854775808"},
		{"9223372036854775808", "u 9223372036854775808"},
		{"9999999999999999999", "u 9999999999999999999"},
		{"18446744073709551615", "u 18446744073709551615"},
	}
	for _, tt := range tests {
		if got := rootValue(t, tt.in); got != tt.want {
			t.Errorf("%s: %q, want %q", tt.in, got, tt.want)
		}
		var p Parser
		if got := parseTape(t, &p, `{"key": `+tt.in+`}`)[3]; got != tt.want {
			t.Errorf("{key:%s}: %q, want %q", tt.in, got, tt.want)
		}
	}
	for i := -1024; i < 1024; i++ { // C++ basictests small_integers
		if got := rootValue(t, strconv.Itoa(i)); got != "l "+strconv.Itoa(i) {
			t.Fatalf("%d: %q", i, got)
		}
	}
}

func TestBigInt(t *testing.T) {
	var p Parser
	for _, in := range []string{`{"val":123456789012345678901}`, `{"val":18446744073709551616}`} {
		if _, err := p.Parse([]byte(in)); !errors.Is(err, ErrBigInt) {
			t.Errorf("default %s: err = %v, want ErrBigInt", in, err)
		}
	}
	p.BigIntAsString = true
	for _, digits := range []string{"123456789012345678901", "-12345678901234567890", "18446744073709551616", "99999999999999999999"} {
		if got := parseTape(t, &p, `{"val":`+digits+`}`)[3]; got != "Z "+digits {
			t.Errorf("%s: %q", digits, got)
		}
	}
	if got := parseTape(t, &p, `[1, 123456789012345678901, 3]`)[1]; got != "[ 8 n=3" { // tape r [ l . Z l . ] r: "[" points past "]"
		t.Errorf("array with big int: %q", got)
	}
	for _, in := range []string{`{"val":123456789012345678901x}`, `{"val":-123456789012345678901x}`, "123456789012345678901x"} {
		if _, err := p.Parse([]byte(in)); !errors.Is(err, ErrNumber) {
			t.Errorf("%s: err = %v, want ErrNumber", in, err)
		}
	}
}

func TestFloats(t *testing.T) {
	tests := []struct {
		in   string
		want float64
	}{
		// C++ basictests ground_truth
		{"2.2250738585072013e-308", 0x1p-1022},
		{"-92666518056446206563E3", -0x1.39f764644154dp+76},
		{"-42823146028335318693e-128", -0x1.0176daa6cdaafp-360},
		{"90054602635948575728E72", 0x1.61ab4ea9cb6c3p+305},
		{"1.00000000000000188558920870223463870174566020691753515394643550663070558368373221972569761144603605635692374830246134201063722058e-309", 0x0.0b8157268fdafp-1022},
		{"0e9999999999999999999999999999", 0},
		{"-2402844368454405395.2", -0x1.0ac4f1c7422e7p+61},
		// C++ basictests nines
		{"9999999999999999999e0", 9999999999999999999.0},
		{"9999999999999999999.0", 9999999999999999999.0},
		{"999999999999999999.9", 999999999999999999.9},
		{"9.999999999999999999", 9.999999999999999999},
		{"0.09999999999999999999", 0.09999999999999999999},
		// C++ issues 2017 and 2570
		{"0.8825149536132812", 0.8825149536132812},
		{"44.411101", 44.411101},
		{"8.908021", 8.908021},
		// limits
		{"1.7976931348623157e308", math.MaxFloat64},
		{"4.9e-324", 5e-324},
		{"1e-400", 0},
		{"1E+2", 100},
		{"-0.0", math.Copysign(0, -1)},
		{"-1e-400", math.Copysign(0, -1)},
	}
	for _, tt := range tests {
		want := "d " + strconv.FormatFloat(tt.want, 'g', -1, 64)
		if got := rootValue(t, tt.in); got != want {
			t.Errorf("%s: %q, want %q", tt.in, got, want)
		}
		var p Parser
		if got := parseTape(t, &p, "["+tt.in+"]")[2]; got != want {
			t.Errorf("[%s]: %q, want %q", tt.in, got, want)
		}
	}
}

func TestNumberErrors(t *testing.T) {
	for _, in := range []string{"1e400", "-1e400", "1e99999999999999999999", "1.", "1.e5", "1e", "1e+", "01", "-", "-a", "1.5.2", "1ee5", "2x"} {
		var p Parser
		if _, err := p.Parse([]byte(in)); !errors.Is(err, ErrNumber) {
			t.Errorf("%s: err = %v, want ErrNumber", in, err)
		}
	}
}
```

`strings_test.go`:

```go
package simdjson

import (
	"strconv"
	"strings"
	"testing"
)

func TestStrings(t *testing.T) {
	tests := []struct{ in, want string }{
		{`""`, ""},
		{`"abc"`, "abc"},
		{`"\"\\\/\b\f\n\r\t"`, "\"\\/\b\f\n\r\t"},
		{`"\u0000"`, "\x00"},
		{`"\u0041\u00e9\u20ac"`, "Aé€"},
		{`"\ud83d\ude00"`, "😀"},
		{`"\ud83d\ude00x"`, "😀x"},
		{"\"é€😀\"", "é€😀"},
	}
	for _, tt := range tests {
		want := strconv.Quote(tt.want)
		if got := rootValue(t, tt.in); got != want {
			t.Errorf("%s: %s, want %s", tt.in, got, want)
		}
		var p Parser
		if got := parseTape(t, &p, `{`+tt.in+`:1}`)[2]; got != want { // keys are unescaped too
			t.Errorf("key %s: %s, want %s", tt.in, got, want)
		}
	}
}

func TestStringsAcrossBlocks(t *testing.T) {
	// Escapes and multi-byte characters at every offset around the 64-byte
	// stage 1 block boundary.
	for pad := 50; pad < 80; pad++ {
		x := strings.Repeat("x", pad)
		for _, tt := range []struct{ esc, want string }{
			{`\"`, `"`}, {`\\`, `\`}, {`\n`, "\n"}, {`\u00e9`, "é"}, {`\ud83d\ude00`, "😀"}, {"é", "é"}, {"😀", "😀"},
		} {
			want := strconv.Quote(x + tt.want + "!")
			if got := rootValue(t, `"`+x+tt.esc+`!"`); got != want {
				t.Fatalf("pad %d %s: %s, want %s", pad, tt.esc, got, want)
			}
		}
	}
}

func TestIndexQuoteOrBackslash(t *testing.T) {
	for n := range 40 {
		for _, c := range []byte{'"', '\\'} {
			s := []byte(strings.Repeat("x", n) + string(c) + "yy")
			if got := indexQuoteOrBackslash(s); got != n {
				t.Fatalf("indexQuoteOrBackslash(%q) = %d, want %d", s, got, n)
			}
		}
		if got := indexQuoteOrBackslash([]byte(strings.Repeat("x", n))); got != -1 {
			t.Fatalf("no match, len %d: got %d", n, got)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test .`
Expected: build failure, `undefined: Document`, `undefined: Parser`, `undefined: ErrEmpty`, and so on.

- [ ] **Step 3: Add the errors and the tape vocabulary**

`errors.go`:

```go
package simdjson

import (
	"errors"

	"simdjson-go/internal/stage1"
)

// Errors returned by this package, one per C++ simdjson error_code it can
// produce (the C++ name is in each comment). Match them with errors.Is.
var (
	ErrCapacity           = errors.New("simdjson: document larger than 4 GiB")      // CAPACITY
	ErrTape               = errors.New("simdjson: invalid JSON structure")          // TAPE_ERROR
	ErrDepth              = errors.New("simdjson: maximum nesting depth exceeded")  // DEPTH_ERROR
	ErrString             = errors.New("simdjson: invalid string escape")           // STRING_ERROR
	ErrTAtom              = errors.New("simdjson: invalid value starting with 't'") // T_ATOM_ERROR
	ErrFAtom              = errors.New("simdjson: invalid value starting with 'f'") // F_ATOM_ERROR
	ErrNAtom              = errors.New("simdjson: invalid value starting with 'n'") // N_ATOM_ERROR
	ErrNumber             = errors.New("simdjson: invalid number")                  // NUMBER_ERROR
	ErrBigInt             = errors.New("simdjson: integer does not fit in 64 bits") // BIGINT_ERROR
	ErrIncorrectType      = errors.New("simdjson: incorrect type")                  // INCORRECT_TYPE
	ErrNumberOutOfRange   = errors.New("simdjson: number out of range")             // NUMBER_OUT_OF_RANGE
	ErrIndexOutOfBounds   = errors.New("simdjson: index out of bounds")             // INDEX_OUT_OF_BOUNDS
	ErrNoSuchField        = errors.New("simdjson: no such field")                   // NO_SUCH_FIELD
	ErrInvalidJSONPointer = errors.New("simdjson: invalid JSON pointer")            // INVALID_JSON_POINTER

	ErrUnclosedString = stage1.ErrUnclosedString // UNCLOSED_STRING
	ErrUnescapedChars = stage1.ErrUnescapedChars // UNESCAPED_CHARS
	ErrEmpty          = stage1.ErrEmpty          // EMPTY
	ErrUTF8           = stage1.ErrUTF8           // UTF8_ERROR
)
```

`tape.go`:

```go
package simdjson

// Type is the type of a JSON value. Its values are the tape tags of C++
// simdjson (doc/tape.md); false is stored as 'f' but reported as TypeBool.
type Type byte

const (
	TypeArray   Type = '['
	TypeObject  Type = '{'
	TypeInt64   Type = 'l'
	TypeUint64  Type = 'u'
	TypeFloat64 Type = 'd'
	TypeString  Type = '"'
	TypeBool    Type = 't'
	TypeNull    Type = 'n'
	TypeBigInt  Type = 'Z'
)

// String returns the C++ element_type name.
func (t Type) String() string {
	switch t {
	case TypeArray:
		return "array"
	case TypeObject:
		return "object"
	case TypeInt64:
		return "int64_t"
	case TypeUint64:
		return "uint64_t"
	case TypeFloat64:
		return "double"
	case TypeString:
		return "string"
	case TypeBool:
		return "bool"
	case TypeNull:
		return "null"
	case TypeBigInt:
		return "bigint"
	}
	return "unknown"
}

// Tape tags that are not value types.
const (
	tagRoot      = 'r'
	tagEndArray  = ']'
	tagEndObject = '}'
	tagFalse     = 'f'
)

// word builds a tape word: an 8-bit tag above a 56-bit payload.
func word(tag byte, payload uint64) uint64 { return uint64(tag)<<56 | payload }
```

- [ ] **Step 4: Add the parser and the stage 2 walk**

`parser.go`:

```go
// Package simdjson is a Go port of the simdjson DOM parser
// (https://github.com/simdjson/simdjson).
package simdjson

import (
	"bytes"
	"slices"

	"simdjson-go/internal/stage1"
)

const (
	defaultMaxDepth = 1024       // C++ DEFAULT_MAX_DEPTH
	maxSize         = 0xFFFFFFFF // C++ SIMDJSON_MAXSIZE_BYTES
)

var bom = []byte{0xEF, 0xBB, 0xBF}

// Document is a parsed JSON document in C++ simdjson's tape format
// (doc/tape.md). It is owned by the Parser that produced it.
type Document struct {
	tape    []uint64
	strings []byte
}

// Parser parses JSON documents. The zero value is ready to use. A Parser
// reuses its buffers across calls and must not be used by more than one
// goroutine at a time.
type Parser struct {
	// MaxDepth limits nesting: at most MaxDepth-1 non-empty arrays/objects
	// may be nested (empty ones do not count, as in C++). 0 means 1024.
	MaxDepth int
	// BigIntAsString stores integers that fit neither int64 nor uint64 as
	// TypeBigInt (their raw digits) instead of failing with ErrBigInt.
	BigIntAsString bool

	indices []uint32
	stack   []scope
	doc     Document
}

// Parse parses b. The returned Document is valid until the next call to
// p.Parse. b is neither retained nor modified, and needs no padding.
// A leading UTF-8 byte-order mark is skipped, as in C++.
func (p *Parser) Parse(b []byte) (*Document, error) {
	if uint64(len(b)) > maxSize {
		return nil, ErrCapacity
	}
	b = bytes.TrimPrefix(b, bom)
	var err error
	if p.indices, err = stage1.Index(b, p.indices); err != nil {
		return nil, err
	}
	bd := builder{
		buf:            b,
		idx:            p.indices,
		tape:           slices.Grow(p.doc.tape[:0], len(b)+3),
		strs:           slices.Grow(p.doc.strings[:0], 5*len(b)/3+64),
		stack:          p.stack[:0],
		maxDepth:       p.MaxDepth,
		bigIntAsString: p.BigIntAsString,
	}
	if bd.maxDepth <= 0 {
		bd.maxDepth = defaultMaxDepth
	}
	err = bd.walk()
	p.doc.tape, p.doc.strings, p.stack = bd.tape, bd.strs, bd.stack
	if err != nil {
		return nil, err
	}
	return &p.doc, nil
}
```

`stage2.go`:

```go
package simdjson

// scope is an open array or object.
type scope struct {
	tapeIndex uint32 // tape index of the opening word, written when the scope closes
	count     uint32 // number of elements (arrays) or fields (objects)
	isArray   bool
}

// builder is stage 2: it walks the structural indices found by stage 1 and
// writes the tape. It is a port of src/generic/stage2/json_iterator.h
// (walk_document) and tape_builder.h; the gotos mirror the C++ labels.
type builder struct {
	buf            []byte
	idx            []uint32
	pos            int // next entry of idx
	tape           []uint64
	strs           []byte
	stack          []scope
	maxDepth       int
	bigIntAsString bool
}

// isStructuralOrSpace is C++ structural_or_whitespace: the bytes that may
// follow a number or atom.
var isStructuralOrSpace = [256]bool{
	' ': true, '\t': true, '\n': true, '\r': true,
	',': true, ':': true, '[': true, ']': true, '{': true, '}': true,
}

// advance returns the byte at the next structural index and its offset.
// Past the last index it returns 0, like the C++ unpadded sentinel.
func (b *builder) advance() (byte, int) {
	if b.pos >= len(b.idx) {
		b.pos++
		return 0, len(b.buf)
	}
	off := int(b.idx[b.pos])
	b.pos++
	return b.buf[off], off
}

func (b *builder) peek() byte {
	if b.pos >= len(b.idx) {
		return 0
	}
	return b.buf[b.idx[b.pos]]
}

func (b *builder) walk() error {
	var (
		c   byte
		off int
		err error
	)
	b.tape = append(b.tape, 0) // root word, written at documentEnd

	c, off = b.advance()
	// An unmatched outer brace or bracket is rejected up front (simdjson issue 906).
	switch last := b.buf[b.idx[len(b.idx)-1]]; c {
	case '{':
		if last != '}' {
			return ErrTape
		}
	case '[':
		if last != ']' {
			return ErrTape
		}
	}
	switch c {
	case '{':
		if b.peek() == '}' {
			b.advance()
			b.emptyContainer('{', tagEndObject)
			goto documentEnd
		}
		goto objectBegin
	case '[':
		if b.peek() == ']' {
			b.advance()
			b.emptyContainer('[', tagEndArray)
			goto documentEnd
		}
		goto arrayBegin
	}
	if err = b.primitive(c, off, true); err != nil {
		return err
	}
	goto documentEnd

objectBegin:
	if err = b.push(false); err != nil {
		return err
	}
	if c, off = b.advance(); c != '"' {
		return ErrTape
	}
	b.stack[len(b.stack)-1].count++
	if err = b.str(off); err != nil {
		return err
	}

objectField:
	if c, _ = b.advance(); c != ':' {
		return ErrTape
	}
	c, off = b.advance()
	switch c {
	case '{':
		if b.peek() == '}' {
			b.advance()
			b.emptyContainer('{', tagEndObject)
			goto objectContinue
		}
		goto objectBegin
	case '[':
		if b.peek() == ']' {
			b.advance()
			b.emptyContainer('[', tagEndArray)
			goto objectContinue
		}
		goto arrayBegin
	}
	if err = b.primitive(c, off, false); err != nil {
		return err
	}

objectContinue:
	switch c, _ = b.advance(); c {
	case ',':
		b.stack[len(b.stack)-1].count++
		if c, off = b.advance(); c != '"' {
			return ErrTape
		}
		if err = b.str(off); err != nil {
			return err
		}
		goto objectField
	case '}':
		b.endContainer('{', tagEndObject)
		goto scopeEnd
	}
	return ErrTape

scopeEnd:
	b.stack = b.stack[:len(b.stack)-1]
	if len(b.stack) == 0 {
		goto documentEnd
	}
	if b.stack[len(b.stack)-1].isArray {
		goto arrayContinue
	}
	goto objectContinue

arrayBegin:
	if err = b.push(true); err != nil {
		return err
	}
	b.stack[len(b.stack)-1].count++

arrayValue:
	c, off = b.advance()
	switch c {
	case '{':
		if b.peek() == '}' {
			b.advance()
			b.emptyContainer('{', tagEndObject)
			goto arrayContinue
		}
		goto objectBegin
	case '[':
		if b.peek() == ']' {
			b.advance()
			b.emptyContainer('[', tagEndArray)
			goto arrayContinue
		}
		goto arrayBegin
	}
	if err = b.primitive(c, off, false); err != nil {
		return err
	}

arrayContinue:
	switch c, _ = b.advance(); c {
	case ',':
		b.stack[len(b.stack)-1].count++
		goto arrayValue
	case ']':
		b.endContainer('[', tagEndArray)
		goto scopeEnd
	}
	return ErrTape

documentEnd:
	b.tape = append(b.tape, word(tagRoot, 0))
	b.tape[0] = word(tagRoot, uint64(len(b.tape)))
	if b.pos != len(b.idx) { // more than one root value, or trailing content
		return ErrTape
	}
	return nil
}

// push opens a scope. Like C++, depth counts open scopes and must stay below maxDepth.
func (b *builder) push(isArray bool) error {
	if len(b.stack)+1 >= b.maxDepth {
		return ErrDepth
	}
	b.stack = append(b.stack, scope{tapeIndex: uint32(len(b.tape)), isArray: isArray})
	b.tape = append(b.tape, 0) // written by endContainer
	return nil
}

func (b *builder) emptyContainer(start, end byte) {
	i := uint64(len(b.tape))
	b.tape = append(b.tape, word(start, i+2), word(end, i))
}

// endContainer writes the closing word (pointing at the opening one) and the
// opening word (index after the closing word, element count saturated to 24 bits).
func (b *builder) endContainer(start, end byte) {
	s := b.stack[len(b.stack)-1]
	b.tape = append(b.tape, word(end, uint64(s.tapeIndex)))
	count := min(s.count, 0xFFFFFF)
	b.tape[s.tapeIndex] = word(start, uint64(count)<<32|uint64(len(b.tape)))
}

// primitive parses a string, number or atom. Inside arrays and objects C++
// visit_primitive tests (c - '0') < 10 in int arithmetic, so every byte below
// '0' goes to the number parser (NUMBER_ERROR); at the root it is a TAPE_ERROR.
func (b *builder) primitive(c byte, off int, root bool) error {
	switch {
	case c == '"':
		return b.str(off)
	case c == '-' || isDigit(c), !root && c < '0':
		return b.number(off)
	case c == 't':
		if !b.atom(off, "true") {
			return ErrTAtom
		}
		b.tape = append(b.tape, word('t', 0))
	case c == 'f':
		if !b.atom(off, "false") {
			return ErrFAtom
		}
		b.tape = append(b.tape, word(tagFalse, 0))
	case c == 'n':
		if !b.atom(off, "null") {
			return ErrNAtom
		}
		b.tape = append(b.tape, word('n', 0))
	default:
		return ErrTape
	}
	return nil
}

// atom reports whether buf[off:] is lit followed by a structural byte,
// whitespace, or the end of the input.
func (b *builder) atom(off int, lit string) bool {
	end := off + len(lit)
	return end <= len(b.buf) && string(b.buf[off:end]) == lit && terminates(b.buf, end)
}

func terminates(buf []byte, p int) bool { return p == len(buf) || isStructuralOrSpace[buf[p]] }
```

- [ ] **Step 5: Add string unescaping**

`strings.go`:

```go
package simdjson

import (
	"encoding/binary"
	"unicode/utf8"
)

// str unescapes the string whose opening quote is at buf[off] into the
// string buffer (4-byte little-endian length, bytes, NUL) and appends its
// tape word. Port of src/generic/stage2/stringparsing.h.
func (b *builder) str(off int) error {
	start := len(b.strs)
	b.strs = append(b.strs, 0, 0, 0, 0) // length, patched below
	var ok bool
	if b.strs, ok = appendUnescaped(b.strs, b.buf, off+1); !ok {
		return ErrString
	}
	binary.LittleEndian.PutUint32(b.strs[start:], uint32(len(b.strs)-start-4))
	b.strs = append(b.strs, 0)
	b.tape = append(b.tape, word('"', uint64(start)))
	return nil
}

var escapeMap = [256]byte{
	'"': '"', '\\': '\\', '/': '/',
	'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t',
}

// appendUnescaped appends the unescaped string starting at src[i] (just after
// the opening quote) up to its closing quote. It reports false for an invalid
// escape or a missing closing quote.
func appendUnescaped(dst, src []byte, i int) ([]byte, bool) {
	for {
		n := indexQuoteOrBackslash(src[i:])
		if n < 0 {
			return dst, false
		}
		dst = append(dst, src[i:i+n]...)
		i += n
		if src[i] == '"' {
			return dst, true
		}
		if i+1 >= len(src) {
			return dst, false
		}
		if src[i+1] == 'u' {
			var ok bool
			if dst, i, ok = appendCodePoint(dst, src, i); !ok {
				return dst, false
			}
			continue
		}
		r := escapeMap[src[i+1]]
		if r == 0 {
			return dst, false
		}
		dst = append(dst, r)
		i += 2
	}
}

// indexQuoteOrBackslash returns the index of the first '"' or '\\' in s, or
// -1. It skips 8 bytes at a time with the SWAR has-zero-byte test.
func indexQuoteOrBackslash(s []byte) int {
	const lo, hi = 0x0101010101010101, 0x8080808080808080
	i := 0
	for ; i+8 <= len(s); i += 8 {
		w := binary.LittleEndian.Uint64(s[i:])
		q, bs := w^('"'*lo), w^('\\'*lo)
		if ((q-lo)&^q|(bs-lo)&^bs)&hi != 0 {
			break
		}
	}
	for ; i < len(s); i++ {
		if s[i] == '"' || s[i] == '\\' {
			return i
		}
	}
	return -1
}

// appendCodePoint decodes the \uXXXX escape at src[i] (combining a surrogate
// pair), appends it as UTF-8 and returns the index after the escape. Lone or
// malformed surrogates and bad hex digits are errors, as in C++.
func appendCodePoint(dst, src []byte, i int) ([]byte, int, bool) {
	cp, ok := hex4(src, i+2)
	if !ok {
		return dst, i, false
	}
	i += 6
	switch {
	case cp >= 0xD800 && cp < 0xDC00:
		if i+1 >= len(src) || src[i] != '\\' || src[i+1] != 'u' {
			return dst, i, false
		}
		low, ok := hex4(src, i+2)
		if !ok || low < 0xDC00 || low > 0xDFFF {
			return dst, i, false
		}
		cp = ((cp-0xD800)<<10 | (low - 0xDC00)) + 0x10000
		i += 6
	case cp >= 0xDC00 && cp <= 0xDFFF:
		return dst, i, false
	}
	return utf8.AppendRune(dst, rune(cp)), i, true
}

func hex4(src []byte, i int) (uint32, bool) {
	if i+4 > len(src) {
		return 0, false
	}
	var v uint32
	for _, c := range src[i : i+4] {
		switch {
		case '0' <= c && c <= '9':
			c -= '0'
		case 'a' <= c && c <= 'f':
			c -= 'a' - 10
		case 'A' <= c && c <= 'F':
			c -= 'A' - 10
		default:
			return 0, false
		}
		v = v<<4 | uint32(c)
	}
	return v, true
}
```

- [ ] **Step 6: Add number parsing**

`numbers.go`:

```go
package simdjson

import (
	"encoding/binary"
	"math"
	"strconv"
	"unsafe"
)

func isDigit(c byte) bool { return c-'0' < 10 }

// number parses the number at buf[off] and appends it to the tape. Port of
// parse_number (include/simdjson/generic/numberparsing.h). Floats are
// converted by strconv.ParseFloat (Eisel-Lemire) once the JSON grammar is
// checked; it fails only on overflow to ±Inf, which C++ also rejects.
func (b *builder) number(off int) error {
	buf := b.buf
	p := off
	neg := p < len(buf) && buf[p] == '-' // off == len(buf) past the last structural
	if neg {
		p++
	}
	start := p
	var i uint64
	for p < len(buf) && isDigit(buf[p]) {
		i = 10*i + uint64(buf[p]-'0') // may wrap; the digit count decides below
		p++
	}
	digits := p - start
	if digits == 0 || (buf[start] == '0' && digits > 1) {
		return ErrNumber
	}
	isFloat := false
	if p < len(buf) && buf[p] == '.' {
		isFloat = true
		p++
		frac := p
		for p < len(buf) && isDigit(buf[p]) {
			p++
		}
		if p == frac {
			return ErrNumber
		}
	}
	if p < len(buf) && (buf[p] == 'e' || buf[p] == 'E') {
		isFloat = true
		p++
		if p < len(buf) && (buf[p] == '-' || buf[p] == '+') {
			p++
		}
		exp := p
		for p < len(buf) && isDigit(buf[p]) {
			p++
		}
		if p == exp {
			return ErrNumber
		}
	}
	if isFloat {
		f, err := strconv.ParseFloat(unsafe.String(&buf[off], p-off), 64)
		if err != nil || !terminates(buf, p) {
			return ErrNumber
		}
		b.tape = append(b.tape, word('d', 0), math.Float64bits(f))
		return nil
	}

	// Integers. As in C++, a too-long integer is reported before its terminator is checked.
	longest := 20
	if neg {
		longest = 19
	}
	switch {
	case digits > longest,
		digits == longest && neg && i > math.MaxInt64+1,
		digits == longest && !neg && (buf[start] != '1' || i <= math.MaxInt64): // wrapped
		return b.bigInt(off)
	}
	if !terminates(buf, p) {
		return ErrNumber
	}
	switch {
	case neg:
		b.tape = append(b.tape, word('l', 0), -i) // two's complement; -2^63 included
	case i > math.MaxInt64:
		b.tape = append(b.tape, word('u', 0), i)
	default:
		b.tape = append(b.tape, word('l', 0), i)
	}
	return nil
}

// bigInt handles an integer outside int64/uint64: ErrBigInt, or with
// BigIntAsString its raw digits stored like a string under tag 'Z'.
func (b *builder) bigInt(off int) error {
	if !b.bigIntAsString {
		return ErrBigInt
	}
	p := off
	if b.buf[p] == '-' {
		p++
	}
	for p < len(b.buf) && isDigit(b.buf[p]) {
		p++
	}
	if !terminates(b.buf, p) {
		return ErrNumber
	}
	start := len(b.strs)
	b.strs = binary.LittleEndian.AppendUint32(b.strs, uint32(p-off))
	b.strs = append(b.strs, b.buf[off:p]...)
	b.strs = append(b.strs, 0)
	b.tape = append(b.tape, word('Z', uint64(start)))
	return nil
}
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test ./... && GOEXPERIMENT=simd go test ./... && go vet ./... && GOEXPERIMENT=simd go vet ./... && gofmt -l .`
Expected: `ok` for both packages, twice; nothing else.

- [ ] **Step 8: Commit**

```bash
git add errors.go tape.go parser.go stage2.go strings.go numbers.go tape_test.go parser_test.go numbers_test.go strings_test.go
git commit -m "feat: stage 2 parser writing the simdjson tape

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: DOM API

**Files:**
- Create: `element.go`
- Test: `element_test.go`

**Interfaces:**
- Consumes: `Document`, `Type` constants, `tagFalse`, the `Err*` values (Task 3).
- Produces:
  - `func (d *Document) Root() Element`; `type Element struct{ doc *Document; i int }` with `Type()`, `Array() (Array, error)`, `Object() (Object, error)`, `StringValue() (string, error)`, `StringBytes() ([]byte, error)`, `BigInt() (string, error)`, `Int64()`, `Uint64()`, `Float64()`, `Bool()`, `IsNull() bool`.
  - `type Array struct{ e Element }` with `Len() int`, `All() iter.Seq2[int, Element]`, `At(i int) (Element, error)`.
  - `type Object struct{ e Element }` with `Len() int`, `All() iter.Seq2[string, Element]`, `Get(key string) (Element, error)`.
  - Unexported, used by Tasks 5 and 6: `Element.tag() byte`, `Element.value() uint64`, `Element.next() int`, `Element.rawString() []byte`, `Object.fields() iter.Seq2[[]byte, Element]`.
  - Test helpers used later: `mustParse`, `mustAt`, `checkErr`, `errOf`, `field` (in `element_test.go`).

- [ ] **Step 1: Write the failing test**

`element_test.go`:

```go
package simdjson

import (
	"errors"
	"math"
	"strings"
	"testing"
)

// mustParse parses s with a fresh Parser and returns the root, failing the test on error.
func mustParse(t *testing.T, s string) Element {
	t.Helper()
	var p Parser
	doc, err := p.Parse([]byte(s))
	if err != nil {
		t.Fatalf("Parse(%.80q): %v", s, err)
	}
	return doc.Root()
}

func mustAt(t *testing.T, a Array, i int) Element {
	t.Helper()
	e, err := a.At(i)
	if err != nil {
		t.Fatalf("At(%d): %v", i, err)
	}
	return e
}

// checkErr fails unless err matches want (nil means success).
func checkErr(t *testing.T, name string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) || (want == nil) != (err == nil) {
		t.Errorf("%s: err = %v, want %v", name, err, want)
	}
}

func errOf[T any](_ T, err error) error { return err }

// field returns e[key] for an object e, failing the test otherwise.
func field(t *testing.T, e Element, key string) Element {
	t.Helper()
	o, err := e.Object()
	if err != nil {
		t.Fatal(err)
	}
	v, err := o.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestGetters(t *testing.T) {
	a, _ := mustParse(t, `[-1, 1, 18446744073709551615, 1.5, "s", true, false, null, [], {}]`).Array()
	el := func(i int) Element { return mustAt(t, a, i) }

	types := []Type{TypeInt64, TypeInt64, TypeUint64, TypeFloat64, TypeString, TypeBool, TypeBool, TypeNull, TypeArray, TypeObject}
	for i, typ := range types {
		if got := el(i).Type(); got != typ {
			t.Errorf("element %d: type %v, want %v", i, got, typ)
		}
	}
	// Numeric conversions follow C++ element-inl.h.
	i, err := el(0).Int64()
	checkErr(t, "Int64(-1)", err, nil)
	checkErr(t, "Uint64(-1)", errOf(el(0).Uint64()), ErrNumberOutOfRange)
	u, err := el(1).Uint64()
	checkErr(t, "Uint64(1)", err, nil)
	checkErr(t, "Int64(MaxUint64)", errOf(el(2).Int64()), ErrNumberOutOfRange)
	f0, err := el(0).Float64()
	checkErr(t, "Float64(-1)", err, nil)
	f2, err := el(2).Float64()
	checkErr(t, "Float64(MaxUint64)", err, nil)
	checkErr(t, "Int64(1.5)", errOf(el(3).Int64()), ErrIncorrectType)
	checkErr(t, "Uint64(1.5)", errOf(el(3).Uint64()), ErrIncorrectType)
	if i != -1 || u != 1 || f0 != -1 || f2 != math.MaxUint64 {
		t.Errorf("values: %d %d %v %v", i, u, f0, f2)
	}
	if s, err := el(4).StringValue(); err != nil || s != "s" {
		t.Errorf("StringValue = %q, %v", s, err)
	}
	if b, err := el(4).StringBytes(); err != nil || string(b) != "s" {
		t.Errorf("StringBytes = %q, %v", b, err)
	}
	if b, err := el(5).Bool(); err != nil || !b {
		t.Errorf("Bool(true) = %v, %v", b, err)
	}
	if b, err := el(6).Bool(); err != nil || b {
		t.Errorf("Bool(false) = %v, %v", b, err)
	}
	if !el(7).IsNull() || el(6).IsNull() {
		t.Error("IsNull")
	}
	checkErr(t, "Bool(string)", errOf(el(4).Bool()), ErrIncorrectType)
	checkErr(t, "StringValue(true)", errOf(el(5).StringValue()), ErrIncorrectType)
	checkErr(t, "Array(string)", errOf(el(4).Array()), ErrIncorrectType)
	checkErr(t, "Object(array)", errOf(el(8).Object()), ErrIncorrectType)
	checkErr(t, "BigInt(string)", errOf(el(4).BigInt()), ErrIncorrectType)
}

func TestIntegerGetters(t *testing.T) { // C++ tests/dom/integer_tests.cpp
	tests := []struct {
		in           string
		i64          int64
		i64Err       error
		u64          uint64
		u64Err       error
		wantUnsigned bool
	}{
		{"9223372036854775807", math.MaxInt64, nil, math.MaxInt64, nil, false},
		{"-9223372036854775808", math.MinInt64, nil, 0, ErrNumberOutOfRange, false},
		{"9223372036854775808", 0, ErrNumberOutOfRange, 1 << 63, nil, true},
		{"18446744073709551615", 0, ErrNumberOutOfRange, math.MaxUint64, nil, true},
		{"0", 0, nil, 0, nil, false},
	}
	for _, tt := range tests {
		v := field(t, mustParse(t, `{"key": `+tt.in+`}`), "key")
		i, err := v.Int64()
		checkErr(t, tt.in+" Int64", err, tt.i64Err)
		u, uerr := v.Uint64()
		checkErr(t, tt.in+" Uint64", uerr, tt.u64Err)
		if i != tt.i64 || u != tt.u64 || (v.Type() == TypeUint64) != tt.wantUnsigned {
			t.Errorf("%s: %d %d %v", tt.in, i, u, v.Type())
		}
	}
}

func TestBigIntGetters(t *testing.T) { // C++ big_integer_tests type_checks
	p := Parser{BigIntAsString: true}
	doc, err := p.Parse([]byte(`{"val":123456789012345678901}`))
	if err != nil {
		t.Fatal(err)
	}
	v := field(t, doc.Root(), "val")
	if v.Type() != TypeBigInt {
		t.Fatalf("type %v", v.Type())
	}
	if d, err := v.BigInt(); err != nil || d != "123456789012345678901" {
		t.Errorf("BigInt() = %q, %v", d, err)
	}
	for name, err := range map[string]error{
		"Int64": errOf(v.Int64()), "Uint64": errOf(v.Uint64()), "Float64": errOf(v.Float64()),
		"StringValue": errOf(v.StringValue()), "Bool": errOf(v.Bool()),
	} {
		checkErr(t, name, err, ErrIncorrectType)
	}
}

func TestArray(t *testing.T) {
	a, _ := mustParse(t, `[1,[2,3],{"a":[4]},"x",5.5]`).Array()
	if a.Len() != 5 {
		t.Errorf("Len = %d", a.Len())
	}
	var types []Type
	for i, v := range a.All() {
		if i == 4 {
			break // stopping early must end the iteration
		}
		types = append(types, v.Type())
	}
	if len(types) != 4 || types[1] != TypeArray || types[2] != TypeObject || types[3] != TypeString {
		t.Errorf("All types = %v", types)
	}
	if v := mustAt(t, a, 4); v.Type() != TypeFloat64 {
		t.Errorf("At(4) type %v", v.Type())
	}
	for _, i := range []int{-1, 5} {
		checkErr(t, "At out of range", errOf(a.At(i)), ErrIndexOutOfBounds)
	}
	empty, _ := mustParse(t, `[]`).Array()
	if empty.Len() != 0 {
		t.Error("empty Len")
	}
	for range empty.All() {
		t.Error("empty array yielded an element")
	}
}

func TestObject(t *testing.T) {
	o, _ := mustParse(t, `{"1":1,"2":{"x":[]},"3":1,"1":"dup","k\u00e9y":true}`).Object()
	if o.Len() != 5 {
		t.Errorf("Len = %d", o.Len())
	}
	var keys []string
	for k := range o.All() {
		keys = append(keys, k)
	}
	if strings.Join(keys, ",") != "1,2,3,1,kéy" {
		t.Errorf("keys = %q", keys)
	}
	if v, err := o.Get("1"); err != nil || v.Type() != TypeInt64 { // the first duplicate wins, as in C++
		t.Errorf("Get(1) = %v, %v", v.Type(), err)
	}
	if v, err := o.Get("kéy"); err != nil || v.Type() != TypeBool { // keys compare unescaped
		t.Errorf("Get(kéy) = %v, %v", v.Type(), err)
	}
	checkErr(t, "Get(nope)", errOf(o.Get("nope")), ErrNoSuchField)
}

func TestZeroElement(t *testing.T) {
	// The zero values returned next to an error must not panic when used.
	o, _ := mustParse(t, `{}`).Object()
	v, err := o.Get("missing")
	checkErr(t, "Get", err, ErrNoSuchField)
	if v.Type() != 0 || v.IsNull() {
		t.Errorf("zero Element: type %v, IsNull %v", v.Type(), v.IsNull())
	}
	checkErr(t, "Int64", errOf(v.Int64()), ErrIncorrectType)
	checkErr(t, "StringValue", errOf(v.StringValue()), ErrIncorrectType)
	var a Array
	if a.Len() != 0 {
		t.Error("zero Array Len")
	}
	for range a.All() {
		t.Error("zero Array yielded an element")
	}
	var obj Object
	checkErr(t, "zero Object Get", errOf(obj.Get("x")), ErrNoSuchField)
}

func TestCountSaturation(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates ~34 MB")
	}
	const n = 0xFFFFFF + 2 // more elements than the 24-bit tape count holds
	a, _ := mustParse(t, "["+strings.Repeat("0,", n-1)+"0]").Array()
	if a.Len() != n {
		t.Errorf("array Len = %d, want %d", a.Len(), n)
	}
	o, _ := mustParse(t, "{"+strings.Repeat(`"":0,`, n-1)+`"":0}`).Object()
	if o.Len() != n {
		t.Errorf("object Len = %d, want %d", o.Len(), n)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test .`
Expected: build failure, `doc.Root undefined`, `undefined: Array`, `undefined: Object`, and so on.

- [ ] **Step 3: Implement the DOM**

`element.go`:

```go
package simdjson

import (
	"encoding/binary"
	"iter"
	"math"
)

// Root returns the document's top-level value.
func (d *Document) Root() Element { return Element{doc: d, i: 1} }

// Element is a JSON value inside a Document. It is a small value type,
// valid until the next Parse on the Document's Parser.
type Element struct {
	doc *Document
	i   int // tape index
}

// tag returns the tape tag of e, or 0 for the zero Element (returned with errors).
func (e Element) tag() byte {
	if e.doc == nil {
		return 0
	}
	return byte(e.doc.tape[e.i] >> 56)
}

func (e Element) value() uint64 { return e.doc.tape[e.i+1] } // second word of a number

// next returns the tape index just past e.
func (e Element) next() int {
	switch e.tag() {
	case '[', '{':
		return int(uint32(e.doc.tape[e.i])) // opening word points past the closing one
	case 'l', 'u', 'd':
		return e.i + 2
	}
	return e.i + 1
}

// rawString returns the bytes of a '"' or 'Z' element in the string buffer.
func (e Element) rawString() []byte {
	off := e.doc.tape[e.i] & (1<<56 - 1)
	n := uint64(binary.LittleEndian.Uint32(e.doc.strings[off:]))
	return e.doc.strings[off+4 : off+4+n : off+4+n]
}

// Type returns the type of e.
func (e Element) Type() Type {
	if t := e.tag(); t != tagFalse {
		return Type(t)
	}
	return TypeBool
}

func (e Element) Array() (Array, error) {
	if e.tag() != '[' {
		return Array{}, ErrIncorrectType
	}
	return Array{e}, nil
}

func (e Element) Object() (Object, error) {
	if e.tag() != '{' {
		return Object{}, ErrIncorrectType
	}
	return Object{e}, nil
}

// StringValue returns a copy of a string value.
func (e Element) StringValue() (string, error) {
	b, err := e.StringBytes()
	return string(b), err
}

// StringBytes returns a string value without copying. The bytes are valid
// until the next Parse and must not be modified.
func (e Element) StringBytes() ([]byte, error) {
	if e.tag() != '"' {
		return nil, ErrIncorrectType
	}
	return e.rawString(), nil
}

// BigInt returns the raw digits of a TypeBigInt value (see Parser.BigIntAsString).
func (e Element) BigInt() (string, error) {
	if e.tag() != 'Z' {
		return "", ErrIncorrectType
	}
	return string(e.rawString()), nil
}

func (e Element) Int64() (int64, error) {
	switch e.tag() {
	case 'l':
		return int64(e.value()), nil
	case 'u':
		if v := e.value(); v <= math.MaxInt64 {
			return int64(v), nil
		}
		return 0, ErrNumberOutOfRange
	}
	return 0, ErrIncorrectType
}

func (e Element) Uint64() (uint64, error) {
	switch e.tag() {
	case 'u':
		return e.value(), nil
	case 'l':
		if v := int64(e.value()); v >= 0 {
			return uint64(v), nil
		}
		return 0, ErrNumberOutOfRange
	}
	return 0, ErrIncorrectType
}

// Float64 returns a number as float64; integers are converted.
func (e Element) Float64() (float64, error) {
	switch e.tag() {
	case 'd':
		return math.Float64frombits(e.value()), nil
	case 'l':
		return float64(int64(e.value())), nil
	case 'u':
		return float64(e.value()), nil
	}
	return 0, ErrIncorrectType
}

func (e Element) Bool() (bool, error) {
	switch e.tag() {
	case 't':
		return true, nil
	case tagFalse:
		return false, nil
	}
	return false, ErrIncorrectType
}

func (e Element) IsNull() bool { return e.tag() == 'n' }

// count returns the element count stored in an opening word, saturated at 2^24-1.
func (e Element) count() int {
	if e.doc == nil {
		return 0
	}
	return int(e.doc.tape[e.i] >> 32 & 0xFFFFFF)
}

// Array is a JSON array.
type Array struct{ e Element }

// Len returns the number of elements.
func (a Array) Len() int {
	if n := a.e.count(); n < 0xFFFFFF {
		return n
	}
	n := 0
	for range a.All() {
		n++
	}
	return n
}

// All iterates over the elements and their indices.
func (a Array) All() iter.Seq2[int, Element] {
	return func(yield func(int, Element) bool) {
		end := a.e.next() - 1 // the closing ']'
		for i, k := a.e.i+1, 0; i < end; k++ {
			v := Element{a.e.doc, i}
			if !yield(k, v) {
				return
			}
			i = v.next()
		}
	}
}

// At returns element i, walking the tape (O(i)).
func (a Array) At(i int) (Element, error) {
	for k, v := range a.All() {
		if k == i {
			return v, nil
		}
	}
	return Element{}, ErrIndexOutOfBounds
}

// Object is a JSON object.
type Object struct{ e Element }

// Len returns the number of fields.
func (o Object) Len() int {
	if n := o.e.count(); n < 0xFFFFFF {
		return n
	}
	n := 0
	for range o.fields() {
		n++
	}
	return n
}

// fields iterates over the unescaped keys (not copied) and values.
func (o Object) fields() iter.Seq2[[]byte, Element] {
	return func(yield func([]byte, Element) bool) {
		end := o.e.next() - 1 // the closing '}'
		for i := o.e.i + 1; i < end; {
			k, v := Element{o.e.doc, i}, Element{o.e.doc, i + 1}
			if !yield(k.rawString(), v) {
				return
			}
			i = v.next()
		}
	}
}

// All iterates over the fields in document order. Keys are copies.
func (o Object) All() iter.Seq2[string, Element] {
	return func(yield func(string, Element) bool) {
		for k, v := range o.fields() {
			if !yield(string(k), v) {
				return
			}
		}
	}
}

// Get returns the value of the first field whose unescaped key equals key.
func (o Object) Get(key string) (Element, error) {
	for k, v := range o.fields() {
		if string(k) == key {
			return v, nil
		}
	}
	return Element{}, ErrNoSuchField
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./... && GOEXPERIMENT=simd go test ./... && go vet ./... && gofmt -l .`
Expected: `ok` for both packages, twice. `TestCountSaturation` allocates about 34 MB; `go test -short` skips it.

- [ ] **Step 5: Commit**

```bash
git add element.go element_test.go
git commit -m "feat: DOM API (Element, Array, Object)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Serialization and Minify

**Files:**
- Create: `serialize.go`
- Test: `serialize_test.go`

**Interfaces:**
- Consumes: `Array.All`, `Object.fields`, `Element.rawString`, `Element.value` (Task 4); `stage1.Minify` (Task 2).
- Produces: `func (e Element) AppendJSON(dst []byte) []byte`; `func (e Element) MarshalJSON() ([]byte, error)`; `func Minify(dst, src []byte) ([]byte, error)`.

- [ ] **Step 1: Write the failing test**

`serialize_test.go`:

```go
package simdjson

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestAppendJSON(t *testing.T) {
	tests := []struct{ in, want string }{
		{` [ 1 , -2 , 18446744073709551615 ] `, `[1,-2,18446744073709551615]`},
		{`{"a" : "x\"y\\z\/\u0001\u001f\b\f\n\r\t" }`, `{"a":"x\"y\\z/\u0001\u001f\b\f\n\r\t"}`},
		{`[1.0, 0.1, -0.0, 1e21, 1e-7, 1E+2, 2.5]`, `[1.0,0.1,-0.0,1e+21,1e-07,100.0,2.5]`},
		{`[true,false,null,{},[]]`, `[true,false,null,{},[]]`},
		{`"\u00e9\ud83d\ude00"`, `"é😀"`},
		// C++ document_tests stable_test: minified input round-trips exactly.
		{`{"Image":{"Width":800,"Height":600,"Title":"View from 15th Floor","Thumbnail":{"Url":"http://www.example.com/image/481989943","Height":125,"Width":100},"Animated":false,"IDs":[116,943.3,234,38793]}}`,
			`{"Image":{"Width":800,"Height":600,"Title":"View from 15th Floor","Thumbnail":{"Url":"http://www.example.com/image/481989943","Height":125,"Width":100},"Animated":false,"IDs":[116,943.3,234,38793]}}`},
	}
	for _, tt := range tests {
		if got := string(mustParse(t, tt.in).AppendJSON(nil)); got != tt.want {
			t.Errorf("AppendJSON(%s)\n got %s\nwant %s", tt.in, got, tt.want)
		}
		// Round trip: the output parses to the same output (types included).
		if again := string(mustParse(t, tt.want).AppendJSON(nil)); again != tt.want {
			t.Errorf("round trip of %s gave %s", tt.want, again)
		}
	}
}

func TestMarshalJSON(t *testing.T) {
	e := field(t, mustParse(t, `{"a":[1,2]}`), "a")
	out, err := json.Marshal(map[string]any{"v": e})
	if err != nil || string(out) != `{"v":[1,2]}` {
		t.Errorf("json.Marshal = %s, %v", out, err)
	}
}

func TestMinify(t *testing.T) {
	got, err := Minify([]byte("x:"), []byte(" { \"a b\" : [ 1 , 2 ] } \n"))
	if err != nil || string(got) != `x:{"a b":[1,2]}` {
		t.Errorf("Minify = %q, %v", got, err)
	}
	if _, err := Minify(nil, []byte(`["abc`)); !errors.Is(err, ErrUnclosedString) {
		t.Errorf("Minify unclosed: err = %v", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test .`
Expected: build failure, `AppendJSON undefined`, `undefined: Minify`.

- [ ] **Step 3: Implement serialization**

`serialize.go`:

```go
package simdjson

import (
	"bytes"
	"math"
	"strconv"

	"simdjson-go/internal/stage1"
)

// AppendJSON appends e as minified JSON to dst. Parsing the output yields an
// equal tree, including element types.
func (e Element) AppendJSON(dst []byte) []byte {
	switch e.tag() {
	case '[':
		dst = append(dst, '[')
		for k, v := range (Array{e}).All() {
			if k > 0 {
				dst = append(dst, ',')
			}
			dst = v.AppendJSON(dst)
		}
		return append(dst, ']')
	case '{':
		dst = append(dst, '{')
		first := true
		for k, v := range (Object{e}).fields() {
			if !first {
				dst = append(dst, ',')
			}
			first = false
			dst = append(appendQuoted(dst, k), ':')
			dst = v.AppendJSON(dst)
		}
		return append(dst, '}')
	case '"':
		return appendQuoted(dst, e.rawString())
	case 'Z':
		return append(dst, e.rawString()...)
	case 'l':
		return strconv.AppendInt(dst, int64(e.value()), 10)
	case 'u':
		return strconv.AppendUint(dst, e.value(), 10)
	case 'd':
		start := len(dst)
		dst = strconv.AppendFloat(dst, math.Float64frombits(e.value()), 'g', -1, 64)
		if !bytes.ContainsAny(dst[start:], ".e") {
			dst = append(dst, ".0"...) // keep it a float when re-parsed
		}
		return dst
	case 't':
		return append(dst, "true"...)
	case tagFalse:
		return append(dst, "false"...)
	}
	return append(dst, "null"...)
}

// MarshalJSON implements json.Marshaler.
func (e Element) MarshalJSON() ([]byte, error) { return e.AppendJSON(nil), nil }

// appendQuoted appends s as a JSON string, escaping '"', '\\' and bytes below 0x20.
func appendQuoted(dst, s []byte) []byte {
	const hex = "0123456789abcdef"
	dst = append(dst, '"')
	start := 0
	for i, c := range s {
		if c >= 0x20 && c != '"' && c != '\\' {
			continue
		}
		dst = append(dst, s[start:i]...)
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
			dst = append(dst, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xF])
		}
		start = i + 1
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"')
}

// Minify appends src to dst with all whitespace outside strings removed.
// Like C++ simdjson::minify it does not validate the document; it returns
// ErrUnclosedString for an unterminated string.
func Minify(dst, src []byte) ([]byte, error) { return stage1.Minify(dst, src) }
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./... && GOEXPERIMENT=simd go test ./... && go vet ./... && gofmt -l .`
Expected: `ok` for both packages, twice.

- [ ] **Step 5: Commit**

```bash
git add serialize.go serialize_test.go
git commit -m "feat: AppendJSON, MarshalJSON and Minify

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: JSON Pointer

Port of the DOM `at_pointer` methods (`include/simdjson/dom/element-inl.h`, `array-inl.h`, `object-inl.h`) and `parse_json_pointer_array_index` (`jsonpathutil.h`), including their error codes. The cases come from `tests/dom/pointercheck.cpp`.

**Files:**
- Create: `pointer.go`
- Test: `pointer_test.go`

**Interfaces:**
- Consumes: `Element.tag`, `Object.Get`, `Array.At` (Task 4); `AppendJSON` (Task 5, used in tests).
- Produces: `func (e Element) AtPointer(ptr string) (Element, error)`.

- [ ] **Step 1: Write the failing test**

`pointer_test.go`:

```go
package simdjson

import (
	"errors"
	"testing"
)

// Fixtures and cases from C++ tests/dom/pointercheck.cpp.
const pointerJSON = `{"/~01abc":[0,{"\\\" 0":["value0","value1"]}],"0":"0 ok","01":"01 ok","":"empty ok","arr":[]}`

const pointerRFCJSON = `{"foo":["bar","baz"],"":0,"a/b":1,"c%d":2,"e^f":3,"g|h":4,"i\\j":5,"k\"l":6," ":7,"m~n":8}`

func TestAtPointer(t *testing.T) {
	tests := []struct {
		doc, ptr, want string
		err            error
	}{
		{pointerRFCJSON, "", pointerRFCJSON, nil},
		{pointerRFCJSON, "/foo", `["bar","baz"]`, nil},
		{pointerRFCJSON, "/foo/0", `"bar"`, nil},
		{pointerRFCJSON, "/", "0", nil},
		{pointerRFCJSON, "/a~1b", "1", nil},
		{pointerRFCJSON, "/c%d", "2", nil},
		{pointerRFCJSON, "/e^f", "3", nil},
		{pointerRFCJSON, "/g|h", "4", nil},
		{pointerRFCJSON, `/i\j`, "5", nil},
		{pointerRFCJSON, `/k"l`, "6", nil},
		{pointerRFCJSON, "/ ", "7", nil},
		{pointerRFCJSON, "/m~0n", "8", nil},
		{pointerJSON, "/~1~001abc", `[0,{"\\\" 0":["value0","value1"]}]`, nil},
		{pointerJSON, "/~1~001abc/1", `{"\\\" 0":["value0","value1"]}`, nil},
		{pointerJSON, `/~1~001abc/1/\" 0`, `["value0","value1"]`, nil},
		{pointerJSON, `/~1~001abc/1/\" 0/0`, `"value0"`, nil},
		{pointerJSON, `/~1~001abc/1/\" 0/1`, `"value1"`, nil},
		{pointerJSON, `/~1~001abc/1/\" 0/2`, "", ErrIndexOutOfBounds},
		{pointerJSON, "/arr", "[]", nil},
		{pointerJSON, "/arr/0", "", ErrIndexOutOfBounds},
		{pointerJSON, "~1~001abc", "", ErrInvalidJSONPointer},
		{pointerJSON, "/0", `"0 ok"`, nil},
		{pointerJSON, "/01", `"01 ok"`, nil},
		{pointerJSON, "/~01abc", "", ErrNoSuchField},
		{pointerJSON, "/~1~001abc/01", "", ErrInvalidJSONPointer},
		{pointerJSON, "/~1~001abc/", "", ErrInvalidJSONPointer},
		{pointerJSON, "/~1~001abc/18446744073709551616", "", ErrIndexOutOfBounds},
		{pointerJSON, "/~1~001abc/-", "", ErrIndexOutOfBounds},
		{`{"key":"value","array":[0,1,2]}`, "/array/not_a_num", "", ErrIncorrectType},
		{`{"key":"value","array":[0,1,2]}`, "/array/9", "", ErrIndexOutOfBounds},
		{`{"key":"value","array":[0,1,2]}`, "/no_such_key", "", ErrNoSuchField},
		// issue 2154: descending into a scalar
		{`{"obj":{"s":"42","n":42,"f":4.2}}`, "/obj/X/42", "", ErrNoSuchField},
		{`{"obj":{"s":"42","n":42,"f":4.2}}`, "/obj/s/42", "", ErrNoSuchField},
		{`{"obj":{"s":"42","n":42,"f":4.2}}`, "/obj/f/4~", "", ErrInvalidJSONPointer},
		{`{"obj":{"s":"42","n":42,"f":4.2}}`, "/obj/f/~", "", ErrInvalidJSONPointer},
		{`{"obj":{"s":"42","n":42,"f":4.2}}`, "/obj/f/~1", "", ErrNoSuchField},
		{`"just a string"`, "", `"just a string"`, nil},
	}
	for _, tt := range tests {
		e, err := mustParse(t, tt.doc).AtPointer(tt.ptr)
		if !errors.Is(err, tt.err) || (tt.err == nil) != (err == nil) {
			t.Errorf("%q: err = %v, want %v", tt.ptr, err, tt.err)
			continue
		}
		if err == nil {
			if got := string(e.AppendJSON(nil)); got != tt.want {
				t.Errorf("%q = %s, want %s", tt.ptr, got, tt.want)
			}
		}
	}
	// Pointers compose: at_pointer("/array").at_pointer("/0") (C++ modern_support).
	arr, _ := mustParse(t, `{"key":"value","array":[0,1,2]}`).AtPointer("/array")
	if v, err := arr.AtPointer("/0"); err != nil || string(v.AppendJSON(nil)) != "0" {
		t.Errorf("/array then /0 = %v", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test .`
Expected: build failure, `AtPointer undefined`.

- [ ] **Step 3: Implement AtPointer**

`pointer.go`:

```go
package simdjson

import (
	"math"
	"strings"
)

// AtPointer returns the value at the RFC 6901 JSON Pointer ptr, relative to
// e. Port of the C++ DOM at_pointer methods, including their error codes.
func (e Element) AtPointer(ptr string) (Element, error) {
	switch e.tag() {
	case '{':
		return Object{e}.atPointer(ptr)
	case '[':
		return Array{e}.atPointer(ptr)
	}
	switch {
	case ptr == "":
		return e, nil
	case pointerWellFormed(ptr): // descending into a scalar (simdjson issue 2154)
		return Element{}, ErrNoSuchField
	}
	return Element{}, ErrInvalidJSONPointer
}

func (o Object) atPointer(ptr string) (Element, error) {
	if ptr == "" {
		return o.e, nil
	}
	if ptr[0] != '/' {
		return Element{}, ErrInvalidJSONPointer
	}
	rest := ptr[1:]
	key := rest
	slash := strings.IndexByte(rest, '/')
	if slash >= 0 {
		key = rest[:slash]
	}
	if strings.IndexByte(key, '~') >= 0 {
		var ok bool
		if key, ok = unescapePointerToken(key); !ok {
			return Element{}, ErrInvalidJSONPointer
		}
	}
	child, err := o.Get(key)
	if err != nil || slash < 0 {
		return child, err
	}
	return child.AtPointer(rest[slash:])
}

func (a Array) atPointer(ptr string) (Element, error) {
	if ptr == "" {
		return a.e, nil
	}
	if ptr[0] != '/' {
		return Element{}, ErrInvalidJSONPointer
	}
	ptr = ptr[1:]
	if ptr == "-" { // the position after the last element
		return Element{}, ErrIndexOutOfBounds
	}
	// parse_json_pointer_array_index
	var index uint64
	n := 0
	for ; n < len(ptr) && ptr[n] != '/'; n++ {
		d := ptr[n] - '0'
		if d > 9 {
			return Element{}, ErrIncorrectType
		}
		if n > 0 && ptr[0] == '0' {
			return Element{}, ErrInvalidJSONPointer // leading zero
		}
		if index > (math.MaxUint64-uint64(d))/10 {
			return Element{}, ErrIndexOutOfBounds
		}
		index = index*10 + uint64(d)
	}
	if n == 0 {
		return Element{}, ErrInvalidJSONPointer
	}
	if index > math.MaxInt {
		return Element{}, ErrIndexOutOfBounds
	}
	child, err := a.At(int(index))
	if err != nil || n == len(ptr) {
		return child, err
	}
	return child.AtPointer(ptr[n:])
}

// unescapePointerToken replaces ~0 with ~ and ~1 with /. Any other use of ~ is invalid.
func unescapePointerToken(s string) (string, bool) {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '~' {
			b = append(b, s[i])
			continue
		}
		if i+1 == len(s) {
			return "", false
		}
		switch s[i+1] {
		case '0':
			b = append(b, '~')
		case '1':
			b = append(b, '/')
		default:
			return "", false
		}
		i++
	}
	return string(b), true
}

// pointerWellFormed is C++ is_pointer_well_formed: a leading '/' and a valid
// first ~ escape. ptr must be non-empty.
func pointerWellFormed(ptr string) bool {
	if ptr[0] != '/' {
		return false
	}
	i := strings.IndexByte(ptr, '~')
	return i < 0 || i+1 < len(ptr) && (ptr[i+1] == '0' || ptr[i+1] == '1')
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./... && GOEXPERIMENT=simd go test ./... && go vet ./... && gofmt -l .`
Expected: `ok` for both packages, twice.

- [ ] **Step 5: Commit**

```bash
git add pointer.go pointer_test.go
git commit -m "feat: JSON Pointer (RFC 6901) with simdjson error codes

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Corpus tests

Ports `jsoncheck`, `minefieldcheck`, `numberparsingcheck`, `stringparsingcheck` and `minify_tests` onto the pinned `simdjson-data` corpora (34 MB unpacked, so they are downloaded rather than committed).

**Files:**
- Create: `scripts/fetch-testdata.sh`, `corpus_test.go`
- Modify: `.gitignore` (currently `testdata/`) so that fuzz regressions under `testdata/fuzz/` can be committed

**Interfaces:**
- Consumes: `Parser`, `bom` (Task 3); `Element` getters, `Object.All`, `Array.All` (Task 4); `AppendJSON`, `Minify` (Task 5).
- Produces (test helpers used by Task 8): `func testdataDir(t testing.TB, sub string) string`, `func sameAsStdlib(e Element, data []byte) string`, `func sameTree(e Element, v any) string`.

- [ ] **Step 1: Narrow `.gitignore` to the downloaded corpora**

Replace the content of `.gitignore` with:

```text
testdata/jsonchecker/
testdata/jsonexamples/
```

- [ ] **Step 2: Write the corpus tests**

`corpus_test.go`:

```go
package simdjson

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// testdataDir returns testdata/<sub>, failing (never skipping) when the
// corpora have not been fetched.
func testdataDir(t testing.TB, sub string) string {
	t.Helper()
	dir := filepath.Join("testdata", sub)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("%s missing: run scripts/fetch-testdata.sh", dir)
	}
	return dir
}

// C++ tests/dom/jsoncheck.cpp
func TestJSONChecker(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(testdataDir(t, "jsonchecker"), "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no files")
	}
	var p Parser
	for _, f := range files {
		name := filepath.Base(f)
		if strings.Contains(name, "EXCLUDE") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.Parse(data)
		switch {
		case strings.HasPrefix(name, "pass") && err != nil:
			t.Errorf("%s: %v", name, err)
		case strings.HasPrefix(name, "fail") && err == nil:
			t.Errorf("%s: parsed, want an error", name)
		}
	}
}

// C++ tests/dom/minefieldcheck.cpp (JSONTestSuite): y_ must parse, n_ must fail, i_ is ignored.
func TestMinefield(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(testdataDir(t, "jsonchecker/minefield"), "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no files")
	}
	var p Parser
	for _, f := range files {
		name := filepath.Base(f)
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.Parse(data)
		switch {
		case strings.HasPrefix(name, "y_") && err != nil:
			t.Errorf("%s: %v", name, err)
		case strings.HasPrefix(name, "n_") && err == nil:
			t.Errorf("%s: parsed, want an error", name)
		}
	}
}

// TestExamples parses every jsonexamples file and checks it against
// encoding/json, the AppendJSON round trip and the Minify round trip
// (C++ numberparsingcheck, stringparsingcheck and minify_tests).
func TestExamples(t *testing.T) {
	var files []string
	if err := filepath.WalkDir(testdataDir(t, "jsonexamples"), func(path string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(path, ".json") {
			files = append(files, path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no files")
	}
	var p, q Parser
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := p.Parse(data)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if diff := sameAsStdlib(doc.Root(), data); diff != "" {
			t.Errorf("%s: differs from encoding/json: %s", f, diff)
		}
		out := doc.Root().AppendJSON(nil)
		if again, err := q.Parse(out); err != nil || !bytes.Equal(again.Root().AppendJSON(nil), out) {
			t.Errorf("%s: AppendJSON does not round-trip (%v)", f, err)
		}
		min, err := Minify(nil, data)
		if err != nil {
			t.Errorf("%s: Minify: %v", f, err)
			continue
		}
		if m, err := q.Parse(min); err != nil || !bytes.Equal(m.Root().AppendJSON(nil), out) {
			t.Errorf("%s: Minify changed the document (%v)", f, err)
		}
	}
}

// sameAsStdlib decodes data with encoding/json (UseNumber) and returns a
// description of the first difference from e, or "".
func sameAsStdlib(e Element, data []byte) string {
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, bom)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "encoding/json: " + err.Error()
	}
	return sameTree(e, v)
}

func sameTree(e Element, v any) string {
	switch v := v.(type) {
	case map[string]any:
		o, err := e.Object()
		if err != nil {
			return "want object, have " + e.Type().String()
		}
		last := map[string]Element{} // encoding/json keeps the last duplicate key
		for k, ev := range o.All() {
			last[k] = ev
		}
		if len(last) != len(v) {
			return "field count"
		}
		for k, ev := range last {
			if d := sameTree(ev, v[k]); d != "" {
				return strconv.Quote(k) + ": " + d
			}
		}
	case []any:
		a, err := e.Array()
		if err != nil {
			return "want array, have " + e.Type().String()
		}
		if a.Len() != len(v) {
			return "array length"
		}
		for i, ev := range a.All() {
			if d := sameTree(ev, v[i]); d != "" {
				return strconv.Itoa(i) + ": " + d
			}
		}
	case string:
		if s, err := e.StringValue(); err != nil || s != v {
			return "string " + strconv.Quote(v)
		}
	case bool:
		if b, err := e.Bool(); err != nil || b != v {
			return "bool"
		}
	case nil:
		if !e.IsNull() {
			return "want null"
		}
	case json.Number:
		var ok bool
		switch e.Type() {
		case TypeInt64:
			got, _ := e.Int64()
			want, err := strconv.ParseInt(string(v), 10, 64)
			ok = err == nil && got == want
		case TypeUint64:
			got, _ := e.Uint64()
			want, err := strconv.ParseUint(string(v), 10, 64)
			ok = err == nil && got == want
		case TypeFloat64:
			got, _ := e.Float64()
			want, err := strconv.ParseFloat(string(v), 64)
			ok = err == nil && math.Float64bits(got) == math.Float64bits(want)
		}
		if !ok {
			return "number " + string(v) + " parsed as " + string(e.AppendJSON(nil))
		}
	}
	return ""
}
```

- [ ] **Step 3: Run them before the data exists to verify they fail loudly**

Run: `go test -run 'TestJSONChecker|TestMinefield|TestExamples' .`
Expected: FAIL with `testdata/jsonchecker missing: run scripts/fetch-testdata.sh` (and the same for the other two).

- [ ] **Step 4: Add the fetch script and download the corpora**

`scripts/fetch-testdata.sh`:

```sh
#!/bin/sh
# Downloads the simdjson test corpora into testdata/ (gitignored), pinned to
# the simdjson-data commit used by C++ simdjson's dependencies/CMakeLists.txt.
set -eu
commit=351949906abde446f0314bf79606fb5d884f5be7
cd "$(dirname "$0")/.."
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/data.zip" "https://github.com/simdjson/simdjson-data/archive/$commit.zip"
unzip -q "$tmp/data.zip" -d "$tmp"
rm -rf testdata/jsonchecker testdata/jsonexamples
mkdir -p testdata
mv "$tmp/simdjson-data-$commit/jsonchecker" "$tmp/simdjson-data-$commit/jsonexamples" testdata/
echo "testdata/ ready"
```

Run: `chmod +x scripts/fetch-testdata.sh && ./scripts/fetch-testdata.sh && ls testdata`
Expected: `testdata/ ready`, then `jsonchecker  jsonexamples`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./... && GOEXPERIMENT=simd go test ./... && go vet ./... && gofmt -l . && git status --short`
Expected: `ok` for both packages, twice. `git status` lists only `.gitignore`, `corpus_test.go` and `scripts/` (the corpora stay ignored).

- [ ] **Step 6: Commit**

```bash
git add .gitignore corpus_test.go scripts/fetch-testdata.sh
git commit -m "test: corpus tests on the pinned simdjson-data corpora

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: Fuzz tests

The oracle is `encoding/json` adjusted for the documented differences: a leading BOM is stripped, UTF-8 is validated, only 1023 non-empty containers may nest, numbers must fit, and `\u` surrogates must be valid.

**Files:**
- Create: `fuzz_test.go`

**Interfaces:**
- Consumes: `sameAsStdlib` (Task 7), `bom`, `Parser` (Task 3), `AppendJSON`, `Minify` (Task 5).
- Produces: `FuzzParse`, `FuzzMinify`.

- [ ] **Step 1: Write the fuzz tests**

`fuzz_test.go`:

```go
package simdjson

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

var fuzzSeeds = []string{
	`{"a":[1,2.5,"x",true,null,{}]}`, `[]`, `"\u00e9\ud83d\ude00"`, `-0`, `1e400`, "\xEF\xBB\xBF{}",
	`[1,]`, `{"a":1,"a":2}`, `{"a":1e400,"a":1}`, `"\ud800"`, `18446744073709551616`, `[[[[1]]]]`, "\"\x01\"",
}

// FuzzParse checks Parse against an oracle built from encoding/json: a document
// is accepted exactly when encoding/json accepts it, it is valid UTF-8, it nests
// at most 1023 non-empty containers, its numbers fit int64/uint64/finite
// float64, and its \u escapes form valid surrogate pairs. Accepted documents must
// decode to the same tree and round-trip through AppendJSON.
func FuzzParse(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		var p Parser
		doc, err := p.Parse(in)
		if want := oracleAccepts(in); want != (err == nil) {
			t.Fatalf("Parse(%q): err = %v, oracle accepts = %v", in, err, want)
		}
		if err != nil {
			return
		}
		if diff := sameAsStdlib(doc.Root(), in); diff != "" {
			t.Fatalf("Parse(%q) differs from encoding/json: %s", in, diff)
		}
		out := doc.Root().AppendJSON(nil)
		var q Parser
		again, err := q.Parse(out)
		if err != nil || !bytes.Equal(again.Root().AppendJSON(nil), out) {
			t.Fatalf("AppendJSON(%q) = %q does not round-trip (%v)", in, out, err)
		}
		if diff := sameAsStdlib(again.Root(), in); diff != "" {
			t.Fatalf("AppendJSON(%q) = %q changed the value: %s", in, out, diff)
		}
	})
}

// FuzzMinify checks that minifying an accepted document does not change it.
func FuzzMinify(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		var p, q Parser
		doc, err := p.Parse(in)
		if err != nil {
			return
		}
		min, err := Minify(nil, in)
		if err != nil {
			t.Fatalf("Minify(%q): %v", in, err)
		}
		m, err := q.Parse(min)
		if err != nil || !bytes.Equal(m.Root().AppendJSON(nil), doc.Root().AppendJSON(nil)) {
			t.Fatalf("Minify(%q) = %q changed the document (%v)", in, min, err)
		}
	})
}

func oracleAccepts(in []byte) bool {
	in = bytes.TrimPrefix(in, bom)
	if !json.Valid(in) || !utf8.Valid(in) || !validSurrogates(in) {
		return false
	}
	// Walk the tokens rather than a decoded tree: a map keeps only the last of
	// duplicate keys, which would hide the depth and numbers of the others.
	dec := json.NewDecoder(bytes.NewReader(in))
	dec.UseNumber()
	var open []bool // per open array/object: whether it is non-empty
	depth := 0      // open non-empty containers (empty ones do not open a scope in simdjson)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return true
		}
		if err != nil {
			return false
		}
		switch tok := tok.(type) {
		case json.Delim:
			if tok == '[' || tok == '{' {
				nonEmpty := dec.More()
				open = append(open, nonEmpty)
				if nonEmpty {
					if depth++; depth > 1023 {
						return false
					}
				}
				continue
			}
			if open[len(open)-1] {
				depth--
			}
			open = open[:len(open)-1]
		case json.Number:
			if !numberFits(string(tok)) {
				return false
			}
		}
	}
}

// numberFits reports whether simdjson accepts the number: integers must fit
// int64 or uint64, floats must not overflow to ±Inf.
func numberFits(s string) bool {
	if strings.ContainsAny(s, ".eE") {
		_, err := strconv.ParseFloat(s, 64)
		return err == nil
	}
	_, errI := strconv.ParseInt(s, 10, 64)
	_, errU := strconv.ParseUint(s, 10, 64)
	return errI == nil || errU == nil
}

// validSurrogates reports whether every \u escape in the (syntactically valid)
// document is a BMP code point or a high surrogate followed by a low one.
// encoding/json silently replaces bad surrogates with U+FFFD; simdjson rejects them.
func validSurrogates(in []byte) bool {
	inString := false
	for i := 0; i < len(in); i++ {
		switch c := in[i]; {
		case c == '"':
			inString = !inString
		case c == '\\' && inString:
			if in[i+1] != 'u' {
				i++ // skip the escaped byte
				continue
			}
			cp, _ := strconv.ParseUint(string(in[i+2:i+6]), 16, 32)
			i += 5
			switch {
			case cp >= 0xD800 && cp < 0xDC00:
				if i+6 >= len(in) || in[i+1] != '\\' || in[i+2] != 'u' {
					return false
				}
				low, _ := strconv.ParseUint(string(in[i+3:i+7]), 16, 32)
				if low < 0xDC00 || low > 0xDFFF {
					return false
				}
				i += 6
			case cp >= 0xDC00 && cp <= 0xDFFF:
				return false
			}
		}
	}
	return true
}
```

- [ ] **Step 2: Run the seed corpus as unit tests**

Run: `go test -run 'FuzzParse|FuzzMinify' -v . 2>&1 | grep -E '^(--- |ok|FAIL)'`
Expected: `--- PASS: FuzzParse`, `--- PASS: FuzzMinify`, `ok`.

- [ ] **Step 3: Fuzz each target briefly**

Run: `go test -run '^$' -fuzz '^FuzzParse$' -fuzztime 60s . && go test -run '^$' -fuzz '^FuzzMinify$' -fuzztime 60s .`
Expected: `PASS` for each. If a fuzzer reports a failure, the failing input is saved under `testdata/fuzz/`. Stop and report it together with the C++ simdjson result for that input; do not change the oracle to make the failure go away.

- [ ] **Step 4: Commit**

```bash
git add fuzz_test.go
git commit -m "test: differential fuzzing against encoding/json

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: Benchmarks and pure-Go baseline

**Files:**
- Create: `bench_test.go`

**Interfaces:**
- Consumes: `Parser.Parse`, `Minify`, `stage1.Index`, the corpora (Task 7).
- Produces: `BenchmarkParse/<file>`, `BenchmarkStdlib/<file>`, `BenchmarkIndex`, `BenchmarkMinify`.

- [ ] **Step 1: Write the benchmarks**

`bench_test.go`:

```go
package simdjson

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"simdjson-go/internal/stage1"
)

var benchFiles = []string{"twitter.json", "citm_catalog.json", "canada.json", "github_events.json", "gsoc-2018.json", "update-center.json"}

func benchData(b *testing.B, name string) []byte {
	b.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "jsonexamples", name))
	if err != nil {
		b.Fatalf("%v (run scripts/fetch-testdata.sh)", err)
	}
	return data
}

func BenchmarkParse(b *testing.B) {
	for _, name := range benchFiles {
		b.Run(name, func(b *testing.B) {
			data := benchData(b, name)
			var p Parser
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			if _, err := p.Parse(data); err != nil {
				b.Fatal(err)
			}
			for b.Loop() {
				if _, err := p.Parse(data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkStdlib(b *testing.B) {
	for _, name := range benchFiles {
		b.Run(name, func(b *testing.B) {
			data := benchData(b, name)
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			for b.Loop() {
				var v any
				if err := json.Unmarshal(data, &v); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkIndex(b *testing.B) {
	data := benchData(b, "twitter.json")
	var idx []uint32
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		var err error
		if idx, err = stage1.Index(data, idx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMinify(b *testing.B) {
	data := benchData(b, "twitter.json")
	var dst []byte
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		var err error
		if dst, err = Minify(dst[:0], data); err != nil {
			b.Fatal(err)
		}
	}
}
```

- [ ] **Step 2: Record the pure-Go baseline**

Run: `go test -run '^$' -bench . -count 6 . | tee /tmp/simdjson-purego.txt | grep -E 'twitter|Index|Minify'`
Expected: every benchmark runs, and `BenchmarkParse` reports `0 allocs/op`. For reference, the prototype's pure-Go `BenchmarkParse/twitter.json` was about 4× faster than `BenchmarkStdlib/twitter.json` on an Apple silicon machine (about 640 vs 160 MB/s). Keep `/tmp/simdjson-purego.txt` for Task 10.

- [ ] **Step 3: Commit**

```bash
git add bench_test.go
git commit -m "test: benchmarks against encoding/json

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 10: arm64 NEON kernel

Port of the arm64 `json_character_block::classify` and `utf8_lookup4_algorithm.h` to `simd/archsimd`. archsimd has no byte movemask and no byte-wise pairwise add on arm64. So each 16-byte chunk is byte-permuted once with VTBL (`interleave`), after which a 3-level 16-bit `ConcatAddPairs` ladder over bit-weighted masks yields masks in natural bit order. One archsimd semantic is easy to get backwards: `x.ConcatShiftBytesRight(y, n)` takes bytes `n..` of `y:x`, with `y` as the low half, so the byte k positions before `in[i]` is `in.ConcatShiftBytesRight(prev, 16-k)[i]`. Correctness is defined as equality with the portable kernel (`classifyGeneric`, `utf8.Valid`).

**Files:**
- Create: `internal/stage1/kernel_arm64.go`
- Modify: `internal/stage1/kernel_purego.go` (add the build constraint)
- Test: `internal/stage1/kernel_arm64_test.go`

**Interfaces:**
- Consumes: `masks`, `classifyGeneric`, `block` (Tasks 1–2).
- Produces: on `arm64 && goexperiment.simd && !purego`, the same `classify(*[64]byte) masks` and `utf8Checker{next, valid}` as `kernel_purego.go`.

- [ ] **Step 1: Write the failing test**

`internal/stage1/kernel_arm64_test.go`:

```go
//go:build arm64 && goexperiment.simd && !purego

package stage1

import (
	"math/rand/v2"
	"testing"
	"unicode/utf8"
)

func TestClassifyMatchesGeneric(t *testing.T) {
	var b [64]byte
	for start := 0; start < 256; start += 64 { // every byte value at every position class
		for i := range b {
			b[i] = byte(start + i)
		}
		checkClassify(t, &b)
	}
	r := rand.New(rand.NewPCG(1, 2))
	for range 10000 {
		for i := range b {
			b[i] = byte(r.Uint32())
		}
		checkClassify(t, &b)
	}
}

func FuzzClassify(f *testing.F) {
	f.Add([]byte(`{"a":[1,2,"x\"y"]}`))
	f.Fuzz(func(t *testing.T, in []byte) {
		var b [64]byte
		copy(b[:], in)
		checkClassify(t, &b)
	})
}

func checkClassify(t *testing.T, b *[64]byte) {
	t.Helper()
	if got, want := classify(b), classifyGeneric(b); got != want {
		t.Fatalf("classify(%q)\n got %+v\nwant %+v", b[:], got, want)
	}
}

// neonValid runs the NEON checker over buf exactly as Index does.
func neonValid(buf []byte) bool {
	var (
		u    utf8Checker
		tail [64]byte
	)
	for off := 0; off < len(buf); off += 64 {
		blk, _ := block(buf, off, &tail)
		u.next(blk)
	}
	return u.valid(buf)
}

func TestUTF8MatchesStdlib(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	pieces := [][]byte{
		[]byte("a"), []byte("é"), []byte("€"), []byte("😀"), // valid 1..4 byte
		{0x80}, {0xc0, 0xaf}, {0xed, 0xa0, 0x80}, {0xf4, 0x90, 0x80, 0x80}, {0xff}, // invalid
		{0xe2, 0x82}, {0xf0, 0x9f}, // truncated
	}
	for range 20000 {
		var buf []byte
		for range r.IntN(200) {
			p := pieces[r.IntN(len(pieces))]
			if r.IntN(8) != 0 {
				p = pieces[r.IntN(4)] // mostly valid input
			}
			buf = append(buf, p...)
		}
		if got, want := neonValid(buf), utf8.Valid(buf); got != want {
			t.Fatalf("neonValid(%x) = %v, want %v", buf, got, want)
		}
	}
}

func FuzzUTF8(f *testing.F) {
	f.Add([]byte("héllo €😀"))
	f.Fuzz(func(t *testing.T, buf []byte) {
		if got, want := neonValid(buf), utf8.Valid(buf); got != want {
			t.Fatalf("neonValid(%x) = %v, want %v", buf, got, want)
		}
	})
}
```

- [ ] **Step 2: Give the portable kernel its build constraint**

Make these the first two lines of `internal/stage1/kernel_purego.go` (the rest of the file stays as is):

```go
//go:build !arm64 || !goexperiment.simd || purego

```

Run: `GOEXPERIMENT=simd go test ./internal/stage1/`
Expected: build failure, `undefined: classify` and `undefined: utf8Checker` (the NEON build now has no kernel).

- [ ] **Step 3: Implement the NEON kernel**

`internal/stage1/kernel_arm64.go`:

```go
//go:build arm64 && goexperiment.simd && !purego

package stage1

import "simd/archsimd"

// classify tables, adapted from the C++ arm64 json_character_block::classify.
var (
	// opTable[(c+3)>>4] == c exactly when c is one of ,:[]{}. Entry 0 is 0x80
	// (C++ uses 0xff) so that no byte of its group (0..12, 253..255) matches.
	opTable = [16]uint8{0x80, 0, ',', ':', 0, '[', ']', '{', '}'}
	// wsTable[c] == c exactly for \t \n \r; space is compared separately.
	wsTable = [16]uint8{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, '\t', '\n', 0xff, 0xff, '\r', 0xff, 0xff}
	// interleave moves bytes 0..7 to even and 8..15 to odd positions. With
	// bitWeights, a 16-bit pairwise-add ladder then yields masks in natural
	// bit order (NEON has no movemask and archsimd has no byte-wise ADDP).
	interleave = [16]uint8{0, 8, 1, 9, 2, 10, 3, 11, 4, 12, 5, 13, 6, 14, 7, 15}
	bitWeights = [16]uint8{1, 1, 2, 2, 4, 4, 8, 8, 16, 16, 32, 32, 64, 64, 128, 128}
)

func chunk(b *[64]byte, k int) archsimd.Uint8x16 {
	return archsimd.LoadUint8x16Array((*[16]uint8)(b[16*k : 16*k+16]))
}

func classify(b *[64]byte) masks {
	var (
		perm  = archsimd.LoadUint8x16Array(&interleave)
		w     = archsimd.LoadUint8x16Array(&bitWeights)
		ops   = archsimd.LoadUint8x16Array(&opTable)
		wss   = archsimd.LoadUint8x16Array(&wsTable)
		quote = archsimd.BroadcastUint8x16('"')
		bsl   = archsimd.BroadcastUint8x16('\\')
		space = archsimd.BroadcastUint8x16(' ')
		three = archsimd.BroadcastUint8x16(3)
		lt20  = archsimd.BroadcastUint8x16(0x20)

		q, bs, ws, op, ctrl [4]archsimd.Uint8x16
	)
	for k := range 4 {
		c := chunk(b, k).LookupOrZero(perm) // c[i] = chunk[interleave[i]]
		q[k] = w.Masked(c.Equal(quote))
		bs[k] = w.Masked(c.Equal(bsl))
		ws[k] = w.Masked(wss.LookupOrZero(c).Equal(c).Or(c.Equal(space)))
		op[k] = w.Masked(ops.LookupOrZero(c.Add(three).ShiftAllRight(4)).Equal(c))
		ctrl[k] = w.Masked(c.Less(lt20))
	}
	qb, wo, cc := pack(q, bs), pack(ws, op), pack(ctrl, ctrl)
	return masks{
		quote: qb.GetElem(0), backslash: qb.GetElem(1),
		ws: wo.GetElem(0), op: wo.GetElem(1),
		ctrl: cc.GetElem(0),
	}
}

// pack reduces two sets of four bit-weighted chunks to two 64-bit masks:
// lane 0 holds a's mask and lane 1 holds b's.
func pack(a, b [4]archsimd.Uint8x16) archsimd.Uint64x2 {
	return ladder(a).ConcatAddPairs(ladder(b)).ReshapeToUint64s()
}

// ladder returns [a0, a0, a1, a1, a2, a2, a3, a3] partial sums: after one more
// ConcatAddPairs each 16-bit lane is the full 16-bit mask of one chunk.
func ladder(a [4]archsimd.Uint8x16) archsimd.Uint16x8 {
	t0 := a[0].ReshapeToUint16s().ConcatAddPairs(a[1].ReshapeToUint16s())
	t1 := a[2].ReshapeToUint16s().ConcatAddPairs(a[3].ReshapeToUint16s())
	return t0.ConcatAddPairs(t1)
}

// UTF-8 lookup tables (src/generic/stage1/utf8_lookup4_algorithm.h).
const (
	tooShort     = 1 << 0
	tooLong      = 1 << 1
	overlong3    = 1 << 2
	tooLarge     = 1 << 3
	surrogate    = 1 << 4
	overlong2    = 1 << 5
	tooLarge1000 = 1 << 6
	overlong4    = 1 << 6
	twoConts     = 1 << 7
	carry        = tooShort | tooLong | twoConts
)

var (
	byte1High = [16]uint8{
		tooLong, tooLong, tooLong, tooLong, tooLong, tooLong, tooLong, tooLong,
		twoConts, twoConts, twoConts, twoConts,
		tooShort | overlong2,
		tooShort,
		tooShort | overlong3 | surrogate,
		tooShort | tooLarge | tooLarge1000 | overlong4,
	}
	byte1Low = [16]uint8{
		carry | overlong3 | overlong2 | overlong4,
		carry | overlong2,
		carry,
		carry,
		carry | tooLarge,
		carry | tooLarge | tooLarge1000,
		carry | tooLarge | tooLarge1000,
		carry | tooLarge | tooLarge1000,
		carry | tooLarge | tooLarge1000,
		carry | tooLarge | tooLarge1000,
		carry | tooLarge | tooLarge1000,
		carry | tooLarge | tooLarge1000,
		carry | tooLarge | tooLarge1000,
		carry | tooLarge | tooLarge1000 | surrogate,
		carry | tooLarge | tooLarge1000,
		carry | tooLarge | tooLarge1000,
	}
	byte2High = [16]uint8{
		tooShort, tooShort, tooShort, tooShort, tooShort, tooShort, tooShort, tooShort,
		tooLong | overlong2 | twoConts | overlong3 | tooLarge1000 | overlong4,
		tooLong | overlong2 | twoConts | overlong3 | tooLarge,
		tooLong | overlong2 | twoConts | surrogate | tooLarge,
		tooLong | overlong2 | twoConts | surrogate | tooLarge,
		tooShort, tooShort, tooShort, tooShort,
	}
	// A block whose last 3 bytes exceed these values ends inside a character.
	maxComplete = [16]uint8{
		255, 255, 255, 255, 255, 255, 255, 255,
		255, 255, 255, 255, 255, 0xf0 - 1, 0xe0 - 1, 0xc0 - 1,
	}
)

// utf8Checker is the lookup4 validator; it runs on every block.
type utf8Checker struct {
	err, prev, prevIncomplete archsimd.Uint8x16
}

func (u *utf8Checker) next(b *[64]byte) {
	in0, in1, in2, in3 := chunk(b, 0), chunk(b, 1), chunk(b, 2), chunk(b, 3)
	or := in0.Or(in1).Or(in2).Or(in3).ReshapeToUint64s()
	if (or.GetElem(0)|or.GetElem(1))&0x8080808080808080 == 0 {
		// An ASCII block cannot complete a character left open by the previous one.
		u.err = u.err.Or(u.prevIncomplete)
		return
	}
	u.check(in0, u.prev)
	u.check(in1, in0)
	u.check(in2, in1)
	u.check(in3, in2)
	u.prevIncomplete = in3.SubSaturated(archsimd.LoadUint8x16Array(&maxComplete))
	u.prev = in3
}

func (u *utf8Checker) check(in, prev archsimd.Uint8x16) {
	low4 := archsimd.BroadcastUint8x16(0x0f)
	// x.ConcatShiftBytesRight(y, n) is bytes n.. of y:x (y is the low half), so
	// in.ConcatShiftBytesRight(prev, 16-k)[i] is the byte k positions before in[i].
	prev1 := in.ConcatShiftBytesRight(prev, 15)
	sc := archsimd.LoadUint8x16Array(&byte1High).LookupOrZero(prev1.ShiftAllRight(4)).
		And(archsimd.LoadUint8x16Array(&byte1Low).LookupOrZero(prev1.And(low4))).
		And(archsimd.LoadUint8x16Array(&byte2High).LookupOrZero(in.ShiftAllRight(4)))
	prev2 := in.ConcatShiftBytesRight(prev, 14)
	prev3 := in.ConcatShiftBytesRight(prev, 13)
	must23 := prev2.SubSaturated(archsimd.BroadcastUint8x16(0xe0 - 0x80)).
		Or(prev3.SubSaturated(archsimd.BroadcastUint8x16(0xf0 - 0x80)))
	u.err = u.err.Or(must23.And(archsimd.BroadcastUint8x16(0x80)).Xor(sc))
}

func (u *utf8Checker) valid([]byte) bool {
	e := u.err.Or(u.prevIncomplete).ReshapeToUint64s()
	return e.GetElem(0)|e.GetElem(1) == 0
}
```

- [ ] **Step 4: Run the tests on every build**

Run: `GOEXPERIMENT=simd go test ./... && go test ./... && go test -tags purego ./... && GOEXPERIMENT=simd go vet ./... && go vet ./... && gofmt -l .`
Expected: `ok` for both packages on each of the three test runs.

- [ ] **Step 5: Fuzz the kernel against the portable one**

Run: `GOEXPERIMENT=simd go test -run '^$' -fuzz '^FuzzClassify$' -fuzztime 60s ./internal/stage1/ && GOEXPERIMENT=simd go test -run '^$' -fuzz '^FuzzUTF8$' -fuzztime 60s ./internal/stage1/`
Expected: `PASS` for each.

- [ ] **Step 6: Compare against the pure-Go baseline**

Run: `GOEXPERIMENT=simd go test -run '^$' -bench . -count 6 . | tee /tmp/simdjson-neon.txt >/dev/null && go run golang.org/x/perf/cmd/benchstat@latest /tmp/simdjson-purego.txt /tmp/simdjson-neon.txt`
Expected: `BenchmarkIndex` and `BenchmarkParse` are faster under NEON. In the prototype, `Index` went from about 0.9 to 2.1 GB/s and `Parse/twitter.json` from about 640 to 1015 MB/s.

- [ ] **Step 7: Commit**

```bash
git add internal/stage1
git commit -m "feat(stage1): arm64 NEON kernel via simd/archsimd

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 11: Build matrix and acceptance

Checks the spec's success criteria (§1) and build matrix (§8.4).

**Files:**
- Create: `scripts/check.sh`

**Interfaces:**
- Consumes: everything above.
- Produces: `scripts/check.sh`.

- [ ] **Step 1: Write the build-matrix script**

`scripts/check.sh`:

```sh
#!/bin/sh
# Runs the build matrix of the design spec (§8.4). Needs testdata/ (scripts/fetch-testdata.sh).
set -eux
cd "$(dirname "$0")/.."
test -z "$(gofmt -l .)"
go vet ./...
GOEXPERIMENT=simd go vet ./...
go test ./...                       # pure Go
go test -tags purego ./...          # pure Go forced
GOEXPERIMENT=simd go test ./...     # NEON kernel on arm64
GOARCH=amd64 go test ./...          # pure Go on amd64 (Rosetta 2 on Apple silicon)
GOOS=linux GOARCH=386 go vet ./...  # 32-bit: type-check only
GOOS=wasip1 GOARCH=wasm go vet ./...
```

- [ ] **Step 2: Run it**

Run: `chmod +x scripts/check.sh && ./scripts/check.sh`
Expected: exits 0. `darwin/386` does not exist, so 32-bit and wasm are type-checked with `go vet` only. `GOARCH=amd64 go test` runs under Rosetta 2 and takes a few seconds longer.

- [ ] **Step 3: Success criterion 3 — at least 3× faster than encoding/json on twitter.json (NEON build)**

Run: `GOEXPERIMENT=simd go test -run '^$' -bench 'Parse/twitter|Stdlib/twitter' -count 6 . > /tmp/simdjson-accept.txt && go run golang.org/x/perf/cmd/benchstat@latest /tmp/simdjson-accept.txt`
Expected: `Parse/twitter.json` time/op is at most a third of `Stdlib/twitter.json` (prototype: 0.62 ms vs 3.88 ms, 6.2×). If it is not, stop and report the numbers.

- [ ] **Step 4: Success criterion 4 — ten minutes per fuzz target without a finding**

Run:

```bash
go test -run '^$' -fuzz '^FuzzParse$' -fuzztime 10m .
go test -run '^$' -fuzz '^FuzzMinify$' -fuzztime 10m .
GOEXPERIMENT=simd go test -run '^$' -fuzz '^FuzzParse$' -fuzztime 10m .
GOEXPERIMENT=simd go test -run '^$' -fuzz '^FuzzClassify$' -fuzztime 10m ./internal/stage1/
GOEXPERIMENT=simd go test -run '^$' -fuzz '^FuzzUTF8$' -fuzztime 10m ./internal/stage1/
```

Expected: `PASS` for each. If an input fails, report it (it is saved under `testdata/fuzz/`); do not edit the oracle.

- [ ] **Step 5: Commit**

```bash
git add scripts/check.sh
git commit -m "build: add the build-matrix script

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
