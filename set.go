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

// Set is an unordered set of comparable values. capacity is the hint the map
// was last reserved for, not the map's real capacity.
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

// needsFreshMap reports whether a batch of addLen insertions into s should land
// in a fresh map sized at target (docs/set-rehash-en.md §2.3).
func (s *Set[T]) needsFreshMap(target, addLen int) bool {
	left := s.estimatedSlotsLeft()
	return left < addLen && 2*(left+s.Len()) < theoreticalSlots(target)
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
	total := s.Len() + len(e)
	makeNew := s.needsFreshMap(total, len(e))
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

	// Fold largest first, probing each argument for elements new to s and to
	// those already folded: size for what s will reach, not the operand sum.
	// See README, "Why the probe samples 256 elements".
	target := s.Len() + extLen
	if s.needsFreshMap(target, extLen) {
		order := make([]*Set[T], len(sets))
		copy(order, sets)
		slices.SortFunc(order, func(x, y *Set[T]) int { return y.Len() - x.Len() })
		estimate := s.Len()
		for i, other := range order {
			if other.Len() == 0 {
				continue
			}
			estimate += sampleCount(other, func(v T) bool {
				if _, in := s.set[v]; in {
					return false
				}
				for _, prev := range order[:i] {
					if _, in := prev.set[v]; in {
						return false
					}
				}
				return true
			})
		}
		target = estimate
	}

	s.ensure(target)
	if s.needsFreshMap(target, target-s.Len()) {
		s.resize(target)
	}
	for _, other := range sets {
		maps.Copy(s.set, other.set)
	}
	s.compact()
}

// Retain keeps, in place, the elements present in every set.
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
	// One pass over s keeping the misses, sized from a probe of the smaller
	// operand and compacted below. See README, "Why the probe samples 256
	// elements".
	res := New[T](differenceReservation(s, other))
	for v := range s.set {
		if _, in := other.set[v]; !in {
			res.set[v] = struct{}{}
		}
	}
	res.compact()
	return res
}

// differenceReservation returns the hint for s − other: the receiver's length
// when no step can be crossed, a probe of the smaller operand otherwise.
func differenceReservation[T comparable](s, other *Set[T]) int {
	if maxDifference := s.Len() - other.Len(); maxDifference > 0 {
		if !hintOversized(s.Len(), maxDifference) {
			return s.Len()
		}
		return s.Len() - sampleCount(other, s.Contains)
	}
	return sampleCount(s, func(v T) bool { return !other.Contains(v) })
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

// Clone copies the source's reserved capacity and over-allocation, so the copy
// grows at the same cost; use Copy for a compact copy.
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

// compact rebuilds the map when its reservation sits a step above its length.
func (s *Set[T]) compact() {
	if hintOversized(s.capacity, s.Len()) {
		s.Rehash()
	}
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

	// Size from a probe of the smallest operand: sampleCount never returns more
	// than the population it probed, so the estimate is bounded by it. See README.
	capacity := minSet.Len()
	if capacity > overlapSample {
		inMin := func(v T) bool {
			for _, other := range sets {
				if other == minSet {
					continue
				}
				if _, in := other.set[v]; !in {
					return false
				}
			}
			return true
		}
		capacity = sampleCount(minSet, inMin)
	}

	res := New[T](capacity)
next:
	for v := range minSet.set {
		for _, other := range sets {
			if other == minSet {
				continue
			}
			if _, in := other.set[v]; !in {
				continue next
			}
		}
		res.set[v] = struct{}{}
	}
	res.compact()
	return res
}

// overlapSample is how many elements of an operand are probed before sizing
// the result. See README, "Why the probe samples 256 elements".
const overlapSample = 256

// sampleCount returns how many elements of small satisfy keep: exact up to
// overlapSample, estimated beyond it.
func sampleCount[T comparable](small *Set[T], keep func(T) bool) int {
	n := small.Len()
	if n <= overlapSample {
		count := 0
		for v := range small.set {
			if keep(v) {
				count++
			}
		}
		return count
	}
	probed, count := 0, 0
	for v := range small.set {
		if keep(v) {
			count++
		}
		probed++
		if probed == overlapSample {
			break
		}
	}
	return count * n / probed
}

// theoreticalSlots returns the slots make(map, hint) reserves: the rounding can
// leave the usable budget, 7/8 of the slots, below hint. See
// docs/set-rehash-en.md §2.1.
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
// docs/set-rehash-en.md §2.4 and §9.
func needsRehash(before, after int) bool {
	if before <= after {
		return false
	}
	t1, t2 := theoreticalSlots(before), theoreticalSlots(after)
	if t2 < t1 {
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

// hintOversized reports whether a map built with make(hint) landed below the
// step it reserved for actual elements. See docs/set-rehash-en.md §2.5.
func hintOversized(hint, actual int) bool {
	return theoreticalSlots(hint) > theoreticalSlots(actual)
}
