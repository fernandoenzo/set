package set

import (
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
	"time"
)

// --- helpers ---------------------------------------------------------------

func syncSetOf(vals ...int) *SyncSet[int] {
	return NewSyncFromSlices(vals)
}

// sortedSyncEqual compares the elements of a SyncSet with the model, order-free.
func assertSyncMatches(t *testing.T, s *SyncSet[int], want map[int]struct{}) {
	t.Helper()
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
	if !s.Equal(syncSetOf(keysOf(want)...)) {
		t.Fatalf("Equal(model) = false for %v", s.GetAll())
	}
	if !s.Copy().Equal(s) {
		t.Fatalf("Copy() lost elements")
	}
	if !s.Clone().Equal(s) {
		t.Fatalf("Clone() lost elements")
	}
}

// mustFinish runs f and fails the test if f has not returned within budget,
// which is how the deadlock regressions are pinned.
func mustFinish(t *testing.T, name string, budget time.Duration, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(budget):
		t.Fatalf("%s: did not finish in %s", name, budget)
	}
}

// --- zero value ------------------------------------------------------------

// The zero value is documented as a usable empty set: every entry point,
// including every writer, must work on it without initialization.
func TestSyncZeroValueWriters(t *testing.T) {
	other := syncSetOf(1, 2, 3)
	empty := NewSync[int](0)

	writers := []struct {
		name    string
		run     func(s *SyncSet[int])
		wantLen int
	}{
		{"Add(1)", func(s *SyncSet[int]) { s.Add(1) }, 1},
		{"AddAll()", func(s *SyncSet[int]) { s.AddAll() }, 0},
		{"AddAll(1)", func(s *SyncSet[int]) { s.AddAll(1) }, 1},
		{"AddAll(3)", func(s *SyncSet[int]) { s.AddAll(1, 2, 3) }, 3},
		{"AddAll(1000)", func(s *SyncSet[int]) { s.AddAll(makeSeq(1000)...) }, 1000},
		{"AddSeq", func(s *SyncSet[int]) { s.AddSeq(slices.Values([]int{7, 8})) }, 2},
		{"AddSeq(empty)", func(s *SyncSet[int]) { s.AddSeq(func(func(int) bool) {}) }, 0},
		{"Extend()", func(s *SyncSet[int]) { s.Extend() }, 0},
		{"Extend(empty)", func(s *SyncSet[int]) { s.Extend(empty) }, 0},
		{"Extend(1)", func(s *SyncSet[int]) { s.Extend(syncSetOf(1)) }, 1},
		{"Extend(many sets)", func(s *SyncSet[int]) { s.Extend(syncSetOf(1), empty, syncSetOf(2, 3)) }, 3},
		{"Extend(zero value)", func(s *SyncSet[int]) { var z SyncSet[int]; s.Extend(&z) }, 0},
		{"Extend(self)", func(s *SyncSet[int]) { s.Extend(s) }, 0},
		{"Retain(self)", func(s *SyncSet[int]) { s.Retain(s) }, 0},
		{"Subtract(self)", func(s *SyncSet[int]) { s.Subtract(s) }, 0},
		{"Remove(absent)", func(s *SyncSet[int]) { s.Remove(1, 2) }, 0},
		{"Retain()", func(s *SyncSet[int]) { s.Retain() }, 0},
		{"Subtract()", func(s *SyncSet[int]) { s.Subtract() }, 0},
		{"Subtract(other)", func(s *SyncSet[int]) { s.Subtract(other) }, 0},
		{"Retain(other)", func(s *SyncSet[int]) { s.Retain(other) }, 0},
		{"Rehash()", func(s *SyncSet[int]) { s.Rehash() }, 0},
	}
	for _, c := range writers {
		t.Run(c.name, func(t *testing.T) {
			var s SyncSet[int]
			mustFinish(t, c.name, 5*time.Second, func() {
				if msg, panicked := tryRun(func() { c.run(&s) }); panicked {
					t.Errorf("panic on zero value: %v", msg)
				}
			})
			if s.Len() != c.wantLen {
				t.Fatalf("Len() = %d, want %d", s.Len(), c.wantLen)
			}
			// The set must still be a working set afterwards.
			if msg, panicked := tryRun(func() { s.Add(1) }); panicked {
				t.Fatalf("set unusable after write: %v", msg)
			}
			if !s.Contains(1) {
				t.Fatalf("Contains(1) = false after Add")
			}
		})
	}
}

