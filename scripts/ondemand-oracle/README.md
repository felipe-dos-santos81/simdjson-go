# On-Demand oracle

`testdata/ondemand/oracle.jsonl` records what C++ simdjson v5.1.1's ondemand API
returns for about 30,000 scripted reads; `ondemand/oracle_test.go` replays them.
`testdata/stream/oracle.jsonl` records what its `parse_many` and `iterate_many`
return for about 3,000 streams read in one batch;
`ondemand/stream_oracle_test.go` replays them through `ParseMany` and
`IterateMany`.

- `gen.py` writes the cases: a document (hex, or a corpus file) and a script.
- `oracle.cpp` runs each script through C++ ondemand, built in release mode
  (`-O2 -DNDEBUG`), and prints the output. Its header documents the script
  language; `runOps` in `oracle_test.go` mirrors it line for line.
- `regen.sh` (`make oracle`) builds `oracle.cpp` against the v5.1.1
  single-header release (downloaded, or from `$SIMDJSON_SINGLEHEADER`) and
  rewrites both files. It needs a C++20 compiler, curl, python3 and the
  corpora (`make testdata`).

Regenerate after changing `gen.py` or `oracle.cpp`; never edit `oracle.jsonl`
by hand.

## The development-build reference

C++ checks nesting depth in On-Demand only in its development build
(`SIMDJSON_DEVELOPMENT_CHECKS`), and `oracle.cpp` is built in release mode, so
the recorded oracle cannot hold those results. `TestMaxDepth`
(`ondemand/regress_test.go`) holds them instead. To measure them again:

1. Copy `oracle.cpp` to a scratch directory outside the repository. Before
   `parser.iterate(json)`, call
   `parser.allocate(std::max<size_t>(input.size(), 64), depth)`; before
   `iterate_many`, call `od_stream_parser.allocate(batch, depth)`. The second
   argument of `allocate` is the limit, Go's `Parser.MaxDepth`; read it from
   an environment variable, defaulting to `DEFAULT_MAX_DEPTH` (1024).
2. Get the v5.1.1 single header with `scripts/fetch-simdjson.sh DIR` (it
   copies from `$SIMDJSON_SINGLEHEADER` when that is set) and build without
   `-DNDEBUG`, so the asserts stay on:
   `c++ -std=c++20 -O1 -DSIMDJSON_DEVELOPMENT_CHECKS=1 -IDIR -o oracle-dev oracle.cpp DIR/simdjson.cpp`
3. Feed it case lines as `gen.py` writes them, one process per case: a
   development check can abort, and a crash is a result. Compare the output
   with `TestMaxDepth`'s `want`.
