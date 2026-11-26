package interptest

import (
	"bytes"
	"fmt"
	"os"
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
