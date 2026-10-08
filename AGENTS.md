# AGENTS.md

This file guides coding agents (and humans) working in this repo. Start with [`README.md`](README.md) for what the library is.

## Ground rules

- **C++ simdjson v5.0.2 is the oracle.** The tape format, the error returned for each bad input, and the edge cases must match C++. Some of those cases look like bugs but are intended, so don't "fix" them: the design spec lists them (§5.5, §9). Change parsing behaviour only together with evidence from C++.
- **Go 1.27, standard library only.** No cgo, no third-party modules in the library or its tests. Dev tooling run through `go run` (the pinned `benchstat` in the Makefile) is the only exception.
- **Recorded C++ results are generated, never edited by hand.** C++ v5.0.2's release build is the oracle for package `ondemand` and for the streams. `testdata/ondemand/oracle.jsonl` holds ~30,000 scripted reads (`TestOracle`); `testdata/stream/oracle.jsonl` holds ~3,000 streams through `parse_many`/`iterate_many` in one batch (`TestStreamOracle`). To change them, edit `scripts/ondemand-oracle/` and run `make oracle`.
- **`encoding/json/v2` is the oracle for data binding.** `Unmarshal` and `Marshal` must agree with the installed v2 on values, output bytes and error classes; the one allowed difference is listed in the binding spec (§1, §6).
- **The spec is the authority.** Read the spec before you change behaviour. If the code and the spec disagree, fix one of them in the same change.
  - [`2026-10-06-simdjson-go-core-design.md`](docs/superpowers/specs/2026-10-06-simdjson-go-core-design.md): parser, DOM
  - [`2026-10-07-simdjson-go-ondemand-design.md`](docs/superpowers/specs/2026-10-07-simdjson-go-ondemand-design.md): package `ondemand`
  - [`2026-10-07-simdjson-go-binding-design.md`](docs/superpowers/specs/2026-10-07-simdjson-go-binding-design.md): data binding
  - [`2026-10-08-simdjson-go-streams-design.md`](docs/superpowers/specs/2026-10-08-simdjson-go-streams-design.md): document streams

## Layout

