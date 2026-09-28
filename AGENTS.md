# Repository Guidelines

## Project Overview

`set` is a generic Go package exposing `Set[T comparable]`, an unordered set of comparable values backed by a `map[T]struct{}` that keeps its memory proportional to the number of elements it holds — including after large deletions. Go maps grow on insert and never shrink on delete, so the set rebuilds its map ("rehash") when a deletion has fallen far enough for the rebuild to pay off. The rules are derived, not tuned: `docs/set-rehash-en.md` models Go's Swiss-table runtime exactly and proves that after a rebuild the expected extra memory stays below 0.4% of the capacity step.

The zero value (`var s set.Set[T]`) is a usable empty set; the map is created by the first write. A nil `*Set` is not usable. `SyncSet[T]` (`syncset.go`) wraps the same type behind an `RWMutex` for callers that do share a set; `ShardedSyncSet[T]` (`sharded_syncset.go`) splits the same wrapper across `shardCount` independent shards for contended reads. The plain type stays lock-free. There are no external dependencies.

## Architecture & Data Flow

The type is two fields: `set map[T]struct{}` and `capacity int` (the hint the map was last reserved for). Everything hangs off one model of what `make(map, hint)` actually reserves.

```
New(hint) ──► make(map, hint), capacity = hint
zero value ──► first write ──► ensure ──► resize(hint) ──► New + maps.Copy
                                          │
     insert path                          │            delete path
Add / AddAll / AddSeq / Extend ───────────┤
  estimatedSlotsLeft() covers the batch?  │  Remove / Subtract:
  no resize, else needsFreshMap → resize  │  delete + needsRehash(before, after)
  + compact
                                          ▼
                             Rehash() = resize(Len())
                             compact() = Rehash when hintOversized(capacity, Len())
```

Four layers:
1. **Public API** (`set.go`): construction, mutators, readers, derived sets, copies — the whole exported surface.
2. **Reservation model** (`theoreticalSlots`, `pow2ceil`, `hintOversized`): mirrors the runtime's sizing — `target = hint*8/7`, a power-of-two directory of `ceil(target/1024)` entries and power-of-two tables of `target/dir` slots (at least 8). The rounding can leave the usable budget (7/8 of the slots) below the hint; those cracks are what `hintOversized` detects.
3. **Trigger rule** (`needsRehash`): fires on threshold *crossings*, not zones, so each capacity step is rebuilt at most once per monotone descent.
4. **Probe estimator** (`sampleCount`, `overlapSample`): `Extend`, `Intersection` and `Difference` cannot know their result size, so they size the map from a 256-element sample of an operand and let `compact()` correct a bad estimate with a rebuild — the estimate decides how much work is done, never what the set contains.
5. **Concurrency wrappers** (`syncset.go`, `sharded_syncset.go`): `SyncSet[T]` holds a `Set` by value plus an `RWMutex` and an identity. Single-set methods take the lock in the obvious mode; multi-set methods go through `lockAll`, which sorts the requested locks by identity and collapses repeats, so aliased and inverted-order operands cannot deadlock. `ShardedSyncSet[T]` is the same contract with the storage split across `shardCount` shards, each with its own `RWMutex` and `Set`; its `lockShards` sorts sets by the same identities and takes each set's shards as one contiguous block of that order, which is what keeps the order total.
6. **Shard layout** (`sharded_syncset.go`): `shardIndex` hashes a value with one process-wide `maphash` seed, so a value's shard is a pure function of the value and shard *i* of one set holds the same values as shard *i* of another. Every multi-set operation decomposes shard by shard on that invariant and delegates to the plain `Set` core.

## Key Directories

| Path | Purpose |
|---|---|
| `.` | The package itself (`set.go`, `syncset.go`), its suites (`set_test.go`, `syncset_test.go`), `go.mod`, `README.md` |
| `docs/` | The rehash derivation, in English (`set-rehash-en.md`) and Spanish (`set-rehash-es.md`) |

## Development Commands

```bash
# Run the behavioural suite
go test ./...

# Same suite under the race detector (the plain type shares nothing; both wrappers must be clean)
go test -race ./...

# Verbose output
go test -v ./...

# Run a single test
go test -run TestSetAlgebraMatchesModel -v

# Static checks
gofmt -l .
go vet ./...
```

Go modules, no Makefile and no CI configuration. `go test ./...` runs the whole suite; `go test -bench .` runs the benchmarks in `bench_test.go`, which are the source of the numbers in `README.md`'s Performance table.

## Code Conventions & Common Patterns

