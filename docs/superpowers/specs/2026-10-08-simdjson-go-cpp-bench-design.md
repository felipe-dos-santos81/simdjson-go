# simdjson-go — Benchmark against C++ simdjson — Design

- **Date:** 2026-10-08
- **Status:** Approved; implemented. The measured results (README tables, a Results section here)
  wait for a `make bench-cpp` run on a quiet machine: the first full run had a load average of
  about 19, so its numbers were not used. Until then, two README numbers are stale: the streams
  table (measured on the old input, 40 whole copies) and the `top_tweet` ratio of the On-Demand
  table (measured before `domTopTweet` changed to read the top tweet once, as C++ does).
- **Builds on:** the DOM (`docs/superpowers/specs/2026-10-06-simdjson-go-core-design.md`),
  On-Demand (`docs/superpowers/specs/2026-10-07-simdjson-go-ondemand-design.md`) and streams
  (`docs/superpowers/specs/2026-10-08-simdjson-go-streams-design.md`), and the benchmarks
  that already exist for them: `BenchmarkParse`, `BenchmarkParseMany` (`bench_test.go`),
  `BenchmarkTasks`, `BenchmarkIterateMany` (`ondemand/bench_test.go`)
- **Reference:** C++ simdjson v5.0.2, its benchmark bodies in
  `benchmark/<task>/simdjson_{dom,ondemand}.h` (tasks `partial_tweets`, `distinct_user_id`,
  `find_tweet`, `top_tweet`, `kostya`, `large_random`, `amazon_cellphones`)

## 1. Goal

One `make` target that runs simdjson-go and C++ simdjson v5.0.2 on the same inputs and prints a
table of their speed, row by row, for the README's Performance section. It covers the DOM parse,
the On-Demand tasks and the streams. It runs by hand, on a quiet machine; not in CI.

### Success criteria

1. **Same work on both sides.** Each row times the same operation on the same bytes: the C++
   body is copied from v5.0.2's benchmark suite, which the Go benchmark of the same name ports.
2. **Every row pairs.** The table has a C++ and a Go value on every row, or the run fails.
3. **Statistics.** `count` runs per side (default 6), alternated, compared by the pinned
   `benchstat` with confidence intervals and p-values.
4. **Your own files.** `file="a.json b.json"` replaces the default parse inputs.
5. **Nothing new in the library.** Standard library only; the C++ side and the scripts are dev
   tooling, like `make oracle`.

## 2. Decisions

| Decision | Choice | Why |
|---|---|---|
| Purpose | A reproducible table for the README | Run now and then; nothing stored over time |
| Scope | DOM parse, the six On-Demand tasks (both APIs), `ParseMany`, `IterateMany` | The library's whole speed story against C++ |
| C++ side | Our own harness built against the single header | C++'s own suite needs CMake and Google Benchmark, generates different `kostya`/`large_random` bytes, measures differently and names rows differently |
| Comparison | The harness prints Go benchmark lines with the Go names; the pinned `benchstat` compares | Statistics and pairing come for free; nothing new to maintain |
| Inputs | The six `BenchmarkParse` files by default, or `file=`; Go writes its generated inputs for C++ to read | Both sides parse identical bytes |
| `large_amazon_cellphones` | C++'s `build_json(10*1024*1024)`: the file, then copies without its header line until it spans 10 MiB; Go's two stream benchmarks change to it | Go repeated the whole file 40 times, so a header line sat in each copy: C++'s body throws on it (`INCORRECT_TYPE`), and Go's ignored the errors |
| Go build | NEON (`GOEXPERIMENT=simd`) | C++ uses its NEON kernel on arm64 |
| C++ build | `-std=c++20 -O3 -DNDEBUG -DSIMDJSON_THREADS_ENABLED=1 -pthread` | C++'s CMake release flags; threads turn on its pipelined streams. No `-march=native`: NEON is the arm64 baseline, and on amd64 simdjson picks its kernel at run time |

## 3. Interface

```sh
make bench-cpp                               # every benchmark, count=6
make bench-cpp file="a.json b.json" count=10 # your own parse inputs
```

Output goes to `bench-cpp/` (gitignored):

| File | What |
|---|---|
| `C++`, `Go` | The raw results, in Go's benchmark format; the names are benchstat's column labels |
| `benchstat.txt` | `uptime`, then benchstat's text tables (time and throughput): medians, confidence intervals, p-values |
| `benchstat.csv` | The throughput table as CSV, the input of `table.py` |
| `table.md` | The Markdown table for the README (§6) |
| `build/`, `gen/` | The C++ binary and Go test binaries; the generated inputs |

The script prints `benchstat.txt` and `table.md` at the end. It needs a C++20 compiler, curl,
python3 and the corpora (`make testdata`), as `make oracle` does.

