//go:build wasm

package stubs

import (
	"github.com/mvm-sh/mvm/internal/runtype"
)

// FillMethods installs methods on the all-interpreted wasm target. Every dispatch
// PC is left as the unreachable sentinel: no native caller dispatches an interpreted
// method (interpreted code uses IfaceCall; interpreted reflect is intercepted), yet
// the synth rtype still carries its method set for reflect introspection. A real PC
// must not be wired -- it enters reflect's GC-scanned reflectOffs, where a synthetic
// wasm code PC aliases a heap span and crashes the GC.
// release is always nil on wasm: no slots are claimed, nothing to free.
func FillMethods(res *runtype.Reservation, methods []Method) (release func(), err error) {
	specs := make([]runtype.MethodSpec, len(methods))
	for i, m := range methods {
		specs[i] = runtype.MethodSpec{
			Name:     m.Name,
			Exported: m.Exported,
			PkgPath:  m.PkgPath,
			Sig:      m.Sig,
		}
	}
	return nil, res.Fill(specs)
}

// FillMethodsRetirable is FillMethods on wasm: there are no slots to give back.
func FillMethodsRetirable(res *runtype.Reservation, methods []Method) (park, retire func(), err error) {
	_, err = FillMethods(res, methods)
	return nil, nil, err
}
