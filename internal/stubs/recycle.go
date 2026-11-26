package stubs

import (
	"errors"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// A slot's stub PC stays baked into its rtype's method table forever, so a
// released slot (Interp.Close) may only be handed to a method that a stale
// call through that PC can safely reach: the same method name on a receiver
// and signature with the same name and deep memory layout. This is what
// reloading the same package in a new interpreter attaches; anything else
// takes a fresh slot.
//
// Interp.CloseAndRelease retires its slots instead: their method tables are
// repointed to the shape's trap at once, and a sweep (sweep.go) repoints every
// itab still holding their PCs before any other method may take them.

// slotClass describes the method a slot serves; key is computed lazily.
type slotClass struct {
	recv    reflect.Type
	name    string
	pkgPath string
	sig     reflect.Type

	once sync.Once
	key  string

	pool   *freeSlots // the pool the slot came from, set by take
	retire bool       // release retires the slot instead of parking it
}

func newSlotClass(recv reflect.Type, m Method) *slotClass {
	return &slotClass{recv: recv, name: m.Name, pkgPath: m.PkgPath, sig: m.Sig}
}

func (c *slotClass) getKey() string {
	if c == nil || c.recv == nil || c.sig == nil {
		return ""
	}
	c.once.Do(func() {
		w := layoutWriter{named: map[string]string{}, seen: map[reflect.Type]bool{}}
		recv, sig := w.shallow(c.recv), w.shallow(c.sig)
		for len(w.queue) > 0 {
			t := w.queue[len(w.queue)-1]
			w.queue = w.queue[:len(w.queue)-1]
			name := typeName(t)
			d := w.shallowNamed(t)
			if prev, ok := w.named[name]; ok && prev != d {
				return // one name, two layouts: not identifiable by name, never recycle
			}
			w.named[name] = d
		}
		names := make([]string, 0, len(w.named))
		for name := range w.named {
			names = append(names, name)
		}
		sort.Strings(names)
		var b strings.Builder
		b.WriteString(c.pkgPath + "." + c.name + "|" + recv + "|" + sig)
		for _, name := range names {
			b.WriteString("|" + name + "=" + w.named[name])
		}
		c.key = b.String()
	})
	return c.key
}

// layoutWriter describes a type graph by name: a named type is written as a
// reference to its name and its own layout is recorded once in named. An
// interpreter may hold several rtypes of one named type, so identity cannot
// be used; a name reached with two different layouts makes the key empty.
type layoutWriter struct {
	named map[string]string
	seen  map[reflect.Type]bool
	queue []reflect.Type
	path  []reflect.Type // unnamed types being written, outermost first
}

func typeName(t reflect.Type) string { return t.PkgPath() + "." + t.String() }

// shallow describes t, referencing named types by name.
func (w *layoutWriter) shallow(t reflect.Type) string {
	if t.Name() != "" {
		if !w.seen[t] {
			w.seen[t] = true
			w.queue = append(w.queue, t)
		}
		return "=" + typeName(t)
	}
	// An unnamed type can recur (cycle-breaking rtypes): refer to the enclosing
	// occurrence by its distance up the path, which is structural, not identity.
	for i, p := range slices.Backward(w.path) {
		if p == t {
			return "^" + strconv.Itoa(len(w.path)-1-i)
		}
	}
	w.path = append(w.path, t)
	var b strings.Builder
	w.write(&b, t)
	w.path = w.path[:len(w.path)-1]
	return b.String()
}

// shallowNamed describes named type t's own layout.
func (w *layoutWriter) shallowNamed(t reflect.Type) string {
	w.path = append(w.path[:0], t)
	var b strings.Builder
	w.write(&b, t)
	w.path = w.path[:0]
	return b.String()
}

// write describes everything a value's memory layout and method dispatch
// depend on: kind, size, alignment, field names and offsets, element types,
// func signatures and interface method sets.
func (w *layoutWriter) write(b *strings.Builder, t reflect.Type) {
	b.WriteString(t.Kind().String())
	b.WriteByte(':')
	b.WriteString(strconv.FormatUint(uint64(t.Size()), 10))
	b.WriteByte(':')
	b.WriteString(strconv.Itoa(t.Align()))
	b.WriteByte('(')
	switch t.Kind() {
	case reflect.Array:
		b.WriteString(strconv.Itoa(t.Len()) + "," + w.shallow(t.Elem()))
	case reflect.Pointer, reflect.Slice:
		b.WriteString(w.shallow(t.Elem()))
	case reflect.Chan:
		b.WriteString(t.ChanDir().String() + "," + w.shallow(t.Elem()))
	case reflect.Map:
		b.WriteString(w.shallow(t.Key()) + "," + w.shallow(t.Elem()))
	case reflect.Struct:
		for f := range t.Fields() {
			b.WriteString(f.PkgPath + "." + f.Name + "@" + strconv.FormatUint(uint64(f.Offset), 10))
			if f.Anonymous {
				b.WriteByte('^')
			}
			b.WriteString(":" + w.shallow(f.Type) + ";")
		}
	case reflect.Func:
		if t.IsVariadic() {
			b.WriteString("...")
		}
		for in := range t.Ins() {
			b.WriteString(w.shallow(in) + ",")
		}
		b.WriteString("->")
		for out := range t.Outs() {
			b.WriteString(w.shallow(out) + ",")
		}
	case reflect.Interface:
		for m := range t.Methods() {
			b.WriteString(m.PkgPath + "." + m.Name + ":" + w.shallow(m.Type) + ";")
		}
	}
	b.WriteByte(')')
}

// freeSlots holds a pool's released slot indices: parked by slotClass key, or
// retired (released by Interp.CloseAndRelease: the closed interpreter's method
// tables already point at the trap, its itabs may not yet) and swept (no
// method table or itab reaches the slot any more, so any method may take it).
type freeSlots struct {
	n     atomic.Int32 // parked slot count, for a lock-free empty check
	nfree atomic.Int32 // swept slot count, likewise
	mu    sync.Mutex
	m     map[string][]uint32

	retired []uint32 // guarded by mu
	swept   []uint32 // guarded by mu

	// Set once at init by the generated pool file (setPool).
	name  string
	next  *atomic.Uint32
	cap   uint32
	stubs []uintptr
	trap  uintptr
}

// setPool records the pool behind f, so sweep can map its slots to stub PCs
// and take can tell when the pool's fresh slots have run out.
func (f *freeSlots) setPool(name string, next *atomic.Uint32, size uint32, stubs []uintptr, trap uintptr) {
	f.name, f.next, f.cap, f.stubs, f.trap = name, next, size, stubs, trap
	registerPool(f)
}

func (f *freeSlots) take(c *slotClass) (uint32, bool) {
	if c != nil {
		c.pool = f
	}
	if n, ok := f.takeParked(c); ok {
		return n, true
	}
	if n, ok := f.takeSwept(); ok {
		return n, true
	}
	// Fresh slots first; once they are gone, sweep what retired interpreters
	// left so it can serve this method. Sweep even if this pool has nothing
	// retired: a sweep running on another goroutine may hold its slots, and
	// Sweep waits for it.
	if f.next == nil || f.next.Load() < f.cap {
		return 0, false
	}
	sweepRetired()
	return f.takeSwept()
}

func (f *freeSlots) takeParked(c *slotClass) (uint32, bool) {
	if f.n.Load() == 0 {
		return 0, false
	}
	key := c.getKey()
	if key == "" {
		return 0, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.m[key]
	if len(s) == 0 {
		return 0, false
	}
	n := s[len(s)-1]
	if len(s) == 1 {
		delete(f.m, key)
	} else {
		f.m[key] = s[:len(s)-1]
	}
	f.n.Add(-1)
	return n, true
}

func (f *freeSlots) takeSwept() (uint32, bool) {
	if f.nfree.Load() == 0 {
		return 0, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.swept) == 0 {
		return 0, false
	}
	n := f.swept[len(f.swept)-1]
	f.swept = f.swept[:len(f.swept)-1]
	f.nfree.Add(-1)
	return n, true
}

// put parks slot n for reuse by c's class; a slot with no class is retired
// for good. A class marked retire (CloseAndRelease) retires n for the sweep.
func (f *freeSlots) put(c *slotClass, n uint32) {
	if c != nil && c.retire {
		f.mu.Lock()
		f.retired = append(f.retired, n)
		f.mu.Unlock()
		return
	}
	key := c.getKey()
	if key == "" {
		return
	}
	f.mu.Lock()
	if f.m == nil {
		f.m = map[string][]uint32{}
	}
	f.m[key] = append(f.m[key], n)
	f.mu.Unlock()
	f.n.Add(1)
}

// staleCallError is the panic value of a shape's trap (see sweep.go).
type staleCallError struct{ shape string }

func (e *staleCallError) Error() string {
	return "stubs: method (shape " + e.shape + ") called on a value of an interpreter closed with CloseAndRelease"
}

func (e *staleCallError) Is(target error) bool { return target == ErrStaleCall }

// ErrStaleCall is matched (errors.Is) by the panic value of a method call on a
// value whose interpreter was closed with CloseAndRelease.
var ErrStaleCall = errors.New("stubs: method called on a value of a released interpreter")

func staleCall(shape string) error { return &staleCallError{shape} }

var (
	poolsMu sync.Mutex
	pools   []*freeSlots
)

func registerPool(f *freeSlots) {
	poolsMu.Lock()
	pools = append(pools, f)
	poolsMu.Unlock()
}

func registeredPools() []*freeSlots {
	poolsMu.Lock()
	defer poolsMu.Unlock()
	return append([]*freeSlots(nil), pools...)
}
