#!/bin/sh
# Downloads the simdjson test corpora into testdata/ (gitignored), pinned to
# the simdjson-data commit used by C++ simdjson's dependencies/CMakeLists.txt.
set -eu
commit=351949906abde446f0314bf79606fb5d884f5be7
cd "$(dirname "$0")/.."
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/data.zip" "https://github.com/simdjson/simdjson-data/archive/$commit.zip"
unzip -q "$tmp/data.zip" -d "$tmp"
rm -rf testdata/jsonchecker testdata/jsonexamples
mkdir -p testdata
mv "$tmp/simdjson-data-$commit/jsonchecker" "$tmp/simdjson-data-$commit/jsonexamples" testdata/
echo "testdata/ ready"
