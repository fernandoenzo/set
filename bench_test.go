package set

import "testing"

// Benchmarks behind the Performance table in README.md. `go test -bench .`
// reports ns/op and allocs/op for the current machine.
//
// Elements are ints so that a benchmark measures the container and not the
// hashing of a heavier key.

const benchSet = 10_000

func benchSeq(n int) []int { return makeSeq(n) }

func BenchmarkAdd(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		var s Set[int]
		s.Add(1)
	}
}

func BenchmarkAddAll1(b *testing.B) {
	b.ReportAllocs()
	e := []int{1}
	for b.Loop() {
		var s Set[int]
		s.AddAll(e...)
	}
}

func BenchmarkAddAll1000(b *testing.B) {
	b.ReportAllocs()
	e := benchSeq(1000)
	for b.Loop() {
		var s Set[int]
		s.AddAll(e...)
	}
}

func BenchmarkContains(b *testing.B) {
	s := NewFromSlices(benchSeq(benchSet))
	b.ReportAllocs()
	for b.Loop() {
		s.Contains(benchSet / 2)
	}
}

func BenchmarkIsSubset(b *testing.B) {
	a := NewFromSlices(benchSeq(benchSet))
	c := NewFromSlices(benchSeq(benchSet * 2))
	b.ReportAllocs()
	for b.Loop() {
		a.IsSubset(c)
	}
}

func BenchmarkDisjoint(b *testing.B) {
	a := NewFromSlices(benchSeq(benchSet))
	c := NewFromSlices(benchSeq(benchSet * 2))
	b.ReportAllocs()
	for b.Loop() {
		a.Disjoint(c)
	}
}

func BenchmarkGetAll(b *testing.B) {
	s := NewFromSlices(benchSeq(benchSet))
	b.ReportAllocs()
	for b.Loop() {
		_ = s.GetAll()
	}
}

// The mutating benchmarks need their setup excluded from the timing, so they
// use the classic b.N form: b.Loop forbids StopTimer/StartTimer.

func BenchmarkRemove(b *testing.B) {
	e := benchSeq(benchSet / 2)
	b.ReportAllocs()
	for range b.N {
		s := NewFromSlices(benchSeq(benchSet))
		b.StopTimer()
		s.Remove(e...)
		b.StartTimer()
	}
}

func BenchmarkRehash(b *testing.B) {
	for range b.N {
		s := NewFromSlices(benchSeq(benchSet))
		b.StopTimer()
		s.Rehash()
		b.StartTimer()
	}
}

func BenchmarkExtend(b *testing.B) {
	a := NewFromSlices(benchSeq(benchSet))
	c := NewFromSlices(benchSeq(benchSet * 2))
	b.ReportAllocs()
	for range b.N {
		d := a.Clone()
		b.StopTimer()
		d.Extend(c)
		b.StartTimer()
	}
}

// Difference, cheap path: the subtraction cannot cross a step, so the receiver
// is copied and trimmed.
func BenchmarkDifferenceCheapPath(b *testing.B) {
	src := NewFromSlices(benchSeq(benchSet))
	other := NewFromSlices([]int{1})
	b.ReportAllocs()
	for b.Loop() {
		_ = src.Difference(other)
	}
}

// Difference, general path: the result may land a step lower, so the map is
// reserved for the receiver's length and compacted afterwards.
func BenchmarkDifferenceGeneralPath(b *testing.B) {
	src := NewFromSlices(benchSeq(benchSet))
	other := NewFromSlices(benchSeq(benchSet - 1))
	b.ReportAllocs()
	for b.Loop() {
		_ = src.Difference(other)
	}
}

func BenchmarkUnion(b *testing.B) {
	a := NewFromSlices(benchSeq(benchSet))
	c := NewFromSlices(benchSeq(benchSet * 2))
	b.ReportAllocs()
	for b.Loop() {
		_ = Union(a, c)
	}
}

func BenchmarkIntersection(b *testing.B) {
	a := NewFromSlices(benchSeq(benchSet))
	c := NewFromSlices(benchSeq(benchSet * 2))
	b.ReportAllocs()
	for b.Loop() {
		_ = Intersection(a, c)
	}
}
