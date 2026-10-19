package shardruntime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/99designs/gqlgen/graphql"
)

const fieldPlanSchema = `
directive @first(location: String!) on FIELD_DEFINITION | OBJECT
directive @second(note: String = "dflt") on FIELD_DEFINITION
directive @codegenOnly on FIELD_DEFINITION | OBJECT
directive @inputish on OBJECT | INPUT_OBJECT
directive @logged(id: String!) on FIELD
directive @fd on SCALAR | FIELD_DEFINITION
directive @sc on SCALAR

scalar Money @fd @sc

type Query {
  deal: Deal! @second
  plain: Int
  input: String
  price: Money
}

type Deal @first(location: "type") @codegenOnly @inputish {
  id: ID!
  name: String @first(location: "field") @second(note: "n")
}
`

func loadFieldPlanSchema(t *testing.T) *ast.Schema {
	t.Helper()
	return gqlparser.MustLoadSchema(&ast.Source{Name: "plan.graphql", Input: fieldPlanSchema})
}

func TestBuildFieldPlan_ReturnShape(t *testing.T) {
	resetInputDirectivePlansForTest()
	RegisterRuntimeDirectives("s", "first", "second")
	schema := loadFieldPlanSchema(t)

	deal, err := buildFieldPlan(schema, "s", "Query", "deal")
	require.NoError(t, err)
	require.Equal(t, "Deal", deal.returnType)
	require.Equal(t, ast.Object, deal.returnKind)
	require.Equal(t, map[string]struct{}{"id": {}, "name": {}}, deal.children)

	plain, err := buildFieldPlan(schema, "s", "Query", "plain")
	require.NoError(t, err)
	require.Equal(t, "Int", plain.returnType)
	require.Equal(t, ast.Scalar, plain.returnKind)
	require.Nil(t, plain.children)
}

func TestBuildFieldPlan_DirectivesInheritFilterAndDefault(t *testing.T) {
	resetInputDirectivePlansForTest()
	RegisterRuntimeDirectives("s", "first", "second", "inputish")
	schema := loadFieldPlanSchema(t)

	plan, err := buildFieldPlan(schema, "s", "Query", "deal")
	require.NoError(t, err)
	require.Equal(t, []plannedDirective{
		{name: "first", args: map[string]any{"location": "type"}},
		{name: "second", args: map[string]any{"note": "dflt"}},
	}, plan.directives)

	name, err := buildFieldPlan(schema, "s", "Deal", "name")
	require.NoError(t, err)
	require.Equal(t, []plannedDirective{
		{name: "first", args: map[string]any{"location": "field"}},
		{name: "second", args: map[string]any{"note": "n"}},
	}, name.directives)
}

func TestFieldDirectives_MatchesBuildFieldPlan(t *testing.T) {
	resetInputDirectivePlansForTest()
	RegisterRuntimeDirectives("agree", "first", "second", "fd", "sc", "inputish")
	schema := loadFieldPlanSchema(t)
	isRuntime := func(name string) bool { return isRuntimeDirective("agree", name) }

	for field, want := range map[[2]string][]PlannedDirective{
		{"Query", "deal"}: {
			{Name: "first", Args: map[string]any{"location": "type"}},
			{Name: "second", Args: map[string]any{"note": "dflt"}},
		},
		{"Deal", "name"}: {
			{Name: "first", Args: map[string]any{"location": "field"}},
			{Name: "second", Args: map[string]any{"note": "n"}},
		},
		{"Query", "price"}: {{Name: "fd"}},
	} {
		plan, err := buildFieldPlan(schema, "agree", field[0], field[1])
		require.NoError(t, err)
		var planned []PlannedDirective
		for _, d := range plan.directives {
			planned = append(planned, PlannedDirective{Name: d.name, Args: d.args})
		}
		got, err := FieldDirectives(
			schema,
			field[0],
			field[1],
			ast.LocationFieldDefinition,
			isRuntime,
		)
		require.NoError(t, err)
		require.Equal(t, planned, got, "%s.%s", field[0], field[1])
		require.Equal(t, want, got, "%s.%s", field[0], field[1])
	}
}

