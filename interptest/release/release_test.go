//go:build !wasm

// Package release tests Interp.CloseAndRelease. It is its own test binary so
// the slots it sweeps do not change the pool counters other tests watch.
package release

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/internal/stubs"
	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// distinctSrc is program k: its own type names (so no two programs share a
// slot-reuse key) with String, MarshalJSON, Len/Less/Swap and Error methods
// that native code calls (fmt, encoding/json, sort, errors).
func distinctSrc(k int) string {
	p := "P" + strconv.Itoa(k)
	return strings.NewReplacer("PFX", p, "KK", strconv.Itoa(k)).Replace(`package main

import (
	"encoding/json"
	"errors"
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

type PFXErr struct{ N int }

func (e PFXErr) Error() string { return "eKK-" + strconv.Itoa(e.N) }

func main() {
	vs := PFXVals{}
	for i := 0; i < 6; i++ {
		vs = append(vs, PFXVal((i*5+KK)%11))
	}
	sort.Sort(vs)
	js, err := json.Marshal(vs)
	if err != nil {
		panic(err)
	}
	var e error = fmt.Errorf("wrap: %w", PFXErr{KK})
	var target PFXErr
	fmt.Println(vs, string(js), e, errors.As(e, &target))
}
`)
}

// distinctWant is distinctSrc(k)'s output, computed natively.
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
	return fmt.Sprintf("[%s] [%s] wrap: e%d-%d true\n", strings.Join(strs, " "), strings.Join(js, ","), k, k)
}

func newInterp(t testing.TB, stdout *bytes.Buffer, extra ...map[string]map[string]reflect.Value) *interp.Interp {
	t.Helper()
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	for _, e := range extra {
		i.ImportPackageValues(e)
	}
	var stderr bytes.Buffer
	i.SetIO(os.Stdin, stdout, &stderr)
	return i
}

// Far more distinct programs than any pool holds load, run and close in one
// process: CloseAndRelease makes their slots available to the next program.
func TestCloseAndReleaseDistinctPrograms(t *testing.T) {
	if err := stubs.SweepAvailable(); err != nil {
		t.Skip(err)
	}
	n := 5000
	if testing.Short() {
		n = 300 // still past the 128 that S2/S9/S10 hold without reuse
	}
	sweeps0, _, _, _ := stubs.SweepStats()
	for k := range n {
		var stdout bytes.Buffer
		i := newInterp(t, &stdout)
		if _, err := i.Eval("distinct.go", distinctSrc(k)); err != nil {
			t.Fatalf("program %d: %v", k, err)
		}
		if got, want := stdout.String(), distinctWant(k); got != want {
			t.Fatalf("program %d: got %q, want %q", k, got, want)
		}
		i.CloseAndRelease()
	}
	sweeps, slots, total, lastErr := stubs.SweepStats()
	if lastErr != nil {
		t.Fatalf("last sweep: %v", lastErr)
	}
	if sweeps == sweeps0 {
		t.Fatal("no sweep ran: the test did not exhaust a pool")
	}
	for _, p := range stubs.HighWater() {
		if p.Used > p.Cap {
			t.Errorf("pool %s over its cap: %d/%d", p.Name, p.Used, p.Cap)
		}
	}
	t.Logf("%d programs: %d sweeps, %d slots swept, %v sweeping", n, sweeps-sweeps0, slots, total)
}

