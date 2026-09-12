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
	"slices"
)

// Set is an unordered set of comparable values.
type Set[T comparable] struct {
	set      map[T]struct{}
	capacity int
}

// New returns an empty set with room for capacity elements. capacity is clamped
// to at least 1.
func New[T comparable](capacity int) *Set[T] {
	capacity = max(1, capacity)
	return &Set[T]{
		set:      make(map[T]struct{}, capacity),
		capacity: capacity,
	}
}

// resize replaces the internal map with one sized for capacity elements and
// moves the current contents into it. capacity becomes the new reservation. A
// nil map copies as empty, so resize is also the first-write initialization of
// the zero value.
func (s *Set[T]) resize(capacity int) {
	rebuilt := New[T](capacity)
	maps.Copy(rebuilt.set, s.set)
	*s = *rebuilt
}

// ensure creates the internal map on the first write, reserving room for hint
// elements. Every writer calls it first: the zero value is a usable empty set.
func (s *Set[T]) ensure(hint int) {
	if s.set == nil {
		s.resize(hint)
	}
}

// estimatedSlotsLeft returns how many more elements fit in the slots reserved
// for the current step before the runtime has to grow the map.
func (s *Set[T]) estimatedSlotsLeft() int {
	setLen := s.Len()
	maxLenCap := max(setLen, s.capacity)
	return theoreticalSlots(maxLenCap) - setLen
}

// NewFromSlices returns the set of the distinct elements of all lists. It
// reserves room for the sum of the lengths and compacts if duplicates leave the
// result below that step.
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

// Add inserts the given elements. A batch big enough to leave the current step
// behind is served by one rebuild instead of by repeated organic growth.
func (s *Set[T]) Add(e ...T) {
	s.ensure(len(e))
	makeNew := false
	var totalLen int
	if estimatedSlots := s.estimatedSlotsLeft(); estimatedSlots < len(e) {
		totalLen = s.Len() + len(e)
		makeNew = 2*(estimatedSlots+s.Len()) < theoreticalSlots(totalLen)
	}
	if makeNew {
		s.resize(totalLen)
	}
	s.AddSeq(slices.Values(e))
	if makeNew && hintOversized(totalLen, s.Len()) {
		s.Rehash()
	}
}

// AddSeq inserts every element produced by it. The sequence does not announce
// its length, so the map grows organically.
func (s *Set[T]) AddSeq(it iter.Seq[T]) {
	s.ensure(0)
	for value := range it {
		s.set[value] = struct{}{}
	}
}

// Extend adds every element of sets to s.
func (s *Set[T]) Extend(sets ...*Set[T]) {
	extLen := 0
	for _, set := range sets {
		extLen += set.Len()
	}
	s.ensure(extLen)
	makeNew := false
	var totalLen int
	if estimatedSlotsLeft := s.estimatedSlotsLeft(); estimatedSlotsLeft < extLen {
		totalLen = s.Len() + extLen
		makeNew = 2*(estimatedSlotsLeft+s.Len()) < theoreticalSlots(totalLen)
	}
	if makeNew {
		s.resize(totalLen)
	}
	for _, set := range sets {
		maps.Copy(s.set, set.set)
	}
	if makeNew && hintOversized(totalLen, s.Len()) {
		s.Rehash()
	}
}

// Intersects leaves s with the elements present in every set. The name reads
// like a query, but the method mutates the receiver.
func (s *Set[T]) Intersects(sets ...*Set[T]) {
	if len(sets) == 0 {
		return
	}
	// Own slice: append on the caller's slice could write into its backing
	// array.
	allSets := make([]*Set[T], 0, len(sets)+1)
	allSets = append(allSets, sets...)
	allSets = append(allSets, s)
	newSet := Intersection(allSets...)
	*s = *newSet
}

// Difference returns s − set.
func (s *Set[T]) Difference(set *Set[T]) *Set[T] {
	if s.Len() < set.Len() {
		// set is the larger one: walk s and keep what is not in set.
		res := New[T](s.Len())
		for value := range s.set {
			if !set.Contains(value) {
				res.set[value] = struct{}{}
			}
		}
		if hintOversized(s.Len(), res.Len()) {
			res.Rehash()
		}
		return res
	}
	res := s.Copy()
	res.Subtract(set)
	return res
}

