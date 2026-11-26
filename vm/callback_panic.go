package vm

import (
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
)

// Panics in callbacks on goroutines mvm did not start.
//
// Native code calls interpreted code through a func value (buildCallFunc), a
// method expression (buildMethodExprFunc) or a synthesized method (the stub
// handlers, via callMethod). A panic in that code comes back as an error and
// is raised again as a Go panic, so the native caller and whatever is beneath
// it see Go's semantics: an interpreted recover further down catches it, and
// so does net/http's handler recovery or the host's own recover.
//
// On a goroutine with nothing beneath that could recover the panic, raising
// it would end the process: a timer's (time.AfterFunc, context.AfterFunc),
// the finalizer and cleanup goroutines, a goroutine a native library starts
// (sync.WaitGroup.Go, an exec copier writing to an interpreted io.Writer).
// There raiseCallback records the panic on the interpreter instead and the
// callback returns zero results: CallbackPanic reports it, the handler set
// with SetCallbackPanicHandler receives it, and, as for a panic on an
// interpreted goroutine, a run in progress ends with exit status 2.
//
// "Nothing beneath that could recover" is decided from the goroutine's stack
// when the panic is raised: no mvm frame (a Run of some machine, or the base
// of a native goroutine an interpreted go statement started), no frame of the
// host program (any function outside the standard library and mvm itself),
// and none of the standard-library frames listed in recoveringFrames, which
// recover a callback's panic and go on or hand it to another goroutine.

// CallbackPanicError is a panic in interpreted code that native code called
// on a goroutine where nothing would have recovered it.
type CallbackPanicError struct {
	Err    error  // the panic: a *PanicError (value, position, interpreted stack) or the run's error
	Caller string // the native function that started the goroutine, e.g. "time.goFunc"
}

func (e *CallbackPanicError) Error() string {
	return fmt.Sprintf("panic in callback (goroutine started by %s): %v", e.Caller, e.Err)
}

func (e *CallbackPanicError) Unwrap() error { return e.Err }

// callbackPanics is the per-interpreter record, reached through lifetime.
type callbackPanics struct {
	mu      sync.Mutex
	first   error
	count   int
	handler func(error)
}

func (c *callbackPanics) record(err error) {
	c.mu.Lock()
	if c.first == nil {
		c.first = err
	}
	c.count++
	h := c.handler
	c.mu.Unlock()
	if h != nil {
		h(err)
	}
}

func (m *Machine) callbackPanics() *callbackPanics {
	if m.life == nil {
		return nil
	}
	return m.life.cb
}

// CallbackPanics returns how many panics were recorded in callbacks that
// native code ran on a goroutine where nothing would recover them, and the
// first one (a *CallbackPanicError); 0 and nil if none.
func (m *Machine) CallbackPanics() (n int, first error) {
	c := m.callbackPanics()
	if c == nil {
		return 0, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count, c.first
}

// SetCallbackPanicHandler sets a function called, on the goroutine of the
// callback, with every panic CallbackPanics counts. It must not block.
func (m *Machine) SetCallbackPanicHandler(h func(error)) {
	if c := m.callbackPanics(); c != nil {
		c.mu.Lock()
		c.handler = h
		c.mu.Unlock()
	}
}

// raiseCallback raises err, returned by an interpreted callback, as the panic
// its native caller expects, unless nothing on this goroutine could recover
// it: then it records err and returns, and the callback returns zero results.
func raiseCallback(life *lifetime, fault *goroutineFault, err error) {
	if life != nil && life.cb != nil && !onMvmGoroutine() {
		if caller, ok := unrecoverable(); ok {
			cpe := &CallbackPanicError{Err: err, Caller: caller}
			life.cb.record(cpe)
			if fault != nil {
				fault.record(cpe)
			}
			return
		}
	}
	if pe, ok := errors.AsType[*PanicError](err); ok {
		panic(reraisedPanic{pe}) // raw value for native recover; see reraisedPanic
	}
	panic(err)
}

func (m *Machine) raiseCallback(err error)      { raiseCallback(m.life, m.fault, err) }
func (rs *runnerState) raiseCallback(err error) { raiseCallback(rs.life, rs.fault, err) }

// onMvmGoroutine reports whether the calling goroutine has an mvm frame
// beneath the caller: an active Run of some machine, or the base of a native
// goroutine started by an interpreted go statement.
func onMvmGoroutine() bool {
	if ActiveMachine() != nil {
		return true
	}
	_, ok := mvmBases.Load(gid())
	return ok
}

// mvmBases marks the native goroutines started by interpreted go statements.
var mvmBases sync.Map // uintptr (g pointer) -> struct{}

// recoveringFrames are standard-library functions that recover a panic in
// code they call and go on, or hand it to another goroutine: a panic beneath
// them does not end the process. Matched as a prefix of the frame's function
// name with type arguments removed.
var recoveringFrames = []string{
	"net/http.(*conn).serve",                 // logs a handler's panic, closes the connection
	"net/http.(*http2serverConn).runHandler", // the same for HTTP/2
	"net/http.(*timeoutHandler).ServeHTTP",   // re-raises it on the handler's goroutine
	"iter.Pull.",                             // re-raises it in next or stop
	"iter.Pull2.",
	"testing.tRunner", // reports it as the test's failure, then ends the binary as go test does
}

// unrecoverable reports whether a panic raised by the caller's caller would
// reach the top of the goroutine: no host frame and no recovering frame
// beneath it. It also returns the function that started the goroutine (or,
// if unknown, its outermost frame).
func unrecoverable() (caller string, ok bool) {
	pcs := make([]uintptr, 256)
	n := runtime.Callers(3, pcs)
	if n == len(pcs) {
		return "", false // too deep to see the base; assume it is handled
	}
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		name := stripTypeArgs(f.Function)
		switch {
		case name == "" || name == "runtime.goexit":
		case isHostFunc(name):
			return "", false
		default:
			for _, p := range recoveringFrames {
				if strings.HasPrefix(name, p) {
					return "", false
				}
			}
			caller = name
		}
		if !more {
			break
		}
	}
	if c := goroutineCreator(); c != "" {
		caller = c
	}
	return caller, true
}

