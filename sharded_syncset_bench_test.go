package set

import (
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"
)

// Benchmarks behind the README's Sharding table: what the shard count buys on
// the reads, what the fast path buys on a repeated Add, and what every shard
// lock costs on the operations that must span the whole set.
//
// `go test -bench Shard` runs them.

const shardBenchSet = 10_000

func shardBenchSeq(n int) []int { return makeSeq(n) }

// nowNanos is the wall clock, used only by the scaling table below.
func nowNanos() int64 { return time.Now().UnixNano() }

// BenchmarkShardContains: one shard read, against SyncSet's single lock word.
func BenchmarkShardContains(b *testing.B) {
	a := NewShardedSyncFromSlices(shardBenchSeq(shardBenchSet))
	s := NewSyncFromSlices(shardBenchSeq(shardBenchSet))
	b.ReportAllocs()
	b.Run("ShardedSyncSet", func(b *testing.B) {
		for b.Loop() {
			a.Contains(shardBenchSet / 2)
		}
	})
	b.Run("SyncSet", func(b *testing.B) {
		for b.Loop() {
			s.Contains(shardBenchSet / 2)
		}
	})
}

// BenchmarkShardContainsParallel is the row the type exists for: SyncSet's
// readers bounce one cache line between cores, these bounce 64.
func BenchmarkShardContainsParallel(b *testing.B) {
	a := NewShardedSyncFromSlices(shardBenchSeq(shardBenchSet))
	s := NewSyncFromSlices(shardBenchSeq(shardBenchSet))
	b.ReportAllocs()
	b.SetParallelism(1)
	b.ResetTimer()
	b.Run("ShardedSyncSet", func(b *testing.B) {
		b.SetParallelism(1)
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				a.Contains(i % shardBenchSet)
				i++
			}
		})
	})
	b.Run("SyncSet", func(b *testing.B) {
		b.SetParallelism(1)
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				s.Contains(i % shardBenchSet)
				i++
			}
		})
	})
}

// BenchmarkShardAddPresent: the fast path. Re-adding a value that is already
// there must cost a read, not a write lock.
func BenchmarkShardAddPresent(b *testing.B) {
	a := NewShardedSyncFromSlices(shardBenchSeq(shardBenchSet))
	s := NewSyncFromSlices(shardBenchSeq(shardBenchSet))
	v := shardBenchSet / 2
	b.ReportAllocs()
	b.Run("ShardedSyncSet", func(b *testing.B) {
		for b.Loop() {
			a.Add(v)
		}
	})
	b.Run("SyncSet", func(b *testing.B) {
		for b.Loop() {
			s.Add(v)
		}
	})
}

func BenchmarkShardAddPresentParallel(b *testing.B) {
	a := NewShardedSyncFromSlices(shardBenchSeq(shardBenchSet))
	s := NewSyncFromSlices(shardBenchSeq(shardBenchSet))
	v := shardBenchSet / 2
	b.ReportAllocs()
	b.Run("ShardedSyncSet", func(b *testing.B) {
		b.SetParallelism(1)
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				a.Add(v)
			}
		})
	})
	b.Run("SyncSet", func(b *testing.B) {
		b.SetParallelism(1)
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				s.Add(v)
			}
		})
	})
}

// BenchmarkShardAddAbsent: a real insert, every element new. The shard count
// must not make this worse than the single-lock version.
func BenchmarkShardAddAbsent(b *testing.B) {
	b.Run("ShardedSyncSet", func(b *testing.B) {
		var next int
		a := NewShardedSyncFromSlices(shardBenchSeq(100))
		b.ReportAllocs()
		for b.Loop() {
			a.Add(shardBenchSet + next)
			next++
		}
	})
	b.Run("SyncSet", func(b *testing.B) {
		var next int
		s := NewSyncFromSlices(shardBenchSeq(100))
		b.ReportAllocs()
		for b.Loop() {
			s.Add(shardBenchSet + next)
			next++
		}
	})
}

// BenchmarkShardIsSubset: a read that must hold every shard of both sets, so it
// pays the shard count in lock acquisitions.
func BenchmarkShardIsSubset(b *testing.B) {
	a := NewShardedSyncFromSlices(shardBenchSeq(shardBenchSet))
	c := NewShardedSyncFromSlices(shardBenchSeq(shardBenchSet * 2))
	s := NewSyncFromSlices(shardBenchSeq(shardBenchSet))
	t := NewSyncFromSlices(shardBenchSeq(shardBenchSet * 2))
	b.ReportAllocs()
	b.Run("ShardedSyncSet", func(b *testing.B) {
		for b.Loop() {
			a.IsSubset(c)
		}
	})
	b.Run("SyncSet", func(b *testing.B) {
		for b.Loop() {
			s.IsSubset(t)
		}
	})
}