// The zero value must also answer every read without panicking.
func TestSyncZeroValueReaders(t *testing.T) {
	var s SyncSet[int]
	other := syncSetOf(1, 2)

	if s.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", s.Len())
	}
	if s.Contains(1) {
		t.Fatalf("Contains(1) = true on empty set")
	}
	if len(s.GetAll()) != 0 {
		t.Fatalf("GetAll() = %v, want empty", s.GetAll())
	}
	if n := len(slices.Collect(s.IterAll())); n != 0 {
		t.Fatalf("IterAll() yielded %d elements, want 0", n)
	}
	assertSyncMatches(t, &s, modelOf())
	if !s.IsSubset(other) {
		t.Fatalf("empty set is not a subset")
	}
	if !s.Disjoint(other) {
		t.Fatalf("empty set is not disjoint")
	}
	if !s.Equal(&SyncSet[int]{}) {
		t.Fatalf("zero value != other zero value")
	}
	if d := s.Difference(other); d.Len() != 0 {
		t.Fatalf("empty - other = %v, want empty", d.GetAll())
	}
	if c := s.Copy(); c.Len() != 0 {
		t.Fatalf("Copy() of empty = %v", c.GetAll())
	}
	if c := s.Clone(); c.Len() != 0 {
		t.Fatalf("Clone() of empty = %v", c.GetAll())
	}
}

func TestSyncZeroValueSetAsArgument(t *testing.T) {
	var z SyncSet[int]
	other := syncSetOf(1, 2, 3)

	if u := SyncUnion(&z, other); !u.Equal(other) {
		t.Fatalf("Union(zero, other) = %v, want %v", u.GetAll(), other.GetAll())
	}
	if i := SyncIntersection(&z, other); i.Len() != 0 {
		t.Fatalf("Intersection(zero, other) = %v, want empty", i.GetAll())
	}
	if d := other.Difference(&z); !d.Equal(other) {
		t.Fatalf("other - zero = %v, want %v", d.GetAll(), other.GetAll())
	}
	s := syncSetOf(1, 2, 3)
	s.Retain(&z)
	assertSyncMatches(t, s, modelOf())
}

// --- algebra against the model ---------------------------------------------

