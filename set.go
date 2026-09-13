// Package set provides an unordered set of comparable values.
//
// The internal map grows on insert and never shrinks on delete, so deletions can
// leave it reserving memory for a peak it no longer has. The set rebuilds the
// map when that is worth it; docs/set-rehash-en.md derives the rules and the
// bounds on the memory they leave behind.
//
// The zero value (var s Set[T]) is a usable empty set: the map is created by the
// first write. A nil *Set is not usable.
package set

import (
	"iter"
	"maps"
	"math/bits"
)

// Set is an unordered set of comparable values.
type Set[T comparable] struct {
	set      map[T]struct{}
	capacity int
}

// New returns an empty set with room for hint elements (at least 1).
func New[T comparable](hint int) *Set[T] {
	hint = max(1, hint)
	return &Set[T]{
		set:      make(map[T]struct{}, hint),
		capacity: hint,
	}
}

// ensure creates the map on the first write, reserving room for hint elements.
func (s *Set[T]) ensure(hint int) {
	if s.set == nil {
		s.resize(hint)
	}
}

// resize rebuilds the map sized for hint elements and moves the contents into
// it. A nil map copies as empty, so this is also the zero-value initialization.
func (s *Set[T]) resize(hint int) {
	rebuilt := New[T](hint)
	maps.Copy(rebuilt.set, s.set)
	*s = *rebuilt
}

// estimatedSlotsLeft returns how many more elements fit in the reserved step
// before the runtime has to grow the map.
func (s *Set[T]) estimatedSlotsLeft() int {
	return theoreticalSlots(max(s.Len(), s.capacity)) - s.Len()
}

// NewFromSlices returns the set of the distinct elements of all lists.
func NewFromSlices[T comparable](lists ...[]T) *Set[T] {
	total := 0
	for _, list := range lists {
		total += len(list)
	}
	res := New[T](total)
	for _, list := range lists {
		for _, v := range list {
			res.set[v] = struct{}{}
		}
	}
	if hintOversized(total, res.Len()) {
		res.Rehash()
	}
	return res
}

// Len returns the number of elements.
func (s *Set[T]) Len() int {
	return len(s.set)
}

// Add inserts a single element.
func (s *Set[T]) Add(v T) {
	s.ensure(1)
	s.set[v] = struct{}{}
}

// AddAll inserts every given element.
func (s *Set[T]) AddAll(e ...T) {
	if len(e) == 1 {
		s.Add(e[0])
		return
	}
	s.ensure(len(e))
	makeNew := false
	var total int
	if left := s.estimatedSlotsLeft(); left < len(e) {
		total = s.Len() + len(e)
		makeNew = 2*(left+s.Len()) < theoreticalSlots(total)
	}
	if makeNew {
		s.resize(total)
	}
	for _, v := range e {
		s.set[v] = struct{}{}
	}
	if makeNew && hintOversized(total, s.Len()) {
		s.Rehash()
	}
}

// AddSeq inserts every element produced by it.
func (s *Set[T]) AddSeq(it iter.Seq[T]) {
	s.ensure(0)
	for v := range it {
		s.set[v] = struct{}{}
	}
}

// Extend adds every element of the given sets to s.
func (s *Set[T]) Extend(sets ...*Set[T]) {
	extLen := 0
	for _, other := range sets {
		extLen += other.Len()
	}
	s.ensure(extLen)
	makeNew := false
	var total int
	if left := s.estimatedSlotsLeft(); left < extLen {
		total = s.Len() + extLen
		makeNew = 2*(left+s.Len()) < theoreticalSlots(total)
	}
	if makeNew {
		s.resize(total)
	}
	for _, other := range sets {
		maps.Copy(s.set, other.set)
	}
	if makeNew && hintOversized(total, s.Len()) {
		s.Rehash()
	}
}

// Retain keeps, in place, the elements present in every set. With no sets it
// is a no-op; with a single set it keeps the elements of that set.
func (s *Set[T]) Retain(sets ...*Set[T]) {
	if len(sets) == 0 {
		return
	}
	all := make([]*Set[T], 0, len(sets)+1)
	all = append(all, sets...)
	all = append(all, s)
	*s = *Intersection(all...)
}

// Difference returns s − other with the result on the step of its own length.
func (s *Set[T]) Difference(other *Set[T]) *Set[T] {
	m, n := s.Len(), other.Len()
	overlap := min(m, n)

	// Even removing every element of the smaller set leaves the result in the
	// same step, so copying and subtracting is the cheapest path: deletes cost
	// less than membership probes and no rebuild is needed for size. Subtract
	// still compacts if the result falls below the X threshold.
	if !hintOversized(m, m-overlap) {
		res := s.Copy()
		res.Subtract(other)
		return res
	}

	// Otherwise the result may land a step lower. Counting the common elements
	// costs one pass over the smaller set and buys a map sized exactly for the
	// result, so the delivered set is never over-allocated and never rebuilt.
	small, large := s, other
	if n < m {
		small, large = large, small
	}
	common := 0
	for v := range small.set {
		if _, in := large.set[v]; in {
			common++
		}
	}
	res := New[T](m - common)
	for v := range s.set {
		if _, in := other.set[v]; !in {
			res.set[v] = struct{}{}
		}
	}
	return res
}

