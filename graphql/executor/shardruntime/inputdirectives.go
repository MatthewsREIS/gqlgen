package shardruntime

import (
	"context"
	"fmt"
	"maps"
	"sync"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/99designs/gqlgen/graphql"
)

// inputDirectivePlan lists the fields of a hybrid input that its generated
// unmarshaler still decodes itself (nested inputs the runtime cannot reach).
// ApplyInputDirectives leaves those to their arm.
type inputDirectivePlan struct {
	generated map[string]bool
}

// schemaProvider is what ApplyInputDirectives needs beyond
// ObjectExecutionContext. Generated root execution contexts satisfy it; the
// interface stays unchanged for other implementations.
type schemaProvider interface {
	Schema() *ast.Schema
}

var (
	inputDirectivePlansMu sync.RWMutex
	inputDirectivePlans   = map[string]inputDirectivePlan{}
	runtimeDirectives     = map[string]map[string]bool{}
)

// RegisterInputDirectivePlan declares inputName as a hybrid input of scope whose
// field directives and defaults ApplyInputDirectives applies at runtime, except
// for generatedFields, which keep a generated case arm.
func RegisterInputDirectivePlan(scope, inputName string, generatedFields []string) {
	inputDirectivePlansMu.Lock()
	defer inputDirectivePlansMu.Unlock()
	key := objectKey(scope, inputName)
	if _, exists := inputDirectivePlans[key]; exists {
		panic("duplicate input directive plan registration: " + key)
	}
	plan := inputDirectivePlan{generated: make(map[string]bool, len(generatedFields))}
	for _, f := range generatedFields {
		plan.generated[f] = true
	}
	inputDirectivePlans[key] = plan
}

// RegisterRuntimeDirectives declares the directives the root package of scope
// dispatches through InvokeDirective. ApplyInputDirectives skips any other
// directive written on a field: the skip_runtime ones such as @goField exist
// for codegen only and have no implementation to call. Until a scope registers
// its set, every directive on a field is applied.
func RegisterRuntimeDirectives(scope string, names ...string) {
	inputDirectivePlansMu.Lock()
	defer inputDirectivePlansMu.Unlock()
	set := runtimeDirectives[scope]
	if set == nil {
		set = map[string]bool{}
		runtimeDirectives[scope] = set
	}
	for _, n := range names {
		set[n] = true
	}
}

func isRuntimeDirective(scope, name string) bool {
	inputDirectivePlansMu.RLock()
	defer inputDirectivePlansMu.RUnlock()
	set, ok := runtimeDirectives[scope]
	if !ok {
		return true
	}
	return set[name]
}

func lookupInputDirectivePlan(scope, inputName string) (inputDirectivePlan, bool) {
	inputDirectivePlansMu.RLock()
	defer inputDirectivePlansMu.RUnlock()
	plan, ok := inputDirectivePlans[objectKey(scope, inputName)]
	return plan, ok
}

func resetInputDirectivePlansForTest() {
	inputDirectivePlansMu.Lock()
	defer inputDirectivePlansMu.Unlock()
	inputDirectivePlans = map[string]inputDirectivePlan{}
	runtimeDirectives = map[string]map[string]bool{}
}