// Subtract deletes, in place, the elements of sets.
func (s *Set[T]) Subtract(sets ...*Set[T]) {
	before := s.Len()
	for _, set := range sets {
		for value := range set.IterAll() {
			delete(s.set, value)
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
	for _, value := range e {
		delete(s.set, value)
	}
	if needsRehash(before, s.Len()) {
		s.Rehash()
	}
}

// Rehash rebuilds the internal map with room for exactly its current length: it
// rehashes every key, drops tombstones and lands on the smallest step.
//
// It deliberately builds a fresh map with maps.Copy rather than maps.Clone:
// Clone replicates the runtime structure as is (same slots, same tombstones) and
// would keep the very over-allocation this method removes. See docs §8.
func (s *Set[T]) Rehash() {
	s.resize(s.Len())
}

// Clone returns an independent copy that keeps the source's reserved capacity,
// so it keeps growing at the same cost. The copy also inherits whatever
// over-allocation the source has: use Copy for a compact copy.
func (s *Set[T]) Clone() *Set[T] {
	return &Set[T]{
		set:      maps.Clone(s.set),
		capacity: s.capacity,
	}
}

// Copy returns an independent, compact copy with room for exactly its current
// length: it does not inherit the source's over-allocation.
func (s *Set[T]) Copy() *Set[T] {
	newSet := New[T](s.Len())
	maps.Copy(newSet.set, s.set)
	return newSet
}

// Contains reports whether e is an element.
func (s *Set[T]) Contains(e T) bool {
	_, res := s.set[e]
	return res
}

// GetAll returns the elements as a new slice, in iteration order.
func (s *Set[T]) GetAll() []T {
	res := make([]T, s.Len())
	i := 0
	for key := range s.IterAll() {
		res[i] = key
		i++
	}
	return res
}

// IterAll returns the elements as an iterator.
func (s *Set[T]) IterAll() iter.Seq[T] {
	return maps.Keys(s.set)
}

// IsSubset reports whether every element of s is an element of set.
func (s *Set[T]) IsSubset(set *Set[T]) bool {
	if set.Len() < s.Len() {
		return false
	}
	for value := range s.IterAll() {
		if !set.Contains(value) {
			return false
		}
	}
	return true
}

// Disjoint reports whether s and set share no element.
func (s *Set[T]) Disjoint(set *Set[T]) bool {
	small, large := s, set
	if large.Len() < small.Len() {
		small, large = large, small
	}
	for value := range small.IterAll() {
		if large.Contains(value) {
			return false
		}
	}
	return true
}

// Equal reports whether s and set hold the same elements.
func (s *Set[T]) Equal(set *Set[T]) bool {
	if s.Len() != set.Len() {
		return false
	}
	return s.IsSubset(set)
}

// Union returns the elements of every set.
func Union[T comparable](sets ...*Set[T]) *Set[T] {
	newSet := New[T](0)
	newSet.Extend(sets...)
	return newSet
}

// Intersection returns the elements present in every set. With no sets it
// returns the empty set; with a single one, a compact copy of it.
func Intersection[T comparable](sets ...*Set[T]) *Set[T] {
	if len(sets) == 0 {
		return New[T](0)
	}
	if len(sets) == 1 {
		return sets[0].Copy()
	}
	minSet := sets[0]
	for _, set := range sets[1:] {
		if set.Len() < minSet.Len() {
			minSet = set
		}
	}
	if minSet.Len() == 0 {
		return New[T](0)
	}
	// Walk the smallest set and check membership in the others: fewer lookups
	// than the other way around.
	newSet := New[T](minSet.Len())
	for value := range minSet.IterAll() {
		inAll := true
		for _, set := range sets {
			if set == minSet {
				continue // the key already came from minSet
			}
			if !set.Contains(value) {
				inAll = false
				break
			}
		}
		if inAll {
			newSet.set[value] = struct{}{}
		}
	}
	if hintOversized(minSet.Len(), newSet.Len()) {
		newSet.Rehash()
	}
	return newSet
}

// theoreticalSlots returns the slots make(map, hint) reserves when the map is
// created. Model of the runtime (Go 1.24+, Swiss maps):
//
//	target = hint * 8 / 7
//	dir    = 2^ceil(log2(ceil(target/1024)))   (power of two)
//	table  = 2^ceil(log2(target/dir))          (at least 8)
//	T      = dir * table
//
// The rounding can leave the usable budget (7/8 of the slots) below hint: those
// are the cracks needsRehash looks for.
func theoreticalSlots(hint int) int {
	if hint <= 8 {
		return 8 // small map: one group after the first insert
	}
	target := hint * 8 / 7
	dirSize := pow2ceil((target + 1023) / 1024)
	table := pow2ceil(target / dirSize)
	table = max(table, 8)
	return dirSize * table
}

// pow2ceil returns the smallest power of two >= v (1 for v <= 1).
func pow2ceil(v int) int {
	if v <= 1 {
		return 1
	}
	return 1 << bits.Len(uint(v-1))
}

// needsRehash reports whether a map WITH HISTORY (grown organically or with
// previous deletions) that fell from before to after elements is over-allocated
// enough to be worth rebuilding. Only Remove and Subtract use it: for maps that
// were just built see hintOversized.
//
// A map ends up over-allocated in two situations:
//
//  1. It fell one theoretical step (T(before) != T(after)): the real map never
//     has fewer slots than the step it started from, so a lower step means
//     guaranteed waste.
//
//  2. It was sitting just above the top of its step. The runtime spreads keys
//     across tables with a random hash, so close to the top (7/8 of the slots)
//     some table overshoots its budget, splits, and the map keeps more slots
//     than make(after) would assign.
//
// Both conditions are CROSSINGS, not zones: they fire only when going DOWN
// through a threshold, never while sitting above it. Deletions only shrink the
// length, so each threshold is crossed at most once per step and a rebuild loop
// is impossible (the previous zone rule rebuilt 116 times in a row, 61 of them
// useless, over a single 100k -> 50k descent; see docs §9).
//
// Condition 2 uses X = 4T/5 (load 0.80) rather than the 7T/8 top: rebuilding
// with make(len) lands at load len/T, where the multinomial spread of the hash
// can still split a table, and at load 0.875 that probability is ~1 at any T.
// X leaves ~2.7 sigma of margin per table, roughly constant in T (verified from
// 2k to 1M slots), so the constant does not need to depend on T. See docs §6-§7
// for the derivation, including why 0.775T catches the residue too late and
// 0.825T is too eager.
func needsRehash(before, after int) bool {
	before, after = max(before, after), min(before, after)
	if before == after {
		return false // nothing was removed (e.g. Remove of absent elements)
	}
	t1, t2 := theoreticalSlots(before), theoreticalSlots(after)
	if t1 != t2 {
		return true // condition 1: fell one step
	}
	if before <= 8 {
		return false // small map: 8 slots is the floor
	}
	if t1 >= 2048 {
		x := 4 * t1 / 5 // multi-table: X threshold
		return before >= x && after < x
	}
	// T <= 1024: threshold is the 7T/8 top. Here a map rebuilt to a single
	// table (or an exact budget) lands clean on that top, with no variance
	// margin that would justify waiting for more.
	band := 7 * t1 / 8
	return before > band && after <= band
}

// hintOversized reports whether a map FRESHLY BUILT with make(hint) and filled
// up to actual <= hint landed below the step it reserved, that is
// T(hint) != T(actual). Nothing else matters: a fresh map at load <= 7/8 is
// already at its natural size, and rebuilding it would just re-roll the same
// hashes at the same O(n) cost. The rule for maps with history (needsRehash)
// must not be used here: its crossing thresholds would fire useless rebuilds on
// clean maps.
func hintOversized(hint, actual int) bool {
	return theoreticalSlots(hint) != theoreticalSlots(actual)
}
