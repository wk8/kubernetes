package generators

import (
	"fmt"
	"io"

	"k8s.io/gengo/generator"
	"k8s.io/gengo/namer"
	"k8s.io/gengo/types"
	"k8s.io/klog"
)

// TODO wkpo move to gengo!!
// TODO wkpo check all parameters used...?
// TODO wkpo capture panics and klog.fatal then in k8s code...?

type ConversionGenerator struct {
	generator.DefaultGen

	// context is the context with which all subsequent operations on this generator should be made with.
	// See the comment on NewConversionGenerator for more context (!).
	context *generator.Context

	// typesPackage is the package that contains the types that conversion func are going to be
	// generated for.
	typesPackage string
	// outputPackage is the package that the conversion funcs are going to be output to.
	outputPackage string
	// peerPackages are the packages that contain the peer of types in typesPacakge.
	peerPackages []string
	// manualConversionsTracker finds and caches which manually defined exist.
	manualConversionsTracker *ManualConversionsTracker

	// see comment on WithTagName
	tagName string
	// see comment on WithAdditionalConversionArguments
	additionalConversionArguments map[string]*types.Type
}

// NewConversionGenerator builds a new ConversionGenerator.
// context is the only context that this generator will allow using for all subsequent operations;
// using any other context will cause a panic.
// This is because we do need to load all packages in the context at some point, and on most
// generator callbacks below gengo does not let us return errors; hence we load all the packages we need here.
func NewConversionGenerator(context *generator.Context, outputFileName, typesPackage, outputPackage string, peerPackages []string) (*ConversionGenerator, error) {
	if err := ensurePackageInContext(context, typesPackage); err != nil {
		return nil, err
	}
	for _, peerPkg := range peerPackages {
		if err := ensurePackageInContext(context, peerPkg); err != nil {
			return nil, err
		}
	}

	return &ConversionGenerator{
		DefaultGen: generator.DefaultGen{
			OptionalName: outputFileName,
		},
		context:       context,
		typesPackage:  typesPackage,
		outputPackage: outputPackage,
		peerPackages:  peerPackages,
	}, nil
}

func ensurePackageInContext(context *generator.Context, packagePath string) error {
	if _, present := context.Universe[packagePath]; present {
		return nil
	}
	_, err := context.AddDirectory(packagePath)
	return err
}

// WithAdditionalConversionArguments allows setting the additional conversion arguments.
// Those will be added to the signature of each conversion function,
// and then passed down to conversion functions for embedded types. This allows to generate
// conversion code with additional argument, eg
//    Convert_a_X_To_b_Y(in *a.X, out *b.Y, s conversion.Scope) error
// Manually defined conversion functions will also be expected to have similar signatures.
func (g *ConversionGenerator) WithAdditionalConversionArguments(additionalConversionArguments map[string]*types.Type) *ConversionGenerator {
	g.additionalConversionArguments = additionalConversionArguments
	return g
}

// WithTagName allows setting the tag name, ie the marker that this generator
// will look for in comments on types or in doc.go.
// e.g., "<tag-name>=<peer-pkg>" in doc.go, where <peer-pkg> is the import path of the package the peer types are defined in.
// or "<tag-name>=false" in a type's comment will let conversion-gen skip that type.
func (g *ConversionGenerator) WithTagName(tagName string) *ConversionGenerator {
	g.tagName = tagName
	return g
}

// WithManualConversionsTracker allows setting the ManualConversionsTracker that this generator uses.
// This is convenient to re-use the same tracker for multiple generators, thus avoiding to re-do the
// work of looking for manual conversions in the same packages several times - which is especially
// notably for peer packages, which often are the same across multiple generators.
func (g *ConversionGenerator) WithManualConversionsTracker(tracker *ManualConversionsTracker) *ConversionGenerator {
	g.manualConversionsTracker = tracker
	return g
}

// TODO wkpo comment?
func (g *ConversionGenerator) Namers(context *generator.Context) namer.NameSystems {
	g.ensureSameContext(context)
	// TODO wkpo
	return nil
}

// TODO wkpo comment?
func (g *ConversionGenerator) Filter(context *generator.Context, t *types.Type) bool {
	g.ensureSameContext(context)

	peerType := g.getPeerTypeFor(t)
	return peerType != nil && g.convertibleOnlyWithinPackage(t, peerType)
}

// TODO wkpo comment?
func (g *ConversionGenerator) Imports(context *generator.Context) (imports []string) {
	g.ensureSameContext(context)
	// TODO wkpo
}

