# simdjson-go — Sub-project 2: Streams — Design

- **Date:** 2026-10-08
- **Status:** Approved; implemented
- **Builds on:** sub-project 1 (core + DOM), `docs/superpowers/specs/2026-10-06-simdjson-go-core-design.md`;
  sub-project 3a (On-Demand), `docs/superpowers/specs/2026-10-07-simdjson-go-ondemand-design.md`
- **Reference semantics:** C++ simdjson v5.0.2 `dom::parser::parse_many` and
  `ondemand::parser::iterate_many` (`include/simdjson/dom/document_stream*.h`,
  `include/simdjson/generic/ondemand/document_stream*.h`,
  `src/generic/stage1/find_next_document_index.h`, `json_structural_indexer.h`,
  `doc/parse_many.md`, `doc/iterate_many.md`)

## 1. Goal

Read many JSON documents from one buffer: NDJSON, concatenated JSON, comma-separated documents,
RFC 7464 sequences and the elements of a top-level array, through the DOM (`ParseMany`) and
On-Demand (`IterateMany`). C++ is the reference for *what* is read; the API and the batching are
Go's own. Stage 1 of the next window runs in a goroutine while the caller reads the current one.

The governing rule: **the batch size is a tuning knob and never changes results.** Every stream
yields what C++ yields when its whole input fits in one batch, with two deliberate differences
(§4.1): a bad tail is reported instead of dropped silently, and a stage 1 error is reported at the
document that holds the bad byte instead of failing the whole input.

### Success criteria

1. **Same results as C++ in one window.** For every case in `testdata/stream/oracle.jsonl` (§8)
   that has no stage 1 error, `ParseMany` and `IterateMany` yield the same documents (offset,
   source, contents read by the script) and the same final error as C++ run with
   `batch_size ≥ len`, translated as in §4.1.
2. **Batch size never changes results.** `FuzzParseMany` and `FuzzIterateMany` find no input,
   format and batch size whose items differ from the same input in a single window, over 10
   minutes each.
3. **Pipelining pays.** On `large_amazon_cellphones` (§8), the default `BatchSize` is at least
   1.15× faster than a single window, NEON build, Apple M3 Max. If the prototype misses this, the
   goroutine is dropped (§9) and the figure recorded here.
4. **No regression.** `BenchmarkParse` and the On-Demand task benchmarks stay within 2% of `main`
   in geometric mean.
5. **No per-document allocation.** Once the `Parser` is warm, a stream allocates a constant amount
   (closure, goroutine, channels) whatever the number of documents.
6. `make check` passes, including the race run.

### Results

- **Oracle.** `testdata/stream/oracle.jsonl` has 2,911 cases. 106 are skipped for a stage 1 error
  (covered by the stage 1 tests and the fuzzers) and 4 are compared up to the DOM document that
  closes on the array's `]`.
- **Fuzzing** (Task 6). `FuzzParseMany`: 600 s pure Go (60.3M execs) and 300 s NEON after a fix
  (61.2M). `FuzzIterateMany`: 600 s (41.0M). `FuzzStream` (`internal/stage1`, NEON build): 120 s
  (5.9M).
- **Pipelining** (NEON, M3 Max, `large_amazon_cellphones`, default `BatchSize` vs one window):
  `ParseMany` 1.25× (704 to 884 MiB/s), `IterateMany` 1.56× (873 to 1360 MiB/s). The goroutine
  stays (§9).
- **Regression vs `main`:** `BenchmarkParse` +1.54% and the On-Demand tasks +1.12% (geometric
  mean), within the 2% bar.
- **Added since the plan:** `CommaDelimitedArray` documents read the array's own `]` (both APIs);
  an On-Demand read that runs past the decided indices (a malformed root, or out of order) first
  indexes the whole input (§5.5); bytes On-Demand's delimiter skip passes over are checked by
  stage 1; `ParseMany` waits after a failed walk in `CommaDelimitedArray` (§5.4); after a read
  that abandons a document, the next step skips from its root (§3); the stage 1 worker's state
  sits on its own cache lines (§5.2).

## 2. Decisions

