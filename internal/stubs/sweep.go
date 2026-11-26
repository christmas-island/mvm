//go:build !wasm

package stubs

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/mvm-sh/mvm/internal/runtype"
)

// A retired slot (Interp.CloseAndRelease) may serve any method once nothing
// can reach it from the closed interpreter's values: its rtypes' method tables
// are repointed to the shape's trap when it retires, and Sweep repoints every
// runtime itab still holding its PC. The runtime keeps no other copy of a
// method PC that native code dispatches through: reflect resolves methods from
// the rtype at call time, and the type-assertion and type-switch caches hold
// itab pointers. Itabs are only reachable through the runtime's itab table,
// which no supported API exposes except runtime/debug.WriteHeapDump: it writes
// every itab's address, before any heap object, while the world is stopped.

var (
	sweepMu     sync.Mutex
	sweepCount  atomic.Uint32
	sweptSlots  atomic.Uint64
	sweepNanos  atomic.Int64
	sweepLastMu sync.Mutex
	sweepLast   error
)

// SweepStats reports how many sweeps ran, how many slots they made available,
// their total duration, and the last sweep error (nil if it succeeded).
func SweepStats() (sweeps uint32, slots uint64, total time.Duration, lastErr error) {
	sweepLastMu.Lock()
	defer sweepLastMu.Unlock()
	return sweepCount.Load(), sweptSlots.Load(), time.Duration(sweepNanos.Load()), sweepLast
}

func sweepRetired() { _, _ = Sweep() }

// Sweep makes every retired slot available to any method: it repoints each
// runtime itab entry holding a retired slot's PC to that shape's trap, then
// moves the slots to their pools' swept lists. On error nothing is swept and
// the slots stay retired (unusable, which is safe). Acquiring a slot sweeps by
// itself when a pool's fresh slots run out; Sweep is exported for tests and
// for hosts that prefer to pay its cost at a time of their choosing.
func Sweep() (int, error) {
	sweepMu.Lock()
	defer sweepMu.Unlock()
	start := time.Now()

	type snap struct {
		f  *freeSlots
		ns []uint32
	}
	var snaps []snap
	repl := map[uintptr]uintptr{}
	for _, f := range registeredPools() {
		f.mu.Lock()
		if ns := f.takeRetired(); len(ns) > 0 {
			snaps = append(snaps, snap{f, ns})
			for _, n := range ns {
				repl[f.stubs[n]] = f.trap
			}
		}
		f.mu.Unlock()
	}
	if len(snaps) == 0 {
		return 0, nil
	}
	err := repointItabs(repl)
	sweepLastMu.Lock()
	sweepLast = err
	sweepLastMu.Unlock()
	total := 0
	for _, s := range snaps {
		s.f.mu.Lock()
		if err != nil {
			// Back to retired, unkeyed: unusable, which is safe.
			if s.f.retired == nil {
				s.f.retired = map[uint32]string{}
			}
			for _, n := range s.ns {
				s.f.retired[n] = ""
			}
		} else {
			s.f.swept = append(s.f.swept, s.ns...)
			s.f.nfree.Add(int32(len(s.ns)))
			total += len(s.ns)
		}
		s.f.mu.Unlock()
	}
	if err != nil {
		return 0, err
	}
	sweepCount.Add(1)
	sweptSlots.Add(uint64(total))
	sweepNanos.Add(int64(time.Since(start)))
	return total, nil
}

func repointItabs(repl map[uintptr]uintptr) error {
	tabs, err := runtimeItabs()
	if err != nil {
		return err
	}
	for _, tab := range tabs {
		runtype.RepointItab(tab, repl)
	}
	return nil
}

// canary gives runtimeItabs a known itab to check its parse against.
type canaryT struct{ _ byte }

//go:noinline
func (*canaryT) String() string { return "canary" }

var canary fmt.Stringer = new(canaryT)

var (
	errDumpFormat = errors.New("stubs: unrecognized heap dump")
	errSweepRace  = errors.New("stubs: no itab sweep under the race detector (runtime/debug.WriteHeapDump faults on reflect-built types)")
)

