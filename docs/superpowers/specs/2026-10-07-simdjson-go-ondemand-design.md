# simdjson-go — Sub-project 3a: On-Demand API — Design

- **Date:** 2026-10-07
- **Status:** Draft, awaiting review
- **Builds on:** sub-project 1 (core + DOM), `docs/superpowers/specs/2026-10-06-simdjson-go-core-design.md`
- **Followed by:** sub-project 3b, `Unmarshal` on On-Demand (its own spec, after this one ships)
- **Reference semantics:** C++ simdjson v5.0.2 `ondemand` (`doc/basics.md`, `doc/ondemand_design.md`,
  `include/simdjson/generic/ondemand/`, `tests/ondemand/`, `benchmark/*/simdjson_ondemand.h`)

## 1. Goal

A public, lazy, forward-only JSON reader — a Go version of C++ `ondemand` — for hot paths that read
some fields of large documents without building the tape. It runs stage 1 only; each value is
parsed when it is read, as the type it is read as. It is also the base sub-project 3b will build a
faster `Unmarshal` on.

### Success criteria

1. **Same results as C++.** For every scripted case in the oracle file (§8), each access returns
   the same value or the same error (the sentinel named after the C++ code) as C++ `ondemand`. A
   full walk (every value read) of every corpus file gives the same values as `Parse` plus a DOM
   walk, and fails exactly when the DOM fails. Both hold on 10 minutes of fuzzing.
2. **Fast.** On each of the six C++ benchmark tasks (§8) the On-Demand implementation is at least
   1.5× faster than `Parse` plus DOM navigation computing the same answer, with 0 allocations per
   operation once the parser is warm (NEON build, Apple M3 Max).
3. **No DOM regression.** After the shared code moves to `internal/` (§6), `Parser.Parse` is within
   2% of its speed before this sub-project, in geometric mean over `BenchmarkParse`, measured on an
   idle machine.
4. `make check` passes, including the race run.

## 2. Decisions

| Topic | Decision | Why |
|---|---|---|
| Scope | Public On-Demand API now; `Unmarshal` on it in 3b | The API is useful alone and 3b builds on it |
| Shape | Go-idiomatic names and iterators, C++ behaviour on everything exposed | Same choice as the DOM |
| Package | `simdjson-go/ondemand` | Mirrors C++'s namespace; avoids clashing with the DOM's `Object`/`Array` |
| Engine | A forward-only iterator over the stage-1 index, no tape | Tape building is about 60% of `Parse` on twitter; skipping it is the speed-up |
| Misuse | Detected and reported as `ErrOutOfOrderIteration` | C++ leaves it undefined in release builds; Go must stay memory-safe |
| Speed bar | ≥1.5× our DOM on the C++ benchmark tasks | Measurable in-repo, no C++ benchmark harness needed |

**Out of scope:** `iterate_many` and other document streams (sub-project 2); C++'s
`current_location`, `raw_json_token`, debug and logging helpers; a value-level `at_path`
(JSONPath); writing JSON (the DOM's `AppendJSON` and the binding's `Marshal` cover it).

## 3. Public API

```go
package ondemand // import "simdjson-go/ondemand"

// Parser reads documents On-Demand. The zero value is ready; reuse it across calls.
// A Parser is not safe for concurrent use.
type Parser struct {
	MaxDepth int // limit on nesting, checked as values are read; 0 means 1024, as the DOM
}
func (p *Parser) Iterate(b []byte) (*Document, error)

// Document is the root value: it has every getter of Value, plus:
func (d *Document) Rewind()
func (d *Document) AtPointer(ptr string) (Value, error) // rewinds first, as C++ does

type Type byte       // Array, Object, Number, String, Bool, Null (C++ json_type)
type NumberType byte // Int64, Uint64, Float64, BigInt (C++ number_type)

type Value struct{ /* handle */ }
func (v Value) Type() (Type, error)
func (v Value) NumberType() (NumberType, error)
func (v Value) Int64() (int64, error)
func (v Value) Uint64() (uint64, error)
func (v Value) Float64() (float64, error)
func (v Value) Bool() (bool, error)
func (v Value) IsNull() (bool, error)
func (v Value) String() (string, error)      // a copy
func (v Value) StringBytes() ([]byte, error) // unescaped; valid until the next Iterate
func (v Value) Raw() ([]byte, error)         // the value's JSON text (C++ raw_json); valid until the next Iterate
func (v Value) Object() (Object, error)
func (v Value) Array() (Array, error)
func (v Value) AtPointer(ptr string) (Value, error)

type Object struct{ /* handle */ }
func (o Object) Get(name string) (Value, error)      // C++ operator[] / find_field_unordered
func (o Object) FindNext(name string) (Value, error) // C++ find_field: forward only
func (o Object) All() iter.Seq2[Field, error]
func (o Object) Count() (int, error)                 // C++ count_fields: then rewinds the object
func (o Object) Reset()                              // C++ reset
func (o Object) Raw() ([]byte, error)

type Field struct{ /* handle */ }
func (f Field) Key() (string, error) // unescaped
func (f Field) RawKey() []byte       // as written, without quotes
func (f Field) Value() Value

type Array struct{ /* handle */ }
func (a Array) All() iter.Seq2[Value, error]
func (a Array) At(i int) (Value, error)
func (a Array) Count() (int, error) // C++ count_elements: then rewinds the array
func (a Array) Reset()
func (a Array) Raw() ([]byte, error)
```

