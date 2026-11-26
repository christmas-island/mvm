package interptest

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	"github.com/mvm-sh/mvm/vm"
)

// runInterruptible evaluates src on its own goroutine and returns the
// interpreter and a channel that receives Eval's error.
func runInterruptible(t *testing.T, src string, extra ...map[string]map[string]reflect.Value) (*interp.Interp, <-chan error, *lockedBuffer) {
	t.Helper()
	out := &lockedBuffer{}
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	for _, e := range extra {
		i.ImportPackageValues(e)
	}
	i.SetIO(strings.NewReader(""), out, out)
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- errors.New("host goroutine panicked: " + strings.TrimSpace(sprint(r)))
			}
		}()
		_, err := i.Eval("prog.go", src)
		done <- err
	}()
	return i, done, out
}

// lockedBuffer is a bytes.Buffer the program writes while the test reads it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func sprint(v any) string {
	if e, ok := v.(error); ok {
		return e.Error()
	}
	if s, ok := v.(string); ok {
		return s
	}
	return reflect.ValueOf(v).String()
}

// stopAndClose interrupts i, expects its run to end with ErrInterrupted (or
// nil when ok is set, for a run that may finish first) and every goroutine
// of i to stop, then closes it.
func stopAndClose(t *testing.T, i *interp.Interp, done <-chan error, mayFinish bool) {
	t.Helper()
	i.Interrupt()
	select {
	case err := <-done:
		if !errors.Is(err, interp.ErrInterrupted) && !(mayFinish && err == nil) {
			t.Fatalf("run ended with %v, want ErrInterrupted", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("run did not stop within 5s of the interrupt (%d goroutines running)", i.Running())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := i.WaitStopped(ctx); err != nil {
		t.Fatalf("WaitStopped: %v", err)
	}
	if n := i.Running(); n != 0 {
		t.Fatalf("Running() = %d after WaitStopped", n)
	}
	i.CloseAndRelease()
}

// waitStarted waits until out contains marker (the program printed it once
// it reached the state under test).
func waitStarted(t *testing.T, out *lockedBuffer, marker string, done <-chan error) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("run ended before %q: %v; output %q", marker, err, out.String())
		default:
		}
		if strings.Contains(out.String(), marker) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("program did not print %q", marker)
}

func TestInterruptStopsRunningCode(t *testing.T) {
	cases := []struct{ name, src string }{
		{"empty for", `package main
func main() { println("ready"); for {} }`},
		{"counter", `package main
var n int
func main() { println("ready"); for { n++ } }`},
		{"condition", `package main
func main() { println("ready"); x := 1; for x > 0 { x = x*3 + 1; if x < 0 { x = 1 } } }`},
		{"goto", `package main
func main() { println("ready"); i := 0
loop:
	i++
	if i > 0 { goto loop } }`},
		{"nested computation", `package main
func main() {
	println("ready")
	s := 0
	for i := 0; i < 1<<40; i++ {
		for j := 0; j < 1000; j++ {
			if j&1 == 0 { s += j } else { s -= i }
		}
	}
	println(s)
}`},
		{"range over slice", `package main
func main() {
	println("ready")
	xs := make([]int, 1000)
	for {
		for i := range xs { xs[i]++ }
	}
}`},
		{"range over func", `package main
func seq(yield func(int) bool) { for i := 0; ; i++ { if !yield(i) { return } } }
func main() {
	println("ready")
	s := 0
	for v := range seq { s += v }
}`},
		{"deep recursion", `package main
func fib(n int) int { if n < 2 { return n }; return fib(n-1) + fib(n-2) }
func main() { println("ready"); println(fib(60)) }`},
		{"infinite recursion", `package main
func f(n int) int { if n == -1 { return 0 }; return f(n+1) + 1 }
var started bool
func main() { println("ready"); println(f(0)) }`},
		{"loop calling native", `package main
import "strings"
func main() { println("ready"); for { _ = strings.Repeat("x", 10) } }`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			i, done, out := runInterruptible(t, c.src)
			waitStarted(t, out, "ready", done)
			time.Sleep(20 * time.Millisecond)
			stopAndClose(t, i, done, false)
		})
	}
}

