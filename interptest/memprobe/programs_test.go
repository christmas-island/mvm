//go:build !wasm

package memprobe

// sixMethods has three named types and six methods that native code calls
// (fmt.Stringer, json.Marshaler, sort.Interface, error).
const sixMethods = `package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
)

type Val int

func (v Val) String() string               { return "v" + strconv.Itoa(int(v)) }
func (v Val) MarshalJSON() ([]byte, error) { return []byte(strconv.Itoa(int(v))), nil }

type Vals []Val

func (s Vals) Len() int           { return len(s) }
func (s Vals) Less(i, j int) bool { return s[i] < s[j] }
func (s Vals) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

type Err struct{ N int }

func (e Err) Error() string { return "e" + strconv.Itoa(e.N) }

func main() {
	vs := Vals{3, 1, 2}
	sort.Sort(vs)
	b, _ := json.Marshal(vs)
	fmt.Println(fmt.Sprint(vs, string(b), error(Err{1})))
}
`

const sixMethodsOut = "[v1 v2 v3][1,2,3]e1\n"

// twentyMethods has ten named types of every kind mvm reserves (int, string,
// slice, map, struct, pointer receivers) and 21 methods that native code
// calls: fmt.Stringer/Formatter/GoStringer, encoding.TextMarshaler (a map
// key), json.Marshaler/Unmarshaler, sort.Interface twice, heap.Interface,
// error, io.Writer and io.Reader.
const twentyMethods = `package main

import (
	"container/heap"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

type Num int

func (n Num) String() string               { return "#" + strconv.Itoa(int(n)) }
func (n Num) MarshalText() ([]byte, error) { return []byte(n.String()), nil }

type Nums []Num

func (s Nums) Len() int           { return len(s) }
func (s Nums) Less(i, j int) bool { return s[i] < s[j] }
func (s Nums) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

type ByMod []int

func (s ByMod) Len() int { return len(s) }
func (s ByMod) Less(i, j int) bool {
	if s[i]%7 != s[j]%7 {
		return s[i]%7 < s[j]%7
	}
	return s[i] < s[j]
}
func (s ByMod) Swap(i, j int) { s[i], s[j] = s[j], s[i] }

type Point struct{ X, Y int }

func (p Point) String() string { return fmt.Sprintf("(%d,%d)", p.X, p.Y) }
func (p Point) MarshalJSON() ([]byte, error) {
	return json.Marshal(strconv.Itoa(p.X) + "," + strconv.Itoa(p.Y))
}
func (p *Point) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	x, y, _ := strings.Cut(s, ",")
	p.X, _ = strconv.Atoi(x)
	p.Y, _ = strconv.Atoi(y)
	return nil
}

type Fault struct{ Code int }

func (e *Fault) Error() string { return "code " + strconv.Itoa(e.Code) }

type Buf struct{ b []byte }

func (w *Buf) Write(p []byte) (int, error) { w.b = append(w.b, p...); return len(p), nil }

type Upper string

func (u Upper) Format(f fmt.State, verb rune) { io.WriteString(f, strings.ToUpper(string(u))) }
func (u Upper) GoString() string              { return "Upper(" + string(u) + ")" }

type Bag map[string]int

func (b Bag) MarshalJSON() ([]byte, error) {
	keys := make([]string, 0, len(b))
	for k := range b {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + ":" + strconv.Itoa(b[k])
	}
	return json.Marshal(strings.Join(parts, ";"))
}

type Reader struct {
	s string
	i int
}

func (r *Reader) Read(p []byte) (int, error) {
	if r.i >= len(r.s) {
		return 0, io.EOF
	}
	n := copy(p[:1], r.s[r.i:])
	r.i += n
	return n, nil
}

type Heap []int

func (h Heap) Len() int           { return len(h) }
func (h Heap) Less(i, j int) bool { return h[i] < h[j] }
func (h Heap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *Heap) Push(x any)        { *h = append(*h, x.(int)) }
func (h *Heap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

func main() {
	n := 4
	nums := Nums{}
	for i := 0; i < 12; i++ {
		nums = append(nums, Num((i*7+n)%13))
	}
	sort.Sort(nums)
	bm := ByMod{}
	for i := 0; i < 10; i++ {
		bm = append(bm, (i*5+n)%17)
	}
	sort.Sort(bm)
	h := &Heap{}
	for i := 0; i < 8; i++ {
		heap.Push(h, (i*3+n)%11)
	}
	var popped []int
	for h.Len() > 0 {
		popped = append(popped, heap.Pop(h).(int))
	}
	p := Point{n, n * 2}
	js, err := json.Marshal(map[string]any{"p": p, "bag": Bag{"a": n, "b": 2 * n}, "keys": map[Num]int{Num(n): 1}})
	if err != nil {
		panic(err)
	}
	var q Point
	if err := json.Unmarshal([]byte("\"" + strconv.Itoa(n+1) + ",5\""), &q); err != nil {
		panic(err)
	}
	buf := &Buf{}
	fmt.Fprintf(buf, "%v|%s|%v|%v", nums, p, Upper("abc"), Num(n))
	var e error = &Fault{n}
	rd, _ := io.ReadAll(&Reader{s: strings.Repeat("xy", n%5+1)})
	fmt.Println(string(buf.b))
	fmt.Println(string(js), bm, popped)
	fmt.Println(fmt.Sprintf("%v", e), string(rd), fmt.Sprintf("%#v", Upper("q")), q.String())
}
`

const twentyMethodsOut = `[#0 #1 #2 #3 #4 #5 #6 #7 #8 #9 #11 #12]|(4,8)|ABC|#4
{"bag":"a:4;b:8","keys":{"#4":1},"p":"4,8"} [0 7 14 15 2 9 10 4 5 12] [0 2 3 4 5 7 8 10]
code 4 xyxyxyxyxy Q (5,5)
`

// goldmarkGFM renders GitHub-flavoured markdown with goldmark and its
// extension package, loaded from the local Go module cache.
const goldmarkGFM = `package main

import (
	"bytes"
	"fmt"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

func main() {
	src := "# T\n\n| a | b |\n|---|---|\n| 1 | ~~2~~ |\n\n- [x] done\n- see https://example.com\n"
	var buf bytes.Buffer
	if err := goldmark.New(goldmark.WithExtensions(extension.GFM)).Convert([]byte(src), &buf); err != nil {
		panic(err)
	}
	fmt.Print(buf.String())
}
`
