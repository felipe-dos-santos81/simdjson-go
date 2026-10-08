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
python3 -I scripts/ondemand-oracle/gen.py >"$tmp/cases.jsonl"
"$tmp/oracle" <"$tmp/cases.jsonl" >"$tmp/out.jsonl"
mkdir -p testdata/ondemand testdata/stream
python3 -I - "$tmp/cases.jsonl" "$tmp/out.jsonl" <<'PY'
import json, sys
cases, outs = open(sys.argv[1], encoding="utf-8"), open(sys.argv[2], encoding="utf-8")
od = open("testdata/ondemand/oracle.jsonl", "w", encoding="utf-8")
st = open("testdata/stream/oracle.jsonl", "w", encoding="utf-8")
for c, o in zip(cases, outs, strict=True):
    c = json.loads(c)
    c["out"] = json.loads(o)
    (st if "stream" in c else od).write(json.dumps(c, ensure_ascii=False, sort_keys=True) + "\n")
PY
echo "testdata/ondemand/oracle.jsonl: $(wc -l <testdata/ondemand/oracle.jsonl) cases"
echo "testdata/stream/oracle.jsonl: $(wc -l <testdata/stream/oracle.jsonl) cases"
