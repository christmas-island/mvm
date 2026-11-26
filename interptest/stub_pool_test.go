package interptest

import (
	"bytes"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
)

// manyMethodsSrc declares n types, each with one method of word shape
// pppp_ (four pointer params, no result).
func manyMethodsSrc(n int) string {
	var b strings.Builder
	b.WriteString("package main\n\nimport \"fmt\"\n\n")
	for k := range n {
		fmt.Fprintf(&b, "type t%d struct{ x int }\n\nfunc (t *t%d) M(a, b, c, d *int) { t.x++ }\n\n", k, k)
	}
	b.WriteString("func main() {\n\tvar t t0\n\tt.M(nil, nil, nil, nil)\n\tfmt.Println(t.x)\n}\n")
	return b.String()
}

func evalManyMethods(src string) error {
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	defer i.Close()
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	if _, err := i.Eval("many_methods.go", src); err != nil {
		return fmt.Errorf("%w\nstderr: %s", err, stderr.String())
	}
	if got := stdout.String(); got != "1\n" {
		return fmt.Errorf("stdout: got %q, want %q", got, "1\n")
	}
	return nil
}

// More than 64 methods of one word shape load in one interpreter
// (goldmark/extension attaches ~82 of shape pppp_).
func TestStubPoolManyMethodsOneShape(t *testing.T) {
	if err := evalManyMethods(manyMethodsSrc(100)); err != nil {
		t.Fatal(err)
	}
}

// Closed interpreters return their stub slots, so the same program loads in
// any number of sequential interpreters without exhausting a pool.
func TestStubPoolSequentialInterpreters(t *testing.T) {
	src := manyMethodsSrc(60)
	for k := range 20 {
		if err := evalManyMethods(src); err != nil {
			t.Fatalf("interpreter %d: %v", k+1, err)
		}
	}
}

// A value that outlives its interpreter keeps its rtype and stub PCs. A later
// interpreter whose same-named type has another layout must not get those
// slots, so a stale call reaches no handler instead of the new method.
func TestStubPoolStaleValueOtherLayout(t *testing.T) {
	var kept []any
	sink := map[string]map[string]reflect.Value{
		"example.com/sink": {"Put": reflect.ValueOf(func(v any) { kept = append(kept, v) })},
	}
	run := func(src string) *interp.Interp {
		t.Helper()
		var stdout, stderr bytes.Buffer
		i := interp.NewInterpreter(golang.GoSpec)
		i.ImportPackageValues(stdlib.Values)
		i.ImportPackageValues(sink)
		i.SetIO(os.Stdin, &stdout, &stderr)
		if _, err := i.Eval("stale.go", src); err != nil {
			t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
		}
		return i
	}
	run(`package main

import "example.com/sink"

type T struct{ a, b int }

func (t T) String() string { return "first" }

func main() { sink.Put(T{1, 2}) }
`).Close()
	second := run(`package main

import "example.com/sink"

type T struct {
	p *int
	s string
}

func (t T) String() string { return "second " + t.s }

func main() { sink.Put(T{s: "x"}) }
`)
	defer second.Close()
	if len(kept) != 2 {
		t.Fatalf("kept %d values, want 2", len(kept))
	}
	if got := kept[1].(fmt.Stringer).String(); got != "second x" {
		t.Errorf("live value: got %q", got)
	}
	if got := kept[0].(fmt.Stringer).String(); !strings.Contains(got, "has no handler") {
		t.Errorf("stale value of a closed interpreter: got %q, want no handler", got)
	}
}
