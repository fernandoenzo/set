package set

import (
	"cmp"
	"hash/maphash"
	"iter"
	"slices"
	"sync"
	"sync/atomic"
)

// ShardedSyncSet is a Set split across shardCount independent shards, each with
// its own RWMutex. It exists to remove the throughput ceiling of SyncSet.
//
// SyncSet serialises every read through one lock word: RLock and RUnlock each
// write readerCount, that word shares a cache line with the semaphore fields,
// and 32 readers bounce the line between cores instead of reading in parallel.
// Measured on an i9-14900KF, SyncSet.Contains goes from 11.3 ns with one
// goroutine to 41.6 ns with 32 -- 0.27x, it loses throughput as readers are
// added. A bare sync.RWMutex with no map behind it reproduces that number, so
// the cost is the lock's own atomic writes, not the probe.
//
// Sharding spreads those writes over shardCount lock words, so the probability
// that two cores contend for the same line falls with the shard count. The
// count is a package constant rather than per-set because every operation that
// spans several sets decomposes shard by shard, and that requires all sets to
// agree on where a value lives.
//
// The shard of a value is a pure function of the value: shardIndex uses one
// process-wide maphash seed, so shard i of one set holds exactly the same
// values as shard i of another. That is what lets Extend, Retain, Subtract,
// Difference, IsSubset, Disjoint, Equal, Union and Intersection run as the
// corresponding Set operation applied to each pair of shards, reusing the
// whole tested core instead of reimplementing the algebra under locks.
//
// The zero value is a usable empty set. Like SyncSet, IterAll and AddSeq run
// the caller's code under the lock, and a nil *ShardedSyncSet is not usable.
type ShardedSyncSet[T comparable] struct {
	shards [shardCount]shard[T]
	idOnce sync.Once
	id     uint64
}

// shardCount is the number of shards, chosen as the balance of two measured
// curves. Read scaling improves monotonically with the count (32 goroutines on
// a 10,000-element set: 8.0 ns at 32 shards, 5.2 at 64, 3.4 at 128, 2.6 at
// 256), while every operation that must hold the whole set pays the count in
// lock acquisitions (IsSubset on a 100-element set: 1.9 us, 2.7, 4.6, 7.2) and
// a fresh insert is best in the middle. 64 takes 7.4x the read throughput of
// SyncSet while keeping the whole-set path affordable; 256 would double that
// read rate for 2.6x the fixed cost. See README, "Sharding".
const shardCount = 64

// shardSeed is the process-wide hash seed. One seed for every set is what makes
// a value's shard index independent of the set that holds it.
var shardSeed = maphash.MakeSeed()

// shardIndex returns the shard that holds v. It is a pure function of v, which
// is the invariant every multi-set operation below depends on.
func shardIndex[T comparable](v T) uint64 {
	return maphash.Comparable(shardSeed, v) & (shardCount - 1)
}

// shard is one independent Set with its own lock. The trailing padding brings
// the struct to 64 bytes on 64-bit platforms so that neighbouring shards do not
// share a cache line: without it a write to one shard would invalidate the
// other's copy in every core, which is the cost this type is built to avoid.
// Go has no alignment directive, so the padding fixes the size but cannot force
// the base address; TestShardFitsCacheLine pins the size.
type shard[T comparable] struct {
	mu  sync.RWMutex
	set Set[T]
	// count is len(set) maintained alongside every mutation, so Len is a sum of
	// atomic loads instead of a pass over every shard's lock: measured at 447 ns
	// to lock all 64 shards against 30 ns to read 64 counters. It is updated
	// only under mu, and always from the map's own length, so it cannot drift.
	count atomic.Int64
	_     [16]byte
}

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

// addLocked inserts v and keeps the shard's count exact. The caller must hold
// the shard's write lock.
func (sh *shard[T]) addLocked(v T) {
	before := sh.set.Len()
	sh.set.Add(v)
	if after := sh.set.Len(); after != before {
		sh.count.Add(int64(after - before))
	}
}

// removeLocked deletes v and keeps the shard's count exact. The caller must
// hold the shard's write lock.
func (sh *shard[T]) removeLocked(v T) {
	before := sh.set.Len()
	sh.set.Remove(v)
	if after := sh.set.Len(); after != before {
		sh.count.Add(int64(after - before))
	}
}

// bulkLocked runs a bulk mutation and reconciles the count from the map's own
// length, so Extend, Retain and Subtract cannot drift it. The caller must hold
// the shard's write lock.
func (sh *shard[T]) bulkLocked(f func(*Set[T])) {
	f(&sh.set)
	sh.count.Store(int64(sh.set.Len()))
}

