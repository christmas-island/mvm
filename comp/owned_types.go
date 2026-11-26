package comp

import "github.com/mvm-sh/mvm/mtype"

// OwnedTypes returns every *mtype.Type reachable from this compiler's symbol
// table and type tables, through every link a type has to another (element,
// key, base, fields, params, results, embeds, method and interface-method
// signatures, union members). Each parser builds its own *Type nodes, the
// builtins included, so no other compiler reaches them: the set is what
// process-global caches keyed by *Type may drop once this compiler's
// interpreter is gone (see derive.Forget).
func (c *Compiler) OwnedTypes() map[*mtype.Type]bool {
	seen := map[*mtype.Type]bool{}
	var stack []*mtype.Type
	push := func(t *mtype.Type) {
		if t != nil && !seen[t] {
			seen[t] = true
			stack = append(stack, t)
		}
	}
	for _, s := range c.Symbols {
		push(s.Type)
	}
	for t := range c.typeSyms {
		push(t)
	}
	for t := range c.typeIdxs {
		push(t)
	}
	for t := range c.zeroTypeIdxs {
		push(t)
	}
	for _, t := range c.zeroSlotType {
		push(t)
	}
	for _, t := range c.typeByRtype {
		push(t)
	}
	for _, t := range c.canonIface {
		push(t)
	}
	for _, p := range c.pendingTypeSlots {
		push(p.typ)
	}
	for len(stack) > 0 {
		t := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		push(t.ElemType)
		push(t.KeyType)
		push(t.Base)
		for _, f := range t.Fields {
			push(f)
		}
		for _, p := range t.Params {
			push(p)
		}
		for _, r := range t.Returns {
			push(r)
		}
		for _, e := range t.Embedded {
			push(e.Type)
		}
		for i := range t.Methods {
			push(t.Methods[i].Sig)
		}
		for i := range t.IfaceMethods {
			push(t.IfaceMethods[i].Sig)
		}
		for _, e := range t.TypeElems {
			push(e.Type)
		}
	}
	return seen
}