| Path | What |
|---|---|
| `internal/stage1/` | Stage 1: classify 64-byte blocks, find structural characters, validate UTF-8, `Minify` |
| `internal/stage1/kernel_generic.go` | Portable classifier; the reference the NEON kernel must equal |
| `internal/stage1/kernel_arm64.go` | NEON kernel (`//go:build arm64 && goexperiment.simd && !purego`) |
| `internal/stage1/kernel_fallback.go` | Portable kernel for every other build (the exact negation of the tag above) |
| `internal/stage1/stream.go` | `Stream`: stage 1 window by window, equal to one pass; finds the first bad byte (`Index` runs on it) |
| `internal/stream/` | The stream reader: C++ `document_stream` in one batch, window by window, stage 1 in a goroutine |
| `internal/jsonerr/` | The error sentinels and `StreamError`, shared by `simdjson` (which re-exports them) and `ondemand` |
| `internal/number/`, `internal/str/` | Float conversion (adapted from Go's `internal/strconv`, `LICENSE-GO`; `go generate` there rebuilds `pow10tab.go`) and terminator rules; string unescaping |
| `parser.go`, `tape.go`, `errors.go` | `Parser`, `Document`, tape tags, sentinel errors |
| `stage2.go`, `strings.go`, `numbers.go` | Stage 2: build the tape, unescape strings, parse numbers; in binding mode, record offsets and repeated names (`checkNames`); in streaming mode, parse one document of a stream |
| `stream.go`, `ondemand/stream.go` | `ParseMany`, `IterateMany` |
| `ondemand/` | On-Demand: `iter.go` and `valueiter.go` port C++'s `json_iterator` and `value_iterator`; `numbers.go` its typed number parsers; `document.go`, `value.go`, `object.go`, `array.go` the API |
| `scripts/ondemand-oracle/` | The C++ program and case generator behind `make oracle` |
| `testdata/ondemand/`, `testdata/stream/` | C++'s recorded results (`TestOracle`, `TestStreamOracle`) |
| `element.go`, `pointer.go`, `serialize.go` | DOM API, JSON Pointer, `AppendJSON` and `Minify` |
| `options.go`, `typeplan.go` | Data binding: options, the per-type codec cache, struct field plans (adapted from v2's `fields.go`) |
| `decode.go`, `encode.go`, `indent.go` | `Unmarshal`, `Marshal`/`MarshalAppend`, `MarshalIndent` |
| `methods.go`, `time.go`, `strcache.go` | `MarshalJSON`/`UnmarshalJSON`/text methods, `time.Time`, the string cache |
| `*_test.go` | Unit, corpus (`corpus_test.go`), fuzz (`fuzz_test.go`) and benchmark tests; data binding against v2 in `bind_test.go`, `bind_fuzz_test.go`, `bind_bench_test.go`; stage 2 binding mode in `binding_test.go` |
| `scripts/fetch-testdata.sh` | Downloads the pinned corpora (`make testdata`) |

## Commands

```sh
make test         # pure-Go build; fetches the corpora into testdata/ on first run
make test-neon    # NEON build (GOEXPERIMENT=simd)
make check        # gofmt, vet, tests on every build, race; must pass before committing
make fuzz target=FuzzParse time=60s       # any Fuzz* target; the Makefile picks its package and build
make bench neon=1 bench=Unmarshal/        # binding benchmarks sit beside their v2 equivalents
make oracle       # record C++'s results again (needs a C++20 compiler, curl, python3)
```

Add `short=1` to the test targets to skip `TestCountSaturation`, which allocates about 0.5 GB.

## When you change code

- Run `make check`. It runs gofmt, vet (including linux/386 and wasip1/wasm) and the tests on pure Go, `-tags purego`, NEON and amd64 (under Rosetta), and ends with a `-race` run (`make test-race`).
- **A stage 1 kernel change** must keep the NEON and portable kernels bit-identical. Run `make fuzz target=FuzzClassify` and `make fuzz target=FuzzUTF8`.
- **A parsing change** needs `make fuzz target=FuzzParse` (both builds) and the corpus tests passing.
- **An On-Demand change** needs `TestOracle` passing and `make fuzz target=FuzzOnDemand`. If C++'s behaviour is in doubt, add a case to `scripts/ondemand-oracle/gen.py` and run `make oracle`; differences between On-Demand and the DOM that C++ has too are listed in `knownDifference` (`ondemand/dom_test.go`).
- **A data binding change** needs `make fuzz target=FuzzUnmarshal` and `make fuzz target=FuzzMarshal`. If they fail, fix the binding: do not loosen `sameUnmarshal`/`sameMarshal` in `bind_test.go`.
- **A stream change** needs `TestStreamOracle`, `make fuzz target=FuzzParseMany` and `make fuzz target=FuzzIterateMany` (`FuzzStream`, in `internal/stage1`, runs on the NEON build). The batch size must never change results; do not loosen the fuzzers' comparison.
- **If a fuzzer fails, investigate the parser first.** Do not loosen the oracle in `fuzz_test.go` to make it pass. Each adjustment the oracle makes to `encoding/json` models a documented difference from C++.
- **Never skip the corpus tests.** They `t.Fatal` when `testdata/` is missing, and that's deliberate.
- `Parse` must not read past `len(b)`, must not keep or modify `b`, and must not allocate once the `Parser` has grown its buffers. `BenchmarkParse` reports allocs/op.

## Traps

- **Literal `\u` escapes in tests.** Test inputs such as `` `"é"` `` must keep the backslash. Some editors and tools decode them to `é`, which silently weakens the test. Check with `grep -c 'u00e9'`.
- **`archsimd` shift direction.** `x.ConcatShiftBytesRight(y, n)` treats `y` as the low half. The byte k positions before `in[i]` is `in.ConcatShiftBytesRight(prev, 16-k)[i]`.
- **Depth.** Empty `[]` and `{}` don't count toward `MaxDepth`, as in C++ (but they do in binding mode, as in v2). The parser and DOM never recurse per nesting level (`AppendJSON` and `AtPointer` walk the tape iteratively); keep it that way so a raised `MaxDepth` cannot overflow the stack. The binding codecs do recurse, as v2's do, bounded by v2's depth limit of 10,000.
- **Binding mode.** `Parser.binding` (set only by `Unmarshal`) makes stage 2 record input offsets and repeated names. Keep every binding-only step behind `if b.binding`, so plain `Parse` and its tape stay as C++ defines them.
- **Duplicate-name hashing.** `checkNames` (stage 2) hashes names with seeded `hash/maphash`. A cheaper unseeded or prefix/suffix hash lets crafted or ordinary input (same-length URLs, timestamps) make `Unmarshal` quadratic; `TestCollidingNames` guards it.
- **Benchmark noise.** Speed bars (binding spec §1) are ratios against v2 or against `main`; twitter `Unmarshal` sits near its 1.4× bar. On a loaded machine, compare in the same binary or alternate old/new test binaries run by run, and record `uptime`.
- **On-Demand ports C++'s release build.** C++'s debug-only checks (`SIMDJSON_DEVELOPMENT_CHECKS`) become `ErrOutOfOrderIteration`, but its end-of-input checks (`SIMDJSON_CHECK_EOF`) are off, as in release: past the last structural the iterator reads 0, like C++'s padding. Keep every index into the input bounds-safe; `FuzzOnDemand` runs arbitrary read sequences to catch panics.
- **Streams' decided point.** Documents are handed out only before the last boundary candidate (`Reader.Decided`), because C++ trims only after it. Stage 2 and On-Demand must not read past a candidate inside a document; the fuzzers check this.
- **Stream document lifetime.** `IterateMany` invalidates the document it yielded at the loop's next step, before `Reader.Compact` moves the indices the document views. `Iterate` keeps its own index buffer (`Parser.idx`), so a call inside the loop cannot overwrite the stream's.
- **Tape counts.** A container's element count saturates at 0xFFFFFF (`saturated`), and `Len()` then walks the tape. Read the count field only through `Element.exactCount`, which says whether it is exact.
