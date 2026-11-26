package main

import (
	"fmt"
	"weak"
)

type node struct{ v int }

type cache struct {
	m map[string]weak.Pointer[node]
}

func (c *cache) get(k string) *node {
	if p, ok := c.m[k]; ok {
		if n := p.Value(); n != nil {
			return n
		}
	}
	n := &node{len(k)}
	c.m[k] = weak.Make(n)
	return n
}

func main() {
	x := new(int)
	p := weak.Make(x)
	q := weak.Make(x)
	fmt.Println(p.Value() == x, p == q, p == weak.Make(new(int)))

	var zero weak.Pointer[int]
	fmt.Println(weak.Make[int](nil) == zero, zero.Value() == nil)

	c := &cache{m: map[string]weak.Pointer[node]{}}
	a := c.get("abc")
	fmt.Println(c.get("abc") == a, a.v)
}

// Output:
// true true false
// true true
// true 3
