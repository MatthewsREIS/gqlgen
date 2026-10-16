package shardruntime

import (
	"context"
	"fmt"
	"slices"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/99designs/gqlgen/graphql"
)

type plannedDirective struct {
	name string
	args map[string]any
}

// fieldPlan is the schema-side shape of one field: the return type's children
// for FieldContext.Child and the field's runtime directive chain. The runtime
// derives it from the parsed schema on the field's first execution, so the
// generated shard carries neither.
type fieldPlan struct {
	returnType      string
	returnKind      ast.DefinitionKind
	children        map[string]struct{}
	directives      []plannedDirective
	fieldMiddleware bool
}

func buildFieldPlan(schema *ast.Schema, scope, objectName, fieldName string) (*fieldPlan, error) {
	obj := schema.Types[objectName]
	if obj == nil {
		return nil, fmt.Errorf("type %s is not in the schema", objectName)
	}
	f := obj.Fields.ForName(fieldName)
	if f == nil {
		return nil, fmt.Errorf("field %s.%s is not in the schema", objectName, fieldName)
	}
	ret := schema.Types[f.Type.Name()]
	if ret == nil {
		return nil, fmt.Errorf(
			"return type %s of %s.%s is not in the schema",
			f.Type.Name(),
			objectName,
			fieldName,
		)
	}
	plan := &fieldPlan{returnType: ret.Name, returnKind: ret.Kind}
	if ret.Kind == ast.Object {
		plan.children = make(map[string]struct{}, len(ret.Fields))
		for _, child := range ret.Fields {
			plan.children[child.Name] = struct{}{}
		}
	}
	for _, d := range effectiveDirectives(schema, f, ast.LocationFieldDefinition) {
		if !isRuntimeDirective(scope, d.Name) {
			continue
		}
		args, err := directiveArgs(schema, d)
		if err != nil {
			return nil, err
		}
		plan.directives = append(plan.directives, plannedDirective{name: d.Name, args: args})
	}
	for name, def := range schema.Directives {
		if slices.Contains(def.Locations, ast.LocationField) && isRuntimeDirective(scope, name) {
			plan.fieldMiddleware = true
			break
		}
	}
	return plan, nil
}

func (p *fieldPlan) child(
	ec ObjectExecutionContext,
) func(context.Context, graphql.CollectedField) (*graphql.FieldContext, error) {
	switch {
	case p.returnKind == ast.Scalar || p.returnKind == ast.Enum:
		return func(context.Context, graphql.CollectedField) (*graphql.FieldContext, error) {
			return nil, fmt.Errorf("field of type %s does not have child fields", p.returnType)
		}
	case p.returnKind != ast.Object:
		return func(context.Context, graphql.CollectedField) (*graphql.FieldContext, error) {
			return nil, fmt.Errorf("FieldContext.Child cannot be called on type %s", p.returnKind)
		}
	}
	return func(ctx context.Context, field graphql.CollectedField) (*graphql.FieldContext, error) {
		if _, ok := p.children[field.Name]; !ok {
			return nil, fmt.Errorf(
				"no field named %q was found under type %s",
				field.Name,
				p.returnType,
			)
		}
		handler, ok := ec.LookupFieldContextHandler(p.returnType, field.Name)
		if !ok {
			return nil, fmt.Errorf("no field context handler for %s.%s", p.returnType, field.Name)
		}
		return handler(ctx, ec, field)
	}
}

// middleware is nil when the field has nothing to wrap, so graphql.ResolveField
// keeps its no-middleware fast path. Otherwise the planned directives wrap next
// in order, last directive outermost, and FieldMiddleware wraps the result.
func (p *fieldPlan) middleware(
	ec ObjectExecutionContext,
	obj any,
) func(context.Context, graphql.Resolver) graphql.Resolver {
	if len(p.directives) == 0 && !p.fieldMiddleware {
		return nil
	}
	return func(ctx context.Context, next graphql.Resolver) graphql.Resolver {
		for _, d := range p.directives {
			prev := next
			next = func(ctx context.Context) (any, error) {
				return ec.InvokeDirective(ctx, d.name, obj, prev, cloneArgs(d.args))
			}
		}
		if p.fieldMiddleware {
			next = ec.FieldMiddleware(ctx, obj, next)
		}
		return next
	}
}

// cloneArgs copies the raw maps and slices a directive argument may hold, so
// coercion or a directive that mutates its input never reaches the cached plan.
func cloneArgs(args map[string]any) map[string]any {
	if args == nil {
		return nil
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = cloneValue(v)
	}
	return out
}

func cloneValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return cloneArgs(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = cloneValue(e)
		}
		return out
	default:
		return v
	}
}
