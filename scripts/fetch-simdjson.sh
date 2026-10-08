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
