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

	/* Internal state */

	// typesPackage is the package that contains the types that conversion func are going to be
	// generated for.
	typesPackage string
	// outputPackage is the package that the conversion funcs are going to be output to.
	outputPackage string
	// peerPackages are the packages that contain the peer of types in typesPacakge.
	peerPackages []string
	// manualConversionsTracker finds and caches which manually defined exist.
	manualConversionsTracker *ManualConversionsTracker
	// memoryLayoutComparator allows comparing types' memory layouts to decide whether
	// to use unsafe conversions.
	memoryLayoutComparator memoryLayoutComparator

	/* Configurable settings (through the With* methods) */

	// see comment on WithTagName
	tagName string
	// see comment on WithFunctionTagName
	functionTagName string
	// see comment on WithAdditionalConversionArguments
	additionalConversionArguments []NamedVariable
	// see comment on WithMissingFieldsHandler
	missingFieldsHandler func(inVar, outVar NamedVariable, fieldName string, sw *generator.SnippetWriter) error
	// see comment on WithInconvertibleTypesHandler
	inconvertibleTypesHandler func(inVar, outVar NamedVariable, fieldName string, sw *generator.SnippetWriter) error
	// see comment on WithUnsupportedTypesHandler
	unsupportedTypesHandler func(inVar, outVar NamedVariable, sw *generator.SnippetWriter) error
	// see comment on WithExternalConversionsHandler
	externalConversionsHandler func(inVar, outVar NamedVariable, sw *generator.SnippetWriter) error
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
func (g *ConversionGenerator) WithAdditionalConversionArguments(additionalConversionArguments ...NamedVariable) *ConversionGenerator {
	g.additionalConversionArguments = additionalConversionArguments
	return g
}

// WithTagName allows setting the tag name, ie the marker that this generator
// will look for in comments on types or in doc.go.
// * "<tag-name>=<peer-pkg>" in doc.go, where <peer-pkg> is the import path of the package the peer types are defined in.
// * "<tag-name>=false" in a type's comment will let conversion-gen skip that type.
func (g *ConversionGenerator) WithTagName(tagName string) *ConversionGenerator {
	g.tagName = tagName
	return g
}

