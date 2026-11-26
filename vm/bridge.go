package vm

import (
	"reflect"
	"sync"

	"github.com/mvm-sh/mvm/internal/runtype"
)

// NativeMethodHook intercepts a native method call. The returned values
// are the replacement results. Whether a hook is installed for the
// (rtype, name) pair is reported separately by hasNativeMethodHook.
type NativeMethodHook = func(m *Machine, recv reflect.Value, args []reflect.Value) []reflect.Value

type hookKey struct {
	rt   reflect.Type
	name string
}

var (
	nativeMethodHooksMu sync.RWMutex
	nativeMethodHooks   = map[hookKey]NativeMethodHook{}
	hookedMethodNames   = map[string]bool{} // names with a hook on any type
)

// RegisterNativeMethodHook installs hook for recvInstance's named method.
// recvInstance is a typed nil (e.g. (*testing.T)(nil)); the function keys
// on reflect.TypeOf(recvInstance).
func RegisterNativeMethodHook(recvInstance any, methodName string, hook NativeMethodHook) {
	rt := reflect.TypeOf(recvInstance)
	if rt == nil {
		return
	}
	nativeMethodHooksMu.Lock()
	nativeMethodHooks[hookKey{rt, methodName}] = hook
	hookedMethodNames[methodName] = true
	nativeMethodHooksMu.Unlock()
}

// hookTarget returns the receiver a hook for the native method name of rv
// applies to: rv's concrete value when its type has one, or else the
// embedded field the method is promoted from (shallowest first, as Go
// resolves it), addressed when the hook is on the pointer type.
func hookTarget(rv reflect.Value, name string) (reflect.Value, bool) {
	if rv.Kind() == reflect.Interface && !rv.IsNil() {
		rv = rv.Elem()
	}
	if !rv.IsValid() {
		return reflect.Value{}, false
	}
	nativeMethodHooksMu.RLock()
	defer nativeMethodHooksMu.RUnlock()
	if _, ok := nativeMethodHooks[hookKey{rv.Type(), name}]; ok {
		return rv, true
	}
	if rv.Kind() != reflect.Pointer && rv.CanAddr() {
		if _, ok := nativeMethodHooks[hookKey{reflect.PointerTo(rv.Type()), name}]; ok {
			return rv.Addr(), true
		}
	}
	if !hookedMethodNames[name] {
		return reflect.Value{}, false
	}
	has := func(t reflect.Type) bool { _, ok := nativeMethodHooks[hookKey{t, name}]; return ok }
	level := []reflect.Value{rv}
	for depth := 0; depth < 8 && len(level) > 0; depth++ {
		var next []reflect.Value
		for _, v := range level {
			if v.Kind() == reflect.Pointer {
				if v.IsNil() {
					continue
				}
				v = v.Elem()
			}
			if v.Kind() != reflect.Struct {
				continue
			}
			for i := range v.NumField() {
				if !v.Type().Field(i).Anonymous {
					continue
				}
				fv := runtype.Exportable(v.Field(i))
				ft := fv.Type()
				switch {
				case ft.Kind() == reflect.Pointer && has(ft) && !fv.IsNil():
					return fv, true
				case ft.Kind() != reflect.Pointer && fv.CanAddr() && has(reflect.PointerTo(ft)):
					return fv.Addr(), true
				case ft.Kind() != reflect.Pointer && has(ft):
					return fv, true
				}
				next = append(next, fv)
			}
		}
		level = next
	}
	return reflect.Value{}, false
}

func hasNativeMethodHook(rt reflect.Type, name string) bool {
	nativeMethodHooksMu.RLock()
	defer nativeMethodHooksMu.RUnlock()
	_, ok := nativeMethodHooks[hookKey{rt, name}]
	return ok
}

func lookupNativeMethodHook(rt reflect.Type, name string) NativeMethodHook {
	nativeMethodHooksMu.RLock()
	defer nativeMethodHooksMu.RUnlock()
	return nativeMethodHooks[hookKey{rt, name}]
}

// hookFunc turns the boundHookCall IfaceCall leaves for a hooked method into
// a native func that runs the hook, for the deferred and go calls, which take
// a func value instead of going through the Call opcode.
func (m *Machine) hookFunc(v Value) (Value, bool) {
	rv := v.ref
	if rv.Kind() == reflect.Interface && !rv.IsNil() {
		rv = rv.Elem()
	}
	if !rv.IsValid() || rv.Type() != boundHookCallRtype {
		return v, false
	}
	bhc := rv.Interface().(boundHookCall)
	hook := lookupNativeMethodHook(bhc.RecvType, bhc.Method)
	if hook == nil {
		return Value{ref: bhc.Fn}, true
	}
	ft := bhc.Fn.Type()
	return Value{ref: reflect.MakeFunc(ft, func(in []reflect.Value) []reflect.Value {
		out := hook(m, bhc.Recv, in)
		if len(out) != ft.NumOut() {
			return zeroResults(ft)
		}
		return out
	})}, true
}

// MethodValueShim intercepts native method-value resolution on a sentinel type
// (e.g. *runtime.Func): it returns a replacement bound method, or an invalid
// Value to decline. Unlike NativeMethodHook it yields the method value (for
// reflect / method-expression use), not the call result.
type MethodValueShim = func(rv reflect.Value, name string) reflect.Value

var (
	methodValueShimsMu sync.RWMutex
	methodValueShims   = map[reflect.Type]MethodValueShim{}
)

// RegisterMethodValueShim installs shim for the exact receiver rtype rt.
func RegisterMethodValueShim(rt reflect.Type, shim MethodValueShim) {
	if rt == nil {
		return
	}
	methodValueShimsMu.Lock()
	methodValueShims[rt] = shim
	methodValueShimsMu.Unlock()
}

// isShimmedNativeType forces rt's methods onto the slow path so tryMethodValueShim runs.
func isShimmedNativeType(rt reflect.Type) bool {
	methodValueShimsMu.RLock()
	defer methodValueShimsMu.RUnlock()
	_, ok := methodValueShims[rt]
	return ok
}

func tryMethodValueShim(rv reflect.Value, name string) reflect.Value {
	if !rv.IsValid() {
		return reflect.Value{}
	}
	methodValueShimsMu.RLock()
	shim := methodValueShims[rv.Type()]
	methodValueShimsMu.RUnlock()
	if shim == nil {
		return reflect.Value{}
	}
	return shim(rv, name)
}
