package codegen

import (
	"fmt"
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
	for _, obj := range data.Objects {
		for _, f := range obj.Fields {
			if f.TypeReference == nil {
				continue
			}
			want := make([]string, 0, len(f.ImplDirectives()))
			for _, d := range f.ImplDirectives() {
				want = append(want, d.Name)
			}
			got, err := shardruntime.FieldDirectiveNames(runtimeSchema, obj.Name, f.Name, isRuntime)
			if err != nil {
				return fmt.Errorf(
					"split-packages: %w; the runtime derives field metadata from the schema sources, so every field must be declared in them",
					err,
				)
			}
			if !slices.Equal(want, got) {
				return fmt.Errorf(
					"split-packages: %s.%s has directives [%s] in the generated schema but the runtime would derive [%s] from the schema sources; a SchemaMutator plugin must write its directives into the sources",
					obj.Name,
					f.Name,
					strings.Join(want, " "),
					strings.Join(got, " "),
				)
			}
		}
	}
	return nil
}