// WithFunctionTagName allows setting the function tag name, ie the marker that this generator
// will look for in comments on functions. In a function's comments:
// * "<tag-name>=copy-only" means TODO wkpo
// * "<tag-name>=drop" means TODO wkpo
func (g *ConversionGenerator) WithFunctionTagName(functionTagName string) *ConversionGenerator {
	g.functionTagName = functionTagName
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

// WithoutUnsafeConversions allows disabling the use of unsafe conversions between types that share
// the same memory layouts.
func (g *ConversionGenerator) WithoutUnsafeConversions() *ConversionGenerator {
	g.memoryLayoutComparator = nil
	return g
}

// WithMissingFieldsHandler allows setting a callback to decide what happens when converting
// from inVar.Type to outVar.Type, and when inType.fieldName doesn't exist in outType.
// The callback can freely write into the snippet writer, at the spot in the auto-generated
// conversion function where the conversion code for that field should be.
// If the handler returns an error, the auto-generated private conversion function
// (i.e. autoConvert_a_X_To_b_Y) will still be generated, but not the public wrapper for it
// (i.e. Convert_a_X_To_b_Y).
// The handler can also choose to panic to stop the generation altogether, e.g. by calling
// klog.Fatalf.
// If this is not set, missing fields are silently ignored.
func (g *ConversionGenerator) WithMissingFieldsHandler(handler func(inVar, outVar NamedVariable, fieldName string, sw *generator.SnippetWriter) error) *ConversionGenerator {
	g.missingFieldsHandler = handler
	return g
}

// WithInconvertibleTypesHandler allows setting a callback to decide what happens when converting
// from inVar.Type to outVar.Type, and when inVar.Type.fieldName and outVar.Type.fieldName are of
// inconvertible types.
// Same as for other handlers, the callback can freely write into the snippet writer, at the spot in
// the auto-generated conversion function where the conversion code for that field should be.
// If the handler returns an error, the auto-generated private conversion function
// (i.e. autoConvert_a_X_To_b_Y) will still be generated, but not the public wrapper for it
// (i.e. Convert_a_X_To_b_Y).
// The handler can also choose to panic to stop the generation altogether, e.g. by calling
// klog.Fatalf.
// If this is not set, missing fields are silently ignored.
func (g *ConversionGenerator) WithInconvertibleTypesHandler(handler func(inVar, outVar NamedVariable, fieldName string, sw *generator.SnippetWriter) error) *ConversionGenerator {
	g.inconvertibleTypesHandler = handler
	return g
}

// WithUnsupportedTypesHandler allows setting a callback to decide what happens when converting
// from inVar.Type to outVar.Type, and this generator has no idea how to handle that conversion.
// Same as for other handlers, the callback can freely write into the snippet writer, at the spot in
// the auto-generated conversion function where the conversion code for that type should be.
// If the handler returns an error, the auto-generated private conversion function
// (i.e. autoConvert_a_X_To_b_Y) will still be generated, but not the public wrapper for it
// (i.e. Convert_a_X_To_b_Y).
// The handler can also choose to panic to stop the generation altogether, e.g. by calling
// klog.Fatalf.
// If this is not set, missing fields are silently ignored.
func (g *ConversionGenerator) WithUnsupportedTypesHandler(handler func(inVar, outVar NamedVariable, sw *generator.SnippetWriter) error) *ConversionGenerator {
	g.unsupportedTypesHandler = handler
	return g
}

// TODO wkpo next from here
// WithExternalConversionsHandler allows setting a callback to decide what happens when converting
// from inVar.Type to outVar.Type, but outVar.Type is in a different package than inVar.Type - and so
// this generator can't know where to find a conversion function for that.
// Same as for other handlers, the callback can freely write into the snippet writer, at the spot in
// the auto-generated conversion function where the conversion code for that type should be.
// If the handler returns an error, the auto-generated private conversion function
// (i.e. autoConvert_a_X_To_b_Y) will still be generated, but not the public wrapper for it
// (i.e. Convert_a_X_To_b_Y).
// The handler can also choose to panic to stop the generation altogether, e.g. by calling
// klog.Fatalf.
// If this is not set, missing fields are silently ignored.
func (g *ConversionGenerator) WithExternalConversionsHandler(handler func(inVar, outVar NamedVariable, sw *generator.SnippetWriter) error) *ConversionGenerator {
	g.externalConversionsHandler = handler
	return g
}

// TODO wkpo!!!
func (g *ConversionGenerator) WithExternalPackageConversionHandler() *ConversionGenerator {
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
	sw := generator.NewSnippetWriter(writer, g.context, "$", "$")
	g.generateConversion(t, peerType, sw)
	g.generateConversion(peerType, t, sw)
	return sw.Error()

}

// TODO wkpo comment?
func (g *ConversionGenerator) generateConversion(inType, outType *types.Type, sw *generator.SnippetWriter) {
	// function signature
	// TODO wkpo publicIT namer???
	sw.Do("func auto", nil)
	g.writeConversionFunctionSignature(inType, outType, sw, true)
	sw.Do(" {\n", nil)

	// body
	errors := g.generateFor(inType, outType, sw)

	// close function body
	sw.Do("return nil\n", nil)
	sw.Do("}\n\n", nil)

	// TODO wkpo next from here if present??
	if _, found := g.preexists(inType, outType); found {
		// there is a public manual Conversion method: use it.
		return
	}

	if len(errors) == 0 {
		// Emit a public conversion function.
		sw.Do("// "+conversionFunctionNameTemplate("publicIT")+" is an autogenerated conversion function.\nfunc", argsFromType(inType, outType))
		g.writeConversionFunctionSignature(inType, outType, sw, true)
		sw.Do(" {\n", nil)
		sw.Do("return auto", nil) // TODO wkpo consolidate ^ ?
		g.writeConversionFunctionSignature(inType, outType, sw, false)
		sw.Do("\n}\n\n", nil)
		return
	}

	// there were errors generating the private conversion function
	klog.Errorf("Warning: could not find nor generate a final Conversion function for %v -> %v", inType, outType)
	klog.Errorf("  you need to add manual conversions:")
	for _, err := range errors {
		klog.Errorf("      - %v", err)
	}
}

func (g *ConversionGenerator) writeConversionFunctionSignature(inType, outType *types.Type, sw *generator.SnippetWriter, includeTypes bool) {
	args := argsFromType(inType, outType)
	sw.Do(conversionFunctionNameTemplate("publicIT"), args)
	sw.Do(" (in", nil)
	if includeTypes {
		sw.Do(" *$.inType|raw$", args)
	}
	sw.Do(", out", nil)
	if includeTypes {
		sw.Do(" *$.outType|raw$", args)
	}
	for _, namedArgument := range g.additionalConversionArguments {
		sw.Do(fmt.Sprintf(", %s", namedArgument.Name), nil)
		if includeTypes {
			sw.Do(" $.|raw$", namedArgument.Type)
		}
	}
	sw.Do(")", nil)
	if includeTypes {
		sw.Do(" error", nil)
	}
}

// TODO wkpo next from here ^!!

// TODO more wkpo comment?
// we use the system of shadowing 'in' and 'out' so that the same code is valid
// at any nesting level. This makes the autogenerator easy to understand, and
// the compiler shouldn't care.
func (g *ConversionGenerator) generateFor(inType, outType *types.Type, sw *generator.SnippetWriter) []error {
	klog.V(5).Infof("generating %v -> %v", inType, outType)
	var f func(*types.Type, *types.Type, *generator.SnippetWriter) []error

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

	return f(inType, outType, sw)
}

// TODO wkpo replace all sw s with textmate

func (g *ConversionGenerator) doBuiltin(inType, outType *types.Type, sw *generator.SnippetWriter) []error {
	if inType == outType {
		sw.Do("*out = *in\n", nil)
	} else {
		sw.Do("*out = $.|raw$(*in)\n", outType)
	}
	return nil
}

func (g *ConversionGenerator) doMap(inType, outType *types.Type, sw *generator.SnippetWriter) (errors []error) {
	sw.Do("*out = make($.|raw$, len(*in))\n", outType)
	if isDirectlyAssignable(inType.Key, outType.Key) {
		sw.Do("for key, val := range *in {\n", nil)
		if isDirectlyAssignable(inType.Elem, outType.Elem) {
			if inType.Key == outType.Key {
				sw.Do("(*out)[key] = ", nil)
			} else {
				sw.Do("(*out)[$.|raw$(key)] = ", outType.Key)
			}
			if inType.Elem == outType.Elem {
				sw.Do("val\n", nil)
			} else {
				sw.Do("$.|raw$(val)\n", outType.Elem)
			}
		} else {
			sw.Do("newVal := new($.|raw$)\n", outType.Elem)

			manualOrInternal := false

			if function, ok := g.preexists(inType.Elem, outType.Elem); ok {
				manualOrInternal = true
				sw.Do("if err := $.|raw$(&val, newVal, s); err != nil {\n", function)
			} else if g.convertibleOnlyWithinPackage(inType.Elem, outType.Elem) {
				manualOrInternal = true
				sw.Do("if err := "+conversionFunctionNameTemplate("publicIT")+"(&val, newVal, s); err != nil {\n",
					argsFromType(inType.Elem, outType.Elem))
			}

			if manualOrInternal {
				sw.Do("return err\n", nil) // TODO wkpo consolidate below?
				sw.Do("}\n", nil)
			} else if g.externalConversionsHandler == nil {
				klog.Warningf("%s's values of type %s require manual conversion to external type %s",
					inType.Name, inType.Elem, outType.Name)
			} else if err := g.externalConversionsHandler(NewNamedVariable("&val", inType.Elem), NewNamedVariable("newVal", outType.Elem), sw); err != nil {
				errors = append(errors, err)
			}

			if inType.Key == outType.Key {
				sw.Do("(*out)[key] = *newVal\n", nil)
			} else {
				sw.Do("(*out)[$.|raw$(key)] = *newVal\n", outType.Key)
			}
		}
	} else {
		// TODO: Implement it when necessary.
		sw.Do("for range *in {\n", nil)
		sw.Do("// FIXME: Converting unassignable keys unsupported $.|raw$\n", inType.Key)
	}
	sw.Do("}\n", nil)

	return
}

func (g *ConversionGenerator) doSlice(inType, outType *types.Type, sw *generator.SnippetWriter) (errors []error) {
	sw.Do("*out = make($.|raw$, len(*in))\n", outType)
	if inType.Elem == outType.Elem && inType.Elem.Kind == types.Builtin {
		sw.Do("copy(*out, *in)\n", nil)
	} else {
		sw.Do("for i := range *in {\n", nil)
		if isDirectlyAssignable(inType.Elem, outType.Elem) {
			if inType.Elem == outType.Elem {
				sw.Do("(*out)[i] = (*in)[i]\n", nil)
			} else {
				sw.Do("(*out)[i] = $.|raw$((*in)[i])\n", outType.Elem)
			}
		} else {
			manualOrInternal := false

			if function, ok := g.preexists(inType.Elem, outType.Elem); ok {
				manualOrInternal = true
				sw.Do("if err := $.|raw$(&(*in)[i], &(*out)[i], s); err != nil {\n", function)
			} else if g.convertibleOnlyWithinPackage(inType.Elem, outType.Elem) {
				manualOrInternal = true
				sw.Do("if err := "+conversionFunctionNameTemplate("publicIT")+"(&(*in)[i], &(*out)[i], s); err != nil {\n",
					argsFromType(inType.Elem, outType.Elem))
			}

			if manualOrInternal {
				sw.Do("return err\n", nil) // TODO wkpo consolidate below?
				sw.Do("}\n", nil)
			} else if g.externalConversionsHandler == nil {
				klog.Warningf("%s's items of type %s require manual conversion to external type %s",
					inType.Name, inType.Name, outType.Name)
			} else if err := g.externalConversionsHandler(NewNamedVariable("&(*in)[i]", inType.Elem), NewNamedVariable("&(*out)[i]", outType.Elem), sw); err != nil {
				errors = append(errors, err)
			}
		}
		sw.Do("}\n", nil)
	}
	return
}

func (g *ConversionGenerator) doStruct(inType, outType *types.Type, sw *generator.SnippetWriter) (errors []error) {
	for _, inMember := range inType.Members {
		if g.optedOut(inMember) {
			// This field is excluded from conversion.
			sw.Do("// INFO: in."+inMember.Name+" opted out of conversion generation\n", nil)
			continue
		}
		outMember, found := findMember(outType, inMember.Name)
		if !found {
			// This field doesn't exist in the peer.
			if g.missingFieldsHandler == nil {
				klog.Warningf("%s.%s requires manual conversion: does not exist in peer-type %s", inType.Name, inMember.Name, outType.Name)
			} else if err := g.missingFieldsHandler(NewNamedVariable("in", inType), NewNamedVariable("out", outType), inMember.Name, sw); err != nil {
				errors = append(errors, err)
			}
			continue
		}

		inMemberType, outMemberType := inMember.Type, outMember.Type
		// create a copy of both underlying types but give them the top level alias name (since aliases
		// are assignable)
		if underlying := unwrapAlias(inMemberType); underlying != inMemberType {
			copied := *underlying
			copied.Name = inMemberType.Name
			inMemberType = &copied
		}
		if underlying := unwrapAlias(outMemberType); underlying != outMemberType {
			copied := *underlying
			copied.Name = outMemberType.Name
			outMemberType = &copied
		}

		args := argsFromType(inMemberType, outMemberType).With("name", inMember.Name)

		// try a direct memory copy for any type that has exactly equivalent values
		if g.sameMemoryLayout(inMemberType, outMemberType) {
			args = args.With("Pointer", types.Ref("unsafe", "Pointer"))
			switch inMemberType.Kind {
			case types.Pointer:
				sw.Do("out.$.name$ = ($.outType|raw$)($.Pointer|raw$(in.$.name$))\n", args)
				continue
			case types.Map:
				sw.Do("out.$.name$ = *(*$.outType|raw$)($.Pointer|raw$(&in.$.name$))\n", args)
				continue
			case types.Slice:
				sw.Do("out.$.name$ = *(*$.outType|raw$)($.Pointer|raw$(&in.$.name$))\n", args)
				continue
			}
		}

		// check based on the top level name, not the underlying names
		if function, ok := g.preexists(inMember.Type, outMember.Type); ok {
			if g.functionHasTag(function, "drop") {
				continue
			}
			// copy-only functions that are directly assignable can be inlined instead of invoked.
			// As an example, conversion functions exist that allow types with private fields to be
			// correctly copied between types. These functions are equivalent to a memory assignment,
			// and are necessary for the reflection path, but should not block memory conversion.
			// Convert_unversioned_Time_to_unversioned_Time is an example of this logic.
			if !g.functionHasTag(function, "copy-only") || !isFastConversion(inMemberType, outMemberType) {
				args["function"] = function
				sw.Do("if err := $.function|raw$(&in.$.name$, &out.$.name$, s); err != nil {\n", args)
				sw.Do("return err\n", nil)
				sw.Do("}\n", nil)
				continue
			}
			klog.V(5).Infof("Skipped function %s because it is copy-only and we can use direct assignment", function.Name)
		}

		// If we can't auto-convert, punt before we emit any code.
		if inMemberType.Kind != outMemberType.Kind {
			if g.inconvertibleTypesHandler == nil {
				klog.Warningf("%s.%s requires manual conversion: inconvertible types: %s VS %s for %s.%s",
					inType.Name, inMember.Name, inMemberType, outMemberType, outType.Name, outMember.Name)
			} else if err := g.inconvertibleTypesHandler(NewNamedVariable("in", inType), NewNamedVariable("out", outType), inMember.Name, sw); err != nil {
				errors = append(errors, err)
			}
			continue
		}

		switch inMemberType.Kind {
		case types.Builtin:
			if inMemberType == outMemberType {
				sw.Do("out.$.name$ = in.$.name$\n", args)
			} else {
				sw.Do("out.$.name$ = $.outType|raw$(in.$.name$)\n", args)
			}
		case types.Map, types.Slice, types.Pointer:
			if isDirectlyAssignable(inMemberType, outMemberType) {
				sw.Do("out.$.name$ = in.$.name$\n", args)
				continue
			}

			sw.Do("if in.$.name$ != nil {\n", args)
			sw.Do("in, out := &in.$.name$, &out.$.name$\n", args)
			g.generateFor(inMemberType, outMemberType, sw)
			sw.Do("} else {\n", nil)
			sw.Do("out.$.name$ = nil\n", args)
			sw.Do("}\n", nil)
		case types.Struct:
			if isDirectlyAssignable(inMemberType, outMemberType) {
				sw.Do("out.$.name$ = in.$.name$\n", args)
				continue
			}
			if g.convertibleOnlyWithinPackage(inMemberType, outMemberType) {
				sw.Do("if err := "+conversionFunctionNameTemplate("publicIT")+"(&in.$.name$, &out.$.name$, s); err != nil {\n", args)
				sw.Do("return err\n", nil)
				sw.Do("}\n", nil)
				// TODO wkpo consolidate ^ ?
			} else {
				errors = g.callExternalConversionsHandlerForStructField(inType, outType, inMemberType, outMemberType, &inMember, &outMember, sw, errors)
			}
		case types.Alias:
			if isDirectlyAssignable(inMemberType, outMemberType) {
				if inMemberType == outMemberType {
					sw.Do("out.$.name$ = in.$.name$\n", args)
				} else {
					sw.Do("out.$.name$ = $.outType|raw$(in.$.name$)\n", args)
				}
			} else {
				if g.convertibleOnlyWithinPackage(inMemberType, outMemberType) {
					sw.Do("if err := "+conversionFunctionNameTemplate("publicIT")+"(&in.$.name$, &out.$.name$, s); err != nil {\n", args)
					sw.Do("return err\n", nil)
					sw.Do("}\n", nil)
					// TODO wkpo consolidate ^ ?
				} else {
					errors = g.callExternalConversionsHandlerForStructField(inType, outType, inMemberType, outMemberType, &inMember, &outMember, sw, errors)
				}
			}
		default:
			if g.convertibleOnlyWithinPackage(inMemberType, outMemberType) {
				sw.Do("if err := "+conversionFunctionNameTemplate("publicIT")+"(&in.$.name$, &out.$.name$, s); err != nil {\n", args)
				sw.Do("return err\n", nil)
				sw.Do("}\n", nil)
				// TODO wkpo consolidate ^ ?
			} else {
				errors = g.callExternalConversionsHandlerForStructField(inType, outType, inMemberType, outMemberType, &inMember, &outMember, sw, errors)
			}
		}
	}
	return
}

func (g *ConversionGenerator) callExternalConversionsHandlerForStructField(inType, outType, inMemberType, outMemberType *types.Type, inMember, outMember *types.Member, sw *generator.SnippetWriter, errors []error) []error {
	if g.externalConversionsHandler == nil {
		klog.Warningf("%s.%s requires manual conversion to external type %s.%s",
			inType.Name, inMember.Name, outType.Name, outMember.Name)
	} else {
		inVar := NewNamedVariable(fmt.Sprintf("&in.%s", inMember.Name), inMemberType)
		outVar := NewNamedVariable(fmt.Sprintf("&out.%s", outMember.Name), outMemberType)
		if err := g.externalConversionsHandler(inVar, outVar, sw); err != nil {
			errors = append(errors, err)
		}
	}
	return errors
}

func (g *ConversionGenerator) doPointer(inType, outType *types.Type, sw *generator.SnippetWriter) (errors []error) {
	sw.Do("*out = new($.Elem|raw$)\n", outType)
	if isDirectlyAssignable(inType.Elem, outType.Elem) {
		if inType.Elem == outType.Elem {
			sw.Do("**out = **in\n", nil)
		} else {
			sw.Do("**out = $.|raw$(**in)\n", outType.Elem)
		}
	} else {
		manualOrInternal := false

		if function, ok := g.preexists(inType.Elem, outType.Elem); ok {
			manualOrInternal = true
			sw.Do("if err := $.|raw$(*in, *out, s); err != nil {\n", function)
		} else if g.convertibleOnlyWithinPackage(inType.Elem, outType.Elem) {
			manualOrInternal = true
			sw.Do("if err := "+conversionFunctionNameTemplate("publicIT")+"(*in, *out, s); err != nil {\n", argsFromType(inType.Elem, outType.Elem))
		}

		if manualOrInternal {
			sw.Do("return err\n", nil)
			sw.Do("}\n", nil)
			// TODO wkpo consolidate ^ ?
		} else if g.externalConversionsHandler == nil {
			klog.Warningf("%s's values of type %s require manual conversion to external type %s",
				inType.Name, inType.Elem, outType.Name)
		} else if err := g.externalConversionsHandler(NewNamedVariable("*in", inType), NewNamedVariable("*out", outType), sw); err != nil {
			errors = append(errors, err)
		}
	}
	return
}

func (g *ConversionGenerator) doAlias(inType, outType *types.Type, sw *generator.SnippetWriter) []error {
	// TODO: Add support for aliases.
	return g.doUnknown(inType, outType, sw)
}

func (g *ConversionGenerator) doUnknown(inType, outType *types.Type, sw *generator.SnippetWriter) []error {
	if g.unsupportedTypesHandler == nil {
		klog.Warningf("Don't know how to convert %s to %s", inType.Name, outType.Name)
	} else if err := g.unsupportedTypesHandler(NewNamedVariable("in", inType), NewNamedVariable("out", outType), sw); err != nil {
		return []error{err}
	}
	return nil
}

func (g *ConversionGenerator) getPeerTypeFor(t *types.Type) *types.Type {
	for _, peerPkgPath := range g.peerPackages {
		peerPkg := g.context.Universe[peerPkgPath]
		if peerPkg != nil && peerPkg.Has(t.Name.Name) {
			return peerPkg.Types[t.Name.Name]
		}
	}
	return nil
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

	if g.optedOut(t) {
		klog.V(5).Infof("type %v requests no conversion generation, skipping", t)
		return false
	}

	return t.Kind == types.Struct && // TODO: Consider generating functions for other kinds too
		!namer.IsPrivateGoName(other.Name.Name) // filter out private types
}

// optedOut returns true iff type (or member) t has a comment tag of the form "<tag-name>=false"
// indicating that it's opting out of the conversion generation.
func (g *ConversionGenerator) optedOut(t interface{}) bool {
	var commentLines []string
	switch in := t.(type) {
	case *types.Type:
		commentLines = in.CommentLines
	case types.Member:
		commentLines = in.CommentLines
	default:
		klog.Fatalf("don't know how to extract comment lines from %#v", t)
	}

	tagVals := g.extractTag(commentLines)
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

func (g *ConversionGenerator) functionHasTag(function *types.Type, tagValue string) bool {
	if g.functionTagName == "" {
		return false
	}
	values := types.ExtractCommentTags("+", function.CommentLines)[g.functionTagName]
	return len(values) == 1 && values[0] == tagValue
}

func (g *ConversionGenerator) ensureSameContext(context *generator.Context) {
	if context != g.context {
		klog.Fatal("Must re-use the same context used for building the generator")
	}
}

func (g *ConversionGenerator) preexists(inType, outType *types.Type) (*types.Type, bool) {
	return g.getManualConversionTracker().preexists(inType, outType)
}

// TODO wkpo comment
func (g *ConversionGenerator) getManualConversionTracker() *ManualConversionsTracker {
	if g.manualConversionsTracker == nil {
		additionalConversionArguments := make([]*types.Type, len(g.additionalConversionArguments))
		i := 0
		for _, namedArgument := range g.additionalConversionArguments {
			additionalConversionArguments[i] = namedArgument.Type
		}

		g.manualConversionsTracker = NewManualConversionsTracker(additionalConversionArguments...)
	}
	return g.manualConversionsTracker
}

func (g *ConversionGenerator) sameMemoryLayout(t1, t2 *types.Type) bool {
	return g.memoryLayoutComparator != nil && g.memoryLayoutComparator.Equal(t1, t2)
}