// reconcileCounts sets every shard's counter from its map. It is for the
// construction paths that write into the shard maps directly; the mutation
// paths keep the counter themselves.
func (s *ShardedSyncSet[T]) reconcileCounts() {
	for i := range s.shards {
		s.shards[i].count.Store(int64(s.shards[i].set.Len()))
	}
}

// setID returns the set's identity, assigning it on first use so that the zero
// value also takes part in the lock order. It shares the monotonic counter with
// SyncSet: the order only has to be total among the sets of one call.
func (s *ShardedSyncSet[T]) setID() uint64 {
	s.idOnce.Do(func() { s.id = lastID.Add(1) })
	return s.id
}

// NewShardedSync returns an empty set with room for hint elements.
func NewShardedSync[T comparable](hint int) *ShardedSyncSet[T] {
	res := &ShardedSyncSet[T]{}
	hint /= shardCount
	for i := range res.shards {
		res.shards[i].set = *New[T](hint)
	}
	res.setID()
	return res
}

// NewShardedSyncFromSlices returns the set of the distinct elements of all
// lists.
func NewShardedSyncFromSlices[T comparable](lists ...[]T) *ShardedSyncSet[T] {
	res := &ShardedSyncSet[T]{}
	for _, list := range lists {
		for _, v := range list {
			res.shards[shardIndex(v)].set.Add(v)
		}
	}
	res.reconcileCounts()
	res.setID()
	return res
}

// ---------------------------------------------------------------------------
// Locking
// ---------------------------------------------------------------------------

// shardReq is one set whose shards a call needs, and whether it needs them
// under the write lock.
type shardReq[T comparable] struct {
	set   *ShardedSyncSet[T]
	write bool
}

// lockShards acquires every shard of every requested set, ordering sets by
// ascending identity and, within a set, shards by ascending index, and
// collapsing repeated sets into one request (a write request winning over a
// read one). Taking a set's shards as one contiguous block of that order keeps
// the order total, so a goroutine can only wait for a lock that is greater than
// every lock it holds and no wait cycle can close. It returns the requests in
// acquisition order, for unlockShards.
func lockShards[T comparable](reqs []shardReq[T]) []shardReq[T] {
	for i := range reqs {
		reqs[i].set.setID()
	}
	slices.SortFunc(reqs, func(a, b shardReq[T]) int { return cmp.Compare(a.set.id, b.set.id) })

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
		for i := range req.set.shards {
			if req.write {
				req.set.shards[i].mu.Lock()
			} else {
				req.set.shards[i].mu.RLock()
			}
		}
	}
	return reqs
}

// unlockShards releases the locks taken by lockShards, in reverse order: sets
// backwards, and each set's shards backwards.
func unlockShards[T comparable](reqs []shardReq[T]) {
	for i := len(reqs) - 1; i >= 0; i-- {
		shards := &reqs[i].set.shards
		for j := len(shards) - 1; j >= 0; j-- {
			if reqs[i].write {
				shards[j].mu.Unlock()
			} else {
				shards[j].mu.RUnlock()
			}
		}
	}
}

// lockSelf acquires every shard of s.
func (s *ShardedSyncSet[T]) lockSelf(write bool) []shardReq[T] {
	return lockShards([]shardReq[T]{{set: s, write: write}})
}

// lockWith acquires s (write when write) plus every set in others (read-only).
func (s *ShardedSyncSet[T]) lockWith(write bool, others ...*ShardedSyncSet[T]) []shardReq[T] {
	reqs := make([]shardReq[T], len(others)+1)
	reqs[0] = shardReq[T]{set: s, write: write}
	for i, other := range others {
		reqs[i+1] = shardReq[T]{set: other}
	}
	return lockShards(reqs)
}

// ---------------------------------------------------------------------------
// Writers
// ---------------------------------------------------------------------------

// Add inserts a single element. It reads the shard under the read lock first
// and only takes the write lock when the element is absent, so re-adding an
// element that is already there costs a read: the same shape xsync gives
// LoadOrStore, and the reason a contended Add of a present element is cheap.
func (s *ShardedSyncSet[T]) Add(v T) {
	sh := &s.shards[shardIndex(v)]
	sh.mu.RLock()
	present := sh.set.Contains(v)
	sh.mu.RUnlock()
	if present {
		return
	}
	sh.mu.Lock()
	defer sh.mu.Unlock()
	sh.addLocked(v)
}

// AddAll inserts every given element. A batch is spread over the shards it
// touches, one lock per shard per element: there is no cross-shard batching,
// because batching would mean buffering the whole argument first.
func (s *ShardedSyncSet[T]) AddAll(e ...T) {
	for _, v := range e {
		sh := &s.shards[shardIndex(v)]
		sh.mu.Lock()
		sh.addLocked(v)
		sh.mu.Unlock()
	}
}

