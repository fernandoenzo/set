package set

import (
	"math/rand/v2"
	"slices"
	"testing"
)

// --- helpers ---------------------------------------------------------------

func modelOf(vals ...int) map[int]struct{} {
	m := make(map[int]struct{}, len(vals))
	for _, v := range vals {
		m[v] = struct{}{}
	}
	return m
}

func setOf(vals ...int) *Set[int] {
	return NewFromSlices(vals)
}

func sortedEqual(t *testing.T, got []int, want map[int]struct{}) {
	t.Helper()
	gotCopy := slices.Clone(got)
	slices.Sort(gotCopy)
	wantSlice := make([]int, 0, len(want))
	for v := range want {
		wantSlice = append(wantSlice, v)
	}
	slices.Sort(wantSlice)
	if !slices.Equal(gotCopy, wantSlice) {
		t.Fatalf("elements = %v, want %v", gotCopy, wantSlice)
	}
}

// assertMatches checks a set against the reference model through the
// read-only API: length, membership probes, full element list and a fresh
// copy/union round trip.
func assertMatches(t *testing.T, s *Set[int], want map[int]struct{}) {
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
	if !s.Equal(setOf(keysOf(want)...)) {
		t.Fatalf("Equal(model) = false for %v", s.GetAll())
	}
	if !s.Copy().Equal(s) {
		t.Fatalf("Copy() lost elements")
	}
	if !s.Clone().Equal(s) {
		t.Fatalf("Clone() lost elements")
	}
	if !s.IsSubset(setOf(keysOf(want)...)) {
		t.Fatalf("IsSubset(self) = false")
	}
}

func keysOf(m map[int]struct{}) []int {
	out := make([]int, 0, len(m))
	for v := range m {
		out = append(out, v)
	}
	return out
}

// --- zero value ------------------------------------------------------------

