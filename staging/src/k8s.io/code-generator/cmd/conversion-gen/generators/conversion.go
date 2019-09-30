/*
Copyright 2016 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package generators

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"k8s.io/gengo/args"
	"k8s.io/gengo/generator"
	"k8s.io/gengo/namer"
	"k8s.io/gengo/types"

	"k8s.io/klog"

	conversionargs "k8s.io/code-generator/cmd/conversion-gen/args"
)

// These are the comment tags that carry parameters for conversion generation.
const (
	// e.g., "+k8s:conversion-gen=<peer-pkg>" in doc.go, where <peer-pkg> is the
	// import path of the package the peer types are defined in.
	// e.g., "+k8s:conversion-gen=false" in a type's comment will let
	// conversion-gen skip that type.
	tagName = "k8s:conversion-gen"

	// TODO wkpo comment
	functionTagName = "k8s:conversion-fn"

	// e.g., "+k8s:conversion-gen=<peer-pkg>" in doc.go, where <peer-pkg> is the
	// import path of the package the peer types are defined in.
	// e.g., "+k8s:conversion-gen=false" in a type's comment will let
	// conversion-gen skip that type.
	// tagName = "k8s:conversion-gen"
	// e.g., "+k8s:conversion-gen-external-types=<type-pkg>" in doc.go, where
	// <type-pkg> is the relative path to the package the types are defined in.
	externalTypesTagName = "k8s:conversion-gen-external-types"

	// TODO wkpo comment
	scopeVarName = "s"
)

// TODO wkpo move to EOF
func extractExternalTypesTag(comments []string) []string {
	return types.ExtractCommentTags("+", comments)[externalTypesTagName]
}

// TODO: This is created only to reduce number of changes in a single PR.
// Remove it and use PublicNamer instead.
// TODO wkpo?? used!!
func conversionNamer() *namer.NameStrategy {
	return &namer.NameStrategy{
		Join: func(pre string, in []string, post string) string {
			return strings.Join(in, "_")
		},
		PrependPackageNames: 1,
	}
}

func defaultFnNamer() *namer.NameStrategy {
	return &namer.NameStrategy{
		Prefix: "SetDefaults_",
		Join: func(pre string, in []string, post string) string {
			return pre + strings.Join(in, "_") + post
		},
	}
}

// TODO wkpo needed this shit?
// NameSystems returns the name system used by the generators in this package.
func NameSystems() namer.NameSystems {
	return namer.NameSystems{
		"public":    conversionNamer(),
		"raw":       namer.NewRawNamer("", nil),
		"defaultfn": defaultFnNamer(),
	}
}

// DefaultNameSystem returns the default name system for ordering the types to be
// processed by the generators in this package.
func DefaultNameSystem() string {
	return "public"
}

// TODO wkpo move to EOF
func extractTag(comments []string) []string {
	return types.ExtractCommentTags("+", comments)[tagName]
}

func Packages(context *generator.Context, arguments *args.GeneratorArgs) generator.Packages {
	boilerplate, err := arguments.LoadGoBoilerplate()
	if err != nil {
		klog.Fatalf("Failed loading boilerplate: %v", err)
	}

	packages := generator.Packages{}
	header := append([]byte(fmt.Sprintf("// +build !%s\n\n", arguments.GeneratedBuildTag)), boilerplate...)
	manualConversionsTracker := NewManualConversionsTracker(NewNamedVariable(scopeVarName, types.Ref(conversionPackagePath, "Scope")))

	processed := map[string]bool{}
	for _, i := range context.Inputs {
		// skip duplicates
		if processed[i] {
			continue
		}
		processed[i] = true

		klog.V(5).Infof("considering pkg %q", i)
		pkg := context.Universe[i]
		if pkg == nil {
			// If the input had no Go files, for example.
			continue
		}

		// Only generate conversions for packages which explicitly request it
		// by specifying one or more "+k8s:conversion-gen=<peer-pkg>"
		// in their doc.go file.
		peerPkgs := extractTag(pkg.Comments)
		if peerPkgs != nil {
			klog.V(5).Infof("  tags: %q", peerPkgs)
		} else {
			klog.V(5).Infof("  no tag")
			continue
		}
		skipUnsafe := false
		if customArgs, ok := arguments.CustomArgs.(*conversionargs.CustomArgs); ok {
			peerPkgs = append(peerPkgs, customArgs.BasePeerDirs...)
			peerPkgs = append(peerPkgs, customArgs.ExtraPeerDirs...)
			skipUnsafe = customArgs.SkipUnsafe
		}

		// typesPkg is where the versioned types are defined. Sometimes it is
		// different from pkg. For example, kubernetes core/v1 types are defined
		// in vendor/k8s.io/api/core/v1, while pkg is at pkg/api/v1.
		typesPkg := pkg

		// if the external types are not in the same package where the conversion functions to be generated
		externalTypesValues := extractExternalTypesTag(pkg.Comments)
		if externalTypesValues != nil {
			if len(externalTypesValues) != 1 {
				klog.Fatalf("  expect only one value for %q tag, got: %q", externalTypesTagName, externalTypesValues)
			}
			externalTypes := externalTypesValues[0]
			klog.V(5).Infof("  external types tags: %q", externalTypes)
			var err error
			typesPkg, err = context.AddDirectory(externalTypes)
			if err != nil {
				klog.Fatalf("cannot import package %s", externalTypes)
			}
			// update context.Order to the latest context.Universe
			orderer := namer.Orderer{Namer: namer.NewPublicNamer(1)}
			context.Order = orderer.OrderUniverse(context.Universe)
		}

		// if the source path is within a /vendor/ directory (for example,
		// k8s.io/kubernetes/vendor/k8s.io/apimachinery/pkg/apis/meta/v1), allow
		// generation to output to the proper relative path (under vendor).
		// Otherwise, the generator will create the file in the wrong location
		// in the output directory.
		// TODO: build a more fundamental concept in gengo for dealing with modifications
		// to vendored packages.
		vendorless := func(pkg string) string {
			if pos := strings.LastIndex(pkg, "/vendor/"); pos != -1 {
				return pkg[pos+len("/vendor/"):]
			}
			return pkg
		}
		for i := range peerPkgs {
			peerPkgs[i] = vendorless(peerPkgs[i])
		}

		path := pkg.Path
		// if the source path is within a /vendor/ directory (for example,
		// k8s.io/kubernetes/vendor/k8s.io/apimachinery/pkg/apis/meta/v1), allow
		// generation to output to the proper relative path (under vendor).
		// Otherwise, the generator will create the file in the wrong location
		// in the output directory.
		// TODO: build a more fundamental concept in gengo for dealing with modifications
		// to vendored packages.
		if strings.HasPrefix(pkg.SourcePath, arguments.OutputBase) {
			expandedPath := strings.TrimPrefix(pkg.SourcePath, arguments.OutputBase)
			if strings.Contains(expandedPath, "/vendor/") {
				path = expandedPath
			}
		}

		conversionGenerator, err := NewConversionGenerator(context, arguments.OutputFileBaseName, typesPkg.Path, pkg.Path, peerPkgs, manualConversionsTracker)
		if err != nil {
			klog.Fatalf(err.Error())
		}
		conversionGenerator.WithTagName(tagName).
			WithFunctionTagName(functionTagName).
			WithMissingFieldsHandler(missingFieldsHandler).
			WithInconvertibleFieldsHandler(inconvertibleFieldsHandler).
			WithUnsupportedTypesHandler(unsupportedTypesHandler).
			WithExternalConversionsHandler(externalConversionsHandler)

		if skipUnsafe {
			conversionGenerator.WithoutUnsafeConversions()
		}

		packages = append(packages,
			&generator.DefaultPackage{
				PackageName: filepath.Base(pkg.Path),
				PackagePath: path,
				HeaderText:  header,
				// TODO wkpo can just be a goddam list
				GeneratorFunc: func(c *generator.Context) []generator.Generator {
					return []generator.Generator{
						&genConversion{
							ConversionGenerator: conversionGenerator,
						},
					}
				},
				FilterFunc: func(c *generator.Context, t *types.Type) bool {
					return t.Name.Package == typesPkg.Path
				},
			})
	}

	return packages
}

func missingFieldsHandler(inVar, outVar NamedVariable, member *types.Member, sw *generator.SnippetWriter) error {
	sw.Do("// WARNING: in."+member.Name+" requires manual conversion: does not exist in peer-type\n", nil)
	return fmt.Errorf("field " + member.Name + "requires manual conversion")
}

func inconvertibleFieldsHandler(inVar, outVar NamedVariable, inMember, outMember *types.Member, sw *generator.SnippetWriter) error {
	sw.Do("// WARNING: in."+inMember.Name+" requires manual conversion: inconvertible types ("+
		inMember.Type.String()+" vs "+outMember.Type.String()+")\n", nil)
	return fmt.Errorf("field " + inMember.Name + "requires manual conversion")
}

func unsupportedTypesHandler(inVar, outVar NamedVariable, sw *generator.SnippetWriter) error {
	sw.Do("// FIXME: Type $.|raw$ is unsupported.\n", inVar.Type)

	return nil
}

func externalConversionsHandler(inVar, outVar NamedVariable, sw *generator.SnippetWriter) error {
	sw.Do("// TODO: Inefficient conversion - can we improve it?\n", nil)
	sw.Do("if err := "+scopeVarName+".Convert("+inVar.Name+", "+outVar.Name+", 0); err != nil {\n", nil)
	sw.Do("return err\n}\n", nil)
	return nil
}

// genConversion produces a file with autogenerated conversions.
type genConversion struct {
	*ConversionGenerator

	// generatedTypes are the types for which there exist conversion functions.
	generatedTypes []*types.Type
}

func (g *genConversion) Filter(context *generator.Context, t *types.Type) bool {
	result := g.ConversionGenerator.Filter(context, t)
	if result {
		g.generatedTypes = append(g.generatedTypes, t)
	}
	return result
}

func (g *genConversion) Init(context *generator.Context, writer io.Writer) error {
	sw := generator.NewSnippetWriter(writer, context, "$", "$")

	sw.Do("func init() {\n", nil)
	sw.Do("localSchemeBuilder.Register(RegisterConversions)\n", nil)
	sw.Do("}\n", nil)

	scheme := context.Universe.Type(types.Name{Package: runtimePackagePath, Name: "Scheme"})
	schemePtr := &types.Type{
		Kind: types.Pointer,
		Elem: scheme,
	}
	sw.Do("// RegisterConversions adds conversion functions to the given scheme.\n", nil)
	sw.Do("// Public to allow building arbitrary schemes.\n", nil)
	sw.Do("func RegisterConversions(s $.|raw$) error {\n", schemePtr)
	for _, t := range g.generatedTypes {
		peerType := g.GetPeerTypeFor(context, t)
		args := argsFromType(t, peerType).With("Scope", types.Ref(conversionPackagePath, "Scope"))
		sw.Do("if err := s.AddGeneratedConversionFunc((*$.inType|raw$)(nil), (*$.outType|raw$)(nil), func(a, b interface{}, scope $.Scope|raw$) error { return "+conversionFunctionNameTemplate("publicIT")+"(a.(*$.inType|raw$), b.(*$.outType|raw$), scope) }); err != nil { return err }\n", args)
		args = argsFromType(peerType, t).With("Scope", types.Ref(conversionPackagePath, "Scope"))
		sw.Do("if err := s.AddGeneratedConversionFunc((*$.inType|raw$)(nil), (*$.outType|raw$)(nil), func(a, b interface{}, scope $.Scope|raw$) error { return "+conversionFunctionNameTemplate("publicIT")+"(a.(*$.inType|raw$), b.(*$.outType|raw$), scope) }); err != nil { return err }\n", args)
	}

	manualConversions := g.ManualConversions()
	for _, pair := range samePkgManualConversionPairs(manualConversions, g.outputPackage) {
		args := argsFromType(pair.inType, pair.outType).With("Scope", types.Ref(conversionPackagePath, "Scope")).With("fn", manualConversions[pair])
		sw.Do("if err := s.AddConversionFunc((*$.inType|raw$)(nil), (*$.outType|raw$)(nil), func(a, b interface{}, scope $.Scope|raw$) error { return $.fn|raw$(a.(*$.inType|raw$), b.(*$.outType|raw$), scope) }); err != nil { return err }\n", args)
	}

	sw.Do("return nil\n", nil)
	sw.Do("}\n\n", nil)
	return sw.Error()
}

// TODO wkpo comment?
func samePkgManualConversionPairs(manualConversions map[conversionPair]*types.Type, outputPackage string) (pairs []conversionPair) {
	for pair, t := range manualConversions {
		if t.Name.Package == outputPackage {
			pairs = append(pairs, pair)
		}
	}

	// sort by name of the conversion function
	sort.Slice(pairs, func(i, j int) bool {
		if manualConversions[pairs[i]].Name.Name < manualConversions[pairs[j]].Name.Name {
			return true
		}
		return false
	})

	return
}

// TODO wkpo??
const (
	runtimePackagePath    = "k8s.io/apimachinery/pkg/runtime"
	conversionPackagePath = "k8s.io/apimachinery/pkg/conversion"
)
