//go:build !wasm

package stubs

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/mvm-sh/mvm/internal/runtype"
)

// fillS1 reserves a synth type over layout with one S1 method and returns
// the method's stub PC, its value, and the release closure.
func fillS1(t *testing.T, layout reflect.Type, out string) (uintptr, reflect.Value, func()) {
	t.Helper()
	res, err := runtype.ReserveMethods(layout, "Recycled", "example.com/recycle")
	if err != nil {
		t.Fatal(err)
	}
	called := new(bool)
	m := Method{Name: "String", Exported: true, Sig: stringerSig, Handler: stubHandler(called, out)}
	stubs, releases, err := acquireSlots(res.Type(), []Method{m})
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Fill([]runtype.MethodSpec{{Name: m.Name, Exported: true, Sig: m.Sig, StubPC: stubs[0]}}); err != nil {
		t.Fatal(err)
	}
	v := reflect.New(res.Type()).Elem()
	return stubs[0], v, func() { runReleases(releases) }
}

func TestSlotRecycledForSameClass(t *testing.T) {
	layout := reflect.StructOf([]reflect.StructField{{Name: "A", Type: reflect.TypeFor[int]()}})
	pc1, v1, release1 := fillS1(t, layout, "first")
	if got := v1.Interface().(fmt.Stringer).String(); got != "first" {
		t.Fatalf("first: got %q", got)
	}
	release1()
	used := SlotsUsedS1()
	pc2, v2, release2 := fillS1(t, layout, "second")
	defer release2()
	if pc2 != pc1 {
		t.Errorf("same class: got a fresh slot, want the released one")
	}
	if SlotsUsedS1() != used {
		t.Errorf("same class: pool counter advanced from %d to %d", used, SlotsUsedS1())
	}
	if got := v2.Interface().(fmt.Stringer).String(); got != "second" {
		t.Errorf("second: got %q", got)
	}
}

func TestSlotNotRecycledForOtherLayout(t *testing.T) {
	small := reflect.StructOf([]reflect.StructField{{Name: "A", Type: reflect.TypeFor[int]()}})
	large := reflect.StructOf([]reflect.StructField{
		{Name: "A", Type: reflect.TypeFor[int]()},
		{Name: "B", Type: reflect.TypeFor[string]()},
	})
	pc1, _, release1 := fillS1(t, small, "small")
	release1()
	pc2, _, release2 := fillS1(t, large, "large")
	defer release2()
	if pc2 == pc1 {
		t.Error("different receiver layout reused a released slot")
	}
}

func TestSlotClassKey(t *testing.T) {
	type node struct {
		next *node
		v    int
	}
	type other struct {
		next *other
		v    int
	}
	key := func(recv reflect.Type, name string) string {
		return newSlotClass(recv, Method{Name: name, Sig: stringerSig}).getKey()
	}
	nodeT := reflect.TypeFor[*node]()
	if k := key(nodeT, "String"); k == "" || k != key(nodeT, "String") {
		t.Errorf("recursive receiver: unstable key %q", k)
	}
	if key(nodeT, "String") == key(nodeT, "Other") {
		t.Error("method name not in key")
	}
	if key(nodeT, "String") == key(reflect.TypeFor[*other](), "String") {
		t.Error("receiver name not in key")
	}
	if k := newSlotClass(nil, Method{Name: "String", Sig: stringerSig}).getKey(); k != "" {
		t.Errorf("nil receiver: got key %q, want none", k)
	}
}

// Two rtypes with one name but different layouts cannot be told apart by name,
// so a receiver reaching both gets no key and its slots are never recycled.
func TestSlotClassKeyAmbiguousName(t *testing.T) {
	dup := func(layout reflect.Type) reflect.Type {
		res, err := runtype.ReserveMethods(layout, "Dup", "example.com/recycle")
		if err != nil {
			t.Fatal(err)
		}
		return res.Type()
	}
	a := dup(reflect.TypeFor[int]())
	b := dup(reflect.TypeFor[string]())
	recv := reflect.PointerTo(reflect.StructOf([]reflect.StructField{
		{Name: "A", Type: a},
		{Name: "B", Type: b},
	}))
	if k := newSlotClass(recv, Method{Name: "String", Sig: stringerSig}).getKey(); k != "" {
		t.Errorf("ambiguous name: got key %q, want none", k)
	}
}
