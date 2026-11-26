//go:build !wasm

package stubs

import (
	"reflect"

	"github.com/mvm-sh/mvm/internal/runtype"
)

// FillMethods installs methods into a reserved rtype, claiming a stub slot each.
// The returned release nils this call's slot handlers (which capture *Machine);
// callers tie it to the Machine via Interp.Close. nil only when err != nil.
func FillMethods(res *runtype.Reservation, methods []Method) (release func(), err error) {
	park, _, err := FillMethodsRetirable(res, methods)
	return park, err
}

// FillMethodsRetirable is FillMethods with two ways to give the slots back;
// call at most one, once. park nils the handlers and parks the slots for the
// same methods of layout-identical types (see slotClass). retire repoints res's
// method table to the shapes' stale-call traps, nils the handlers and retires
// the slots, which any method may take after the next Sweep. Both nil only
// when err != nil.
func FillMethodsRetirable(res *runtype.Reservation, methods []Method) (park, retire func(), err error) {
	stubs, releases, classes, err := acquireSlotsClassed(res.Type(), methods)
	if err != nil {
		return nil, nil, err
	}
	specs := make([]runtype.MethodSpec, len(methods))
	for i, m := range methods {
		specs[i] = runtype.MethodSpec{
			Name:     m.Name,
			Exported: m.Exported,
			PkgPath:  m.PkgPath,
			Sig:      m.Sig,
			StubPC:   stubs[i],
		}
	}
	if err := res.Fill(specs); err != nil {
		runReleases(releases)
		return nil, nil, err
	}
	park = func() { runReleases(releases) }
	retire = func() {
		repl := make(map[uintptr]uintptr, len(stubs))
		for i, c := range classes {
			if c.pool != nil && c.pool.trap != 0 {
				repl[stubs[i]] = c.pool.trap
			}
		}
		res.RepointMethods(repl)
		for i, c := range classes {
			// A slot whose trap is unknown stays out of every free list.
			c.retire = repl[stubs[i]] != 0
			if !c.retire {
				c.recv = nil // no key: the slot is retired for good
			}
		}
		runReleases(releases)
	}
	return park, retire, nil
}

func runReleases(releases []func()) {
	for _, r := range releases {
		r()
	}
}

// acquireSlotsClassed claims one slot per method, returning slot PCs, release
// closures and the slots' classes. Each slot is classed by recv and its method
// for later reuse (see slotClass). On mid-batch failure it releases what it
// claimed.
func acquireSlotsClassed(recv reflect.Type, methods []Method) (stubs []uintptr, releases []func(), classes []*slotClass, err error) {
	stubs = make([]uintptr, len(methods))
	releases = make([]func(), 0, len(methods))
	classes = make([]*slotClass, 0, len(methods))
	for i, m := range methods {
		c := newSlotClass(recv, m)
		pc, release, err := acquireSlot(m, c)
		if err != nil {
			runReleases(releases)
			return nil, nil, nil, err
		}
		stubs[i] = pc
		releases = append(releases, release)
		classes = append(classes, c)
	}
	return stubs, releases, classes, nil
}
