//go:build !wasm

// Package memprobe measures the memory an interpreter leaves behind after it
// is closed: it loads, runs and closes N interpreters and reports the live
// heap and RSS retained per load. It is its own test binary so the types and
// stub slots it creates do not change what other tests observe.
//
// The report is opt-in (it takes a while):
//
//	MVM_MEMPROBE=1 go test ./interptest/memprobe -run TestMemProbe -v
//
// Knobs: MVM_MEMPROBE_N (loads per workload, default 300; the module workload
// uses a tenth), MVM_MEMPROBE_CLOSE=close|release (default close),
// MVM_MEMPROBE_ONLY=six|twenty|goldmark, MVM_MEMPROBE_PROFDIR=dir (write heap
// profiles before and after the measured loads, at MemProfileRate 1; compare
// with go tool pprof -base). The module workload reads the local Go module
// cache offline and is skipped when goldmark is not there.
package memprobe

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mvm-sh/mvm/internal/derive"
	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/modfs"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

func TestMain(m *testing.M) {
	if os.Getenv("MVM_MEMPROBE_PROFDIR") != "" {
		runtime.MemProfileRate = 1
	}
	os.Exit(m.Run())
}

type workload struct {
	name   string
	src    string
	want   string // "" accepts whatever the first load printed
	remote fs.FS
}

// closeMode selects how a load ends.
type closeMode int

const (
	modeClose closeMode = iota
	modeRelease
)

func (c closeMode) String() string {
	if c == modeRelease {
		return "CloseAndRelease"
	}
	return "Close"
}

var collected atomic.Int64

// load runs one fresh interpreter over w and closes it.
func load(w *workload, mode closeMode) (string, error) {
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	runtime.AddCleanup(i, func(struct{}) { collected.Add(1) }, struct{}{})
	i.ImportPackageValues(stdlib.Values)
	if w.remote != nil {
		i.SetRemoteFS(w.remote)
	}
	i.SetIO(os.Stdin, &stdout, &stderr)
	_, err := i.Eval("prog.go", w.src)
	if mode == modeRelease {
		i.CloseAndRelease()
	} else {
		i.Close()
	}
	if err != nil {
		return "", fmt.Errorf("%w\nstderr: %s", err, stderr.String())
	}
	return stdout.String(), nil
}

// sample is the process's memory after two GCs and FreeOSMemory.
type sample struct {
	ms  runtime.MemStats
	rss uint64 // bytes; 0 if unknown
}

func settle() sample {
	// Cleanups (Close's cache pruning) run after a GC finds an interpreter
	// unreachable; give them a moment, then collect what they released.
	for range 3 {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
	runtime.GC()
	debug.FreeOSMemory()
	var s sample
	runtime.ReadMemStats(&s.ms)
	s.rss = rss()
	return s
}

// rss returns the resident set size in bytes (ps works on darwin and linux).
func rss() uint64 {
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return 0
	}
	kib, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0
	}
	return kib << 10
}

// result is the memory retained per load between two settled samples.
type result struct {
	name  string
	mode  closeMode
	loads int
	from  sample
	to    sample
}

func per(a, b uint64, n int) float64 { return (float64(b) - float64(a)) / float64(n) / 1024 }

func (r result) heapPerLoad() float64 { return per(r.from.ms.HeapAlloc, r.to.ms.HeapAlloc, r.loads) }

func (r result) rssPerLoad() float64 { return per(r.from.rss, r.to.rss, r.loads) }

// rows returns (metric, KiB per load) pairs for the report.
func (r result) rows() [][2]string {
	f, t, n := &r.from.ms, &r.to.ms, r.loads
	row := func(name string, a, b uint64) [2]string {
		return [2]string{name, fmt.Sprintf("%.1f", per(a, b, n))}
	}
	return [][2]string{
		row("RSS", r.from.rss, r.to.rss),
		row("Sys", f.Sys, t.Sys),
		row("HeapAlloc (live)", f.HeapAlloc, t.HeapAlloc),
		row("HeapInuse", f.HeapInuse, t.HeapInuse),
		row("HeapIdle", f.HeapIdle, t.HeapIdle),
		row("HeapReleased", f.HeapReleased, t.HeapReleased),
		row("HeapSys", f.HeapSys, t.HeapSys),
		row("HeapObjects (count)", f.HeapObjects*1024, t.HeapObjects*1024),
		row("StackInuse", f.StackInuse, t.StackInuse),
		row("MSpanInuse", f.MSpanInuse, t.MSpanInuse),
		row("MCacheInuse", f.MCacheInuse, t.MCacheInuse),
		row("BuckHashSys", f.BuckHashSys, t.BuckHashSys),
		row("GCSys", f.GCSys, t.GCSys),
		row("OtherSys", f.OtherSys, t.OtherSys),
	}
}

