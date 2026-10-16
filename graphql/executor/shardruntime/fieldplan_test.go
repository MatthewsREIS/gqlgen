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

type Query {
  deal: Deal! @second
  plain: Int
  input: String
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
	_, err := chain(context.Background(), func(context.Context) (any, error) { return "r", nil })(context.Background())
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
	_, err := (&fieldPlan{returnType: "UUID", returnKind: ast.Scalar}).child(ec)(context.Background(), graphql.CollectedField{})
	require.EqualError(t, err, "field of type UUID does not have child fields")
	_, err = (&fieldPlan{returnType: "MyInput", returnKind: ast.InputObject}).child(ec)(context.Background(), graphql.CollectedField{})
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
	plan := &fieldPlan{returnType: "Escrow", returnKind: ast.Object, children: map[string]struct{}{"id": {}, "address": {}}}
	child := plan.child(ec)

	fc, err := child(context.Background(), graphql.CollectedField{Field: &ast.Field{Name: "id"}})
	require.NoError(t, err)
	require.Equal(t, "Escrow", fc.Object)

	_, err = child(context.Background(), graphql.CollectedField{Field: &ast.Field{Name: "nonexistent"}})
	require.EqualError(t, err, `no field named "nonexistent" was found under type Escrow`)

	_, err = child(context.Background(), graphql.CollectedField{Field: &ast.Field{Name: "address"}})
	require.EqualError(t, err, "no field context handler for Escrow.address")
}