// ApplyInputDirectives applies, in place on raw, the defaults and the
// INPUT_FIELD_DEFINITION directives the schema declares for inputName, and
// recurses into nested values whose input type registered a plan of its own.
//
// It mirrors a generated case arm: the same directive chain order, the same
// argument coercion (InvokeDirective runs the codecs), the same path context,
// and the same directive object, obj, which is the value the unmarshaler was
// handed before defaults were applied. Two differences are deliberate. A
// directive receives the raw request value rather than the codec-coerced Go
// value, because the method decodes the field afterwards and a nested input
// has to stay a map for it; and a directive's result is checked by that decode
// rather than by a generated type assertion.
func ApplyInputDirectives(
	ctx context.Context,
	ec ObjectExecutionContext,
	scope, inputName string,
	obj any,
	raw map[string]any,
) error {
	plan, ok := lookupInputDirectivePlan(scope, inputName)
	if !ok {
		return fmt.Errorf("input %s has no directive plan registered in scope %s", inputName, scope)
	}
	sp, ok := ec.(schemaProvider)
	if !ok {
		return fmt.Errorf(
			"execution context %T has no Schema(); cannot apply the directives of input %s",
			ec,
			inputName,
		)
	}
	schema := sp.Schema()
	def := schema.Types[inputName]
	if def == nil || def.Kind != ast.InputObject {
		return fmt.Errorf("input %s is not an input object in the schema", inputName)
	}
	for _, f := range def.Fields {
		if plan.generated[f.Name] {
			continue
		}
		v, present := raw[f.Name]
		if !present {
			if f.DefaultValue == nil {
				continue
			}
			dv, err := f.DefaultValue.Value(nil)
			if err != nil {
				return err
			}
			raw[f.Name] = dv
			v = dv
		}
		fctx := graphql.WithPathContext(ctx, graphql.NewPathWithField(f.Name))
		// A resolver-backed field is decoded by its codec in ApplyInputFieldResolvers,
		// which runs the nested input's own directives, so the walk stops at the field.
		descend := !isResolverOwnedField(scope, inputName, f.Name)
		res, err := applyFieldDirectives(fctx, ec, scope, schema, f, obj, v, descend)
		if err != nil {
			return graphql.ErrorOnPath(fctx, err)
		}
		raw[f.Name] = res
	}
	return nil
}

// applyFieldDirectives wraps the nested walk in the field's runtime directives,
// last directive outermost, exactly as the generated arm chains directive0..N.
func applyFieldDirectives(
	ctx context.Context,
	ec ObjectExecutionContext,
	scope string,
	schema *ast.Schema,
	f *ast.FieldDefinition,
	obj any,
	v any,
	descend bool,
) (any, error) {
	next := graphql.Resolver(func(ctx context.Context) (any, error) {
		if !descend {
			return v, nil
		}
		if err := walkNested(ctx, ec, scope, schema, f.Type, v); err != nil {
			return nil, err
		}
		return v, nil
	})
	for _, d := range f.Directives {
		if !isRuntimeDirective(scope, d.Name) {
			continue
		}
		args, err := directiveArgs(schema, d)
		if err != nil {
			return nil, err
		}
		prev := next
		next = func(ctx context.Context) (any, error) {
			return ec.InvokeDirective(ctx, d.Name, obj, prev, args)
		}
	}
	return next(ctx)
}

// directiveArgs is the runtime twin of the splitDirectiveArgs template: the
// literal arguments written on the field, plus the directive definition's
// defaults for the ones left out.
func directiveArgs(schema *ast.Schema, d *ast.Directive) (map[string]any, error) {
	def := schema.Directives[d.Name]
	if len(d.Arguments) == 0 && (def == nil || len(def.Arguments) == 0) {
		return nil, nil
	}
	args := make(map[string]any, len(d.Arguments))
	for _, a := range d.Arguments {
		val, err := a.Value.Value(nil)
		if err != nil {
			return nil, err
		}
		args[a.Name] = val
	}
	if def != nil {
		for _, da := range def.Arguments {
			if _, ok := args[da.Name]; ok || da.DefaultValue == nil {
				continue
			}
			val, err := da.DefaultValue.Value(nil)
			if err != nil {
				return nil, err
			}
			args[da.Name] = val
		}
	}
	return args, nil
}

func walkNested(
	ctx context.Context,
	ec ObjectExecutionContext,
	scope string,
	schema *ast.Schema,
	t *ast.Type,
	v any,
) error {
	named := t.Name()
	def := schema.Types[named]
	if def == nil || def.Kind != ast.InputObject {
		return nil
	}
	if _, ok := lookupInputDirectivePlan(scope, named); !ok {
		return nil
	}
	return walkValue(ctx, ec, scope, named, v)
}

// walkValue recurses into a nested input value. A nested map is mutated in
// place; the directives of its fields receive a copy taken before that, which
// is what the nested generated unmarshaler was handed.
func walkValue(ctx context.Context, ec ObjectExecutionContext, scope, named string, v any) error {
	switch x := v.(type) {
	case map[string]any:
		return ApplyInputDirectives(ctx, ec, scope, named, maps.Clone(x), x)
	case []any:
		for i, e := range x {
			ictx := graphql.WithPathContext(ctx, graphql.NewPathWithIndex(i))
			if err := walkValue(ictx, ec, scope, named, e); err != nil {
				return err
			}
		}
	}
	return nil
}
