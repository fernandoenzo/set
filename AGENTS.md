# Repository Guidelines

## Project Overview

`set` is a generic Go package exposing `Set[T comparable]`, an unordered set of comparable values backed by a `map[T]struct{}` that keeps its memory proportional to the number of elements it holds — including after large deletions. Go maps grow on insert and never shrink on delete, so the set rebuilds its map ("rehash") when a deletion has fallen far enough for the rebuild to pay off. The rules are derived, not tuned: `docs/set-rehash-en.md` models Go's Swiss-table runtime exactly and proves that after a rebuild the expected extra memory stays below 0.4% of the capacity step.

The zero value (`var s set.Set[T]`) is a usable empty set; the map is created by the first write. A nil `*Set` is not usable. There are no external dependencies.

## Architecture & Data Flow

The type is two fields: `set map[T]struct{}` and `capacity int` (the hint the map was last reserved for). Everything hangs off one model of what `make(map, hint)` actually reserves.

```
New(hint) ──► make(map, hint), capacity = hint
zero value ──► first write ──► ensure ──► resize(hint) ──► New + maps.Copy
                                          │
     insert path                          │            delete path
Add / AddAll / AddSeq / Extend ───────────┤
  estimatedSlotsLeft() covers the batch?  │  Remove / Subtract:
  no resize, else resize + compact        │  delete + needsRehash(before, after)
                                          ▼
                             Rehash() = resize(Len())
                             compact() = Rehash when hintOversized(capacity, Len())
```

Four layers:
1. **Public API** (`set.go`): construction, mutators, readers, derived sets, copies — the whole exported surface.
2. **Reservation model** (`theoreticalSlots`, `pow2ceil`, `hintOversized`): mirrors the runtime's sizing — `target = hint*8/7`, a power-of-two directory of `ceil(target/1024)` entries and power-of-two tables of `target/dir` slots (at least 8). The rounding can leave the usable budget (7/8 of the slots) below the hint; those cracks are what `hintOversized` detects.
3. **Trigger rule** (`needsRehash`): fires on threshold *crossings*, not zones, so each capacity step is rebuilt at most once per monotone descent.
4. **Probe estimator** (`sampleCount`, `overlapSample`, `samplingFloor`): `Extend` and `Intersection` cannot know their result size, so they size the map from a 256-element sample of an operand and let `compact()` correct a bad estimate with a rebuild — the estimate decides how much work is done, never what the set contains.

## Key Directories

| Path | Purpose |
|---|---|
| `.` | The package itself (`set.go`), its suite (`set_test.go`), `go.mod`, `README.md` |
| `docs/` | The rehash derivation, in English (`set-rehash-en.md`) and Spanish (`set-rehash-es.md`) |

## Development Commands

```bash
# Run the behavioural suite
go test ./...

# Same suite under the race detector (no shared state, but it must stay clean)
go test -race ./...

# Verbose output
go test -v ./...

# Run a single test
go test -run TestSetAlgebraMatchesModel -v

# Static checks
gofmt -l .
go vet ./...
```

There is no Makefile, no CI configuration and no lint or coverage target. The suite defines no `Benchmark` functions, so `go test -bench .` (quoted in `README.md`) currently reports nothing; the performance table there was measured out of band.

## Code Conventions & Common Patterns

