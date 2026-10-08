# simdjson-go — Sub-project 3a: On-Demand API — Design

- **Date:** 2026-10-07 (amended the same day after a verified prototype)
- **Status:** Approved; amended with the prototype's findings (§3–§9)
- **Builds on:** sub-project 1 (core + DOM), `docs/superpowers/specs/2026-10-06-simdjson-go-core-design.md`
- **Followed by:** sub-project 3b, `Unmarshal` on On-Demand (its own spec, after this one ships)
- **Reference semantics:** C++ simdjson v5.0.2 `ondemand`, release build (`doc/basics.md`,
  `doc/ondemand_design.md`, `include/simdjson/generic/ondemand/`, `benchmark/*/simdjson_ondemand.h`)

## 1. Goal

A public, lazy, forward-only JSON reader — a Go version of C++ `ondemand` — for hot paths that read
some fields of large documents without building the tape. It runs stage 1 only; each value is
parsed when it is read, as the type it is read as. It is also the base sub-project 3b will build a
faster `Unmarshal` on.

### Success criteria

1. **Same results as C++.** For every scripted case in the oracle file (§8), each read returns the
   same value or the same error (the sentinel named after the C++ code) as C++ `ondemand`. A full
   walk (every value read, then `AtEnd`) of every corpus file and fuzz input gives the same values
   as `Parse` plus a DOM walk, and fails exactly when the DOM fails, apart from the differences C++
   itself has between the two (§4). Both hold on 10 minutes of fuzzing.
2. **Fast.** On each of the six C++ benchmark tasks (§8) the On-Demand implementation is at least
   1.5× faster than `Parse` plus DOM navigation computing the same answer, with 0 allocations per
   operation once the parser is warm (NEON build, Apple M3 Max).
3. **No DOM regression.** After the shared code moves to `internal/` (§6), `Parser.Parse` is within
   2% of its speed before this sub-project, in geometric mean over `BenchmarkParse`.
4. `make check` passes, including the race run.

### Prototype results (NEON, Apple M3 Max, load average about 7)

| | `partial_tweets` | `distinct_user_id` | `find_tweet` | `top_tweet` | `kostya` | `large_random` |
|---|---|---|---|---|---|---|
| On-Demand | 0.356 ms | 0.350 ms | 0.281 ms | 0.343 ms | 120 ms | 125 ms |
| `Parse` + DOM | 0.613 ms | 0.597 ms | 0.580 ms | 0.591 ms | 189 ms | 193 ms |
| Speed-up | 1.72× | 1.71× | 2.06× | 1.73× | 1.58× | 1.54× |

All with 0 allocations per operation. `BenchmarkParse` after the refactor: −0.04% geomean
(interleaved runs). Oracle: 30,065 cases, all equal to C++. Fuzzing: 5 minutes, 65 million inputs.

## 2. Decisions

| Topic | Decision | Why |
|---|---|---|
| Scope | Public On-Demand API now; `Unmarshal` on it in 3b | The API is useful alone and 3b builds on it |
| Shape | Go-idiomatic names and iterators, C++ behaviour on everything exposed | Same choice as the DOM |
| Package | `simdjson-go/ondemand` | Mirrors C++'s namespace; avoids clashing with the DOM's `Object`/`Array` |
| Engine | A port of C++'s `json_iterator` and `value_iterator` over the stage-1 index, no tape | Same semantics by construction; tape building is about 60% of `Parse` on twitter |
| C++ build | Release semantics, with the development checks turned into errors | The oracle is a release build; the development checks only catch misuse, which must not crash Go |
| Depth | No limit, as C++ On-Demand has none | Nothing recurses per level; depth costs no memory beyond one int per open level |
| Speed bar | ≥1.5× our DOM on the C++ benchmark tasks | Measurable in-repo, no C++ benchmark harness needed |

**Out of scope:** `iterate_many` and other document streams (sub-project 2); C++'s
`current_location`, `raw_json_token`, `get_number`, `is_integer`, `for_each`, `at_path` and
debug/logging helpers; the `_in_string` getters; writing JSON.

## 3. Public API

