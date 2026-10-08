# Benchmark Against C++ simdjson Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `make bench-cpp` runs simdjson-go and C++ simdjson v5.0.2 on the same inputs and prints a benchstat comparison and a Markdown table for the README.

**Architecture:** A C++ harness (`scripts/cpp-bench/bench.cpp`), built against the v5.0.2 single header, runs C++'s own benchmark bodies and prints Go benchmark lines under the Go benchmarks' names. `run.sh` builds both sides, runs them in alternating rounds and compares them with the pinned benchstat; `table.py` turns benchstat's CSV into Markdown and fails if a row is unpaired.

**Tech Stack:** Go 1.27 (standard library, NEON build), C++20 (Apple clang or any C++20 compiler), POSIX sh, python3, the pinned `benchstat` in the Makefile.

**Spec:** `docs/superpowers/specs/2026-10-08-simdjson-go-cpp-bench-design.md`

**Prototype:** every file below ran end to end before this plan was written (`count=1`, `twitter.json`, Apple M3 Max): 21 rows paired, one round took about 70 s. Transcribe the code exactly; do not "improve" it.

## Global Constraints

- Standard library only in Go; the C++ side and the scripts are dev tooling, like `make oracle` (spec §1, criterion 5).
- C++ simdjson **v5.0.2**, single-header release, built with `-std=c++20 -O3 -DNDEBUG -DSIMDJSON_THREADS_ENABLED=1 -pthread` (spec §2).
- The Go side is the NEON build (`GOEXPERIMENT=simd`).
- Each C++ row times exactly what the Go benchmark of the same name times; names match exactly, including the `-N` suffix (spec §5).
- The batch size never changes results; nothing here touches the library.
- `make check` must pass before each commit (`short=1` is allowed while iterating; the final task runs the full one).
- Commit messages end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Docs follow the repo's style: plain, short sentences (AGENTS.md, README.md).

## Review Focus

1. **A relative `file=` path** → it resolves against the directory `make` ran in (the repo root), and both sides get the same absolute path. Task 3, Step 5 runs `file=testdata/jsonexamples/twitter.json`.
2. **An invalid or missing input file** → the C++ harness prints the benchmark's name and exits 1, and `run.sh` stops (`set -e`); no partial table. Task 3, Step 6.
3. **A second run** → the result files are truncated, not appended: `n` stays `count`. Task 3, Step 7.
4. **No network** → `SIMDJSON_SINGLEHEADER=dir` copies the single header instead of downloading it, for `make oracle` and `make bench-cpp`. Task 2, Step 3.
5. **A renamed benchmark on one side** → `table.py` fails naming it, instead of printing a shorter table. Task 3, Step 2 (`--check`).

---

### Task 1: Go side — parse inputs, generated inputs, C++'s stream input

**Files:**
- Modify: `bench_test.go` (imports; `BenchmarkParse`; add `largeAmazon` before `BenchmarkParseMany`; its input list)
- Modify: `ondemand/bench_test.go` (imports; add `TestWriteBenchInputs` before `// --- results ---`; add `largeAmazon` before `BenchmarkIterateMany`; its input list)

**Interfaces:**
- Produces: `BENCH_FILES` (absolute paths separated by spaces) read by `BenchmarkParse`; sub-benchmark name `filepath.Base(path)`.
- Produces: `BENCH_INPUTS=dir go test -run '^TestWriteBenchInputs$' ./ondemand` writes `dir/kostya.json` and `dir/large_random.json`.
- Produces: `large_amazon_cellphones` input = C++'s `build_json(10*1024*1024)`.

- [ ] **Step 1: Apply this diff**

