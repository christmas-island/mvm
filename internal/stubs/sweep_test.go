//go:build !wasm

package stubs

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/internal/runtype"
)

// fillRetirableS1 reserves a synth type over layout with one S1 method
// returning out, via FillMethodsRetirable.
func fillRetirableS1(t *testing.T, layout reflect.Type, out string) (pc uintptr, v reflect.Value, retire func()) {
	t.Helper()
	res, err := runtype.ReserveMethods(layout, "Retired", "example.com/retire")
	if err != nil {
		t.Fatal(err)
	}
	called := new(bool)
	m := Method{Name: "String", Exported: true, Sig: stringerSig, Handler: stubHandler(called, out)}
	_, retire, err = FillMethodsRetirable(res, []Method{m})
	if err != nil {
		t.Fatal(err)
	}
	v = reflect.New(res.Type()).Elem()
	return v.Type().Method(0).Func.Pointer(), v, retire
}

// callStale calls f and returns its result, or the panic value if it panicked.
func callStale(f func() string) (out string, panicked any) {
	defer func() { panicked = recover() }()
	return f(), nil
}

func wantStale(t *testing.T, what string, f func() string) {
	t.Helper()
	out, p := callStale(f)
	err, _ := p.(error)
	if !errors.Is(err, ErrStaleCall) {
		t.Errorf("%s: got result %q, panic %v; want a stale-call panic", what, out, p)
	}
}

// A retired slot serves another layout only after a sweep, and every stale
// path into it then reaches the trap, never the new method.
func TestRetiredSlotSweptForAnyLayout(t *testing.T) {
	skipWithoutSweep(t)
	t.Cleanup(discardSwept)
	small := reflect.StructOf([]reflect.StructField{{Name: "A", Type: reflect.TypeFor[int]()}})
	large := reflect.StructOf([]reflect.StructField{
		{Name: "A", Type: reflect.TypeFor[string]()},
		{Name: "B", Type: reflect.TypeFor[*int]()},
	})
	pc1, v1, retire1 := fillRetirableS1(t, small, "first")
	itabHeld := v1.Interface().(fmt.Stringer) // itab built before retirement
	boxed := v1.Interface()                   // no itab for it yet
	if got := itabHeld.String(); got != "first" {
		t.Fatalf("live: got %q", got)
	}
	retire1()

	// Retired, not swept: another layout must not get the slot.
	pc2, _, retire2 := fillRetirableS1(t, large, "unswept")
	retire2()
	if pc2 == pc1 {
		t.Fatal("unswept retired slot handed to another layout")
	}
	// The rtype's method table already points at the trap: an itab built now
	// (another interface type than fmt.Stringer's cached one) reaches it.
	wantStale(t, "new itab before sweep", func() string { return boxed.(interface{ String() string }).String() })
	// The itab built before retirement still holds the slot, whose handler is nil.
	if got := itabHeld.String(); !strings.Contains(got, "has no handler") {
		t.Errorf("old itab before sweep: got %q, want no handler", got)
	}

	n, err := Sweep()
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if n < 2 {
		t.Fatalf("Sweep made %d slots available, want >= 2", n)
	}
	used := SlotsUsedS1()
	pc3, v3, retire3 := fillRetirableS1(t, large, "second")
	defer retire3()
	if pc3 != pc1 && pc3 != pc2 {
		t.Fatalf("after sweep: got a fresh slot, want a swept one")
	}
	if SlotsUsedS1() != used {
		t.Errorf("after sweep: pool counter advanced from %d to %d", used, SlotsUsedS1())
	}
	if got := v3.Interface().(fmt.Stringer).String(); got != "second" {
		t.Errorf("new owner: got %q", got)
	}

	wantStale(t, "old itab after sweep", itabHeld.String)
	wantStale(t, "new itab after sweep", func() string { return boxed.(interface{ String() string }).String() })
	wantStale(t, "cached itab after sweep", func() string { return boxed.(fmt.Stringer).String() })
	wantStale(t, "reflect method value", func() string {
		return v1.Method(0).Call(nil)[0].String()
	})
	wantStale(t, "reflect MethodByName", func() string {
		return reflect.ValueOf(boxed).MethodByName("String").Call(nil)[0].String()
	})
	if got := fmt.Sprint(boxed); strings.Contains(got, "second") || !strings.Contains(got, "PANIC") {
		t.Errorf("fmt.Sprint of stale value: got %q, want a recovered stale-call panic", got)
	}
}

// Parking (Close) is unchanged: a parked slot never goes to the swept list.
func TestSweepIgnoresParkedSlots(t *testing.T) {
	skipWithoutSweep(t)
	t.Cleanup(discardSwept)
	layout := reflect.StructOf([]reflect.StructField{{Name: "P", Type: reflect.TypeFor[int8]()}})
	res, err := runtype.ReserveMethods(layout, "Parked", "example.com/retire")
	if err != nil {
		t.Fatal(err)
	}
	m := Method{Name: "String", Exported: true, Sig: stringerSig, Handler: stubHandler(new(bool), "p")}
	park, _, err := FillMethodsRetirable(res, []Method{m})
	if err != nil {
		t.Fatal(err)
	}
	park()
	if _, err := Sweep(); err != nil {
		t.Fatal(err)
	}
	freeSlotsS1.mu.Lock()
	defer freeSlotsS1.mu.Unlock()
	pc := reflect.New(res.Type()).Elem().Type().Method(0).Func.Pointer()
	for _, n := range freeSlotsS1.swept {
		if stubsS1[n] == pc {
			t.Fatal("parked slot was swept")
		}
	}
}

func TestRuntimeItabsSelfCheck(t *testing.T) {
	skipWithoutSweep(t)
	tabs, err := runtimeItabs()
	if err != nil {
		t.Fatal(err)
	}
	if len(tabs) == 0 {
		t.Fatal("no itabs")
	}
}

// discardSwept drops every swept slot, so later tests see only fresh slots.
func discardSwept() {
	for _, f := range registeredPools() {
		f.mu.Lock()
		f.nfree.Add(-int32(len(f.swept)))
		f.swept = nil
		f.mu.Unlock()
	}
}

func skipWithoutSweep(t *testing.T) {
	t.Helper()
	if err := SweepAvailable(); err != nil {
		t.Skip(err)
	}
}

// Without a sweep a retired slot is never handed out again.
func TestRetiredSlotUnusedWithoutSweep(t *testing.T) {
	if SweepAvailable() == nil {
		t.Skip("sweep available; covered by TestRetiredSlotSweptForAnyLayout")
	}
	layout := reflect.StructOf([]reflect.StructField{{Name: "R", Type: reflect.TypeFor[uint16]()}})
	pc1, _, retire1 := fillRetirableS1(t, layout, "a")
	retire1()
	if _, err := Sweep(); err == nil {
		t.Fatal("Sweep succeeded without a sweep")
	}
	pc2, _, retire2 := fillRetirableS1(t, layout, "b")
	defer retire2()
	if pc2 == pc1 {
		t.Fatal("retired slot reused without a sweep")
	}
}
