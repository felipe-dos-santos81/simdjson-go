# simdjson-go

A pure-Go port of [simdjson](https://github.com/simdjson/simdjson)'s parser and DOM API, with `Unmarshal` and `Marshal` that behave like `encoding/json/v2`. It uses no cgo and no dependencies outside the standard library. On arm64 an optional NEON kernel speeds up the scan for structural characters.

C++ simdjson v5.0.2 is the reference: this port uses the same tape format, error codes and edge-case behaviour, and was checked against the C++ parser on the simdjson-data corpora.

## Usage

```go
import "simdjson-go"

var p simdjson.Parser // the zero value is ready; reuse it across calls
doc, err := p.Parse([]byte(`{"user":{"name":"Ada","ids":[1,2,3]}}`))
if err != nil {
	log.Fatal(err)
}

name, _ := doc.Root().AtPointer("/user/name")
s, _ := name.StringValue() // "Ada"

ids, _ := doc.Root().AtPointer("/user/ids")
arr, _ := ids.Array()
for i, v := range arr.All() {
	n, _ := v.Int64()
	fmt.Println(i, n)
}

out := doc.Root().AppendJSON(nil) // minified JSON
```

- **`Parser`**:
  - `Parse(b)` returns a `*Document`. The input is not copied, kept or modified, and needs no padding. A leading UTF-8 BOM is skipped.
  - `MaxDepth` (default 1024) limits nesting.
  - `BigIntAsString` keeps integers that fit neither int64 nor uint64 as `TypeBigInt` instead of failing.
- **`Element`**:
  - `Type`, `Int64`, `Uint64`, `Float64`, `Bool`, `IsNull`, `StringValue`, `StringBytes`, `BigInt`, `Array`, `Object`
  - `AtPointer` (RFC 6901 JSON Pointer)
  - `AppendJSON`, `MarshalJSON`
- **`Array`**: `Len`, `At`, `All`. **`Object`**: `Len`, `Get` (the first duplicate key wins, as in C++), `All` (copies keys), `AllBytes` (zero-copy keys).
- **`Minify(dst, src)`** removes whitespace without parsing.
- **Errors** are sentinel values (`ErrTape`, `ErrNumber`, `ErrDepth`, …), one per C++ error code. Check them with `errors.Is`.

**Lifetime:** a `Document` and everything read from it stay valid until the next `Parse` on the same `Parser`, including a call that fails. A `Parser` is not safe for concurrent use. A `Document` can be read from several goroutines as long as no `Parse` runs at the same time.

### Data binding

```go
type User struct {
	Name string  `json:"name"`
	IDs  []int64 `json:"ids,omitempty"`
}

var u User
err := simdjson.Unmarshal(data, &u)
out, err := simdjson.Marshal(&u)
```

`Unmarshal`, `Marshal`, `MarshalAppend` and `MarshalIndent` have the semantics of `encoding/json/v2` under its default options: exact name matching, duplicate names rejected, invalid UTF-8 rejected, `[]`/`{}` for nil slices and maps. Errors are v2's `*jsontext.SyntacticError` and `*json.SemanticError`. Struct tags use v2's syntax (`omitempty`, `omitzero`, `string`, `case:ignore`, `embed`; `format:` is not supported), and `MarshalJSON`/`UnmarshalJSON`/text methods are honoured. Options turn on the v1 behaviours services rely on: `MatchCaseInsensitiveNames`, `FormatNilSliceAsNull`, `FormatNilMapAsNull`, `Deterministic`, `RejectUnknownMembers`.

One difference: invalid JSON is always a `SyntacticError`, because the whole input is validated before decoding, where v2 may first report a semantic error it meets earlier. Not supported: the `io.Reader`/`io.Writer` forms, `MarshalerTo`/`UnmarshalerFrom` methods, the `format:` tag option, `embed` of anything but a struct, v2 options other than the five above, and `ByteOffset` in errors (always 0).

## Builds

| Build | How | Stage 1 kernel |
|---|---|---|
| Default | `go build` | Portable Go (all platforms) |
| NEON | `GOEXPERIMENT=simd go build` on arm64 | `simd/archsimd` NEON |
| Forced portable | `-tags purego` | Portable Go |

Requires Go 1.27. `GOEXPERIMENT=simd` is needed only for the NEON kernel.

## Performance

These numbers are for `twitter.json` on an Apple M3 Max:

| | Time | vs `encoding/json` |
|---|---|---|
| `encoding/json.Unmarshal` into `any` | 3.89 ms | 1× |
| `Parse`, portable Go | 0.96 ms | 4.0× |
| `Parse`, NEON | 0.59 ms | 6.6× |

`Parse` makes no allocations once the `Parser` has grown its buffers. The NEON structural indexer runs at about 2.1 GB/s.

Typed `Unmarshal` and `Marshal` against `encoding/json/v2` (NEON, structs of `bind_bench_test.go`):

| | twitter.json | citm_catalog.json | canada.json |
|---|---|---|---|
| `Unmarshal` | 1.46× | 1.12× | 1.71× |
| `Marshal` | 1.2× | 1.5× | 1.03× |

## Development

```sh
make            # list the targets
make test       # test the pure-Go build (downloads the test corpora on first run)
make test-neon  # test the NEON build
make check      # full build matrix: gofmt, vet, every build (run before committing)
make fuzz target=FuzzParse time=60s
make bench neon=1 bench=Parse/twitter
```

The test corpora come from [simdjson-data](https://github.com/simdjson/simdjson-data), pinned by commit and downloaded into `testdata/` (gitignored).

The design and the plan are in [`docs/superpowers/`](docs/superpowers/). Contributor and agent guidance is in [`AGENTS.md`](AGENTS.md).

## Status

Done: the core and the DOM (sub-project 1) and data binding (sub-project 4). Planned next are streams (`ParseMany`) and On-Demand. There are no x86 SIMD kernels; amd64 uses the portable Go code.

## License

Apache-2.0 or MIT, at your option, the same as C++ simdjson. See [`LICENSE`](LICENSE), [`LICENSE-MIT`](LICENSE-MIT) and [`NOTICE`](NOTICE). The float conversion and parts of the data binding are adapted from the Go standard library under [`LICENSE-GO`](LICENSE-GO).
