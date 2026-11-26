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
	stubs, releases, err := acquireSlots(res.Type(), methods)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	return func() { runReleases(releases) }, nil
}

func runReleases(releases []func()) {
	for _, r := range releases {
		r()
	}
}

// acquireSlots claims one slot per method, returning slot PCs and release closures.
// Each slot is classed by recv and its method for later reuse (see slotClass).
// On mid-batch failure it releases what it claimed.
func acquireSlots(recv reflect.Type, methods []Method) (stubs []uintptr, releases []func(), err error) {
	stubs = make([]uintptr, len(methods))
	releases = make([]func(), 0, len(methods))
	for i, m := range methods {
		pc, release, err := acquireSlot(m, newSlotClass(recv, m))
		if err != nil {
			runReleases(releases)
			return nil, nil, err
		}
		stubs[i] = pc
		releases = append(releases, release)
	}
	return stubs, releases, nil
}
