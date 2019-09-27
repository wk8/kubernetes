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
	// tagName = "k8s:conversion-gen"
	// e.g., "+k8s:conversion-gen-external-types=<type-pkg>" in doc.go, where
	// <type-pkg> is the relative path to the package the types are defined in.
	externalTypesTagName = "k8s:conversion-gen-external-types"
)

func extractExternalTypesTag(comments []string) []string {
	return types.ExtractCommentTags("+", comments)[externalTypesTagName]
}

func isCopyOnly(comments []string) bool {
	values := types.ExtractCommentTags("+", comments)["k8s:conversion-fn"]
	return len(values) == 1 && values[0] == "copy-only"
}

// TODO: This is created only to reduce number of changes in a single PR.
// Remove it and use PublicNamer instead.
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

func getPeerTypeFor(context *generator.Context, t *types.Type, potentialPeerPkgs []string) *types.Type {
	for _, ppp := range potentialPeerPkgs {
		p := context.Universe.Package(ppp)
		if p == nil {
			continue
		}
		if p.Has(t.Name.Name) {
			return p.Type(t.Name.Name)
		}
	}
	return nil
}

// All of the types in conversions map are of type "DeclarationOf" with
// the underlying type being "Func".
type conversionFuncMap map[conversionPair]*types.Type

