package vm

import (
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
)

// ErrGoroutineFault aborts a Run that was waiting on a channel when a goroutine
// panicked elsewhere; recoverPanic returns it and the top-level Eval maps it to a
// non-zero exit.
var ErrGoroutineFault = errors.New("vm: unrecovered goroutine panic")

// goroutineFault carries the first unrecovered panic from a spawned goroutine,
// shared by the root machine and every goroutine child.
type goroutineFault struct {
	mu       sync.Mutex
	err      error         // first recorded fault, nil until one occurs
	abort    chan struct{} // closed when err is first set, to wake channel waits
	cont     bool          // policy: record+log only, don't propagate (mvm test)
	spawned  atomic.Bool   // a goroutine has been started (arms channel-wait aborts)
	out      io.Writer     // where record surfaces the panic
	onRecord func()        // closes the interpreter's halt channel
}

func newGoroutineFault(out io.Writer, cont bool) *goroutineFault {
	return &goroutineFault{abort: make(chan struct{}), cont: cont, out: out}
}

// EnableGoroutineFaults arms goroutine-panic capture on the root machine before
// it runs, so runner machines and goroutine children share the sink. Idempotent.
func (m *Machine) EnableGoroutineFaults() {
	m.isRoot = true
	if m.fault == nil {
		m.fault = newGoroutineFault(m.err, m.faultContinue)
		m.linkFault()
	}
}

func (g *goroutineFault) record(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err != nil {
		return
	}
	g.err = err
	_, _ = fmt.Fprintf(g.out, "\n-- panic in goroutine --\n%v\n", err)
	close(g.abort)
	if g.onRecord != nil {
		g.onRecord()
	}
}

func (g *goroutineFault) pending() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.err
}

// SetGoroutineFaultContinue picks the policy for a later goroutine panic: false
// (default) propagates it as a non-zero exit; true records+logs only and lets
// execution continue (mvm test keeps running the suite). Call before the run.
func (m *Machine) SetGoroutineFaultContinue(v bool) {
	m.faultContinue = v
	if m.fault != nil {
		m.fault.cont = v
	}
}

// PendingGoroutineFault reports a recorded goroutine panic that should propagate
// (nil when none, or under the continue policy).
func (m *Machine) PendingGoroutineFault() error {
	if m.fault == nil || m.fault.cont {
		return nil
	}
	return m.fault.pending()
}

// GoroutineFault reports the first recorded goroutine panic regardless of policy.
// Under the continue policy the suite still runs to the end; the driver calls
// this afterward to fail the run.
func (m *Machine) GoroutineFault() error {
	if m.fault == nil {
		return nil
	}
	return m.fault.pending()
}

// chanRecv receives from ch for the ChanRecv opcode. A receive that would
// block also waits for the interpreter's halt channel, closed by an
// interrupt or a goroutine fault (interrupt.go), so it cannot outlive
// either.
func (m *Machine) chanRecv(ch reflect.Value) (reflect.Value, bool) {
	c := m.control()
	if c == nil {
		if !m.watchFault() {
			return ch.Recv()
		}
		chosen, v, ok := reflect.Select([]reflect.SelectCase{
			{Dir: reflect.SelectRecv, Chan: ch},
			{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(m.fault.abort)},
		})
		if chosen == 1 {
			panic(ErrGoroutineFault)
		}
		return v, ok
	}
	if v, ok := ch.TryRecv(); v.IsValid() {
		return v, ok
	}
	cases := []reflect.SelectCase{{Dir: reflect.SelectRecv, Chan: ch}, {Dir: reflect.SelectRecv, Chan: c.haltV}}
	chosen, v, ok := reflect.Select(cases)
	if chosen == 0 {
		return v, ok
	}
	m.halted()
	cases[1].Chan = c.stopV
	if chosen, v, ok = reflect.Select(cases); chosen == 1 {
		panic(ErrInterrupted)
	}
	return v, ok
}

// chanSend sends v on ch for the ChanSend opcode; see chanRecv.
func (m *Machine) chanSend(ch, v reflect.Value) {
	c := m.control()
	if c == nil {
		if !m.watchFault() {
			ch.Send(v)
			return
		}
		if chosen, _, _ := reflect.Select([]reflect.SelectCase{
			{Dir: reflect.SelectSend, Chan: ch, Send: v},
			{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(m.fault.abort)},
		}); chosen == 1 {
			panic(ErrGoroutineFault)
		}
		return
	}
	if ch.TrySend(v) {
		return
	}
	cases := []reflect.SelectCase{{Dir: reflect.SelectSend, Chan: ch, Send: v}, {Dir: reflect.SelectRecv, Chan: c.haltV}}
	if chosen, _, _ := reflect.Select(cases); chosen == 0 {
		return
	}
	m.halted()
	cases[1].Chan = c.stopV
	if chosen, _, _ := reflect.Select(cases); chosen == 1 {
		panic(ErrInterrupted)
	}
}

// halted is called when a wait saw the halt channel closed: it panics with
// the interrupt, or with the goroutine fault when m aborts on one, and
// otherwise returns so the wait goes on without the halt channel.
func (m *Machine) halted() {
	if m.Interrupted() {
		panic(ErrInterrupted)
	}
	if m.watchFault() && m.fault.pending() != nil {
		panic(ErrGoroutineFault)
	}
}

func (m *Machine) watchFault() bool {
	return m.isRoot && m.fault != nil && !m.fault.cont && m.fault.spawned.Load()
}
