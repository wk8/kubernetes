package generators

// TODO wkpo unit tests!!

import (
	"bytes"
	"fmt"
	"strings"

	"k8s.io/klog"

	"k8s.io/gengo/generator"
	"k8s.io/gengo/types"
)

// a ManualConversionsTracker keeps track of manually defined conversion functions.
type ManualConversionsTracker struct {
	// see the explanation on NewManualConversionsTracker.
	additionalConversionArguments []NamedVariable

	// processedPackages keeps track of which packages have already been processed, as there
	// is no need to ever process the same package twice.
	processedPackages map[string][]error

	// conversionFunctions keeps track of the manual function definitions known to this tracker.
	conversionFunctions map[conversionPair]*types.Type
}

// NewManualConversionsTracker builds a new ManualConversionsTracker.
// Additional conversion arguments allow users to set which arguments should be part of
// a conversion function signature.
// When generating conversion code, those will be added to the signature of each conversion function,
// and then passed down to conversion functions for embedded types. This allows to generate
// conversion code with additional argument, eg
//    Convert_a_X_To_b_Y(in *a.X, out *b.Y, s conversion.Scope) error
// Manually defined conversion functions will also be expected to have similar signatures.
func NewManualConversionsTracker(additionalConversionArguments ...NamedVariable) *ManualConversionsTracker {
	return &ManualConversionsTracker{
		additionalConversionArguments: additionalConversionArguments,
		processedPackages:             make(map[string][]error),
		conversionFunctions:           make(map[conversionPair]*types.Type),
	}
}

var errorName = types.Ref("", "error").Name

// findManualConversionFunctions looks for conversion functions in the given package.
// TODO wkpo log errors?
func (t *ManualConversionsTracker) findManualConversionFunctions(context *generator.Context, packagePath string) (errors []error) {
	pkg := context.Universe[packagePath]
	if pkg == nil {
		return nil
	}

	if e, present := t.processedPackages[pkg.Path]; present {
		// already processed
		return e
	}

	buffer := &bytes.Buffer{}
	sw := generator.NewSnippetWriter(buffer, context, "$", "$")

	for _, function := range pkg.Functions {
		if function.Underlying == nil || function.Underlying.Kind != types.Func {
			errors = append(errors, fmt.Errorf("malformed function: %#v", function))
			continue
		}
		if function.Underlying.Signature == nil {
			errors = append(errors, fmt.Errorf("function without signature: %#v", function))
			continue
		}

		klog.V(8).Infof("Considering function %s", function.Name)

		isConversionFunc, inType, outType := t.isConversionFunction(function, buffer, sw)
		if !isConversionFunc {
			if strings.HasPrefix(function.Name.Name, conversionFunctionPrefix) {
				errors = append(errors, fmt.Errorf("function %s %s does not match expected conversion signature",
					function.Name.Package, function.Name.Name))
			}
			continue
		}

		// it is a conversion function
		key := conversionPair{inType.Elem, outType.Elem}
		if previousConversionFunc, present := t.conversionFunctions[key]; present {
			errors = append(errors, fmt.Errorf("duplicate static conversion defined: %s -> %s from:\n%s.%s\n%s.%s",
				inType, outType, previousConversionFunc.Name.Package, previousConversionFunc.Name.Name, function.Name.Package, function.Name.Name))
			continue
		}
		t.conversionFunctions[key] = function
	}

	t.processedPackages[pkg.Path] = errors
	return
}

// isConversionFunction returns true iff the given function is a conversion function; that is of the form
// func Convert_a_X_To_b_Y(in *a.X, out *b.Y, additionalConversionArguments...) error
// If it is a signature functions, also returns the inType and outType.
func (t *ManualConversionsTracker) isConversionFunction(function *types.Type, buffer *bytes.Buffer, sw *generator.SnippetWriter) (bool, *types.Type, *types.Type) {
	signature := function.Underlying.Signature

	if signature.Receiver != nil {
		return false, nil, nil
	}
	if len(signature.Results) != 1 || signature.Results[0].Name != errorName {
		return false, nil, nil
	}
	// 2 (in and out) + additionalConversionArguments
	if len(signature.Parameters) != 2+len(t.additionalConversionArguments) {
		return false, nil, nil
	}
	inType := signature.Parameters[0]
	outType := signature.Parameters[1]
	if inType.Kind != types.Pointer || outType.Kind != types.Pointer {
		return false, nil, nil
	}
	for i, extraArg := range t.additionalConversionArguments {
		if signature.Parameters[i+2].Name != extraArg.Type.Name {
			return false, nil, nil
		}
	}

	// check it satisfies the naming convention
	// TODO: This should call the Namer directly.
	buffer.Reset()
	// TODO wkpo le namer la.... il vient d'ou? du context? si oui er... comment on s'assure que le contexte a le bon namer? peut etre en l'ajoutant aux namers du generator?
	// TODO wkpo try renaming it to wkpo
	// TODO wkpo peut etre plus propre de passer un namer directement? ou d'en construire un internally??
	sw.Do(conversionFunctionNameTemplate("public"), argsFromType(inType.Elem, outType.Elem))
	if function.Name.Name != buffer.String() {
		return false, nil, nil
	}

	return true, inType, outType
}

func (t *ManualConversionsTracker) preexists(inType, outType *types.Type) (*types.Type, bool) {
	function, ok := t.conversionFunctions[conversionPair{inType, outType}]
	return function, ok
}

func (t *ManualConversionsTracker) isEmpty() bool {
	return len(t.processedPackages) == 0
}
