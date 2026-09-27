package set

import (
	"cmp"
	"iter"
	"slices"
	"sync"
	"sync/atomic"
)

// SyncSet is a Set that is safe for concurrent use: readers share the internal
// lock, and writers exclude readers and each other. The plain Set stays
// lock-free for callers that never share it.
//
// An operation over several sets locks all its operands at once, always in the
// same global order (the order of the sets' identities, assigned in
// construction order), so the order the caller passes them in is irrelevant
// and aliasing is safe: s.Extend(s), s.Retain(s), s.Subtract(s),
// s.Extend(x, x), s.Difference(s), and two goroutines running a.Extend(b) and
// b.Extend(a) at the same time are all well defined. A set is never locked
// twice by one call, so a pending writer cannot wedge a second request either.
//
// Two methods run the caller's code under the lock, exactly like
// sync.Map.Range: the body of an IterAll loop and the sequence passed to
// AddSeq. Neither may call a method of any SyncSet; use GetAll (a snapshot to
// iterate freely) or AddAll (values already in hand) instead.
//
// The zero value (var s SyncSet[T]) is a usable empty set. A nil *SyncSet is
// not usable, and a SyncSet must not be copied after first use.
type SyncSet[T comparable] struct {
	set    Set[T]
	locker sync.RWMutex
	idOnce sync.Once
	id     uint64
}

// lastID numbers SyncSets in construction order. The order of the identities
// is the global lock order that keeps multi-set operations deadlock-free.
var lastID atomic.Uint64

// setID returns the set's identity, assigning it on first use so that every
// route into existence, the zero value included, takes part in the lock order.
func (s *SyncSet[T]) setID() uint64 {
	s.idOnce.Do(func() { s.id = lastID.Add(1) })
	return s.id
}

// lockReq is one lock a call needs: the set and whether it is a write lock.
type lockReq[T comparable] struct {
	s *SyncSet[T]
	w bool
}

// lockAll acquires every request in ascending identity order, collapsing
// repeats so that each set is locked once with a write request winning over a
// read one, and returns the requests in acquisition order for unlockAll.
//
// The ascending order is what makes multi-set operations deadlock-free: a
// goroutine holding locks can only wait for a lock whose identity is higher
// than every identity it already holds, so no wait cycle can close. Collapsing
// repeats is what makes aliasing safe: s.Difference(s) asks for s's read lock
// twice and takes it once, so a pending writer cannot wedge the second request.
//
// It takes ownership of reqs.
func lockAll[T comparable](reqs []lockReq[T]) []lockReq[T] {
	for i := range reqs {
		reqs[i].s.setID()
	}
	slices.SortFunc(reqs, func(a, b lockReq[T]) int { return cmp.Compare(a.s.id, b.s.id) })

	kept := 0
	for _, r := range reqs {
		if kept > 0 && reqs[kept-1].s == r.s {
			if r.w {
				reqs[kept-1].w = true
			}
			continue
		}
		reqs[kept] = r
		kept++
	}
	reqs = reqs[:kept]

	for _, r := range reqs {
		if r.w {
			r.s.locker.Lock()
		} else {
			r.s.locker.RLock()
		}
	}
	return reqs
}

// unlockAll releases the locks taken by lockAll, in reverse order.
func unlockAll[T comparable](reqs []lockReq[T]) {
	for i := len(reqs) - 1; i >= 0; i-- {
		if reqs[i].w {
			reqs[i].s.locker.Unlock()
		} else {
			reqs[i].s.locker.RUnlock()
		}
	}
}

// pairReqs is the request set of the two-set operations: exactly two locks, so
// it travels in a value and those operations allocate nothing.
type pairReqs[T comparable] struct {
	reqs [2]lockReq[T]
	n    int
}

