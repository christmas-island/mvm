package stubs

import (
	"sync/atomic"
	"unsafe"
)

// HandlerS12 is the per-method callback for shape S12: func(*T) any.
// Covers heap.Interface.Pop.
type HandlerS12 = func(recv unsafe.Pointer) any

type methodDescS12 struct{ handler HandlerS12 }

var (
	slotPoolS12  [poolSizeS12]methodDescS12
	nextSlotS12  atomic.Uint32
	freeSlotsS12 freeSlots
)

func acquireSlotS12(h HandlerS12, c *slotClass) (pc uintptr, release func(), err error) {
	n, ok := freeSlotsS12.take(c)
	if !ok {
		n = nextSlotS12.Add(1) - 1
		if n >= poolSizeS12 {
			return 0, nil, errPoolFmt("S12", poolSizeS12)
		}
	}
	slotPoolS12[n].handler = h
	return stubsS12[n], func() { slotPoolS12[n].handler = nil; freeSlotsS12.put(c, n) }, nil
}

// SlotsUsedS12 reports how many S12 stub slots have been consumed.
func SlotsUsedS12() uint32 { return nextSlotS12.Load() }

//go:nosplit
func dispatchS12(slot uint32, recv unsafe.Pointer) any {
	if slot >= poolSizeS12 {
		return nil
	}
	d := &slotPoolS12[slot]
	if d.handler == nil {
		return nil
	}
	return d.handler(recv)
}
