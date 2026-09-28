package set

import (
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
	"time"
)

// --- helpers ---------------------------------------------------------------

func shardSetOf(vals ...int) *ShardedSyncSet[int] {
	return NewShardedSyncFromSlices(vals)
}

// assertShardMatches compares a ShardedSyncSet with the model, order-free, and
// checks the invariant Len is built on: every shard's counter equals the length
// of its map. Every test that reads a set goes through here, so a mutation path
// that forgets to maintain the counter fails immediately.
func assertShardMatches(t *testing.T, s *ShardedSyncSet[int], want map[int]struct{}) {
	t.Helper()
	for i := range s.shards {
		if got, real := int(s.shards[i].count.Load()), s.shards[i].set.Len(); got != real {
			t.Fatalf("shard %d counter drifted: count=%d, map holds %d", i, got, real)
		}
	}
	if s.Len() != len(want) {
		t.Fatalf("Len() = %d, want %d", s.Len(), len(want))
	}
	for _, probe := range []int{-1, 0, 1, 2, 3, 4, 5, 99} {
		_, inWant := want[probe]
		if s.Contains(probe) != inWant {
			t.Fatalf("Contains(%d) = %v, want %v", probe, s.Contains(probe), inWant)
		}
	}
	sortedEqual(t, s.GetAll(), want)
	if !s.Equal(shardSetOf(keysOf(want)...)) {
		t.Fatalf("Equal(model) = false for %v", s.GetAll())
	}
	if !s.Copy().Equal(s) {
		t.Fatalf("Copy() lost elements")
	}
	if !s.Clone().Equal(s) {
		t.Fatalf("Clone() lost elements")
	}
	if !s.ToSet().Equal(setOf(keysOf(want)...)) {
		t.Fatalf("ToSet() diverged from the model")
	}
}

// --- shard layout ----------------------------------------------------------

// The multi-set operations decompose shard by shard. That is only sound if a
// value's shard index is a pure function of the value, so that shard i of one
// set holds exactly the same values as shard i of another.
func TestShardIndexIsPerValue(t *testing.T) {
	vals := []int{0, 1, 2, 3, 7, 64, 65, 127, 128, 1_000, 12_345, 99_999, -1, -64}
	for _, v := range vals {
		idx := shardIndex(v)
		if idx >= shardCount {
			t.Fatalf("shardIndex(%d) = %d, out of range", v, idx)
		}
		if shardIndex(v) != idx {
			t.Fatalf("shardIndex(%d) is not deterministic", v)
		}
	}

	// The same set of values hashed into two different sets lands in the same
	// shards, and a multi-set operation sees each shard pair aligned.
	a, b := shardSetOf(vals...), shardSetOf(vals...)
	for i := range a.shards {
		if !a.shards[i].set.Equal(&b.shards[i].set) {
			t.Fatalf("shard %d diverged between two identical sets", i)
		}
	}

	// A value present in one set is present in the same shard of the other.
	probe := 12345
	idx := shardIndex(probe)
	if !a.shards[idx].set.Contains(probe) {
		t.Fatalf("value %d not in its own shard %d", probe, idx)
	}
}

// The size invariant that keeps two shards off one cache line is pinned in
// theoretical_slots_test.go, the only file allowed to import unsafe.

// --- zero value ------------------------------------------------------------

