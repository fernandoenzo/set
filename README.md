# set

`set` is an unordered set of comparable values, backed by a Go `map[T]struct{}`,
that keeps its memory proportional to the number of elements it holds — including
after large deletions.

```go
import "github.com/fernandoenzo/set"

s := set.New[string](16)
s.Add("a")
s.AddAll("b", "c")

s.Remove("b")
s.Contains("a") // true
s.Len()         // 2
```

**Requirements.** Go 1.27.1 or later, as declared in `go.mod`. The module path is
`github.com/fernandoenzo/set`; pin a release with `go get
github.com/fernandoenzo/set@v1.1.0`. There are no dependencies to pull in.

## Table of contents

- [The problem it solves](#the-problem-it-solves)
- [Guarantees](#guarantees)
  - [Concurrency](#concurrency)
- [API](#api)
  - [Construction](#construction)
  - [Adding](#adding)
  - [Reading](#reading)
  - [Removing](#removing)
  - [Derived sets](#derived-sets)
  - [Copies](#copies)
- [What a rebuild costs](#what-a-rebuild-costs)
- [Performance](#performance)
- [Why the probe samples 256 elements](#why-the-probe-samples-256-elements)
  - [The sampling distribution is hypergeometric](#the-sampling-distribution-is-hypergeometric)
  - [From hypergeometric to binomial to normal](#from-hypergeometric-to-binomial-to-normal)
  - [Where 256 comes from: Cochran's formula](#where-256-comes-from-cochrans-formula)
  - [Why small sets opt out](#why-small-sets-opt-out)
  - [A caveat on exactness](#a-caveat-on-exactness)
- [Verifying the design](#verifying-the-design)
- [Documentation](#documentation)

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
  (`Difference`, `Intersection`, `Extend`, `Copy`) deliver a map whose
  reservation sits in the capacity step of their final length — never a step
  above — so no rebuild is needed after the fact. The two that cannot know their
  result in advance estimate it from a probe of the operands and compact the
  delivery; the estimate decides how much work is done, never what the set
  contains.
- **No rebuild loops.** The trigger rules are threshold *crossings*, not zones,
  so each capacity step is rebuilt at most once per monotone descent.
- **Rebuilds are rare.** Rebuilding only happens when the element count crosses
  a step boundary or falls below 80% of a multi-table step's slots.
- **No background work, no goroutines, no finalizers, no `unsafe`.** The type is
  a map header plus an `int`; no external dependencies.

### Concurrency

The set is **not** safe for concurrent use without external synchronisation, the
same as a Go map. That is a deliberate design decision, not an omission.

An internal lock cannot be added without breaking the API and the performance
guarantees, for three measured reasons:

- **It would deadlock on self-operations.** `s.Extend(s)` and `s.Retain(s)`
  re-enter the receiver while it is locked. A non-recursive mutex blocks and a
  recursive one hides the aliasing instead of fixing it.
- **It would have to copy the lock.** `resize` replaces the receiver wholesale
  (`*s = *rebuilt`), which copies the struct. With a mutex inside, `go vet`
  rejects it: `assignment copies lock value`.
- **It would serialise reads and collapse parallel scaling.** Reads are lock-free
  today and parallelise for free. Measured with 32 goroutines reading from a
  1000-element set:

  | Mechanism | Single-threaded | 32 goroutines |
  |---|---|---|
  | lock-free (this package) | 3.5 ns | **0.26 ns** |
  | caller-held `sync.RWMutex` | 10.4 ns | 38 ns |
  | internal `sync.Mutex` | 10.7 ns | 150–246 ns |
  | `chan struct{}` of capacity 1 | 23.8 ns | 119–147 ns |
  | `atomic.Pointer` to an immutable snapshot | 3.6 ns | 0.27 ns |

  A channel used as a semaphore is strictly worse than a mutex — 7× slower
  uncontended, no better under contention — because a capacity-1 channel is a
  mutex with more machinery. An internal lock also serialises readers, which is
  exactly the workload sets face most often.

A lock inside the type would also make every caller pay for concurrency they may
not need, and still could not make a read-then-write sequence atomic: `Contains`
followed by `Add` would remain a race, because the critical section has to span
both calls.

**Use a lock held by the caller instead**, which knows the access pattern:

```go
type SharedSet struct {
	mu sync.RWMutex
	s  *set.Set[int]
}

func (x *SharedSet) Contains(v int) bool {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.s.Contains(v)
}
```

Readers then run in parallel, writers are serialised, and callers that do not
share the set pay nothing.

If you need lock-free concurrent reads at scale, the shape that works is an
immutable snapshot behind an `atomic.Pointer`, rebuilt copy-on-write for
writes — the last row of the table above. It keeps reads as fast as the
lock-free case, but writes become `O(n)` and reads observe a snapshot rather
than the latest element. That is a different contract and belongs in a separate
wrapper, not in this type.

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
| `Add(v T)` | Insert one element. |
| `AddAll(e ...T)` | Insert elements. A batch big enough to leave the current step is served by a single rebuild. |
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
| `Retain(sets ...*Set[T])` | Keep, in place, the elements present in every set. |
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

The three binary set operations are worth calling out because they are where a
naive implementation pays twice. Each of them has an unknown result to size a map
for, and each resolves it differently: `Difference` reserves the upper bound and
lets the compaction take back the difference, while `Extend` and `Intersection`
probe a few hundred elements of their operands to estimate how much will survive
before reserving. All three deliver a set already on the step of its final
length, so none of them needs a rebuild afterwards. The probe's sample size and
its error are derived in "Why the probe samples 256 elements" below; the rehash
rules themselves in `docs/set-rehash-en.md`.

`Difference` also keeps the cheap path for the case where no step can be crossed:
when the subtraction provably cannot drop the result far enough, it copies and
deletes instead of walking the misses into a fresh map.

## Performance

Measured on an Intel i9-14900KF, Go 1.27, average of repeated runs. Numbers are
indicative; benchmark on your own workload.

| Operation | Cost |
|---|---|
| `Add` on the zero value | 2 allocations, one 8-slot map |
| `AddAll` of 1000 elements | 6 allocations |
| `Contains` | one map lookup, no allocation |
| `IsSubset` / `Disjoint` | no allocation |
| `GetAll` | one allocation (the result slice) |
| `Difference` | allocation proportional to the result, never to the source |

Hot loops range directly over the internal map rather than going through
iterators, and results are pre-sized, so the common paths do not allocate beyond
the container itself. `go test -bench .` in the repository reports the numbers
for the current machine.

## Why the probe samples 256 elements

`Extend` and `Intersection` must reserve a map for a result whose size they
cannot know: how many of an operand's elements are new, or survive the
intersection, depends on an overlap that is only visible once the pass is made.
Undersizing costs a runtime rehash; sizing for the whole operand over-allocates
by up to two capacity steps and pays the same rehash. So they estimate the
result from a sample of the operand — `sampleCount` — and size the map from the
estimate.

The estimate is the sample proportion scaled up:

$$\hat{k}\ =\ N\cdot\frac{C}{s},\qquad s=\texttt{overlapSample}=256$$

### The sampling distribution is hypergeometric

The $s$ probed elements are drawn **without replacement** from a finite
population of $N$: iterating a map visits each element once, so the probe is a
sample without replacement by construction. That is a hypergeometric experiment,
and it is what governs the estimate's error.

In the parametrisation $H(N,n,p)$ — total population, sample size, and the
proportion belonging to the subpopulation of interest:

| Parameter | Value in `sampleCount` |
|---|---|
| $N$ | population: `small.Len()`, the set being probed |
| $n$ | sample size: `overlapSample` = 256 |
| $p$ | proportion of $N$ satisfying the predicate |

The population splits into two exclusive subpopulations: elements satisfying the
predicate (`Intersection`: present in every other set; `Extend`: new to `s` and
to the arguments already folded) and those that do not. Since $p=k/N$ for an
integer count $k$, $Np$ is always an integer and the parametrisations
$H(N,K,n)$ and $H(N,n,p)$ coincide.

> **Notation.** This section uses $N$ for the population, $n$ for the sample and
> $p$ for the proportion. Elsewhere the *size of a set* is written `Len()`.

For $C\sim H(N,n,p)$:

$$\mathbb{E}[C]=np,\qquad
\mathrm{Var}[C]=np(1-p)\,\frac{N-n}{N-1}$$

so $\hat k=NC/n$ is unbiased with coefficient of variation

$$\mathrm{CV}[\hat{k}]
=\underbrace{\sqrt{\frac{1-p}{p\,n}}}_{\text{proportion's error}}\;\cdot\;
\underbrace{\sqrt{\frac{N-n}{N-1}}}_{\text{finite population correction}}$$

### From hypergeometric to binomial to normal

The finite population correction is the only thing separating the hypergeometric
from the binomial. In the regime `sampleCount` runs — $n=256$ against
$N\ge4096$, the floor below — the correction is at worst $0{,}968$, i.e. $6\%$
low in variance and so $3\%$ low in standard error. Dropping it therefore
*overstates* the error, which is the safe direction: $\mathrm{Bin}(n,p)$ is
indistinguishable from $H(N,n,p)$ there, and the standard error is taken as
$\sqrt{p(1-p)/n}$.

The binomial is then approximated by the normal, which is what makes the bound a
one-line calculation once $Z=2$ is fixed. That approximation has a known edge:
it underestimates far tails, which is exactly where $p$ sits near the ends of
the interval. It is acceptable here because those are the cases where the
estimate does not need to be good — at $p\to0$ and $p\to1$ the rounding to a
capacity step is exact whatever the sample says. For $p=1/2$, where the
criterion binds, the binomial is at its closest to normal.

The left factor peaks at $p=1/2$ and falls as the overlap grows:

| $p$ | CV | $2\sigma$ |
|---|---|---|
| $1\%$ | $62\%$ | $124\%$ |
| $10\%$ | $19\%$ | $37\%$ |
| $50\%$ | $6{,}3\%$ | $12{,}5\%$ |
| $100\%$ | $0$ | $0$ |

The alarming-looking small-$p$ rows are in fact harmless, and the reason is that
**the relevant quantity is not the error in $p$ but where the estimate lands
relative to the capacity step $T(N)$**. Writing the estimate as $N\theta$ and the
truth as $Np$, a rebuild happens when $T(N\theta)\neq T(Np)$. Since the step is a
power of two, that needs $N\theta$ to leave the step of $Np$; for any $p$ below
roughly $1/4$ both the estimate and the truth already sit *below* the smallest
step a map of $N$ keys can land on, so all of them round to the same place. The
probe only needs accuracy in the band where the outcome sits inside the step, and
there $p\ge1/4$, hence

$$\mathrm{CV}\le\sqrt{\frac{1-p}{p\,n}}\Big|_{p=1/4}=\sqrt{\frac{3}{n}}$$

### Where 256 comes from: Cochran's formula

The finite-population sample size formula (Cochran, *Sampling Techniques*, 1977,
eq. 5.10) is

$$n\ =\ \frac{n_0}{1+\dfrac{n_0-1}{N}},
\qquad n_0=\frac{Z^2\,p(1-p)}{e^2}$$

and its correction factor $1/(1+(n_0-1)/N)$ **is** the finite population
correction derived above from the hypergeometric variance: the same quantity
reached from two directions. Two of the three inputs are fixed by the problem
rather than chosen by the analyst:

- **$Z=2$**, as a "rare, not impossible" criterion. This is *not* a confidence
  level. A wrong estimate is corrected by `compact()` with a rebuild — a cost,
  never a wrong answer — so the sample must make that rebuild rare, not bound it
  in probability.
- **$e=41\%$**, imposed by the data structure: to land on the same step the
  estimate must stay within a factor of $2^{1/2}$ of the truth, i.e. a relative
  error under $\sqrt2-1\approx41\%$.
- **$p=1/4$**, the worst case in the band that matters, as derived above.

$$e=0{,}41p=0{,}1025,\qquad
n=\frac{Z^2p(1-p)}{e^2}=\frac{4\cdot0{,}1875}{0{,}1025^2}=71{,}38$$

`overlapSample` is **256**, that bound with a factor of $3.6$ of margin. The
margin is deliberate: the hypergeometric tail near $p=1/2$ is only approximately
normal, and $2\sigma$ is a criterion rather than a guarantee. The cost is
negligible — 256 lookups against a pass of $N$; at $N=10^6$ that is $0.026\%$ of
the work being sized.

### Why small sets opt out

`samplingFloor` is `overlapSample * 16 = 4096`. The probe pays for itself in
proportion to what it avoids: with $n=256$ and $N=4096$ it adds at most $6\%$ to
the pass, and below that it grows as $1/N$ while the mistake it prevents shrinks
as $N$. The measured crossover is near $N=10^3$, where the probe costs more than
the sizing it saves.

Below the floor the finite population correction stops being negligible in the
other direction too — at $N$ approaching $n$ it would tighten the bound rather
than loosen it — so the floor is also what keeps the binomial substitution
harmless.

`Intersection` uses the same floor although it could afford a smaller one, but
for a different reason it cannot: it has no counting pass for the probe to
replace, so the probe is pure addition there and its threshold must clear the
whole cost of a wrong reservation rather than a fraction of it.

### A caveat on exactness

Go does not hand out a uniformly random subset of size $n$: it picks a random
bucket and offset and walks deterministically from there. The start point is
uniform, the subset is not, so the hypergeometric describes the probe well but
not as a formal identity. Nothing in the design depends on exactness — the
bound needs the estimator to be approximately unbiased, and `compact()` corrects
any deviation — but the distribution should not be read as a guarantee.

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
