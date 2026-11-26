package vm

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
)

// Interrupting a running interpreter.
//
// Interrupt sets a flag shared by every machine of an interpreter (the root,
// the pooled runners native callbacks borrow, and the machines of interpreted
// goroutines) and closes a channel. The dispatch loop polls the flag at every
// backward jump and every call, so a loop or a recursion stops within one
// iteration or one call. A goroutine blocked in an interpreted channel
// operation, a select, time.Sleep, or a sync.Mutex, sync.RWMutex or
// sync.WaitGroup method called from interpreted code wakes on the channel.
//
// A stopped goroutine ends at once: it returns ErrInterrupted from Run and
// runs none of its pending deferred calls, so neither recover nor a deferred
// function can keep it alive. An interrupt passing through native code (a
// callback from sort.Sort, json.Marshal, ...) is a Go panic that native
// recovers may catch (fmt does); the flag is sticky, so the goroutine stops
// again at its next poll once it is back in interpreted code.
//
// What cannot be stopped is a goroutine parked in native code: a blocking
// read, net/http's accept loop, sync.Cond.Wait. It sees the flag only when
// that call returns. Running reports how many of the interpreter's goroutines
// are still running, and WaitStopped waits for none to be left. Only then is it
// safe to close the interpreter with a releasing close.
//
// An interrupt is final: the interpreter cannot run again.

// ErrInterrupted is returned by Run, Eval and CallFunc when the interpreter
// was interrupted.
var ErrInterrupted = errors.New("vm: interrupted")

// StillRunningError reports goroutines of an interpreter that did not stop
// before WaitStopped's context ended, most likely because they are parked in
// native code.
type StillRunningError struct {
	Goroutines int   // goroutines still running when the wait ended
	Err        error // the context's error
}

func (e *StillRunningError) Error() string {
	return fmt.Sprintf("vm: %d goroutine(s) still running: %v", e.Goroutines, e.Err)
}

func (e *StillRunningError) Unwrap() error { return e.Err }

// control is the per-interpreter stop state, reached from every machine
// through lifetime. Goroutines and callbacks hold it directly, never the
// lifetime, so they do not delay AfterCollected.
type control struct {
	flag     atomic.Bool
	stop     chan struct{} // closed by interrupt
	stopOnce sync.Once

	mu   sync.Mutex
	live int           // goroutines executing for the interpreter (see enter)
	idle chan struct{} // closed when live drops to zero; nil when nobody waits

	// wake is closed by kick when a hooked sync primitive changes state, to
	// wake goroutines polling one in an interruptible wait.
	wakeMu  sync.Mutex
	wake    chan struct{}
	waiters atomic.Int32
}

func newControl() *control { return &control{stop: make(chan struct{})} }

// noInterrupt is the flag of a Machine made without NewMachine; it is never set.
var noInterrupt atomic.Bool

func (m *Machine) control() *control {
	if m.life == nil {
		return nil
	}
	return m.life.ctl
}

// interruptFlag returns the flag the dispatch loop polls; never nil.
func (m *Machine) interruptFlag() *atomic.Bool {
	if c := m.control(); c != nil {
		return &c.flag
	}
	return &noInterrupt
}

// stopChan returns the channel closed by Interrupt, or nil (blocks forever in
// a select) for a Machine made without NewMachine.
func (m *Machine) stopChan() chan struct{} {
	if c := m.control(); c != nil {
		return c.stop
	}
	return nil
}

func (c *control) interrupt() {
	c.flag.Store(true)
	c.stopOnce.Do(func() { close(c.stop) })
}

// enter counts one more goroutine executing for the interpreter: the host
// goroutine running the root machine, an interpreted goroutine, a native
// goroutine the program started with a go statement, or a goroutine the
// interpreter did not start while it runs an interpreted callback.
func (c *control) enter() {
	c.mu.Lock()
	c.live++
	c.mu.Unlock()
}

func (c *control) exit() {
	c.mu.Lock()
	c.live--
	if c.live == 0 && c.idle != nil {
		close(c.idle)
		c.idle = nil
	}
	c.mu.Unlock()
}

func (c *control) running() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.live
}

func (c *control) waitStopped(ctx context.Context) error {
	for {
		c.mu.Lock()
		if c.live == 0 {
			c.mu.Unlock()
			return nil
		}
		if c.idle == nil {
			c.idle = make(chan struct{})
		}
		idle := c.idle
		c.mu.Unlock()
		select {
		case <-idle:
		case <-ctx.Done():
			if n := c.running(); n > 0 {
				return &StillRunningError{Goroutines: n, Err: ctx.Err()}
			}
			return nil
		}
	}
}