func TestShardZeroValueWriters(t *testing.T) {
	other := shardSetOf(1, 2, 3)
	empty := NewShardedSync[int](0)

	writers := []struct {
		name    string
		run     func(s *ShardedSyncSet[int])
		wantLen int
	}{
		{"Add(1)", func(s *ShardedSyncSet[int]) { s.Add(1) }, 1},
		{"AddAll()", func(s *ShardedSyncSet[int]) { s.AddAll() }, 0},
		{"AddAll(1)", func(s *ShardedSyncSet[int]) { s.AddAll(1) }, 1},
		{"AddAll(3)", func(s *ShardedSyncSet[int]) { s.AddAll(1, 2, 3) }, 3},
		{"AddAll(1000)", func(s *ShardedSyncSet[int]) { s.AddAll(makeSeq(1000)...) }, 1000},
		{"AddSeq", func(s *ShardedSyncSet[int]) { s.AddSeq(slices.Values([]int{7, 8})) }, 2},
		{"AddSeq(empty)", func(s *ShardedSyncSet[int]) { s.AddSeq(func(func(int) bool) {}) }, 0},
		{"Extend()", func(s *ShardedSyncSet[int]) { s.Extend() }, 0},
		{"Extend(empty)", func(s *ShardedSyncSet[int]) { s.Extend(empty) }, 0},
		{"Extend(1)", func(s *ShardedSyncSet[int]) { s.Extend(shardSetOf(1)) }, 1},
		{"Extend(many sets)", func(s *ShardedSyncSet[int]) { s.Extend(shardSetOf(1), empty, shardSetOf(2, 3)) }, 3},
		{"Extend(zero value)", func(s *ShardedSyncSet[int]) { var z ShardedSyncSet[int]; s.Extend(&z) }, 0},
		{"Extend(self)", func(s *ShardedSyncSet[int]) { s.Extend(s) }, 0},
		{"Retain(self)", func(s *ShardedSyncSet[int]) { s.Retain(s) }, 0},
		{"Subtract(self)", func(s *ShardedSyncSet[int]) { s.Subtract(s) }, 0},
		{"Remove(absent)", func(s *ShardedSyncSet[int]) { s.Remove(1, 2) }, 0},
		{"Retain()", func(s *ShardedSyncSet[int]) { s.Retain() }, 0},
		{"Subtract()", func(s *ShardedSyncSet[int]) { s.Subtract() }, 0},
		{"Subtract(other)", func(s *ShardedSyncSet[int]) { s.Subtract(other) }, 0},
		{"Retain(other)", func(s *ShardedSyncSet[int]) { s.Retain(other) }, 0},
		{"Rehash()", func(s *ShardedSyncSet[int]) { s.Rehash() }, 0},
	}
	for _, c := range writers {
		t.Run(c.name, func(t *testing.T) {
			var s ShardedSyncSet[int]
			mustFinish(t, c.name, 5*time.Second, func() {
				if msg, panicked := tryRun(func() { c.run(&s) }); panicked {
					t.Errorf("panic on zero value: %v", msg)
				}
			})
			if s.Len() != c.wantLen {
				t.Fatalf("Len() = %d, want %d", s.Len(), c.wantLen)
			}
			if msg, panicked := tryRun(func() { s.Add(1) }); panicked {
				t.Fatalf("set unusable after write: %v", msg)
			}
			if !s.Contains(1) {
				t.Fatalf("Contains(1) = false after Add")
			}
		})
	}
}

func TestShardZeroValueReaders(t *testing.T) {
	var s ShardedSyncSet[int]
	other := shardSetOf(1, 2)

	if s.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", s.Len())
	}
	if s.Contains(1) {
		t.Fatalf("Contains(1) = true on empty set")
	}
	if len(s.GetAll()) != 0 {
		t.Fatalf("GetAll() = %v, want empty", s.GetAll())
	}
	n := 0
	for range s.IterAll() {
		n++
	}
	if n != 0 {
		t.Fatalf("IterAll yielded %d elements on the zero value, want 0", n)
	}
	if !s.IsSubset(other) {
		t.Fatalf("empty set is not a subset of %v", other.GetAll())
	}
	if !s.IsSubset(shardSetOf(1)) {
		t.Fatalf("the empty set is a subset of every set, including %v", shardSetOf(1).GetAll())
	}
	if !s.Disjoint(other) {
		t.Fatalf("empty set is not disjoint from %v", other.GetAll())
	}
	if s.Equal(other) {
		t.Fatalf("empty set equals a non-empty one")
	}
	if s.Copy().Len() != 0 || s.Clone().Len() != 0 {
		t.Fatalf("Copy or Clone of the zero value is not empty")
	}
	if d := s.Difference(other); d.Len() != 0 {
		t.Fatalf("empty − other = %v, want empty", d.GetAll())
	}
	if !s.ToSet().Equal(setOf()) {
		t.Fatalf("ToSet() of the zero value is not empty")
	}
}

func TestShardZeroValueSetAsArgument(t *testing.T) {
	var z ShardedSyncSet[int]
	other := shardSetOf(1, 2)

	if u := ShardedUnion(&z, other); !u.Equal(other) {
		t.Fatalf("Union(zero, other) = %v, want %v", u.GetAll(), other.GetAll())
	}
	if i := ShardedIntersection(&z, other); i.Len() != 0 {
		t.Fatalf("Intersection(zero, other) = %v, want empty", i.GetAll())
	}
	if d := other.Difference(&z); !d.Equal(other) {
		t.Fatalf("other - zero = %v, want %v", d.GetAll(), other.GetAll())
	}
	s := shardSetOf(1, 2, 3)
	s.Retain(&z)
	assertShardMatches(t, s, modelOf())
}

