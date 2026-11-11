# Upstream sync: meta-prompt

Give this file to an agent at the repository root to plan a sync with
[C++ simdjson](https://github.com/simdjson/simdjson). Optional input: `TARGET`, an upstream
release tag. The agent writes one file: a ledger of every upstream commit in the range and one
porting prompt per group of commits that the Go port must follow. Other agents run those
prompts.

---

You are planning the port of upstream simdjson changes into simdjson-go. Your deliverable is
`docs/upstream/<BASE>..<TARGET>.md`: the **ledger** and the **porting prompts**. That file is
the only one you create in this repository. Everything else you build or download goes in a
scratch directory outside it.

Read [`AGENTS.md`](../AGENTS.md) first. Its ground rules bind you and every prompt you write:
C++ is the oracle, and parsing behaviour changes only together with evidence from C++.

## Steps

### 1. Fix the range

- `BASE` is the tag in `scripts/fetch-simdjson.sh` (the download URL and the
  `SIMDJSON_VERSION` check). The core spec names a local clone a few commits past that tag;
  the range still starts at the tag.
- `TARGET` is the tag you were given, else the latest release:
  `gh release list -R simdjson/simdjson --limit 1`.
- `TARGET` is a release tag. `scripts/fetch-simdjson.sh` downloads
  `singleheader/simdjson.{h,cpp}`, and upstream regenerates those files only in release
  commits, so the recorded oracle can be pinned only to a release. Count the commits on
  `master` past the last release and report them as "unreleased, next sync".

Done when you have stated `BASE` and `TARGET` with their commit SHAs. If they are equal,
report that there is nothing to sync and stop.

### 2. Get the delta

Clone upstream into a new, empty scratch directory and read from the clone. Run nothing from
it.

```sh
git clone --filter=blob:none --no-checkout https://github.com/simdjson/simdjson "$up"
git -C "$up" rev-list --count BASE..TARGET
git -C "$up" log --reverse --format='%h %ad %s' --date=short BASE..TARGET
git -C "$up" diff --name-only BASE TARGET -- src include benchmark dependencies
git -C "$up" show --stat --format= <sha>     # files one commit touches
git -C "$up" log -1 --format=%b <sha>        # why; often carries the failing input
gh release view <tag> -R simdjson/simdjson --json body -q .body
```

The commit list is the index. Release notes are generated lists of pull requests, and some
commits belong to no pull request; use the notes to label commits with their release.

Done when your list has exactly `git rev-list --count BASE..TARGET` commits.

### 3. Triage every commit

Give each commit one **verdict**. Read the diff of every commit that touches a path in the
[path map](#path-map); the subject line is enough only for commits that touch none.

| Verdict | The commit… | Prompt |
|---|---|---|
| `port` | makes C++ return a different value, tape, error code, accepted or rejected input, iteration result or document boundary, in code with a Go owner | yes |
| `verify` | fixes a C++ fault that Go cannot have in the same form (a crash, a read past the buffer), and so changes what C++ returns for some input, in code with a Go owner. Go may already return the fixed result | yes: record the case, fix only if it is red |
| `perf` | keeps every result and does less work, in code with a Go owner | yes when the Go owner has the same shape; else a ledger note that says why |
| `ask` | adds public API or an option inside a ported area, or changes something the specs exclude that the maintainer may now want | no: list it for the maintainer |
| `none` | touches only code with no Go owner | no |

- A commit that fits several verdicts takes the first that applies, in the table's order.
  Its prompt names the parts left out.
- A ported file can hold functions the port leaves out. The On-Demand spec §2, the core spec
  §3 and the streams spec §4.3 list them. A change to one of those is `none` or `ask`.
- Check each commit against the **quirk records**, the places where this repository writes
  down C++ behaviour it copies or declines. Start with core spec §5.5 and §9, On-Demand spec
  §4, streams spec §4.3, `knownDifference` in `ondemand/dom_test.go`, and the adjustments
  `fuzz_test.go` makes to `encoding/json`. Then search the specs and the test files for the
  C++ identifiers and error codes the commit touches: a test helper that models a difference
  is a record too. When upstream fixes a recorded quirk, the Go port follows C++ and the
  record changes in the same package.
- A commit that changes no Go result but makes a record stale is `none`. Put the edit to the
  record in the pin package.
- For each `port` and `verify` commit, collect the inputs that show the change: from the tests
  the commit adds and from its body. Copy them byte for byte.

Done when every commit has one verdict and a one-line reason, and every path printed by the
`git diff --name-only` command in step 2 is either in the path map, in the
[no Go owner](#no-go-owner) list, or in a table of unmapped paths with your reading of each.

### 4. Measure

Find out what C++ at `TARGET` returns, and where Go disagrees. This step needs a C++20
compiler, `curl` and `python3`. If one is missing, say so in the ledger and mark every prompt
"unmeasured".

1. Add a throwaway git worktree in the scratch directory and copy the corpora under
   `testdata/` into it. Run `make oracle` there unchanged: `git diff --quiet testdata/` must
   pass, which proves your toolchain reproduces the committed files.
2. Change the tag and the version check in `scripts/fetch-simdjson.sh` to `TARGET` and run
   `make oracle` again. The changed cases are the diff of the two oracle files. Go passes
   both oracle tests at `BASE`, so these are also the cases Go fails; the tests print only
   the first 20 failures, so use them to confirm the count.
3. Attach every changed case to the commit that explains it. When there are too many to
   attribute by reading, build `oracle.cpp` against the upstream source tree at each commit
   that touches `include/` or `src/` (`-Iinclude -Isrc … src/simdjson.cpp` gives the same
   output as the single header) and find where each case changes. A case that no commit
   explains is a triage miss: return to step 3.
4. For each collected input that the recorded cases miss, get C++'s result at `BASE` and at
   `TARGET`. On-Demand and stream inputs go through `scripts/ondemand-oracle/oracle.cpp`;
   its header documents the script language. A script ends at the first error and cannot
   set a batch size, threads or development checks, so those reads go through a scratch
   program. The DOM and stages 1 and 2 have no recorded oracle: run their inputs through
   `dom::parser` in a scratch program. Run one process per input, because C++ at `BASE` may
   crash; a crash is a result.
5. Run the same inputs through Go at `HEAD`. A scratch `_test.go` in the worktree's
   `ondemand/` can call the oracle tests' own runners (`runScript`, `streamOut`).
6. Remove the worktree.

Done when every `port` and `verify` commit has at least one input with three results beside
it: C++ at `BASE`, C++ at `TARGET`, Go today. A commit with no input that separates `BASE`
from `TARGET` becomes `none` or `perf`, with that finding as its reason. Where Go already
disagrees with C++ at `BASE`, list it as an `ask` item: it is a fault of the port, older
than this sync.

### 5. Group and order

Group commits into **packages**: one behaviour, one Go owner, one prompt. Order them so that
each package builds on finished work:

1. **Pin.** Move every pin to `TARGET` and regenerate the oracles. The pins are
   `scripts/fetch-simdjson.sh`, the simdjson-data commit in `scripts/fetch-testdata.sh` (it
   follows upstream's `dependencies/CMakeLists.txt`), and every version string that
   `rg -n '<BASE without the v>'` finds outside `testdata/` and `docs/superpowers/plans/`.
2. Stage 1, then stage 2 and the DOM, then On-Demand, then streams.
3. `perf` packages.
4. **Close.** Compare `scripts/cpp-bench/bench.cpp` with `TARGET`'s
   `benchmark/<task>/simdjson_{dom,ondemand}.h`, then run `make check` and `make bench-cpp`.

The sync lands as one branch. After the pin package, the only failing tests are the oracle
cases the ledger assigns to later packages; each prompt lists the cases it turns green.
Name a case by its document and script, because line numbers move when `gen.py` grows. A
case that two commits change turns green with the later package. `make check` passes at the
close.

Done when every `port`, `verify` and prompt-worthy `perf` commit sits in exactly one package.

### 6. Write the prompts

Each prompt is read by an agent that has this repository and nothing else. Use this shape:

```markdown
## Package N: <behaviour, in a few words>

Read `AGENTS.md` first. This is a <stage 1 kernel | parsing | On-Demand | stream | benchmark> change.
<Name every kind the package's Go files fall under. The pin and close packages name none.>

**Upstream:** <short SHA> (#PR), … from simdjson <BASE>..<TARGET>
**C++ before and after:** <one paragraph>
**Inputs:**
| Input (bytes, verbatim) | Read (script, format or API) | C++ <BASE> | C++ <TARGET> | Go today |
**Go owner:** <files and functions, confirmed by reading them>
**Change:** <what Go must do after this package, stated as behaviour>
**Evidence to add:** <cases for `scripts/ondemand-oracle/gen.py`, or a ported test for the DOM>
**Spec and quirk records:** <the sections to rewrite, or "none">
**Left out:** <parts of these commits with no Go owner>
**Done when:** <the oracle cases that turn green; the checks `AGENTS.md` asks for this kind of change>
```

State the change as behaviour and leave the Go code to the agent that ports it. Name the
kind of change so the agent finds its checks in `AGENTS.md`.

Done when every package has a prompt with every field filled, and every input in a prompt
carries its three results or the word "unmeasured".

### 7. Assemble and report

Write `docs/upstream/<BASE>..<TARGET>.md`:

1. Range: `BASE`, `TARGET`, their SHAs, the commit count, the count of unreleased commits,
   and the table of unmapped paths.
2. Ledger: `| SHA | PR | Release | Subject | Verdict | Reason | Go owner | Package |`, one
   row per commit, in upstream order.
3. `ask` items, for the maintainer.
4. The prompts, in package order.

Then report in chat: the counts per verdict, the `ask` items, and everything unmeasured.

## Evidence

Ranked by how much it proves:

1. **C++ built from `TARGET`'s single header and run on the input.** This is proof.
2. **The upstream diff and the tests the commit adds.** They show intent and supply inputs.
3. **The commit body.** It often carries the failing input; some bodies are empty.
4. **Release notes.** They label and group commits.
5. **Context7, library `/simdjson/simdjson`.** It serves upstream's `doc/*.md` at `master`
   with no version index. Use it to learn what an API is documented to do: name the
   identifiers in the query ("iterate_many and parse_many document streams: batch_size,
   truncated documents"). Confirm with rank 1 anything you take from it; its generated
   summaries have contradicted upstream's own examples.

## Path map

Upstream paths are relative to `include/simdjson/` unless they start with `src/`,
`benchmark/` or `dependencies/`. The Go column extends the Layout table in `AGENTS.md`.

| Upstream | Go owner |
|---|---|
| `src/generic/stage1/json_{escape_scanner,string_scanner,scanner,structural_indexer}.h`, `buf_block_reader.h`, `src/generic/json_character_block.h` | `internal/stage1/stage1.go`, `kernel_generic.go`; `stream.go` for the resumable scan. The stream branches of the indexer's `finish()` are `Reader.finish` in `internal/stream/stream.go` |
| `src/arm64.cpp`, `arm64/` | `internal/stage1/kernel_arm64.go` |
| `src/generic/stage1/utf8_*.h` | `utf8Checker` in `kernel_arm64.go`; other builds use `unicode/utf8` (`kernel_fallback.go`) |
| `src/generic/stage1/json_minifier.h`, `minify.h` | `Minify` in `internal/stage1/stage1.go`, exported by `serialize.go` |
| `src/generic/stage2/{json_iterator,tape_builder,tape_writer}.h`, `generic/atomparsing.h`, `internal/tape_type.h` | `stage2.go`, `tape.go` |
| `src/generic/stage2/stringparsing.h`, `generic/jsoncharutils.h` | `internal/str/str.go`, `strings.go`, `ondemand/iter.go` |
| `generic/numberparsing.h` | grammar and integers: `numbers.go`, `internal/number/{number,digits}.go`; On-Demand getters: `ondemand/numbers.go`. Float conversion is Go's `strconv` algorithm (`internal/number/fastfloat.go`), so only a changed result ports |
| `dom/{element,array,object,document,parser}*.h`, `internal/tape_ref*.h`, `internal/dom_parser_implementation.h`, `generic/dom_parser_implementation.h` | `element.go`, `parser.go`; `at_pointer` is `pointer.go` |
| `dom/serialization*.h`, `internal/json_escape.h`, `internal/jeaiii.h` | `AppendJSON` in `serialize.go`, written on `strconv`; it owes only a round trip (core spec §6) |
| `simdjson_version.h` | the pin in `scripts/fetch-simdjson.sh` |
| `error.h` | `internal/jsonerr/jsonerr.go`, `internal/stage1/stage1.go`, `errors.go` |
| `generic/ondemand/{json_iterator,token_iterator,parser}*.h` | `ondemand/iter.go` |
| `generic/ondemand/value_iterator*.h` | `ondemand/valueiter.go` |
| `generic/ondemand/{document,value,object,field,object_iterator,array,array_iterator,json_type}*.h` | `ondemand/{document,value,object,array}.go` |
| `generic/ondemand/raw_json_string*.h` | `keyEquals` in `ondemand/valueiter.go` |
| `dom/document_stream*.h`, `generic/ondemand/document_stream*.h`, `src/generic/stage1/find_next_document_index.h` | `internal/stream/stream.go`, `stream.go`, `ondemand/stream.go`, streaming mode in `stage2.go` |
| `padded_string*.h` | no type. Go emulates the padding where C++ reads it: `block` in `stage1.go`, `stage2.go`, `pad` in `ondemand/iter.go`, `ondemand/document.go` |
| `benchmark/<task>/simdjson_{dom,ondemand}.h` | `scripts/cpp-bench/bench.cpp` and the Go benchmark of the same name |
| `dependencies/CMakeLists.txt` (simdjson-data commit) | `scripts/fetch-testdata.sh` |

### No Go owner

- The other kernels: `haswell`, `icelake`, `westmere`, `ppc64`, `lsx`, `lasx`, `rvv-vls`.
- C++'s `fallback` kernel. Go's portable build runs the generic algorithm. A fallback fix
  often travels with a generic fix for the same input, so read the commit for the generic half.
- The builder, static reflection, compile-time JSON (`constevalutil.h`,
  `compile_time_accessors.h`), `deserialize`, `key_selector`.
- Include lists: `ondemand.h`, `generic/ondemand/amalgamated.h`.
- `internal/fast_float.h`: Go converts floats with its own `strconv` algorithm.
- JSONPath (`at_path`), `fractured_json`, `prettify`, `to_string`, On-Demand serialization.
- The exceptions interface, the threaded `document_stream` (`stage1_worker`), runtime dispatch.
- Error message text, loggers, file loading.
- `tools/`, `fuzz/`, `cmake/`, CI, `singleheader/` (generated), and everything under
  `benchmark/` except the task bodies in the path map.

Data binding (`decode.go`, `encode.go` and the files beside them) follows `encoding/json/v2`.
An upstream change reaches it only through the stage 2 it shares.

## Traps

- `gh api repos/simdjson/simdjson/compare/A...B` stops at 250 commits and 300 files without
  an error. Count with the clone.
- In zsh, `git show $t:singleheader/simdjson.h` misparses, because zsh reads `:s` after a
  variable as a modifier. Write `${t}:singleheader/simdjson.h`.
- On macOS, `-pthread` leaves the single header's threads off. A scratch program that needs
  them adds `-DSIMDJSON_THREADS_ENABLED`.
- A `perf` commit upstream can change results by accident. Its prompt still needs the
  fuzzers `AGENTS.md` lists for that kind of change.
