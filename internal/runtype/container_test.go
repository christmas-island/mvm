package runtype

import (
	"fmt"
	"reflect"
	"runtime"
	"testing"
	"unsafe"
)

var capLayouts = []reflect.Type{
	reflect.TypeFor[int](),
	reflect.TypeFor[string](),
	reflect.TypeFor[[]int](),
	reflect.TypeFor[[2]*int](),
	reflect.TypeFor[map[string]int](),
	reflect.TypeFor[func(int) string](),
	reflect.TypeFor[*int](),
	reflect.TypeFor[chan int](),
	reflect.TypeFor[struct {
		A *int
		B string
	}](),
}

func methodsNamed(n int) []MethodSpec {
	ms := make([]MethodSpec, n)
	for i := range ms {
		ms[i] = sampleMethod(fmt.Sprintf("M%03d", i))
	}
	return ms
}

// checkTable verifies r's method table is exactly n entries long and sits
// where uncommon.Moff says, inside the rtype's own allocation.
func checkTable(t *testing.T, r *Reservation, n int) {
	t.Helper()
	if got := r.Cap(); got != n {
		t.Fatalf("Cap = %d, want %d", got, n)
	}
	if n == 0 {
		return
	}
	at := uintptr(unsafe.Pointer(r.u)) + uintptr(r.u.Moff)
	if got := uintptr(unsafe.Pointer(&r.m[0])); got != at {
		t.Fatalf("table at %#x, uncommon.Moff points at %#x", got, at)
	}
}

// A reservation's method table holds exactly the reserved count, for every
// kind, and Fill installs up to it and refuses more.
func TestReserveMethodsCap(t *testing.T) {
	for _, layout := range capLayouts {
		for _, n := range []int{0, 1, 3, 17, maxMethods} {
			name := fmt.Sprintf("Cap%d_%s", n, layout.Kind())
			r, err := ReserveMethodsCap(layout, name, "example.com/cap", n)
			if err != nil {
				t.Fatalf("%v cap %d: %v", layout, n, err)
			}
			checkTable(t, r, n)
			pr, err := ReservePtrMethodsCap(r.Type(), "*"+name, "example.com/cap", n)
			if err != nil {
				t.Fatalf("*%v cap %d: %v", layout, n, err)
			}
			checkTable(t, pr, n)
			if n < maxMethods {
				if err := r.Fill(methodsNamed(n + 1)); err == nil {
					t.Fatalf("%v cap %d: Fill of %d methods should error", layout, n, n+1)
				}
			}
			if n == 0 {
				continue
			}
			if err := r.Fill(methodsNamed(n)); err != nil {
				t.Fatalf("%v cap %d: Fill: %v", layout, n, err)
			}
			if err := pr.Fill(methodsNamed(n)); err != nil {
				t.Fatalf("*%v cap %d: Fill: %v", layout, n, err)
			}
			runtime.GC()
			for _, rt := range []reflect.Type{r.Type(), pr.Type()} {
				if got := rt.NumMethod(); got != n {
					t.Fatalf("%v: NumMethod = %d, want %d", rt, got, n)
				}
				if got := rt.Method(n - 1).Name; got != fmt.Sprintf("M%03d", n-1) {
					t.Fatalf("%v: last method %q", rt, got)
				}
			}
			if got, want := r.Type().String(), name; got != want {
				t.Fatalf("String = %q, want %q", got, want)
			}
			if pr.Type().Elem() != r.Type() {
				t.Fatalf("*T elem = %v, want %v", pr.Type().Elem(), r.Type())
			}
		}
	}
}

// The pointers in a container head (elem, struct fields, func in/out) are
// seen by the GC: a *T whose elem is reachable only through it, and a func
// type whose params are reachable only through it, survive collections.
func TestReserveCapHeadPointersSurviveGC(t *testing.T) {
	type pair struct{ ptr, fn reflect.Type }
	var kept []pair
	for i := range 500 {
		st := reflect.StructOf([]reflect.StructField{
			{Name: "P", Type: reflect.TypeFor[*int]()},
			{Name: fmt.Sprintf("F%d", i), Type: reflect.TypeFor[string]()},
		})
		elem, err := ReserveMethodsCap(st, fmt.Sprintf("GCElem%d", i), "example.com/gc", 1)
		if err != nil {
			t.Fatal(err)
		}
		ptr, err := ReservePtrMethodsCap(elem.Type(), fmt.Sprintf("*GCElem%d", i), "example.com/gc", 2)
		if err != nil {
			t.Fatal(err)
		}
		fn, err := ReserveMethodsCap(reflect.FuncOf([]reflect.Type{elem.Type()}, []reflect.Type{ptr.Type()}, false),
			fmt.Sprintf("GCFunc%d", i), "example.com/gc", 1)
		if err != nil {
			t.Fatal(err)
		}
		kept = append(kept, pair{ptr.Type(), fn.Type()})
	}
	for range 3 {
		runtime.GC()
	}
	for i, p := range kept {
		e := p.ptr.Elem()
		if got, want := e.String(), fmt.Sprintf("GCElem%d", i); got != want {
			t.Fatalf("elem %d: %q, want %q", i, got, want)
		}
		if got := e.Field(1).Name; got != fmt.Sprintf("F%d", i) {
			t.Fatalf("elem %d field: %q", i, got)
		}
		if p.fn.In(0) != e || p.fn.Out(0) != p.ptr {
			t.Fatalf("func %d: in %v out %v", i, p.fn.In(0), p.fn.Out(0))
		}
		_ = reflect.New(e)
	}
}