```diff
diff --git a/bench_test.go b/bench_test.go
index d470b35..ce8b943 100644
--- a/bench_test.go
+++ b/bench_test.go
@@ -3,6 +3,9 @@ package simdjson
 import (
 	"bytes"
 	"encoding/json"
+	"os"
+	"path/filepath"
+	"strings"
 	"testing"
 
 	"simdjson-go/internal/stage1"
@@ -11,9 +14,18 @@ import (
 var benchFiles = []string{"twitter.json", "citm_catalog.json", "canada.json", "github_events.json", "gsoc-2018.json", "update-center.json"}
 
 func BenchmarkParse(b *testing.B) {
-	for _, name := range benchFiles {
-		b.Run(name, func(b *testing.B) {
-			data := readTestdata(b, "jsonexamples", name)
+	paths := strings.Fields(os.Getenv("BENCH_FILES")) // set by scripts/cpp-bench/run.sh
+	if len(paths) == 0 {
+		for _, name := range benchFiles {
+			paths = append(paths, filepath.Join(testdataDir(b, "jsonexamples"), name))
+		}
+	}
+	for _, path := range paths {
+		b.Run(filepath.Base(path), func(b *testing.B) {
+			data, err := os.ReadFile(path)
+			if err != nil {
+				b.Fatal(err)
+			}
 			var p Parser
 			b.SetBytes(int64(len(data)))
 			b.ReportAllocs()
@@ -113,12 +125,23 @@ func amazonDOM(p *Parser, data []byte, out map[string]*brand) error {
 	return nil
 }
 
+// largeAmazon is C++'s large_amazon_cellphones input (build_json(10*1024*1024)
+// in benchmark/large_amazon_cellphones): the file, then copies of it without
+// its header line until it spans 10 MiB.
+func largeAmazon(small []byte) []byte {
+	out := bytes.Clone(small)
+	for rest := small[bytes.IndexByte(small, '\n')+1:]; len(out) < 10<<20; {
+		out = append(out, rest...)
+	}
+	return out
+}
+
 func BenchmarkParseMany(b *testing.B) {
 	small := readTestdata(b, "jsonexamples", "amazon_cellphones.ndjson")
 	for _, in := range []struct {
 		name string
 		data []byte
-	}{{"amazon_cellphones", small}, {"large_amazon_cellphones", bytes.Repeat(small, 40)}} {
+	}{{"amazon_cellphones", small}, {"large_amazon_cellphones", largeAmazon(small)}} {
 		for _, bs := range []struct {
 			name string
 			size int
diff --git a/ondemand/bench_test.go b/ondemand/bench_test.go
index c5c941d..50584ad 100644
--- a/ondemand/bench_test.go
+++ b/ondemand/bench_test.go
@@ -9,6 +9,7 @@ import (
 	"fmt"
 	"math/rand/v2"
 	"os"
+	"path/filepath"
 	"reflect"
 	"sync"
 	"testing"
@@ -65,6 +66,21 @@ func buildLargeRandom(n int) []byte {
 	return b.Bytes()
 }
 
+// TestWriteBenchInputs writes kostya.json and large_random.json, the
+// generated inputs of BenchmarkTasks, to $BENCH_INPUTS, for
+// scripts/cpp-bench/run.sh to give C++ the same bytes.
+func TestWriteBenchInputs(t *testing.T) {
+	dir := os.Getenv("BENCH_INPUTS")
+	if dir == "" {
+		t.Skip("set BENCH_INPUTS to a directory")
+	}
+	for name, data := range map[string][]byte{"kostya.json": kostyaJSON(), "large_random.json": largeRandomJSON()} {
+		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
+			t.Fatal(err)
+		}
+	}
+}
+
 // --- results ---
 
 type tweet struct {
@@ -472,6 +488,17 @@ func amazonOD(p *ondemand.Parser, data []byte, out map[string]*brand) error {
 	return nil
 }
 
+// largeAmazon is C++'s large_amazon_cellphones input (build_json(10*1024*1024)
+// in benchmark/large_amazon_cellphones): the file, then copies of it without
+// its header line until it spans 10 MiB.
+func largeAmazon(small []byte) []byte {
+	out := bytes.Clone(small)
+	for rest := small[bytes.IndexByte(small, '\n')+1:]; len(out) < 10<<20; {
+		out = append(out, rest...)
+	}
+	return out
+}
+
 func BenchmarkIterateMany(b *testing.B) {
 	small, err := os.ReadFile("../testdata/jsonexamples/amazon_cellphones.ndjson")
 	if err != nil {
@@ -480,7 +507,7 @@ func BenchmarkIterateMany(b *testing.B) {
 	for _, in := range []struct {
 		name string
 		data []byte
-	}{{"amazon_cellphones", small}, {"large_amazon_cellphones", bytes.Repeat(small, 40)}} {
+	}{{"amazon_cellphones", small}, {"large_amazon_cellphones", largeAmazon(small)}} {
 		for _, bs := range []struct {
 			name string
 			size int
```

- [ ] **Step 2: Check it builds and the defaults still run**

Run: `gofmt -l . && go vet . ./ondemand && go test -run '^$' -bench '^Benchmark(Parse|ParseMany)$' -benchtime 1x . && go test -run '^$' -bench '^BenchmarkIterateMany$' -benchtime 1x ./ondemand`
Expected: no gofmt output; `ok` for both packages; the Parse rows are the six names of `benchFiles`; four `ParseMany` and four `IterateMany` rows.

- [ ] **Step 3: Check `BENCH_FILES` and `BENCH_INPUTS`**

Run:
```sh
BENCH_FILES="$PWD/testdata/jsonexamples/twitter.json $PWD/testdata/jsonexamples/canada.json" \
  go test -run '^$' -bench '^BenchmarkParse$' -benchtime 1x . | grep Benchmark
d=$(mktemp -d) && BENCH_INPUTS=$d go test -run '^TestWriteBenchInputs$' -v ./ondemand | grep -E '^(---|ok)' && ls -l "$d" && rm -r "$d"
go test -run '^TestWriteBenchInputs$' -v ./ondemand | grep SKIP
```
Expected: exactly `BenchmarkParse/twitter.json-N` and `BenchmarkParse/canada.json-N`; `--- PASS: TestWriteBenchInputs`, `kostya.json` ≈ 153 MB and `large_random.json` ≈ 77 MB; without the variable, `--- SKIP`.

- [ ] **Step 4: Run the checks and commit**

Run: `make check short=1`
Expected: exit 0.

```bash
git add bench_test.go ondemand/bench_test.go
git commit -m "bench: inputs shared with the C++ comparison

BenchmarkParse reads BENCH_FILES; TestWriteBenchInputs writes the generated
task inputs; large_amazon_cellphones is now C++'s build_json input (copies
without the header line, 10 MiB) instead of 40 whole copies.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: One script fetches C++ simdjson

**Files:**
- Create: `scripts/fetch-simdjson.sh` (executable)
- Modify: `scripts/ondemand-oracle/regen.sh:1-18`

**Interfaces:**
- Produces: `scripts/fetch-simdjson.sh DIR` → `DIR/simdjson.h`, `DIR/simdjson.cpp` (v5.0.2), copied from `$SIMDJSON_SINGLEHEADER` when set, else downloaded.

- [ ] **Step 1: Create `scripts/fetch-simdjson.sh`**

```sh
#!/bin/sh
# Puts C++ simdjson v5.0.2's single-header release (simdjson.h and
# simdjson.cpp) in DIR: copied from $SIMDJSON_SINGLEHEADER when it is set,
# else downloaded. make oracle and make bench-cpp build against it.
set -eu
mkdir -p "$1"
for f in simdjson.h simdjson.cpp; do
	if [ -n "${SIMDJSON_SINGLEHEADER:-}" ]; then
		cp "$SIMDJSON_SINGLEHEADER/$f" "$1/$f"
	else
		curl -fsSL -o "$1/$f" "https://raw.githubusercontent.com/simdjson/simdjson/v5.0.2/singleheader/$f"
	fi
