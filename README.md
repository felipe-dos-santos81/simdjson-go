# simdjson-go

A pure-Go port of [simdjson](https://github.com/simdjson/simdjson): its parser and DOM API, plus `Unmarshal` and `Marshal` that behave like `encoding/json/v2`. No cgo, no dependencies outside the standard library. On arm64, an optional NEON kernel speeds up the structural scan.

The parser matches C++ simdjson v5.0.2 (tape format, error codes, edge cases) and was checked against it on the simdjson-data corpora.

## Usage

### DOM

```go
import "simdjson-go"

var p simdjson.Parser // zero value is ready; reuse it across calls
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

- **`Parser`**: `Parse(b)` returns a `*Document`. The input is not copied, kept or modified, and needs no padding; a leading UTF-8 BOM is skipped. `MaxDepth` (default 1024) limits nesting. `BigIntAsString` keeps integers beyond int64/uint64 as `TypeBigInt` instead of failing.
- **`Element`**: `Type`, `Int64`, `Uint64`, `Float64`, `Bool`, `IsNull`, `StringValue`, `StringBytes`, `BigInt`, `Array`, `Object`, `AtPointer` (RFC 6901), `AppendJSON`, `MarshalJSON`.
- **`Array`**: `Len`, `At`, `All`. **`Object`**: `Len`, `Get` (first duplicate key wins, as in C++), `All` (copies keys), `AllBytes` (zero-copy keys).
- **`Minify(dst, src)`** removes whitespace without parsing.
- **Errors** are sentinels (`ErrTape`, `ErrNumber`, `ErrDepth`, …), one per C++ error code; check them with `errors.Is`.

**Lifetime:** a `Document` and everything read from it stay valid until the next `Parse` on the same `Parser`, even a failed one. A `Parser` is not safe for concurrent use; a `Document` may be read from several goroutines while no `Parse` runs.

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

`Unmarshal`, `Marshal`, `MarshalAppend` and `MarshalIndent` follow `encoding/json/v2` defaults:

- exact name matching; duplicate names, invalid UTF-8 and a leading BOM are errors; nil slices and maps marshal as `[]` and `{}`;
- errors are v2's `*jsontext.SyntacticError` and `*json.SemanticError`;
- v2 tag syntax (`omitempty`, `omitzero`, `string`, `case:ignore`, `embed`), and `MarshalJSON`/`UnmarshalJSON`/text methods;
- options for common v1 behaviours: `MatchCaseInsensitiveNames`, `FormatNilSliceAsNull`, `FormatNilMapAsNull`, `Deterministic`, `RejectUnknownMembers`.

They are safe for concurrent use, and decoded values never alias the input.

**Differences from v2:** invalid JSON is always a `SyntacticError` (the whole input is validated first). Not supported: `io.Reader`/`io.Writer` forms, `MarshalerTo`/`UnmarshalerFrom` (and so v1's `json.Number`), the `format:` tag option, `embed` of non-structs, other v2 options, and `ByteOffset` in errors (always 0).

## Builds

| Build | How | Stage 1 kernel |
|---|---|---|
| Default | `go build` | Portable Go |
| NEON | `GOEXPERIMENT=simd go build` on arm64 | `simd/archsimd` NEON |
| Forced portable | `-tags purego` | Portable Go |

Requires Go 1.27.

## Performance

Apple M3 Max, NEON build.

| `twitter.json` | Time | vs `encoding/json` into `any` |
|---|---|---|
| `encoding/json.Unmarshal` into `any` | 3.89 ms | 1× |
| `Parse`, portable Go | 0.96 ms | 4.0× |
| `Parse`, NEON | 0.59 ms | 6.6× |

`Parse` makes no allocations once the `Parser` has grown its buffers.

Typed structs (`bind_bench_test.go`), speed-up over `encoding/json/v2`:

| | twitter | citm_catalog | canada |
|---|---|---|---|
| `Unmarshal` | 1.42× | 1.14× | 1.74× |
| `Marshal` | 1.19× | 1.51× | 1.02× |

## Development

```sh
make             # list targets
make test        # pure-Go build (downloads the corpora into testdata/ on first run)
make check       # gofmt, vet, every build, race; run before committing
make fuzz target=FuzzUnmarshal time=60s
make bench neon=1 bench=Unmarshal/
```

Corpora come from [simdjson-data](https://github.com/simdjson/simdjson-data), pinned by commit. Designs and plans are in [`docs/superpowers/`](docs/superpowers/); contributor and agent guidance is in [`AGENTS.md`](AGENTS.md).

## Status

Done: core and DOM (sub-project 1), data binding (sub-project 4). Next: streams (`ParseMany`) and On-Demand. amd64 uses the portable kernel (no x86 SIMD yet).

## License

Apache-2.0 or MIT, at your option, as C++ simdjson. See [`LICENSE`](LICENSE), [`LICENSE-MIT`](LICENSE-MIT) and [`NOTICE`](NOTICE). The float conversion and parts of the data binding are adapted from the Go standard library ([`LICENSE-GO`](LICENSE-GO)).