```go
package ondemand // import "simdjson-go/ondemand"

type Parser struct{ /* buffers */ } // zero value ready; reuse it; not safe for concurrent use
func (p *Parser) Iterate(b []byte) (*Document, error)

// Document is the root value.
func (d *Document) Get(name string) (Value, error)      // on the root object
func (d *Document) FindNext(name string) (Value, error)
func (d *Document) Object() (Object, error)
func (d *Document) Array() (Array, error)
func (d *Document) Value() (Value, error)                // root array/object; a scalar root is ErrScalarDocumentAsValue
func (d *Document) Type() (Type, error)
func (d *Document) NumberType() (NumberType, error)
func (d *Document) Int64() (int64, error)                // and Uint64, Float64, Bool, IsNull,
func (d *Document) String() (string, error)              // StringBytes: a scalar root
func (d *Document) Raw() ([]byte, error)
func (d *Document) AtPointer(ptr string) (Value, error)  // rewinds first, as C++ does
func (d *Document) Rewind()
func (d *Document) AtEnd() bool                          // C++ at_end: the whole document was read

type Type byte       // TypeUnknown, TypeArray, TypeObject, TypeNumber, TypeString, TypeBool, TypeNull
type NumberType byte // Int64, Uint64, Float64, BigInt

type Value struct{ /* handle */ }
func (v Value) Type() (Type, error)
func (v Value) NumberType() (NumberType, error)
func (v Value) Int64() (int64, error)       // and Uint64, Float64, Bool, IsNull
func (v Value) String() (string, error)     // a copy
func (v Value) StringBytes() ([]byte, error)
func (v Value) Raw() ([]byte, error)        // C++ raw_json
func (v Value) Object() (Object, error)
func (v Value) Array() (Array, error)
func (v Value) Get(name string) (Value, error)
func (v Value) FindNext(name string) (Value, error)
func (v Value) AtPointer(ptr string) (Value, error)

type Object struct{ /* handle */ }
func (o Object) Get(name string) (Value, error)      // C++ operator[] / find_field_unordered
func (o Object) FindNext(name string) (Value, error) // C++ find_field
func (o Object) All() iter.Seq2[Field, error]
func (o Object) Count() (int, error)                 // C++ count_fields: then rewinds the object
func (o Object) Reset() error
func (o Object) Raw() ([]byte, error)
func (o Object) AtPointer(ptr string) (Value, error)

type Field struct{ /* handle */ }
func (f Field) Key() (string, error) // unescaped
func (f Field) RawKey() []byte       // as written, without quotes (C++ escaped_key)
func (f Field) Value() Value

type Array struct{ /* handle */ }
func (a Array) All() iter.Seq2[Value, error]
func (a Array) At(i int) (Value, error)
func (a Array) Count() (int, error) // C++ count_elements: then rewinds the array
func (a Array) Reset() error
func (a Array) Raw() ([]byte, error)
func (a Array) AtPointer(ptr string) (Value, error)
```

Errors are the root package's sentinels, the same values in both packages, so `errors.Is` works
either way. Four are added, named after their C++ codes: `ErrOutOfOrderIteration`,
`ErrIncompleteArrayOrObject`, `ErrScalarDocumentAsValue`, `ErrTrailingContent`.

## 4. Behaviour

Everything below is C++ `ondemand`'s behaviour in a release build, verified against it (§8), unless
marked **Go**.

- **Validation.** `Iterate` checks only what stage 1 checks: UTF-8, unclosed strings, unescaped
  control characters and an empty document; a leading byte-order mark is skipped. Everything else
  is checked when read. Skipped values are not validated ("validate what you use").
- **Root containers.** `Object`/`Array` on the root check that the last structural character
  closes it (`ErrIncompleteArrayOrObject`). Nothing checks what follows a root container:
  `[1] [2]` reads as `[1]` unless the caller checks `AtEnd`.
- **End of input.** Release C++ has no end-of-input checks inside containers: past the last
  structural it reads its padding (0), so a truncated document fails with `ErrTape` at the next
  comma or bracket check.
- **Forward only.** Objects and arrays are traversed once, unless rewound by `Count`, `Reset`,
  `Rewind` or `AtPointer`; fields and elements not read in a loop body are skipped.
- **Scalars may be read later.** A `Value` holding a scalar is parsed from its own position, so it
  can be read after the cursor has moved on (C++ `top_tweet` relies on this).
- **Lookup.** `FindNext` searches from the cursor to the end of the object; `Get` then wraps to the
  first field and searches up to where it started. Names are compared as written: escapes are not
  decoded, so `{"\u0061":1}` has no field `"a"`. A requested name longer than 64 bytes
  with an unescaped quote never matches (C++ `is_equal`). `AtPointer` uses `FindNext`; an empty
  pointer is the root on the `Document` and `ErrInvalidJSONPointer` elsewhere. A missing field is
  `ErrNoSuchField`. `Array.At` counts an element whose read failed as an element.
- **Numbers.** `Int64`/`Uint64` reject fractions, exponents and out-of-range values with
  `ErrIncorrectType`; `Float64` accepts any number but rejects an exponent of more than 19 digits
  and overflow (`ErrNumber`); `NumberType` classifies from the digits without validating, and a
  `BigInt` is read with `Raw`, unvalidated.
- **Scalar documents.** A root scalar is read with the document's getters (`Value` is
  `ErrScalarDocumentAsValue`); content after it is `ErrTrailingContent`. As C++ copies the root
  scalar and its trailing whitespace into a fixed buffer, a root integer longer than 21 bytes with
  that whitespace, or a number longer than 1083, is `ErrNumber`; past 1083 bytes, `NumberType`
  classifies a root that is `-` or digits followed by whitespace as `BigInt` (read with `Raw`), as
  C++'s `check_if_integer` does; and a root `false` is recognised
  by its first four bytes (`falsy` reads as `false`).
