package main

import (
	"bytes"
	"crypto/mlkem"
	"crypto/mlkem/mlkemtest"
	"fmt"
)

func main() {
	seed := bytes.Repeat([]byte{1}, mlkem.SeedSize)
	dk, err := mlkem.NewDecapsulationKey768(seed)
	if err != nil {
		panic(err)
	}
	random := bytes.Repeat([]byte{2}, 32)
	k1, ct1, err := mlkemtest.Encapsulate768(dk.EncapsulationKey(), random)
	if err != nil {
		panic(err)
	}
	k2, ct2, _ := mlkemtest.Encapsulate768(dk.EncapsulationKey(), random)
	shared, err := dk.Decapsulate(ct1)
	if err != nil {
		panic(err)
	}
	fmt.Println(bytes.Equal(k1, shared), bytes.Equal(k1, k2), bytes.Equal(ct1, ct2), len(ct1))
}

// Output:
// true true true 1088
