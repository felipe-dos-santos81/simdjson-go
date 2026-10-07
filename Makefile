# Makefile for simdjson-go — Go port of the simdjson parser
# Targets follow the development loop:
#   testdata → fmt/vet → test (pure Go, purego, NEON, amd64) → fuzz → bench → check
SERVICE = simdjson-go

# Variables
GO = go
SIMD = GOEXPERIMENT=simd
CORPORA = testdata/jsonchecker testdata/jsonexamples
BENCHSTAT = $(GO) run golang.org/x/perf/cmd/benchstat@latest
target ?= FuzzParse
time ?= 60s
count ?= 6

# FuzzClassify and FuzzUTF8 compare the NEON kernel with the portable one, so
# they live in internal/stage1 and exist only in the GOEXPERIMENT=simd build.
NEON_FUZZ = FuzzClassify FuzzUTF8

.PHONY: help testdata clean \
        fmt vet \
        test test-purego test-neon test-amd64 \
        fuzz \
        bench benchstat \
        check

# ── Environment ──────────────────────────────────────────────────────────────

help: ## Print this help message
	@printf '\033[01;32m${SERVICE} — Go port of the simdjson parser\033[00;37m\n\n'
	@printf "\033[33mUsage:\033[0m\n  make [target] [arg=\"val\"...]\n\n\033[33mTargets:\033[0m\n"
	@grep -E '^[-a-zA-Z0-9_\.\/]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; \
		{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

testdata: ## Download the pinned simdjson-data corpora into testdata/ (skipped when present)
	@if [ ! -d testdata/jsonchecker ] || [ ! -d testdata/jsonexamples ]; then \
		./scripts/fetch-testdata.sh; \
	fi; \
	echo "Test corpora ready."

clean: ## Remove the downloaded corpora and the Go test cache
	rm -rf $(CORPORA)
	$(GO) clean -testcache
	@echo "Cleanup complete."

# ── Static checks ────────────────────────────────────────────────────────────

fmt: ## List files gofmt would change (fails if there are any)
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }

vet: ## Run go vet on the pure-Go and NEON builds
	$(GO) vet ./...
	$(SIMD) $(GO) vet ./...

# ── Tests ────────────────────────────────────────────────────────────────────

test: testdata ## Run the tests on the pure-Go build (short=1 skips the ~0.5 GB count-saturation test)
	$(GO) test $(if $(short),-short) ./...

test-purego: testdata ## Run the tests with the portable kernel forced (-tags purego)
	$(GO) test -tags purego $(if $(short),-short) ./...

test-neon: testdata ## Run the tests on the arm64 NEON kernel (GOEXPERIMENT=simd)
	$(SIMD) $(GO) test $(if $(short),-short) ./...

test-amd64: testdata ## Run the tests as amd64 (Rosetta 2 on Apple silicon)
	GOARCH=amd64 $(GO) test $(if $(short),-short) ./...

# ── Fuzzing ──────────────────────────────────────────────────────────────────

fuzz: ## Fuzz one target (usage: make fuzz target=FuzzParse time=60s [neon=1]); FuzzClassify/FuzzUTF8 always use NEON
	$(if $(or $(neon),$(filter $(target),$(NEON_FUZZ))),$(SIMD) )$(GO) test -run '^$$' -fuzz '^$(target)$$' -fuzztime $(time) \
		$(if $(filter $(target),$(NEON_FUZZ)),./internal/stage1/,.)

# ── Benchmarks ───────────────────────────────────────────────────────────────

bench: testdata ## Run benchmarks (usage: make bench [neon=1] [count=6] [bench=Parse/twitter] [out=file.txt])
	$(if $(neon),$(SIMD) )$(GO) test -run '^$$' -bench '$(or $(bench),.)' -count $(count) . \
		$(if $(out),> $(out) && cat $(out))

benchstat: ## Compare two benchmark runs (usage: make benchstat old=purego.txt new=neon.txt)
	$(BENCHSTAT) $(old) $(new)

# ── Development ──────────────────────────────────────────────────────────────

check: testdata ## Run the full build matrix (spec §8.4: gofmt, vet, every build) via scripts/check.sh
	./scripts/check.sh
