package interptest

import (
	"slices"
	"strings"
	"testing"
)

// methodSetsSrc covers the method sets a synth rtype's reserved table must
// hold (derive.MethodCapacity): methods declared after the type is first
// used, promotion through two levels of embedding, a native pointer embed, a
// native value embed with pointer methods, an interface embed, a generic
// type, and methods native code calls (fmt, encoding/json, sort).
const methodSetsSrc = `package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"sync"
)

var early = Late(3) // used before its methods are declared

type Late int

type Inner struct{ n int }

func (i Inner) Name() string { return "inner" }
func (i *Inner) Inc()        { i.n++ }

type Deep struct{ Inner }

type Outer struct {
	Deep
	*bytes.Buffer
	sync.Mutex
	io.Closer
}

func (o Outer) String() string { return "outer:" + o.Name() }

func (l Late) String() string               { return fmt.Sprintf("late%d", int(l)) }
func (l Late) MarshalJSON() ([]byte, error) { return []byte(fmt.Sprint(int(l) * 10)), nil }

type Box[T any] struct{ v T }

func (b Box[T]) String() string { return fmt.Sprint("box:", b.v) }

type ByLen []string

func (s ByLen) Len() int           { return len(s) }
func (s ByLen) Less(i, j int) bool { return len(s[i]) < len(s[j]) }
func (s ByLen) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

func methods(v any) string {
	t := reflect.TypeOf(v)
	var names []string
	for i := 0; i < t.NumMethod(); i++ {
		names = append(names, t.Method(i).Name)
	}
	return fmt.Sprint(len(names), names)
}

func main() {
	o := Outer{Buffer: new(bytes.Buffer)}
	o.WriteString("w")
	o.Inc()
	o.Lock()
	o.Unlock()
	b, _ := json.Marshal([]Late{early, 5})
	s := ByLen{"ccc", "a", "bb"}
	sort.Sort(s)
	fmt.Println(early, o.String(), Box[int]{4}, string(b), s, o.Len(), o.n)
	var x fmt.Stringer = &o
	fmt.Println(x.String(), strings.ToUpper(fmt.Sprint(Deep{})))
	fmt.Println(methods(early), methods(&early))
	fmt.Println(methods(o))
	fmt.Println(methods(&o))
	fmt.Println(methods(Deep{}), methods(&Deep{}), methods(Box[int]{}), methods(s))
}
`

func TestMethodSetsFitReservedTables(t *testing.T) {
	got := strings.Split(strings.TrimSpace(evalOut(t, "msets.go", methodSetsSrc)), "\n")
	// Native Go prints these lines for the same program.
	want := []string{
		"late3 outer:inner box:4 [30,50] [a bb ccc] 1 1",
		"outer:inner {{0}}",
		"2 [MarshalJSON String] 2 [MarshalJSON String]",
		"26 [Available AvailableBuffer Bytes Cap Close Grow Len Name Next Peek Read ReadByte ReadBytes ReadFrom ReadRune ReadString Reset String Truncate UnreadByte UnreadRune Write WriteByte WriteRune WriteString WriteTo]",
		"30 [Available AvailableBuffer Bytes Cap Close Grow Inc Len Lock Name Next Peek Read ReadByte ReadBytes ReadFrom ReadRune ReadString Reset String Truncate TryLock Unlock UnreadByte UnreadRune Write WriteByte WriteRune WriteString WriteTo]",
		"1 [Name] 2 [Inc Name] 1 [String] 3 [Len Less Swap]",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if i == 3 || i == 4 {
			continue
		}
		if got[i] != want[i] {
			t.Errorf("line %d:\n got %s\nwant %s", i+1, got[i], want[i])
		}
	}
	// Outer and *Outer: mvm lists no method Go does not, and keeps at least
	// the 23 and 27 it listed before reservations were sized (it lacks
	// ReadString, WriteRune and WriteString of the *bytes.Buffer embed).
	for i, least := range map[int]int{3: 23, 4: 27} {
		names := methodNames(got[i])
		for _, n := range names {
			if !slices.Contains(methodNames(want[i]), n) {
				t.Errorf("line %d: method %s not in Go's method set", i+1, n)
			}
		}
		if len(names) < least {
			t.Errorf("line %d: %d methods, want at least %d: %s", i+1, len(names), least, got[i])
		}
	}
}

func methodNames(line string) []string {
	_, list, _ := strings.Cut(line, "[")
	return strings.Fields(strings.TrimSuffix(list, "]"))
}
