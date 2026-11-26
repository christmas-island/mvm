package stubs

import (
	"sync/atomic"
	"unsafe"
)

// HandlerS21 is the per-method callback for shape S21: func(*T) bool.
// Covers flag.boolFlag.IsBoolFlag.
type HandlerS21 = func(recv unsafe.Pointer) bool

type methodDescS21 struct{ handler HandlerS21 }

var (
	slotPoolS21  [poolSizeS21]methodDescS21
	nextSlotS21  atomic.Uint32
	freeSlotsS21 freeSlots
)

func acquireSlotS21(h HandlerS21, c *slotClass) (pc uintptr, release func(), err error) {
	n, ok := freeSlotsS21.take(c)
	if !ok {
		n = nextSlotS21.Add(1) - 1
		if n >= poolSizeS21 {
			return 0, nil, errPoolFmt("S21", poolSizeS21)
		}
	}
	slotPoolS21[n].handler = h
	return stubsS21[n], func() { slotPoolS21[n].handler = nil; freeSlotsS21.put(c, n) }, nil
}

// SlotsUsedS21 reports how many S21 stub slots have been consumed.
func SlotsUsedS21() uint32 { return nextSlotS21.Load() }

//go:nosplit
func dispatchS21(slot uint32, recv unsafe.Pointer) bool {
	if slot >= poolSizeS21 {
		return false
	}
	d := &slotPoolS21[slot]
	if d.handler == nil {
		return false
	}
	return d.handler(recv)
}