// A program cannot swallow the interrupt with recover, nor keep running in a
// deferred function.
func TestInterruptNotRecoverable(t *testing.T) {
	cases := []struct{ name, src string }{
		{"recover and loop again", `package main
var caught int
func spin() {
	defer func() {
		if r := recover(); r != nil { caught++ }
	}()
	for {}
}
func main() { println("ready"); for { spin() } }`},
		{"loop in deferred func", `package main
func main() {
	defer func() { recover(); for {} }()
	println("ready")
	for {}
}`},
		{"recover in main", `package main
func main() {
	defer func() { recover(); println("recovered") }()
	println("ready")
	for {}
}`},
		{"recover around native callback", `package main
import "sort"
func main() {
	for {
		func() {
			defer func() { recover() }()
			xs := []int{3, 1, 2}
			sort.Slice(xs, func(i, j int) bool { println("ready"); for {}; return false })
		}()
	}
}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			i, done, out := runInterruptible(t, c.src)
			waitStarted(t, out, "ready", done)
			time.Sleep(20 * time.Millisecond)
			stopAndClose(t, i, done, false)
			if strings.Contains(out.String(), "recovered") {
				t.Errorf("a deferred function ran after the interrupt: %q", out.String())
			}
		})
	}
}

func TestInterruptStopsGoroutines(t *testing.T) {
	cases := []struct{ name, src string }{
		{"spinning", `package main
func main() {
	for i := 0; i < 16; i++ { go func() { for {} }() }
	println("ready")
	select {}
}`},
		{"channel receive", `package main
func main() {
	ch := make(chan int)
	for i := 0; i < 4; i++ { go func() { <-ch }() }
	println("ready")
	<-ch
}`},
		{"channel send", `package main
func main() {
	ch := make(chan int)
	for i := 0; i < 4; i++ { go func() { ch <- 1 }() }
	go func() { ch <- 1 }()
	println("ready")
	for { ch <- 2 }
}`},
		{"empty select", `package main
func main() {
	go func() { select {} }()
	println("ready")
	select {}
}`},
		{"select", `package main
func main() {
	a, b := make(chan int), make(chan string)
	go func() {
		select {
		case <-a:
		case b <- "x":
		}
	}()
	println("ready")
	select {
	case <-a:
	case v := <-b:
		_ = v
		<-a
	}
}`},
		{"nil channel", `package main
func main() {
	var ch chan int
	go func() { ch <- 1 }()
	println("ready")
	<-ch
}`},
		{"mutex", `package main
import "sync"
func main() {
	var mu sync.Mutex
	mu.Lock()
	for i := 0; i < 4; i++ { go func() { mu.Lock() }() }
	println("ready")
	mu.Lock()
}`},
		{"mutex in struct", `package main
import "sync"
type guarded struct {
	sync.Mutex
	n int
}
func main() {
	g := &guarded{}
	g.Lock()
	go func() { g.Lock(); g.n++ }()
	println("ready")
	g.Lock()
}`},
		{"locker interface", `package main
import "sync"
func main() {
	var l sync.Locker = &sync.Mutex{}
	l.Lock()
	println("ready")
	l.Lock()
}`},
		{"rwmutex", `package main
import "sync"
func main() {
	var mu sync.RWMutex
	mu.Lock()
	go func() { mu.RLock() }()
	println("ready")
	mu.Lock()
}`},
		{"waitgroup", `package main
import "sync"
func main() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { wg.Wait() }()
	println("ready")
	wg.Wait()
}`},
		{"sleep", `package main
import "time"
func main() {
	go func() { time.Sleep(time.Hour) }()
	println("ready")
	time.Sleep(time.Hour)
}`},
		{"timer channel", `package main
import "time"
func main() {
	go func() { <-time.After(time.Hour) }()
	println("ready")
	<-time.NewTimer(time.Hour).C
}`},
		{"context", `package main
import "context"
func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-ctx.Done() }()
	println("ready")
	<-ctx.Done()
}`},
		{"entry returned", `package main
func main() {
	ch := make(chan int)
	go func() { <-ch }()
	go func() { for {} }()
	println("ready")
}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base := runtime.NumGoroutine()
			i, done, out := runInterruptible(t, c.src)
			if c.name == "entry returned" {
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				// The entry is done; its goroutines are not.
				if n := i.Running(); n != 2 {
					t.Errorf("Running() = %d after the entry returned, want 2", n)
				}
				i.Interrupt()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := i.WaitStopped(ctx); err != nil {
					t.Fatal(err)
				}
				i.CloseAndRelease()
				waitGoroutines(t, base)
				return
			}
			waitStarted(t, out, "ready", done)
			time.Sleep(20 * time.Millisecond)
			if n := i.Running(); n == 0 {
				t.Errorf("Running() = 0 before the interrupt")
			}
			stopAndClose(t, i, done, false)
			waitGoroutines(t, base)
		})
	}
}

// waitGoroutines waits for the goroutine count to fall back to base.
func waitGoroutines(t *testing.T, base int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > base && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > base {
		buf := make([]byte, 1<<20)
		buf = buf[:runtime.Stack(buf, true)]
		t.Fatalf("%d goroutines left, %d before the run:\n%s", n, base, buf)
	}
}

// A goroutine parked in a native read cannot be stopped: Running and
// WaitStopped say so, and the goroutine stops once the read returns.
func TestInterruptNativeRead(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	host := map[string]map[string]reflect.Value{
		"hostpipe": {"R": reflect.ValueOf(&r).Elem()},
	}
	i, done, out := runInterruptible(t, `package main
import "hostpipe"
func main() {
	go func() { for {} }()
	println("ready")
	buf := make([]byte, 8)
	hostpipe.R.Read(buf)
	for {}
}`, host)
	waitStarted(t, out, "ready", done)
	time.Sleep(20 * time.Millisecond)
	i.Interrupt()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	var sre *vm.StillRunningError
	if err := i.WaitStopped(ctx); !errors.As(err, &sre) || sre.Goroutines != 1 {
		t.Fatalf("WaitStopped = %v, want a StillRunningError for the 1 goroutine in Read", err)
	}
	select {
	case err := <-done:
		t.Fatalf("run ended while parked in Read: %v", err)
	default:
	}
	w.Close() // the read returns, the goroutine sees the interrupt
	stopAndClose(t, i, done, false)
}