// Every ordered pair of subsets of {0,1,2} checked against the model, with the
// receiver and the argument swapped, aliased and doubled.
func TestSyncSetAlgebraMatchesModel(t *testing.T) {
	universe := []int{0, 1, 2}
	subsets := make([][]int, 0, 8)
	for mask := 0; mask < 1<<len(universe); mask++ {
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
			sa, sb := syncSetOf(a...), syncSetOf(b...)

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

			assertSyncMatches(t, SyncUnion(sa, sb), wantUnion)
			assertSyncMatches(t, SyncIntersection(sa, sb), wantInter)
			assertSyncMatches(t, sa.Difference(sb), wantDiff)

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

func TestSyncIntersectionArity(t *testing.T) {
	a, b, c := syncSetOf(1, 2, 3), syncSetOf(2, 3, 4), syncSetOf(3, 4, 5)

	if got := SyncIntersection[int](); got.Len() != 0 {
		t.Fatalf("SyncIntersection() = %v, want empty", got.GetAll())
	}
	assertSyncMatches(t, SyncIntersection(a), modelOf(1, 2, 3))
	assertSyncMatches(t, SyncIntersection(a, b), modelOf(2, 3))
	assertSyncMatches(t, SyncIntersection(a, b, c), modelOf(3))
	// Aliased operands: each set must be locked exactly once.
	assertSyncMatches(t, SyncIntersection(a, a, a), modelOf(1, 2, 3))
	assertSyncMatches(t, SyncIntersection(a, a, b), modelOf(2, 3))
	assertSyncMatches(t, SyncUnion[int](), modelOf())
	assertSyncMatches(t, SyncUnion(a), modelOf(1, 2, 3))
	assertSyncMatches(t, SyncUnion(a, a, b), modelOf(1, 2, 3, 4))
}

func TestSyncMutatorsMatchModel(t *testing.T) {
	rng := rand.New(rand.NewPCG(0xC0FFEE, 0xBEEF))
	for trial := 0; trial < 200; trial++ {
		s := NewSync[int](0)
		model := modelOf()
		for step := 0; step < 40; step++ {
			other := syncSetOf(randVals(rng, 4)...)
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
			assertSyncMatches(t, s, model)
		}
	}
}

// --- self and aliased operations -------------------------------------------

// The operations the plain Set documents as valid must be valid here too, on
// any argument position and in any order, without deadlocking or panicking.
func TestSyncSelfOperations(t *testing.T) {
	s := syncSetOf(1, 2, 3, 4, 5)
	model := modelOf(1, 2, 3, 4, 5)

	mustFinish(t, "Extend(self)", 5*time.Second, func() { s.Extend(s) })
	assertSyncMatches(t, s, model)

	mustFinish(t, "Retain(self)", 5*time.Second, func() { s.Retain(s) })
	assertSyncMatches(t, s, model)

	mustFinish(t, "Extend(self, self)", 5*time.Second, func() { s.Extend(s, s) })
	assertSyncMatches(t, s, model)

	mustFinish(t, "Extend(other, self)", 5*time.Second, func() { s.Extend(syncSetOf(6), s) })
	assertSyncMatches(t, s, modelOf(1, 2, 3, 4, 5, 6))

	mustFinish(t, "Subtract(self)", 5*time.Second, func() { s.Subtract(s) })
	assertSyncMatches(t, s, modelOf())

	// A self argument anywhere in the list wins: subtracting s itself empties.
	s = syncSetOf(1, 2, 3)
	mustFinish(t, "Subtract(other, self, other)", 5*time.Second, func() {
		s.Subtract(syncSetOf(2), s, syncSetOf(3))
	})
	assertSyncMatches(t, s, modelOf())

	s = syncSetOf(1, 2, 3)
	mustFinish(t, "Retain(self, other)", 5*time.Second, func() { s.Retain(s, syncSetOf(2, 3)) })
	assertSyncMatches(t, s, modelOf(2, 3))

	s = syncSetOf(1, 2, 3)
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

	empty := NewSync[int](0)
	if !empty.Disjoint(empty) {
		t.Fatalf("the empty set is not disjoint from itself")
	}
}

// --- concurrency ------------------------------------------------------------

// The lock order makes these shapes safe; a regression shows up as a call that
// never returns. Each case gets its own budget so a failure names the shape.
func TestSyncAliasedOperationsDoNotDeadlock(t *testing.T) {
	// Same operand twice, with a writer on it.
	t.Run("same operand twice", func(t *testing.T) {
		x := syncSetOf(1, 2, 3, 4)
		stop := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					x.Add(99)
					x.Remove(99)
				}
			}
		}()
		mustFinish(t, "SyncUnion(x, x)", 10*time.Second, func() {
			for range 50_000 {
				_ = SyncUnion(x, x)
				_ = SyncIntersection(x, x)
				_ = x.IsSubset(x)
				_ = x.Disjoint(x)
				_ = x.Equal(x)
				_ = x.Difference(x)
			}
		})
		close(stop)
		wg.Wait()
	})

	// Two sets in opposite argument orders, each with a writer.
	t.Run("inverted order pair", func(t *testing.T) {
		a, b := syncSetOf(1, 2, 3, 4), syncSetOf(3, 4, 5, 6)
		var wg sync.WaitGroup
		wg.Add(4)
		go func() {
			defer wg.Done()
			for range 40_000 {
				a.Extend(b)
			}
		}()
		go func() {
			defer wg.Done()
			for range 40_000 {
				b.Extend(a)
			}
		}()
		go func() {
			defer wg.Done()
			for range 40_000 {
				_ = a.Difference(b)
			}
		}()
		go func() {
			defer wg.Done()
			for range 40_000 {
				_ = b.Difference(a)
			}
		}()
		mustFinish(t, "inverted order pair", 30*time.Second, wg.Wait)
	})
}

