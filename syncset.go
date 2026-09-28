package set

import (
	"cmp"
	"iter"
	"slices"
	"sync"
	"sync/atomic"
)

// SyncSet is a Set with an internal RWMutex: readers share it, writers exclude
// readers and each other. The plain Set stays lock-free for callers that never
// share it.
//
// Operations over several sets take their locks in ascending identity order, so
// the argument order is irrelevant and aliasing is safe: s.Extend(s),
// s.Extend(x, x), s.Difference(s) and a.Extend(b) racing b.Extend(a) all work.
//
// IterAll and AddSeq run the caller's code under the lock, like sync.Map.Range;
// that code must not call a SyncSet method. Use IterSnapshot or GetAll.
//
// The zero value is a usable empty set. A nil *SyncSet is not usable, and a
// SyncSet must not be copied after first use.
type SyncSet[T comparable] struct {
	set    Set[T]
	locker sync.RWMutex
	idOnce sync.Once
	id     uint64
}

// lastID numbers SyncSets in construction order: the order of the identities
// is the global lock order.
var lastID atomic.Uint64

// setID returns the set's identity, assigning it on first use so that the zero
// value also takes part in the lock order.
func (s *SyncSet[T]) setID() uint64 {
	s.idOnce.Do(func() { s.id = lastID.Add(1) })
	return s.id
}

// lockReq is one lock a call needs: the set and whether it is a write lock.
type lockReq[T comparable] struct {
	set   *SyncSet[T]
	write bool
}

// lockAll acquires every request in ascending identity order, collapsing
// repeats into one lock (a write request winning over a read one), and returns
// them in acquisition order for unlockAll. The order is what rules out wait
// cycles; the collapse is what makes s.Difference(s) safe.
//
// It takes ownership of reqs.
func lockAll[T comparable](reqs []lockReq[T]) []lockReq[T] {
	for i := range reqs {
		reqs[i].set.setID()
	}
	slices.SortFunc(reqs, func(a, b lockReq[T]) int { return cmp.Compare(a.set.id, b.set.id) })

	kept := 0
	for _, req := range reqs {
		if kept > 0 && reqs[kept-1].set.id == req.set.id {
			if req.write {
				reqs[kept-1].write = true
			}
			continue
		}
		reqs[kept] = req
		kept++
	}
	reqs = reqs[:kept]

	for _, req := range reqs {
		if req.write {
			req.set.locker.Lock()
		} else {
			req.set.locker.RLock()
		}
	}
	return reqs
}

// unlockAll releases the locks taken by lockAll, in reverse order.
func unlockAll[T comparable](reqs []lockReq[T]) {
	for i := range slices.Backward(reqs) {
		if reqs[i].write {
			reqs[i].set.locker.Unlock()
		} else {
			reqs[i].set.locker.RUnlock()
		}
	}
}

// lockWith acquires s (write when write) plus every set in others (read-only),
// in the global lock order, and returns the acquisition for unlockAll.
func (s *SyncSet[T]) lockWith(write bool, others ...*SyncSet[T]) []lockReq[T] {
	reqs := make([]lockReq[T], 0, len(others)+1)
	reqs = append(reqs, lockReq[T]{set: s, write: write})
	for _, other := range others {
		reqs = append(reqs, lockReq[T]{set: other})
	}
	return lockAll(reqs)
}

// innersOf returns the inner sets, to hand to the unsynchronised Set API. The
// caller must hold every one of their locks.
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
	locks := s.lockWith(true, sets...)
	defer unlockAll(locks)
	s.set.Extend(innersOf(sets)...)
}

// Retain keeps, in place, the elements present in every set.
func (s *SyncSet[T]) Retain(sets ...*SyncSet[T]) {
	if len(sets) == 0 {
		return
	}
	locks := s.lockWith(true, sets...)
	defer unlockAll(locks)
	s.set.Retain(innersOf(sets)...)
}

// Difference returns s − other with the result on the step of its own length.
func (s *SyncSet[T]) Difference(other *SyncSet[T]) *SyncSet[T] {
	locks := s.lockWith(false, other)
	defer unlockAll(locks)
	return newSync(s.set.Difference(&other.set))
}

// Subtract deletes, in place, the elements of the given sets.
func (s *SyncSet[T]) Subtract(sets ...*SyncSet[T]) {
	if len(sets) == 0 {
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

// IterSnapshot returns the elements as an iterator over a private clone, so the
// loop body runs without the lock and may call methods of s, mutations
// included. The loop sees the set as it was when IterSnapshot was called, and
// the clone is paid on the call. Use IterAll when the body only reads.
func (s *SyncSet[T]) IterSnapshot() iter.Seq[T] {
	s.locker.RLock()
	defer s.locker.RUnlock()
	cloned := s.set.Clone()
	return cloned.IterAll()
}

// IsSubset reports whether every element of s is an element of other.
func (s *SyncSet[T]) IsSubset(other *SyncSet[T]) bool {
	locks := s.lockWith(false, other)
	defer unlockAll(locks)
	return s.set.IsSubset(&other.set)
}

// Disjoint reports whether s and other share no element.
func (s *SyncSet[T]) Disjoint(other *SyncSet[T]) bool {
	locks := s.lockWith(false, other)
	defer unlockAll(locks)
	return s.set.Disjoint(&other.set)
}

// Equal reports whether s and other hold the same elements.
func (s *SyncSet[T]) Equal(other *SyncSet[T]) bool {
	locks := s.lockWith(false, other)
	defer unlockAll(locks)
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
		reqs = append(reqs, lockReq[T]{set: other})
	}
	locks := lockAll(reqs)
	defer unlockAll(locks)
	return newSync(Intersection(innersOf(sets)...))
}
