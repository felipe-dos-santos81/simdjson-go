# AGENTS.md

This file guides coding agents (and humans) working in this repo. Start with [`README.md`](README.md) for what the library is.

## Ground rules

- **C++ simdjson v5.0.2 is the oracle.** The tape format, the error returned for each bad input, and the edge cases must match C++. Some of those cases look like bugs but are intended, so don't "fix" them: the design spec lists them (§5.5, §9). Change parsing behaviour only together with evidence from C++.
- **Go 1.27, standard library only.** No cgo, no third-party modules in the library or its tests. Dev tooling run through `go run` (the pinned `benchstat` in the Makefile) is the only exception.
- **`encoding/json/v2` is the oracle for data binding.** `Unmarshal` and `Marshal` must agree with the installed v2 on values, output bytes and error classes; the one allowed difference is listed in the binding spec (§1, §6).
- **The spec is the authority.** Read [`docs/superpowers/specs/2026-10-06-simdjson-go-core-design.md`](docs/superpowers/specs/2026-10-06-simdjson-go-core-design.md) (parser, DOM) or [`docs/superpowers/specs/2026-10-07-simdjson-go-binding-design.md`](docs/superpowers/specs/2026-10-07-simdjson-go-binding-design.md) (data binding) before changing behaviour. If the code and the spec disagree, fix one of them in the same change.

## Layout

| Path | What |
|---|---|
| `internal/stage1/` | Stage 1: classify 64-byte blocks, find structural characters, validate UTF-8, `Minify` |
| `internal/stage1/kernel_generic.go` | Portable classifier; the reference the NEON kernel must equal |
| `internal/stage1/kernel_arm64.go` | NEON kernel (`//go:build arm64 && goexperiment.simd && !purego`) |
| `internal/stage1/kernel_fallback.go` | Portable kernel for every other build (the exact negation of the tag above) |
| `stage2.go`, `strings.go`, `numbers.go` | Stage 2: build the tape, unescape strings, parse numbers |
| `fastfloat.go`, `pow10tab.go` | Decimal→float64 conversion adapted from Go's `internal/strconv` (BSD, `LICENSE-GO`); regenerate the table with `go generate` (`pow10gen.go`) |
| `parser.go`, `tape.go`, `errors.go` | `Parser`, `Document`, tape tags, sentinel errors |
| `element.go`, `pointer.go`, `serialize.go` | DOM API, JSON Pointer, `AppendJSON` and `Minify` |
| `options.go`, `typeplan.go` | Data binding: options, the per-type codec cache, struct field plans (adapted from v2's `fields.go`) |
| `decode.go`, `encode.go`, `indent.go` | `Unmarshal`, `Marshal`/`MarshalAppend`, `MarshalIndent` |
| `methods.go`, `time.go`, `strcache.go` | `MarshalJSON`/`UnmarshalJSON`/text methods, `time.Time`, the string cache |
| `*_test.go` | Unit, corpus (`corpus_test.go`), fuzz (`fuzz_test.go`) and benchmark tests; data binding against v2 in `bind_test.go`, `bind_fuzz_test.go`, `bind_bench_test.go` |
| `scripts/fetch-testdata.sh` | Downloads the pinned corpora (`make testdata`) |

## Commands

```sh
make test         # pure-Go build; fetches the corpora into testdata/ on first run
make test-neon    # NEON build (GOEXPERIMENT=simd)
make check        # gofmt, vet and tests on every build; must pass before committing
make fuzz target=FuzzParse time=60s
make bench neon=1
```

Add `short=1` to the test targets to skip `TestCountSaturation`, which allocates about 0.5 GB.

## When you change code

- Run `make check`. It runs gofmt, vet (including linux/386 and wasip1/wasm) and the tests on pure Go, `-tags purego`, NEON and amd64 (under Rosetta).
- **A stage 1 kernel change** must keep the NEON and portable kernels bit-identical. Run `make fuzz target=FuzzClassify` and `make fuzz target=FuzzUTF8`.
- **A parsing change** needs `make fuzz target=FuzzParse` (both builds) and the corpus tests passing.
- **A data binding change** needs `make fuzz target=FuzzUnmarshal` and `make fuzz target=FuzzMarshal`. If they fail, fix the binding: do not loosen `sameUnmarshal`/`sameMarshal` in `bind_test.go`.
- **If a fuzzer fails, investigate the parser first.** Do not loosen the oracle in `fuzz_test.go` to make it pass. Each adjustment the oracle makes to `encoding/json` models a documented difference from C++.
- **Never skip the corpus tests.** They `t.Fatal` when `testdata/` is missing, and that's deliberate.
- `Parse` must not read past `len(b)`, must not keep or modify `b`, and must not allocate once the `Parser` has grown its buffers. `BenchmarkParse` reports allocs/op.

## Traps

- **Literal `\u` escapes in tests.** Test inputs such as `` `"é"` `` must keep the backslash. Some editors and tools decode them to `é`, which silently weakens the test. Check with `grep -c 'u00e9'`.
- **`archsimd` shift direction.** `x.ConcatShiftBytesRight(y, n)` treats `y` as the low half. The byte k positions before `in[i]` is `in.ConcatShiftBytesRight(prev, 16-k)[i]`.
- **Depth.** Empty `[]` and `{}` don't count toward `MaxDepth`, as in C++ (but they do in binding mode, as in v2). The parser and DOM never recurse per nesting level (`AppendJSON` and `AtPointer` walk the tape iteratively); keep it that way so a raised `MaxDepth` cannot overflow the stack. The binding codecs do recurse, as v2's do, bounded by v2's depth limit of 10,000.
- **Binding mode.** `Parser.binding` (set only by `Unmarshal`) makes stage 2 record input offsets and repeated names. Keep every binding-only step behind `if b.binding`, so plain `Parse` and its tape stay as C++ defines them.
- **Tape counts.** A container's element count saturates at 0xFFFFFF (`saturated`), and `Len()` then walks the tape. Read the count field only through `Element.exactCount`, which says whether it is exact.