// Subtract deletes, in place, the elements of the given sets.
func (s *Set[T]) Subtract(sets ...*Set[T]) {
	before := s.Len()
	for _, other := range sets {
		for v := range other.set {
			delete(s.set, v)
		}
	}
	if needsRehash(before, s.Len()) {
		s.Rehash()
	}
}

// Remove deletes the given elements. Removing absent elements is a no-op and
// never triggers a rehash.
func (s *Set[T]) Remove(e ...T) {
	before := s.Len()
	for _, v := range e {
		delete(s.set, v)
	}
	if needsRehash(before, s.Len()) {
		s.Rehash()
	}
}

// Rehash rebuilds the map sized for exactly its current length, dropping
// tombstones and any over-allocation. See docs/set-rehash-en.md §2.2.
func (s *Set[T]) Rehash() {
	s.resize(s.Len())
}

// Clone returns an independent copy that keeps the source's reserved capacity
// and over-allocation, so it keeps growing at the same cost. Use Copy for a
// compact copy.
func (s *Set[T]) Clone() *Set[T] {
	return &Set[T]{
		set:      maps.Clone(s.set),
		capacity: s.capacity,
	}
}

// Copy returns an independent, compact copy sized for exactly its length.
func (s *Set[T]) Copy() *Set[T] {
	res := New[T](s.Len())
	maps.Copy(res.set, s.set)
	return res
}

// Contains reports whether v is an element.
func (s *Set[T]) Contains(v T) bool {
	_, in := s.set[v]
	return in
}

// GetAll returns the elements as a new slice, in iteration order.
func (s *Set[T]) GetAll() []T {
	res := make([]T, s.Len())
	i := 0
	for v := range s.set {
		res[i] = v
		i++
	}
	return res
}

// IterAll returns the elements as an iterator.
func (s *Set[T]) IterAll() iter.Seq[T] {
	return maps.Keys(s.set)
}

// IsSubset reports whether every element of s is an element of other.
func (s *Set[T]) IsSubset(other *Set[T]) bool {
	if other.Len() < s.Len() {
		return false
	}
	for v := range s.set {
		if _, in := other.set[v]; !in {
			return false
		}
	}
	return true
}

// Disjoint reports whether s and other share no element.
func (s *Set[T]) Disjoint(other *Set[T]) bool {
	small, large := s, other
	if other.Len() < s.Len() {
		small, large = large, small
	}
	for v := range small.set {
		if _, in := large.set[v]; in {
			return false
		}
	}
	return true
}

// Equal reports whether s and other hold the same elements.
func (s *Set[T]) Equal(other *Set[T]) bool {
	return s.Len() == other.Len() && s.IsSubset(other)
}

// Union returns the elements of every set.
func Union[T comparable](sets ...*Set[T]) *Set[T] {
	res := New[T](0)
	res.Extend(sets...)
	return res
}

// Intersection returns the elements present in every set.
func Intersection[T comparable](sets ...*Set[T]) *Set[T] {
	if len(sets) == 0 {
		return New[T](0)
	}
	if len(sets) == 1 {
		return sets[0].Copy()
	}
	minSet := sets[0]
	for _, other := range sets[1:] {
		if other.Len() < minSet.Len() {
			minSet = other
		}
	}
	if minSet.Len() == 0 {
		return New[T](0)
	}
	res := New[T](minSet.Len())
	for v := range minSet.set {
		inAll := true
		for _, other := range sets {
			if other == minSet {
				continue
			}
			if _, in := other.set[v]; !in {
				inAll = false
				break
			}
		}
		if inAll {
			res.set[v] = struct{}{}
		}
	}
	if hintOversized(minSet.Len(), res.Len()) {
		res.Rehash()
	}
	return res
}

// theoreticalSlots returns the slots make(map, hint) reserves on creation:
// target = hint*8/7, a power-of-two directory of ceil(target/1024) entries and
// power-of-two tables of target/dir entries (at least 8). The rounding can leave
// the usable budget (7/8 of the slots) below hint: those are the cracks
// needsRehash looks for. See docs/set-rehash-en.md §2.1.
func theoreticalSlots(hint int) int {
	if hint <= 8 {
		return 8
	}
	target := hint * 8 / 7
	dirSize := pow2ceil((target + 1023) / 1024)
	table := max(pow2ceil(target/dirSize), 8)
	return dirSize * table
}

// pow2ceil returns the smallest power of two >= v (1 for v <= 1).
func pow2ceil(v int) int {
	if v <= 1 {
		return 1
	}
	return 1 << bits.Len(uint(v-1))
}

// needsRehash reports whether a map with history that fell from before to after
// elements is over-allocated enough to be worth rebuilding. It fires only on
// threshold crossings, so each step is rebuilt at most once. See
// docs/set-rehash-en.md §2.3 and §9.
func needsRehash(before, after int) bool {
	before, after = max(before, after), min(before, after)
	if before == after {
		return false
	}
	t1, t2 := theoreticalSlots(before), theoreticalSlots(after)
	if t1 != t2 {
		return true
	}
	if before <= 8 {
		return false
	}
	if t1 >= 2048 {
		x := 4 * t1 / 5
		return before >= x && after < x
	}
	band := 7 * t1 / 8
	return before > band && after <= band
}

// hintOversized reports whether a map built with make(hint) and filled to
// actual <= hint landed below the step it reserved. See docs/set-rehash-en.md
// §2.4.
func hintOversized(hint, actual int) bool {
	return theoreticalSlots(hint) != theoreticalSlots(actual)
}