done
```

Run: `chmod +x scripts/fetch-simdjson.sh`

- [ ] **Step 2: Make `regen.sh` use it**

Replace lines 1–18 of `scripts/ondemand-oracle/regen.sh` (from `#!/bin/sh` to the `c++ …` line) with:

```sh
#!/bin/sh
# Regenerates testdata/ondemand/oracle.jsonl and testdata/stream/oracle.jsonl:
# builds oracle.cpp against the C++ simdjson v5.0.2 single-header release
# (scripts/fetch-simdjson.sh: downloaded, or taken from $SIMDJSON_SINGLEHEADER),
# runs gen.py's cases through it, and records the output, stream cases in their
# own file. Needs a C++20 compiler, curl, python3 and the corpora (make testdata).
set -eu
cd "$(dirname "$0")/../.."
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
scripts/fetch-simdjson.sh "$tmp"
c++ -std=c++20 -O2 -DNDEBUG -I"$tmp" -o "$tmp/oracle" scripts/ondemand-oracle/oracle.cpp "$tmp/simdjson.cpp"
```

The rest of the file (from `python3 -I scripts/ondemand-oracle/gen.py` on) stays.

- [ ] **Step 3: Check both ways of fetching, and that the oracle is unchanged**

Run:
```sh
d=$(mktemp -d) && scripts/fetch-simdjson.sh "$d/net" && grep -c 'SIMDJSON_VERSION "5.0.2"' "$d/net/simdjson.h"
SIMDJSON_SINGLEHEADER=$d/net scripts/fetch-simdjson.sh "$d/copy" && cmp "$d/net/simdjson.cpp" "$d/copy/simdjson.cpp" && echo copied
SIMDJSON_SINGLEHEADER=$d/net make oracle && git diff --exit-code testdata/ && echo unchanged
rm -r "$d"
```
Expected: `1`, `copied`, the two case counts, `unchanged`.

- [ ] **Step 4: Commit**

```bash
git add scripts/fetch-simdjson.sh scripts/ondemand-oracle/regen.sh
git commit -m "scripts: fetch C++ simdjson in one place

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: The harness, the run script, the table, `make bench-cpp`

**Files:**
- Create: `scripts/cpp-bench/bench.cpp`
- Create: `scripts/cpp-bench/table.py`
- Create: `scripts/cpp-bench/run.sh` (executable)
- Create: `scripts/cpp-bench/README.md`
- Modify: `Makefile` (`.PHONY`, the `bench-cpp` target after `benchstat`)
- Modify: `.gitignore`

**Interfaces:**
- Consumes: Task 1's `BENCH_FILES`, `BENCH_INPUTS`, `largeAmazon`; Task 2's `scripts/fetch-simdjson.sh`.
- Produces: `make bench-cpp [count=6] [file="a.json b.json"]` → `bench-cpp/{C++,Go,benchstat.txt,benchstat.csv,table.md,build/,gen/}`.

- [ ] **Step 1: Create `scripts/cpp-bench/table.py`**

```python
"""Prints run.sh's Markdown table from benchstat's CSV on stdin
(benchstat -table pkg -format csv -filter .unit:B/s C++ Go): the median
throughput of each benchmark on both sides and Go / C++, then the geometric
mean of the ratios. Exits 1 if a benchmark has a result on one side only.

python3 -I table.py --check runs it on a recorded sample."""

import csv
import math
import re
import sys


def table(lines):
    rows, unpaired = [], []
    for rec in csv.reader(lines):
        if len(rec) < 2 or rec[0] in ("", "geomean"):
            continue  # "pkg: …", headers, blank lines, benchstat's own geomean
        name = re.sub(r"-\d+$", "", rec[0])
        cpp, go = rec[1], rec[3] if len(rec) > 3 else ""
        if not cpp or not go:
            unpaired.append(name)
            continue
        rows.append((name, float(cpp), float(go)))
    if unpaired:
        raise ValueError("on one side only: " + ", ".join(unpaired))
    out = ["| Benchmark | C++ | Go | Go / C++ |", "|---|---|---|---|"]
    for name, cpp, go in rows:
        out.append(f"| `{name}` | {cpp / 2**20:.0f} MiB/s | {go / 2**20:.0f} MiB/s | {go / cpp:.2f}× |")
    geomean = math.exp(sum(math.log(go / cpp) for _, cpp, go in rows) / len(rows))
    out.append(f"| geomean | | | {geomean:.2f}× |")
    return "\n".join(out) + "\n"


SAMPLE = """pkg: simdjson-go
,C++,,Go,,,
,B/s,CI,B/s,CI,vs base,P
Parse/twitter.json-14,4.194304e+09,1%,1.048576e+09,0%,-75.00%,p=0.002 n=6
geomean,4.194304e+09,,1.048576e+09,,-75.00%,
"""