- **Differences from the DOM** (all C++'s own): the exponent, `BigInt`, root-buffer and `false`
  rules above. Everything else a full walk accepts, the DOM accepts, with the same values.
- **Misuse (Go).** Reading a container out of order or twice, a stale `Field`, a zero handle, or
  any handle after the next `Iterate` returns `ErrOutOfOrderIteration` and never panics. These are
  C++'s development-build checks; release C++ leaves them undefined. The `*Document` is the
  exception: its `Parser` reuses it, so after the next `Iterate` it reads the new document.
- **Lifetime.** A `Document` and every handle and `Raw`/`RawKey` slice are valid until the next
  `Iterate`; `StringBytes` until the next `Iterate` or `Rewind` (C++ reuses its string buffer;
  `Key` and `String` return copies). The input is not copied or modified and needs no padding.

## 5. Internals

- `ondemand/iter.go` and `valueiter.go` port C++'s `json_iterator` and `value_iterator` with C++'s
  names, so the two can be compared method by method. The structural index ends, as in C++, with
  two entries at the input's end and a 0; peeking past the input reads 0, like C++'s padding.
- Handles carry the document generation (incremented by every `Iterate`), checked on every read.
- Strings without escapes are returned as slices of the input; others are unescaped into the
  document's buffer.
- `ondemand/numbers.go` ports C++'s On-Demand number parsers (`parse_integer`, `parse_unsigned`,
  `parse_double`, `get_number_type`), reading 8 fraction digits at a time as C++ does; the float
  value comes from the shared conversion (§6).

## 6. Refactor of existing code

- `internal/jsonerr`: the error sentinels; the root package re-exports them with the same values.
- `internal/number`: the decimal-to-float64 conversion (`fastfloat.go`, `pow10tab.go`,
  `pow10gen.go`, adapted from Go's `internal/strconv`) and the terminator rules, used by stage 2
  and `ondemand`. Stage 2 keeps its own number scanner in `numbers.go`: moving it behind a call cost
  6% on `canada.json`.
- `internal/str`: string unescaping.
- The DOM's behaviour does not change: the existing suites, the DOM's C++ oracle and `FuzzParse`
  pin it, and §1 criterion 3 bounds its speed.

## 7. Layout

| Path | Responsibility |
|---|---|
| `internal/jsonerr/`, `internal/number/`, `internal/str/` | Shared errors, float conversion and terminators, unescaping (§6) |
| `ondemand/iter.go` | `Parser`, `Iterate`, `Document`'s cursor, skipping, unescaping, `Rewind`, `AtEnd` |
| `ondemand/valueiter.go` | The `value_iterator` port: containers, lookup, scalars' positions |
| `ondemand/numbers.go` | The number parsers |
| `ondemand/document.go`, `value.go`, `object.go`, `array.go` | The public API |
| `ondemand/oracle_test.go` | Replays `testdata/ondemand/oracle.jsonl` |
| `ondemand/dom_test.go`, `fuzz_test.go` | DOM agreement, `FuzzOnDemand`, misuse tests |
| `ondemand/bench_test.go` | The six benchmark tasks, both ways, and their agreement test |
| `scripts/ondemand-oracle/` | `oracle.cpp`, `gen.py`, `regen.sh` (`make oracle`), README |

## 8. Testing

- **C++ oracle.** `gen.py` writes cases: hand-picked scalars (well- and ill-formed numbers,
  literals, strings, escapes) alone, padded and inside containers; containers including
  ill-formed and truncated ones; scripts over every corpus file (walks, keys, counts, pointers,
  lookups in and out of order); and 4,000 mutated documents. `oracle.cpp` runs each script through
  C++ `ondemand` (release build) and `regen.sh` records the output in `oracle.jsonl` (about 30,000
  cases, 3 MB). `TestOracle` replays every case. The script language is documented in
  `oracle.cpp`.
- **DOM agreement.** A full walk of every corpus file, and of every fuzz input, gives the DOM's
  values or fails with it, apart from C++'s own differences (§4).
- **Misuse.** Out-of-order and repeated container reads, a stale field, zero handles and handles
  after `Iterate` return `ErrOutOfOrderIteration`; a saved scalar reads correctly later.
- **Fuzzing.** `FuzzOnDemand`: arbitrary input and an arbitrary read script; never panics; the full
  walk agrees with the DOM.
- **Benchmarks.** The six C++ tasks, each with On-Demand and with `Parse` plus DOM, and a test that
  both give the same answer: `partial_tweets`, `distinct_user_id`, `find_tweet` (status
  505874901689851904), `top_tweet` (highest `retweet_count` ≤ 60) on `twitter.json`; `kostya`
  (524,288 generated objects) and `large_random` (1,000,000 generated `{x,y,z}`), generated like
  C++'s (same shape, not the same digits).

## 9. Risks

| Risk | Mitigation |
|---|---|
| A C++ behaviour the oracle does not cover | 30,000 cases from corpora, edge cases and mutations; fuzzing against the DOM; new cases are one line in `gen.py` |
| Out-of-range reads in the port (C++ relies on padding) | Every index is bounds-checked; `FuzzOnDemand` runs arbitrary read sequences |
| The float tasks are close to the 1.5× bar | Measured 1.54–1.58× on a loaded machine; stage 1 is the floor (a third to a half of their time) |
| Users expect DOM strictness | The README and godoc state "validate what you use", raw name comparison and `AtEnd` |
