package generators

import (
	"fmt"

	"k8s.io/gengo/generator"
	"k8s.io/gengo/types"
)

const conversionFunctionPrefix = "Convert_"

func conversionFunctionTemplate(namer string) string {
	return fmt.Sprintf("%s$.inType|%s$_To_$.outType|%s$", conversionFunctionPrefix, namer, namer)
}

func argsFromType(inType, outType *types.Type) generator.Args {
	return generator.Args{
		"inType":  inType,
		"outType": outType,
	}
}