// Model helpers for the three algebra results, so the comparisons read like the
// algebra rather than like three hand-rolled loops.
func unionModel(a, b map[int]struct{}) map[int]struct{} {
	out := modelOf()
	for v := range a {
		out[v] = struct{}{}
	}
	for v := range b {
		out[v] = struct{}{}
	}
	return out
}

func intersectModel(a, b map[int]struct{}) map[int]struct{} {
	out := modelOf()
	for v := range a {
		if _, in := b[v]; in {
			out[v] = struct{}{}
		}
	}
	return out
}

func differenceModel(a, b map[int]struct{}) map[int]struct{} {
	out := modelOf()
	for v := range a {
		if _, in := b[v]; !in {
			out[v] = struct{}{}
		}
	}
	return out
}

// --- algebra against the model ---------------------------------------------

func TestShardSetAlgebraMatchesModel(t *testing.T) {
	universe := []int{0, 1, 2}
	subsets := make([][]int, 0, 8)
	for mask := range 1 << len(universe) {
		var vals []int
		for i, v := range universe {
			if mask&(1<<i) != 0 {
				vals = append(vals, v)
			}
		}
		subsets = append(subsets, vals)
	}
	for _, a := range subsets {
		for _, b := range subsets {
			ma, mb := modelOf(a...), modelOf(b...)
			sa, sb := shardSetOf(a...), shardSetOf(b...)

			wantUnion := modelOf(a...)
			wantInter := modelOf()
			wantDiff := modelOf()
			for v := range mb {
				wantUnion[v] = struct{}{}
				if _, in := ma[v]; in {
					wantInter[v] = struct{}{}
				}
			}
			for v := range ma {
				if _, in := mb[v]; !in {
					wantDiff[v] = struct{}{}
				}
			}

			assertShardMatches(t, ShardedUnion(sa, sb), wantUnion)
			assertShardMatches(t, ShardedIntersection(sa, sb), wantInter)
			assertShardMatches(t, sa.Difference(sb), wantDiff)

			if got, want := sa.IsSubset(sb), isSubsetModel(ma, mb); got != want {
				t.Fatalf("IsSubset(%v, %v) = %v, want %v", a, b, got, want)
			}
			if got, want := sa.Disjoint(sb), len(wantInter) == 0; got != want {
				t.Fatalf("Disjoint(%v, %v) = %v, want %v", a, b, got, want)
			}
			if got, want := sa.Equal(sb), len(ma) == len(mb) && isSubsetModel(ma, mb); got != want {
				t.Fatalf("Equal(%v, %v) = %v, want %v", a, b, got, want)
			}
		}
	}
}

// Values are sharded by a process-wide hash seed, so a value can land in any of
// the 64 shards. The model comparison has to keep holding when the sets are
// large enough to populate every shard, which is what this pins.
func TestShardSetAlgebraAcrossAllShards(t *testing.T) {
	a := NewShardedSync[int](0)
	model := modelOf()
	touched := map[uint64]bool{}
	for i := range 4_000 {
		a.Add(i * 7)
		model[i*7] = struct{}{}
		touched[shardIndex(i*7)] = true
	}
	if len(touched) < shardCount/2 {
		t.Fatalf("only %d of %d shards were exercised; the test is not covering the layout", len(touched), shardCount)
	}
	assertShardMatches(t, a, model)

	b := NewShardedSyncFromSlices(makeSeq(8_000))
	wantB := modelOf(makeSeq(8_000)...)
	assertShardMatches(t, b, wantB)

	assertShardMatches(t, ShardedUnion(a, b), unionModel(model, wantB))
	assertShardMatches(t, ShardedIntersection(a, b), intersectModel(model, wantB))
	assertShardMatches(t, a.Difference(b), differenceModel(model, wantB))
}

func TestShardIntersectionArity(t *testing.T) {
	a, b, c := shardSetOf(1, 2, 3), shardSetOf(2, 3, 4), shardSetOf(3, 4, 5)

	if got := ShardedIntersection[int](); got.Len() != 0 {
		t.Fatalf("ShardedIntersection() = %v, want empty", got.GetAll())
	}
	assertShardMatches(t, ShardedIntersection(a), modelOf(1, 2, 3))
	assertShardMatches(t, ShardedIntersection(a, b), modelOf(2, 3))
	assertShardMatches(t, ShardedIntersection(a, b, c), modelOf(3))
	assertShardMatches(t, ShardedIntersection(a, a, a), modelOf(1, 2, 3))
	assertShardMatches(t, ShardedIntersection(a, a, b), modelOf(2, 3))
	assertShardMatches(t, ShardedUnion[int](), modelOf())
	assertShardMatches(t, ShardedUnion(a), modelOf(1, 2, 3))
	assertShardMatches(t, ShardedUnion(a, a, b), modelOf(1, 2, 3, 4))
}

