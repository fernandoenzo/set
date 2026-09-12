# set

`set` is an unordered set of comparable values, backed by a Go `map[T]struct{}`,
that keeps its memory proportional to the number of elements it holds — including
after large deletions.

```go
import "github.com/fernandoenzo/set"

s := set.New[string](16)
s.Add("a", "b", "c")

s.Remove("b")
s.Contains("a") // true
s.Len()         // 2
```

**Requirements.** Go 1.27 or later, as declared in `go.mod`. The module path is
`github.com/fernandoenzo/set`; pin a release with
`go get github.com/fernandoenzo/set@v1.0.0`. There are no dependencies to pull
in.

## The problem it solves

A Go map **grows on insert and never shrinks on delete**. A map that reached
100,000 elements and now holds 50,000 still occupies the tables allocated at its
peak, even though half the slots are dead. Nothing in the standard library
recovers that memory: `maps.Clone` replicates the internal structure as-is, so it
preserves the over-allocation.

`Set` rebuilds its map when doing so is worth it. Deletions that leave the
element count in the same capacity step do nothing; when the count falls far
enough that a rebuild pays off, the map is recreated with room for exactly the
current length, dropping the excess and the tombstones.

The rules are not heuristics tuned by trial and error. `docs/set-rehash-en.md`
(and `docs/set-rehash-es.md`) derive them and prove the bound: after a rebuild,
the expected extra memory is **below 0.4%** of the capacity step. The derivation
models Go's Swiss-table runtime exactly — 1024-slot tables, an insertion budget
of 896 keys per table, a power-of-two directory and a freshly seeded hash on
every rebuild — and the model is validated against the real runtime slot count
for every hint up to 300,000.

## Guarantees

- **The zero value is usable.** `var s set.Set[T]` is a valid empty set; the map
  is created by the first write. No constructor required.
- **Memory tracks length.** Deletions cannot leave the set holding memory for a
  peak it no longer has, beyond the proven 0.4% bound.
- **Results are sized exactly.** Operations that return a new set
  (`Difference`, `Intersection`, `Copy`) deliver a map whose reservation sits in
  the capacity step of their final length — never a step above — so no rebuild is
  needed after the fact.
- **No rebuild loops.** The trigger rules are threshold *crossings*, not zones,
  so each capacity step is rebuilt at most once per monotone descent.
- **Rebuilds are rare.** Rebuilding only happens when the element count crosses
  a step boundary or falls below 80% of a multi-table step's slots.
- **No background work, no goroutines, no finalizers, no `unsafe`.** One field of
  two words plus a map header; no external dependencies.

The set is **not** safe for concurrent use without external synchronisation, the
same as a Go map.

## API

### Construction

| Function | Description |
|---|---|
| `New[T](hint int) *Set[T]` | Empty set with room for `hint` elements (clamped to at least 1). |
| `NewFromSlices[T](lists ...[]T) *Set[T]` | Set of the distinct elements of all lists. Reserves for the total length and compacts if duplicates leave it below that step. |
| `var s Set[T]` | Zero value: a usable empty set. |

### Adding

| Method | Description |
|---|---|
| `Add(e ...T)` | Insert elements. A batch big enough to leave the current step is served by a single rebuild. |
| `AddSeq(it iter.Seq[T])` | Insert every element produced by an iterator. |
| `Extend(sets ...*Set[T])` | Add every element of the given sets. |

### Reading

| Method | Description |
|---|---|
| `Len() int` | Number of elements. |
| `Contains(v T) bool` | Membership test. |
| `GetAll() []T` | Elements as a new slice. |
| `IterAll() iter.Seq[T]` | Elements as an iterator (range-over-func). |
| `IsSubset(other *Set[T]) bool` | Every element of `s` is in `other`. |
| `Disjoint(other *Set[T]) bool` | No shared element. |
| `Equal(other *Set[T]) bool` | Same elements. |

### Removing

| Method | Description |
|---|---|
| `Remove(e ...T)` | Delete elements; absent ones are ignored. Never triggers a rebuild on its own. |
| `Subtract(sets ...*Set[T])` | Delete, in place, every element of the given sets. |
| `Intersects(sets ...*Set[T])` | Keep, in place, the elements present in every set. Despite the name, it mutates the receiver. |
| `Rehash()` | Rebuild the map sized for exactly the current length. |

### Derived sets

| Function / method | Description |
|---|---|
| `Union(sets ...*Set[T]) *Set[T]` | Elements of every set. |
| `Intersection(sets ...*Set[T]) *Set[T]` | Elements present in every set. |
| `Difference(other *Set[T]) *Set[T]` | `s − other`. |

### Copies

| Method | Description |
|---|---|
| `Copy() *Set[T]` | Independent copy with room for exactly its length: compact, no inherited over-allocation. |
| `Clone() *Set[T]` | Independent copy that keeps the source's reserved capacity, so it keeps growing at the same cost. |

Both return sets that share no state with the source. The difference is the
reservation: `Copy` trades growth headroom for memory, `Clone` trades memory for
growth headroom.

## What a rebuild costs

`Rehash` is the expensive operation: one pass over the elements, a fresh map and
a new hash seed. The 0.4% bound quantifies what it leaves behind; the crossing
rule guarantees you pay for it at most once per step boundary crossed, so the
amortised cost per deletion is `O(1)` with a small constant.

`Difference` is worth calling out because it is the operation where a naive
implementation pays twice. The delivered set is sized from the exact result
count, using one extra pass over the smaller operand to count the intersection,
so the result never needs a rebuild afterwards. When the subtraction provably
cannot drop a step, the cheaper copy-and-delete path is used instead.

## Performance

Measured on an Intel i9-14900KF, Go 1.27, average of repeated runs. Numbers are
indicative; benchmark on your own workload.

| Operation | Cost |
|---|---|
| `Add` on the zero value | 2 allocations, one 8-slot map |
| `Add` of 1000 elements | 6 allocations |
| `Contains` | one map lookup, no allocation |
| `IsSubset` / `Disjoint` | no allocation |
| `GetAll` | one allocation (the result slice) |
| `Difference` | allocation proportional to the result, never to the source |

Hot loops range directly over the internal map rather than going through
iterators, and results are pre-sized, so the common paths do not allocate beyond
the container itself. `go test -bench .` in the repository reports the numbers
for the current machine.

## Verifying the design

```sh
go test ./...          # behavioural suite, exhaustive algebra against a model
go test -race ./...    # no shared state, but the suite is race-clean
go vet ./...
```

The test suite fixes the observable contracts: every reader and writer against
the zero value, the full algebra against a reference model, the `Copy`/`Clone`
reservation contracts, `Rehash` landing on the smallest step, `Difference`
delivering an exactly-sized result, and self-operations such as `s.Extend(s)`.

## Documentation

- `docs/set-rehash-en.md` — English: full derivation, with proofs.
- `docs/set-rehash-es.md` — Spanish: the same document.

Both cover the reservation model, why the trigger rule is forced to be a
threshold crossing, the Excess Theorem and its 0.4% bound, termination, and the
numerical checks against the real runtime.
