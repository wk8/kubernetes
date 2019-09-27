package generators

import (
	"k8s.io/gengo/types"
)

// TODO wkpo comment?

// TODO wkpo unit tests?

type memoryLayoutComparator map[conversionPair]bool

func (c memoryLayoutComparator) Equal(a, b *types.Type) bool {
	// alreadyVisitedTypes holds all the types that have already been checked in the structural type recursion.
	alreadyVisitedTypes := make(map[*types.Type]bool)
	return c.cachingEqual(a, b, alreadyVisitedTypes)
}

func (c memoryLayoutComparator) cachingEqual(a, b *types.Type, alreadyVisitedTypes map[*types.Type]bool) bool {
	if a == b {
		return true
	}
	if equal, ok := c[conversionPair{a, b}]; ok {
		return equal
	}
	if equal, ok := c[conversionPair{b, a}]; ok {
		return equal
	}
	result := c.equal(a, b, alreadyVisitedTypes)
	c[conversionPair{a, b}] = result
	return result
}

func (c memoryLayoutComparator) equal(a, b *types.Type, alreadyVisitedTypes map[*types.Type]bool) bool {
	in, out := unwrapAlias(a), unwrapAlias(b)
	switch {
	case in == out:
		return true
	case in.Kind == out.Kind:
		// if the type exists already, return early to avoid recursion
		if alreadyVisitedTypes[in] {
			return true
		}
		alreadyVisitedTypes[in] = true

		switch in.Kind {
		case types.Struct:
			if len(in.Members) != len(out.Members) {
				return false
			}
			for i, inMember := range in.Members {
				outMember := out.Members[i]
				if !c.cachingEqual(inMember.Type, outMember.Type, alreadyVisitedTypes) {
					return false
				}
			}
			return true
		case types.Pointer:
			return c.cachingEqual(in.Elem, out.Elem, alreadyVisitedTypes)
		case types.Map:
			return c.cachingEqual(in.Key, out.Key, alreadyVisitedTypes) && c.cachingEqual(in.Elem, out.Elem, alreadyVisitedTypes)
		case types.Slice:
			return c.cachingEqual(in.Elem, out.Elem, alreadyVisitedTypes)
		case types.Interface:
			// TODO: determine whether the interfaces are actually equivalent - for now, they must have the
			// same type.
			return false
		case types.Builtin:
			return in.Name.Name == out.Name.Name
		}
	}
	return false
}
