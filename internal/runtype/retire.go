package runtype

import (
	"sync/atomic"
	"unsafe"
)

// RepointMethods rewrites every Ifn/Tfn entry of r's method table whose code
// PC is a key of repl to the mapped PC, so method calls through the rtype (and
// itabs the runtime builds from it later) reach the replacement. Used to point
// a closed interpreter's methods at a stale-call trap before its stub slots
// serve other code. Entries are stored atomically; a concurrent reader sees the
// old or the new offset.
func (r *Reservation) RepointMethods(repl map[uintptr]uintptr) int {
	if r == nil || r.u == nil || len(repl) == 0 {
		return 0
	}
	at := rtypePtr(r.rt)
	n := 0
	for i := range int(r.u.Mcount) {
		m := &r.m[i]
		for _, off := range []*uint32{&m.Ifn, &m.Tfn} {
			cur := atomic.LoadUint32(off)
			if cur == unreachableTextOff {
				continue
			}
			pc := uintptr(resolveTextOff(unsafe.Pointer(at), int32(cur)))
			if to, ok := repl[pc]; ok {
				atomic.StoreUint32(off, textOffForPC(to))
				n++
			}
		}
	}
	return n
}

// abiItab mirrors internal/abi.ITab. Fun is really [len(Inter.Methods)]; a zero
// Fun[0] marks a negative cache entry (the type does not implement Inter).
type abiItab struct {
	Inter *abiInterfaceType
	Type  *abiType
	Hash  uint32
	Fun   [1]uintptr
}

func itabFuns(tab unsafe.Pointer) []uintptr {
	t := (*abiItab)(tab)
	if t == nil || t.Inter == nil {
		return nil
	}
	n := len(t.Inter.Methods)
	if n == 0 || t.Fun[0] == 0 {
		return nil
	}
	return unsafe.Slice(&t.Fun[0], n)
}

// ItabFuns returns a copy of the method entry PCs of the runtime itab at tab
// (nil for a negative cache entry).
func ItabFuns(tab unsafe.Pointer) []uintptr {
	return append([]uintptr(nil), itabFuns(tab)...)
}

// RepointItab rewrites each method entry of the runtime itab at tab whose PC is
// a key of repl to the mapped PC, atomically per entry, and reports how many it
// rewrote.
func RepointItab(tab unsafe.Pointer, repl map[uintptr]uintptr) int {
	n := 0
	for i, pc := range itabFuns(tab) {
		if to, ok := repl[pc]; ok {
			atomic.StoreUintptr(&itabFuns(tab)[i], to)
			n++
		}
	}
	return n
}