func TestShardMutatorsMatchModel(t *testing.T) {
	rng := rand.New(rand.NewPCG(0xC0FFEE, 0xBEEF))
	for range 200 {
		s := NewShardedSync[int](0)
		model := modelOf()
		for range 40 {
			other := shardSetOf(randVals(rng, 4)...)
			switch rng.IntN(6) {
			case 0:
				vals := randVals(rng, 3)
				s.AddAll(vals...)
				for _, v := range vals {
					model[v] = struct{}{}
				}
			case 1:
				vals := randVals(rng, 3)
				s.AddSeq(slices.Values(vals))
				for _, v := range vals {
					model[v] = struct{}{}
				}
			case 2:
				vals := randVals(rng, 3)
				s.Remove(vals...)
				for _, v := range vals {
					delete(model, v)
				}
			case 3:
				s.Extend(other)
				for v := range other.IterAll() {
					model[v] = struct{}{}
				}
			case 4:
				s.Subtract(other)
				for v := range other.IterAll() {
					delete(model, v)
				}
			case 5:
				s.Retain(other)
				for v := range model {
					if !other.Contains(v) {
						delete(model, v)
					}
				}
			}
			assertShardMatches(t, s, model)
		}
	}
}

// --- self and aliased operations -------------------------------------------

func TestShardSelfOperations(t *testing.T) {
	s := shardSetOf(1, 2, 3, 4, 5)
	model := modelOf(1, 2, 3, 4, 5)

	mustFinish(t, "Extend(self)", 5*time.Second, func() { s.Extend(s) })
	assertShardMatches(t, s, model)

	mustFinish(t, "Retain(self)", 5*time.Second, func() { s.Retain(s) })
	assertShardMatches(t, s, model)

	mustFinish(t, "Extend(self, self)", 5*time.Second, func() { s.Extend(s, s) })
	assertShardMatches(t, s, model)

	mustFinish(t, "Extend(other, self)", 5*time.Second, func() { s.Extend(shardSetOf(6), s) })
	assertShardMatches(t, s, modelOf(1, 2, 3, 4, 5, 6))

	mustFinish(t, "Subtract(self)", 5*time.Second, func() { s.Subtract(s) })
	assertShardMatches(t, s, modelOf())

	s = shardSetOf(1, 2, 3)
	mustFinish(t, "Subtract(other, self, other)", 5*time.Second, func() {
		s.Subtract(shardSetOf(2), s, shardSetOf(3))
	})
	assertShardMatches(t, s, modelOf())

	s = shardSetOf(1, 2, 3)
	mustFinish(t, "Retain(self, other)", 5*time.Second, func() { s.Retain(s, shardSetOf(2, 3)) })
	assertShardMatches(t, s, modelOf(2, 3))

	s = shardSetOf(1, 2, 3)
	mustFinish(t, "Difference(self)", 5*time.Second, func() {
		if d := s.Difference(s); d.Len() != 0 {
			t.Errorf("s − s = %v, want empty", d.GetAll())
		}
	})
	if !s.IsSubset(s) || !s.Equal(s) {
		t.Fatalf("self predicates are false for a non-empty set")
	}
	if s.Disjoint(s) {
		t.Fatalf("s is disjoint from itself but is not empty")
	}
}

// Inverted argument orders between two goroutines are the deadlock shape the
// global lock order exists for. The sharded version takes a whole set's shards
// as one contiguous block of that order.
func TestShardInvertedOrderDoesNotDeadlock(t *testing.T) {
	a, b := shardSetOf(makeSeq(2_000)...), shardSetOf(disjointSeq(2_000)...)

	mustFinish(t, "a.Extend(b) || b.Extend(a)", 10*time.Second, func() {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); a.Extend(b) }()
		go func() { defer wg.Done(); b.Extend(a) }()
		wg.Wait()
	})
	// Extender en ambos sentidos deja la unión en los dos: 2.000 + 2.000.
	if a.Len() != 4_000 || b.Len() != 4_000 {
		t.Fatalf("after extending both ways a=%d b=%d, want 4000 each", a.Len(), b.Len())
	}
	if !a.Equal(b) {
		t.Fatalf("after extending both ways the two sets differ: a=%d b=%d elements", a.Len(), b.Len())
	}

	mustFinish(t, "a.Difference(b) || b.Difference(a)", 10*time.Second, func() {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); a.Difference(b) }()
		go func() { defer wg.Done(); b.Difference(a) }()
		wg.Wait()
	})

	// Writers committing while the inverted-order reads run.
	mustFinish(t, "reads under writers", 10*time.Second, func() {
		stop := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(3)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					a.IsSubset(b)
					b.IsSubset(a)
				}
			}
		}()
		go func() {
			defer wg.Done()
			for i := range 20_000 {
				a.Add(100_000 + i)
			}
		}()
		go func() {
			defer wg.Done()
			for i := range 20_000 {
				b.Add(1_000_000 + i)
			}
		}()
		time.Sleep(50 * time.Millisecond)
		close(stop)
		wg.Wait()
	})
}

