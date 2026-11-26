//go:build !unix && !wasm

package stubs

func dumpHeadViaSocket() (head []byte, ok bool) { return nil, false }