- **Comments carry the derivation, not the mechanics**: every non-obvious decision points at `docs/set-rehash-en.md §N` or at a named README section, so the math lives in one place. Public identifiers get their contract in a doc comment; internal helpers (`resize`, `compact`, `sampleCount`, `theoreticalSlots`, `needsRehash`, `needsFreshMap`, `hintOversized`) document *why* they exist.
- **In-repo terminology**: *rehash* is a rebuild of the map; *step* is the capacity class `theoreticalSlots(hint)` returns; *over-allocation* is the gap between what the map reserves and what it holds; *probe/sample* is the 256-element estimate. Use these words, not synonyms.
- **Two thresholds, two mechanisms**: `needsRehash` watches a *descent* that crossed a boundary (`Remove`, `Subtract`); `hintOversized`/`compact` fix a *delivery that landed one step above its length* (`Extend`, `Intersection`, `Difference`, `NewFromSlices`, `AddAll`). Do not conflate them.
- **Constants over literals**: `overlapSample` (256) is named and derived in the README; never inline the sample size.
- **Self-operations must stay valid**: `s.Extend(s)`, `s.Retain(s)`, `s.Subtract(s)` and zero-valued arguments are in scope, and `resize` replaces the receiver wholesale (`*s = *rebuilt`) — which is why `Set` carries no lock (it would be copied by that assignment) and why `SyncSet` holds its `Set` by value and never re-enters a lock it holds. `lockAll` collapses repeats, so `s.Extend(s)` locks `s` once.
- **No background work**: no goroutines, no finalizers, no `unsafe`. Keep it that way. `SyncSet` gets its lock order from a counter (`lastID`/`setID`), not from pointer addresses, precisely to avoid `unsafe` and to stay reproducible.
- **Lock order is the deadlock proof**: every `SyncSet` and `ShardedSyncSet` has an identity assigned in construction order, and `lockAll`/`lockShards` sort requests by it and collapse repeats. Never take a wrapper lock outside those helpers; never call a public wrapper method while holding one (it would re-enter). When the loop body touches a set, use `IterSnapshot`, never `IterAll`.
- **The shard layout is an invariant, not a detail**: `shardIndex` must stay a pure function of the value with a single process-wide seed. Changing it to a per-set seed, or to a different hash per set, silently breaks every multi-set operation of `ShardedSyncSet` — the shard pairs would no longer hold the same values. `TestShardIndexIsPerValue` pins it. The shard count is a package constant for the same reason: all sets must agree on where a value lives.
- **Every shard has a counter that every mutation must maintain**: `shard.count` is what makes `Len` a sum of atomic loads instead of a 64-mutex walk. Insert and remove update it from the map's own length; bulk mutations reconcile it with `bulkLocked`; construction paths that write into the shard maps directly call `reconcileCounts`. `assertShardMatches` checks the invariant on every assertion, so a path that forgets it fails loudly instead of making `Len` wrong.
- **A transient copy takes `Clone`, not `Copy`**: `Clone` memmoves the runtime's groups without rehashing, `Copy` reinserts every element (`~2-4×` slower, widening with the count — see `BenchmarkCloneVsCopy`).
- **Every multi-set operation takes the same slice route**: `lockWith` + `lockAll` build one `[]lockReq[T]` per call, ordered and collapsed by identity. A value-typed pair specialisation (`pairReqs`) once kept the binary predicates allocation-free; it was measured at ~40 ns against ~35 ns of allocation for `Disjoint` and removed for the shared path. Every multi-set call allocates exactly that one request slice, so never add a "no allocation" claim for one of them — `TestSyncMultiSetOperationsTakeTheSliceRoute` pins the count.
- **Iteration is unordered**: never let a public result depend on map iteration order (`GetAll` documents it, `Union`/`Intersection` build from ranges).
- **Table-driven tests with a reference model**: `TestSetAlgebraMatchesModel` checks every ordered pair of subsets of `{0,1,2}` against `map[int]struct{}`; `TestMutatorsMatchModel` runs 200 seeded random trials against the same model. `syncset_test.go` mirrors both (`TestSyncSetAlgebraMatchesModel`, `TestSyncMutatorsMatchModel`) plus the zero-value, self-operation and concurrency contracts; `sharded_syncset_test.go` does the same for `ShardedSyncSet` (`TestShardSetAlgebraMatchesModel`, `TestShardMutatorsMatchModel`, `TestShardSetAlgebraAcrossAllShards`). New behaviour goes into that model comparison, not into ad-hoc assertions.
- **Concurrency tests have a budget**: `mustFinish` fails a concurrency test that does not return within its budget, so a regression names the shape that deadlocked instead of stalling the package until the go test timeout. Do not compare values measured by two separate lock acquisitions (`s.Len()` against `s.GetAll()`): a writer may commit between them, and that is the documented contract, not a bug.
- **Seeded randomness**: `rand.New(rand.NewPCG(0xC0FFEE, 0xBEEF))` — deterministic trials, no flaky runs.
- **Helpers stay unexported and prefixed by role**: `modelOf`, `setOf`, `assertMatches`, `assertSyncMatches`, `makeSeq`, `disjointSeq`, `extendOf`, `mustFinish`.

