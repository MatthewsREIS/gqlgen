package shardruntime

import (
	"context"
	"fmt"
	"sync"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/99designs/gqlgen/graphql"
)

// inputDirectivePlan lists the fields of a hybrid input that its generated
// unmarshaler still decodes itself (field resolvers, and nested inputs that
// have no runtime decoder). ApplyInputDirectives leaves those to their arm.
type inputDirectivePlan struct {
	generated map[string]bool
}

var (
	inputDirectivePlansMu sync.RWMutex
	inputDirectivePlans   = map[string]inputDirectivePlan{}
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
}

// ApplyInputDirectives applies, in place on raw, the defaults and the
// INPUT_FIELD_DEFINITION directives the schema declares for inputName, and
// recurses into nested values whose input type registered a plan of its own.
// It mirrors what a generated case arm does for a field, with the same path
// context, so a rejecting directive fails with the same error and path.
func ApplyInputDirectives(
	ctx context.Context,
	ec ObjectExecutionContext,
	scope, inputName string,
	raw map[string]any,
) error {
	plan, ok := lookupInputDirectivePlan(scope, inputName)
	if !ok {
		return fmt.Errorf("input %s has no directive plan registered in scope %s", inputName, scope)
	}
	schema := ec.Schema()
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
		res, err := applyFieldDirectives(fctx, ec, scope, schema, f, raw, v)
		if err != nil {
			return graphql.ErrorOnPath(fctx, err)
		}
		raw[f.Name] = res
	}
	return nil
}

// applyFieldDirectives wraps the nested walk in the field's directives, last
// directive outermost, exactly as the generated arm chains directive0..N.
func applyFieldDirectives(
	ctx context.Context,
	ec ObjectExecutionContext,
	scope string,
	schema *ast.Schema,
	f *ast.FieldDefinition,
	obj map[string]any,
	v any,
) (any, error) {
	next := graphql.Resolver(func(ctx context.Context) (any, error) {
		if err := walkNested(ctx, ec, scope, schema, f.Type, v); err != nil {
			return nil, err
		}
		return v, nil
	})
	for _, d := range f.Directives {
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

func walkValue(ctx context.Context, ec ObjectExecutionContext, scope, named string, v any) error {
	switch x := v.(type) {
	case map[string]any:
		return ApplyInputDirectives(ctx, ec, scope, named, x)
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
