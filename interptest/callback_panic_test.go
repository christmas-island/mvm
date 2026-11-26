package interptest

import (
	"bytes"
	"errors"
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

// A panic in interpreted code that native code calls on a goroutine with
// nothing beneath to recover it is recorded on the interpreter; before, it
// ended the process. Where something beneath recovers it (an interpreted
// caller, net/http, iter.Pull, the host), it still propagates.

type cbRun struct {
	i   *interp.Interp
	err error
	out string
}

func evalCallback(t *testing.T, src string, extra ...map[string]map[string]reflect.Value) cbRun {
	t.Helper()
	var out bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	for _, e := range extra {
		i.ImportPackageValues(e)
	}
	w := &lockedWriter{w: &out}
	i.SetIO(strings.NewReader(""), w, w)
	_, err := i.Eval("prog.go", src)
	return cbRun{i: i, err: err, out: w.String()}
}

type lockedWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func (l *lockedWriter) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.String()
}

// wantCallbackPanic checks that i recorded one callback panic carrying value
// and an authored position, started by caller.
func wantCallbackPanic(t *testing.T, i *interp.Interp, value, caller string) *vm.CallbackPanicError {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if n, _ := i.CallbackPanics(); n > 0 || time.Now().After(deadline) {
			break
		}
		runtime.GC()
		time.Sleep(5 * time.Millisecond)
	}
	n, first := i.CallbackPanics()
	if n != 1 {
		t.Fatalf("CallbackPanics() = %d, %v; want 1", n, first)
	}
	var cpe *vm.CallbackPanicError
	if !errors.As(first, &cpe) {
		t.Fatalf("recorded %T %v, want a *vm.CallbackPanicError", first, first)
	}
	msg := cpe.Error()
	if !strings.Contains(msg, value) || !strings.Contains(msg, "prog.go:") {
		t.Errorf("callback panic %q lacks the value %q or a prog.go position", msg, value)
	}
	if caller != "" && cpe.Caller != caller {
		t.Errorf("Caller = %q, want %q", cpe.Caller, caller)
	}
	return cpe
}

// wantExit2 checks that a run ended as after a panic on another goroutine,
// with the callback panic as the interpreter's goroutine fault.
func wantExit2(t *testing.T, r cbRun) {
	t.Helper()
	var xe *interp.ExitError
	if !errors.As(r.err, &xe) || xe.Code != 2 {
		t.Fatalf("Eval = %v, want exit status 2", r.err)
	}
	var cpe *vm.CallbackPanicError
	if !errors.As(r.i.GoroutineFault(), &cpe) {
		t.Fatalf("GoroutineFault() = %v, want the callback panic", r.i.GoroutineFault())
	}
}

func TestCallbackPanicAfterFuncDuringRun(t *testing.T) {
	r := evalCallback(t, `package main
import "time"
func main() {
	time.AfterFunc(20*time.Millisecond, func() { panic("boom in AfterFunc callback") })
	time.Sleep(300 * time.Millisecond)
}`)
	wantExit2(t, r)
	wantCallbackPanic(t, r.i, "boom in AfterFunc callback", "time.goFunc")
}

func TestCallbackPanicAfterFuncAfterReturn(t *testing.T) {
	var handled []error
	var mu sync.Mutex
	r := evalCallback(t, `package main
import "time"
func main() {
	time.AfterFunc(50*time.Millisecond, func() { panic("boom after return") })
}`)
	r.i.SetCallbackPanicHandler(func(err error) { mu.Lock(); handled = append(handled, err); mu.Unlock() })
	if r.err != nil {
		t.Fatalf("Eval: %v", r.err)
	}
	wantCallbackPanic(t, r.i, "boom after return", "time.goFunc")
	mu.Lock()
	defer mu.Unlock()
	if len(handled) != 1 {
		t.Fatalf("handler called %d times, want 1", len(handled))
	}
}

func TestCallbackPanicContextAfterFunc(t *testing.T) {
	r := evalCallback(t, `package main
import ("context"; "time")
func main() {
	ctx, cancel := context.WithCancel(context.Background())
	context.AfterFunc(ctx, func() { panic("boom in context.AfterFunc") })
	cancel()
	time.Sleep(200 * time.Millisecond)
}`)
	wantExit2(t, r)
	wantCallbackPanic(t, r.i, "boom in context.AfterFunc", "")
}

func TestCallbackPanicWaitGroupGo(t *testing.T) {
	r := evalCallback(t, `package main
import "sync"
func main() {
	var wg sync.WaitGroup
	wg.Go(func() { panic("boom in wg.Go") })
	wg.Wait()
}`)
	wantExit2(t, r)
	wantCallbackPanic(t, r.i, "boom in wg.Go", "")
}

func TestCallbackPanicOnceOnTimer(t *testing.T) {
	r := evalCallback(t, `package main
import ("sync"; "time")
var once sync.Once
func main() {
	time.AfterFunc(10*time.Millisecond, func() { once.Do(func() { panic("boom in Once") }) })
	time.Sleep(200 * time.Millisecond)
}`)
	wantExit2(t, r)
	wantCallbackPanic(t, r.i, "boom in Once", "time.goFunc")
}

func TestCallbackPanicExitOnTimer(t *testing.T) {
	r := evalCallback(t, `package main
import ("os"; "time")
func main() {
	time.AfterFunc(10*time.Millisecond, func() { os.Exit(3) })
	time.Sleep(200 * time.Millisecond)
}`)
	wantExit2(t, r)
	n, first := r.i.CallbackPanics()
	if n != 1 || !strings.Contains(first.Error(), "exit status 3") {
		t.Fatalf("CallbackPanics() = %d, %v; want the exit status", n, first)
	}
}

