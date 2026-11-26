package main

import (
	"fmt"
	"slices"
)

type block struct {
	hist [8]uint32
}

func first[T any](s []T) T { return s[0] }

func id[T any](v T) T { return v }

func main() {
	var b block
	b.hist[2] = 7
	h := &b.hist
	fmt.Println(slices.Max(b.hist[:4]), slices.Max(h[:4]), slices.Max(h[1:3:4]))
	fmt.Println(first(h[2:]), id(h[2]))
	fmt.Printf("%T %T\n", id(h[:]), id(h[0]))
}

// Output:
// 7 7 7
// 7 7
// []uint32 uint32
