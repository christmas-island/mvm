package interptest

import (
	"bytes"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/modfs"
	"github.com/mvm-sh/mvm/stdlib"
)

// A named interface whose method signature mentions a method-bearing type
// (whose rtype is per interpreter) must not reuse another interpreter's rtype.
const ifaceAcrossInterpsSrc = `package main

import "fmt"

type S struct{}

func (*S) m() {}

type I interface{ f(*S) }

type T struct{}

func (*T) f(*S) {}

func main() {
	var t I = &T{}
	_ = []I{t}
	fmt.Println("ok")
}
`

func runIfaceAcrossInterps(name string) (string, error) {
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	defer i.Close()
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	if _, err := i.Eval(name, ifaceAcrossInterpsSrc); err != nil {
		return "", fmt.Errorf("%w\nstderr: %s", err, stderr.String())
	}
	return stdout.String(), nil
}

func TestNamedIfaceSequentialInterpreters(t *testing.T) {
	for k := range 8 {
		got, err := runIfaceAcrossInterps("iface_seq.go")
		if err != nil {
			t.Fatalf("interpreter %d: %v", k+1, err)
		}
		if got != "ok\n" {
			t.Fatalf("interpreter %d: got %q, want %q", k+1, got, "ok\n")
		}
	}
}

func TestNamedIfaceConcurrentInterpreters(t *testing.T) {
	const n = 4
	var wg sync.WaitGroup
	errs := make([]error, n)
	for k := range n {
		wg.Go(func() {
			got, err := runIfaceAcrossInterps("iface_conc.go")
			if err == nil && got != "ok\n" {
				err = fmt.Errorf("got %q, want %q", got, "ok\n")
			}
			errs[k] = err
		})
	}
	wg.Wait()
	for k, err := range errs {
		if err != nil {
			t.Errorf("interpreter %d: %v", k+1, err)
		}
	}
}

// Two packages with the same name and the same interface declaration but
// different import paths must not share one interface rtype.
func TestNamedIfaceSamePkgNameDistinctPaths(t *testing.T) {
	shapeSrc := `package shape

type S struct{}

func (*S) m() {}

type I interface{ f(*S) }

type T struct{}

func (*T) f(*S) {}

func Wrap() []I { return []I{&T{}} }
`
	url, _ := startFakeProxy(t,
		remoteModule{
			path: "example.com/a/shape", version: "v1.0.0",
			files: map[string]string{"go.mod": "module example.com/a/shape\n", "shape.go": shapeSrc},
		},
		remoteModule{
			path: "example.com/b/shape", version: "v1.0.0",
			files: map[string]string{"go.mod": "module example.com/b/shape\n", "shape.go": shapeSrc},
		},
	)
	src := `package main

import (
	"fmt"

	ashape "example.com/a/shape"
	bshape "example.com/b/shape"
)

func main() {
	fmt.Println(len(ashape.Wrap()), len(bshape.Wrap()))
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	defer i.Close()
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	if _, err := i.Eval("main.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "1 1\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}
