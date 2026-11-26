package vm

import (
	"reflect"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// Interruptible sync primitives.
//
// sync.Mutex.Lock, sync.RWMutex.Lock/RLock and sync.WaitGroup.Wait block in
// native code, which an interrupt cannot reach. Called from interpreted code,
// they are replaced by a native method hook that tries the operation without
// blocking and, while it cannot succeed, waits for a state change, the
// interrupt, or a short timeout, and tries again. The unlocking methods and
// WaitGroup.Add/Done called from interpreted code wake the waiters of the
// interpreter at once; the timeout covers a state change made by native code.
//
// The trade-off: a contended Lock is no longer queued FIFO by the runtime's
// mutex (no starvation mode), and a waiter woken by a native unlock or Done
// may see it up to syncPollMax late. sync.Cond.Wait is not hooked: it parks
// in native code until signalled.

// syncPollMax bounds how long a waiter sleeps between two tries when no
// interpreted unlock wakes it.
const syncPollMax = time.Millisecond

// wgStateOff is the offset of sync.WaitGroup's state word (counter in the
// high 32 bits), or -1 when the layout is not the expected one, in which case
// WaitGroup.Wait is left native and uninterruptible.
var wgStateOff = func() uintptr {
	f, ok := reflect.TypeFor[sync.WaitGroup]().FieldByName("state")
	if !ok || f.Type != reflect.TypeFor[atomic.Uint64]() {
		return ^uintptr(0)
	}
	return f.Offset
}()

func init() {
	// lock hooks a blocking method: try is the non-blocking form, block the
	// native one, used for a receiver that is not addressable or a Machine
	// made without NewMachine.
	lock := func(try, block func(unsafe.Pointer) bool) NativeMethodHook {
		return func(m *Machine, recv reflect.Value, _ []reflect.Value) []reflect.Value {
			p, c := syncRecvPtr(recv), m.control()
			switch {
			case p == nil || c == nil:
				block(p) // a nil receiver panics as natively
			default:
				c.waitUntil(func() bool { return try(p) })
			}
			return nil
		}
	}
	// wake hooks a method that may unblock a waiter: it runs it, then wakes
	// the interpreter's waiters.
	wake := func(f func(p unsafe.Pointer, args []reflect.Value)) NativeMethodHook {
		return func(m *Machine, recv reflect.Value, args []reflect.Value) []reflect.Value {
			f(syncRecvPtr(recv), args) // a nil receiver panics as natively
			if c := m.control(); c != nil {
				c.kick()
			}
			return nil
		}
	}
	mu := func(p unsafe.Pointer) *sync.Mutex { return (*sync.Mutex)(p) }
	rw := func(p unsafe.Pointer) *sync.RWMutex { return (*sync.RWMutex)(p) }
	wg := func(p unsafe.Pointer) *sync.WaitGroup { return (*sync.WaitGroup)(p) }
	for _, rt := range []any{(*sync.Mutex)(nil), sync.Mutex{}} {
		RegisterNativeMethodHook(rt, "Lock", lock(
			func(p unsafe.Pointer) bool { return mu(p).TryLock() },
			func(p unsafe.Pointer) bool { mu(p).Lock(); return true }))
		RegisterNativeMethodHook(rt, "Unlock", wake(func(p unsafe.Pointer, _ []reflect.Value) { mu(p).Unlock() }))
	}
	for _, rt := range []any{(*sync.RWMutex)(nil), sync.RWMutex{}} {
		RegisterNativeMethodHook(rt, "Lock", lock(
			func(p unsafe.Pointer) bool { return rw(p).TryLock() },
			func(p unsafe.Pointer) bool { rw(p).Lock(); return true }))
		RegisterNativeMethodHook(rt, "RLock", lock(
			func(p unsafe.Pointer) bool { return rw(p).TryRLock() },
			func(p unsafe.Pointer) bool { rw(p).RLock(); return true }))
		RegisterNativeMethodHook(rt, "Unlock", wake(func(p unsafe.Pointer, _ []reflect.Value) { rw(p).Unlock() }))
		RegisterNativeMethodHook(rt, "RUnlock", wake(func(p unsafe.Pointer, _ []reflect.Value) { rw(p).RUnlock() }))
	}
	if wgStateOff == ^uintptr(0) {
		return
	}
	for _, rt := range []any{(*sync.WaitGroup)(nil), sync.WaitGroup{}} {
		RegisterNativeMethodHook(rt, "Wait", lock(
			func(p unsafe.Pointer) bool {
				if (*atomic.Uint64)(unsafe.Add(p, wgStateOff)).Load()>>32 != 0 {
					return false
				}
				// The counter is zero, so the native Wait returns at once; it
				// also gives the race detector its edge from Done.
				wg(p).Wait()
				return true
			},
			func(p unsafe.Pointer) bool { wg(p).Wait(); return true }))
		RegisterNativeMethodHook(rt, "Done", wake(func(p unsafe.Pointer, _ []reflect.Value) { wg(p).Done() }))
		RegisterNativeMethodHook(rt, "Add", wake(func(p unsafe.Pointer, args []reflect.Value) { wg(p).Add(int(args[0].Int())) }))
	}
}

// syncRecvPtr returns the address of a sync value receiver, or nil.
func syncRecvPtr(recv reflect.Value) unsafe.Pointer {
	if recv.Kind() == reflect.Interface && !recv.IsNil() {
		recv = recv.Elem()
	}
	if recv.Kind() != reflect.Pointer {
		if !recv.CanAddr() {
			return nil
		}
		recv = recv.Addr()
	}
	if recv.IsNil() {
		return nil
	}
	return recv.UnsafePointer()
}

// waitUntil returns once try succeeds, polling it on every kick and at least
// every syncPollMax, and panics with ErrInterrupted on an interrupt.
func (c *control) waitUntil(try func() bool) {
	if try() {
		return
	}
	c.waiters.Add(1)
	defer c.waiters.Add(-1)
	var t *time.Timer
	defer func() {
		if t != nil {
			t.Stop()
		}
	}()
	d := 10 * time.Microsecond
	for {
		c.wakeMu.Lock()
		if c.wake == nil {
			c.wake = make(chan struct{})
		}
		wake := c.wake
		c.wakeMu.Unlock()
		if try() {
			return
		}
		if c.flag.Load() {
			panic(ErrInterrupted)
		}
		if t == nil {
			t = time.NewTimer(d)
		} else {
			t.Reset(d)
		}
		select {
		case <-wake:
		case <-t.C:
			d = min(2*d, syncPollMax)
		case <-c.stop:
			panic(ErrInterrupted)
		}
	}
}

// kick wakes every goroutine of the interpreter waiting in waitUntil.
func (c *control) kick() {
	if c.waiters.Load() == 0 {
		return
	}
	c.wakeMu.Lock()
	if c.wake != nil {
		close(c.wake)
		c.wake = nil
	}
	c.wakeMu.Unlock()
}