- **Function length**: Max 25 lines of code per function (comments excluded). Extract helpers early. Two known exceptions: `Intersection` (53) and `Extend` (37). Do not grow that list.
- **Comments carry the derivation, not the mechanics**: every non-obvious decision points at `docs/set-rehash-en.md §N` or at a named README section, so the math lives in one place. Public identifiers get their contract in a doc comment; internal helpers (`resize`, `compact`, `sampleCount`, `theoreticalSlots`, `needsRehash`, `hintOversized`) document *why* they exist.
- **In-repo terminology**: *rehash* is a rebuild of the map; *step* is the capacity class `theoreticalSlots(hint)` returns; *over-allocation* is the gap between what the map reserves and what it holds; *probe/sample* is the 256-element estimate. Use these words, not synonyms.
- **Two thresholds, two mechanisms**: `needsRehash` watches a *descent* that crossed a boundary (`Remove`, `Subtract`); `hintOversized`/`compact` fix a *delivery that landed one step above its length* (`Extend`, `Intersection`, `Difference`, `NewFromSlices`, `AddAll`). Do not conflate them.
- **Constants over literals**: `overlapSample` (256) and `samplingFloor` (`overlapSample * 16`, 4096) are named and derived in the README; never inline a sample size or a floor.
- **Self-operations must stay valid**: `s.Extend(s)`, `s.Retain(s)`, `s.Subtract(s)` and zero-valued arguments are in scope, and `resize` replaces the receiver wholesale (`*s = *rebuilt`) — an internal lock would deadlock on re-entry and be copied by that assignment. See the README on why the set carries no concurrency control.
- **No background work**: no goroutines, no finalizers, no `unsafe`. Keep it that way.
- **Iteration is unordered**: never let a public result depend on map iteration order (`GetAll` documents it, `Union`/`Intersection` build from ranges).
- **Table-driven tests with a reference model**: `TestSetAlgebraMatchesModel` checks every ordered pair of subsets of `{0,1,2}` against `map[int]struct{}`; `TestMutatorsMatchModel` runs 200 seeded random trials against the same model. New behaviour goes into that model comparison, not into ad-hoc assertions.
- **Seeded randomness**: `rand.New(rand.NewPCG(0xC0FFEE, 0xBEEF))` — deterministic trials, no flaky runs.
- **Helpers stay unexported and prefixed by role**: `modelOf`, `setOf`, `assertMatches`, `makeSeq`, `disjointSeq`, `extendOf`.

## Important Files