| Topic | Decision | Why |
|---|---|---|
| Scope | DOM `ParseMany` and On-Demand `IterateMany`, in-memory `[]byte` | C++ parity; C++ has no reader form either |
| Shape | `iter.Seq2[*Document, error]`, `Offset`/`Source` on the document, an error type with the offset | Go 1.23 iterators, as `Array.All` and `Object.All` |
| Formats | All five C++ `stream_format` values | Requested; they share one segmenter |
| Batching | A knob that never changes results; equal to C++ in one window | C++'s window artefacts are limitations, not features |
| Engine | One continuous stage 1 cut into 64-byte-aligned windows, then a document segmenter | Index stream identical to a single pass by construction |
| Bad tail | A final `ErrTrailingContent` item | A plain range loop cannot lose data unnoticed |
| Stage 1 errors | Reported at the document holding the bad byte | Reporting them first would need all of stage 1 before the first document |
| Threading | Always pipelined when there is more than one window; no switch | C++'s `threaded` flag only exists to work around missing threads |

**Out of scope:** `load_many` (`os.ReadFile` covers it); `io.Reader` input; streams in
`Unmarshal`; C++'s `size_in_bytes`, `truncated_bytes` (replaced by the final error) and iterator
class; an x86 SIMD kernel.

## 3. Public API

```go
package simdjson

// Format says how documents are separated. It is defined in internal/stream; both packages
// alias the type and its constants.
type Format = stream.Format

const (
	Whitespace          Format = iota // C++ whitespace_delimited: NDJSON, concatenated JSON
	NewlineDelimited                  // DOM: as Whitespace. On-Demand: unread documents skip to '\n'
	JSONSequence                      // RFC 7464: each record starts with RS (0x1E)
	CommaDelimited                    // {...},{...}
	CommaDelimitedArray               // [{...},{...}]: the array's elements
)

type Parser struct {
	MaxDepth       int
	BigIntAsString bool
	// BatchSize is the stage 1 window of ParseMany, in bytes: 0 means 1,000,000; above 1 GiB it
	// means 1 GiB; it is rounded up to a multiple of 64. It changes speed and memory, never results.
	BatchSize int
	// ...
}

func (p *Parser) ParseMany(b []byte, f Format) iter.Seq2[*Document, error]
func (d *Document) Offset() int    // where the document starts in b
func (d *Document) Source() []byte // its bytes in b, as C++ source()

// StreamError ends a stream. Unwrap returns Err, so errors.Is(err, ErrTape) works.
// Defined in internal/jsonerr, as the sentinels.
type StreamError = jsonerr.StreamError // struct{ Offset int; Err error }: Offset is where the
                                       // failing document (or the dropped tail) starts in b
```

```go
package ondemand

type Parser struct{ BatchSize int /* as above */ }
func (p *Parser) IterateMany(b []byte, f Format) iter.Seq2[*Document, error]
func (d *Document) Offset() int
func (d *Document) Source() []byte
type StreamError = jsonerr.StreamError // shared with package simdjson, as the error sentinels
```

```go
for doc, err := range p.ParseMany(data, simdjson.Whitespace) {
	if err != nil {
		return err // always the last item
	}
	...
}
```

- One `(doc, nil)` per document; an error is always the last item, as `(nil, *StreamError)`.
  Empty input yields nothing.
- The `Seq2` is not single-use: ranging over it again starts again.
- Breaking out of the loop stops the stage 1 goroutine before `range` returns.
- A yielded `*Document`, and everything read from it, is valid until the loop's next step or the
  next `Parse`, `ParseMany`, `Iterate` or `IterateMany` on `p`. The stream keeps its own index
  buffers in the `Parser`, so a `Parse` or `Iterate` inside the loop only invalidates the current
  document. Starting a second stream on `p` while one runs ends the older one with
  `ErrOutOfOrderIteration`.
