package main

// Since Go 1.27 a type with more than 1024 pointer bytes ([129]P or more)
// carries an on-demand GC mask: its GCData is the address of a mask slot, not
// the mask. A synthesized array type must keep that flag, or the GC scans its
// values through the slot's bytes and frees what the elements point to: here a
// parser-style per-byte callback table of method values, and a node table.

import (
	"bytes"
	"fmt"
	"runtime"
)

type callback func(w *bytes.Buffer, prev byte, text []byte) int

type node struct{ id int }

type renderer struct {
	n         int
	callbacks [256]callback
	nodes     [200]*node
}

func (r *renderer) dash(w *bytes.Buffer, prev byte, text []byte) int {
	r.n++
	w.WriteByte('-')
	return 1
}

func newRenderer() *renderer {
	r := &renderer{}
	r.callbacks['-'] = r.dash
	r.callbacks['a'] = r.dash
	for i := range r.nodes {
		r.nodes[i] = &node{i}
	}
	return r
}

func main() {
	r := newRenderer()
	total := 0
	for i := 0; i < 2000; i++ {
		if i%10 == 0 {
			runtime.GC()
		}
		_ = make([]byte, 1<<16)
		var tmp bytes.Buffer
		total += r.callbacks['-'](&tmp, 0, []byte("x"))
		total += r.callbacks['a'](&tmp, 0, []byte("x"))
	}
	bad := 0
	for i, n := range r.nodes {
		if n.id != i {
			bad++
		}
	}
	fmt.Println(total, r.n, bad)
}

// Output:
// 4000 4000 0