// AddSeq inserts every element produced by it. The sequence and the code it
// calls run without any lock held: each element is inserted under its shard's
// lock, so the sequence may call methods of s.
func (s *ShardedSyncSet[T]) AddSeq(it iter.Seq[T]) {
	for v := range it {
		s.Add(v)
	}
}

// Extend adds every element of the given sets to s.
func (s *ShardedSyncSet[T]) Extend(sets ...*ShardedSyncSet[T]) {
	if len(sets) == 0 {
		return
	}
	locks := s.lockWith(true, sets...)
	defer unlockShards(locks)
	for i := range s.shards {
		others := shardSets(sets, i)
		s.shards[i].bulkLocked(func(set *Set[T]) { set.Extend(others...) })
	}
}

// Retain keeps, in place, the elements present in every set.
func (s *ShardedSyncSet[T]) Retain(sets ...*ShardedSyncSet[T]) {
	if len(sets) == 0 {
		return
	}
	locks := s.lockWith(true, sets...)
	defer unlockShards(locks)
	for i := range s.shards {
		others := shardSets(sets, i)
		s.shards[i].bulkLocked(func(set *Set[T]) { set.Retain(others...) })
	}
}

// Subtract deletes, in place, the elements of the given sets.
func (s *ShardedSyncSet[T]) Subtract(sets ...*ShardedSyncSet[T]) {
	if len(sets) == 0 {
		return
	}
	locks := s.lockWith(true, sets...)
	defer unlockShards(locks)
	for i := range s.shards {
		others := shardSets(sets, i)
		s.shards[i].bulkLocked(func(set *Set[T]) { set.Subtract(others...) })
	}
}

// Remove deletes the given elements.
func (s *ShardedSyncSet[T]) Remove(e ...T) {
	for _, v := range e {
		sh := &s.shards[shardIndex(v)]
		sh.mu.Lock()
		sh.removeLocked(v)
		sh.mu.Unlock()
	}
}

// Rehash rebuilds every shard's map sized for its current length.
func (s *ShardedSyncSet[T]) Rehash() {
	locks := s.lockSelf(true)
	defer unlockShards(locks)
	for i := range s.shards {
		s.shards[i].bulkLocked(func(set *Set[T]) { set.Rehash() })
	}
}

// ---------------------------------------------------------------------------
// Readers
// ---------------------------------------------------------------------------

// Len returns the number of elements. It is exact and takes no lock: every
// mutation keeps its shard's counter, so this is a sum of shardCount atomic
// loads. It is the one whole-set read that stays cheap.
func (s *ShardedSyncSet[T]) Len() int {
	n := int64(0)
	for i := range s.shards {
		n += s.shards[i].count.Load()
	}
	return int(n)
}

// Contains reports whether v is an element. It reads one shard under the read
// lock and touches no other.
func (s *ShardedSyncSet[T]) Contains(v T) bool {
	sh := &s.shards[shardIndex(v)]
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	return sh.set.Contains(v)
}

// GetAll returns the elements as a new slice, in shard order within the shard.
func (s *ShardedSyncSet[T]) GetAll() []T {
	locks := s.lockSelf(false)
	defer unlockShards(locks)
	out := make([]T, 0, s.Len())
	for i := range s.shards {
		// Range the shard rather than call GetAll on it: GetAll would allocate
		// one slice per shard and then be copied again into out.
		for v := range s.shards[i].set.IterAll() {
			out = append(out, v)
		}
	}
	return out
}

// IterAll returns the elements as an iterator. Every shard is held under its
// read lock for the whole loop, like SyncSet.IterAll and sync.Map.Range, so the
// body must not call any method of a ShardedSyncSet. Use IterSnapshot to
// iterate while touching the set.
func (s *ShardedSyncSet[T]) IterAll() iter.Seq[T] {
	return func(yield func(T) bool) {
		locks := s.lockSelf(false)
		defer unlockShards(locks)
		for i := range s.shards {
			for v := range s.shards[i].set.IterAll() {
				if !yield(v) {
					return
				}
			}
		}
	}
}

// IterSnapshot returns the elements as an iterator over a private clone, so the
// loop body runs without any lock and may call methods of s.
func (s *ShardedSyncSet[T]) IterSnapshot() iter.Seq[T] {
	locks := s.lockSelf(false)
	cloned := s.cloneLocked()
	unlockShards(locks)
	return cloned.IterAll()
}

// Clone copies the source's reserved capacity and over-allocation, shard by
// shard; use Copy for a compact copy.
func (s *ShardedSyncSet[T]) Clone() *ShardedSyncSet[T] {
	locks := s.lockSelf(false)
	defer unlockShards(locks)
	return s.cloneLocked()
}

