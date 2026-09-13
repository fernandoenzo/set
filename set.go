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

	// The arguments' elements need not be new: a union of two sets that share
	// half their elements grows the receiver by half of what they weigh, and
	// they overlap among themselves as well as with s. Folding the arguments
	// from the largest down, and probing each for elements absent from s and
	// from the arguments already folded, estimates each one's contribution in
	// turn and sums them. Reserving the whole upper bound instead leaves an
	// over-allocation that the compaction below then has to undo with a second
	// pass over the result.
	target := s.Len() + extLen
	if target >= samplingFloor {
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
	if left := s.estimatedSlotsLeft(); left < extLen && 2*(left+s.Len()) < theoreticalSlots(target) {
		s.resize(target)
	}
	for _, other := range sets {
		maps.Copy(s.set, other.set)
	}
	s.compact()
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
	// Intersection's result is compacted to its own length, so the assignment
	// adopts the step the surviving elements need rather than the step the
	// receiver reserved before dropping them.
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

	// Otherwise the result may land a step lower. One pass over s, probing other
	// and keeping the misses, fills a map reserved for the upper bound m; the
	// compaction below returns it to the step of its final length. Counting the
	// common elements first would cost a second pass over the smaller operand to
	// buy a reservation that is only closer than this one, and the map rounds
	// both to the same step often enough that the pass is not worth its price.
	res := New[T](m)
	for v := range s.set {
		if _, in := other.set[v]; !in {
			res.set[v] = struct{}{}
		}
	}
	res.compact()
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

	// The result is a subset of the smallest operand, but is usually far
	// smaller: reserving its length over-allocates by a factor that reaches two
	// whole steps when the operands overlap halfway, and the delivered set then
	// has to be rebuilt. Probing part of the smallest operand estimates how much
	// will survive and sizes the map from that instead.
	//
	// The probe only replaces the reservation, never the answer, so an estimate
	// that lands a step out is corrected by the same rebuild the over-allocation
	// would have caused. It is skipped below samplingFloor because a small set
	// has no wrong reservation worth the probe, and it is not worth one at all
	// in the disjoint case, where nothing survives and the rebuild is the work
	// that has to happen either way.
	capacity := minSet.Len()
	if capacity >= samplingFloor {
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
		if est := sampleCount(minSet, inMin); est < capacity {
			capacity = est
		}
	}

	res := New[T](capacity)
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
	res.compact()
	return res
}

// overlapSample is how many elements of an operand are probed to estimate an
// intersection before the result is sized. Map iteration order is randomised,
// so the probe is a uniform sample and the estimate is unbiased: its error is
// the binomial standard error sqrt((1-p)/(p·overlapSample)), which peaks at 6%
// for p = 1/2 and shrinks as the overlap grows. A wrong estimate cannot change
// an answer — only leave the reservation off its step, which a rebuild then
// corrects — so the sample needs to make those rebuilds rare, not impossible.
// 256 keeps the error far inside the factor-of-two width of a step while
// costing a rounding error against the pass it saves.
const overlapSample = 256

// samplingFloor is the size a set must reach before Intersection estimates its
// result rather than reserving for the whole smallest operand. Unlike the other
// callers, Intersection has no counting pass for the probe to replace, so it is
// extra work there and only pays once it is a small fraction of the set: at
// this floor the probe adds under 7% to the pass a wrong reservation repeats.
const samplingFloor = overlapSample * 16

// sampleCount returns how many elements of small satisfy keep. Up to
// overlapSample elements are counted, which makes the answer exact; beyond
// that a probe of the first overlapSample is scaled up. See overlapSample.
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

// compact rebuilds the map when its reservation sits a step above its length,
// so a set sized from an estimate still lands on the step of what it holds.
func (s *Set[T]) compact() {
	if hintOversized(s.capacity, s.Len()) {
		s.Rehash()
	}
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
