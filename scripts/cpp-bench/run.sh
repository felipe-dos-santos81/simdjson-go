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
	dir=$(cd -P -- "$(dirname -- "$f")" >/dev/null && pwd) # fails here on a bad directory
	files="$files $dir/$(basename -- "$f")"
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
BENCH_INPUTS=$out/gen go test -run '^TestWriteBenchInputs$' ./ondemand >&2
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