// The classic lost-update check: every writer must land.
func TestSyncConcurrentAddsAllLand(t *testing.T) {
	const workers = 64
	s := NewSync[int](0)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 500 {
				s.Add(w*500 + i)
			}
		}()
	}
	mustFinish(t, "concurrent Add", 30*time.Second, wg.Wait)
	assertSyncMatches(t, s, modelOf(makeSeq(workers*500)...))
}

// Extend in both directions must converge: both sets end holding the union,
// and each element is visible from every reader.
func TestSyncConcurrentExtendConverges(t *testing.T) {
	a, b := syncSetOf(1, 2), syncSetOf(3, 4)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); a.Extend(b) }()
	go func() { defer wg.Done(); b.Extend(a) }()
	mustFinish(t, "mutual Extend", 30*time.Second, wg.Wait)
	assertSyncMatches(t, a, modelOf(1, 2, 3, 4))
	assertSyncMatches(t, b, modelOf(1, 2, 3, 4))
}

// Readers and writers on one set: the results must be consistent snapshots
// (no torn length, no duplicated element) and nothing may race. Run under
// -race, this is what pins Len, Rehash and the read paths.
func TestSyncReadersAndWritersAgree(t *testing.T) {
	const elems = 4000
	s := NewSync[int](0)
	s.AddAll(makeSeq(elems)...)
	var wg sync.WaitGroup

	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 6_000 {
				s.Add(elems + 1) // present or absent, never corrupting
				s.Remove(elems + 1)
				if i%500 == 0 {
					s.Rehash() // O(n), so it stays occasional
				}
			}
		}()
	}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 1_000 {
				// Each call is atomic on its own, not across calls: a writer
				// may commit between a Len and a GetAll, so the two are not
				// compared with each other. What must hold is that every
				// snapshot is internally consistent, and that a set is a
				// subset of itself.
				if n := s.Len(); n < 1 || n > elems+1 {
					t.Errorf("Len() = %d: outside the possible range", n)
					return
				}
				got := s.GetAll()
				if len(got) < 1 || len(got) > elems+1 {
					t.Errorf("GetAll() returned %d elements: outside the possible range", len(got))
					return
				}
				seen := make(map[int]struct{}, len(got))
				for _, v := range got {
					if _, dup := seen[v]; dup {
						t.Errorf("GetAll() returned %d twice", v)
						return
					}
					seen[v] = struct{}{}
				}
				_ = s.Contains(1)
				if !s.IsSubset(s) {
					t.Errorf("IsSubset(self) = false")
					return
				}
				// A Copy is private, so nothing can mutate it while it is
				// inspected: Len and GetAll must agree exactly here.
				c := s.Copy()
				if c.Len() != len(c.GetAll()) {
					t.Errorf("Copy(): Len() = %d but GetAll() = %d elements", c.Len(), len(c.GetAll()))
					return
				}
				for _, v := range c.GetAll() {
					if !c.Contains(v) {
						t.Errorf("Copy() lost %d between calls", v)
						return
					}
				}
			}
		}()
	}
	mustFinish(t, "readers and writers", 60*time.Second, wg.Wait)
}

