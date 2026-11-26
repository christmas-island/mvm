package runtype

import (
	"reflect"
	"runtime"
	"sync/atomic"
	"testing"
	"unsafe"
)

// Array lengths straddling both on-demand GC mask thresholds: above 128
// pointers since Go 1.27 (abi.MaxPtrmaskBytes 16), above 16384 before.
var gcMaskLens = []int{4, 128, 129, 200, 20000}

// gcBox is a pointee the tiny allocator will not pack with other objects.
type gcBox struct {
	v int
	_ *int
}

func gcMaskOnDemand(t reflect.Type) bool {
	return rtypePtr(t).TFlag&tflagGCMaskOnDemand != 0
}

// synthPtrStruct is a methodless synth struct holding one pointer.
func synthPtrStruct(t *testing.T, name string) reflect.Type {
	t.Helper()
	res, err := ReserveMethods(reflect.StructOf([]reflect.StructField{
		{Name: "P", Type: reflect.TypeFor[*gcBox]()},
	}), name, "test")
	if err != nil {
		t.Fatalf("ReserveMethods(%q): %v", name, err)
	}
	return res.Type()
}

// checkArrayScanned fills a value of the array type at with fresh pointers,
// collects, and reports any pointee the GC freed while the array held it.
func checkArrayScanned(t *testing.T, at reflect.Type) {
	t.Helper()
	n := at.Len()
	arr := reflect.New(at)
	freed := new(atomic.Int32)
	for i := range n {
		p := &gcBox{v: i}
		runtime.AddCleanup(p, func(c *atomic.Int32) { c.Add(1) }, freed)
		arr.Elem().Index(i).Field(0).Set(reflect.ValueOf(p))
	}
	for range 3 {
		runtime.GC()
	}
	runtime.KeepAlive(arr)
	// Cleanups run on their own goroutine; give it a chance before counting.
	for range 3 {
		runtime.Gosched()
		runtime.GC()
	}
	if f := freed.Load(); f != 0 {
		t.Fatalf("%v: GC freed %d of %d pointees still held by the array", at, f, n)
	}
	for i := range n {
		if got := arr.Elem().Index(i).Field(0).Interface().(*gcBox).v; got != i {
			t.Fatalf("%v: element %d = %d", at, i, got)
		}
	}
	runtime.KeepAlive(arr)
}

func TestArrayOfKeepsGCMaskOnDemand(t *testing.T) {
	elem := synthPtrStruct(t, "GCMaskElem")
	for _, n := range gcMaskLens {
		at := ArrayOf(n, elem)
		want := gcMaskOnDemand(reflect.ArrayOf(n, layoutFor(elem)))
		if got := gcMaskOnDemand(at); got != want {
			t.Errorf("ArrayOf(%d): GCMaskOnDemand = %v, layout has %v", n, got, want)
		}
		if rtypePtr(at).GCData != rtypePtr(layoutFor(at)).GCData {
			t.Errorf("ArrayOf(%d): GCData differs from its layout shadow", n)
		}
		checkArrayScanned(t, at)
	}
}

func TestRebuildArrayGeometryKeepsGCMaskOnDemand(t *testing.T) {
	for _, n := range gcMaskLens {
		// A forward placeholder: no pointers until FillStructLayout grows it.
		res, err := ReserveMethods(reflect.StructOf([]reflect.StructField{
			{Name: "V", Type: reflect.TypeFor[int]()},
		}), "GCMaskFwd", "test")
		if err != nil {
			t.Fatal(err)
		}
		elem := res.Type()
		at := ArrayOf(n, elem)
		FillStructLayout(elem, reflect.StructOf([]reflect.StructField{
			{Name: "P", Type: reflect.TypeFor[*gcBox]()},
			{Name: "Q", Type: reflect.TypeFor[*gcBox]()},
		}))
		if !RebuildArrayGeometry(at) {
			t.Fatalf("RebuildArrayGeometry([%d]) reported no change", n)
		}
		want := gcMaskOnDemand(reflect.ArrayOf(n, layoutFor(elem)))
		if got := gcMaskOnDemand(at); got != want {
			t.Errorf("RebuildArrayGeometry([%d]): GCMaskOnDemand = %v, layout has %v", n, got, want)
		}
		if at.Size() != uintptr(n)*2*unsafe.Sizeof(uintptr(0)) {
			t.Fatalf("[%d]: Size = %d after rebuild", n, at.Size())
		}
		checkArrayScanned(t, at)
	}
}