## Important Files

| File | Role |
|---|---|
| `set.go` | The entire package: `Set[T]`, `New`, `NewFromSlices`, every method, `Union`, `Intersection`, `sampleCount`, `theoreticalSlots`, `needsRehash`, `needsFreshMap`, `hintOversized` |
| `set_test.go` | The behavioural suite: 34 tests, exhaustive algebra against a model |
| `syncset.go` | `SyncSet[T]`: the concurrent wrapper — `lockAll`/`unlockAll`, `lockWith`, `setID`, `IterSnapshot`, and one method per `Set` method |
| `sharded_syncset.go` | `ShardedSyncSet[T]`: the sharded wrapper — `shardCount`, `shardIndex`, `shard`, `lockShards`/`unlockShards`, `ToSet`, and one method per `Set` method |
| `sharded_syncset_test.go` | The sharded wrapper's suite: model comparison, shard-layout invariant, zero value, self-operations, inverted orders, iterator contracts, counters |
| `sharded_syncset_bench_test.go` | The sharded wrapper's benchmarks, plus `TestShardScalingTable`, which prints the scaling the README quotes |
| `syncset_test.go` | The wrapper's suite: the same model comparisons, the zero-value and self-operation contracts, the iterator contracts, and the deadlock shapes under a budget |
| `theoretical_slots_test.go` | Reads the runtime's real slot count (behind `unsafe`) and checks `theoreticalSlots` against it; the only tie between the model and the runtime |
| `bench_test.go` | The benchmarks behind the README's Performance table (`go test -bench .`) |
| `bench_sync_test.go` | The wrapper's benchmarks, plus `BenchmarkCloneVsCopy` behind the `IterSnapshot` row (run with `go test -bench Sync`) |
| `LICENSE` | GPLv3 full text (byte-identical to `nvfp/LICENSE`) |
| `go.mod` | Module path and Go version (`go 1.27.1`); no dependencies, so no `go.sum` |
| `README.md` | User-facing contract: guarantees, API tables, cost model, concurrency rationale, and the derivation of the 256-element sample (Cochran's formula) |
| `docs/set-rehash-en.md` | Full derivation with proofs: reservation model, why the trigger is forced to be a crossing, the Excess Theorem and its 0.4% bound, termination, numerical checks |
| `docs/set-rehash-es.md` | The same document in Spanish; the two must stay in sync |

## Runtime/Tooling Preferences

- **Language**: Go 1.27 — the `go` directive in `go.mod` pins the newest available patch (currently `1.27.1`); bump it when a new patch ships.
- **Dependencies**: none. Standard library only (`iter`, `maps`, `slices`, `math/bits` in `set.go`; `cmp`, `iter`, `slices`, `sync`, `sync/atomic` in `syncset.go`; `math/rand/v2`, `slices`, `sync`, `testing` in the suites). No `go.sum`, no mocking library, no assertion library.
- **API shape**: generics only (`Set[T comparable]`, `SyncSet[T comparable]`, `ShardedSyncSet[T comparable]`), pointer receiver for every mutator and reader, free functions (`Union`, `Intersection`, `SyncUnion`, `SyncIntersection`, `ShardedUnion`, `ShardedIntersection`) for the operations that need no receiver. Both wrappers mirror the `Set` surface method for method, so the three stay comparable.
- **Docs are bilingual**: `docs/set-rehash-en.md` and `docs/set-rehash-es.md` are the same argument in two languages; a change to one is a change to both.
- **Published versions live in the tags**: the released versions are the repository's tags (`git tag -l`), and neither this file nor the README names one. A version written down here goes stale on the next release, and the tags are already the record.
- **The `go` directive is a consumer requirement**: it gates download, not only the local toolchain, so a `go 1.27.1` module makes older toolchains fetch a newer one or fail. Raising it is a breaking change for consumers; only raise it above the newest patch with a reason.

## Git Workflow

- **Every commit must be signed**: `commit.gpgsign` is set in this repo's local config, so plain `git commit` signs; in a fresh clone pass `-S` explicitly. If signing fails, stop and report; never fall back to an unsigned commit.
- **Every plan execution must end with commit and push**: after all changes are verified, commit and push to remote.
- **Commit messages are one short line**: a single concise subject, no body, no bullet points, no paragraph. Say what changed, not why.
- **No conventional commit prefixes** (fix:, feat:, refactor:, etc.). Commit messages go in plain natural language.

## Testing & QA

- Run everything: `go test ./...` and `go test -race ./...` (the wrapper's suite adds ~10 s, more under `-race`).
- Benchmarks live in `bench_test.go` and cover every row of the README's Performance table; run them with `go test -bench . -benchmem`. All of them use `b.Loop()`. Where per-iteration setup must be excluded from the measurement, stop the timer around the setup *inside* the body and leave it running at the end: `b.Loop` resets it on the first call (so setup before the loop is already excluded) and `StopTimer` poisons the loop on purpose if a body ends with it stopped. If a row of the table changes, the matching benchmark must change with it: the table must stay reproducible from `go test -bench .` alone.
- The suite runs against a reference model, not mocks: `map[int]struct{}` for membership and `slices`/`maps` for the algebra.
- Coverage is contractual, not structural. The fixed contracts are: every writer and reader against the zero value (`TestZeroValueWriters`, `TestZeroValueReaders`, `TestZeroValueSetAsArgument`), the full algebra against the model (`TestSetAlgebraMatchesModel`, `TestMutatorsMatchModel`), the `Copy`/`Clone` reservation contracts (`TestCopyAndCloneContracts`), `Rehash` landing on the smallest step (`TestRehashCompactsToSmallestStep`), `Difference` delivering an exactly-sized result (`TestDifferenceNeverOverAllocated`, `TestDifferenceDisjointKeepsStep`), self-operations (`TestSelfOperations`), the estimator still delivering on a step (`TestEstimatedSizingStillDeliversOnStep`), and the probe that sizes `Difference` (`TestDifferenceReservationTracksResult`, `TestDifferenceProbeDeliversExactResult`). Statement coverage is 100%; the tests that close the last branches are `TestNewFromSlicesCompactsOversizedHint`, `TestAddAllCompactsAfterResize`, `TestExtendProbeSkipsExistingElements`, `TestIntersectionProbeSeesAbsentElements` and `TestSampleCountExactUpToOverlapSample`.
- **Coverage is not sensitivity, and for the probes it is not enough.** A probe's branches can all be executed while the delivered set stays identical, because a wrong estimate only moves the reservation and `compact` repairs it — mutating the probes (making them report everything as new, or never probing exactly) leaves `go test ./...` green at 100% coverage. The estimate is observable only in *allocations* and time, not in contents; `bench_test.go` is what guards it (mutating `Extend`'s probe moves `BenchmarkExtend` from ~131 to ~196 allocs/op). Treat a green suite as proof of correctness, never as proof that the probe still estimates well.
- Tests may read unexported state (`s.capacity`) where the contract *is* the reservation; that is deliberate and is what pins the model to the behaviour.
- `theoretical_slots_test.go` is the one place the package is tied to the runtime it models: it reads the real slot count of `make(map, hint)` through `internal/runtime/maps`'s layout (behind `unsafe`, in a test file only — `set.go` must stay free of it) and compares it with `theoreticalSlots`. Run `SET_XCHECK_FULL=1 go test ./...` for the exhaustive sweep to 300,000; the default grid covers every capacity-step boundary and every crack in about a second. `TestRuntimeLayoutReadable` runs first and reports "the runtime layout changed" — update the reader in that case, don't touch `theoreticalSlots`. Changing the reader itself requires re-deriving the offsets from `map.go`/`table.go` (`dirPtr` is a `[]*table`; `table.capacity` is a `uint16` at offset 2; `dirLen == 0` means one 8-slot group).
- `TestTheoreticalSlotsIsNonDecreasing` and `TestTheoreticalSlotsAtMostDoublesPerStep` pin the sizing model itself up to 2,000,000, with the stated rationale that `needsRehash`'s threshold rule stops being valid if they fail.
- Adding a public operation means: a model comparison for its semantics, plus a reservation assertion if it returns or leaves a set whose size is knowable.
- **A probe's estimate is a reservation, never an answer.** `differenceReservation` and the other `sampleCount` callers may be wrong about size; the tests pin that the *contents* stay exact and the *delivered* set lands on its own step. When changing a probe, mutate the estimate (return the upper bound, or a fraction of it) and confirm the test fails — that is what proves the test covers the branch.
- `TestSingleAddNeverReserves` guarantees that a single `Add` never rebuilds, which is what makes the amortised cost claim in the README hold.

## License

GPLv3+. The full text is in [`LICENSE`](LICENSE), which is byte-identical to the one in `nvfp/`; the README's "License" section points at it. Do not add a license header to source files without asking.