func TestBuildFieldPlan_FieldMiddlewarePredicate(t *testing.T) {
	resetInputDirectivePlansForTest()
	schema := loadFieldPlanSchema(t)

	RegisterRuntimeDirectives("with", "first", "second", "logged")
	with, err := buildFieldPlan(schema, "with", "Query", "plain")
	require.NoError(t, err)
	require.True(t, with.fieldMiddleware)

	RegisterRuntimeDirectives("without", "first", "second")
	without, err := buildFieldPlan(schema, "without", "Query", "plain")
	require.NoError(t, err)
	require.False(t, without.fieldMiddleware)
}

func TestBuildFieldPlan_UnknownField(t *testing.T) {
	resetInputDirectivePlansForTest()
	schema := loadFieldPlanSchema(t)

	_, err := buildFieldPlan(schema, "s", "Query", "missing")
	require.EqualError(t, err, "field Query.missing is not in the schema")
	_, err = buildFieldPlan(schema, "s", "Nope", "x")
	require.EqualError(t, err, "type Nope is not in the schema")
}

func TestFieldPlanMiddleware_OrderAndNil(t *testing.T) {
	var calls []string
	ec := &fakeEC{
		invokeDirective: func(ctx context.Context, name string, _ any, next graphql.Resolver, _ map[string]any) (any, error) {
			calls = append(calls, name)
			return next(ctx)
		},
		fieldMiddleware: func(_ context.Context, _ any, next graphql.Resolver) graphql.Resolver {
			calls = append(calls, "FIELD")
			return next
		},
	}

	empty := &fieldPlan{}
	require.Nil(t, empty.middleware(ec, "obj"))

	onlyField := &fieldPlan{fieldMiddleware: true}
	chain := onlyField.middleware(ec, "obj")
	require.NotNil(t, chain)
	_, err := chain(
		context.Background(),
		func(context.Context) (any, error) { return "r", nil },
	)(
		context.Background(),
	)
	require.NoError(t, err)
	require.Equal(t, []string{"FIELD"}, calls)

	calls = nil
	two := &fieldPlan{directives: []plannedDirective{{name: "first"}, {name: "second"}}}
	res, err := two.middleware(ec, "obj")(context.Background(), func(context.Context) (any, error) {
		calls = append(calls, "resolve")
		return "r", nil
	})(context.Background())
	require.NoError(t, err)
	require.Equal(t, "r", res)
	require.Equal(t, []string{"second", "first", "resolve"}, calls)
}

func TestFieldPlanChild_ScalarAndNonObject(t *testing.T) {
	ec := &fakeEC{}
	_, err := (&fieldPlan{returnType: "UUID", returnKind: ast.Scalar}).child(
		ec,
	)(
		context.Background(),
		graphql.CollectedField{},
	)
	require.EqualError(t, err, "field of type UUID does not have child fields")
	_, err = (&fieldPlan{returnType: "MyInput", returnKind: ast.InputObject}).child(
		ec,
	)(
		context.Background(),
		graphql.CollectedField{},
	)
	require.EqualError(t, err, "FieldContext.Child cannot be called on type INPUT_OBJECT")
}

