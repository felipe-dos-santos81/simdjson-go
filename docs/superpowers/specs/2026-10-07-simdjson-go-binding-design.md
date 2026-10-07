# simdjson-go — Sub-project 4: Data binding — Design

- **Date:** 2026-10-07
- **Status:** Draft, awaiting review
- **Builds on:** sub-project 1 (core + DOM), `docs/superpowers/specs/2026-10-06-simdjson-go-core-design.md`
- **Reference semantics:** Go 1.27 `encoding/json/v2` (`go doc encoding/json`, "Migrating to v2", and
  `go doc encoding/json/v2`); C++ simdjson reflective deserialization and builder
  (`doc/basics.md`, `doc/builder.md`) for the binding model

## 1. Goal

`Unmarshal` and `Marshal` between JSON and Go values, for services that import this library in place
of `encoding/json`. Decoding runs on the existing simdjson parser and tape; behaviour matches
`encoding/json/v2` defaults, with opt-in options for the v1 behaviours services rely on most.

### Success criteria

1. **Same results as v2.** For every input, target type and corresponding option set in the test
   suite (§8), `Unmarshal` agrees with `encoding/json/v2` on whether it fails and on the error's
   type (`*jsontext.SyntacticError` or `*json.SemanticError`); where v2 wraps an exported sentinel
   (`jsontext.ErrDuplicateName`, `json.ErrUnknownName`) the same sentinel is wrapped. On success
   the decoded values are `reflect.DeepEqual`. `Marshal` output is byte-identical to v2's (with
   `Deterministic(true)` on both sides when maps are involved). This holds on the corpora, the
   mapping tables and 10 minutes of fuzzing per target.
2. **Fast decoding.** `Unmarshal` into typed structs is at least 1.5× faster than v2 on
   `twitter.json` and `citm_catalog.json` (NEON build, Apple M3 Max).
3. **Encoding no slower.** `Marshal` of the same typed values is no slower than v2.
4. `make check` passes, including a race test of concurrent `Unmarshal`/`Marshal`.

### Baseline (twitter.json, NEON, Apple M3 Max, measured 2026-10-07)

| | Time | Allocs |
|---|---|---|
| v1 `Unmarshal` into a typed struct (7 fields used) | 1.33 ms | 261 |
| v2 `Unmarshal` into the same struct | 1.08 ms | 261 |
| v2 `Unmarshal` into `any` | 2.18 ms | 28,102 |
| `Parser.Parse` to the tape alone | 0.57 ms | 0 |
| `Parse` + a hand-written binder over the DOM | 0.62 ms (1.74× v2) | — |
| v2 `Marshal` of the struct | 0.057 ms | 1 |

The hand-written binder bounds what a tape-based binder can reach. A reflection-based binder must
stay within about 0.09 ms of it to keep 1.5×, which is why decoders are compiled per type (§5).

## 2. Decisions

| Topic | Decision | Why |
|---|---|---|
| Semantics | `encoding/json/v2` defaults, plus opt-in v1 behaviours (§3) | The parser already rejects invalid UTF-8, so v2 is the only standard library behaviour we can match exactly; exact name matching is the fast path |
| Scope | `Unmarshal`, `Marshal`, `MarshalAppend`, `MarshalIndent` on byte slices | Services need both directions; `io.Reader` forms would still read whole documents |
| Bar | Correctness first, ≥1.5× v2 decoding, encoding ≥ v2 | Reachable on the tape; On-Demand (sub-project 3) can raise it later |
| Architecture | Per-type decoders and encoders, compiled once and cached, over the existing tape | Meets the bar with margin; no change to stages 1–2; the public API survives a later On-Demand decoder |
| Package | The root package `simdjson` | One import for services |

**Out of scope:** `io.Reader`/`io.Writer` forms; v2's `MarshalerTo`/`UnmarshalerFrom` and
`WithMarshalers`/`WithUnmarshalers` (they need `jsontext` encoders and decoders); the `format:`,
`inline` and `unknown` tag options; `StringifyNumbers`, `FormatDurationAsNano` and the other v2
options not listed in §3; an On-Demand decoder (sub-project 3).

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
v2's depth of 10,000 (boundary verified against v2 in tests). The core `Parser` keeps its own
default of 1024 for direct `Parse` calls.

**Mechanics:** `Unmarshal` takes a `Parser{MaxDepth: 10000, BigIntAsString: true}` from a
`sync.Pool` and returns it afterwards. Decoded values never alias the parser's buffers (strings and
bytes are copied). Each Go type's decoder and encoder are built on first use and cached in a
`sync.Map`.

**Struct tags (v2 syntax).** Supported: the name, `-`, `omitempty` (v2: omit when the field
encodes as JSON `null`, `""`, `{}` or `[]`), `omitzero` (the Go zero value, or `IsZero()` returns
true), `string` (v2: a value normally encoded as a JSON number is quoted), `case:ignore`,
`case:strict`. Unexported fields are ignored; embedded structs are promoted using v2's visibility
rules (shallowest depth wins, then a tagged field, otherwise the name is dropped). The `format:`,
`inline` and `unknown` options return an error the first time the type is used, rather than
behaving differently from v2.