// A mixed workload over a pool of sets: the lock order must keep every shape
// deadlock-free while the race detector watches the data. The algebra itself
// is pinned by the single-threaded model tests; here the contract is
// termination, no race and internal consistency.
func TestSyncConcurrentMixedWorkload(t *testing.T) {
	pool := make([]*SyncSet[int], 6)
	for i := range pool {
		pool[i] = NewSyncFromSlices(makeSeq(1 + i*50))
	}

	rng := rand.New(rand.NewPCG(0x5E7, 0x1234))
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := rand.New(rand.NewPCG(uint64(w), 99))
			for range 12_000 {
				x, y := pool[local.IntN(len(pool))], pool[local.IntN(len(pool))]
				switch local.IntN(10) {
				case 0:
					x.Add(local.IntN(1000))
				case 1:
					x.Remove(local.IntN(1000))
				case 2:
					x.Extend(y)
				case 3:
					x.Subtract(y)
				case 4:
					x.Retain(y)
				case 5:
					_ = x.Difference(y)
				case 6:
					_ = SyncUnion(x, y)
				case 7:
					_ = SyncIntersection(x, y)
				case 8:
					_ = x.IsSubset(y)
					_ = x.Disjoint(y)
					_ = x.Equal(y)
				case 9:
					x.Rehash()
					_ = x.Copy()
				}
			}
		}()
	}
	mustFinish(t, "mixed workload", 120*time.Second, wg.Wait)

	for i, s := range pool {
		got := s.GetAll()
		if len(got) != s.Len() {
			t.Fatalf("set %d: Len() = %d but GetAll() = %d elements", i, s.Len(), len(got))
		}
		if !s.Equal(s.Copy()) {
			t.Fatalf("set %d: Copy() disagrees with the source", i)
		}
	}
	_ = rng
}

// IterAll and AddSeq run the caller's code under the lock, exactly like
// sync.Map.Range: the documented way to touch a set while iterating is to take
// the snapshot through GetAll first.
func TestSyncGetAllSnapshotAllowsMutation(t *testing.T) {
	s := syncSetOf(1, 2, 3)
	mustFinish(t, "GetAll then mutate", 5*time.Second, func() {
		for _, v := range s.GetAll() {
			s.Add(v + 10)
		}
	})
	assertSyncMatches(t, s, modelOf(1, 2, 3, 11, 12, 13))

	// AddSeq over values that are already in hand needs no set access.
	other := syncSetOf(4, 5)
	mustFinish(t, "AddSeq of a plain slice", 5*time.Second, func() {
		s.AddSeq(slices.Values(other.GetAll()))
	})
	assertSyncMatches(t, s, modelOf(1, 2, 3, 4, 5, 11, 12, 13))
}

// An early stop in the IterAll body must return the iterator and release the
// read lock, so the next writer does not block.
func TestSyncIterAllEarlyStopReleasesLock(t *testing.T) {
	s := NewSyncFromSlices(makeSeq(1000))
	seen := 0
	for range s.IterAll() {
		seen++
		break
	}
	if seen != 1 {
		t.Fatalf("IterAll yielded %d elements before the stop, want 1", seen)
	}
	mustFinish(t, "Add after an early stop", 3*time.Second, func() { s.Add(-1) })
	if !s.Contains(-1) {
		t.Fatalf("the write after the early stop did not land")
	}
}

// IterSnapshot clones under the read lock and returns the iterator with no
// lock held, so the body may touch the same set: it must not deadlock, must
// see the pre-call contents, and must not be affected by writes made inside
// the loop.
func TestSyncIterSnapshotAllowsMutation(t *testing.T) {
	s := syncSetOf(1, 2, 3)
	seen := map[int]struct{}{}
	mustFinish(t, "IterSnapshot body mutating the same set", 5*time.Second, func() {
		for v := range s.IterSnapshot() {
			seen[v] = struct{}{}
			s.Add(v + 10)
			s.Remove(v)
		}
	})
	// The snapshot reflects the set as it was when IterSnapshot ran: each
	// original element was visited exactly once, including those removed.
	for _, v := range []int{1, 2, 3} {
		if _, ok := seen[v]; !ok {
			t.Fatalf("IterSnapshot skipped %d: %v", v, seen)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("IterSnapshot yielded %d distinct elements, want 3: %v", len(seen), seen)
	}
	// The mutations landed: 1, 2, 3 removed, 11, 12, 13 added.
	assertSyncMatches(t, s, modelOf(11, 12, 13))
}

// A writer running while IterSnapshot iterates must neither deadlock nor race.
func TestSyncIterSnapshotWithConcurrentWriter(t *testing.T) {
	s := NewSyncFromSlices(makeSeq(64))
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				s.Add(1000 + i%16)
				s.Remove(1000 + i%16)
			}
		}
	}()
	mustFinish(t, "IterSnapshot under a concurrent writer", 30*time.Second, func() {
		for range 2_000 {
			n := 0
			for range s.IterSnapshot() {
				n++
			}
			if n < 64 {
				t.Errorf("snapshot yielded %d elements, want at least the 64 originals", n)
				return
			}
		}
	})
	close(stop)
	wg.Wait()
}