// BenchmarkShardIterSnapshot: the escape hatch for whole-set operations.
func BenchmarkShardIterSnapshot(b *testing.B) {
	a := NewShardedSyncFromSlices(shardBenchSeq(shardBenchSet))
	s := NewSyncFromSlices(shardBenchSeq(shardBenchSet))
	b.ReportAllocs()
	b.Run("ShardedSyncSet", func(b *testing.B) {
		for b.Loop() {
			for range a.IterSnapshot() {
			}
		}
	})
	b.Run("SyncSet", func(b *testing.B) {
		for b.Loop() {
			for range s.IterSnapshot() {
			}
		}
	})
}

// BenchmarkShardToSet measures the bridge back to the plain type.
func BenchmarkShardToSet(b *testing.B) {
	a := NewShardedSyncFromSlices(shardBenchSeq(shardBenchSet))
	b.ReportAllocs()
	for b.Loop() {
		_ = a.ToSet()
	}
}

// TestShardScalingTable is not a benchmark: it prints the scaling that the
// README's Sharding section quotes, so the numbers in the prose have a single
// reproducible source. Run with:
//
//	go test -run TestShardScalingTable -v
func TestShardScalingTable(t *testing.T) {
	if testing.Short() {
		t.Skip("scaling table skipped in short mode")
	}
	a := NewShardedSyncFromSlices(shardBenchSeq(shardBenchSet))
	s := NewSyncFromSlices(shardBenchSeq(shardBenchSet))

	measure := func(g int, fn func(int) bool) float64 {
		const iters = 2_000_000
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(g)
		for k := range g {
			go func() {
				defer wg.Done()
				<-start
				for i := range iters {
					fn(k + i)
				}
			}()
		}
		startT := nowNanos()
		close(start)
		wg.Wait()
		return float64(nowNanos()-startT) / float64(g*iters)
	}

	t.Logf("%-16s %10s %12s %10s", "variant", "1 goroutine", "32 goroutines", "scaling")
	for _, c := range []struct {
		name string
		fn   func(int) bool
	}{
		{"ShardedSyncSet", func(i int) bool { return a.Contains(i % shardBenchSet) }},
		{"SyncSet", func(i int) bool { return s.Contains(i % shardBenchSet) }},
	} {
		one := measure(1, c.fn)
		many := measure(32, c.fn)
		t.Logf("%-16s %9.2f ns %9.2f ns %9.2fx", c.name, one, many, one/many)
	}
	t.Logf("GOMAXPROCS=%d shardCount=%d", runtime.GOMAXPROCS(0), shardCount)
}

// BenchmarkShardMultiSetSmall is the worst case of the multi-set path: a set
// small enough that acquiring every one of the shardCount locks costs far more
// than the comparison itself. It is the number the README's caveat quotes.
func BenchmarkShardMultiSetSmall(b *testing.B) {
	for _, n := range []int{10, 100, 10_000} {
		a := NewShardedSyncFromSlices(shardBenchSeq(n))
		c := NewShardedSyncFromSlices(shardBenchSeq(n * 2))
		s := NewSyncFromSlices(shardBenchSeq(n))
		t := NewSyncFromSlices(shardBenchSeq(n * 2))
		b.Run("ShardedSyncSet", func(b *testing.B) {
			b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					a.IsSubset(c)
				}
			})
		})
		b.Run("SyncSet", func(b *testing.B) {
			b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					s.IsSubset(t)
				}
			})
		})
	}
}

// BenchmarkShardLenAndGetAll prices the whole-set readers, which must take
// every shard's lock and are therefore not the cheap calls of the type.
func BenchmarkShardLenAndGetAll(b *testing.B) {
	a := NewShardedSyncFromSlices(shardBenchSeq(shardBenchSet))
	s := NewSyncFromSlices(shardBenchSeq(shardBenchSet))
	b.Run("Len", func(b *testing.B) {
		b.Run("ShardedSyncSet", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = a.Len()
			}
		})
		b.Run("SyncSet", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = s.Len()
			}
		})
	})
	b.Run("GetAll", func(b *testing.B) {
		b.Run("ShardedSyncSet", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = a.GetAll()
			}
		})
		b.Run("SyncSet", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = s.GetAll()
			}
		})
	})
}
