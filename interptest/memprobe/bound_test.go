//go:build !wasm

package memprobe

import (
	"os"
	"testing"
)

// Each closed interpreter leaves little live heap behind. Before the type
// caches were pruned and reservations sized to their method sets, these
// programs left 42 and 144 KiB per load; now about 4 and 15. The bounds
// leave room for noise and toolchain differences while catching either
// regression (pruning alone gives 32 and 108).
func TestRetainedHeapPerLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("loads 260 interpreters")
	}
	if os.Getenv("MVM_MEMPROBE") != "" {
		t.Skip("TestMemProbe measures instead")
	}
	for _, c := range []struct {
		w     *workload
		bound float64 // KiB per load
	}{
		{sixWorkload(), 16},
		{twentyWorkload(), 48},
	} {
		for _, mode := range []closeMode{modeClose, modeRelease} {
			r := probe(t, c.w, mode, 10, 120, "")
			if got := r.heapPerLoad(); got > c.bound {
				t.Errorf("%s (%s): %.1f KiB of live heap retained per load, want <= %.0f",
					c.w.name, mode, got, c.bound)
			} else {
				t.Logf("%s (%s): %.1f KiB per load", c.w.name, mode, got)
			}
		}
	}
}
