//go:build !wasm

package memprobe

import (
	"bytes"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mvm-sh/mvm/internal/derive"
	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
)

type cacheSizes struct{ reservations, derived, synthIfaces int }

func caches() cacheSizes {
	r, d, s := derive.CacheSizes()
	return cacheSizes{r, d, s}
}

// waitCaches collects garbage until the type caches are back to want (Close
// prunes them from a cleanup once the interpreter is unreachable).
func waitCaches(t *testing.T, want cacheSizes) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for caches() != want {
		if time.Now().After(deadline) {
			t.Fatalf("type caches %+v, want %+v (not pruned)", caches(), want)
		}
		runtime.GC()
		time.Sleep(5 * time.Millisecond)
	}
}

func mustLoad(t *testing.T, w *workload, mode closeMode) {
	t.Helper()
	got, err := load(w, mode)
	if err != nil {
		t.Fatal(err)
	}
	if got != w.want {
		t.Fatalf("output %q, want %q", got, w.want)
	}
}

func sixWorkload() *workload {
	return &workload{name: "six", src: sixMethods, want: sixMethodsOut}
}

func twentyWorkload() *workload {
	return &workload{name: "twenty", src: twentyMethods, want: twentyMethodsOut}
}

// CloseAndRelease drops the interpreter's entries in the global type caches
// at once.
func TestCloseAndReleasePrunesTypeCaches(t *testing.T) {
	waitCaches(t, cacheSizes{}) // earlier tests' interpreters
	for _, w := range []*workload{sixWorkload(), twentyWorkload()} {
		before := caches()
		mustLoad(t, w, modeRelease)
		if got := caches(); got != before {
			t.Errorf("%s: type caches %+v after CloseAndRelease, want %+v", w.name, got, before)
		}
	}
}

// Close drops them once the interpreter has been collected.
func TestClosePrunesTypeCachesOnceCollected(t *testing.T) {
	waitCaches(t, cacheSizes{})
	for _, w := range []*workload{sixWorkload(), twentyWorkload()} {
		before := caches()
		for range 3 {
			mustLoad(t, w, modeClose)
		}
		waitCaches(t, before)
	}
}

// A goroutine still running after Close keeps the interpreter's cache entries
// until it ends, and works with them. (Its methods that native code calls
// have no handler after Close, as before: Close parks their stub slots; so
// the goroutine calls them itself.)
func TestCloseKeepsTypeCachesWhileGoroutineRuns(t *testing.T) {
	waitCaches(t, cacheSizes{})
	before := caches()
	release := make(chan struct{})
	got := make(chan string, 1)
	host := map[string]map[string]reflect.Value{
		"example.com/host": {
			"Wait": reflect.ValueOf(func() { <-release }),
			"Done": reflect.ValueOf(func(s string) { got <- s }),
		},
	}
	src := `package main

import (
	"encoding/json"
	"fmt"

	"example.com/host"
)

type Val int

func (v Val) String() string { return fmt.Sprintf("v%d", int(v)) }

type Vals []Val

func (s Vals) Len() int           { return len(s) }
func (s Vals) Less(i, j int) bool { return s[i] < s[j] }
func (s Vals) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

func main() {
	go func() {
		host.Wait()
		vs := Vals{3, 1, 2}
		for i := 1; i < vs.Len(); i++ {
			for j := i; j > 0 && vs.Less(j, j-1); j-- {
				vs.Swap(j, j-1)
			}
		}
		p := &vs
		m := map[string][]*Vals{"k": {p}}
		b, _ := json.Marshal(m)
		host.Done(vs[0].String() + vs[2].String() + " " + string(b))
	}()
}
`
	func() {
		var stdout, stderr bytes.Buffer
		i := interp.NewInterpreter(golang.GoSpec)
		i.ImportPackageValues(stdlib.Values)
		i.ImportPackageValues(host)
		i.SetIO(os.Stdin, &stdout, &stderr)
		if _, err := i.Eval("goroutine.go", src); err != nil {
			t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
		}
		i.Close()
	}()
	for range 5 {
		runtime.GC()
		time.Sleep(5 * time.Millisecond)
	}
	if caches() == before {
		t.Fatal("type caches pruned while a goroutine of the closed interpreter still runs")
	}
	close(release)
	select {
	case s := <-got:
		if want := `v1v3 {"k":[[1,2,3]]}`; s != want {
			t.Errorf("goroutine after Close: got %q, want %q", s, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("goroutine did not finish")
	}
	waitCaches(t, before)
}

// Pruning a closed interpreter's entries leaves a live interpreter's alone,
// including one running the same code.
func TestPruneSparesLiveInterpreter(t *testing.T) {
	waitCaches(t, cacheSizes{})
	var stdout, stderr bytes.Buffer
	live := interp.NewInterpreter(golang.GoSpec)
	defer live.CloseAndRelease()
	live.ImportPackageValues(stdlib.Values)
	live.SetIO(os.Stdin, &stdout, &stderr)
	if _, err := live.Eval("live.go", sixMethods); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	withLive := caches()
	if withLive == (cacheSizes{}) {
		t.Fatal("live interpreter has no type cache entries")
	}
	for k := range 10 {
		mode := modeClose
		if k%2 == 1 {
			mode = modeRelease
		}
		mustLoad(t, sixWorkload(), mode)
	}
	waitCaches(t, withLive)
	stdout.Reset()
	// A second Eval continues the program, REPL style (redefining main
	// would not run it).
	if _, err := live.Eval("live2.go", `vs := Vals{9, 7, 8}
sort.Sort(vs)
p := &vs
b, _ := json.Marshal([]*Vals{p})
fmt.Println(fmt.Sprint(vs, string(b), error(Err{2})))`); err != nil {
		t.Fatalf("live Eval after others closed: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "[v7 v8 v9][[7,8,9]]e2\n"; !strings.HasPrefix(got, want) {
		t.Errorf("live interpreter: got %q, want %q", got, want)
	}
}
