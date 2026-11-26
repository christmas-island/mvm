//go:build !wasm

// Package interruptsoak interrupts hundreds of runs in one process and checks
// that each interrupted interpreter is released as completely as one whose
// run finished: its stub slots, goroutines and memory. It is its own test
// binary so the slots it takes do not change what other tests observe.
package interruptsoak

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mvm-sh/mvm/internal/stubs"
	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// program is the run under test: twelve native-callable methods (so each
// interpreter takes stub slots), output computed through them, then, for
// mode "spin", goroutines and main spinning, for "block", goroutines and main
// blocked in a channel, a select, a mutex, a WaitGroup and a sleep, and for
// "done", a normal return.
const program = `package main

import (
	"encoding/json"
	"fmt"
	"host"
	"sort"
	"strconv"
	"sync"
	"time"
)

type SoakVal int

func (v SoakVal) String() string               { return "v" + strconv.Itoa(int(v)) }
func (v SoakVal) MarshalJSON() ([]byte, error) { return []byte(strconv.Itoa(int(v) * 2)), nil }

type SoakVals []SoakVal

func (s SoakVals) Len() int           { return len(s) }
func (s SoakVals) Less(i, j int) bool { return s[i] < s[j] }
func (s SoakVals) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

type SoakErr struct{ N int }

func (e SoakErr) Error() string { return "e" + strconv.Itoa(e.N) }

func main() {
	vs := SoakVals{3, 1, 2}
	sort.Sort(vs)
	b, _ := json.Marshal(vs)
	fmt.Println(vs, string(b), error(SoakErr{1}))
	switch host.Mode() {
	case "spin":
		for i := 0; i < 3; i++ {
			go func() { n := 0; for { n++ } }()
		}
		host.Ready()
		for {
		}
	case "block":
		var mu sync.Mutex
		var wg sync.WaitGroup
		mu.Lock()
		wg.Add(1)
		ch := make(chan int)
		go func() { <-ch }()
		go func() { select {} }()
		go func() { mu.Lock() }()
		go func() { wg.Wait() }()
		go func() { time.Sleep(time.Hour) }()
		host.Ready()
		<-ch
	}
}
`

const programOut = "[v1 v2 v3] [2,4,6] e1\n"

type result struct {
	err error
	out string
}

