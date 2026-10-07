# simdjson-go

A pure-Go port of [simdjson](https://github.com/simdjson/simdjson)'s parser and DOM API. It uses no cgo and no dependencies outside the standard library. On arm64 an optional NEON kernel speeds up the scan for structural characters.

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

This is sub-project 1 of 5: the core and the DOM. Planned next are streams (`ParseMany`), On-Demand, and data binding (`Unmarshal`/`Marshal`). There are no x86 SIMD kernels; amd64 uses the portable Go code.

## License

Apache-2.0 or MIT, at your option, the same as C++ simdjson. See [`LICENSE`](LICENSE), [`LICENSE-MIT`](LICENSE-MIT) and [`NOTICE`](NOTICE). The float conversion is adapted from the Go standard library under [`LICENSE-GO`](LICENSE-GO).