Errors are the root package's sentinels (`simdjson.ErrNoSuchField`, `simdjson.ErrIncorrectType`,
…), the same values in both packages, so `errors.Is` works either way. Four sentinels are added,
named after their C++ codes: `ErrOutOfOrderIteration`, `ErrIncompleteArrayOrObject`,
`ErrTrailingContent`, `ErrScalarDocumentAsValue`.

## 4. Behaviour

Everything below is C++ `ondemand`'s behaviour, verified against it (§8), unless marked **Go**.

- **Validation.** `Iterate` checks only what stage 1 checks: UTF-8, unclosed strings, unescaped
  control characters and an empty document. Unbalanced brackets are `ErrIncompleteArrayOrObject`,
  reported where C++ reports them (at `Iterate` or when reached, as the oracle shows). Everything else — numbers, literals, string escapes, commas and
  colons — is checked when it is read. Skipped values are not validated ("validate what you use").
- **Forward only.** Reading an object or array moves the document's one cursor through it. Fields
  and elements not read in a loop body are skipped. Objects and arrays can be traversed once,
  unless rewound by `Count`, `Reset`, `Rewind` or `AtPointer`.
- **Scalars may be read later.** A `Value` holding a scalar may be read after the cursor has moved
  past it (C++ `top_tweet` relies on this): it is parsed from its own position.
- **Lookup.** `FindNext` searches from the cursor to the end of the object. `Get` does the same,
  then wraps to the object's first field and searches up to where it started, so each field is
  examined once. Names are compared on their raw bytes; a name containing a backslash is unescaped
  for the comparison. A missing field is `ErrNoSuchField`.
- **Numbers.** `Int64`/`Uint64` reject fractions and exponents (`ErrIncorrectType`) and report
  overflow as C++ does; `Float64` accepts any number; `NumberType` classifies without converting.
- **Scalar documents.** A document whose root is a scalar can be read with the getters;
  `Object`/`Array` on it are `ErrIncorrectType`; content after it is `ErrTrailingContent`.
- **Misuse (Go).** Reading an object or array out of order, reading a container twice, or using
  any handle after the next `Iterate` returns `ErrOutOfOrderIteration` and never panics or reads
  stale memory. (C++ checks this only in development builds.)
- **Lifetime.** A `Document` and every handle, `StringBytes` and `Raw` result are valid until the
  next `Iterate` on the same `Parser`, as with the DOM. The input is not copied, kept after the
  next `Iterate`, or modified, and needs no padding.

## 5. Internals

- **State.** A `Document` holds the input, the stage-1 structural indices (shared code with the
  DOM), one cursor (token position and depth), a reusable buffer for unescaped strings, and a
  generation counter incremented by every `Iterate`. Nothing is allocated per document once the
  buffers have grown; only `String` allocates (it copies).
- **Handles.** `Value`, `Object`, `Array` and `Field` are small structs: the document, the token
  position where the value starts, its depth, and the generation. Every container read checks
  that the cursor is at the handle's position and the generation matches — one comparison — else
  `ErrOutOfOrderIteration`. Scalar reads check only the generation.
- **Scalars** are parsed from the input at their token with the number and string code shared with
  stage 2 (§6); a terminator check rejects `123abc` and `truex` as stage 2 does.
- **Containers.** `Object`/`Array` check the opening bracket and step inside, enforcing `MaxDepth`
  (stage 1 does not count depth). After each loop body, an unread field or element is skipped by
  walking structurals and counting brackets until the depth is back to the parent's.
- **Strings.** Unescaped into the document's buffer and returned as a slice of it; the buffer is
  only appended to until the next `Iterate`, so earlier slices stay valid.

## 6. Refactor of existing code

