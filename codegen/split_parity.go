package codegen

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/99designs/gqlgen/graphql/executor/shardruntime"
)

// checkRuntimeSchemaParity fails codegen when the schema the generated root
// will parse at runtime plans a different directive chain for any field than
// codegen saw, which happens when a SchemaMutator plugin adds or removes a
// directive without writing it into the schema sources.
func checkRuntimeSchemaParity(data *Data) error {
	sources := make([]*ast.Source, 0, len(data.AugmentedSources))
	for _, s := range data.AugmentedSources {
		sources = append(
			sources,
			&ast.Source{Name: s.RelativePath, Input: s.Source, BuiltIn: s.BuiltIn},
		)
	}
	runtimeSchema, gqlErr := gqlparser.LoadSchema(sources...)
	if gqlErr != nil {
		return fmt.Errorf("load the runtime schema from the exec sources: %w", gqlErr)
	}
	runtime := data.Directives()
	isRuntime := func(name string) bool { _, ok := runtime[name]; return ok }
	compare := func(objName string, f *Field, loc ast.DirectiveLocation) error {
		var want []shardruntime.PlannedDirective
		for _, d := range f.ImplDirectives() {
			args := map[string]any{}
			for _, a := range d.Args {
				args[a.Name] = a.Value
			}
			want = append(want, shardruntime.PlannedDirective{Name: d.Name, Args: valuedArgs(args)})
		}
		got, err := shardruntime.FieldDirectives(runtimeSchema, objName, f.Name, loc, isRuntime)
		if err != nil {
			return fmt.Errorf(
				"split-packages: %w; the runtime derives field metadata from the schema sources, so every field must be declared in them",
				err,
			)
		}
		for i := range got {
			got[i].Args = valuedArgs(got[i].Args)
		}
		if !reflect.DeepEqual(want, got) {
			return fmt.Errorf(
				"split-packages: %s.%s has directives [%s] in the generated schema but the runtime would derive [%s] from the schema sources; a SchemaMutator plugin must write its directives into the sources",
				objName,
				f.Name,
				formatDirectiveChain(want),
				formatDirectiveChain(got),
			)
		}
		return nil
	}
	for _, obj := range data.Objects {
		for _, f := range obj.Fields {
			if f.TypeReference == nil || f.IsReserved() {
				continue
			}
			if err := compare(obj.Name, f, ast.LocationFieldDefinition); err != nil {
				return err
			}
		}
	}
	for _, obj := range data.Inputs {
		if !obj.HasContextUnmarshal() {
			continue
		}
		generated := obj.HybridGeneratedFields()
		for _, f := range obj.Fields {
			if f.TypeReference == nil || f.IsResolver || slices.Contains(generated, f) {
				continue
			}
			if err := compare(obj.Name, f, ast.LocationInputFieldDefinition); err != nil {
				return err
			}
		}
	}
	return nil
}

func valuedArgs(args map[string]any) map[string]any {
	var valued map[string]any
	for name, v := range args {
		if v == nil {
			continue
		}
		if valued == nil {
			valued = map[string]any{}
		}
		valued[name] = v
	}
	return valued
}

func formatDirectiveChain(chain []shardruntime.PlannedDirective) string {
	parts := make([]string, 0, len(chain))
	for _, d := range chain {
		args := make([]string, 0, len(d.Args))
		for _, name := range slices.Sorted(maps.Keys(d.Args)) {
			args = append(args, fmt.Sprintf("%s: %v", name, d.Args[name]))
		}
		parts = append(parts, d.Name+"("+strings.Join(args, ", ")+")")
	}
	return strings.Join(parts, " ")
}