def check():
    want = (
        "| Benchmark | C++ | Go | Go / C++ |\n|---|---|---|---|\n"
        "| `Parse/twitter.json` | 4000 MiB/s | 1000 MiB/s | 0.25× |\n"
        "| geomean | | | 0.25× |\n"
    )
    got = table(SAMPLE.splitlines())
    assert got == want, got
    try:
        table((SAMPLE + "Parse/canada.json-14,,,9e+06,1%\n").splitlines())
    except ValueError as e:
        assert "Parse/canada.json" in str(e), e
    else:
        raise AssertionError("an unpaired row passed")
    print("ok")


if __name__ == "__main__":
    if sys.argv[1:] == ["--check"]:
        check()
    else:
        try:
            sys.stdout.write(table(sys.stdin))
        except ValueError as e:
            sys.exit(f"table.py: {e}")
```

- [ ] **Step 2: Run its check**

Run: `python3 -I scripts/cpp-bench/table.py --check`
Expected: `ok`

- [ ] **Step 3: Create `scripts/cpp-bench/bench.cpp`**

The bodies are v5.0.2's `benchmark/<task>/simdjson_{dom,ondemand}.h` (check against `git show v5.0.2:benchmark/<task>/simdjson_ondemand.h` in a simdjson clone if in doubt); only the result types are inlined and the stream bodies take the batch size.

```cpp
// bench runs C++ simdjson v5.0.2 on the inputs of simdjson-go's benchmarks
// and prints the results in Go's benchmark format, under the names of the Go
// benchmarks, for benchstat to compare (run.sh). The task bodies are copied
// from v5.0.2's benchmark/<task>/simdjson_{dom,ondemand}.h, which the Go
// benchmarks port.
//
// usage: bench CORPUS_DIR GEN_DIR FILE...
//
// CORPUS_DIR holds twitter.json and amazon_cellphones.ndjson, GEN_DIR the
// kostya.json and large_random.json that TestWriteBenchInputs writes, and each
// FILE is an input of BenchmarkParse.
#include "simdjson.h"

#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <exception>
#include <map>
#include <string>
#include <thread>
#include <vector>

using namespace simdjson;

// --- timing ---

static unsigned procs = std::thread::hardware_concurrency();

// bench times f as Go's b.Loop does with -benchtime 1s: one untimed run, then
// runs of n iterations, n growing, until one takes at least a second. It
// prints one line in Go's format; any error exits.
template <typename F>
static void bench(const std::string &name, size_t bytes, F f) {
  try {
    f();
    for (uint64_t n = 1;;) {
      auto t0 = std::chrono::steady_clock::now();
      for (uint64_t i = 0; i < n; i++) { f(); }
      double ns = std::chrono::duration<double, std::nano>(std::chrono::steady_clock::now() - t0).count();
      if (ns >= 1e9 || n >= 1000000000) {
        std::printf("Benchmark%s-%u\t%llu\t%.1f ns/op\t%.2f MB/s\n", name.c_str(), procs,
                    (unsigned long long)n, ns / n, double(bytes) * n / ns * 1e3);
        std::fflush(stdout);
        return;
      }
      // Go's predictNs: aim 20% past a second, grow at most 100x, at least 2x.
      uint64_t next = uint64_t(1.2e9 * n / (ns > 1 ? ns : 1));
      n = std::max(std::min(next, 100 * n), 2 * n);
    }
  } catch (const std::exception &e) {
    std::fprintf(stderr, "Benchmark%s: %s\n", name.c_str(), e.what());
    std::exit(1);
  }
}

static padded_string load(const std::string &path) {
  padded_string s;
  if (auto err = padded_string::load(path).get(s)) {
    std::fprintf(stderr, "%s: %s\n", path.c_str(), error_message(err));
    std::exit(1);
  }
  return s;
}

// --- results (benchmark/<task>/<task>.h) ---

struct twitter_user {
  uint64_t id{};
  std::string_view screen_name{};
};

struct tweet {
  std::string_view created_at{};
  uint64_t id{};
  std::string_view result{};
  uint64_t in_reply_to_status_id{};
  twitter_user user{};
  uint64_t retweet_count{};
  uint64_t favorite_count{};
};

struct top_tweet_result {
  int64_t retweet_count{};
  std::string_view screen_name{};
  std::string_view text{};
};

struct point {
  double x, y, z;
};

struct brand {
  double cumulative_rating;
  uint64_t reviews_count;
};

// --- partial_tweets ---

struct partial_tweets_ondemand {
  ondemand::parser parser{};
  simdjson_inline uint64_t nullable_int(ondemand::value value) {
    if (value.is_null()) { return 0; }
    return value;
  }
  simdjson_inline twitter_user read_user(ondemand::object user) {
    return { user.find_field("id"), user.find_field("screen_name") };
  }
  bool run(padded_string &json, std::vector<tweet> &result) {
    auto doc = parser.iterate(json);
    for (ondemand::object tweet : doc.find_field("statuses")) {
      result.emplace_back(::tweet{
        tweet.find_field("created_at"),
        tweet.find_field("id"),
        tweet.find_field("text"),
        nullable_int(tweet.find_field("in_reply_to_status_id")),
        read_user(tweet.find_field("user")),
        tweet.find_field("retweet_count"),
        tweet.find_field("favorite_count")
      });
    }
    return true;
  }
};

struct partial_tweets_dom {
  dom::parser parser{};
  simdjson_inline uint64_t nullable_int(dom::element element) {
    if (element.is_null()) { return 0; }
    return element;
  }
  bool run(padded_string &json, std::vector<tweet> &result) {
    for (dom::element tweet : parser.parse(json)["statuses"]) {
      auto user = tweet["user"];
      result.emplace_back(::tweet{
        tweet["created_at"],
        tweet["id"],
        tweet["text"],
        nullable_int(tweet["in_reply_to_status_id"]),
        { user["id"], user["screen_name"] },
        tweet["retweet_count"],
        tweet["favorite_count"]
      });
    }
    return true;
  }
};