func TestFieldPlanChild_Object(t *testing.T) {
	ec := &fakeEC{
		fieldContextHandlers: map[string]FieldContextHandler{
			"Escrow.id": func(_ context.Context, _ ObjectExecutionContext, _ graphql.CollectedField) (*graphql.FieldContext, error) {
				return &graphql.FieldContext{Object: "Escrow"}, nil
			},
		},
	}
	plan := &fieldPlan{
		returnType: "Escrow",
		returnKind: ast.Object,
		children:   map[string]struct{}{"id": {}, "address": {}},
	}
	child := plan.child(ec)

	fc, err := child(context.Background(), graphql.CollectedField{Field: &ast.Field{Name: "id"}})
	require.NoError(t, err)
	require.Equal(t, "Escrow", fc.Object)

	_, err = child(
		context.Background(),
		graphql.CollectedField{Field: &ast.Field{Name: "nonexistent"}},
	)
	require.EqualError(t, err, `no field named "nonexistent" was found under type Escrow`)

	_, err = child(context.Background(), graphql.CollectedField{Field: &ast.Field{Name: "address"}})
	require.EqualError(t, err, "no field context handler for Escrow.address")
}

func TestBuildFieldPlan_InheritedScalarDirectivesUseFieldLocation(t *testing.T) {
	resetInputDirectivePlansForTest()
	RegisterRuntimeDirectives("s", "fd", "sc")
	schema := loadFieldPlanSchema(t)

	plan, err := buildFieldPlan(schema, "s", "Query", "price")
	require.NoError(t, err)
	require.Equal(t, []plannedDirective{{name: "fd"}}, plan.directives)
}

func TestFieldPlanMiddleware_ArgsObjAndFieldOutermost(t *testing.T) {
	var calls []string
	var gotArgs map[string]any
	var gotObj any
	ec := &fakeEC{
		invokeDirective: func(ctx context.Context, name string, obj any, next graphql.Resolver, args map[string]any) (any, error) {
			calls = append(calls, name)
			gotArgs, gotObj = args, obj
			return next(ctx)
		},
		fieldMiddleware: func(_ context.Context, _ any, next graphql.Resolver) graphql.Resolver {
			return func(ctx context.Context) (any, error) {
				calls = append(calls, "FIELD")
				return next(ctx)
			}
		},
	}
	plan := &fieldPlan{
		directives:      []plannedDirective{{name: "first", args: map[string]any{"scope": "s"}}},
		fieldMiddleware: true,
	}

	_, err := plan.middleware(
		ec,
		"parent",
	)(
		context.Background(),
		func(context.Context) (any, error) {
			calls = append(calls, "resolve")
			return "r", nil
		},
	)(
		context.Background(),
	)
	require.NoError(t, err)
	require.Equal(t, []string{"FIELD", "first", "resolve"}, calls)
	require.Equal(t, map[string]any{"scope": "s"}, gotArgs)
	require.Equal(t, "parent", gotObj)
}

func TestFieldPlanMiddleware_ArgsAreCopiedPerInvocation(t *testing.T) {
	type seen struct {
		opt    any
		scope  any
		hasNew bool
	}
	var calls []seen
	ec := &fakeEC{
		invokeDirective: func(ctx context.Context, _ string, _ any, next graphql.Resolver, args map[string]any) (any, error) {
			opts := args["opts"].(map[string]any)
			scopes := args["scopes"].([]any)[0].([]any)
			_, hasNew := args["new"]
			calls = append(calls, seen{opt: opts["k"], scope: scopes[0], hasNew: hasNew})
			opts["k"] = "changed"
			scopes[0] = "changed"
			args["new"] = 1
			return next(ctx)
		},
	}
	plan := &fieldPlan{directives: []plannedDirective{{
		name: "first",
		args: map[string]any{"scopes": []any{[]any{"a"}}, "opts": map[string]any{"k": "v"}},
	}}}

	resolve := func(context.Context) (any, error) { return "r", nil }
	for range 2 {
		_, err := plan.middleware(ec, "obj")(context.Background(), resolve)(context.Background())
		require.NoError(t, err)
	}
	require.Equal(t, []seen{{opt: "v", scope: "a"}, {opt: "v", scope: "a"}}, calls)
	require.Equal(
		t,
		map[string]any{"scopes": []any{[]any{"a"}}, "opts": map[string]any{"k": "v"}},
		plan.directives[0].args,
	)
}
