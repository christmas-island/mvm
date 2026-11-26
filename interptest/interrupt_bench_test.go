package interptest

import (
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
)

// Micro benchmarks for the hot paths the interrupt poll touches: loop
// back-edges, calls, channel operations, select and sync primitives. They
// exist to measure the poll's cost, so each one keeps its body small.

const interruptBenchSrc = `
import "sync"

func tight() int {
	s := 0
	for i := 0; i < 1000000; i++ {
		s += i
	}
	return s
}

func nested() int {
	s := 0
	for i := 0; i < 1000; i++ {
		for j := 0; j < 1000; j++ {
			if j&1 == 0 {
				s += j
			} else {
				s -= i
			}
		}
	}
	return s
}

func leaf(x int) int { return x + 1 }

func calls() int {
	s := 0
	for i := 0; i < 200000; i++ {
		s = leaf(s)
	}
	return s
}

func pingpong() int {
	a, b := make(chan int), make(chan int)
	go func() {
		for v := range a {
			b <- v + 1
		}
		close(b)
	}()
	s := 0
	for i := 0; i < 2000; i++ {
		a <- i
		v := <-b // s += <-b does not compile in mvm today
		s += v
	}
	close(a)
	return s
}

func selects() int {
	a, b := make(chan int, 1), make(chan int, 1)
	s := 0
	for i := 0; i < 2000; i++ {
		if i&1 == 0 {
			a <- i
		} else {
			b <- i
		}
		select {
		case v := <-a:
			s += v
		case v := <-b:
			s -= v
		}
	}
	return s
}

func locks() int {
	var mu sync.Mutex
	var wg sync.WaitGroup
	s := 0
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				mu.Lock()
				s++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return s
}
`

func BenchmarkInterruptPoll(b *testing.B) {
	for _, fn := range []string{"tight", "nested", "calls", "pingpong", "selects", "locks"} {
		b.Run(fn, func(b *testing.B) {
			intp := interp.NewInterpreter(golang.GoSpec)
			intp.ImportPackageValues(stdlib.Values)
			if _, err := intp.Eval("setup", interruptBenchSrc); err != nil {
				b.Fatal(err)
			}
			for b.Loop() {
				if _, err := intp.Eval("bench", fn+"()"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