| File | Role |
|---|---|
| `set.go` | The entire package: `Set[T]`, `New`, `NewFromSlices`, every method, `Union`, `Intersection`, `sampleCount`, `theoreticalSlots`, `needsRehash`, `hintOversized` |
| `set_test.go` | The behavioural suite: 34 tests, exhaustive algebra against a model |
| `theoretical_slots_test.go` | Reads the runtime's real slot count (behind `unsafe`) and checks `theoreticalSlots` against it; the only tie between the model and the runtime |
| `bench_test.go` | The benchmarks behind the README's Performance table (`go test -bench .`) |
| `LICENSE` | GPLv3 full text (byte-identical to `nvfp/LICENSE`) |
| `go.mod` | Module path and Go version (`go 1.27.1`); no dependencies, so no `go.sum` |
| `README.md` | User-facing contract: guarantees, API tables, cost model, concurrency rationale, and the derivation of the 256-element sample (Cochran's formula) |
| `docs/set-rehash-en.md` | Full derivation with proofs: reservation model, why the trigger is forced to be a crossing, the Excess Theorem and its 0.4% bound, termination, numerical checks |
| `docs/set-rehash-es.md` | The same document in Spanish; the two must stay in sync |

## Runtime/Tooling Preferences

- **Language**: Go 1.27 — the `go` directive in `go.mod` pins the newest available patch (currently `1.27.1`); bump it when a new patch ships.
- **Dependencies**: none. Standard library only (`iter`, `maps`, `slices`, `math/bits` in `set.go`; `math/rand/v2`, `slices`, `testing` in the suite). No `go.sum`, no mocking library, no assertion library.
- **API shape**: generics only (`Set[T comparable]`), pointer receiver for every mutator and reader, free functions (`Union`, `Intersection`) for the operations that need no receiver.
- **Docs are bilingual**: `docs/set-rehash-en.md` and `docs/set-rehash-es.md` are the same argument in two languages; a change to one is a change to both.
- **Published versions**: v1.1.0 is the current API, and the README documents it.
- **The `go` directive is a consumer requirement**: it gates download, not only the local toolchain, so a `go 1.27.1` module makes older toolchains fetch a newer one or fail. Raising it is a breaking change for consumers; only raise it above the newest patch with a reason.

## Git Workflow

- **Every commit must be signed**: `commit.gpgsign` is set in this repo's local config, so plain `git commit` signs; in a fresh clone pass `-S` explicitly. If signing fails, stop and report; never fall back to an unsigned commit.
- **Every plan execution must end with commit and push**: after all changes are verified, commit and push to remote.
- **Commit messages are one short line**: a single concise subject, no body, no bullet points, no paragraph. Say what changed, not why.
- **No conventional commit prefixes** (fix:, feat:, refactor:, etc.). Commit messages go in plain natural language.

## Testing & QA

- Run everything: `go test ./...` (32 tests, sub-second) and `go test -race ./...`.
- Benchmarks live in `bench_test.go` and cover every row of the README's Performance table; run them with `go test -bench . -benchmem`. They use `b.Loop()` except the three mutating ones (`Remove`, `Rehash`, `Extend`), which need `StopTimer`/`StartTimer` around their setup and therefore the classic `b.N` form — `b.Loop` panics if called with the timer stopped. If a row of the table changes, the matching benchmark must change with it: the table must stay reproducible from `go test -bench .` alone.
- The suite runs against a reference model, not mocks: `map[int]struct{}` for membership and `slices`/`maps` for the algebra.
- Coverage is contractual, not structural. The fixed contracts are: every writer and reader against the zero value (`TestZeroValueWriters`, `TestZeroValueReaders`, `TestZeroValueSetAsArgument`), the full algebra against the model (`TestSetAlgebraMatchesModel`, `TestMutatorsMatchModel`), the `Copy`/`Clone` reservation contracts (`TestCopyAndCloneContracts`), `Rehash` landing on the smallest step (`TestRehashCompactsToSmallestStep`), `Difference` delivering an exactly-sized result (`TestDifferenceNeverOverAllocated`, `TestDifferenceDisjointKeepsStep`), self-operations (`TestSelfOperations`), the estimator still delivering on a step (`TestEstimatedSizingStillDeliversOnStep`), and the probe that sizes `Difference` (`TestDifferenceReservationTracksResult`, `TestDifferenceProbeDeliversExactResult`). Statement coverage is 100%; the tests that close the last branches are `TestNewFromSlicesCompactsOversizedHint`, `TestAddAllCompactsAfterResize`, `TestExtendProbeSkipsExistingElements`, `TestIntersectionProbeSeesAbsentElements` and `TestExtendProbesSmallArgumentExactly`.
- **Coverage is not sensitivity, and for the probes it is not enough.** A probe's branches can all be executed while the delivered set stays identical, because a wrong estimate only moves the reservation and `compact` repairs it — mutating the probes (making them report everything as new, or never probing exactly) leaves `go test ./...` green at 100% coverage. The estimate is observable only in *allocations* and time, not in contents; `bench_test.go` is what guards it (the same mutation moves `BenchmarkExtend` from ~93 µs to ~112 µs per op). Treat a green suite as proof of correctness, never as proof that the probe still estimates well.
- Tests may read unexported state (`s.capacity`) where the contract *is* the reservation; that is deliberate and is what pins the model to the behaviour.
- `theoretical_slots_test.go` is the one place the package is tied to the runtime it models: it reads the real slot count of `make(map, hint)` through `internal/runtime/maps`'s layout (behind `unsafe`, in a test file only — `set.go` must stay free of it) and compares it with `theoreticalSlots`. Run `SET_XCHECK_FULL=1 go test ./...` for the exhaustive sweep to 300,000; the default grid covers every capacity-step boundary and every crack in about a second. `TestRuntimeLayoutReadable` runs first and reports "the runtime layout changed" — update the reader in that case, don't touch `theoreticalSlots`. Changing the reader itself requires re-deriving the offsets from `map.go`/`table.go` (`dirPtr` is a `[]*table`; `table.capacity` is a `uint16` at offset 2; `dirLen == 0` means one 8-slot group).
- `TestTheoreticalSlotsIsNonDecreasing` and `TestTheoreticalSlotsAtMostDoublesPerStep` pin the sizing model itself up to 2,000,000, with the stated rationale that `needsRehash`'s threshold rule stops being valid if they fail.
- Adding a public operation means: a model comparison for its semantics, plus a reservation assertion if it returns or leaves a set whose size is knowable.
- **A probe's estimate is a reservation, never an answer.** `differenceReservation` and the other `sampleCount` callers may be wrong about size; the tests pin that the *contents* stay exact and the *delivered* set lands on its own step. When changing a probe, mutate the estimate (return the upper bound, or a fraction of it) and confirm the test fails — that is what proves the test covers the branch.
- `TestSingleAddNeverReserves` guarantees that a single `Add` never rebuilds, which is what makes the amortised cost claim in the README hold.

## License

GPLv3+. The full text is in [`LICENSE`](LICENSE), which is byte-identical to the one in `nvfp/`; the README's "License" section points at it. Do not add a license header to source files without asking.
