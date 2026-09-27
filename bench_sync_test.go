package set

import (
	"sync"
	"testing"
)

// Benchmarks behind the concurrency rows of README.md's Performance table:
// what the internal lock costs on the hot reads, and what the lock order costs
// on the multi-set operations. `go test -bench Sync` runs them.
//
// The plain Set benchmarks live in bench_test.go and measure the container
// itself; these measure the wrapper, so a change to lockAll or to a read path
// shows up as a number rather than as a claim.

func BenchmarkSyncContains(b *testing.B) {
	s := NewSyncFromSlices(benchSeq(benchSet))
	b.ReportAllocs()
	for b.Loop() {
		s.Contains(benchSet / 2)
	}
}

func BenchmarkSyncAdd(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		var s SyncSet[int]
		s.Add(1)
	}
}

// A multi-set read: the lock order sorts and collapses two requests per call.
func BenchmarkSyncIsSubset(b *testing.B) {
	a := NewSyncFromSlices(benchSeq(benchSet))
	c := NewSyncFromSlices(benchSeq(benchSet * 2))
	b.ReportAllocs()
	for b.Loop() {
		a.IsSubset(c)
	}
}

// Aliased operands: the requests collapse to one lock, so the cost is the
// single-set path plus the sort of two equal identities.
func BenchmarkSyncIsSubsetSelf(b *testing.B) {
	a := NewSyncFromSlices(benchSeq(benchSet))
	b.ReportAllocs()
	for b.Loop() {
		a.IsSubset(a)
	}
}

func BenchmarkSyncDifference(b *testing.B) {
	a := NewSyncFromSlices(benchSeq(benchSet))
	c := NewSyncFromSlices(benchSeq(benchSet / 2))
	b.ReportAllocs()
	for b.Loop() {
		a.Difference(c)
	}
}

func BenchmarkSyncUnion(b *testing.B) {
	a := NewSyncFromSlices(benchSeq(benchSet))
	c := NewSyncFromSlices(benchSeq(benchSet / 2))
	b.ReportAllocs()
	for b.Loop() {
		SyncUnion(a, c)
	}
}

// Parallel reads: the wrapper serialises nothing among readers, so this is the
// row that shows whether the internal RWMutex still scales.
func BenchmarkSyncContainsParallel(b *testing.B) {
	s := NewSyncFromSlices(benchSeq(benchSet))
	b.ReportAllocs()
	b.SetParallelism(32)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			s.Contains(benchSet / 2)
		}
	})
}

// The plain counterpart of BenchmarkSyncContainsParallel: the pair is what
// makes the parallel row of README.md's concurrency table reproducible.
func BenchmarkContainsParallel(b *testing.B) {
	s := NewFromSlices(benchSeq(benchSet))
	b.ReportAllocs()
	b.SetParallelism(32)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			s.Contains(benchSet / 2)
		}
	})
}

// The caller-held pattern of README.md's "Concurrency" section, measured with
// the same harness as the two rows above so the three are comparable.
type guardedSet struct {
	mu sync.RWMutex
	s  *Set[int]
}

func (g *guardedSet) Contains(v int) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.s.Contains(v)
}

func BenchmarkGuardedContains(b *testing.B) {
	g := &guardedSet{s: NewFromSlices(benchSeq(benchSet))}
	b.ReportAllocs()
	for b.Loop() {
		g.Contains(benchSet / 2)
	}
}

func BenchmarkGuardedContainsParallel(b *testing.B) {
	g := &guardedSet{s: NewFromSlices(benchSeq(benchSet))}
	b.ReportAllocs()
	b.SetParallelism(32)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			g.Contains(benchSet / 2)
		}
	})
}
