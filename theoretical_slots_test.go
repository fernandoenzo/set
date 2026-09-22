package set

import (
	"os"
	"testing"
	"unsafe"
)

// The reservation model is the foundation the whole package stands on:
// theoreticalSlots predicts what make(map, hint) allocates, and needsRehash and
// hintOversized decide with it whether a map must be rebuilt. If the runtime
// changes the layout of internal/runtime/maps — table size, load factor,
// minimum group — the shipped tests would keep passing while the set started
// rebuilding too often or too rarely, silently.
//
// So this file reads the real slot count out of a freshly made map and checks
// theoreticalSlots against it. It is the one place the package's model is tied
// to the implementation it models, and it is deliberately kept in a test file:
// set.go itself stays free of unsafe.
//
// The layout read here is Go 1.27's (internal/runtime/maps):
//
//	Map { used uint64; seed uintptr; dirPtr unsafe.Pointer; dirLen int; ... }
//	table { used uint16; capacity uint16; growthLeft uint16; ... }
//
// A map value is a pointer to that Map. dirLen is the length of the directory
// at dirPtr, an array of *table; directory entries may repeat a table under
// extendible hashing, so the slots are the sum over the distinct tables. A map
// that never outgrew a single group has dirLen == 0 and holds one group of
// abi.MapGroupSlots slots, which is where the 8 comes from.

// runtimeMap mirrors the leading fields of internal/runtime/maps.Map. Only the
// fields up to dirLen are read.
type runtimeMap struct {
	used   uint64
	seed   uintptr
	dirPtr unsafe.Pointer
	dirLen int
}

// slotCount returns the number of key/elem slots the map has allocated.
func slotCount(m map[int]struct{}) int {
	h := *(*unsafe.Pointer)(unsafe.Pointer(&m)) // the map value is a *Map
	rt := (*runtimeMap)(h)
	if rt.dirLen == 0 {
		return 8 // small map: one group
	}
	total, seen := 0, make(map[unsafe.Pointer]bool, rt.dirLen)
	entry := unsafe.Sizeof(uintptr(0))
	for i := range rt.dirLen {
		tab := *(*unsafe.Pointer)(unsafe.Add(rt.dirPtr, uintptr(i)*entry))
		if seen[tab] {
			continue
		}
		seen[tab] = true
		total += int(*(*uint16)(unsafe.Add(tab, 2))) // table.capacity
	}
	return total
}

// usable reports whether this test can read the layout at all. A 64-bit
// pointer makes the offsets above hold; elsewhere the shape differs and the
// test is skipped rather than reported as a failure of the model.
func usable(t *testing.T) {
	t.Helper()
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("the runtime map layout is only read on 64-bit platforms")
	}
}

// TestRuntimeLayoutReadable guards the test below against the runtime changing
// shape: those two hints are known to reserve a single group, so if they no
// longer read as 8 the reader — not the model — is what is out of date.
func TestRuntimeLayoutReadable(t *testing.T) {
	usable(t)
	for _, hint := range []int{1, 8} {
		if got := slotCount(make(map[int]struct{}, hint)); got != 8 {
			t.Fatalf("hint=%d reads %d slots, want 8: the runtime layout changed, so this reader (not theoreticalSlots) needs updating",
				hint, got)
		}
	}
}

// xcheckMaxHint bounds the default grid. The dense sweep covers the same range.
const xcheckMaxHint = 300_000

// xcheckGrid returns the hints worth checking by default: every point where the
// reservation can change — each step boundary, and every crack where the usable
// budget (7/8 of the slots) falls below the hint — each with its neighbours.
// Sizes in between cannot behave differently from their neighbour, so the whole
// range is covered in under a thousand hints instead of three hundred thousand.
func xcheckGrid() []int {
	seen := make(map[int]bool, 4096)
	grid := make([]int, 0, 4096)
	add := func(n int) {
		if n >= 1 && n <= xcheckMaxHint && !seen[n] {
			seen[n] = true
			grid = append(grid, n)
		}
	}
	for h := 1; h <= xcheckMaxHint; h++ {
		crack := theoreticalSlots(h)*7/8 < h
		boundary := h == 1 || h&(h-1) == 0
		if crack || boundary {
			add(h - 1)
			add(h)
			add(h + 1)
		}
	}
	return grid
}

// TestTheoreticalSlotsMatchesRuntime is the tie between the model and the
// runtime. It runs the structural grid always; the exhaustive sweep of every
// hint up to xcheckMaxHint is opt-in through SET_XCHECK_FULL, because it costs
// minutes while the grid costs milliseconds.
func TestTheoreticalSlotsMatchesRuntime(t *testing.T) {
	usable(t)

	if os.Getenv("SET_XCHECK_FULL") != "" {
		bad := 0
		for hint := 1; hint <= xcheckMaxHint; hint++ {
			if got, want := slotCount(make(map[int]struct{}, hint)), theoreticalSlots(hint); got != want {
				if bad < 20 {
					t.Errorf("hint=%d: runtime slots=%d, theoreticalSlots=%d", hint, got, want)
				}
				bad++
			}
		}
		if bad > 0 {
			t.Fatalf("%d/%d hints mismatch", bad, xcheckMaxHint)
		}
		return
	}

	grid := xcheckGrid()
	for _, hint := range grid {
		if got, want := slotCount(make(map[int]struct{}, hint)), theoreticalSlots(hint); got != want {
			t.Fatalf("hint=%d: runtime slots=%d, theoreticalSlots=%d", hint, got, want)
		}
	}
	t.Logf("checked %d hints against the runtime", len(grid))
}