// probe loads w warm times (one-time caches, pools), settles, loads it n more
// times and settles again.
func probe(t testing.TB, w *workload, mode closeMode, warm, n int, profdir string) result {
	t.Helper()
	run := func() {
		got, err := load(w, mode)
		if err != nil {
			t.Fatalf("%s: %v", w.name, err)
		}
		if w.want == "" {
			w.want = got
		} else if got != w.want {
			t.Fatalf("%s: output %q, want %q", w.name, got, w.want)
		}
	}
	for range warm {
		run()
	}
	r := result{name: w.name, mode: mode, loads: n, from: settle()}
	writeProfile(t, profdir, w.name+"-before.pb.gz")
	for range n {
		run()
	}
	r.to = settle()
	writeProfile(t, profdir, w.name+"-after.pb.gz")
	return r
}

func writeProfile(t testing.TB, dir, name string) {
	if dir == "" {
		return
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := pprof.Lookup("heap").WriteTo(f, 0); err != nil {
		t.Fatal(err)
	}
}

// goModCacheFS returns an offline modfs over the local Go module cache, or nil
// when goldmark is not in it.
func goModCacheFS() fs.FS {
	out, err := exec.Command("go", "env", "GOMODCACHE").Output()
	if err != nil {
		return nil
	}
	dl := filepath.Join(strings.TrimSpace(string(out)), "cache", "download")
	if m, _ := filepath.Glob(filepath.Join(dl, "github.com", "yuin", "goldmark", "@v", "*.zip")); len(m) == 0 {
		return nil
	}
	return modfs.New(modfs.Options{Offline: true, ReadDirs: []string{dl}})
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

func TestMemProbe(t *testing.T) {
	if os.Getenv("MVM_MEMPROBE") == "" {
		t.Skip("set MVM_MEMPROBE=1 to run the memory probe")
	}
	n := envInt("MVM_MEMPROBE_N", 300)
	mode := modeClose
	if os.Getenv("MVM_MEMPROBE_CLOSE") == "release" {
		mode = modeRelease
	}
	profdir := os.Getenv("MVM_MEMPROBE_PROFDIR")
	only := os.Getenv("MVM_MEMPROBE_ONLY")
	works := []struct {
		w       *workload
		warm, n int
	}{
		{&workload{name: "six", src: sixMethods, want: sixMethodsOut}, 20, n},
		{&workload{name: "twenty", src: twentyMethods, want: twentyMethodsOut}, 20, n},
		{&workload{name: "goldmark", src: goldmarkGFM, remote: goModCacheFS()}, 3, max(n/10, 5)},
	}
	var results []result
	for _, x := range works {
		if only != "" && only != x.w.name {
			continue
		}
		if x.w.name == "goldmark" && x.w.remote == nil {
			t.Log("goldmark: not in the local module cache, skipped")
			continue
		}
		before := collected.Load()
		r := probe(t, x.w, mode, x.warm, x.n, profdir)
		results = append(results, r)
		res, der, sif := derive.CacheSizes()
		t.Logf("%s: %d loads (%s), %d interpreters collected; heap %.1f KiB/load, RSS %.1f KiB/load; "+
			"derive caches now %d reservations, %d derived, %d synth ifaces",
			r.name, r.loads, r.mode, collected.Load()-before, r.heapPerLoad(), r.rssPerLoad(), res, der, sif)
	}
	var b strings.Builder
	b.WriteString("\n| KiB per load |")
	for _, r := range results {
		fmt.Fprintf(&b, " %s (%d) |", r.name, r.loads)
	}
	b.WriteString("\n|---|")
	for range results {
		b.WriteString("---:|")
	}
	b.WriteString("\n")
	if len(results) > 0 {
		for k := range results[0].rows() {
			fmt.Fprintf(&b, "| %s |", results[0].rows()[k][0])
			for _, r := range results {
				fmt.Fprintf(&b, " %s |", r.rows()[k][1])
			}
			b.WriteString("\n")
		}
	}
	t.Log(b.String())
}