- Offsets index the caller's `b`, so they count a skipped BOM and the brackets of
  `CommaDelimitedArray` (C++'s `current_index` does not).
- `MaxDepth` and `BigIntAsString` apply to every document (C++'s threaded path loses them after
  the first batch).
- On-Demand: `Document.AtEnd` says whether this document has been read to its end (C++ compares
  with the end of the batch). Read errors (wrong type, bad number, missing field) belong to the
  read and do not end the stream; the next step skips the rest of the document, as in C++.
  After a read that abandons a document (a fatal error), the next step skips from the document's
  root, as for an unread document; C++ leaves this undefined.
  Handles from an earlier document return `ErrOutOfOrderIteration`.

## 4. Behaviour

### 4.1 Errors

Every error ends the stream, as in C++. `Offset` is C++'s `current_index` translated into `b`.

| Cause | `Err` | `Offset` |
|---|---|---|
| `len(b)` over 0xFFFFFFFC | `ErrCapacity` | 0 |
| Invalid UTF-8, or a control character in a string | `ErrUTF8` / `ErrUnescapedChars` | start of the document holding the first bad byte |
| DOM stage 2 failure | what `Parse` reports for that document | document start |
| On-Demand skip whose brackets never balance | `ErrIncompleteArrayOrObject` | as C++ |
| Tail dropped by C++'s final trim (§5.3) | `ErrTrailingContent` | start of the dropped region: `len − truncated_bytes` in C++ terms |
| `CommaDelimitedArray` input not `[`…`]` after trimming whitespace | `ErrTape`, as the only item | 0 |

A document *holds* the bytes from its first structural up to the next document's first structural
(or the end of the input). For the DOM, the next document starts where stage 2 stopped; if stage 2
fails, and for On-Demand, it starts where a bracket count from the root ends (C++ `skip_child`).
The dropped tail holds its own bytes, so a bad byte there is reported at the tail's start with the
stage 1 error instead of `ErrTrailingContent`. Bytes that On-Demand's delimiter skip
(`NewlineDelimited`, `JSONSequence`) passes over, from the root's bracket-count end to the next
document, form a region of their own in the same way. Within the document that holds a bad byte, C++'s
check order applies (`ErrUnescapedChars` before `ErrUTF8`), so a stream of one document reports
what `Parse` reports. This and the trailing error are the two differences from C++ in one window,
where a stage 1 error is the first and only item and a bad tail is dropped without an error.

### 4.2 Input rules

- Empty, whitespace-only, BOM-only input and `[]` (array format) yield nothing.
- A leading UTF-8 BOM is skipped.
- `1 2 34` yields `1`, `2`, `34`. As in C++ streams, a root scalar never gets
  `ErrTrailingContent`, and the DOM skips the unmatched-outer-brace check (C++ issue 906).
- Comma formats: commas at depth 0 only separate; `,1,,2,,"x",,` yields three documents.
- `JSONSequence`: RS bytes are dropped and runs of RS and whitespace collapse. If any RS is
  present, every record is kept without a balance check (C++'s final-window rule), so an
  incomplete last record is a stage 2 error, not trailing content. Without any RS, the
  `Whitespace` rules apply. RS-only input yields nothing.
- `NewlineDelimited`: as `Whitespace` in the DOM. In On-Demand, a document not read to its end is
  skipped to the next `\n` without validating the rest, as C++'s `skip_to_delimiter`.
- In `CommaDelimitedArray`, a document still open at the end of the array's contents reads the
  array's own `]` there, as C++ does (C++ strips the brackets by moving its pointers, so the byte
  after its input is that `]`).
- A number or literal that ends the input inside a container fails as C++'s does (`ErrNumber`,
  `ErrTAtom`, …): C++ parses it in place, before a `\0` padding byte.
- `BatchSize` below 64 becomes 64, and above 1 GiB becomes 1 GiB; at most `min(BatchSize, len(b))`
  is allocated.

### 4.3 C++ behaviour deliberately not ported

All of it is a consequence of C++'s windows or threads:

- `CAPACITY` for a document longer than `batch_size`, and for a scalar touching the window edge.
- A stage 1 error failing every document of its batch.
- `json_sequence` silently skipping a window without RS.
- The threaded path's two divergences: settings lost after the first batch, and documents skipped
  after an empty mid-stream batch.
- The fallback kernel's narrower UTF-8 check (Go has one checker, equal to NEON).
- After the DOM's last document closes on that `]` (§4.2), C++ goes on parsing past its own
  end-of-input sentinel, into indices its comma filter left behind and then stale memory; Go
  stops there.
- `trim_partial_utf8`: C++ drops a UTF-8 character cut at the end of every window, the final one
  included, without an error. In Go a character cut at the end of the input is invalid UTF-8
  (§4.1); a character cut at a window's end is completed by the next window.

## 5. Internals

### 5.1 Stage 1 as a resumable scan

`internal/stage1` gains a `Stream` holding the state `Index` already carries from block to block
(`scanner`, `utf8Checker`, offset). `Stream.Reset(buf)` binds the input and `Stream.Next(end, idx)` scans the 64-byte blocks of
`buf[off:end]`, `end` a multiple of 64 or `len(buf)`, and appends absolute offsets. `Index`
becomes one `Next` over the whole buffer plus its existing end-of-input checks, so the kernels are
unchanged and the indices of any window sequence equal a single pass. The portable UTF-8 path
validates each window with `utf8.Valid`, holding back a character cut at the window's end.

### 5.2 Pipelining

