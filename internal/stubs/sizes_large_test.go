//go:build !wasm && mvm_largepools

package stubs

import "testing"

func TestLargeProfileSizes(t *testing.T) {
	for _, p := range HighWater() {
		floor := uint32(1024)
		if p.Name[0] == 'W' {
			floor = 256
		}
		if p.Cap < floor {
			t.Errorf("%s: cap %d below the profile's floor %d", p.Name, p.Cap, floor)
		}
	}
}