// An early stop must not leak the clone's iterator state, and an unconsumed
// iterator is still a valid call.
func TestSyncIterSnapshotEdgeCases(t *testing.T) {
	s := syncSetOf(1, 2, 3)
	mustFinish(t, "early stop", 5*time.Second, func() {
		for range s.IterSnapshot() {
			break
		}
	})
	mustFinish(t, "iterator never consumed", 5*time.Second, func() {
		_ = s.IterSnapshot()
	})
	mustFinish(t, "Add after an unconsumed iterator", 5*time.Second, func() { s.Add(4) })

	empty := NewSync[int](0)
	if n := len(slices.Collect(empty.IterSnapshot())); n != 0 {
		t.Fatalf("IterSnapshot on an empty set yielded %d elements", n)
	}
	var zero SyncSet[int]
	if n := len(slices.Collect(zero.IterSnapshot())); n != 0 {
		t.Fatalf("IterSnapshot on the zero value yielded %d elements", n)
	}
}

// IterAll and IterSnapshot must agree on the elements of an untouched set.
func TestSyncIteratorsAgree(t *testing.T) {
	s := NewSyncFromSlices(makeSeq(500))
	viaAll := slices.Sorted(s.IterAll())
	viaSnapshot := slices.Sorted(s.IterSnapshot())
	viaGetAll := slices.Sorted(slices.Values(s.GetAll()))
	if !slices.Equal(viaAll, viaSnapshot) || !slices.Equal(viaAll, viaGetAll) {
		t.Fatalf("iterators disagree: IterAll=%d IterSnapshot=%d GetAll=%d elements",
			len(viaAll), len(viaSnapshot), len(viaGetAll))
	}
}

// --- lockPair ---------------------------------------------------------------

// lockPair is the binary operations' shared path: it must order by identity,
// collapse the aliased case into one lock, and never allocate.
func TestSyncLockPairOrdersAndCollapses(t *testing.T) {
	a, b := NewSync[int](0), NewSync[int](0)
	if a.id >= b.id {
		t.Fatalf("identities are not in construction order: %d, %d", a.id, b.id)
	}

	// Distinct sets: two requests in ascending identity order.
	got := lockPair(a, false, b, false)
	unlockPair(got)
	if got.n != 2 || got.reqs[0].set != a || got.reqs[1].set != b {
		t.Fatalf("lockPair(a, b) = %+v, want a then b", got)
	}

	// Passed the other way round, the order must not change.
	got = lockPair(b, false, a, false)
	unlockPair(got)
	if got.n != 2 || got.reqs[0].set != a || got.reqs[1].set != b {
		t.Fatalf("lockPair(b, a) = %+v, want a then b", got)
	}

	// Aliased: one lock, and a write request wins over a read one.
	got = lockPair(a, true, a, false)
	if got.n != 1 || got.reqs[0].set != a || !got.reqs[0].write {
		t.Fatalf("lockPair(a,true,a,false) = %+v, want a single write on a", got)
	}
	unlockPair(got)
	got = lockPair(a, false, a, true)
	if got.n != 1 || got.reqs[0].set != a || !got.reqs[0].write {
		t.Fatalf("lockPair(a,false,a,true) = %+v, want a single write on a", got)
	}
	unlockPair(got)
	got = lockPair(a, false, a, false)
	if got.n != 1 || got.reqs[0].set != a || got.reqs[0].write {
		t.Fatalf("lockPair(a,false,a,false) = %+v, want a single read on a", got)
	}
	unlockPair(got)
}

