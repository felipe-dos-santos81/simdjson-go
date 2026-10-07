# simdjson-go — Sub-project 4: Data binding — Design

- **Date:** 2026-10-07 (amended the same day after a verified prototype)
- **Status:** Approved; amended with the prototype's findings (§1 criteria, §4–§7, §9)
- **Builds on:** sub-project 1 (core + DOM), `docs/superpowers/specs/2026-10-06-simdjson-go-core-design.md`
- **Reference semantics:** Go 1.27 `encoding/json/v2` (`go doc encoding/json`, "Migrating to v2", and
  `go doc encoding/json/v2`, plus its source in `$(go env GOROOT)/src/encoding/json/v2`); C++
  simdjson reflective deserialization and builder (`doc/basics.md`, `doc/builder.md`) for the
  binding model

## 1. Goal

`Unmarshal` and `Marshal` between JSON and Go values, for services that import this library in place
of `encoding/json`. Decoding runs on the existing simdjson parser and tape; behaviour matches
`encoding/json/v2` defaults, with opt-in options for the v1 behaviours services rely on most.

### Success criteria

1. **Same results as v2.** For every input, target type and corresponding option set in the test
   suite (§8), `Unmarshal` agrees with `encoding/json/v2` on whether it fails and on the error's
   type (`*jsontext.SyntacticError` or `*json.SemanticError`); where v2 wraps an exported sentinel
   (`jsontext.ErrDuplicateName`, `json.ErrUnknownName`) the same sentinel is wrapped. One
   deviation is allowed: on input that is not valid JSON, we always report a `SyntacticError`,
   where v2 may report a `SemanticError` it meets first (§6). On success the decoded values are
   `reflect.DeepEqual`. `Marshal` output is byte-identical to v2's (with `Deterministic(true)` on
   both sides when maps are involved). This holds on the corpora, the mapping tables and 10
   minutes of fuzzing per target.
2. **Fast decoding.** `Unmarshal` into typed structs is at least 1.4× faster than v2 on
   `twitter.json`, 1.1× on `citm_catalog.json` and 1.5× on `canada.json` (NEON build, Apple M3
   Max). (Amended from 1.5× on twitter and citm: the prototype measured 1.46×, 1.12× and 1.71×.
   On citm, `Parse` alone takes half of v2's whole `Unmarshal`, so 1.5× would need a faster
   parser.)
3. **Encoding no slower.** `Marshal` of the same typed values is no slower than v2 on all three
   files.
4. **Parsing barely slower.** Plain `Parser.Parse` (not binding) is at most 3% slower than before
   this sub-project, in geometric mean over `BenchmarkParse` (the prototype measured +2.6%).
5. `make check` passes, including a `-race` run.

### Baseline (twitter.json, NEON, Apple M3 Max, measured 2026-10-07)

| | Time | Allocs |
|---|---|---|
| v1 `Unmarshal` into a typed struct (7 fields used) | 1.33 ms | 261 |
| v2 `Unmarshal` into the same struct | 1.05 ms | 261 |
| v2 `Unmarshal` into `any` | 2.18 ms | 28,102 |
| `Parser.Parse` to the tape alone | 0.57 ms | 0 |
| `Parse` + a hand-written binder over the DOM | 0.62 ms (1.74× v2) | — |
| v2 `Marshal` of the struct | 0.077 ms | 1 |

### Prototype results (same machine, typed structs of `bind_bench_test.go`)

| | twitter | citm | canada |
|---|---|---|---|
| `Unmarshal` | 0.72 ms (1.46× v2) | 2.31 ms (1.12×) | 6.08 ms (1.71×) |
| v2 `Unmarshal` | 1.05 ms | 2.60 ms | 10.4 ms |
| `Marshal` | 0.064 ms (1.2× v2) | 0.61 ms (1.5×) | 5.05 ms (1.03×) |
| v2 `Marshal` | 0.077 ms | 0.92 ms | 5.19 ms |

## 2. Decisions

