package generators

import (
	"fmt"
	"strings"

	"k8s.io/gengo/generator"
	"k8s.io/gengo/namer"
	"k8s.io/gengo/types"
)

type conversionPair struct {
	inType  *types.Type
	outType *types.Type
}

// A NamedVariable represents a named variable to be rendered in snippets.
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

const (
	conversionFunctionPrefix = "Convert_"
	snippetDelimiter         = "$"
)

func conversionFunctionNameTemplate(namer string) string {
	return fmt.Sprintf("%s%s.inType|%s%s_To_%s.outType|%s%s",
		conversionFunctionPrefix, snippetDelimiter, namer, snippetDelimiter, snippetDelimiter, namer, snippetDelimiter)
}

// ConversionNamer returns a namer for conversion function names.
func ConversionNamer() *namer.NameStrategy {
	return &namer.NameStrategy{
		Join: func(pre string, in []string, post string) string {
			return strings.Join(in, "_")
		},
		PrependPackageNames: 1,
	}
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

func functionHasTag(function *types.Type, functionTagName, tagValue string) bool {
	if functionTagName == "" {
		return false
	}
	values := types.ExtractCommentTags("+", function.CommentLines)[functionTagName]
	return len(values) == 1 && values[0] == tagValue
}

func isCopyOnlyFunction(function *types.Type, functionTagName string) bool {
	return functionHasTag(function, functionTagName, "copy-only")
}