// The binary predicates are documented as allocation-free, like their plain
// counterparts: the two requests must not reach the heap.
func TestSyncBinaryPredicatesDoNotAllocate(t *testing.T) {
	a, b := NewSyncFromSlices(makeSeq(64)), NewSyncFromSlices(makeSeq(64))
	if allocs := testing.AllocsPerRun(100, func() { _ = a.IsSubset(b) }); allocs != 0 {
		t.Fatalf("IsSubset allocated %v times per run, want 0", allocs)
	}
	if allocs := testing.AllocsPerRun(100, func() { _ = a.Disjoint(b) }); allocs != 0 {
		t.Fatalf("Disjoint allocated %v times per run, want 0", allocs)
	}
	if allocs := testing.AllocsPerRun(100, func() { _ = a.Equal(b) }); allocs != 0 {
		t.Fatalf("Equal allocated %v times per run, want 0", allocs)
	}
	if allocs := testing.AllocsPerRun(100, func() { _ = a.Contains(1) }); allocs != 0 {
		t.Fatalf("Contains allocated %v times per run, want 0", allocs)
	}
}

// --- internal invariants ----------------------------------------------------

// lockAll is the whole deadlock proof: the requests come out sorted by
// identity and free of repeats, whatever order the caller passed them in.
func TestSyncLockOrderSortsAndCollapses(t *testing.T) {
	sets := make([]*SyncSet[int], 8)
	for i := range sets {
		sets[i] = NewSync[int](0)
	}
	// Identities are assigned in construction order, so the expected order is
	// the slice order and the shuffle below must not survive.
	reqs := []lockReq[int]{
		{set: sets[5]}, {set: sets[0], write: true}, {set: sets[3]},
		{set: sets[0]}, {set: sets[7], write: true}, {set: sets[3], write: true},
		{set: sets[0]}, {set: sets[2]},
	}
	got := lockAll(reqs)
	defer unlockAll(got)

	if len(got) != 5 {
		t.Fatalf("kept %d requests, want 5 (8 with repeats collapsed)", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].set.id >= got[i].set.id {
			t.Fatalf("requests not in ascending identity order: %d then %d", got[i-1].set.id, got[i].set.id)
		}
	}
	// A write request must win over read requests for the same set.
	for _, r := range got {
		if r.set == sets[0] && !r.write {
			t.Fatalf("set 0 kept as a read request, want the write request")
		}
		if r.set == sets[3] && !r.write {
			t.Fatalf("set 3 kept as a read request, want the write request")
		}
	}
}

// Identities must be distinct and assigned once, no matter which route created
// the set or how many times it is queried.
func TestSyncIdentitiesAreUniqueAndStable(t *testing.T) {
	seen := make(map[uint64]*SyncSet[int])
	var zero SyncSet[int]
	candidates := []*SyncSet[int]{
		NewSync[int](0),
		NewSync[int](0),
		NewSyncFromSlices([]int{1}),
		NewSync[int](4),
		&zero,
	}
	for _, s := range candidates {
		first := s.setID()
		if again := s.setID(); again != first {
			t.Fatalf("setID() changed from %d to %d", first, again)
		}
		if prev, dup := seen[first]; dup {
			t.Fatalf("two sets share identity %d: %p and %p", first, prev, s)
		}
		seen[first] = s
	}
}

// A constructor must hand back a set with an identity already assigned, so
// that the first multi-set operation does not have to allocate the Once state.
func TestSyncConstructorsAssignIdentity(t *testing.T) {
	if s := NewSync[int](1); s.setID() == 0 {
		t.Fatalf("NewSync left the identity unassigned")
	}
	if s := NewSyncFromSlices([]int{1}); s.setID() == 0 {
		t.Fatalf("NewSyncFromSlices left the identity unassigned")
	}
	if r := SyncUnion(syncSetOf(1), syncSetOf(2)); r.setID() == 0 {
		t.Fatalf("SyncUnion left the identity unassigned")
	}
	if r := SyncIntersection(syncSetOf(1), syncSetOf(1)); r.setID() == 0 {
		t.Fatalf("SyncIntersection left the identity unassigned")
	}
}

// --- copies and reservations ------------------------------------------------

