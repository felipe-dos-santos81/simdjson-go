# simdjson-go — Go port of the simdjson JSON parser.
# Builds: pure Go (default), NEON (GOEXPERIMENT=simd, arm64), portable forced (-tags purego).
SERVICE = simdjson-go

GO        = go
SIMD      = GOEXPERIMENT=simd
BENCHSTAT = $(GO) run golang.org/x/perf/cmd/benchstat@latest
target ?= FuzzParse
time   ?= 60s
count  ?= 6

# These fuzz targets compare the NEON kernel with the portable one, so they
# exist only in the NEON build, in internal/stage1.
NEON_FUZZ = FuzzClassify FuzzUTF8
FUZZ_ENV  = $(if $(or $(neon),$(filter $(target),$(NEON_FUZZ))),$(SIMD))
FUZZ_PKG  = $(if $(filter $(target),$(NEON_FUZZ)),./internal/stage1/,.)

.PHONY: help testdata clean fmt vet check \
        test test-neon test-purego test-amd64 \
        fuzz bench benchstat

# ── Setup ────────────────────────────────────────────────────────────────────

help: ## Show this help
	@printf '\033[01;32m${SERVICE} — Go port of the simdjson JSON parser\033[00;37m\n\n'
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

vet: ## go vet the pure-Go and NEON builds; type-check 32-bit and wasm
	$(GO) vet ./...
	$(SIMD) $(GO) vet ./...
	GOOS=linux GOARCH=386 $(GO) vet ./...
	GOOS=wasip1 GOARCH=wasm $(GO) vet ./...

check: fmt vet test test-purego test-neon test-amd64 ## Full build matrix (spec §8.4); run before committing

# ── Tests ────────────────────────────────────────────────────────────────────

test: testdata ## Test the pure-Go build [short=1]
	$(GO) test $(if $(short),-short) ./...

test-neon: testdata ## Test the NEON build [short=1]
	$(SIMD) $(GO) test $(if $(short),-short) ./...

test-purego: testdata ## Test with -tags purego [short=1]
	$(GO) test -tags purego $(if $(short),-short) ./...

test-amd64: testdata ## Test as amd64, via Rosetta 2 on Apple silicon [short=1]
	GOARCH=amd64 $(GO) test $(if $(short),-short) ./...

# ── Fuzzing and benchmarks ───────────────────────────────────────────────────

fuzz: ## Fuzz one target [target=FuzzParse time=60s neon=1]
	$(FUZZ_ENV) $(GO) test -run '^$$' -fuzz '^$(target)$$' -fuzztime $(time) $(FUZZ_PKG)

bench: testdata ## Run benchmarks [bench=regex count=6 neon=1 out=file]
	$(if $(neon),$(SIMD)) $(GO) test -run '^$$' -bench '$(or $(bench),.)' -count $(count) . \
		$(if $(out),> $(out) && cat $(out))

benchstat: ## Compare two benchmark files [old=a.txt new=b.txt]
	$(BENCHSTAT) $(old) $(new)