## 4. Pieces

| Path | What |
|---|---|
| `scripts/fetch-simdjson.sh DIR` | Puts v5.0.2's `simdjson.h` and `simdjson.cpp` in DIR: copied from `$SIMDJSON_SINGLEHEADER` when set, else downloaded. `scripts/ondemand-oracle/regen.sh` uses it too, so the download exists once |
| `scripts/cpp-bench/bench.cpp` | The C++ harness (§5) |
| `scripts/cpp-bench/run.sh` | Builds both sides, runs them alternately, calls benchstat and `table.py` (§6) |
| `scripts/cpp-bench/table.py` | benchstat CSV to the Markdown table (§6) |
| `scripts/cpp-bench/README.md` | What it measures, the flags, the generated inputs, the outputs |
| `ondemand/bench_test.go` | `TestWriteBenchInputs`: skipped unless `BENCH_INPUTS=dir`; writes `kostya.json` and `large_random.json` there, the bytes `BenchmarkTasks` uses (`kostyaJSON()`, `largeRandomJSON()`) |
| `bench_test.go` | `BenchmarkParse` reads the paths in `BENCH_FILES` (separated by spaces, read with `os.ReadFile`) instead of `benchFiles` when it is set; the sub-benchmark name is the file's base name. `largeAmazon` builds the `large_amazon_cellphones` input (§2) |
| `ondemand/bench_test.go` (again) | The same `largeAmazon`, for `BenchmarkIterateMany` (another package, so a copy). `domTopTweet` keeps the top tweet and reads `text` and `screen_name` once, after the loop, as C++'s DOM body does (it read them on every new maximum) |
| `Makefile` | `bench-cpp: testdata ## Go vs C++ simdjson [count=6 file="a.json b.json"]` |

## 5. The C++ harness

`bench CORPUS_DIR GEN_DIR FILE...`: `CORPUS_DIR` holds `twitter.json` and
`amazon_cellphones.ndjson`; `GEN_DIR` holds the generated inputs; each `FILE` is a parse input.
One pass prints every benchmark once, in Go's format:

```
pkg: simdjson-go
BenchmarkParse/twitter.json-16  	    2000	    590000 ns/op	1070.33 MB/s
…
pkg: simdjson-go/ondemand
BenchmarkTasks/partial_tweets/ondemand-16	…
```

### 5.1 Rows

Each row times exactly what the Go benchmark of the same name times.

| Name | `pkg:` | C++ timed operation |
|---|---|---|
| `BenchmarkParse/<base name>` | `simdjson-go` | `dom::parser::parse(padded_string)`, one parser reused |
| `BenchmarkParseMany/{amazon_cellphones,large_amazon_cellphones}/{default,single}` | `simdjson-go` | `amazon_cellphones` DOM body (`parse_many`) |
| `BenchmarkTasks/<task>/{ondemand,dom}` | `simdjson-go/ondemand` | The task's `run()`; its result container cleared at the start of each run (Go reslices `out[:0]`) |
| `BenchmarkIterateMany/{amazon_cellphones,large_amazon_cellphones}/{default,single}` | `simdjson-go/ondemand` | `amazon_cellphones` On-Demand body (`iterate_many`) |

- Tasks: `partial_tweets`, `distinct_user_id`, `find_tweet`, `top_tweet` on `twitter.json`;
  `kostya` and `large_random` on the files in `GEN_DIR`.
- `large_amazon_cellphones` is built in memory as C++'s `build_json(10*1024*1024)` (§2), as in Go.
- Streams: `default` is C++'s default batch (1,000,000 bytes) with `threaded = true`, against
  Go's default `BatchSize` with its stage 1 goroutine. `single` is `batch_size = len(input)`,
  against Go's one window. The brand map lives across iterations on both sides, as it always
  did in Go; v5.0.2's runner clears it before each run, outside the timed body, and the keys
  stop changing after the first run, so the timed work is the same.
- Each input is copied into a `padded_string` once, outside the timed loop: Go's `Parse` needs
  no padding.
- The task bodies use C++ exceptions, as in C++'s suite.

### 5.2 Timing