- The number parser (`numbers.go`, `fastfloat.go`, `pow10tab.go`) and the string unescaper
  (`strings.go`) move to internal packages used by both stage 2 and `ondemand`.
- The error sentinels move to an internal package; the root package re-exports them, keeping
  every exported name and value (as it already does for stage 1's errors). This also avoids an
  import cycle in 3b, when the root `Unmarshal` will import `ondemand`.
- The DOM's behaviour must not change: the existing suites, the C++ oracle and `FuzzParse` pin it,
  and §1 criterion 3 bounds its speed.

## 7. Layout

| Path | Responsibility |
|---|---|
| `internal/jsonerr/` | The error sentinels (re-exported by the root package) |
| `internal/number/` | Number parsing, shared by stage 2 and `ondemand` |
| `internal/str/` | String unescaping, shared by stage 2 and `ondemand` |
| `ondemand/parser.go` | `Parser`, `Document`, the cursor, `Rewind`, `AtPointer` |
| `ondemand/value.go` | `Value` and its getters, `Type`, `NumberType` |
| `ondemand/object.go`, `ondemand/array.go` | `Object`, `Field`, `Array`, lookup, iteration, skipping |
| `ondemand/*_test.go` | Oracle replay, DOM agreement, ported C++ tests, misuse, fuzz, benchmarks |
| `scripts/ondemand-oracle/` | The C++ program and script that regenerate the oracle file |
| `testdata/ondemand/oracle.jsonl` | The committed oracle results |

## 8. Testing

- **C++ oracle (golden file).** A C++ program under `scripts/ondemand-oracle/` (built by hand, not
  by the Go tests) runs scripted access patterns through C++ `ondemand` and records each result or
  error. A script is a small sequence of steps such as `get user`, `get id`, `int64`, `array`,
  `all`, `count`, `at_pointer /a/1`, `raw`. Cases are the corpus files and the C++ test inputs, with
  scripts that read, skip, look up out of order, and mutate documents (truncation, bad literals,
  bad numbers, escapes in names). The output is committed as `testdata/ondemand/oracle.jsonl`; a
  Go test replays every case. This pins C++ behaviour, including lazy validation, without cgo.
- **DOM agreement.** On every corpus file and fuzz input, a full On-Demand walk gives the same
  values as the DOM, and fails exactly when `Parse` fails.
- **Ported C++ tests.** The C++ `tests/ondemand/` cases for lookup, iteration, numbers, strings,
  scalar documents, `count`, `reset`, `rewind` and `at_pointer`, as Go table tests.
- **Misuse.** Reading a container twice or out of order, a stale `Field` after the loop moves on, and any handle
  after the next `Iterate` all return `ErrOutOfOrderIteration`; reading a saved scalar later works.
- **Fuzzing.** `FuzzOnDemand`: arbitrary bytes and an arbitrary access script; never panics; the
  full walk agrees with the DOM. `FuzzParse` and the existing suites keep guarding the DOM through
  the refactor.
- **Benchmarks.** The six C++ tasks, each written with On-Demand and with `Parse` plus DOM, and a
  test that both give the same answer:
  - `partial_tweets` (twitter.json: for each status, `created_at`, `id`, `text`,
    `in_reply_to_status_id` or 0, `user.id`, `user.screen_name`, `retweet_count`, `favorite_count`);
  - `distinct_user_id` (each status's `user.id`, and `retweeted_status.user.id` when present);
  - `find_tweet` (the `text` of the status with `id` 505874901689851904);
  - `top_tweet` (the status with the highest `retweet_count` ≤ 60: its `text` and `user.screen_name`);
  - `kostya` (generated: 524,288 objects with `x`, `y`, `z`, `name`, `opts` in `coordinates`; read
    `x`, `y`, `z`);
  - `large_random` (generated: an array of 1,000,000 `{x, y, z}` objects; read them).
  The generators are deterministic Go ports of the C++ ones (same shape, not the same random
  digits).

## 9. Risks

| Risk | Mitigation |
|---|---|
| Lazy-validation edge cases differ from C++ | The oracle file covers truncations and bad tokens in read and skipped positions; fuzzing adds DOM agreement |
| The misuse check costs speed | One comparison per container read; measured against the 1.5× bar |
| Moving shared code slows the DOM | Bounded by §1 criterion 3; the moved functions stay small enough to inline across packages |
| Benchmark noise on a busy machine | Criteria are measured on an idle machine; ratios use the same binary |
| `Get`'s wrap-around search makes repeated lookups quadratic in the field count | As in C++; documented, with `FindNext` for in-order reads |