// TODO wkpo comment?
func (g *ConversionGenerator) GenerateType(context *generator.Context, t *types.Type, writer io.Writer) error {
	g.ensureSameContext(context)

	klog.V(5).Infof("generating for type %v", t)
	peerType := g.getPeerTypeFor(t)
	snippetWriter := generator.NewSnippetWriter(writer, g.context, "$", "$")
	g.generateConversion(t, peerType, snippetWriter)
	g.generateConversion(peerType, t, snippetWriter)
	return snippetWriter.Error()

}

// TODO wkpo comment?
func (g *ConversionGenerator) generateConversion(inType, outType *types.Type, snippetWriter *generator.SnippetWriter) {
	// function signature
	// TODO wkpo publicIT namer???
	snippetWriter.Do("func auto"+conversionFunctionNameTemplate("publicIT")+" (in *$.inType|raw$, out *$.outType|raw$",
		argsFromType(inType, outType))
	for paramName, paramType := range g.additionalConversionArguments {
		snippetWriter.Do(fmt.Sprintf(", %s $.|raw$", paramName), paramType)
	}
	snippetWriter.Do(") error {\n", nil)

	// body
	g.generateFor(inType, outType, snippetWriter)

	// close function body
	snippetWriter.Do("return nil\n", nil)
	snippetWriter.Do("}\n\n", nil)

	// TODO wkpo next from here if present??
}

// TODO more wkpo comment?
// we use the system of shadowing 'in' and 'out' so that the same code is valid
// at any nesting level. This makes the autogenerator easy to understand, and
// the compiler shouldn't care.
func (g *ConversionGenerator) generateFor(inType, outType *types.Type, snippetWriter *generator.SnippetWriter) {
	klog.V(5).Infof("generating %v -> %v", inType, outType)
	var f func(*types.Type, *types.Type, *generator.SnippetWriter)

	switch inType.Kind {
	case types.Builtin:
		f = g.doBuiltin
	case types.Map:
		f = g.doMap
	case types.Slice:
		f = g.doSlice
	case types.Struct:
		f = g.doStruct
	case types.Pointer:
		f = g.doPointer
	case types.Alias:
		f = g.doAlias
	default:
		f = g.doUnknown
	}

	f(inType, outType, snippetWriter)
}

// TODO wkpo next from here! ^ and remove all functions from conversion.go
// TODO wkpo next need errors?

func (g *ConversionGenerator) getPeerTypeFor(t *types.Type) *types.Type {
	for _, peerPkgPath := range g.peerPackages {
		peerPkg := g.context.Universe[peerPkgPath]
		if peerPkg != nil && peerPkg.Has(t.Name.Name) {
			return peerPkg.Types[t.Name.Name]
		}
	}
}

func (g *ConversionGenerator) convertibleOnlyWithinPackage(inType, outType *types.Type) bool {
	var t, other *types.Type
	if inType.Name.Package == g.typesPackage {
		t, other = inType, outType
	} else {
		t, other = outType, inType
	}

	if t.Name.Package != g.typesPackage {
		return false
	}

	if g.typeOptedOut(t) {
		klog.V(5).Infof("type %v requests no conversion generation, skipping", t)
		return false
	}

	return t.Kind == types.Struct && // TODO: Consider generating functions for other kinds too
		!namer.IsPrivateGoName(other.Name.Name) // filter out private types
}

// TODO wkpo used?
func (g *ConversionGenerator) manualConversionTracker() *ManualConversionsTracker {
	if g.manualConversionsTracker == nil {
		additionalConversionArguments := make([]*types.Type, len(g.additionalConversionArguments))
		i := 0
		for _, paramType := range g.additionalConversionArguments {
			additionalConversionArguments[i] = paramType
		}

		g.manualConversionsTracker = NewManualConversionsTracker(additionalConversionArguments...)
	}
	return g.manualConversionsTracker
}

// typeOptedOut iff type t has a comment tag of the form "<tag-name>=false" indicating that
// it's opting out of the conversion generation.
func (g *ConversionGenerator) typeOptedOut(t *types.Type) bool {
	tagVals := g.extractTag(t.CommentLines)
	if len(tagVals) > 0 {
		if tagVals[0] != "false" {
			klog.Fatalf(fmt.Sprintf("Type %v: unsupported %s value: %q", t, g.tagName, tagVals[0]))
		}
		return true
	}
	return false
}

func (g *ConversionGenerator) extractTag(comments []string) []string {
	// TODO wkpo nice! ya le meme pour doc.go? on peut pas re utiliser ca pour csi-gen plutot que du regex parsing?
	// TODO wkpo en tout cas on devrait appeler ca un tag aussi, for consistency
	if g.tagName == "" {
		return nil
	}
	return types.ExtractCommentTags("+", comments)[g.tagName]
}

func (g *ConversionGenerator) ensureSameContext(context *generator.Context) {
	if context != g.context {
		klog.Fatal("Must re-use the same context used for building the generator")
	}
}