If `len(b) <= BatchSize` there is one window and no goroutine. Otherwise a goroutine runs `Next`
window by window into two index buffers that alternate, handed over on channels (filled one way,
empty the other). Breaking out of the loop cancels the worker and waits for it. The state the
worker writes (`stage1.Stream`, the two windows) is padded onto its own cache lines: sharing one
with the fields the consumer reads per index cost `ParseMany` 15% and `IterateMany` 7% on
`large_amazon_cellphones`, depending on how the `Parser` happened to be laid out. When a stream
ends, the `Parser` drops its references to the input (`Release`); a document already yielded
keeps its own.

When a window holds a UTF-8 or unescaped-character error, `stage1.Stream.Next` (on the worker,
when pipelined) rescans that window block by block from its saved start state to find the first
bad byte (error path only).

### 5.3 Segmenter (`internal/stream`)

The segmenter reads the index stream in order and keeps `pending`, the unread indices: before each
new window is appended, the unread tail moves to the front. Memory is O(window + largest
document).

- **Format filters**, applied as indices arrive: drop depth-0 commas (comma formats); drop RS and
  insert missing value starts (`JSONSequence`), as C++'s stage 1 does in those modes.
- **Decided point.** A *boundary candidate* is a structural that starts a value (not a closer, `:`
  or `,`) and whose predecessor is not `{`, `[`, `:` or `,`: C++'s `find_next_document_index`
  rule. C++ trims only after the last boundary of its window, so with one window everything before
  the latest candidate seen is final. Documents are handed out only from that decided prefix.
- **End of input.** C++'s final trim runs once, ported exactly, on the region after the last
  candidate: drop a trailing unclosed-string quote, keep the region if its brackets balance,
  otherwise drop it. With no candidate at all, the whole input is the region. A dropped region
  becomes the final `ErrTrailingContent`.

### 5.4 DOM

`builder` gains a streaming mode, as C++'s `walk_document<true>`: no unmatched-outer-brace check,
no trailing-content check, and it returns where it stopped, which is where the next document
starts. It never reads past a boundary candidate inside a document without failing first: inside
a container, a value start where `,`, `]` or `}` was expected is `ErrTape`. So parsing from the
decided prefix gives the same result as one window. One exception: in a `CommaDelimitedArray`
stream, a walk that fails before the input is done may have read 0 at the decided limit where, had
that candidate turned out to start the dropped tail, it would have read the array's `]` (C++'s
sentinel, §4.2). So after any failed walk the stream loads until the limit moves or the input is
done and, if the limit is then the end of the indices, walks again reading `]`. The fuzzer (§8)
checks this.

### 5.5 On-Demand

Before a document is yielded, the segmenter extends the decided prefix to the document's
bracket-count end plus four structurals (`reachAhead`: `rootTokenLen` peeks two ahead and, in C++,
sees the next document's start). Moving to the next document (C++ `next_document`) runs in the segmenter, not
in the `Document`, so it can load windows: a bracket count from the reader's cursor and depth
(`skip_child`), or, for `NewlineDelimited` and `JSONSequence` when the document was not read to
its end, a forward scan with `bytes.IndexByte` for the delimiter (`skip_to_delimiter`). The
`Document` reads a view of `pending` that runs past the decided point, followed by C++'s
sentinels once the input is done. Handles keep positions, not pointers, and `pending` is compacted
only between documents (once the consumed half is at least half of it, so copying stays linear).
`Iterate` gets its own index buffer, so calling it inside the loop cannot overwrite `pending`. The
fuzzer checks that no read reaches the view's end before the input's. A read can still run past the
root to the end of C++'s one batch: on a malformed root (`[}` leaves the array open for
On-Demand; C++'s source walk of a root starting with `]` or `}`), or out of order (an empty root
array started again, which C++'s release build allows, reads on from its end). So every step
that moves the cursor (`reach`, at the entry of the `valueIter` steps and `skipChild`, and in
`skipChild`'s and `Source`'s walks) first checks that the indices it can read are final; if
not, the view grows to the whole input (`more`), as one window would see it. Reading a root in
order never grows the view, since the decided prefix runs `reachAhead` past it. A view whose
stream has ended (`Release`) or been replaced cannot grow. `peekAt` and `advance` carry no
check, so they stay inlined. Past the last document, C++'s sentinels (`len`, where the dropped tail
starts, 0) can make `peek_length` negative, which C++ reads as a huge `size_t`: Go reads 0.

## 6. Changes to existing code

- `internal/stage1`: `Stream` (§5.1); `Index` built on it.
- `stage2.go`: the streaming mode of `builder` (§5.4), behind one flag so `Parse` is unchanged.
- `ondemand/iter.go`: `Iterate`'s setup split so `IterateMany` can point a `Document` at a view.
- `internal/jsonerr`: `StreamError`, re-exported by both packages.