| Topic | Decision | Why |
|---|---|---|
| Semantics | `encoding/json/v2` defaults, plus opt-in v1 behaviours (§3) | The parser already rejects invalid UTF-8, so v2 is the only standard library behaviour we can match exactly; exact name matching is the fast path |
| Scope | `Unmarshal`, `Marshal`, `MarshalAppend`, `MarshalIndent` on byte slices | Services need both directions; `io.Reader` forms would still read whole documents |
| Bar | Correctness first, then the speeds of §1 | Reachable on the tape; On-Demand (sub-project 3) or a faster parser can raise it later |
| Architecture | Per-type decoders and encoders over `reflect.Value`, built once and cached, over the existing tape | Meets the bar; the public API survives a later On-Demand decoder |
| Binding mode | An unexported `Parser.binding` flag makes stage 2 record what v2 semantics need (§4) | The DOM's tape stays identical to C++ simdjson's; plain parsing pays only the flag checks |
| Package | The root package `simdjson` | One import for services |

**Out of scope:** `io.Reader`/`io.Writer` forms; v2's `MarshalerTo`/`UnmarshalerFrom` and
`WithMarshalers`/`WithUnmarshalers` (they need `jsontext` encoders and decoders; this also leaves
v1's `json.Number` unsupported, since it has only those methods); the `format:`
tag option; `embed` of anything but a struct (v2's map and `jsontext.Value` fallbacks for unknown
names); `StringifyNumbers`, `FormatDurationAsNano` and the other v2 options not listed in §3;
filling `ByteOffset` in errors; an On-Demand decoder (sub-project 3).

## 3. Public API

```go
func Unmarshal(data []byte, v any, opts ...Option) error
func Marshal(v any, opts ...Option) ([]byte, error)
func MarshalAppend(dst []byte, v any, opts ...Option) ([]byte, error)
func MarshalIndent(v any, prefix, indent string, opts ...Option) ([]byte, error)

type Option func(*options)

// Opt-in v1 behaviours, named and behaving as the v2 options of the same name.
func MatchCaseInsensitiveNames(v bool) Option // per-field `case:ignore`/`case:strict` still win
func FormatNilSliceAsNull(v bool) Option      // nil slice marshals as null, not []
func FormatNilMapAsNull(v bool) Option        // nil map marshals as null, not {}
func Deterministic(v bool) Option             // map keys sorted when marshaling
func RejectUnknownMembers(v bool) Option      // an unknown object name is an error
```

**Defaults (v2's):** struct fields match JSON names exactly; a duplicate object name anywhere in
the input is an error; unknown names are ignored; invalid UTF-8 is an error; nesting is limited to
v2's depth of 10,000, empty arrays and objects included (boundary verified against v2 in tests).
A leading byte-order mark is a `SyntacticError`, as in v2. The core `Parser` keeps its own default
of 1024 for direct `Parse` calls.

**Mechanics:** `Unmarshal` takes a `Parser{MaxDepth: 10001, BigIntAsString: true, binding: true}`
from a `sync.Pool` (a depth of 10001 because the parser counts the root; 10,000 nested levels
pass) and returns it afterwards, unless it grew past 1 Mi tape words (8 MiB) or 8 MiB of strings. Decoded
values never alias the parser's buffers (strings and bytes are copied; short strings go through a
per-parser 256-entry cache, as in v2). Each Go type's decoder and encoder are built on first use
and cached in a `sync.Map`.

**Struct tags (v2 syntax).** Supported: the name, `-`, `omitempty` (v2: omit when the field
encodes as JSON `null`, `""`, `{}` or `[]`), `omitzero` (the Go zero value, or `IsZero()` returns
true), `string` (v2: a value normally encoded as a JSON number is quoted), `case:ignore`,
`case:strict`, `embed` (of a struct). Unexported fields are ignored and may not carry a tag;
embedded structs are promoted using v2's rules (shallowest depth wins, then a tagged field,
otherwise the name is dropped; two same-depth fields of one struct with the same name are an
error). Folded names ignore case, `-` and `_`. The `format:` option, and embedding anything but a
struct, return an error the first time the type is used, rather than behaving differently from v2.

