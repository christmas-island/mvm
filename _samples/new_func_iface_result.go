package main

import "fmt"

type pair struct{ a, b int }

func main() {
	a := new(func() interface{})
	b := new(func() any)
	c := new(func(interface{}) interface{})
	d := new(func() []interface{})
	e := new(func() (interface{}, error))
	f := new(func() struct{})
	g := new(func(struct{ x int }) map[string]interface{})
	h := make([]func() interface{}, 2)
	k := make(map[string]func() interface{})
	l := make(chan func() interface{}, 1)
	fmt.Println(*a == nil, *b == nil, *c == nil, *d == nil, *e == nil, *f == nil, *g == nil)
	fmt.Println(len(h), len(k), cap(l))

	*a = func() interface{} { return 1 }
	*c = func(v interface{}) interface{} { return v }
	*d = func() []interface{} { return []interface{}{"x", 2} }
	*f = func() struct{} { return struct{}{} }
	h[0] = func() interface{} { return pair{1, 2} }
	k["k"] = func() interface{} { return "v" }
	l <- func() interface{} { return 3.5 }
	fmt.Println((*a)(), (*c)("y"), (*d)(), (*f)(), h[0](), k["k"](), (<-l)())
	fmt.Printf("%T %T\n", *a, h)

	z := (func() interface{})(nil)
	fmt.Println(z == nil, func() interface{} { return "iife" }(), func() struct{} { return struct{}{} }())
}

// Output:
// true true true true true true true
// 2 0 1
// 1 y [x 2] {} {1 2} v 3.5
// func() interface {} []func() interface {}
// true iife {}