func TestCallbackPanicCleanup(t *testing.T) {
	r := evalCallback(t, `package main
import "runtime"
type box struct{ n [64]int }
func main() {
	b := &box{}
	runtime.AddCleanup(b, func(int) { panic("boom in cleanup") }, 0)
	b = nil
	for i := 0; i < 5; i++ { runtime.GC() }
}`)
	if r.err != nil && !strings.Contains(r.err.Error(), "exit status 2") {
		t.Fatalf("Eval: %v", r.err)
	}
	wantCallbackPanic(t, r.i, "boom in cleanup", "")
}

func TestCallbackPanicFinalizer(t *testing.T) {
	r := evalCallback(t, `package main
import "runtime"
type box struct{ n [64]int }
func main() {
	b := &box{}
	runtime.SetFinalizer(b, func(*box) { panic("boom in finalizer") })
	b = nil
	for i := 0; i < 5; i++ { runtime.GC() }
}`)
	if r.err != nil && !strings.Contains(r.err.Error(), "exit status 2") {
		t.Fatalf("Eval: %v", r.err)
	}
	wantCallbackPanic(t, r.i, "boom in finalizer", "")
}

// A native goroutine the program starts with go (here sort.Slice) whose
// callback panics: the goroutine's fault, as for an interpreted goroutine.
func TestCallbackPanicNativeGoStatement(t *testing.T) {
	r := evalCallback(t, `package main
import ("sort"; "time")
func main() {
	xs := []int{3, 2, 1}
	go sort.Slice(xs, func(i, j int) bool { panic("boom in go sort.Slice") })
	time.Sleep(200 * time.Millisecond)
}`)
	var xe *interp.ExitError
	if !errors.As(r.err, &xe) || xe.Code != 2 {
		t.Fatalf("Eval = %v, want exit status 2", r.err)
	}
	if gf := r.i.GoroutineFault(); gf == nil || !strings.Contains(gf.Error(), "boom in go sort.Slice") {
		t.Fatalf("GoroutineFault() = %v", gf)
	}
}

// Same goroutine as an interpreted caller: the panic propagates through the
// native caller and interpreted recover catches it, unchanged.
func TestCallbackPanicSameGoroutineRecoverable(t *testing.T) {
	r := evalCallback(t, `package main
import ("fmt"; "sort"; "sync")
func try(name string, f func()) {
	defer func() { fmt.Println(name, recover()) }()
	f()
}
func main() {
	try("slice", func() { sort.Slice([]int{2, 1}, func(i, j int) bool { panic("less func") }) })
	var once sync.Once
	try("once", func() { once.Do(func() { panic("once") }) })
}`)
	if r.err != nil {
		t.Fatalf("Eval: %v\n%s", r.err, r.out)
	}
	for _, want := range []string{"slice less func", "once once"} {
		if !strings.Contains(r.out, want) {
			t.Errorf("output lacks %q:\n%s", want, r.out)
		}
	}
	if n, first := r.i.CallbackPanics(); n != 0 {
		t.Errorf("CallbackPanics() = %d, %v; want 0", n, first)
	}
}

// A range-over-func iterator runs on an iter.Pull coroutine, which hands its
// panic to the loop's goroutine: it propagates (recover in the loop's
// function does not see it today, on integration too; a separate issue).
func TestCallbackPanicRangeOverFunc(t *testing.T) {
	r := evalCallback(t, `package main
func seq(yield func(int) bool) { yield(1); panic("boom in seq") }
func main() { for range seq {} }`)
	if r.err == nil || !strings.Contains(r.err.Error(), "boom in seq") {
		t.Fatalf("Eval = %v, want the iterator's panic", r.err)
	}
	if n, first := r.i.CallbackPanics(); n != 0 {
		t.Errorf("CallbackPanics() = %d, %v; want 0", n, first)
	}
}

// net/http recovers a handler's panic and closes the connection: unchanged.
func TestCallbackPanicHTTPHandler(t *testing.T) {
	r := evalCallback(t, `package main
import ("fmt"; "log"; "io"; "net/http"; "net/http/httptest")
func main() {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom in handler")
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.Start()
	defer srv.Close()
	_, err := http.Get(srv.URL)
	fmt.Println("get error:", err != nil)
}`)
	if r.err != nil {
		t.Fatalf("Eval: %v\n%s", r.err, r.out)
	}
	if !strings.Contains(r.out, "get error: true") {
		t.Errorf("output %q: the client should see the closed connection", r.out)
	}
	if n, first := r.i.CallbackPanics(); n != 0 {
		t.Errorf("CallbackPanics() = %d, %v; want 0 (net/http recovered it)", n, first)
	}
}

// A host that calls an interpreted func value itself, on its own goroutine,
// still sees the panic.
func TestCallbackPanicHostCaller(t *testing.T) {
	var kept func()
	r := evalCallback(t, `package main
import "host"
func main() { host.Keep(func() { panic("boom for host") }) }`, map[string]map[string]reflect.Value{
		"host": {"Keep": reflect.ValueOf(func(f func()) { kept = f })},
	})
	if r.err != nil {
		t.Fatal(r.err)
	}
	got := make(chan any, 1)
	go func() {
		defer func() { got <- recover() }()
		kept()
	}()
	if p := <-got; p == nil || !strings.Contains(sprintAny(p), "boom for host") {
		t.Fatalf("host recovered %v, want the panic", p)
	}
	if n, _ := r.i.CallbackPanics(); n != 0 {
		t.Errorf("CallbackPanics() = %d, want 0", n)
	}
}

func sprintAny(v any) string {
	if e, ok := v.(error); ok {
		return e.Error()
	}
	if s, ok := v.(string); ok {
		return s
	}
	return reflect.ValueOf(v).String()
}
