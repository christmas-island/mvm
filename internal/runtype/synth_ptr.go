package runtype

import (
	"errors"
)

var errNilElemType = errors.New("runtype: ReservePtrMethods: nil elem type")

// synthPtr is the container head for a synth *T.
// Layout: abiType(48) + Elem(8) + uncommon(16), then the method table.
// Uncommon at offset 56, matching runtime's PtrType + UncommonType.
type synthPtr struct {
	t    abiType
	elem *abiType
	u    abiUncommon
}
