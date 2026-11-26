//go:build !wasm

package stubs

import "reflect"

// acquireSlots is acquireSlotsClassed without the classes.
func acquireSlots(recv reflect.Type, methods []Method) (stubs []uintptr, releases []func(), err error) {
	stubs, releases, _, err = acquireSlotsClassed(recv, methods)
	return stubs, releases, err
}