// goroutineCreator returns the function whose go statement started the
// calling goroutine, from its traceback's "created by" line; "" if none.
func goroutineCreator() string {
	buf := make([]byte, 16<<10)
	for {
		n := runtime.Stack(buf, false)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		if len(buf) >= 1<<20 {
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	s := string(buf)
	i := strings.LastIndex(s, "\ncreated by ")
	if i < 0 {
		return ""
	}
	line, _, _ := strings.Cut(s[i+len("\ncreated by "):], "\n")
	line, _, _ = strings.Cut(line, " in goroutine")
	return stripTypeArgs(strings.TrimSpace(line))
}

// vmPkg is this package's import path; its parent is mvm's module root.
var vmPkg = reflect.TypeFor[Machine]().PkgPath()

// mvmInternal lists the mvm packages whose frames belong to the interpreter
// itself rather than to a program embedding it.
var mvmInternal = func() []string {
	root := strings.TrimSuffix(vmPkg, "/vm")
	return []string{vmPkg, root + "/internal", root + "/stdlib", root + "/interp"}
}()

// isHostFunc reports whether a frame's function belongs to the program that
// embeds mvm: outside the standard library and outside mvm's own packages.
func isHostFunc(name string) bool {
	pkg := funcPkg(name)
	if pkg == "main" {
		return true
	}
	first, _, _ := strings.Cut(pkg, "/")
	if !strings.Contains(first, ".") {
		return false // standard library
	}
	for _, p := range mvmInternal {
		if pkg == p || strings.HasPrefix(pkg, p+"/") {
			return false
		}
	}
	return true
}

// funcPkg returns the import path of a function name such as
// "github.com/a/b.(*T).M.func1".
func funcPkg(name string) string {
	slash := strings.LastIndex(name, "/")
	if dot := strings.Index(name[slash+1:], "."); dot >= 0 {
		return name[:slash+1+dot]
	}
	return name
}

// stripTypeArgs removes the bracketed type arguments of a generic function
// name ("iter.Pull[...].func1" -> "iter.Pull.func1").
func stripTypeArgs(name string) string {
	if !strings.Contains(name, "[") {
		return name
	}
	var b strings.Builder
	depth := 0
	for _, r := range name {
		switch {
		case r == '[':
			depth++
		case r == ']':
			depth--
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// nativeGoPanic is a panic in a native function an interpreted go statement
// started (typically raised by a callback it ran), recorded as that
// goroutine's fault instead of ending the process.
func nativeGoPanic(r any) error {
	if pe, ok := asPanicError(r); ok {
		return pe
	}
	if e, ok := r.(error); ok {
		return fmt.Errorf("panic in native goroutine: %w\n%s", e, debug.Stack())
	}
	return fmt.Errorf("panic in native goroutine: %v\n%s", r, debug.Stack())
}

// zeroResults returns the zero value of each result of ft.
func zeroResults(ft reflect.Type) []reflect.Value {
	out := make([]reflect.Value, ft.NumOut())
	for i := range out {
		out[i] = reflect.Zero(ft.Out(i))
	}
	return out
}
