# simdjson-go

A pure-Go port of [simdjson](https://github.com/simdjson/simdjson): its parser, DOM and On-Demand APIs, plus `Unmarshal` and `Marshal` that behave like `encoding/json/v2`. No cgo, no dependencies outside the standard library. On arm64, an optional NEON kernel speeds up the structural scan.

The parser, the On-Demand reader and the document streams match C++ simdjson v5.1.1: tape format, error codes and edge cases. Tests check them against C++: the parser on the simdjson-data corpora, On-Demand on about 30,000 reads recorded from C++, and the streams on about 3,000 recorded streams.

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

Package `ondemand` is C++'s lazy, forward-only reader: `Iterate` only finds the structural characters, and each value is parsed when read, as the type it is read as. It suits reading some fields of large documents; it is 1.3–2× faster than `Parse` plus the DOM on C++'s benchmark tasks and allocates nothing once warm.

- **`Parser`**: `Iterate(b)` returns the `*Document`. `MaxDepth` (default 1024) limits nesting, also for `IterateMany` documents: a read that starts an array or object at depth `MaxDepth` or deeper fails with `ErrDepth`, as C++ built with development checks does. The root is at depth 1, empty arrays and objects count, and skipped values don't. For deeper documents, raise it: `ondemand.Parser{MaxDepth: 100_000}`. The limit came with the sync to C++ v5.1.1; before it, `ondemand` read any depth.
- **`Document`** (the root): `Get`, `FindNext`, `Object`, `Array`, `Value`, typed getters for a scalar root, `AtPointer`, `Raw`, `Rewind`, `AtEnd`.
- **`Value`**: `Type`, `NumberType`, `Int64`, `Uint64`, `Float64`, `Bool`, `IsNull`, `String`, `StringBytes`, `Raw`, `Object`, `Array`, `Get`, `FindNext`, `AtPointer`.
- **`Object`**: `Get` (searches forward, then wraps around once), `FindNext` (forward only), `All` (`iter.Seq2[Field, error]`), `Count`, `Reset`, `Raw`, `AtPointer`. **`Field`**: `Key`, `RawKey`, `Value`. **`Array`**: `All`, `At`, `Count`, `Reset`, `Raw`, `AtPointer`.

As in C++:

- Values are validated only when read ("validate what you use"), and nothing checks what follows a root array or object unless you call `AtEnd`.
- Objects and arrays are read once, in order; `Count`, `Reset`, `Rewind` and `AtPointer` go back. A scalar `Value` may be read later.
- Field names are compared as written, without decoding escapes.
- A fatal error (`ErrTape`, `ErrIncompleteArrayOrObject`) kills the document: `AtPointer` returns the error again, and `Rewind` does not revive it.

Reading out of order, or a handle after the next `Iterate`, returns `ErrOutOfOrderIteration` (C++ leaves it undefined). The `*Document` itself is reused by its `Parser`, so an old one reads the new document. Everything read is valid until the next `Iterate` (`StringBytes` until the next `Rewind` too). Errors are the `simdjson` package's sentinels.

### Streams

```go
var p simdjson.Parser // or ondemand.Parser with IterateMany
for doc, err := range p.ParseMany(data, simdjson.Whitespace) {
	if err != nil {
		return err // a *simdjson.StreamError, always the last item
	}
	…
}
```

`ParseMany` and `ondemand.Parser.IterateMany` read many documents from one buffer, as C++'s `parse_many` and `iterate_many` do.

- **Formats:** `Whitespace` (NDJSON, concatenated JSON), `NewlineDelimited`, `JSONSequence` (RFC 7464), `CommaDelimited`, and `CommaDelimitedArray` (the elements of one top-level array).
- **`BatchSize`** (default 1,000,000 bytes) sets how much input stage 1 indexes at a time. A goroutine indexes the next window while you read the current one. The batch size changes speed and memory, never the result.
- **Results** are C++'s results with the whole input in one batch, with three differences. An incomplete last document ends the stream with `ErrTrailingContent` (C++ drops it silently). Invalid UTF-8 or a control character in a string is reported at the document that holds it, after the documents before it. A read error never ends an `IterateMany` stream (next bullet).
- **Read errors in `IterateMany`:** a wrong type, a bad number or a missing field belongs to the read, and the stream goes on. After a read that meets a fatal error (`ErrTape`, `ErrIncompleteArrayOrObject`) or `ErrDepth`, the stream skips the rest of that document and yields the next one; C++ v5.1.1 ends the stream there, so one bad record would cost every record after it.
- **`Document.Offset` and `Source`** give each document's position and bytes in the input.

**Lifetime:** a yielded document is valid until the loop's next step, or the next parse on the same `Parser`. After its step, an On-Demand document returns `ErrOutOfOrderIteration`. A second stream on the same `Parser` ends the first.

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

The next two tables come from the Go column of the C++ comparison below.

On-Demand against `Parse` plus the DOM, on C++ simdjson's benchmark tasks (`ondemand/bench_test.go`):

| `partial_tweets` | `distinct_user_id` | `find_tweet` | `top_tweet` | `kostya` | `large_random` |
|---|---|---|---|---|---|
| 1.75× | 1.77× | 2.02× | 1.75× | 1.45× | 1.27× |

Streams on `large_amazon_cellphones` (C++'s 10 MiB build of `amazon_cellphones.ndjson`), one window against the default `BatchSize`:

| | One window | Default (pipelined) | Speed-up |
|---|---|---|---|
| `ParseMany` | 769 MiB/s | 1082 MiB/s | 1.41× |
| `IterateMany` | 923 MiB/s | 1401 MiB/s | 1.52× |

Against C++ simdjson v5.1.1 on the same machine and inputs (`make bench-cpp`): C++ built with `-O3` and threads, NEON Go build, median of 6 alternating runs. The load average was 3.7 before the run and 1.7 after it; the confidence intervals stay within ±3% on every row except `Tasks/top_tweet/dom` (C++ ±8%). Go runs at 0.20–0.49× of C++'s throughput, 0.29× on the geometric mean:

| Benchmark | C++ | Go | Go / C++ |
|---|---|---|---|
| `Parse/twitter.json` | 4098 MiB/s | 1071 MiB/s | 0.26× |
| `Parse/citm_catalog.json` | 4634 MiB/s | 1267 MiB/s | 0.27× |
| `Parse/canada.json` | 1455 MiB/s | 718 MiB/s | 0.49× |
| `Parse/github_events.json` | 4507 MiB/s | 1141 MiB/s | 0.25× |
| `Parse/gsoc-2018.json` | 5339 MiB/s | 1440 MiB/s | 0.27× |
| `Parse/update-center.json` | 3706 MiB/s | 748 MiB/s | 0.20× |
| `ParseMany/amazon_cellphones/default` | 2906 MiB/s | 779 MiB/s | 0.27× |
| `ParseMany/amazon_cellphones/single` | 2909 MiB/s | 780 MiB/s | 0.27× |
| `ParseMany/large_amazon_cellphones/default` | 3829 MiB/s | 1082 MiB/s | 0.28× |
| `ParseMany/large_amazon_cellphones/single` | 2868 MiB/s | 769 MiB/s | 0.27× |
| `Tasks/partial_tweets/ondemand` | 5940 MiB/s | 1802 MiB/s | 0.30× |
| `Tasks/partial_tweets/dom` | 3847 MiB/s | 1031 MiB/s | 0.27× |
| `Tasks/distinct_user_id/ondemand` | 6049 MiB/s | 1856 MiB/s | 0.31× |
| `Tasks/distinct_user_id/dom` | 3942 MiB/s | 1051 MiB/s | 0.27× |
| `Tasks/find_tweet/ondemand` | 7654 MiB/s | 2168 MiB/s | 0.28× |
| `Tasks/find_tweet/dom` | 4078 MiB/s | 1074 MiB/s | 0.26× |
| `Tasks/top_tweet/ondemand` | 6083 MiB/s | 1866 MiB/s | 0.31× |
| `Tasks/top_tweet/dom` | 3970 MiB/s | 1066 MiB/s | 0.27× |
| `Tasks/kostya/ondemand` | 3659 MiB/s | 1347 MiB/s | 0.37× |
| `Tasks/kostya/dom` | 2832 MiB/s | 930 MiB/s | 0.33× |
| `Tasks/large_random/ondemand` | 1679 MiB/s | 655 MiB/s | 0.39× |
| `Tasks/large_random/dom` | 1303 MiB/s | 514 MiB/s | 0.39× |
| `IterateMany/amazon_cellphones/default` | 3626 MiB/s | 940 MiB/s | 0.26× |
| `IterateMany/amazon_cellphones/single` | 3644 MiB/s | 940 MiB/s | 0.26× |
| `IterateMany/large_amazon_cellphones/default` | 5195 MiB/s | 1401 MiB/s | 0.27× |
| `IterateMany/large_amazon_cellphones/single` | 3525 MiB/s | 923 MiB/s | 0.26× |
| geomean | | | 0.29× |

## Development

```sh
make             # list the targets
make test        # tests, pure-Go build (downloads the corpora into testdata/ on first run)
make check       # gofmt, vet, every build, race: run before you commit
make fuzz target=FuzzParseMany time=60s   # any Fuzz* target; make picks its package
make bench neon=1 bench=Unmarshal/
make bench neon=1 pkg=./ondemand bench=Tasks
make bench-cpp   # Go vs C++ simdjson, as a table (needs a C++20 compiler)
make oracle      # record the C++ results again (needs a C++20 compiler)
```

The corpora come from [simdjson-data](https://github.com/simdjson/simdjson-data), pinned to one commit. The designs and plans are in [`docs/superpowers/`](docs/superpowers/). [`AGENTS.md`](AGENTS.md) has guidance for contributors and agents.

To follow a new C++ release, give [`docs/upstream-sync-prompt.md`](docs/upstream-sync-prompt.md) to an agent. It triages every upstream commit since the pinned version, measures what C++ returns before and after, and writes one porting prompt per change. Each sync's plan is kept in [`docs/upstream/`](docs/upstream/).

## Status

Follows C++ simdjson v5.1.1. Done: core and DOM (sub-project 1), streams (2), the On-Demand API (3a), data binding (4). `Unmarshal` on On-Demand (3b) was prototyped and dropped: with v2's full validation it gained only 1.13–1.26×. amd64 uses the portable kernel (no x86 SIMD yet).

## License

Apache-2.0 or MIT, at your option, as C++ simdjson. See [`LICENSE`](LICENSE), [`LICENSE-MIT`](LICENSE-MIT) and [`NOTICE`](NOTICE). The float conversion and parts of the data binding are adapted from the Go standard library ([`LICENSE-GO`](LICENSE-GO)).