// An interrupt reaches interpreted code that native code calls back.
func TestInterruptInCallback(t *testing.T) {
	cases := []struct{ name, src string }{
		{"sort.Sort Less", `package main
import "sort"
type vals []int
func (v vals) Len() int { return len(v) }
func (v vals) Less(i, j int) bool { println("ready"); for {}; return false }
func (v vals) Swap(i, j int) { v[i], v[j] = v[j], v[i] }
func main() { sort.Sort(vals{3, 2, 1}) }`},
		{"sort.Slice less", `package main
import "sort"
func main() { xs := []int{3, 2, 1}; sort.Slice(xs, func(i, j int) bool { println("ready"); for {}; return false }) }`},
		{"json MarshalJSON", `package main
import "encoding/json"
type v int
func (v) MarshalJSON() ([]byte, error) { println("ready"); for {}; return nil, nil }
func main() { json.Marshal([]v{1, 2}) }`},
		{"fmt String", `package main
import "fmt"
type v int
func (v) String() string { println("ready"); for {}; return "" }
func main() { for { _ = fmt.Sprint(v(1)) } }`},
		{"callback blocks", `package main
import "sort"
func main() { ch := make(chan int); xs := []int{3, 2, 1}; sort.Slice(xs, func(i, j int) bool { println("ready"); <-ch; return false }) }`},
		{"native goroutine", `package main
import "sort"
func main() {
	xs := []int{3, 2, 1}
	go sort.Slice(xs, func(i, j int) bool { println("ready"); for {}; return false })
	select {}
}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base := runtime.NumGoroutine()
			i, done, out := runInterruptible(t, c.src)
			waitStarted(t, out, "ready", done)
			time.Sleep(20 * time.Millisecond)
			stopAndClose(t, i, done, false)
			waitGoroutines(t, base)
		})
	}
}

// A callback a native goroutine runs after the interrupt (a timer) returns
// at once without running interpreted code.
func TestInterruptTimerCallback(t *testing.T) {
	i, done, out := runInterruptible(t, `package main
import "time"
func main() {
	time.AfterFunc(100*time.Millisecond, func() { println("fired"); for {} })
	println("ready")
	select {}
}`)
	waitStarted(t, out, "ready", done)
	stopAndClose(t, i, done, false)
	time.Sleep(200 * time.Millisecond)
	if strings.Contains(out.String(), "fired") {
		t.Fatalf("the timer callback ran interpreted code after the interrupt")
	}
}

// Interrupting a run that is finishing on its own: the run ends either way,
// and nothing is left running.
func TestInterruptRacesCompletion(t *testing.T) {
	n := 200
	if testing.Short() {
		n = 40
	}
	for k := range n {
		i, done, _ := runInterruptible(t, `package main
func main() {
	ch := make(chan int)
	go func() { s := 0; for i := 0; i < 3000; i++ { s += i }; ch <- s }()
	<-ch
}`)
		time.Sleep(time.Duration(k%20) * 50 * time.Microsecond)
		stopAndClose(t, i, done, true)
	}
}

// Interrupt before the run and after it.
func TestInterruptBeforeAndAfter(t *testing.T) {
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	if _, err := i.Eval("a.go", `package main
func main() {}`); err != nil {
		t.Fatal(err)
	}
	i.Interrupt()
	i.Interrupt()
	if _, err := i.Eval("b.go", `println("ran")`); !errors.Is(err, interp.ErrInterrupted) {
		t.Fatalf("Eval after Interrupt = %v, want ErrInterrupted", err)
	}
	if err := i.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	i.CloseAndRelease()
}

// The interruptible sync methods keep their semantics, called directly,
// deferred, through an interface, promoted from an embedded field, and as a
// go statement.
func TestInterruptibleSyncSemantics(t *testing.T) {
	out := evalOut(t, "sync.go", `package main
import (
	"fmt"
	"sync"
)
type counter struct {
	sync.Mutex
	n int
}
func main() {
	var mu sync.Mutex
	var rw sync.RWMutex
	var wg sync.WaitGroup
	c := &counter{}
	var l sync.Locker = &mu
	n, r := 0, 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				func() { mu.Lock(); defer mu.Unlock(); n++ }()
				l.Lock(); n++; l.Unlock()
				c.Lock(); c.n++; c.Unlock()
				rw.RLock(); _ = r; rw.RUnlock()
				rw.Lock(); r++; rw.Unlock()
			}
		}()
	}
	wg.Wait()
	var wg2 sync.WaitGroup
	wg2.Add(2)
	go wg2.Done()
	go func() { defer wg2.Done() }()
	wg2.Wait()
	fmt.Println(n, c.n, r)
}`)
	if out != "8000 4000 4000\n" {
		t.Fatalf("got %q", out)
	}
}