## 7. Layout

| Path | What |
|---|---|
| `internal/stage1/stream.go` | The resumable scan |
| `internal/stream/` | `Format`, the window worker, the segmenter, the port of `find_next_document_index` |
| `stream.go`, `stream_test.go` | `ParseMany`, `Document.Offset`/`Source` |
| `ondemand/stream.go`, `ondemand/stream_test.go` | `IterateMany` |
| `scripts/ondemand-oracle/` | Gains a stream mode (§8); the directory keeps its name |
| `testdata/stream/oracle.jsonl` | C++'s recorded stream results; never edited by hand |

## 8. Testing

- **Stream oracle.** `oracle.cpp` gains cases
  `{"doc":hex|"file":path, "stream":{"api":"dom"|"ondemand","format":...}, "script":[...]}`, run
  with `batch_size = max(len, 32)` (one window, no thread). Per document it prints
  `@current_index`, the source in hex, then the script's output: the DOM walks the whole
  document; On-Demand runs the existing script language, including partial reads and documents
  left unread. The case ends with `!code` or `~truncated_bytes`. `make oracle` regenerates
  `testdata/stream/oracle.jsonl` beside the On-Demand file. `TestStreamOracle` (package `ondemand`,
  covering both APIs) replays it, translating indices to offsets and `~n` to the final
  `ErrTrailingContent`. Cases whose input is not valid UTF-8 (which covers every input C++'s `trim_partial_utf8` changes) or
  where C++ reports `UNESCAPED_CHARS` are skipped there and covered by the next test. DOM
  `CommaDelimitedArray` cases where C++ goes on after a document that closed on the array's `]`
  (§4.3) are compared only up to that document, and counted apart. C++'s next step after a read
  that abandons a stream document dereferences a null parser, so the oracle ends such a case
  with `x<code> dead`.
- **Cases** (`gen.py`): the C++ tests' inputs (`issue2181`, `issue2170`, `test_naked_iterators`,
  `issue1977`, `issue2137`, `fuzzaccess`, `truncated_bytes_filtered_formats`,
  `source_scalar_before_truncated`, the comma and RS runs); corpus scalars and containers joined
  with each format's separators; every byte prefix of small streams; malformed documents
  mid-stream; whole corpus files as one document.
- **Stage 1 errors.** A valid stream with one byte corrupted at p (invalid UTF-8, or a control
  character inside a string) yields the clean stream's documents that start before the document
  holding p, then a `StreamError` at that document's start.
- **Fuzzing.** `FuzzParseMany` and `FuzzIterateMany` take bytes, a format and a batch size from 64
  to a few KB. Items (offsets, sources, errors, the DOM's `AppendJSON` or a full On-Demand walk)
  must equal those of a single window, also past a document a read abandoned (where the oracle
  stops). Each DOM document must equal `Parse(doc.Source())`
  whenever that `Parse` succeeds.
- **Lifecycle.** `testing/synctest` tests break out of the loop at many positions, including
  while the worker is blocked, and check no goroutine is left; with `iter.Pull2`, a second stream
  on the same `Parser` runs to its end after the first is stopped mid-way. The race run covers the buffer
  handoff. A second stream on one `Parser` ends the first with `ErrOutOfOrderIteration`.
- **Benchmarks.** `BenchmarkParseMany` and `BenchmarkIterateMany` on `amazon_cellphones.ndjson`
  (277 KB) and `large_amazon_cellphones` (the same file repeated 40 times, 11 MB), computing C++'s
  `amazon_cellphones` benchmark answer: count and mean rating by brand.
- **AGENTS.md** gains: "A stream change needs `TestStreamOracle` and both stream fuzzers."

## 9. Risks

- **The decided-point arguments (§5.4, §5.5) fail on some input.** The fuzzers compare against a
  single window, so a counterexample shows up as a failing case. The fix widens what is loaded
  before a document is handed out (up to the next candidate after its end); it never changes the
  single-window semantics.
- **The port of `find_next_document_index` drifts from C++.** The oracle's byte-prefix cases run
  the trim at every possible end.
- **Pipelining misses its bar (§1.3).** On Apple silicon stage 1 is a small share of `Parse` with
  NEON. If the gain is below 1.15×, the goroutine is removed and the stream runs stage 1 over the
  whole input first; the API and results are unchanged, and the figures go in this section.
  Measured (NEON, M3 Max, load 5–7): `large_amazon_cellphones` default vs single window is 1.25×
  for `ParseMany` and 1.56× for `IterateMany`, so the goroutine stays.
