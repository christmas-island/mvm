package stubs

import (
	"fmt"
	"sync/atomic"
	"unsafe"
)

// HandlerS19 is the per-method callback for shape S19:
// func(*T, fmt.ScanState, rune) error. Covers fmt.Scanner.Scan.
type HandlerS19 = func(recv unsafe.Pointer, st fmt.ScanState, verb rune) error

type methodDescS19 struct{ handler HandlerS19 }

var (
	slotPoolS19  [poolSizeS19]methodDescS19
	nextSlotS19  atomic.Uint32
	freeSlotsS19 freeSlots
)

func acquireSlotS19(h HandlerS19, c *slotClass) (pc uintptr, release func(), err error) {
	n, ok := freeSlotsS19.take(c)
	if !ok {
		n = nextSlotS19.Add(1) - 1
		if n >= poolSizeS19 {
			return 0, nil, errPoolFmt("S19", poolSizeS19)
		}
	}
	slotPoolS19[n].handler = h
	return stubsS19[n], func() { slotPoolS19[n].handler = nil; freeSlotsS19.put(c, n) }, nil
}

// SlotsUsedS19 reports how many S19 stub slots have been consumed.
func SlotsUsedS19() uint32 { return nextSlotS19.Load() }

//go:nosplit
func dispatchS19(slot uint32, recv unsafe.Pointer, st fmt.ScanState, verb rune) error {
	if slot >= poolSizeS19 {
		return nil
	}
	d := &slotPoolS19[slot]
	if d.handler == nil {
		return nil
	}
	return d.handler(recv, st, verb)
}