// --- distinct_user_id ---

struct distinct_user_id_ondemand {
  ondemand::parser parser{};
  bool run(padded_string &json, std::vector<uint64_t> &result) {
    auto doc = parser.iterate(json);
    for (ondemand::object tweet : doc.find_field("statuses")) {
      result.push_back(tweet.find_field("user").find_field("id"));
      auto retweet = tweet.find_field("retweeted_status");
      if (!retweet.error()) {
        result.push_back(retweet.find_field("user").find_field("id"));
      }
    }
    return true;
  }
};

struct distinct_user_id_dom {
  dom::parser parser{};
  bool run(padded_string &json, std::vector<uint64_t> &result) {
    auto doc = parser.parse(json);
    for (dom::object tweet : doc["statuses"]) {
      result.push_back(tweet["user"]["id"]);
      auto retweet = tweet["retweeted_status"];
      if (retweet.error() != NO_SUCH_FIELD) {
        result.push_back(retweet["user"]["id"]);
      }
    }
    return true;
  }
};

// --- find_tweet ---

struct find_tweet_ondemand {
  ondemand::parser parser{};
  bool run(padded_string &json, uint64_t find_id, std::string_view &result) {
    auto doc = parser.iterate(json);
    for (auto tweet : doc.find_field("statuses")) {
      if (uint64_t(tweet.find_field("id")) == find_id) {
        result = tweet.find_field("text");
        return true;
      }
    }
    return false;
  }
};

struct find_tweet_dom {
  dom::parser parser{};
  bool run(padded_string &json, uint64_t find_id, std::string_view &result) {
    result = "";
    auto doc = parser.parse(json);
    for (auto tweet : doc["statuses"]) {
      if (uint64_t(tweet["id"]) == find_id) {
        result = tweet["text"];
        return true;
      }
    }
    return false;
  }
};

// --- top_tweet ---

struct top_tweet_ondemand {
  ondemand::parser parser{};
  bool run(padded_string &json, int64_t max_retweet_count, top_tweet_result &result) {
    result.retweet_count = -1;
    ondemand::value screen_name, text;
    auto doc = parser.iterate(json);
    for (auto tweet : doc["statuses"]) {
      auto tweet_text = tweet["text"];
      auto tweet_screen_name = tweet["user"]["screen_name"];
      int64_t retweet_count = tweet["retweet_count"];
      if (retweet_count <= max_retweet_count && retweet_count >= result.retweet_count) {
        result.retweet_count = retweet_count;
        text = std::move(tweet_text);
        screen_name = std::move(tweet_screen_name);
      }
    }
    result.screen_name = screen_name;
    result.text = text;
    return result.retweet_count != -1;
  }
};

struct top_tweet_dom {
  dom::parser parser{};
  bool run(padded_string &json, int64_t max_retweet_count, top_tweet_result &result) {
    result.retweet_count = -1;
    dom::element top_tweet{};
    auto doc = parser.parse(json);
    for (auto tweet : doc["statuses"]) {
      int64_t retweet_count = tweet["retweet_count"];
      if (retweet_count <= max_retweet_count && retweet_count >= result.retweet_count) {
        result.retweet_count = retweet_count;
        top_tweet = tweet;
      }
    }
    result.text = top_tweet["text"];
    result.screen_name = top_tweet["user"]["screen_name"];
    return result.retweet_count != -1;
  }
};

// --- kostya ---

struct kostya_ondemand {
  ondemand::parser parser{};
  bool run(padded_string &json, std::vector<point> &result) {
    auto doc = parser.iterate(json);
    for (ondemand::object point : doc.find_field("coordinates")) {
      result.emplace_back(::point{point.find_field("x"), point.find_field("y"), point.find_field("z")});
    }
    return true;
  }
};

struct kostya_dom {
  dom::parser parser{};
  bool run(padded_string &json, std::vector<point> &result) {
    for (auto point : parser.parse(json)["coordinates"]) {
      result.emplace_back(::point{point["x"], point["y"], point["z"]});
    }
    return true;
  }
};

// --- large_random ---

struct large_random_ondemand {
  ondemand::parser parser{};
  bool run(padded_string &json, std::vector<point> &result) {
    auto doc = parser.iterate(json);
    for (ondemand::object coord : doc) {
      result.emplace_back(::point{coord.find_field("x"), coord.find_field("y"), coord.find_field("z")});
    }
    return true;
  }
};

struct large_random_dom {
  dom::parser parser{};
  bool run(padded_string &json, std::vector<point> &result) {
    for (auto point : parser.parse(json)) {
      result.emplace_back(::point{point["x"], point["y"], point["z"]});
    }
    return true;
  }
};

// --- amazon_cellphones, with the batch size as a parameter ---

struct amazon_cellphones_dom {
  dom::parser parser{};
  bool run(padded_string &json, size_t batch_size, std::map<std::string, brand> &result) {
    parser.threaded = true;
    auto stream = parser.parse_many(json, batch_size);
    auto i = stream.begin();
    ++i;  // Skip first line
    for (; i != stream.end(); ++i) {
      auto doc = *i;
      std::string copy(std::string_view(doc.at(1)));
      auto x = result.find(copy);
      if (x == result.end()) {
        result.emplace(copy, brand{double(doc.at(5)) * uint64_t(doc.at(7)), uint64_t(doc.at(7))});
      } else {
        x->second.cumulative_rating += double(doc.at(5)) * uint64_t(doc.at(7));
        x->second.reviews_count += uint64_t(doc.at(7));
      }
    }
    return true;
  }
};

