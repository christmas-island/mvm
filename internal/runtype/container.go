package runtype

import (
	"reflect"
	"sync"
	"unsafe"
)

// containerKey names a container type: a kind's head and its method count.
type containerKey struct {
	head reflect.Type
	n    int
}

// containerTypes caches struct{ H head; M [n]abiMethod } per key. At most
// (kinds) x (maxMethods+1) types, built once each.
var containerTypes sync.Map // containerKey -> reflect.Type

var abiMethodType = reflect.TypeFor[abiMethod]()

// newContainer allocates a synth rtype container: the head H followed, in the
// same allocation, by an n-entry method table. The allocation is typed (a
// reflect.StructOf of the two parts), so the GC sees the head's pointers and
// a pointer-free table; n == 0 allocates the head alone. moff is the table's
// offset from the head's uncommon struct at uoff (uncommon.Moff).
func newContainer[H any](n int, uoff uintptr) (h *H, m []abiMethod, moff uint32) {
	head := reflect.TypeFor[H]()
	if n == 0 {
		return new(H), nil, uint32(head.Size() - uoff)
	}
	key := containerKey{head, n}
	ct, ok := containerTypes.Load(key)
	if !ok {
		ct, _ = containerTypes.LoadOrStore(key, reflect.StructOf([]reflect.StructField{
			{Name: "H", Type: head},
			{Name: "M", Type: reflect.ArrayOf(n, abiMethodType)},
		}))
	}
	t := ct.(reflect.Type)
	p := reflect.New(t).UnsafePointer()
	off := t.Field(1).Offset
	return (*H)(p), unsafe.Slice((*abiMethod)(unsafe.Add(p, off)), n), uint32(off - uoff)
}