**Interfaces honoured:** `json.Marshaler`, `json.Unmarshaler`, `encoding.TextAppender`,
`encoding.TextMarshaler`, `encoding.TextUnmarshaler`, with v2's precedence (§4, §5). Values are
made addressable so pointer-receiver methods are called, as v2 does. A type implementing only
`MarshalerTo` or `UnmarshalerFrom` returns an "unsupported" error, even if it also has
`MarshalJSON` or `UnmarshalJSON`: v2 would call the `…To`/`…From` method, so calling the other one
would silently differ from v2.
`time.Time` and `time.Duration` use v2's built-in rules, not their own methods.

## 4. Decoding (JSON → Go)

**Precedence:** `Unmarshaler`, then `TextUnmarshaler` (JSON string input only), then the default
rule for the type:

| Go target | Accepts | Notes |
|---|---|---|
| `bool` | `true`, `false` | |
| `string` | string | |
| `[]byte`, `[N]byte` | base64 string (RFC 4648 §4, no line breaks) | `[N]byte`: exactly N bytes; a named byte element type is an array of numbers instead |
| `int*`, `uint*` | number without `.` or exponent | overflow is an error; so are `1.0` and `1e2`; `-0` fits an `int` but not a `uint` |
| `float32`, `float64` | any number | parsed from the number's text for its own bit size; overflow is an error |
| struct | object | fields by name (§3); unknown names ignored unless `RejectUnknownMembers` |
| map | object | key kinds: string, integer, float, or a `TextUnmarshaler` (other kinds, `bool` included, are an error, as in v2); not cleared; an existing entry is decoded into |
| slice | array | length set to the array's; capacity reused; elements zeroed then decoded; an empty array gives a non-nil empty slice |
| array | array | elements zeroed then decoded; the length must match |
| pointer | any | `null` stores nil; otherwise a nil pointer is allocated and decoded into |
| `any` | any | `bool`, `string`, `float64`, `map[string]any`, `[]any`; `null` stores nil |
| non-empty interface | any | decodes into a copy of the existing concrete value; a nil interface is an error |
| `time.Time` | RFC 3339 string | as v2, including its stricter checks |
| `time.Duration`, complex, channel, function | — | error: no default representation in v2 (even for `null`) |
| any other target | `null` | stores the zero value |

Numbers quoted by the `string` tag option, and map keys of number kinds, must hold valid JSON
number text. Duplicate map keys follow v2: for string and integer keys without methods, two
names that decode to the same key are duplicates (`"0"` and `"-0"`); for other keys, identical
names are.

**Binding mode** (stage 2, only when `Parser.binding` is set):
- `Document.offs` records each tape word's input offset (values, names and closing brackets), so
  decoders can read the original text of numbers and values (`UnmarshalJSON`, `-0`, float32).
- Each object's names are collected as the object is parsed; when it closes, the first name
  repeating an earlier one is recorded in `Document.dups` (sorted). Small objects compare names
  pairwise; larger ones use a hash table reused across objects.
- A float that overflows `float64` becomes ±Inf instead of a parse error, so the decoder reports
  v2's semantic overflow error for the target type.
- Empty arrays and objects count toward the depth limit, as in v2.

**Mechanics.**
- A decoder is a `func(*decodeState, Element, reflect.Value, mode uint8) error` built once per
  `reflect.Type`; `mode` carries the `string` tag option and "decoding a map key".
- A struct plan holds fields in v2's order, an exact-name index by name length, and a folded-name
  index used only with `MatchCaseInsensitiveNames` or a `case:ignore` field.
- A per-call bit set of filled fields catches a field set twice (also through case folding);
  `Document.dups` catches every other duplicate, including in skipped values and maps.
- Unknown names are skipped by jumping over the value on the tape.

## 5. Encoding (Go → JSON)

**Precedence:** `Marshaler`, then `TextAppender`, then `TextMarshaler`, then the default rule:

| Go value | JSON | Notes |
|---|---|---|
| `bool` | boolean | |
| `string` | string | invalid UTF-8 is an error |
| `int*`, `uint*` | number | `string` tag option: quoted |
| `float32`, `float64` | number in v2 format | shortest form for its own bit size; NaN and ±Inf are errors |
| `[]byte`, `[N]byte` | base64 string | |
| struct | object | fields in v2's order; `omitempty`, `omitzero` |
| map | object | key kinds as in §4; unordered unless `Deterministic` (sorted by the encoded name) |
| slice, array | array | nil slice → `[]`, or `null` with `FormatNilSliceAsNull` |
| nil map | `{}` | `null` with `FormatNilMapAsNull` |
| pointer, interface | the value, or `null` | pointer cycles are an error after 1000 levels, as in v2 |
| `time.Time` | RFC 3339 string with nanoseconds | as v2; years outside 0–9999 are an error |
| `time.Duration`, complex, channel, function | — | error |