struct amazon_cellphones_ondemand {
  ondemand::parser parser{};
  bool run(padded_string &json, size_t batch_size, std::map<std::string, brand> &result) {
    parser.threaded = true;
    ondemand::document_stream stream = parser.iterate_many(json, batch_size);
    ondemand::document_stream::iterator i = stream.begin();
    ++i;  // Skip first line
    for (; i != stream.end(); ++i) {
      auto doc = *i;
      size_t index{0};
      std::string copy;
      double rating;
      uint64_t reviews;
      for (auto value : doc) {
        switch (index) {
        case 1: copy = std::string(std::string_view(value)); break;
        case 5: rating = double(value); break;
        case 7: reviews = uint64_t(value); break;
        default: break;
        }
        index++;
      }
      auto x = result.find(copy);
      if (x == result.end()) {
        result.emplace(copy, brand{rating * reviews, reviews});
      } else {
        x->second.cumulative_rating += rating * reviews;
        x->second.reviews_count += reviews;
      }
    }
    return true;
  }
};

// large_amazon_cellphones is C++'s build_json(10*1024*1024)
// (benchmark/large_amazon_cellphones/large_amazon_cellphones.h): the file,
// then copies of it without its header line until it spans 10 MiB.
static std::string large_amazon_cellphones(std::string answer) {
  std::string copy(answer, answer.find('\n') + 1);
  while (answer.size() < 10 * 1024 * 1024) { answer.append(copy); }
  return answer;
}

// --- main ---

// task runs one task body under BenchmarkTasks/<name>/<api>, clearing its
// result vector at the start of each run as Go reslices it.
template <typename Impl, typename T>
static void task(const std::string &name, padded_string &json) {
  Impl impl;
  std::vector<T> result;
  bench("Tasks/" + name, json.size(), [&] {
    result.clear();
    if (!impl.run(json, result)) { throw std::runtime_error("run failed"); }
  });
}

template <typename Impl>
static void stream(const std::string &name, padded_string &json) {
  for (auto [bs, size] : {std::pair<const char *, size_t>{"default", dom::DEFAULT_BATCH_SIZE}, {"single", json.size()}}) {
    Impl impl;
    std::map<std::string, brand> brands;
    bench(name + "/" + bs, json.size(), [&] { impl.run(json, size, brands); });
  }
}

int main(int argc, char **argv) {
  if (argc < 3) {
    std::fprintf(stderr, "usage: bench CORPUS_DIR GEN_DIR FILE...\n");
    return 2;
  }
  std::string corpus = argv[1], gen = argv[2];

  std::printf("pkg: simdjson-go\n");
  for (int i = 3; i < argc; i++) {
    std::string path = argv[i];
    padded_string json = load(path);
    dom::parser parser;
    bench("Parse/" + path.substr(path.find_last_of('/') + 1), json.size(), [&] {
      if (auto err = parser.parse(json).error()) { throw simdjson_error(err); }
    });
  }
  padded_string small = load(corpus + "/amazon_cellphones.ndjson");
  padded_string large(large_amazon_cellphones(std::string(small.data(), small.size())));
  for (auto *in : {&small, &large}) {
    std::string name = in == &small ? "amazon_cellphones" : "large_amazon_cellphones";
    stream<amazon_cellphones_dom>("ParseMany/" + name, *in);
  }

  std::printf("pkg: simdjson-go/ondemand\n");
  padded_string twitter = load(corpus + "/twitter.json");
  padded_string kostya = load(gen + "/kostya.json");
  padded_string large_random = load(gen + "/large_random.json");
  task<partial_tweets_ondemand, tweet>("partial_tweets/ondemand", twitter);
  task<partial_tweets_dom, tweet>("partial_tweets/dom", twitter);
  task<distinct_user_id_ondemand, uint64_t>("distinct_user_id/ondemand", twitter);
  task<distinct_user_id_dom, uint64_t>("distinct_user_id/dom", twitter);
  {
    find_tweet_ondemand od;
    find_tweet_dom dom;
    std::string_view text;
    bench("Tasks/find_tweet/ondemand", twitter.size(), [&] {
      if (!od.run(twitter, 505874901689851904ULL, text)) { throw std::runtime_error("not found"); }
    });
    bench("Tasks/find_tweet/dom", twitter.size(), [&] {
      if (!dom.run(twitter, 505874901689851904ULL, text)) { throw std::runtime_error("not found"); }
    });
  }
  {
    top_tweet_ondemand od;
    top_tweet_dom dom;
    top_tweet_result result;
    bench("Tasks/top_tweet/ondemand", twitter.size(), [&] {
      if (!od.run(twitter, 60, result)) { throw std::runtime_error("none found"); }
    });
    bench("Tasks/top_tweet/dom", twitter.size(), [&] {
      if (!dom.run(twitter, 60, result)) { throw std::runtime_error("none found"); }
    });
  }
  task<kostya_ondemand, point>("kostya/ondemand", kostya);
  task<kostya_dom, point>("kostya/dom", kostya);
  task<large_random_ondemand, point>("large_random/ondemand", large_random);
  task<large_random_dom, point>("large_random/dom", large_random);
  for (auto *in : {&small, &large}) {
    std::string name = in == &small ? "amazon_cellphones" : "large_amazon_cellphones";
    stream<amazon_cellphones_ondemand>("IterateMany/" + name, *in);
  }
  return 0;
}
```

- [ ] **Step 4: Create `scripts/cpp-bench/run.sh`, the Makefile target and the ignore rule**

```sh
#!/bin/sh
# Benchmarks simdjson-go against C++ simdjson v5.0.2 (make bench-cpp): builds
# bench.cpp and the Go test binaries (NEON), runs both COUNT times, taking
# turns, and compares them with benchstat (the command in $BENCHSTAT). The
# FILEs (default: the six files of BenchmarkParse) are the parse inputs of both
# sides. Writes bench-cpp/. Needs a C++20 compiler, curl, python3 and the
# corpora (make testdata).
#
# usage: run.sh COUNT [FILE...]
set -eu
: "${BENCHSTAT:?the benchstat command, set by make bench-cpp}"
count=$1
shift
files=
for f; do
	files="$files $(cd "$(dirname "$f")" && pwd)/$(basename "$f")"
