package set

import (
	"iter"
	"sync"
)

type SyncSet[T comparable] struct {
	set    *Set[T]
	locker sync.RWMutex
}

// New returns an empty set with room for hint elements (at least 1).
func NewSync[T comparable](hint int) *SyncSet[T] {
	return &SyncSet[T]{
		set: New[T](hint),
	}
}

// NewFromSlices returns the set of the distinct elements of all lists.
func NewSyncFromSlices[T comparable](lists ...[]T) *SyncSet[T] {
	innerSet := NewFromSlices[T](lists...)
	return &SyncSet[T]{set: innerSet}
}

// Len returns the number of elements.
func (s *SyncSet[T]) Len() int {
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

// AddSeq inserts every element produced by it.
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
	unsafeSets := make([]*Set[T], 0, len(sets))
	for _, set := range sets {
		if set == s {
			continue
		}
		set.locker.RLock()
		defer set.locker.RUnlock()
		unsafeSets = append(unsafeSets, set.set)
	}
	s.locker.Lock()
	defer s.locker.Unlock()
	s.set.Extend(unsafeSets...)
}

// Retain keeps, in place, the elements present in every set.
func (s *SyncSet[T]) Retain(sets ...*SyncSet[T]) {
	if len(sets) == 0 {
		return
	}
	unsafeSets := make([]*Set[T], 0, len(sets))
	for _, set := range sets {
		if set == s {
			continue
		}
		set.locker.RLock()
		defer set.locker.RUnlock()
		unsafeSets = append(unsafeSets, set.set)
	}
	s.locker.Lock()
	defer s.locker.Unlock()
	s.set.Retain(unsafeSets...)
}

// Difference returns s − other with the result on the step of its own length.
func (s *SyncSet[T]) Difference(other *SyncSet[T]) *SyncSet[T] {
	s.locker.RLock()
	defer s.locker.RUnlock()
	other.locker.RLock()
	defer other.locker.RUnlock()
	innterDifference := s.set.Difference(other.set)
	return &SyncSet[T]{set: innterDifference}
}

// Subtract deletes, in place, the elements of the given sets.
func (s *SyncSet[T]) Subtract(sets ...*SyncSet[T]) {
	unsafeSets := make([]*Set[T], 0, len(sets))
	for _, set := range sets {
		if set == s {
			break
		}
		set.locker.RLock()
		defer set.locker.RUnlock()
		unsafeSets = append(unsafeSets, set.set)
	}
	s.locker.Lock()
	defer s.locker.Unlock()
	if len(sets) != len(unsafeSets) {
		s.set = New[T](0)
	} else {
		s.set.Subtract(unsafeSets...)
	}
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
	s.locker.RLock()
	defer s.locker.RUnlock()
	s.set.Rehash()
}

// Clone copies the source's reserved capacity and over-allocation, so the copy
// grows at the same cost; use Copy for a compact copy.
func (s *SyncSet[T]) Clone() *SyncSet[T] {
	s.locker.RLock()
	defer s.locker.RUnlock()
	return &SyncSet[T]{set: s.set.Clone()}
}

// Copy returns an independent, compact copy sized for exactly its length.
func (s *SyncSet[T]) Copy() *SyncSet[T] {
	s.locker.RLock()
	defer s.locker.RUnlock()
	return &SyncSet[T]{set: s.set.Copy()}
}

// Contains reports whether v is an element.
func (s *SyncSet[T]) Contains(v T) bool {
	s.locker.RLock()
	defer s.locker.RUnlock()
	return s.set.Contains(v)
}

// GetAll returns the elements as a new slice, in iteration order.
func (s *SyncSet[T]) GetAll() []T {
	s.locker.RLock()
	defer s.locker.RUnlock()
	return s.set.GetAll()
}

// IterAll returns the elements as an iterator.
func (s *SyncSet[T]) IterAll() iter.Seq[T] {
	return func(yield func(T) bool) {
		s.locker.RLock()
		defer s.locker.RUnlock()
		for elemento := range s.set.IterAll() {
			if !yield(elemento) {
				return
			}
		}
	}
}

// IsSubset reports whether every element of s is an element of other.
func (s *SyncSet[T]) IsSubset(other *SyncSet[T]) bool {
	if other.Len() < s.Len() {
		return false
	}
	s.locker.RLock()
	defer s.locker.RUnlock()
	other.locker.RLock()
	defer other.locker.RUnlock()
	return s.set.IsSubset(other.set)
}

// Disjoint reports whether s and other share no element.
func (s *SyncSet[T]) Disjoint(other *SyncSet[T]) bool {
	s.locker.RLock()
	defer s.locker.RUnlock()
	other.locker.RLock()
	defer other.locker.RUnlock()
	return s.set.Disjoint(other.set)
}

// Equal reports whether s and other hold the same elements.
func (s *SyncSet[T]) Equal(other *SyncSet[T]) bool {
	return s.Len() == other.Len() && s.IsSubset(other)
}

// Union returns the elements of every set.
func SyncUnion[T comparable](sets ...*SyncSet[T]) *SyncSet[T] {
	res := NewSync[T](0)
	res.Extend(sets...)
	return res
}

// Intersection returns the elements present in every set.
func SyncIntersection[T comparable](sets ...*SyncSet[T]) *SyncSet[T] {
	unsafeSets := make([]*Set[T], len(sets))
	for i, set := range sets {
		set.locker.RLock()
		defer set.locker.RUnlock()
		unsafeSets[i] = set.set
	}
	return &SyncSet[T]{set: Intersection(unsafeSets...)}
}