func Packages(context *generator.Context, arguments *args.GeneratorArgs) generator.Packages {
	boilerplate, err := arguments.LoadGoBoilerplate()
	if err != nil {
		klog.Fatalf("Failed loading boilerplate: %v", err)
	}

	packages := generator.Packages{}
	header := append([]byte(fmt.Sprintf("// +build !%s\n\n", arguments.GeneratedBuildTag)), boilerplate...)

	// Accumulate pre-existing conversion functions.
	// TODO: This is too ad-hoc.  We need a better way.
	manualConversions := conversionFuncMap{}

	// Record types that are memory equivalent. A type is memory equivalent
	// if it has the same memory layout and no nested manual conversion is
	// defined.
	// TODO: in the future, relax the nested manual conversion requirement
	//   if we can show that a large enough types are memory identical but
	//   have non-trivial conversion
	// TODO wkpo comment ^ ?
	memoryEquivalentTypes := equalMemoryTypes{}

	// We are generating conversions only for packages that are explicitly
	// passed as InputDir.
	processed := map[string]bool{}
	for _, i := range context.Inputs {
		// skip duplicates
		if processed[i] {
			continue
		}
		processed[i] = true

		klog.V(5).Infof("considering pkg %q", i)
		pkg := context.Universe[i]
		// typesPkg is where the versioned types are defined. Sometimes it is
		// different from pkg. For example, kubernetes core/v1 types are defined
		// in vendor/k8s.io/api/core/v1, while pkg is at pkg/api/v1.
		typesPkg := pkg
		if pkg == nil {
			// If the input had no Go files, for example.
			continue
		}

		// Add conversion and defaulting functions.
		getManualConversionFunctions(context, pkg, manualConversions)

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

		// Make sure our peer-packages are added and fully parsed.
		for _, pp := range peerPkgs {
			context.AddDir(pp)
			p := context.Universe[pp]
			if nil == p {
				klog.Fatalf("failed to find pkg: %s", pp)
			}
			getManualConversionFunctions(context, p, manualConversions)
		}

		unsafeEquality := TypesEqual(memoryEquivalentTypes)
		if skipUnsafe {
			unsafeEquality = noEquality{}
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
		packages = append(packages,
			&generator.DefaultPackage{
				PackageName: filepath.Base(pkg.Path),
				PackagePath: path,
				HeaderText:  header,
				GeneratorFunc: func(c *generator.Context) (generators []generator.Generator) {
					return []generator.Generator{
						NewGenConversion(arguments.OutputFileBaseName, typesPkg.Path, pkg.Path, manualConversions, peerPkgs, unsafeEquality),
					}
				},
				FilterFunc: func(c *generator.Context, t *types.Type) bool {
					return t.Name.Package == typesPkg.Path
				},
			})
	}

	// If there is a manual conversion defined between two types, exclude it
	// from being a candidate for unsafe conversion
	// TODO wkpo ^ !!
	for k, v := range manualConversions {
		if isCopyOnly(v.CommentLines) {
			klog.V(5).Infof("Conversion function %s will not block memory copy because it is copy-only", v.Name)
			continue
		}
		// this type should be excluded from all equivalence, because the converter must be called.
		memoryEquivalentTypes.Skip(k.inType, k.outType)
	}

	return packages
}

// TODO wkpo what of this?
type equalMemoryTypes map[conversionPair]bool

func (e equalMemoryTypes) Skip(a, b *types.Type) {
	e[conversionPair{a, b}] = false
	e[conversionPair{b, a}] = false
}

func (e equalMemoryTypes) Equal(a, b *types.Type) bool {
	// alreadyVisitedTypes holds all the types that have already been checked in the structural type recursion.
	alreadyVisitedTypes := make(map[*types.Type]bool)
	return e.cachingEqual(a, b, alreadyVisitedTypes)
}

func (e equalMemoryTypes) cachingEqual(a, b *types.Type, alreadyVisitedTypes map[*types.Type]bool) bool {
	if a == b {
		return true
	}
	if equal, ok := e[conversionPair{a, b}]; ok {
		return equal
	}
	if equal, ok := e[conversionPair{b, a}]; ok {
		return equal
	}
	result := e.equal(a, b, alreadyVisitedTypes)
	e[conversionPair{a, b}] = result
	e[conversionPair{b, a}] = result
	return result
}

func (e equalMemoryTypes) equal(a, b *types.Type, alreadyVisitedTypes map[*types.Type]bool) bool {
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
				if !e.cachingEqual(inMember.Type, outMember.Type, alreadyVisitedTypes) {
					return false
				}
			}
			return true
		case types.Pointer:
			return e.cachingEqual(in.Elem, out.Elem, alreadyVisitedTypes)
		case types.Map:
			return e.cachingEqual(in.Key, out.Key, alreadyVisitedTypes) && e.cachingEqual(in.Elem, out.Elem, alreadyVisitedTypes)
		case types.Slice:
			return e.cachingEqual(in.Elem, out.Elem, alreadyVisitedTypes)
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

const (
	runtimePackagePath    = "k8s.io/apimachinery/pkg/runtime"
	conversionPackagePath = "k8s.io/apimachinery/pkg/conversion"
)

type noEquality struct{}

func (noEquality) Equal(_, _ *types.Type) bool { return false }

type TypesEqual interface {
	Equal(a, b *types.Type) bool
}

// genConversion produces a file with a autogenerated conversions.
type genConversion struct {
	generator.DefaultGen
	// the package that contains the types that conversion func are going to be
	// generated for
	typesPackage string
	// the package that the conversion funcs are going to be output to
	outputPackage string
	// packages that contain the peer of types in typesPacakge
	peerPackages      []string
	manualConversions conversionFuncMap
	imports           namer.ImportTracker
	types             []*types.Type
	skippedFields     map[*types.Type][]string
	useUnsafe         TypesEqual
}

func NewGenConversion(sanitizedName, typesPackage, outputPackage string, manualConversions conversionFuncMap, peerPkgs []string, useUnsafe TypesEqual) generator.Generator {
	return &genConversion{
		DefaultGen: generator.DefaultGen{
			OptionalName: sanitizedName,
		},
		typesPackage:      typesPackage,
		outputPackage:     outputPackage,
		peerPackages:      peerPkgs,
		manualConversions: manualConversions,
		imports:           generator.NewImportTracker(),
		types:             []*types.Type{},
		skippedFields:     map[*types.Type][]string{},
		useUnsafe:         useUnsafe,
	}
}

func (g *genConversion) Namers(c *generator.Context) namer.NameSystems {
	// Have the raw namer for this file track what it imports.
	return namer.NameSystems{
		"raw": namer.NewRawNamer(g.outputPackage, g.imports),
		"publicIT": &namerPlusImportTracking{
			delegate: conversionNamer(),
			tracker:  g.imports,
		},
	}
}

type namerPlusImportTracking struct {
	delegate namer.Namer
	tracker  namer.ImportTracker
}

func (n *namerPlusImportTracking) Name(t *types.Type) string {
	n.tracker.AddType(t)
	return n.delegate.Name(t)
}

func (g *genConversion) isOtherPackage(pkg string) bool {
	if pkg == g.outputPackage {
		return false
	}
	if strings.HasSuffix(pkg, `"`+g.outputPackage+`"`) {
		return false
	}
	return true
}

func (g *genConversion) Imports(c *generator.Context) (imports []string) {
	var importLines []string
	for _, singleImport := range g.imports.ImportLines() {
		if g.isOtherPackage(singleImport) {
			importLines = append(importLines, singleImport)
		}
	}
	return importLines
}

func (g *genConversion) Init(c *generator.Context, w io.Writer) error {
	if klog.V(5) {
		if m, ok := g.useUnsafe.(equalMemoryTypes); ok {
			var result []string
			klog.Infof("All objects without identical memory layout:")
			for k, v := range m {
				if v {
					continue
				}
				result = append(result, fmt.Sprintf("  %s -> %s = %t", k.inType, k.outType, v))
			}
			sort.Strings(result)
			for _, s := range result {
				klog.Infof(s)
			}
		}
	}
	sw := generator.NewSnippetWriter(w, c, "$", "$")
	sw.Do("func init() {\n", nil)
	sw.Do("localSchemeBuilder.Register(RegisterConversions)\n", nil)
	sw.Do("}\n", nil)

	scheme := c.Universe.Type(types.Name{Package: runtimePackagePath, Name: "Scheme"})
	schemePtr := &types.Type{
		Kind: types.Pointer,
		Elem: scheme,
	}
	sw.Do("// RegisterConversions adds conversion functions to the given scheme.\n", nil)
	sw.Do("// Public to allow building arbitrary schemes.\n", nil)
	sw.Do("func RegisterConversions(s $.|raw$) error {\n", schemePtr)
	for _, t := range g.types {
		peerType := getPeerTypeFor(c, t, g.peerPackages)
		args := argsFromType(t, peerType).With("Scope", types.Ref(conversionPackagePath, "Scope"))
		sw.Do("if err := s.AddGeneratedConversionFunc((*$.inType|raw$)(nil), (*$.outType|raw$)(nil), func(a, b interface{}, scope $.Scope|raw$) error { return "+nameTmpl+"(a.(*$.inType|raw$), b.(*$.outType|raw$), scope) }); err != nil { return err }\n", args)
		args = argsFromType(peerType, t).With("Scope", types.Ref(conversionPackagePath, "Scope"))
		sw.Do("if err := s.AddGeneratedConversionFunc((*$.inType|raw$)(nil), (*$.outType|raw$)(nil), func(a, b interface{}, scope $.Scope|raw$) error { return "+nameTmpl+"(a.(*$.inType|raw$), b.(*$.outType|raw$), scope) }); err != nil { return err }\n", args)
	}
	var pairs []conversionPair
	for pair, t := range g.manualConversions {
		if t.Name.Package != g.outputPackage {
			continue
		}
		pairs = append(pairs, pair)
	}
	// sort by name of the conversion function
	sort.Slice(pairs, func(i, j int) bool {
		if g.manualConversions[pairs[i]].Name.Name < g.manualConversions[pairs[j]].Name.Name {
			return true
		}
		return false
	})
	for _, pair := range pairs {
		args := argsFromType(pair.inType, pair.outType).With("Scope", types.Ref(conversionPackagePath, "Scope")).With("fn", g.manualConversions[pair])
		sw.Do("if err := s.AddConversionFunc((*$.inType|raw$)(nil), (*$.outType|raw$)(nil), func(a, b interface{}, scope $.Scope|raw$) error { return $.fn|raw$(a.(*$.inType|raw$), b.(*$.outType|raw$), scope) }); err != nil { return err }\n", args)
	}

	sw.Do("return nil\n", nil)
	sw.Do("}\n\n", nil)
	return sw.Error()
}