// --- iterators -------------------------------------------------------------

func TestShardIteratorContracts(t *testing.T) {
	s := shardSetOf(makeSeq(500)...)
	model := modelOf(makeSeq(500)...)

	got := map[int]struct{}{}
	for v := range s.IterAll() {
		got[v] = struct{}{}
	}
	if len(got) != len(model) {
		t.Fatalf("IterAll yielded %d elements, want %d", len(got), len(model))
	}

	// IterSnapshot must let the body touch the set, mutations included.
	snap := s.IterSnapshot()
	for v := range snap {
		s.Remove(v)
	}
	assertShardMatches(t, s, modelOf())

	// Breaking out of IterAll must release every shard's lock.
	s = shardSetOf(makeSeq(500)...)
	for range s.IterAll() {
		break
	}
	mustFinish(t, "write after early break", 5*time.Second, func() { s.Add(-1) })
	if !s.Contains(-1) {
		t.Fatalf("Add after an early break from IterAll did not land")
	}

	// Mutating through AddSeq: the sequence runs without any lock held.
	s = NewShardedSync[int](0)
	s.AddSeq(slices.Values(makeSeq(300)))
	assertShardMatches(t, s, modelOf(makeSeq(300)...))

	// A sequence that calls back into the set must not deadlock.
	s = shardSetOf(1)
	s.AddSeq(func(yield func(int) bool) {
		for i := 10; i < 20; i++ {
			if s.Contains(i) {
				continue
			}
			if !yield(i) {
				return
			}
		}
	})
	if s.Len() != 11 {
		t.Fatalf("AddSeq with a re-entrant sequence gave %d elements, want 11", s.Len())
	}
}

// --- concurrency -----------------------------------------------------------

// Concurrent writers on disjoint ranges must all land: the sharded locks must
// not drop an insert.
func TestShardConcurrentWritersLoseNothing(t *testing.T) {
	const (
		goroutines = 32
		perG       = 2_000
	)
	s := NewShardedSync[int](0)
	model := modelOf()
	var mu sync.Mutex

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := range goroutines {
		go func() {
			defer wg.Done()
			for i := range perG {
				v := g*perG + i
				s.Add(v)
				mu.Lock()
				model[v] = struct{}{}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	assertShardMatches(t, s, model)
}

// Readers must never see an element disappear, and the set must be coherent
// when the writers stop.
func TestShardMixedReadersAndWriters(t *testing.T) {
	const (
		readers = 8
		writers = 8
		rounds  = 20_000
		initial = 2_000
	)
	s := shardSetOf(makeSeq(initial)...)

	var wg sync.WaitGroup
	wg.Add(writers + readers)
	for w := range writers {
		go func() {
			defer wg.Done()
			for i := range rounds {
				s.Add(initial + w*rounds + i)
			}
		}()
	}
	for range readers {
		go func() {
			defer wg.Done()
			for i := range rounds {
				if v := i % initial; !s.Contains(v) {
					t.Errorf("reader lost preexisting element %d", v)
					return
				}
			}
		}()
	}
	wg.Wait()
	if got, want := s.Len(), initial+writers*rounds; got != want {
		t.Fatalf("Len() = %d, want %d", got, want)
	}
}

// Writers doing Add followed by Remove of their own value must leave the set
// exactly as they found it.
func TestShardAddRemoveChurn(t *testing.T) {
	const (
		goroutines = 32
		rounds     = 20_000
	)
	s := shardSetOf(makeSeq(1_000)...)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := range goroutines {
		go func() {
			defer wg.Done()
			v := 10_000 + g
			for range rounds {
				s.Add(v)
				s.Remove(v)
			}
		}()
	}
	wg.Wait()
	assertShardMatches(t, s, modelOf(makeSeq(1_000)...))
}
