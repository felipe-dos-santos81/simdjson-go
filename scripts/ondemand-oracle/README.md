# On-Demand oracle

`testdata/ondemand/oracle.jsonl` records what C++ simdjson v5.0.2's ondemand API
returns for about 30,000 scripted reads; `ondemand/oracle_test.go` replays them.

- `gen.py` writes the cases: a document (hex, or a corpus file) and a script.
- `oracle.cpp` runs each script through C++ ondemand, built in release mode
  (`-O2 -DNDEBUG`), and prints the output. Its header documents the script
  language; `runOps` in `oracle_test.go` mirrors it line for line.
- `regen.sh` (`make oracle`) builds `oracle.cpp` against the v5.0.2
  single-header release (downloaded, or from `$SIMDJSON_SINGLEHEADER`) and
  rewrites `oracle.jsonl`. It needs a C++20 compiler, curl, python3 and the
  corpora (`make testdata`).

Regenerate after changing `gen.py` or `oracle.cpp`; never edit `oracle.jsonl`
by hand.