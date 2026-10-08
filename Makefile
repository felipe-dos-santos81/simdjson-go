# simdjson-go: a pure-Go port of simdjson (parser, DOM, On-Demand, streams) with
# encoding/json/v2-style binding.
# Builds: pure Go (default), NEON (GOEXPERIMENT=simd, arm64), portable only (-tags purego).
PROJECT = simdjson-go

GO        = go
SIMD      = GOEXPERIMENT=simd
# Dev tooling only, pinned; the library uses the standard library only.
BENCHSTAT = $(GO) run golang.org/x/perf/cmd/benchstat@v0.0.0-20260929162123-406019bb8b68
target ?= FuzzParse
time   ?= 60s
count  ?= 6

# The package of each fuzz target (the root package by default).
# The stage 1 targets compare the NEON and portable kernels, so they run on the NEON build.
STAGE1_FUZZ   = FuzzClassify FuzzUTF8 FuzzStream
ONDEMAND_FUZZ = FuzzOnDemand FuzzIterateMany
FUZZ_PKG = $(if $(filter $(target),$(STAGE1_FUZZ)),./internal/stage1/,$(if $(filter $(target),$(ONDEMAND_FUZZ)),./ondemand/,.))
FUZZ_ENV = $(if $(or $(neon),$(filter $(target),$(STAGE1_FUZZ))),$(SIMD))

.PHONY: help testdata clean fmt vet check \
        test test-neon test-purego test-amd64 test-race \
        fuzz bench benchstat bench-cpp oracle

# ── Setup ────────────────────────────────────────────────────────────────────

help: ## Show this help
	@printf '\033[01;32m${PROJECT}\033[00;37m\n\n'
	@printf "\033[33mUsage:\033[0m\n  make [target] [arg=\"val\"...]\n\n\033[33mTargets:\033[0m\n"
	@grep -E '^[-a-zA-Z0-9_\.\/]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; \
		{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

testdata: ## Download the pinned test corpora (once)
	@if [ ! -d testdata/jsonchecker ] || [ ! -d testdata/jsonexamples ]; then \
		./scripts/fetch-testdata.sh; \
	fi

clean: ## Delete the downloaded corpora
	rm -rf testdata/jsonchecker testdata/jsonexamples

# ── Checks ───────────────────────────────────────────────────────────────────

fmt: ## Fail if any file needs gofmt
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }

vet: ## Vet pure Go and NEON; type-check 386 and wasm
	$(GO) vet ./...
	$(SIMD) $(GO) vet ./...
	GOOS=linux GOARCH=386 $(GO) vet ./...
	GOOS=wasip1 GOARCH=wasm $(GO) vet ./...

check: fmt vet test test-purego test-neon test-amd64 test-race ## All checks; run before you commit

# ── Tests ────────────────────────────────────────────────────────────────────

test: testdata ## Pure-Go build [short=1]
	$(GO) test $(if $(short),-short) ./...

test-neon: testdata ## NEON build [short=1]
	$(SIMD) $(GO) test $(if $(short),-short) ./...

test-purego: testdata ## -tags purego [short=1]
	$(GO) test -tags purego $(if $(short),-short) ./...

test-amd64: testdata ## amd64 (Rosetta 2 on Apple silicon) [short=1]
	GOARCH=amd64 $(GO) test $(if $(short),-short) ./...

test-race: testdata ## Race detector, short mode
	$(GO) test -race -short ./...

# ── Fuzzing and benchmarks ───────────────────────────────────────────────────

fuzz: ## Fuzz one target in its package [target=FuzzParse time=60s neon=1]
	$(FUZZ_ENV) $(GO) test -run '^$$' -fuzz '^$(target)$$' -fuzztime $(time) $(FUZZ_PKG)

bench: testdata ## Benchmarks [bench=regex count=6 neon=1 pkg=./ondemand out=file]
	$(if $(neon),$(SIMD)) $(GO) test -run '^$$' -bench '$(or $(bench),.)' -count $(count) $(or $(pkg),.) \
		$(if $(out),> $(out) && cat $(out))

benchstat: ## Compare two benchmark files [old=a.txt new=b.txt]
	$(BENCHSTAT) $(old) $(new)

bench-cpp: testdata ## Go vs C++ simdjson v5.0.2, as a table (needs a C++20 compiler) [count=6 file="a.json b.json"]
	BENCHSTAT='$(BENCHSTAT)' ./scripts/cpp-bench/run.sh $(count) $(file)

oracle: testdata ## Record C++ simdjson's results in testdata/{ondemand,stream}/ (needs a C++20 compiler)
	./scripts/ondemand-oracle/regen.sh
