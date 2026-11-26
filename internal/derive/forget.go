package derive

import "github.com/mvm-sh/mvm/mtype"

// Owner records the *mtype.Type keys one interpreter adds to the
// process-global caches below (reservations, derived types, synth
// interfaces), so they can be dropped with it (Forget). Guarded by derivedMu.
type Owner struct{ keys map[*mtype.Type]bool }

// NewOwner returns an empty Owner.
func NewOwner() *Owner { return &Owner{keys: map[*mtype.Type]bool{}} }

// ActiveOwner returns the Owner of the interpreter compiling or running on
// the calling goroutine, or nil. The vm sets it.
var ActiveOwner = func() *Owner { return nil }

// own records t for the active Owner. Caller holds derivedMu.
func own(t *mtype.Type) {
	if o := ActiveOwner(); o != nil {
		o.keys[t] = true
	}
}

// ForgetOwner is Forget over the keys o recorded and types.
func ForgetOwner(o *Owner, types map[*mtype.Type]bool) {
	all := make(map[*mtype.Type]bool, len(types))
	for t := range types {
		all[t] = true
	}
	if o != nil {
		derivedMu.Lock()
		for t := range o.keys {
			all[t] = true
		}
		clear(o.keys)
		derivedMu.Unlock()
	}
	Forget(all)
}

// Forget drops the process-global cache entries keyed by the given types:
// their reservations, derived (*T, []T, [n]T, chan T, map[T]V) types and
// synth-interface rtypes, and those of the derived types in turn. The types
// must belong to one interpreter that is gone (closed, and no goroutine of
// it running): a later lookup for them would build a new identity. The
// rtypes themselves stay valid (the runtime keeps synth rtypes for good);
// only mvm's compile-time graph behind them becomes collectable.
func Forget(types map[*mtype.Type]bool) {
	derivedMu.Lock()
	defer derivedMu.Unlock()
	stack := make([]*mtype.Type, 0, len(types))
	for t := range types {
		stack = append(stack, t)
	}
	seen := map[*mtype.Type]bool{}
	for len(stack) > 0 {
		t := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if t == nil || seen[t] {
			continue
		}
		seen[t] = true
		if d := derivedCache[t]; d != nil {
			stack = append(stack, d.ptr, d.slice)
			for _, n := range d.array {
				stack = append(stack, n)
			}
			for _, n := range d.chans {
				stack = append(stack, n)
			}
			for e, n := range d.maps {
				stack = append(stack, e, n)
			}
			delete(derivedCache, t)
		}
		delete(reservations, t)
		delete(synthIfaceCache, t)
	}
}

// CacheSizes reports the entry counts of the *mtype.Type-keyed caches (for
// tests and memory probes).
func CacheSizes() (reservationCount, derivedCount, synthIfaceCount int) {
	derivedMu.Lock()
	defer derivedMu.Unlock()
	return len(reservations), len(derivedCache), len(synthIfaceCache)
}