// lockPair is lockAll specialised to a receiver and one operand, in identity
// order and collapsing into a single lock when both are the same set. The
// binary predicates are documented as allocation-free, so they take their two
// requests in a value instead of a slice.
func lockPair[T comparable](a *SyncSet[T], aw bool, b *SyncSet[T], bw bool) pairReqs[T] {
	a.setID()
	b.setID()
	var out pairReqs[T]
	if a == b {
		out.n = 1
		out.reqs[0] = lockReq[T]{s: a, w: aw || bw}
	} else {
		out.n = 2
		out.reqs[0] = lockReq[T]{s: a, w: aw}
		out.reqs[1] = lockReq[T]{s: b, w: bw}
		if a.id > b.id {
			out.reqs[0], out.reqs[1] = out.reqs[1], out.reqs[0]
		}
	}
	for i := range out.n {
		if out.reqs[i].w {
			out.reqs[i].s.locker.Lock()
		} else {
			out.reqs[i].s.locker.RLock()
		}
	}
	return out
}

// unlockPair releases the locks taken by lockPair, in reverse order.
func unlockPair[T comparable](reqs pairReqs[T]) {
	for i := reqs.n - 1; i >= 0; i-- {
		if reqs.reqs[i].w {
			reqs.reqs[i].s.locker.Unlock()
		} else {
			reqs.reqs[i].s.locker.RUnlock()
		}
	}
}

// lockWith acquires s (write when write) plus every set in others (read-only),
// all in the global lock order, and returns the acquisition for unlockAll.
func (s *SyncSet[T]) lockWith(write bool, others ...*SyncSet[T]) []lockReq[T] {
	reqs := make([]lockReq[T], 0, len(others)+1)
	reqs = append(reqs, lockReq[T]{s: s, w: write})
	for _, other := range others {
		reqs = append(reqs, lockReq[T]{s: other})
	}
	return lockAll(reqs)
}

// innersOf returns the inner sets of the given wrappers, to hand to the
// unsynchronised Set API. The caller must hold every one of their locks.
func innersOf[T comparable](sets []*SyncSet[T]) []*Set[T] {
	out := make([]*Set[T], len(sets))
	for i, other := range sets {
		out[i] = &other.set
	}
	return out
}

// newSync wraps a freshly built set and gives it an identity.
func newSync[T comparable](inner *Set[T]) *SyncSet[T] {
	res := &SyncSet[T]{set: *inner}
	res.setID()
	return res
}

// NewSync returns an empty set with room for hint elements (at least 1).
func NewSync[T comparable](hint int) *SyncSet[T] {
	return newSync(New[T](hint))
}

// NewSyncFromSlices returns the set of the distinct elements of all lists.
func NewSyncFromSlices[T comparable](lists ...[]T) *SyncSet[T] {
	return newSync(NewFromSlices[T](lists...))
}

// Len returns the number of elements.
func (s *SyncSet[T]) Len() int {
	s.locker.RLock()
	defer s.locker.RUnlock()
	return s.set.Len()
}

// Add inserts a single element.
func (s *SyncSet[T]) Add(v T) {
	s.locker.Lock()
	defer s.locker.Unlock()
	s.set.Add(v)
}

// AddAll inserts every given element.
func (s *SyncSet[T]) AddAll(e ...T) {
	s.locker.Lock()
	defer s.locker.Unlock()
	s.set.AddAll(e...)
}

// AddSeq inserts every element produced by it. The sequence and the code it
// calls run under s's write lock and must not call any SyncSet method.
func (s *SyncSet[T]) AddSeq(it iter.Seq[T]) {
	s.locker.Lock()
	defer s.locker.Unlock()
	s.set.AddSeq(it)
}

// Extend adds every element of the given sets to s.
func (s *SyncSet[T]) Extend(sets ...*SyncSet[T]) {
	if len(sets) == 0 {
		return
	}
	if len(sets) == 1 {
		locks := lockPair(s, true, sets[0], false)
		defer unlockPair(locks)
		s.set.Extend(&sets[0].set)
		return
	}
	locks := s.lockWith(true, sets...)
	defer unlockAll(locks)
	s.set.Extend(innersOf(sets)...)
}

// Retain keeps, in place, the elements present in every set.
func (s *SyncSet[T]) Retain(sets ...*SyncSet[T]) {
	if len(sets) == 0 {
		return
	}
	if len(sets) == 1 {
		locks := lockPair(s, true, sets[0], false)
		defer unlockPair(locks)
		s.set.Retain(&sets[0].set)
		return
	}
	locks := s.lockWith(true, sets...)
	defer unlockAll(locks)
	s.set.Retain(innersOf(sets)...)
}

