//go:build !wasm && mvm_largepools

// Package largepools checks the mvm_largepools build profile; run it with
// go test -tags mvm_largepools. It is its own test binary so the slots it uses
// do not change the pool counters other tests watch.
package largepools

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// distinctSrc is program k: its own type names (so no two programs share a
// slot-reuse key) with String, MarshalJSON and Len/Less/Swap methods that
// native code calls, 2 slots in each of S2, S9 and S10.
func distinctSrc(k int) string {
	return strings.NewReplacer("PFX", "P"+strconv.Itoa(k), "KK", strconv.Itoa(k)).Replace(`package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
)

type PFXVal int

func (v PFXVal) String() string               { return "KK:" + strconv.Itoa(int(v)) }
func (v PFXVal) MarshalJSON() ([]byte, error) { return []byte(strconv.Itoa(int(v) * KK)), nil }

type PFXVals []PFXVal

func (s PFXVals) Len() int           { return len(s) }
func (s PFXVals) Less(i, j int) bool { return s[i] < s[j] }
func (s PFXVals) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

func main() {
	vs := PFXVals{}
	for i := 0; i < 6; i++ {
		vs = append(vs, PFXVal((i*5+KK)%11))
	}
	sort.Sort(vs)
	js, _ := json.Marshal(vs)
	fmt.Println(vs, string(js))
}
`)
}

func distinctWant(k int) string {
	vs := make([]int, 0, 6)
	for i := range 6 {
		vs = append(vs, (i*5+k)%11)
	}
	sort.Ints(vs)
	strs, js := make([]string, len(vs)), make([]string, len(vs))
	for i, v := range vs {
		strs[i] = fmt.Sprintf("%d:%d", k, v)
		js[i] = strconv.Itoa(v * k)
	}
	return fmt.Sprintf("[%s] [%s]\n", strings.Join(strs, " "), strings.Join(js, ","))
}

// With 1024-slot S2/S9/S10 pools, 500 distinct programs closed with plain
// Close load in one process (the default 256-slot pools stop at 128).
func TestLargePoolsDistinctPrograms(t *testing.T) {
	const n = 500
	for k := range n {
		var stdout, stderr bytes.Buffer
		i := interp.NewInterpreter(golang.GoSpec)
		i.ImportPackageValues(stdlib.Values)
		i.SetIO(os.Stdin, &stdout, &stderr)
		if _, err := i.Eval("distinct.go", distinctSrc(k)); err != nil {
			t.Fatalf("program %d: %v", k, err)
		}
		if got, want := stdout.String(), distinctWant(k); got != want {
			t.Fatalf("program %d: got %q, want %q", k, got, want)
		}
		i.Close()
	}
}
