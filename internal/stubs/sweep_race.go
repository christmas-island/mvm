//go:build race && !wasm

package stubs

// Under the race detector runtime/debug.WriteHeapDump faults (SIGSEGV at a
// near-nil address) as soon as an itab's type is one reflect or mvm built with
// a package path: dumping the type name takes the runtime's reflect-offsets
// lock, whose race annotation needs a race context the dumping g0 lacks
// (go1.26/go1.27, darwin/arm64). So race builds never sweep, and retired
// slots stay unused.
const raceBuild = true