func TestSyncCopyAndCloneContracts(t *testing.T) {
	s := NewSync[int](0)
	s.AddAll(makeSeq(1000)...)
	s.Remove(makeSeq(900)...) // leaves 900..999

	if got := s.Copy(); got.set.capacity != got.Len() {
		t.Fatalf("Copy capacity = %d, want Len = %d", got.set.capacity, got.Len())
	}
	if got := s.Clone(); got.set.capacity != s.set.capacity {
		t.Fatalf("Clone capacity = %d, want source capacity = %d", got.set.capacity, s.set.capacity)
	}

	// Independence: mutating either side must not touch the other, and the
	// copies must not share the source's lock either.
	cp, cl := s.Copy(), s.Clone()
	cp.Add(-1)
	cl.Add(-2)
	s.Add(-3)
	for _, tc := range []struct {
		name string
		set  *SyncSet[int]
		gone int
	}{{"source vs Copy", s, -1}, {"source vs Clone", s, -2}} {
		if tc.set.Contains(tc.gone) {
			t.Fatalf("%s: shares state with the copy", tc.name)
		}
	}
	if s.Len() != 101 {
		t.Fatalf("source Len = %d, want 101", s.Len())
	}
	if cp.Len() != 101 || cp.Contains(-3) {
		t.Fatalf("Copy Len = %d, Contains(-3) = %v", cp.Len(), cp.Contains(-3))
	}
	if cl.Len() != 101 || cl.Contains(-3) {
		t.Fatalf("Clone Len = %d, Contains(-3) = %v", cl.Len(), cl.Contains(-3))
	}
}

// Difference over the wrapper must hold the same reservation contract as the
// plain Set: the result sits on the step of its own length.
func TestSyncDifferenceReservation(t *testing.T) {
	for _, c := range [][2]int{{1000, 0}, {1000, 500}, {1000, 999}, {10000, 9000}, {100000, 1}} {
		s := NewSync[int](0)
		s.AddAll(makeSeq(c[0])...)
		other := NewSyncFromSlices(makeSeq(c[1]))

		res := s.Difference(other)
		if got, want := res.Len(), c[0]-c[1]; got != want {
			t.Fatalf("m=%d n=%d: Len() = %d, want %d", c[0], c[1], got, want)
		}
		if hintOversized(res.set.capacity, res.Len()) {
			t.Fatalf("m=%d n=%d: capacity = %d reserves above the step for Len = %d",
				c[0], c[1], res.set.capacity, res.Len())
		}
		assertSyncMatches(t, res, modelOf(seqFrom(c[1], c[0])...))
	}
}

// A nil *SyncSet is documented as not usable, like a nil *Set.
func TestSyncNilPanics(t *testing.T) {
	if msg, panicked := tryRun(func() { var s *SyncSet[int]; s.Add(1) }); !panicked {
		t.Fatalf("nil *SyncSet did not panic on Add (%v)", msg)
	}
	if msg, panicked := tryRun(func() {
		s := NewSync[int](0)
		s.Extend(nil)
	}); !panicked {
		t.Fatalf("nil argument did not panic on Extend (%v)", msg)
	}
}

// A panic in code the wrapper calls must still release the lock: IterAll and
// AddSeq run the caller's body under it, and Add is a map write that can panic
// on an unhashable key.
func TestSyncPanicInCallerCodeReleasesLock(t *testing.T) {
	t.Run("IterAll body", func(t *testing.T) {
		s := syncSetOf(1, 2, 3)
		func() {
			defer func() { _ = recover() }()
			for range s.IterAll() {
				panic("body")
			}
		}()
		mustFinish(t, "Add after a panic in the IterAll body", 3*time.Second, func() { s.Add(4) })
	})
	t.Run("AddSeq sequence", func(t *testing.T) {
		s := NewSync[int](2)
		func() {
			defer func() { _ = recover() }()
			s.AddSeq(func(yield func(int) bool) {
				panic("sequence")
			})
		}()
		mustFinish(t, "Add after a panic in the AddSeq sequence", 3*time.Second, func() { s.Add(4) })
	})
	t.Run("AddSeq yield", func(t *testing.T) {
		s := NewSync[int](2)
		func() {
			defer func() { _ = recover() }()
			s.AddSeq(func(yield func(int) bool) {
				yield(1)
				panic("after yield")
			})
		}()
		mustFinish(t, "Add after a panic past a yield in AddSeq", 3*time.Second, func() { s.Add(4) })
	})
}