// SweepAvailable reports why Sweep cannot run in this build, or nil. Without a
// sweep, slots retired by CloseAndRelease are never reused.
func SweepAvailable() error {
	if raceBuild {
		return errSweepRace
	}
	return nil
}

// Heap dump record tags (runtime/heapdump.go).
const (
	tagEOF    = 0
	tagType   = 3
	tagParams = 6
	tagItab   = 8
)

// runtimeItabs lists every itab in the runtime's itab table, from a heap dump.
// It checks the result against a known itab and fails rather than return a
// partial list.
func runtimeItabs() ([]unsafe.Pointer, error) {
	if err := SweepAvailable(); err != nil {
		return nil, err
	}
	// The dump's head (params and itab records) usually fits a socket buffer;
	// a truncated head fails to parse and the whole dump goes to a file.
	if head, ok := dumpHeadViaSocket(); ok {
		if addrs, err := parseItabs(bufio.NewReader(bytes.NewReader(head))); err == nil {
			return checkItabs(addrs)
		}
	}
	f, err := os.CreateTemp("", "mvm-itabs-*")
	if err != nil {
		return nil, fmt.Errorf("stubs: itab sweep: %w", err)
	}
	defer func() { _ = os.Remove(f.Name()) }()
	defer func() { _ = f.Close() }()
	debug.WriteHeapDump(f.Fd())
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("stubs: itab sweep: %w", err)
	}
	addrs, err := parseItabs(bufio.NewReaderSize(f, 1<<16))
	if err != nil {
		return nil, err
	}
	return checkItabs(addrs)
}

func checkItabs(addrs []uintptr) ([]unsafe.Pointer, error) {
	want := (*[2]unsafe.Pointer)(unsafe.Pointer(&canary))[0]
	wantPC := reflect.TypeOf(canary).Method(0).Func.Pointer()
	tabs := make([]unsafe.Pointer, 0, len(addrs))
	found := false
	for _, a := range addrs {
		tab := runtype.PointerFromUintptr(a)
		if tab == want {
			fns := runtype.ItabFuns(tab)
			found = len(fns) == 1 && fns[0] == wantPC
		}
		tabs = append(tabs, tab)
	}
	if !found {
		return nil, fmt.Errorf("%w: self-check itab not found", errDumpFormat)
	}
	return tabs, nil
}

// parseItabs reads the itab addresses from a heap dump: the header, the params
// record, then type and itab records until the first record of another kind.
func parseItabs(r *bufio.Reader) ([]uintptr, error) {
	const hdr = "go1.7 heap dump\n"
	h := make([]byte, len(hdr))
	if _, err := io.ReadFull(r, h); err != nil || string(h) != hdr {
		return nil, fmt.Errorf("%w: header %q", errDumpFormat, h)
	}
	var err error
	u := func() uint64 {
		if err != nil {
			return 0
		}
		var v uint64
		v, err = binary.ReadUvarint(r)
		return v
	}
	skip := func() {
		n := u()
		if err == nil {
			_, err = r.Discard(int(n))
		}
	}
	var out []uintptr
	params := false
	for {
		tag := u()
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errDumpFormat, err)
		}
		switch tag {
		case tagParams:
			u() // big-endian
			if u() != uint64(unsafe.Sizeof(uintptr(0))) {
				return nil, fmt.Errorf("%w: pointer size", errDumpFormat)
			}
			u()    // arena start
			u()    // arena end
			skip() // GOARCH
			skip() // version
			u()    // ncpu
			params = err == nil
		case tagType:
			u()    // address
			u()    // size
			skip() // name
			u()    // indirect
		case tagItab:
			tab := u()
			u() // type
			if err == nil {
				out = append(out, uintptr(tab))
			}
		default:
			if !params {
				return nil, fmt.Errorf("%w: no params record", errDumpFormat)
			}
			return out, nil
		}
	}
}
