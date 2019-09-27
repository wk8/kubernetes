package generators

import (
	"fmt"

	"k8s.io/gengo/generator"
	"k8s.io/gengo/types"
)

type conversionPair struct {
	inType  *types.Type
	outType *types.Type
}

// TODO wkpo comment
type NamedVariable struct {
	Name string
	Type *types.Type
}

func NewNamedVariable(name string, t *types.Type) NamedVariable {
	return NamedVariable{
		Name: name,
		Type: t,
	}
}

const conversionFunctionPrefix = "Convert_"

func conversionFunctionNameTemplate(namer string) string {
	return fmt.Sprintf("%s$.inType|%s$_To_$.outType|%s$", conversionFunctionPrefix, namer, namer)
}

func argsFromType(inType, outType *types.Type) generator.Args {
	return generator.Args{
		"inType":  inType,
		"outType": outType,
	}
}

// unwrapAlias recurses down aliased types to find the bedrock type.
func unwrapAlias(in *types.Type) *types.Type {
	for in.Kind == types.Alias {
		in = in.Underlying
	}
	return in
}

func findMember(t *types.Type, name string) (types.Member, bool) {
	if t.Kind != types.Struct {
		return types.Member{}, false
	}
	for _, member := range t.Members {
		if member.Name == name {
			return member, true
		}
	}
	return types.Member{}, false
}

func isFastConversion(inType, outType *types.Type) bool {
	switch inType.Kind {
	case types.Builtin:
		return true
	case types.Map, types.Slice, types.Pointer, types.Struct, types.Alias:
		return isDirectlyAssignable(inType, outType)
	default:
		return false
	}
}

// TODO wkpo j'ai fucke avec ca, used to be 2 different isDirectlyAssignable.... check i didn't break nothing?
func isDirectlyAssignable(inType, outType *types.Type) bool {
	// TODO: This should maybe check for actual assignability between the two
	// types, rather than superficial traits that happen to indicate it is
	// assignable in the ways we currently use this code.
	return inType.IsAssignable() && (inType.IsPrimitive() || isSamePackage(inType, outType)) ||
		unwrapAlias(inType) == unwrapAlias(outType)
}

func isSamePackage(inType, outType *types.Type) bool {
	return inType.Name.Package == outType.Name.Package
}