// Difference returns s − other with the result on the step of its own length.
func (s *SyncSet[T]) Difference(other *SyncSet[T]) *SyncSet[T] {
	locks := lockPair(s, false, other, false)
	defer unlockPair(locks)
	return newSync(s.set.Difference(&other.set))
}

// Subtract deletes, in place, the elements of the given sets.
func (s *SyncSet[T]) Subtract(sets ...*SyncSet[T]) {
	if len(sets) == 0 {
		return
	}
	if len(sets) == 1 {
		locks := lockPair(s, true, sets[0], false)
		defer unlockPair(locks)
		s.set.Subtract(&sets[0].set)
		return
	}
	locks := s.lockWith(true, sets...)
	defer unlockAll(locks)
	s.set.Subtract(innersOf(sets)...)
}

// Remove deletes the given elements. Removing absent elements is a no-op and
// never triggers a rehash.
func (s *SyncSet[T]) Remove(e ...T) {
	s.locker.Lock()
	defer s.locker.Unlock()
	s.set.Remove(e...)
}

// Rehash rebuilds the map sized for exactly its current length, dropping
// tombstones and any over-allocation. See docs/set-rehash-en.md §2.2.
func (s *SyncSet[T]) Rehash() {
	s.locker.Lock()
	defer s.locker.Unlock()
	s.set.Rehash()
}

// Clone copies the source's reserved capacity and over-allocation, so the copy
// grows at the same cost; use Copy for a compact copy.
func (s *SyncSet[T]) Clone() *SyncSet[T] {
	s.locker.RLock()
	defer s.locker.RUnlock()
	return newSync(s.set.Clone())
}

// Copy returns an independent, compact copy sized for exactly its length.
func (s *SyncSet[T]) Copy() *SyncSet[T] {
	s.locker.RLock()
	defer s.locker.RUnlock()
	return newSync(s.set.Copy())
}

// Contains reports whether v is an element.
func (s *SyncSet[T]) Contains(v T) bool {
	s.locker.RLock()
	defer s.locker.RUnlock()
	return s.set.Contains(v)
}

// GetAll returns the elements as a new slice, in iteration order. It is the
// snapshot to iterate when the body has to touch a set.
func (s *SyncSet[T]) GetAll() []T {
	s.locker.RLock()
	defer s.locker.RUnlock()
	return s.set.GetAll()
}

// IterAll returns the elements as an iterator. The loop body runs under s's
// read lock, like sync.Map.Range, and must not call any SyncSet method.
func (s *SyncSet[T]) IterAll() iter.Seq[T] {
	return func(yield func(T) bool) {
		s.locker.RLock()
		defer s.locker.RUnlock()
		for v := range s.set.IterAll() {
			if !yield(v) {
				return
			}
		}
	}
}

// IsSubset reports whether every element of s is an element of other.
func (s *SyncSet[T]) IsSubset(other *SyncSet[T]) bool {
	locks := lockPair(s, false, other, false)
	defer unlockPair(locks)
	return s.set.IsSubset(&other.set)
}

// Disjoint reports whether s and other share no element.
func (s *SyncSet[T]) Disjoint(other *SyncSet[T]) bool {
	locks := lockPair(s, false, other, false)
	defer unlockPair(locks)
	return s.set.Disjoint(&other.set)
}

// Equal reports whether s and other hold the same elements.
func (s *SyncSet[T]) Equal(other *SyncSet[T]) bool {
	locks := lockPair(s, false, other, false)
	defer unlockPair(locks)
	return s.set.Equal(&other.set)
}

// SyncUnion returns the elements of every set.
func SyncUnion[T comparable](sets ...*SyncSet[T]) *SyncSet[T] {
	res := NewSync[T](0)
	res.Extend(sets...)
	return res
}

// SyncIntersection returns the elements present in every set.
func SyncIntersection[T comparable](sets ...*SyncSet[T]) *SyncSet[T] {
	reqs := make([]lockReq[T], 0, len(sets))
	for _, other := range sets {
		reqs = append(reqs, lockReq[T]{s: other})
	}
	locks := lockAll(reqs)
	defer unlockAll(locks)
	return newSync(Intersection(innersOf(sets)...))
}