// cloneLocked clones every shard. The caller must hold every shard's lock.
func (s *ShardedSyncSet[T]) cloneLocked() *ShardedSyncSet[T] {
	res := &ShardedSyncSet[T]{}
	for i := range s.shards {
		res.shards[i].set = *s.shards[i].set.Clone()
		res.shards[i].count.Store(int64(res.shards[i].set.Len()))
	}
	res.setID()
	return res
}

// Copy returns an independent, compact copy sized for exactly its length.
func (s *ShardedSyncSet[T]) Copy() *ShardedSyncSet[T] {
	locks := s.lockSelf(false)
	defer unlockShards(locks)
	res := &ShardedSyncSet[T]{}
	for i := range s.shards {
		res.shards[i].set = *s.shards[i].set.Copy()
		res.shards[i].count.Store(int64(res.shards[i].set.Len()))
	}
	res.setID()
	return res
}

// ---------------------------------------------------------------------------
// Relations. Each one decomposes shard by shard because a value's shard index
// depends only on the value, so v is in s exactly when it is in the shard of s
// that shardIndex(v) names: holding every shard of both operands under the read
// lock holds both sets still.
// ---------------------------------------------------------------------------

// IsSubset reports whether every element of s is an element of other.
func (s *ShardedSyncSet[T]) IsSubset(other *ShardedSyncSet[T]) bool {
	locks := s.lockWith(false, other)
	defer unlockShards(locks)
	for i := range s.shards {
		if !s.shards[i].set.IsSubset(&other.shards[i].set) {
			return false
		}
	}
	return true
}

// Disjoint reports whether s and other share no element.
func (s *ShardedSyncSet[T]) Disjoint(other *ShardedSyncSet[T]) bool {
	locks := s.lockWith(false, other)
	defer unlockShards(locks)
	for i := range s.shards {
		if !s.shards[i].set.Disjoint(&other.shards[i].set) {
			return false
		}
	}
	return true
}

// Equal reports whether s and other hold the same elements.
func (s *ShardedSyncSet[T]) Equal(other *ShardedSyncSet[T]) bool {
	locks := s.lockWith(false, other)
	defer unlockShards(locks)
	for i := range s.shards {
		if !s.shards[i].set.Equal(&other.shards[i].set) {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Derived sets
// ---------------------------------------------------------------------------

// ToSet merges every shard into a plain, lock-free Set, so the result can be
// handed to code that takes one. It is O(n) and it copies: the returned Set is
// independent, and further writes to s do not appear in it. This is the way out
// of the sharded type when a whole-set operation is needed without holding
// shard locks.
func (s *ShardedSyncSet[T]) ToSet() *Set[T] {
	locks := s.lockSelf(false)
	defer unlockShards(locks)
	res := New[T](s.Len())
	for i := range s.shards {
		res.Extend(&s.shards[i].set)
	}
	return res
}

// Difference returns s − other, shard by shard.
func (s *ShardedSyncSet[T]) Difference(other *ShardedSyncSet[T]) *ShardedSyncSet[T] {
	locks := s.lockWith(false, other)
	defer unlockShards(locks)
	res := &ShardedSyncSet[T]{}
	for i := range s.shards {
		res.shards[i].set = *s.shards[i].set.Difference(&other.shards[i].set)
		res.shards[i].count.Store(int64(res.shards[i].set.Len()))
	}
	res.setID()
	return res
}

// ShardedUnion returns the elements of every set.
func ShardedUnion[T comparable](sets ...*ShardedSyncSet[T]) *ShardedSyncSet[T] {
	res := NewShardedSync[T](0)
	res.Extend(sets...)
	return res
}

// ShardedIntersection returns the elements present in every set.
func ShardedIntersection[T comparable](sets ...*ShardedSyncSet[T]) *ShardedSyncSet[T] {
	if len(sets) == 0 {
		return NewShardedSync[T](0)
	}
	reqs := make([]shardReq[T], len(sets))
	for i, other := range sets {
		reqs[i] = shardReq[T]{set: other}
	}
	locks := lockShards(reqs)
	defer unlockShards(locks)

	res := &ShardedSyncSet[T]{}
	for i := range res.shards {
		perShard := make([]*Set[T], len(sets))
		for j, other := range sets {
			perShard[j] = &other.shards[i].set
		}
		res.shards[i].set = *Intersection(perShard...)
		res.shards[i].count.Store(int64(res.shards[i].set.Len()))
	}
	res.setID()
	return res
}

// shardSets returns the i-th shard of every set, to hand to the unsynchronised
// Set API. The caller must hold every one of their locks.
func shardSets[T comparable](sets []*ShardedSyncSet[T], i int) []*Set[T] {
	out := make([]*Set[T], len(sets))
	for j, other := range sets {
		out[j] = &other.shards[i].set
	}
	return out
}