done
cd "$(dirname "$0")/../.."
root=$(pwd)
out=$root/bench-cpp
corpus=$root/testdata/jsonexamples
if [ -z "$files" ]; then
	for f in twitter.json citm_catalog.json canada.json github_events.json gsoc-2018.json update-center.json; do
		files="$files $corpus/$f"
	done
fi

mkdir -p "$out/build" "$out/gen"
scripts/fetch-simdjson.sh "$out/build"
c++ -std=c++20 -O3 -DNDEBUG -DSIMDJSON_THREADS_ENABLED=1 -pthread -I"$out/build" \
	-o "$out/build/bench" scripts/cpp-bench/bench.cpp "$out/build/simdjson.cpp"
BENCH_INPUTS=$out/gen go test -run '^TestWriteBenchInputs$' ./ondemand >/dev/null
GOEXPERIMENT=simd go test -c -o "$out/build/root.test" .
GOEXPERIMENT=simd go test -c -o "$out/build/ondemand.test" ./ondemand

: >"$out/C++"
: >"$out/Go"
i=0
while [ "$i" -lt "$count" ]; do
	i=$((i + 1))
	echo "round $i of $count" >&2
	"$out/build/bench" "$corpus" "$out/gen" $files >>"$out/C++"
	BENCH_FILES=$files "$out/build/root.test" -test.run '^$' -test.bench '^Benchmark(Parse|ParseMany)$' >>"$out/Go"
	(cd ondemand && "$out/build/ondemand.test" -test.run '^$' -test.bench '^Benchmark(Tasks|IterateMany)$') >>"$out/Go"
done

cd "$out"
{
	uptime
	$BENCHSTAT -table pkg -filter '.unit:(sec/op OR B/s)' C++ Go
} >benchstat.txt
$BENCHSTAT -table pkg -filter '.unit:B/s' -format csv C++ Go >benchstat.csv
python3 -I "$root/scripts/cpp-bench/table.py" <benchstat.csv >table.md
cat benchstat.txt table.md
```

Run: `chmod +x scripts/cpp-bench/run.sh`

In `Makefile`, add `bench-cpp` to `.PHONY` (the line `fuzz bench benchstat oracle` becomes `fuzz bench benchstat bench-cpp oracle`) and add after the `benchstat` target:

```make
bench-cpp: testdata ## Go vs C++ simdjson v5.0.2, as a table (needs a C++20 compiler) [count=6 file="a.json b.json"]
	BENCHSTAT='$(BENCHSTAT)' ./scripts/cpp-bench/run.sh $(count) $(file)
```

In `.gitignore`, after the `*.out` line:

```
# make bench-cpp output
bench-cpp/
```

- [ ] **Step 5: Run it end to end with one file**

Run: `make bench-cpp count=1 file=testdata/jsonexamples/twitter.json`
Expected (about 70 s): benchstat's tables for `pkg: simdjson-go` and `pkg: simdjson-go/ondemand`, then `table.md`: 21 rows (`Parse/twitter.json`, 4 `ParseMany`, 12 `Tasks`, 4 `IterateMany`) plus `geomean`, every row with a C++ and a Go value. Exit 0.

- [ ] **Step 6: Check that a bad input stops the harness**

Run:
```sh
printf '{"a":' >bench-cpp/bad.json
bench-cpp/build/bench testdata/jsonexamples bench-cpp/gen bench-cpp/bad.json; echo "exit=$?"
bench-cpp/build/bench testdata/jsonexamples bench-cpp/gen bench-cpp/missing.json; echo "exit=$?"
rm bench-cpp/bad.json
```
Expected: `BenchmarkParse/bad.json: …` (a simdjson error such as `TAPE_ERROR` or `INCOMPLETE_ARRAY_OR_OBJECT`) then `exit=1`; `…/missing.json: …` then `exit=1`.

- [ ] **Step 7: Check that a second run does not append**

Run: `make bench-cpp count=1 file=testdata/jsonexamples/twitter.json >/dev/null && grep -c '^Benchmark' bench-cpp/C++ bench-cpp/Go`
Expected: `bench-cpp/C++:21` and `bench-cpp/Go:21`.

- [ ] **Step 8: Create `scripts/cpp-bench/README.md`**

````markdown
# C++ comparison

`make bench-cpp` runs simdjson-go and C++ simdjson v5.0.2 on the same inputs
and compares them. It needs a C++20 compiler, curl (or
`$SIMDJSON_SINGLEHEADER`), python3 and the corpora (`make testdata`).

- `bench.cpp` runs C++'s own benchmark bodies (v5.0.2's
  `benchmark/<task>/simdjson_{dom,ondemand}.h`) and prints Go benchmark lines
  under the names of the Go benchmarks: `BenchmarkParse`, `BenchmarkParseMany`
  (`bench_test.go`), `BenchmarkTasks` and `BenchmarkIterateMany`
  (`ondemand/bench_test.go`). Each row times the same work on the same bytes.
  It is built with `-O3 -DNDEBUG` and threads, as C++'s release build.
- `run.sh` builds `bench.cpp` and the Go test binaries (NEON build), runs them
  `count` times in turns, and compares them with the pinned benchstat.
  `kostya.json` and `large_random.json` are generated by Go
  (`TestWriteBenchInputs`), so that C++ parses the same bytes.
- `table.py` turns benchstat's CSV into a Markdown table and fails if a
  benchmark has a result on one side only. `python3 -I table.py --check`
  tests it.

```sh
make bench-cpp                                # the six BenchmarkParse files, count=6
make bench-cpp file="a.json b.json" count=10  # your own parse inputs
```

The results go to `bench-cpp/` (ignored by git): the raw `C++` and `Go`
files, `benchstat.txt` (with confidence intervals), `benchstat.csv` and
`table.md`.

If you change one of the four Go benchmarks, make the same change in
`bench.cpp`.
````

- [ ] **Step 9: Run the checks and commit**

Run: `make check short=1 && git status --short`
Expected: exit 0; `bench-cpp/` does not appear (ignored).

```bash
git add scripts/cpp-bench Makefile .gitignore
git commit -m "scripts: make bench-cpp compares simdjson-go with C++ simdjson

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: The full run and the docs

