# simdjson-go

A pure-Go port of [simdjson](https://github.com/simdjson/simdjson): its parser, DOM and On-Demand APIs, plus `Unmarshal` and `Marshal` that behave like `encoding/json/v2`. No cgo, no dependencies outside the standard library. On arm64, an optional NEON kernel speeds up the structural scan.

The parser and On-Demand reader match C++ simdjson v5.0.2 (tape format, error codes, edge cases) and are checked against it: the parser on the simdjson-data corpora, On-Demand on 30,000 scripted reads recorded from C++.

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

### On-Demand

```go
import "simdjson-go/ondemand"

var p ondemand.Parser // reuse it across calls
doc, err := p.Iterate(data)
statuses, err := doc.FindNext("statuses")
arr, err := statuses.Array()
for tweet, err := range arr.All() {
	id, err := tweet.FindNext("id") // fields read in document order: one pass
	n, err := id.Uint64()
	…
}
```

Package `ondemand` is C++'s lazy, forward-only reader: `Iterate` only finds the structural characters, and each value is parsed when read, as the type it is read as. It suits reading some fields of large documents; it is 1.6–2× faster than `Parse` plus the DOM on C++'s benchmark tasks and allocates nothing once warm.

- **`Document`** (the root): `Get`, `FindNext`, `Object`, `Array`, `Value`, typed getters for a scalar root, `AtPointer`, `Raw`, `Rewind`, `AtEnd`.
- **`Value`**: `Type`, `NumberType`, `Int64`, `Uint64`, `Float64`, `Bool`, `IsNull`, `String`, `StringBytes`, `Raw`, `Object`, `Array`, `Get`, `FindNext`, `AtPointer`.
- **`Object`**: `Get` (searches forward, then wraps around once), `FindNext` (forward only), `All` (`iter.Seq2[Field, error]`), `Count`, `Reset`, `Raw`, `AtPointer`. **`Field`**: `Key`, `RawKey`, `Value`. **`Array`**: `All`, `At`, `Count`, `Reset`, `Raw`, `AtPointer`.

As in C++: values are validated only when read ("validate what you use"); objects and arrays are read once, in order (`Count`, `Reset`, `Rewind` and `AtPointer` go back); a scalar `Value` may be read later; field names are compared as written, without decoding escapes; and nothing checks what follows a root array or object unless you call `AtEnd`. Reading out of order, or a handle after the next `Iterate`, returns `ErrOutOfOrderIteration` (C++ leaves it undefined); the `*Document` itself is reused by its `Parser`, so an old one reads the new document. Everything read is valid until the next `Iterate` (`StringBytes` until the next `Rewind` too). Errors are the `simdjson` package's sentinels.

### Streams

```go
var p simdjson.Parser // or ondemand.Parser with IterateMany
for doc, err := range p.ParseMany(data, simdjson.Whitespace) {
	if err != nil {
		return err // *simdjson.StreamError, always the last item
	}
	…
}
```

`ParseMany` and `ondemand.Parser.IterateMany` read many documents from one buffer, as C++'s `parse_many` and `iterate_many`. The formats are `Whitespace` (NDJSON, concatenated JSON), `NewlineDelimited`, `JSONSequence` (RFC 7464), `CommaDelimited` and `CommaDelimitedArray` (a top-level array's elements). Stage 1 runs `BatchSize` bytes at a time, one window ahead in a goroutine, and the batch size never changes what is yielded. That is C++'s result with the whole input in one batch, with two differences. An incomplete last document, which C++ drops silently, ends the stream with `ErrTrailingContent`. Invalid UTF-8 or a control character in a string is reported at the document that holds it, after the documents before it. `Document.Offset` and `Source` give each document's place in the input.

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
| `Unmarshal` | 1.42× | 1.22× | 2.00× |
| `Marshal` | 1.19× | 1.51× | 1.02× |

On-Demand against `Parse` plus the DOM, on C++ simdjson's benchmark tasks (`ondemand/bench_test.go`):

| `partial_tweets` | `distinct_user_id` | `find_tweet` | `top_tweet` | `kostya` | `large_random` |
|---|---|---|---|---|---|
| 1.69× | 1.76× | 1.96× | 1.65× | 1.63× | 1.61× |

Streams, `large_amazon_cellphones` (`amazon_cellphones.ndjson` repeated 40 times, 11 MB), default `BatchSize` against a single window:

| | single window | pipelined | |
|---|---|---|---|
| `ParseMany` | 704 MiB/s | 884 MiB/s | 1.25× |
| `IterateMany` | 873 MiB/s | 1360 MiB/s | 1.56× |

## Development

```sh
make             # list targets
make test        # pure-Go build (downloads the corpora into testdata/ on first run)
make check       # gofmt, vet, every build, race; run before committing
make fuzz target=FuzzUnmarshal time=60s   # or FuzzOnDemand, FuzzParse, FuzzParseMany, FuzzIterateMany
make bench neon=1 bench=Unmarshal/
make bench neon=1 pkg=./ondemand bench=Tasks
```

Corpora come from [simdjson-data](https://github.com/simdjson/simdjson-data), pinned by commit. Designs and plans are in [`docs/superpowers/`](docs/superpowers/); contributor and agent guidance is in [`AGENTS.md`](AGENTS.md).

## Status

Done: core and DOM (sub-project 1), streams (2), the On-Demand API (3a), data binding (4). `Unmarshal` on On-Demand (3b) was prototyped and dropped: with v2's full validation it gained only 1.13–1.26×. amd64 uses the portable kernel (no x86 SIMD yet).

## License

Apache-2.0 or MIT, at your option, as C++ simdjson. See [`LICENSE`](LICENSE), [`LICENSE-MIT`](LICENSE-MIT) and [`NOTICE`](NOTICE). The float conversion and parts of the data binding are adapted from the Go standard library ([`LICENSE-GO`](LICENSE-GO)).