The loop does what Go's `b.Loop()` does with `-benchtime 1s`: one untimed run (warms the buffers
and catches an error), then the iteration count grows as Go's `predictN` grows it (aim 20% past
a second, at most 100×; here at least 2×) until one timed run of `n` iterations takes at least
1 s. It prints `n`, the time per iteration in ns and the MB/s
(10⁶ bytes per second, as Go's `SetBytes`). The name's suffix `-N` is
`std::thread::hardware_concurrency()`, which is Go's default GOMAXPROCS, so the names match
exactly. Where the two differ (a container CPU limit, which Go respects), no row pairs and
`table.py` fails. Any error (a parse error, a `simdjson_result` error, an exception) prints the
benchmark's name and exits non-zero; an unreadable input file prints its path.

## 6. Run flow and table

`run.sh`, from the repo root:

0. **Paths.** The `FILE` arguments are made absolute before the script moves to the repo root,
   so a path relative to the caller's directory works.
1. **Build C++.** `fetch-simdjson.sh bench-cpp/build`, then compile `bench.cpp` with the flags
   of §2.
2. **Inputs.** `BENCH_INPUTS=bench-cpp/gen go test -run '^TestWriteBenchInputs$' ./ondemand`.
   The parse files are `file=` or `run.sh`'s default list: the six files of `benchFiles`. Both
   sides always get the same list as absolute paths (C++ as arguments, Go through
   `BENCH_FILES`), so the two lists cannot drift apart.
3. **Build Go.** `GOEXPERIMENT=simd go test -c` for `.` and `./ondemand`, once, into
   `bench-cpp/build`.
4. **Run, `count` rounds.** Each round runs the C++ binary once (appends to `C++`), then
   `root.test -test.run '^$' -test.bench '^Benchmark(Parse|ParseMany)$'` from the repo root and
   `ondemand.test -test.run '^$' -test.bench '^Benchmark(Tasks|IterateMany)$'` from `ondemand/`
   (append to `Go`). Alternating spreads machine noise over both sides.
5. **Compare,** from `bench-cpp/`: `benchstat -table pkg -filter '.unit:(sec/op OR B/s)' C++ Go`,
   after `uptime`, into `benchstat.txt`; `benchstat -table pkg -filter .unit:B/s -format csv C++ Go`
   into `benchstat.csv`, and `python3 -I table.py <benchstat.csv` into `table.md`. `-table pkg`
   keeps Go's `cpu:` line, which C++ does not print, from splitting the tables; the filter drops
   Go's B/op and allocs/op, which C++ does not report.

`table.py` reads only the B/s part of the CSV and prints:

| Benchmark | C++ | Go | Go / C++ |
|---|---|---|---|
| `Parse/twitter.json` | 1999 MiB/s | 1017 MiB/s | 0.51× |
| … | | | |
| geomean | | | 0.xx× |

Names lose `Benchmark` and the `-N` suffix; throughput is in MiB/s, as in the README's streams
table. **It exits non-zero if a row has a value on one side only**: a renamed or missing
benchmark fails the run instead of shortening the table.

## 7. Testing

- **Row pairing** is the one invariant that can break silently. `table.py`'s guard checks it on
  every real run.
- **`table.py --check`** runs the conversion on a short recorded benchstat CSV kept in the
  script: one paired row gives the expected Markdown, and an unpaired row fails.
- **End to end:** `make bench-cpp count=1` on the development machine (the plan's last task)
  checks the C++ build, the pairing of every name, `BENCH_FILES` and `file=`.
- **Not tested:** `BENCH_FILES` (trivial), `TestWriteBenchInputs` (a tool). That C++ and Go
  compute the same task results is not checked: both bodies come from the same C++ source, and
  `TestTasks` checks Go's On-Demand against its DOM. Add a check if the table shows a
  suspicious gap.
- No CI job: the comparison needs a C++ compiler and a quiet machine.

## 8. Docs

- `README.md`: the streams table's input becomes C++'s 10 MiB build (re-measured in the same
  run); a "Against C++ simdjson v5.0.2" table in Performance, from a real run (machine,
  `count`); `make bench-cpp` under Development.
- `AGENTS.md`: layout rows for `scripts/cpp-bench/` and `scripts/fetch-simdjson.sh`; the command;
  in the Benchmark-noise trap, that the comparison alternates its runs; under "When you change
  code", that a change to one of the four Go benchmarks needs the same change in `bench.cpp`.
- `.gitignore`: `bench-cpp/`.
- The streams spec (§8, Benchmarks) names the new `large_amazon_cellphones` input.

## Prototype

Before the plan, the harness, `run.sh`, `table.py` and the Go changes ran end to end
(`count=1`, `twitter.json`) on an Apple M3 Max: all 21 rows paired (`-14` on both sides), one
round took about 70 s, and Go ran at 0.23–0.40× of C++ (geomean 0.29×).

## 9. Risks

- **The C++ bodies drift from the Go ports.** The Go ports follow v5.0.2's suite; the harness
  copies the same files at the same tag. A change to a Go task needs the same change in
  `bench.cpp`.
- **Noise.** A loaded machine skews both sides; alternating rounds and benchstat's intervals show
  it, and `uptime` is recorded.
- **Exceptions or threads unavailable** in an unusual toolchain: the build fails loudly.