// run evaluates program in mode; when the mode does not finish, it waits for
// the program to be ready, interrupts it and waits for it to stop. The
// interpreter is closed with CloseAndRelease in every case.
func run(t testing.TB, mode string) {
	t.Helper()
	var out bytes.Buffer
	ready := make(chan struct{})
	var once sync.Once
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageValues(map[string]map[string]reflect.Value{"host": {
		"Mode":  reflect.ValueOf(func() string { return mode }),
		"Ready": reflect.ValueOf(func() { once.Do(func() { close(ready) }) }),
	}})
	i.SetIO(os.Stdin, &out, &out)
	done := make(chan result, 1)
	go func() {
		_, err := i.Eval("soak.go", program)
		done <- result{err, out.String()}
	}()
	var r result
	if mode == "done" {
		r = <-done
		if r.err != nil {
			t.Fatalf("run: %v", r.err)
		}
	} else {
		select {
		case <-ready:
		case r = <-done:
			t.Fatalf("%s run ended before it was ready: %v %q", mode, r.err, r.out)
		}
		i.Interrupt()
		r = <-done
		if !errors.Is(r.err, interp.ErrInterrupted) {
			t.Fatalf("%s run ended with %v, want ErrInterrupted", mode, r.err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := i.WaitStopped(ctx); err != nil {
		t.Fatalf("%s: %v", mode, err)
	}
	if r.out != programOut {
		t.Fatalf("%s: output %q, want %q", mode, r.out, programOut)
	}
	i.CloseAndRelease()
}

func slots() int {
	n := 0
	for _, p := range stubs.HighWater() {
		n += int(p.Used)
	}
	return n
}

func settle() runtime.MemStats {
	for range 3 {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
	runtime.GC()
	debug.FreeOSMemory()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms
}

func waitGoroutines(t *testing.T, base int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > base && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > base {
		buf := make([]byte, 1<<20)
		t.Fatalf("%d goroutines, %d at the start:\n%s", n, base, buf[:runtime.Stack(buf, true)])
	}
}

// heapPerRun runs mode n times after a warm-up and returns the live heap
// retained per run, in KiB.
func heapPerRun(t *testing.T, mode string, n int) float64 {
	for range 10 {
		run(t, mode)
	}
	before := settle()
	for range n {
		run(t, mode)
	}
	after := settle()
	return (float64(after.HeapAlloc) - float64(before.HeapAlloc)) / float64(n) / 1024
}

// 500 interrupted runs, half spinning and half blocked, leave no goroutine
// behind, hold no more stub slots than one run, and leave as little memory
// as completed runs; programs loaded afterwards still load.
func TestInterruptedRunsRelease(t *testing.T) {
	n := 500
	if testing.Short() {
		n = 100
	}
	base := runtime.NumGoroutine()
	run(t, "done") // the program's slots, parked for reuse
	slots0 := slots()
	for k := range n {
		if k%2 == 0 {
			run(t, "spin")
		} else {
			run(t, "block")
		}
		if got := slots(); got > slots0 {
			t.Fatalf("interrupted run %d: %d stub slots held, %d after one completed run", k, got, slots0)
		}
	}
	waitGoroutines(t, base)

	// Other programs still load: more distinct ones than a 256-slot pool
	// holds when the build can reclaim slots, a few otherwise.
	distinct := 300
	if stubs.SweepAvailable() != nil {
		distinct = 20
	}
	for k := range distinct {
		var out bytes.Buffer
		i := interp.NewInterpreter(golang.GoSpec)
		i.ImportPackageValues(stdlib.Values)
		i.SetIO(os.Stdin, &out, &out)
		if _, err := i.Eval("distinct.go", distinctSrc(k)); err != nil {
			t.Fatalf("distinct program %d after %d interrupted runs: %v", k, n, err)
		}
		if got, want := out.String(), distinctWant(k); got != want {
			t.Fatalf("distinct program %d: %q, want %q", k, got, want)
		}
		i.CloseAndRelease()
	}
	waitGoroutines(t, base)
	t.Logf("%d interrupted runs, %d distinct programs; stub slots %d, goroutines %d (start %d)",
		n, distinct, slots(), runtime.NumGoroutine(), base)
}

// An interrupted run leaves about as much live heap behind as a completed
// one.
func TestInterruptedRunMemory(t *testing.T) {
	n := 150
	if testing.Short() {
		n = 40
	}
	done := heapPerRun(t, "done", n)
	spin := heapPerRun(t, "spin", n)
	block := heapPerRun(t, "block", n)
	t.Logf("live heap retained per run: completed %.1f KiB, interrupted spinning %.1f KiB, interrupted blocked %.1f KiB",
		done, spin, block)
	limit := max(2*done, done+16)
	for _, c := range []struct {
		name string
		kib  float64
	}{{"spinning", spin}, {"blocked", block}} {
		if c.kib > limit {
			t.Errorf("interrupted %s run retains %.1f KiB per run, completed %.1f KiB", c.name, c.kib, done)
		}
	}
}

func distinctSrc(k int) string {
	return strings.NewReplacer("PFX", "P"+strconv.Itoa(k), "KK", strconv.Itoa(k)).Replace(`package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
)

type PFXVal int

func (v PFXVal) String() string               { return "KK:" + strconv.Itoa(int(v)) }
func (v PFXVal) MarshalJSON() ([]byte, error) { return []byte(strconv.Itoa(int(v) * KK)), nil }

type PFXVals []PFXVal

func (s PFXVals) Len() int           { return len(s) }
func (s PFXVals) Less(i, j int) bool { return s[i] < s[j] }
func (s PFXVals) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

func main() {
	vs := PFXVals{}
	for i := 0; i < 6; i++ {
		vs = append(vs, PFXVal((i*5+KK)%11))
	}
	sort.Sort(vs)
	js, _ := json.Marshal(vs)
	fmt.Println(vs, string(js))
}
`)
}

func distinctWant(k int) string {
	vs := make([]int, 0, 6)
	for i := range 6 {
		vs = append(vs, (i*5+k)%11)
	}
	sort.Ints(vs)
	strs := make([]string, len(vs))
	js := make([]string, len(vs))
	for i, v := range vs {
		strs[i] = fmt.Sprintf("%d:%d", k, v)
		js[i] = strconv.Itoa(v * k)
	}
	return fmt.Sprintf("[%s] [%s]\n", strings.Join(strs, " "), strings.Join(js, ","))
}
