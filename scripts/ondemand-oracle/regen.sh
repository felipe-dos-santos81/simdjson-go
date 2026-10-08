#!/bin/sh
# Regenerates testdata/ondemand/oracle.jsonl: builds oracle.cpp against the
# C++ simdjson v5.0.2 single-header release (downloaded, or taken from
# $SIMDJSON_SINGLEHEADER), runs gen.py's cases through it, and records the
# output. Needs a C++20 compiler, curl, python3 and the corpora (make testdata).
set -eu
cd "$(dirname "$0")/../.."
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
src=${SIMDJSON_SINGLEHEADER:-}
if [ -z "$src" ]; then
	src=$tmp
	for f in simdjson.h simdjson.cpp; do
		curl -fsSL -o "$tmp/$f" "https://raw.githubusercontent.com/simdjson/simdjson/v5.0.2/singleheader/$f"
	done
fi
c++ -std=c++20 -O2 -DNDEBUG -I"$src" -o "$tmp/oracle" scripts/ondemand-oracle/oracle.cpp "$src/simdjson.cpp"
python3 -I scripts/ondemand-oracle/gen.py >"$tmp/cases.jsonl"
"$tmp/oracle" <"$tmp/cases.jsonl" >"$tmp/out.jsonl"
mkdir -p testdata/ondemand
python3 -I - "$tmp/cases.jsonl" "$tmp/out.jsonl" >testdata/ondemand/oracle.jsonl <<'PY'
import json, sys
cases, outs = open(sys.argv[1], encoding="utf-8"), open(sys.argv[2], encoding="utf-8")
for c, o in zip(cases, outs, strict=True):
    c = json.loads(c)
    c["out"] = json.loads(o)
    sys.stdout.write(json.dumps(c, ensure_ascii=False, sort_keys=True) + "\n")
PY
echo "testdata/ondemand/oracle.jsonl: $(wc -l <testdata/ondemand/oracle.jsonl) cases"