// The zero value is documented as a usable empty set: every entry point,
// including every writer, must work on it without initialization.
func TestZeroValueWriters(t *testing.T) {
	other := setOf(1, 2, 3)
	empty := New[int](0)

	writers := []struct {
		name    string
		run     func(s *Set[int])
		wantLen int
	}{
		{"Add()", func(s *Set[int]) { s.Add() }, 0},
		{"Add(1)", func(s *Set[int]) { s.Add(1) }, 1},
		{"Add(3)", func(s *Set[int]) { s.Add(1, 2, 3) }, 3},
		{"Add(16)", func(s *Set[int]) { s.Add(makeSeq(16)...) }, 16},
		{"Add(1000)", func(s *Set[int]) { s.Add(makeSeq(1000)...) }, 1000},
		{"AddSeq", func(s *Set[int]) { s.AddSeq(slices.Values([]int{7, 8})) }, 2},
		{"AddSeq(empty)", func(s *Set[int]) { s.AddSeq(func(func(int) bool) {}) }, 0},
		{"Extend()", func(s *Set[int]) { s.Extend() }, 0},
		{"Extend(empty)", func(s *Set[int]) { s.Extend(empty) }, 0},
		{"Extend(1)", func(s *Set[int]) { s.Extend(setOf(1)) }, 1},
		{"Extend(3)", func(s *Set[int]) { s.Extend(setOf(1, 2, 3)) }, 3},
		{"Extend(16)", func(s *Set[int]) { s.Extend(setOf(makeSeq(16)...)) }, 16},
		{"Extend(1000)", func(s *Set[int]) { s.Extend(setOf(makeSeq(1000)...)) }, 1000},
		{"Extend(many sets)", func(s *Set[int]) { s.Extend(setOf(1), empty, setOf(2, 3)) }, 3},
		{"Extend(zero value)", func(s *Set[int]) { var z Set[int]; s.Extend(&z) }, 0},
		{"Extend(nil map set)", func(s *Set[int]) { s.Extend(&Set[int]{}) }, 0},
		{"Remove(absent)", func(s *Set[int]) { s.Remove(1, 2) }, 0},
		{"Subtract()", func(s *Set[int]) { s.Subtract() }, 0},
		{"Subtract(other)", func(s *Set[int]) { s.Subtract(other) }, 0},
		{"Intersects(other)", func(s *Set[int]) { s.Intersects(other) }, 0},
		{"Rehash()", func(s *Set[int]) { s.Rehash() }, 0},
	}
	for _, c := range writers {
		t.Run(c.name, func(t *testing.T) {
			var s Set[int]
			if msg, panicked := tryRun(func() { c.run(&s) }); panicked {
				t.Fatalf("panic on zero value: %v", msg)
			}
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

func tryRun(f func()) (msg any, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			msg, panicked = r, true
		}
	}()
	f()
	return nil, false
}

// The zero value must also answer every read without panicking.
func TestZeroValueReaders(t *testing.T) {
	var s Set[int]
	other := setOf(1, 2)

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
	assertMatches(t, &s, modelOf())
	if !s.IsSubset(other) {
		t.Fatalf("empty set is not a subset")
	}
	if !s.Disjoint(other) {
		t.Fatalf("empty set is not disjoint")
	}
	if !s.Equal(&Set[int]{}) {
		t.Fatalf("zero value != other zero value")
	}
	if d := s.Difference(other); d.Len() != 0 {
		t.Fatalf("empty - other = %v, want empty", d.GetAll())
	}
	if d := s.Difference(&Set[int]{}); d.Len() != 0 {
		t.Fatalf("empty - empty = %v, want empty", d.GetAll())
	}
	if c := s.Copy(); c.Len() != 0 {
		t.Fatalf("Copy() of empty = %v", c.GetAll())
	}
	if c := s.Clone(); c.Len() != 0 {
		t.Fatalf("Clone() of empty = %v", c.GetAll())
	}
}

func TestZeroValueSetAsArgument(t *testing.T) {
	var z Set[int]
	other := setOf(1, 2, 3)

	if u := Union(&z, other); !u.Equal(other) {
		t.Fatalf("Union(zero, other) = %v, want %v", u.GetAll(), other.GetAll())
	}
	if u := Union(&z); u.Len() != 0 {
		t.Fatalf("Union(zero) = %v, want empty", u.GetAll())
	}
	if i := Intersection(&z, other); i.Len() != 0 {
		t.Fatalf("Intersection(zero, other) = %v, want empty", i.GetAll())
	}
	if i := Intersection(&z); i.Len() != 0 {
		t.Fatalf("Intersection(zero) = %v, want empty", i.GetAll())
	}
	if d := other.Difference(&z); !d.Equal(other) {
		t.Fatalf("other - zero = %v, want %v", d.GetAll(), other.GetAll())
	}
	s := setOf(1, 2, 3)
	s.Intersects(&z)
	assertMatches(t, s, modelOf())
}

// --- exhaustive algebra ----------------------------------------------------

// Every ordered pair of subsets of {0,1,2} checked against the model.
func TestSetAlgebraMatchesModel(t *testing.T) {
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
			sa, sb := setOf(a...), setOf(b...)

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

			assertMatches(t, Union(sa, sb), wantUnion)
			assertMatches(t, Intersection(sa, sb), wantInter)
			assertMatches(t, sa.Difference(sb), wantDiff)

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

func isSubsetModel(a, b map[int]struct{}) bool {
	for v := range a {
		if _, in := b[v]; !in {
			return false
		}
	}
	return true
}

func TestIntersectionArity(t *testing.T) {
	a, b, c := setOf(1, 2, 3), setOf(2, 3, 4), setOf(3, 4, 5)

	if got := Intersection[int](); got.Len() != 0 {
		t.Fatalf("Intersection() = %v, want empty", got.GetAll())
	}
	assertMatches(t, Intersection(a), modelOf(1, 2, 3))
	assertMatches(t, Intersection(a, b), modelOf(2, 3))
	assertMatches(t, Intersection(a, b, c), modelOf(3))
	assertMatches(t, Intersection(a, a, a), modelOf(1, 2, 3))
	assertMatches(t, Union[int](), modelOf())
	assertMatches(t, Union(a), modelOf(1, 2, 3))
}

// --- mutation --------------------------------------------------------------

func TestMutatorsMatchModel(t *testing.T) {
	rng := rand.New(rand.NewPCG(0xC0FFEE, 0xBEEF))
	for trial := 0; trial < 200; trial++ {
		s := New[int](0)
		model := modelOf()
		for step := 0; step < 40; step++ {
			other := setOf(randVals(rng, 4)...)
			switch rng.IntN(6) {
			case 0:
				vals := randVals(rng, 3)
				s.Add(vals...)
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
				s.Intersects(other)
				for v := range model {
					if !other.Contains(v) {
						delete(model, v)
					}
				}
			}
			assertMatches(t, s, model)
		}
	}
}

func randVals(rng *rand.Rand, n int) []int {
	vals := make([]int, rng.IntN(n+1))
	for i := range vals {
		vals[i] = rng.IntN(12)
	}
	return vals
}

func TestSelfOperations(t *testing.T) {
	s := setOf(1, 2, 3, 4, 5)
	model := modelOf(1, 2, 3, 4, 5)

	s.Extend(s)
	assertMatches(t, s, model)

	s.Subtract(s)
	assertMatches(t, s, modelOf())

	s = setOf(1, 2, 3)
	s.Intersects(s)
	assertMatches(t, s, modelOf(1, 2, 3))

	s = setOf(1, 2, 3)
	s.Subtract(s, setOf(2, 3)) // removes s itself, so everything goes
	assertMatches(t, s, modelOf())

	s = setOf(1, 2, 3)
	s.Subtract(setOf(2, 3))
	assertMatches(t, s, modelOf(1))

	s = setOf(1, 2, 3)
	s.Remove()
	assertMatches(t, s, modelOf(1, 2, 3))

	s.Add()
	assertMatches(t, s, modelOf(1, 2, 3))
}

// Intersects must not write into the caller's backing array.
func TestIntersectsDoesNotClobberCallerSlice(t *testing.T) {
	a := setOf(1, 2, 3, 4)
	arg := setOf(2, 3, 4)
	args := []*Set[int]{arg}

	a.Intersects(args...)
	if len(args) != 1 || args[0] != arg {
		t.Fatalf("caller slice was modified: %v", args)
	}
	if arg.Len() != 3 {
		t.Fatalf("argument set modified: %v", arg.GetAll())
	}
	assertMatches(t, a, modelOf(2, 3, 4))
}

func TestNewFromSlicesDistinct(t *testing.T) {
	assertMatches(t, NewFromSlices([]int{1, 1, 2, 3, 3, 3}), modelOf(1, 2, 3))
	assertMatches(t, NewFromSlices([]int{1}, []int{1, 2}, []int{}), modelOf(1, 2))
	assertMatches(t, NewFromSlices[int](), modelOf())
	assertMatches(t, NewFromSlices([]int{}), modelOf())
}

// Difference is documented to deliver a result that is never over-allocated:
// its reservation must match the step of its final length, with no room for a
// second rebuild. Subtracting a strict subset that drops the result one step
// is the case that used to need one.
func TestDifferenceNeverOverAllocated(t *testing.T) {
	cases := [][2]int{
		{1000, 0}, {1000, 1}, {1000, 499}, {1000, 500}, {1000, 501},
		{1000, 900}, {1000, 999}, {1000, 1000},
		{10000, 1000}, {10000, 5000}, {10000, 9000}, {10000, 10000},
		{100000, 50000}, {100000, 90000},
	}
	for _, c := range cases {
		s, other := New[int](0), New[int](0)
		s.Add(makeSeq(c[0])...)
		other.Add(makeSeq(c[1])...)

		res := s.Difference(other)
		if got, want := res.Len(), c[0]-c[1]; got != want {
			t.Fatalf("m=%d n=%d: Len() = %d, want %d", c[0], c[1], got, want)
		}
		// The delivered reservation must sit in the step of the result's own
		// length: anything above that would be memory the caller never uses and
		// a rebuild waiting to happen.
		if res.capacity < res.Len() {
			t.Fatalf("m=%d n=%d: capacity = %d < Len = %d", c[0], c[1], res.capacity, res.Len())
		}
		if hintOversized(res.capacity, res.Len()) {
			t.Fatalf("m=%d n=%d: capacity = %d reserves above the step for Len = %d",
				c[0], c[1], res.capacity, res.Len())
		}
		assertMatches(t, res, modelOf(seqFrom(c[1], c[0])...))
	}
}

// Subtracting a disjoint set leaves the source intact: this is the branch that
// skips the counting pass, so the result must still be correct and keep the
// source's step rather than shrinking it.
func TestDifferenceDisjointKeepsStep(t *testing.T) {
	s := New[int](0)
	s.Add(makeSeq(10000)...)
	other := NewFromSlices(disjointSeq(1000))

	res := s.Difference(other)
	if res.Len() != 10000 {
		t.Fatalf("Len() = %d, want 10000", res.Len())
	}
	assertMatches(t, res, modelOf(makeSeq(10000)...))
}

func disjointSeq(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i + 1_000_000
	}
	return out
}

// seqFrom returns lo, lo+1, ..., hi-1.
func seqFrom(lo, hi int) []int {
	out := make([]int, max(0, hi-lo))
	for i := range out {
		out[i] = lo + i
	}
	return out
}

// Difference must not mutate its operands.
func TestDifferenceLeavesOperandsIntact(t *testing.T) {
	s, other := NewFromSlices(makeSeq(500)), NewFromSlices(makeSeq(200))
	beforeS, beforeOther := s.Len(), other.Len()

	res := s.Difference(other)

	if res.Len() != 300 {
		t.Fatalf("Len() = %d, want 300", res.Len())
	}
	if s.Len() != beforeS || other.Len() != beforeOther {
		t.Fatalf("operands mutated: s=%d other=%d", s.Len(), other.Len())
	}
	assertMatches(t, s, modelOf(makeSeq(500)...))
	assertMatches(t, other, modelOf(makeSeq(200)...))
}

func TestIntersects(t *testing.T) {
	s := setOf(1, 2, 3, 4, 5)
	s.Intersects(setOf(2, 3, 4), setOf(3, 4, 9))
	assertMatches(t, s, modelOf(3, 4))

	s = setOf(1, 2, 3)
	s.Intersects()
	assertMatches(t, s, modelOf(1, 2, 3))

	s = setOf(1, 2, 3)
	s.Intersects(setOf())
	assertMatches(t, s, modelOf())
}

func TestGetAllAndIterAllAgree(t *testing.T) {
	s := setOf(5, 3, 1, 4, 2)
	got := s.GetAll()
	if len(got) != 5 {
		t.Fatalf("GetAll() = %v", got)
	}
	seen := make(map[int]bool, 5)
	for v := range s.IterAll() {
		seen[v] = true
	}
	for _, v := range got {
		if !seen[v] {
			t.Fatalf("GetAll returned %d which IterAll did not", v)
		}
	}
}

// --- copy semantics --------------------------------------------------------

// Copy is documented as compact: room for exactly its length. Clone is
// documented as preserving the source's reservation so it keeps growing at the
// same cost. Both must be independent of the source.
func TestCopyAndCloneContracts(t *testing.T) {
	s := New[int](0)
	s.Add(makeSeq(1000)...)
	s.Remove(makeSeq(900)...) // leaves 900..999

	if got := s.Copy(); got.capacity != got.Len() {
		t.Fatalf("Copy capacity = %d, want Len = %d", got.capacity, got.Len())
	}
	if got := s.Clone(); got.capacity != s.capacity {
		t.Fatalf("Clone capacity = %d, want source capacity = %d", got.capacity, s.capacity)
	}

	// Independence: mutating either side must not touch the other.
	cp, cl := s.Copy(), s.Clone()
	cp.Add(-1)
	cl.Add(-2)
	s.Add(-3)
	for _, tc := range []struct {
		name string
		set  *Set[int]
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

// Clone of a never-written zero value must not be a nil-pointer trap: the
// result is still usable through the same zero-value contract.
func TestCloneOfZeroValueIsUsable(t *testing.T) {
	var s Set[int]
	c := s.Clone()
	if c.Len() != 0 {
		t.Fatalf("Clone() Len = %d, want 0", c.Len())
	}
	c.Add(1)
	if c.Len() != 1 || !c.Contains(1) {
		t.Fatalf("Clone() of zero value unusable after Add: %v", c.GetAll())
	}
	if s.Len() != 0 {
		t.Fatalf("source modified: %v", s.GetAll())
	}
}

// --- compaction ------------------------------------------------------------

// Rehash is documented to land on the smallest step that fits the current
// length, dropping whatever the map reserved on its way down.
func TestRehashCompactsToSmallestStep(t *testing.T) {
	s := New[int](0)
	s.Add(makeSeq(100_000)...)
	if s.capacity != 100_000 {
		t.Fatalf("capacity after Add = %d, want 100000", s.capacity)
	}

	s.Remove(makeSeq(99_999)...)
	if s.Len() != 1 || s.capacity != 1 {
		t.Fatalf("after shrink: Len = %d, capacity = %d, want 1/1", s.Len(), s.capacity)
	}
	if !s.Contains(99_999) {
		t.Fatalf("wrong element survived: %v", s.GetAll())
	}

	// Reads keep working on the compacted map.
	assertMatches(t, s, modelOf(99_999))
}

func TestRehashOnZeroValue(t *testing.T) {
	var s Set[int]
	s.Rehash()
	if s.Len() != 0 {
		t.Fatalf("Len = %d, want 0", s.Len())
	}
	s.Add(1)
	if !s.Contains(1) {
		t.Fatalf("unusable after Rehash: %v", s.GetAll())
	}
}

// Remove of absent elements must not rebuild anything.
func TestRemoveAbsentIsNoOp(t *testing.T) {
	s := New[int](0)
	s.Add(makeSeq(1000)...)
	before := s.capacity
	s.Remove(-1, -2, -3)
	if s.capacity != before || s.Len() != 1000 {
		t.Fatalf("capacity changed to %d (was %d), Len = %d", s.capacity, before, s.Len())
	}
}

// --- helpers used by the tests --------------------------------------------

func makeSeq(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}