// Interrupt stops every goroutine of m's interpreter, now or at its next
// poll: running code stops at its next backward jump or call, and a goroutine
// blocked in an interpreted channel operation, select, time.Sleep or a hooked
// sync primitive wakes and stops. Each returns ErrInterrupted. Safe to call from
// any goroutine, more than once, and before, during or after a run. The
// interpreter cannot run again afterwards.
func (m *Machine) Interrupt() {
	if c := m.control(); c != nil {
		c.interrupt()
	}
}

// Interrupted reports whether Interrupt was called.
func (m *Machine) Interrupted() bool { return m.interruptFlag().Load() }

// Running returns how many goroutines are executing for m's interpreter: the
// goroutine running the root machine (Eval, Run, CallFunc), every interpreted
// goroutine, every native goroutine the program started with a go statement,
// and every other goroutine while it runs an interpreted callback. Timers and
// finalizers that have not fired are not goroutines and are not counted;
// after Interrupt such a callback returns at once without running interpreted
// code.
func (m *Machine) Running() int {
	if c := m.control(); c != nil {
		return c.running()
	}
	return 0
}

// WaitStopped waits until no goroutine is executing for m's interpreter (see
// Running), or ctx ends. It returns nil when none is left, and otherwise a
// *StillRunningError: those goroutines are parked in native code, and closing
// the interpreter with a releasing close is not safe while they are.
func (m *Machine) WaitStopped(ctx context.Context) error {
	if c := m.control(); c != nil {
		return c.waitStopped(ctx)
	}
	return nil
}

// Stop is Interrupt followed by WaitStopped.
func (m *Machine) Stop(ctx context.Context) error {
	m.Interrupt()
	return m.WaitStopped(ctx)
}

// isInterrupt reports whether a recovered value or an error is the interrupt.
func isInterrupt(r any) bool {
	e, ok := r.(error)
	return ok && errors.Is(e, ErrInterrupted)
}

// onMvmGoroutine reports whether the calling goroutine has an mvm frame
// beneath the caller: an active Run of some machine, or the base of a native
// goroutine started by an interpreted go statement. A panic raised there is
// caught by mvm; anywhere else it may end the process.
func onMvmGoroutine() bool {
	if ActiveMachine() != nil {
		return true
	}
	_, ok := mvmBases.Load(gid())
	return ok
}

// mvmBases marks the native goroutines started by interpreted go statements.
var mvmBases sync.Map // uintptr (g pointer) -> struct{}

// callbackEntry is taken by every entry from native code into interpreted
// code (func values, method expressions, synth methods). On a goroutine
// without an mvm frame beneath it, the callback is counted as running for
// the interpreter while it runs.
type callbackEntry struct {
	ctl     *control
	foreign bool
}

func (rs *runnerState) ctl() *control {
	if rs.life == nil {
		return nil
	}
	return rs.life.ctl
}

func (c *control) enterCallback() callbackEntry {
	if c == nil || onMvmGoroutine() {
		return callbackEntry{ctl: c}
	}
	c.enter()
	return callbackEntry{ctl: c, foreign: true}
}

func (e callbackEntry) exit() {
	if e.foreign {
		e.ctl.exit()
	}
}

// interrupted handles a callback that returned err. On a goroutine with no
// mvm frame beneath it an interrupt must not become a panic, which nothing
// would catch: the callback returns zero results instead. It reports whether
// it handled err.
func (e callbackEntry) interrupted(err error, ft reflect.Type) ([]reflect.Value, bool) {
	if !e.foreign || !errors.Is(err, ErrInterrupted) {
		return nil, false
	}
	return zeroResults(ft), true
}

func zeroResults(ft reflect.Type) []reflect.Value {
	out := make([]reflect.Value, ft.NumOut())
	for i := range out {
		out[i] = reflect.Zero(ft.Out(i))
	}
	return out
}

// Sleep is time.Sleep for interpreted code: Interrupt wakes it.
func (m *Machine) Sleep(d time.Duration) {
	stop := m.stopChan()
	if stop == nil {
		time.Sleep(d)
		return
	}
	if m.Interrupted() {
		panic(ErrInterrupted)
	}
	if d <= 0 {
		time.Sleep(d)
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-stop:
		panic(ErrInterrupted)
	}
}

// stopIters calls the stop function of the range iterators above base, so
// the coroutines of loops an interrupt left do not stay parked.
func (m *Machine) stopIters(base int) {
	for len(m.iterStack) > base {
		func() {
			defer func() { _ = recover() }()
			m.popIterStop()
		}()
	}
}
