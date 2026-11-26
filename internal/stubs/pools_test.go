//go:build !wasm

package stubs

import "testing"

// Every pool's stub-PC array is fully populated with distinct PCs, in the
// default build and in the mvm_largepools profile (whose upper slots come
// from the pool_*_large.go files).
func TestPoolStubsPopulated(t *testing.T) {
	type pool struct {
		name  string
		stubs []uintptr
		cap   int
	}
	pools := []pool{
		{"S1", stubsS1[:], poolSizeS1},
		{"S2", stubsS2[:], poolSizeS2},
		{"S3", stubsS3[:], poolSizeS3},
		{"S4", stubsS4[:], poolSizeS4},
		{"S5", stubsS5[:], poolSizeS5},
		{"S6", stubsS6[:], poolSizeS6},
		{"S7", stubsS7[:], poolSizeS7},
		{"S8", stubsS8[:], poolSizeS8},
		{"S9", stubsS9[:], poolSizeS9},
		{"S10", stubsS10[:], poolSizeS10},
		{"S11", stubsS11[:], poolSizeS11},
		{"S12", stubsS12[:], poolSizeS12},
		{"S13", stubsS13[:], poolSizeS13},
		{"S14", stubsS14[:], poolSizeS14},
		{"S15", stubsS15[:], poolSizeS15},
		{"S16", stubsS16[:], poolSizeS16},
		{"S17", stubsS17[:], poolSizeS17},
		{"S18", stubsS18[:], poolSizeS18},
		{"S19", stubsS19[:], poolSizeS19},
		{"S20", stubsS20[:], poolSizeS20},
		{"S21", stubsS21[:], poolSizeS21},
		{"S22", stubsS22[:], poolSizeS22},
		{"S23", stubsS23[:], poolSizeS23},
		{"S24", stubsS24[:], poolSizeS24},
		{"S25", stubsS25[:], poolSizeS25},
		{"S26", stubsS26[:], poolSizeS26},
		{"S27", stubsS27[:], poolSizeS27},
		{"S28", stubsS28[:], poolSizeS28},
		{"S29", stubsS29[:], poolSizeS29},
		{"S30", stubsS30[:], poolSizeS30},
		{"S31", stubsS31[:], poolSizeS31},
		{"S32", stubsS32[:], poolSizeS32},
		{"S33", stubsS33[:], poolSizeS33},
		{"S34", stubsS34[:], poolSizeS34},
		{"S35", stubsS35[:], poolSizeS35},
		{"S36", stubsS36[:], poolSizeS36},
		{"S37", stubsS37[:], poolSizeS37},
		{"S38", stubsS38[:], poolSizeS38},
	}
	wordPools.Range(func(k, v any) bool {
		p := v.(*wordPool)
		pools = append(pools, pool{"W_" + k.(string), p.stubs, int(p.cap)})
		return true
	})
	seen := map[uintptr]string{}
	for _, p := range pools {
		if len(p.stubs) != p.cap {
			t.Errorf("%s: %d stubs, cap %d", p.name, len(p.stubs), p.cap)
		}
		for i, pc := range p.stubs {
			if pc == 0 {
				t.Errorf("%s: slot %d has no stub", p.name, i)
				break
			}
			if prev, ok := seen[pc]; ok {
				t.Errorf("%s: slot %d shares its PC with %s", p.name, i, prev)
				break
			}
			seen[pc] = p.name
		}
	}
}
