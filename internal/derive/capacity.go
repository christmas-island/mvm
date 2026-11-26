package derive

import (
	"reflect"

	"github.com/mvm-sh/mvm/internal/runtype"
	"github.com/mvm-sh/mvm/mtype"
)

// MethodCapacity bounds how many methods the synth attach can install on t's
// value or pointer rtype, so its reservation is allocated with that many
// method slots instead of runtype.MaxMethods. A reserved table cannot grow
// without changing the rtype's identity, so the bound must hold for every
// later fill: it counts every method t declares (both receivers, resolved or
// not) plus every method each embedded field could promote, walking embeds
// and Base chains, and falls back to runtype.MaxMethods where a method set is
// not yet knowable (a generic instance, an interface embed with no method
// list yet).
//
// It relies on what reserving already relies on: a type's methods are
// registered (Compiler.preregisterMethods) before its rtype is materialized,
// so the set of declared methods is known at reserve time. Promoted methods
// are added to t.Methods later; they are covered by the embed walk.
func MethodCapacity(t *mtype.Type) int {
	n, ok := methodBound(t, 0)
	if !ok || n > runtype.MaxMethods {
		return runtype.MaxMethods
	}
	return n
}

// methodBound counts the methods t and the types it embeds may contribute to
// t's method sets; ok is false when one of them is unknowable.
func methodBound(t *mtype.Type, depth int) (n int, ok bool) {
	if t == nil || depth >= maxEmbedWalkDepth || IsGenericInstanceName(t.Name) {
		return 0, false
	}
	for i := 0; t != nil && i < canonicalTypeMaxDepth; i, t = i+1, t.Base {
		for k := range t.Methods {
			if t.Methods[k].IsResolved() || t.Methods[k].Sig != nil {
				n++
			}
		}
		for _, emb := range t.Embedded {
			k, ok := embedBound(emb.Type, depth+1)
			if !ok {
				return 0, false
			}
			n += k
		}
		if IsGenericInstanceName(t.Name) {
			return 0, false
		}
	}
	return n, true
}

// embedBound counts the methods an embedded field of type e may promote.
func embedBound(e *mtype.Type, depth int) (int, bool) {
	if e != nil && e.IsPtr() {
		e = e.ElemType
	}
	if e == nil {
		return 0, false
	}
	if e.IsInterface() {
		n := len(e.IfaceMethods)
		if e.Rtype != nil && e.Rtype.Kind() == reflect.Interface {
			n = max(n, e.Rtype.NumMethod())
		} else if n == 0 {
			return 0, false // method set not yet known
		}
		return n, true
	}
	n, ok := methodBound(e, depth)
	if !ok {
		return 0, false
	}
	// A native type's methods are on its rtype, not in e.Methods. (An
	// interpreted type has a nil Rtype until materialized, or a synth one;
	// its methods are the ones counted above.)
	if rt := e.Rtype; rt != nil && !runtype.IsSynth(rt) {
		if rt.Kind() != reflect.Pointer {
			rt = reflect.PointerTo(rt)
		}
		n += rt.NumMethod()
	}
	return n, true
}