**Interfaces honoured:** `json.Marshaler`, `json.Unmarshaler`, `encoding.TextAppender`,
`encoding.TextMarshaler`, `encoding.TextUnmarshaler`, with v2's precedence (§4, §5). Values are
made addressable so pointer-receiver methods are called, as v2 does. A type implementing only
`MarshalerTo` or `UnmarshalerFrom` returns an "unsupported" error.

## 4. Decoding (JSON → Go)

**Precedence:** `Unmarshaler`, then `TextUnmarshaler` (JSON string input only), then the default
rule for the type:

| Go target | Accepts | Notes |
|---|---|---|
| `bool` | `true`, `false` | |
| `string` | string | |
| `[]byte`, `[N]byte` | base64 string (RFC 4648 §4) | `[]byte`: length reset, then appended; `[N]byte`: exactly N bytes |
| `int*`, `uint*` | number without `.` or exponent | overflow is an error; so are `1.0` and `1e2` |
| `float32`, `float64` | any number | overflow of the Go type is an error; integers past `uint64` (tape tag `Z`) are converted from their digits |
| struct | object | fields by name (§3); unknown names ignored unless `RejectUnknownMembers` |
| map | object | key kinds: string, integer, or a `TextUnmarshaler`; not cleared; an existing entry is decoded into |
| slice | array | length reset to zero, elements appended; a nil slice is allocated |
| array | array | elements zeroed then decoded; the length must match |
| pointer | any | `null` stores nil; otherwise a nil pointer is allocated and decoded into |
| `any` | any | `bool`, `string`, `float64`, `map[string]any`, `[]any`; `null` stores nil |
| non-empty interface | any | decodes into the existing concrete value; a nil interface is an error |
| `time.Time` | RFC 3339 string with nanoseconds | as v2 |
| `time.Duration`, complex, channel, function | — | error: no default representation in v2 |
| any target | `null` | stores the zero value |

**Mechanics.**
- A decoder is a `func(Element, unsafe.Pointer) error` built once per `reflect.Type`; a struct plan
  holds field offsets, an exact-name index and a folded-name index for case-insensitive matching.
- Structs with at most eight fields match names by a linear scan (length, then bytes); larger ones
  use a map.
- Unknown names are skipped by jumping over the value on the tape, without decoding it.
- Duplicate names: a per-instance bit set of filled fields catches duplicates in bound structs; a
  reusable scratch hash set checks the names of every other object (maps, skipped subtrees, `any`),
  since v2 rejects duplicates anywhere in the input.
- Numbers come from the tape's typed words (`l`, `u`, `d`, `Z`). Converting an `int64` or `uint64`
  to `float64` rounds exactly as `strconv.ParseFloat` does on the same digits.

## 5. Encoding (Go → JSON)

**Precedence:** `Marshaler` (its output is validated and minified, as v2 does), then
`TextAppender`, then `TextMarshaler`, then the default rule:

| Go value | JSON | Notes |
|---|---|---|
| `bool` | boolean | |
| `string` | string | invalid UTF-8 is an error |
| `int*`, `uint*` | number | `string` tag option: quoted |
| `float32`, `float64` | number in v2 format | shortest form for its own bit size; NaN and ±Inf are errors |
| `[]byte`, `[N]byte` | base64 string | |
| struct | object | fields in declaration order (promoted fields in their v2 position); `omitempty`, `omitzero` |
| map | object | key kinds: string, integer, or a `TextMarshaler`; unordered unless `Deterministic` |
| slice, array | array | nil slice → `[]`, or `null` with `FormatNilSliceAsNull` |
| nil map | `{}` | `null` with `FormatNilMapAsNull` |
| pointer, interface | the value, or `null` | |
| `time.Time` | RFC 3339 string with nanoseconds | as v2 |
| `time.Duration`, complex, channel, function | — | error |

**v2 output format** (probed on Go 1.27):
- Floats use decimal notation for 1e-6 ≤ |x| < 1e21 and exponent notation otherwise, with no
  padding or trailing `.0`: `100`, `0.000001`, `1e-7`, `1e+21`. A `float32` uses its own shortest
  form (`0.1`).