`MarshalJSON` output is parsed and re-emitted compactly, with strings in our quoting and numbers
verbatim; invalid output or a duplicate name in it is an error. As a map key, it must be a string.

**v2 output format** (probed on Go 1.27):
- Floats use decimal notation for 1e-6 ≤ |x| < 1e21 and exponent notation otherwise, with no
  padding or trailing `.0`: `100`, `0.000001`, `1e-7`, `1e+21`. A `float32` uses its own shortest
  form (`0.1`).
- Strings use minimal escaping: `"`, `\` and bytes below 0x20 only, as `\b \f \n \r \t` or
  lowercase `\u00XX`; `<`, `>`, `&`, U+2028 and U+2029 are written as is.

This differs on purpose from the DOM's `AppendJSON`, which keeps `.0` on integral floats so a
parsed float re-parses as a float.

**Mechanics.** An encoder is a `func(*encodeState, reflect.Value, mode uint8) error` built once per
type, cached with the decoder. `Marshal` encodes into a pooled buffer (up to 8 MiB kept) and
returns an exactly sized copy; `MarshalAppend` appends to the caller's buffer and returns it
unchanged on error. `MarshalIndent` re-indents the compact output in a separate pass, matching
v2's `jsontext.WithIndentPrefix`/`WithIndent` (the first line has no prefix; `{}` and `[]` stay
on one line); a prefix or indent with characters other than spaces and tabs is an error (v2
panics on such options).

## 6. Errors

The same error types as v2, so callers' `errors.As` code works unchanged; `errors.Is` also reaches
the wrapped sentinel:

| Situation | Type | Wrapped error and fields |
|---|---|---|
| Malformed JSON (any parser error), leading BOM | `*jsontext.SyntacticError` | our sentinel (`ErrTape`, `ErrNumber`, `ErrUTF8`, …) |
| Duplicate object name | `*jsontext.SyntacticError` | `jsontext.ErrDuplicateName` |
| Unknown name with `RejectUnknownMembers` | `*json.SemanticError` | `json.ErrUnknownName` |
| JSON kind does not fit the Go type; overflow; array length | `*json.SemanticError` | `JSONKind`, `GoType` |
| Unsupported Go type or tag option; NaN or ±Inf on `Marshal` | `*json.SemanticError` | `GoType` |
| A map key that does not encode as a string | `*json.SemanticError` | a `*jsontext.SyntacticError` wrapping `jsontext.ErrNonStringName`, as in v2 |
| Invalid UTF-8 in a Go string on `Marshal` | `*jsontext.SyntacticError` | our `ErrUTF8` (v2 wraps an unexported error) |
| Nesting deeper than 10,000 on `Marshal` | `*jsontext.SyntacticError` | our `ErrDepth` |
| Error from a user `MarshalJSON`/`UnmarshalJSON` method | `*json.SemanticError` | the method's error; a `*json.SemanticError` it returns is merged in (its `JSONPointer` taken as relative), as in v2 |
| Error from a user text method | `*json.SemanticError` | the method's error; a `*json.SemanticError` it returns (or, from `UnmarshalText`, a `*jsontext.SyntacticError`) is returned as is, as in v2 |
| `errors.ErrUnsupported` from any user method | `*json.SemanticError` | replaced by "… method may not return errors.ErrUnsupported", as in v2 |
| `Unmarshal` into a non-pointer or nil pointer | `*json.SemanticError` | as v2 |

**Which error wins.** v2 reads some values whole before checking them, and others token by token.
Where a value is both the wrong kind and contains a duplicate name, we report what v2 does:
- the duplicate (`SyntacticError`) for targets v2 reads whole: strings, numbers, `[]byte`,
  `time.Time`, `TextUnmarshaler`, `Unmarshaler`, skipped unknown values and array overflow
  elements;
- the kind mismatch (`SemanticError`) for the rest: bool, struct, map, slice, array, pointer,
  interface, unsupported kinds, `time.Duration` and `string`-option errors.

Because our parser validates the whole document before binding starts, malformed input is always
a `SyntacticError`, where v2 may first report a semantic error found earlier (§1 criterion 1).

`JSONPointer` is set to the location being decoded (e.g. `/statuses/3/user/id`). `ByteOffset` is
always 0. Error message text is not promised to match v2's; the type and wrapped error are.

**On error:** decoding stops at the first error, and the contents of `v` are unspecified, as in
v2. In practice a syntax error leaves `v` untouched; after a semantic error, values decoded before
it remain.

## 7. Layout

| File | Responsibility |
|---|---|
| `parser.go`, `stage2.go`, `numbers.go` (modified) | binding mode: `Document.offs`, `Document.dups`, ±Inf floats, empty-container depth |
| `serialize.go` (modified) | an internal `appendJSON` hook that writes numbers verbatim (for `MarshalJSON` output) |
| `options.go` | `Option` and its constructors |
| `typeplan.go` | the per-type codec cache, method detection, tag parsing and struct field plans (adapted from v2's `fields.go`) |
| `decode.go` | `Unmarshal` and the default decoders |
| `encode.go` | `Marshal`, `MarshalAppend` and the default encoders, v2 number format, string quoting |
| `methods.go` | `Marshaler`, `Unmarshaler` and text-method codecs |
| `time.go` | `time.Time` and `time.Duration` (adapted from v2's `arshal_time.go`) |
| `strcache.go` | the string cache (adapted from v2's `intern.go`) |
| `indent.go` | `MarshalIndent` |
| `bind_test.go` | the differential suite against v2 |
| `bind_fuzz_test.go` | `FuzzUnmarshal`, `FuzzMarshal`, the concurrency test |
| `bind_bench_test.go` | `BenchmarkUnmarshal`, `BenchmarkMarshal`, each beside the v2 equivalent |

Files adapted from the Go source say so in a header and are covered by `LICENSE-GO`.

## 8. Testing

`encoding/json/v2` from the installed toolchain is the oracle, called in-process.

- **Differential `Unmarshal`.** Same input, target type and corresponding options on both sides;
  they must agree as in §1 criterion 1. Targets: about 40 types covering every row of §4, tags,
  embedding, methods and conflicts, over about 110 inputs, under no option,
  `MatchCaseInsensitiveNames` and `RejectUnknownMembers`; deep and wide documents at the depth
  limit; typed structs for `twitter.json`, `citm_catalog.json` and `canada.json`.
- **Differential `Marshal`.** Byte-identical output, with `Deterministic(true)` on both sides when
  maps are involved; `MarshalIndent` against v2 with `jsontext.WithIndentPrefix`/`WithIndent`.
- **Fuzzing (10 minutes per target).** `FuzzUnmarshal` decodes arbitrary bytes into every type of
  the differential suite under each option set and compares with v2. `FuzzMarshal` decodes
  arbitrary JSON into `any` and a struct with v2, then compares our `Marshal` with v2's.
- **Concurrency.** A test runs `Unmarshal` and `Marshal` from 8 goroutines over a type not yet in
  the cache; `make check` gains a `-race` run.
- **Parser regressions.** The existing suites (C++ ports, corpus, DOM fuzzers) keep passing, so the
  non-binding tape is unchanged.
- **Benchmarks.** Each benchmark sits beside its v2 equivalent.

## 9. Risks

| Risk | Mitigation |
|---|---|
| v2 behaviour changes between Go releases | Tests compare against the installed v2, so drift fails loudly; this spec pins Go 1.27 |
| A precedence rule of §6 is missed for some type | The fuzzers try every type of the suite against v2 |
| Binding-mode checks slow plain parsing | Measured: +2.6%, bounded by §1 criterion 4 |
| Pooled parsers or buffers keep large memory alive | Parsers over 1 Mi tape words (8 MiB) or 8 MiB of strings, and `Marshal` buffers over 8 MiB, are dropped instead of pooled |
| Reflection overhead caps decoding speed | Accepted at the speeds of §1; per-kind `unsafe` fast paths are the next step if a service needs more |