// Programs load, run and close concurrently, with sweeps (when the build has
// them) racing the loads of other goroutines.
func TestCloseAndReleaseConcurrent(t *testing.T) {
	const workers = 8
	per := 100 // 800 programs: several sweeps
	if stubs.SweepAvailable() != nil || testing.Short() {
		per = 10 // 80 programs fit the pools without a sweep
	}
	errs := make(chan error, workers)
	for w := range workers {
		go func() {
			for j := range per {
				k := 100000 + w*per + j
				var stdout bytes.Buffer
				i := newInterp(t, &stdout)
				if _, err := i.Eval("distinct.go", distinctSrc(k)); err != nil {
					errs <- fmt.Errorf("program %d: %w", k, err)
					return
				}
				if got, want := stdout.String(), distinctWant(k); got != want {
					errs <- fmt.Errorf("program %d: got %q, want %q", k, got, want)
					return
				}
				i.CloseAndRelease()
			}
			errs <- nil
		}()
	}
	for range workers {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
}

// A value the host keeps past CloseAndRelease must not run the method of the
// program that takes its slot next (a type of another layout, whose method
// would misread the stale value's memory): every call through its type, an
// itab or reflect panics with ErrStaleMethodCall.
func TestCloseAndReleaseStaleValue(t *testing.T) {
	if err := stubs.SweepAvailable(); err != nil {
		t.Skip(err)
	}
	// Sweep what earlier tests retired, so the swept slots on top after the
	// sweep below are the first program's.
	if _, err := stubs.Sweep(); err != nil {
		t.Fatal(err)
	}
	var kept []any
	sink := map[string]map[string]reflect.Value{
		"example.com/sink": {"Put": reflect.ValueOf(func(v any) { kept = append(kept, v) })},
	}
	run := func(src string) *interp.Interp {
		t.Helper()
		var stdout bytes.Buffer
		i := newInterp(t, &stdout, sink)
		if _, err := i.Eval("stale.go", src); err != nil {
			t.Fatalf("Eval: %v", err)
		}
		return i
	}
	first := run(`package main

import "example.com/sink"

type T struct{ a, b int }

func (t T) String() string { return "first" }

func (t T) MarshalJSON() ([]byte, error) { return []byte("1"), nil }

type Ts []T

func (s Ts) Len() int           { return len(s) }
func (s Ts) Less(i, j int) bool { return s[i].a < s[j].a }
func (s Ts) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

func main() { sink.Put(T{1, 2}); sink.Put(Ts{{2, 0}, {1, 0}}) }
`)
	if len(kept) != 2 {
		t.Fatalf("kept %d values, want 2", len(kept))
	}
	stale, staleSlice := kept[0], kept[1]
	// itabs built while the first program is live.
	asStringer := stale.(fmt.Stringer)
	asSorter := staleSlice.(sort.Interface)
	firstPCs := map[uintptr]bool{}
	for _, v := range kept {
		for _, pc := range methodPCs(reflect.TypeOf(v)) {
			firstPCs[pc] = true
		}
		for _, pc := range methodPCs(reflect.PointerTo(reflect.TypeOf(v))) {
			firstPCs[pc] = true
		}
	}
	first.CloseAndRelease()

	if _, err := stubs.Sweep(); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	second := run(`package main

import "example.com/sink"

type T struct {
	p *int
	s string
}

func (t T) String() string { return "second " + t.s + string(rune('0'+*t.p)) }

func (t T) MarshalJSON() ([]byte, error) { return []byte(t.s), nil }

type Ts []T

func (s Ts) Len() int           { return len(s) }
func (s Ts) Less(i, j int) bool { return *s[i].p < *s[j].p }
func (s Ts) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

func main() { n := 7; sink.Put(T{&n, "x"}); sink.Put(Ts{{&n, "a"}}) }
`)
	defer second.CloseAndRelease()
	live := kept[2]
	if got := live.(fmt.Stringer).String(); got != "second x7" {
		t.Fatalf("live value: got %q", got)
	}
	// The test is only meaningful if the second program took the first one's
	// slots: the sweep made them free and a swept slot goes before a fresh one.
	if !sharesSlot(reflect.TypeOf(live), firstPCs) && !sharesSlot(reflect.PointerTo(reflect.TypeOf(live)), firstPCs) {
		sw, sl, _, _ := stubs.SweepStats()
		t.Fatalf("second program did not reuse a swept slot: first %v, live %v, sweeps %d slots %d", firstPCs, methodPCs(reflect.TypeOf(live)), sw, sl)
	}

	wantStale(t, "Stringer itab", func() { _ = asStringer.String() })
	wantStale(t, "new itab", func() { _ = stale.(interface{ String() string }).String() })
	wantStale(t, "reflect", func() { reflect.ValueOf(stale).MethodByName("String").Call(nil) })
	wantStale(t, "sort.Sort", func() { sort.Sort(asSorter) })
	wantStale(t, "sort.Sort new itab", func() {
		sort.Sort(staleSlice.(interface {
			Len() int
			Less(i, j int) bool
			Swap(i, j int)
		}))
	})
	// encoding/json (on json/v2 since Go 1.25) does not recover a panicking
	// MarshalJSON; an error return would be acceptable too.
	func() {
		defer func() {
			if p := recover(); p != nil {
				if err, _ := p.(error); !errors.Is(err, interp.ErrStaleMethodCall) {
					t.Errorf("json.Marshal(stale): panic %v, want ErrStaleMethodCall", p)
				}
			}
		}()
		if out, err := json.Marshal(stale); err == nil || strings.Contains(string(out), "x") {
			t.Errorf("json.Marshal(stale): got %q, %v; want an error or a stale-call panic", out, err)
		}
	}()
	if got := fmt.Sprint(stale); strings.Contains(got, "second") || !strings.Contains(got, "PANIC") {
		t.Errorf("fmt.Sprint(stale): got %q, want a recovered stale-call panic", got)
	}
	// The live value is untouched by all of the above.
	if got := live.(fmt.Stringer).String(); got != "second x7" {
		t.Errorf("live value after stale calls: got %q", got)
	}
}

// sharesSlot reports whether one of t's methods dispatches through a stub slot
// that pcs (the slots retired by the first program) contains.
func sharesSlot(t reflect.Type, pcs map[uintptr]bool) bool {
	for k := range t.NumMethod() {
		if pcs[t.Method(k).Func.Pointer()] {
			return true
		}
	}
	return false
}

func wantStale(t *testing.T, what string, f func()) {
	t.Helper()
	var p any
	func() {
		defer func() { p = recover() }()
		f()
	}()
	err, _ := p.(error)
	if !errors.Is(err, interp.ErrStaleMethodCall) {
		t.Errorf("%s: got panic %v, want ErrStaleMethodCall", what, p)
	}
}

func methodPCs(t reflect.Type) []uintptr {
	var out []uintptr
	for k := range t.NumMethod() {
		out = append(out, t.Method(k).Func.Pointer())
	}
	return out
}