- Strings use minimal escaping: `"`, `\` and bytes below 0x20 only, as `\b \f \n \r \t` or
  lowercase `\u00XX`; `<`, `>`, `&`, U+2028 and U+2029 are written as is.

This differs on purpose from the DOM's `AppendJSON`, which keeps `.0` on integral floats so a
parsed float re-parses as a float.

**Mechanics.** An encoder is a `func(dst []byte, p unsafe.Pointer) ([]byte, error)` built once per
type, cached with the decoder. Struct plans precompute each quoted name with its colon. String
quoting copies runs of safe bytes and validates UTF-8 in the same pass. `MarshalIndent` re-indents
the compact output in a separate pass (matching v2's `jsontext.WithIndentPrefix`/`WithIndent`
output, including `{}` and `[]` staying on one line), so `Marshal` pays nothing for it. `Marshal`
allocates only its result; `MarshalAppend` reuses the caller's buffer.

## 6. Errors

The same error types as v2, so callers' `errors.As` code works unchanged; `errors.Is` also reaches
the wrapped sentinel:

| Situation | Type | Wrapped error and fields |
|---|---|---|
| Malformed JSON (any parser error) | `*jsontext.SyntacticError` | our sentinel (`ErrTape`, `ErrNumber`, `ErrUTF8`, …) |
| Duplicate object name | `*jsontext.SyntacticError` | `jsontext.ErrDuplicateName` |
| Unknown name with `RejectUnknownMembers` | `*json.SemanticError` | `json.ErrUnknownName` |
| JSON kind does not fit the Go type; overflow; array length | `*json.SemanticError` | `JSONKind`, `GoType` |
| Unsupported Go type or tag option; NaN or ±Inf on `Marshal` | `*json.SemanticError` | `GoType` |
| Invalid UTF-8 in a Go string on `Marshal` | `*jsontext.SyntacticError` | our `ErrUTF8` (v2 wraps an unexported error) |
| Error from a user `UnmarshalJSON`/`MarshalJSON`/`…Text` method | `*json.SemanticError` | the method's error |
| `Unmarshal` into a non-pointer or nil pointer | `*json.SemanticError` | as v2 |

`JSONPointer` is set to the location being decoded (e.g. `/statuses/3/user/id`). `ByteOffset` is
always 0: the tape records no input offsets (nor does the C++ DOM), and adding them would slow
parsing. Error message text is not promised to match v2's; the type and wrapped error are.

**On error:** decoding stops at the first error, and the contents of `v` are unspecified, as in
v2. In practice a syntax error leaves `v` untouched, because the whole document is validated before
binding starts; after a semantic error, values decoded before it remain.

## 7. Layout

| File | Responsibility |
|---|---|
| `options.go` | `Option` and its constructors |
| `typeplan.go` | tag parsing, struct field plans (embedding, name indexes), the per-type cache |
| `decode.go` | `Unmarshal`, per-type decoders, duplicate-name checking |
| `encode.go` | `Marshal`, `MarshalAppend`, per-type encoders, v2 number format, string quoting |
| `indent.go` | `MarshalIndent` |
| `decode_test.go`, `encode_test.go` | mapping tables, options, tags, interfaces, errors, differential checks against v2 |
| `bind_fuzz_test.go` | `FuzzUnmarshal`, `FuzzMarshal` |
| `bind_bench_test.go` | `BenchmarkUnmarshal`, `BenchmarkMarshal`, each beside the v2 equivalent |

## 8. Testing

`encoding/json/v2` from the installed toolchain is the oracle, called in-process.

- **Differential `Unmarshal`.** Same input, target type and corresponding options on both sides;
  they must agree as in §1 criterion 1. Targets: `any`; typed structs for `twitter.json`,
  `citm_catalog.json` and `canada.json`; a table covering every row of §4.
- **Differential `Marshal`.** Byte-identical output, with `Deterministic(true)` on both sides when
  maps are involved; `MarshalIndent` against v2 with `jsontext.WithIndentPrefix`/`WithIndent`.
- **Unit tests.** Every option and tag option; embedded-field promotion and its conflicts;
  duplicate names in bound and skipped objects; the depth limit; `time.Time`, `netip.Addr` and
  custom `Marshaler`/`Text` types, including pointer receivers; every row of §6.
- **Fuzzing (10 minutes per target).** `FuzzUnmarshal` decodes arbitrary bytes into `any` and into
  a fixed set of struct types and compares with v2. `FuzzMarshal` decodes arbitrary JSON into `any`
  with v2, then compares our `Marshal` of that value with v2's.
- **Concurrency.** A race test runs `Unmarshal` and `Marshal` concurrently over types not yet in
  the cache; `make check` gains a `-race` run of it.
- **Benchmarks.** Each benchmark sits beside its v2 equivalent. The duplicate-name check is
  benchmarked first, as an early go/no-go on the speed budget.

## 9. Risks

| Risk | Mitigation |
|---|---|
| The duplicate-name check eats the speed budget | Measured first; a faster check (e.g. a hash of name lengths and first bytes before full comparison) is the fallback, not dropping the rule |
| Errors in the `unsafe` offset code | Every decoder is fuzzed against v2 and race-tested; `reflect.Value` paths remain for types not specialised |
| v2 behaviour changes between Go releases | Tests compare against the installed v2, so drift fails loudly; this spec pins Go 1.27 |
| `time.Time` edge cases differ from v2 | A table of edge-case timestamps is compared with v2 |
| Pooled parsers keep large buffers alive | A parser that grew beyond a size threshold is dropped instead of returned to the pool |
