#!/bin/sh
# Runs the build matrix of the design spec (§8.4). Needs testdata/ (scripts/fetch-testdata.sh).
set -eux
cd "$(dirname "$0")/.."
test -z "$(gofmt -l .)"
go vet ./...
GOEXPERIMENT=simd go vet ./...
go test ./...                       # pure Go
go test -tags purego ./...          # pure Go forced
GOEXPERIMENT=simd go test ./...     # NEON kernel on arm64
GOARCH=amd64 go test ./...          # pure Go on amd64 (Rosetta 2 on Apple silicon)
GOOS=linux GOARCH=386 go vet ./...  # 32-bit: type-check only
GOOS=wasip1 GOARCH=wasm go vet ./...