> **Status:** Step 3 and the README's Development line are done (ea6ce7a). Steps 1, 2 (the tables) and 4 wait for a run on a quiet machine; Step 5 follows them.

**Files:**
- Modify: `README.md` (Performance: the streams table and a new C++ table; Development)
- Modify: `AGENTS.md` (Layout, Commands, When you change code, Traps)
- Modify: `docs/superpowers/specs/2026-10-08-simdjson-go-cpp-bench-design.md` (Status, a Results section)

- [ ] **Step 1: Run the full comparison on a quiet machine**

Close other heavy programs. Run: `uptime && make bench-cpp`
Expected (about 10 minutes): exit 0; `bench-cpp/table.md` has 26 rows (6 `Parse`, 4 `ParseMany`, 12 `Tasks`, 4 `IterateMany`) plus `geomean`. If the load average is above about 3, wait and run again.

- [ ] **Step 2: Update `README.md`**

In Performance, replace the streams paragraph's input description and refresh its four numbers from `bench-cpp/benchstat.txt` (the Go column of the B/s tables, rows `ParseMany/large_amazon_cellphones/{single,default}` and `IterateMany/large_amazon_cellphones/{single,default}`; speed-up = default / single, two decimals). Also refresh the `top_tweet` cell of the On-Demand table (`Tasks/top_tweet/dom` sec/op ÷ `Tasks/top_tweet/ondemand` sec/op, Go column, two decimals): `domTopTweet` changed after it was measured.

```markdown
Streams on `large_amazon_cellphones` (C++'s 10 MiB build of `amazon_cellphones.ndjson`), one window against the default `BatchSize`:
```

Then add at the end of Performance:

```markdown
Against C++ simdjson v5.0.2 on the same machine and inputs (`make bench-cpp`: C++ built with `-O3` and threads, NEON Go build, median of 6 alternating runs):

<paste bench-cpp/table.md here>
```

In Development, after the `make bench … Tasks` line, add:

```sh
make bench-cpp   # Go vs C++ simdjson, as a table (needs a C++20 compiler)
```

- [ ] **Step 3: Update `AGENTS.md`**

In Layout, after the `scripts/ondemand-oracle/` row, add:

```markdown
| `scripts/cpp-bench/` | `make bench-cpp`: the C++ harness (C++'s own benchmark bodies, printed as Go benchmark lines), the run script and the table script |
| `scripts/fetch-simdjson.sh` | Gets C++ v5.0.2's single header for `make oracle` and `make bench-cpp` |
```

In Commands, after the `make oracle` line, add:

```sh
make bench-cpp    # Go vs C++ simdjson, benchstat and a Markdown table (needs a C++20 compiler)
```

In "When you change code", after the stream-change bullet, add:

```markdown
- **A benchmark change** to `BenchmarkParse`, `BenchmarkParseMany`, `BenchmarkTasks` or `BenchmarkIterateMany` needs the same change in `scripts/cpp-bench/bench.cpp`, so both sides keep timing the same work; `make bench-cpp` fails if a row loses its pair.
```

In Traps, at the end of the **Benchmark noise** bullet, add: `` `make bench-cpp` alternates C++ and Go rounds for the same reason. ``

- [ ] **Step 4: Record the results in the spec**

In the spec, set `- **Status:** Approved; implemented` and add before `## 9. Risks`:

```markdown
## Results

Measured on <machine> (`uptime` load <x>), `make bench-cpp` with count=6: Go runs at
<min>–<max>× of C++ (geomean <g>×). The table is in README.md (Performance).
```

Fill the placeholders from `bench-cpp/benchstat.txt` (first line) and `bench-cpp/table.md`.

- [ ] **Step 5: Run the full checks and commit**

Run: `make check`
Expected: exit 0.

```bash
git add README.md AGENTS.md docs/superpowers/specs/2026-10-08-simdjson-go-cpp-bench-design.md
git commit -m "docs: simdjson-go against C++ simdjson, measured

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
