//go:build wasm

package stubs

import "time"

// Wasm has no stub pools (see gen_pools.go): nothing ever retires.

func sweepRetired() {}

// Sweep is a no-op on wasm.
func Sweep() (int, error) { return 0, nil }

// SweepStats reports no sweeps on wasm.
func SweepStats() (sweeps uint32, slots uint64, total time.Duration, lastErr error) {
	return 0, 0, 0, nil
}
