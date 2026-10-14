package shardruntime

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/99designs/gqlgen/graphql"
)

const walkerSchema = `
directive @gate(need: [[String!]], mode: String = "read") on INPUT_FIELD_DEFINITION
directive @codegenOnly on INPUT_FIELD_DEFINITION

input Top {
	plain: String
	gated: String @gate(need: [["a"]])
	dflt: String = "d" @gate
	two: String @gate(need: [["1"]]) @gate(need: [["2"]])
	tagged: String @codegenOnly @gate(need: [["t"]])
	gen: Child
	kid: Child
	kids: [Child!]
	plainKid: Plain
}

input Child {
	inner: String @gate(need: [["c"]])
	deeper: Child
}

input Plain {
	x: String @gate(need: [["p"]])
}

type Query { q(arg: Top): String }
`

type directiveCall struct {
	name string
	args map[string]any
	path string
	obj  any
}

func stripObj(calls []directiveCall) []directiveCall {
	out := make([]directiveCall, 0, len(calls))
	for _, c := range calls {
		c.obj = nil
		out = append(out, c)
	}
	return out
}

func newWalkerEC(t *testing.T, calls *[]directiveCall) *fakeEC {
	t.Helper()
	schema := gqlparser.MustLoadSchema(&ast.Source{Name: "walker.graphql", Input: walkerSchema})
	return &fakeEC{
		schema: schema,
		invokeDirective: func(
			ctx context.Context,
			name string,
			obj any,
			next graphql.Resolver,
			args map[string]any,
		) (any, error) {
			*calls = append(*calls, directiveCall{
				name: name,
				args: args,
				path: graphql.GetPath(ctx).String(),
				obj:  obj,
			})
			res, err := next(ctx)
			if err != nil {
				return nil, err
			}
			if s, ok := res.(string); ok {
				if s == "deny" {
					return nil, errors.New("denied")
				}
				return s + "!", nil
			}
			return res, nil
		},
	}
}

func setupWalkerPlans(t *testing.T) {
	t.Helper()
	resetInputDirectivePlansForTest()
	t.Cleanup(resetInputDirectivePlansForTest)
	RegisterInputDirectivePlan("s", "Top", []string{"gen"})
	RegisterInputDirectivePlan("s", "Child", nil)
}

func TestApplyInputDirectives_RunsDirectivesWithArgsAndDefaults(t *testing.T) {
	setupWalkerPlans(t)
	var calls []directiveCall
	ec := newWalkerEC(t, &calls)
	raw := map[string]any{"plain": "p", "gated": "x"}

	require.NoError(
		t,
		ApplyInputDirectives(context.Background(), ec, "s", "Top", maps.Clone(raw), raw),
	)

	require.Equal(t, "x!", raw["gated"], "directive result replaces the raw value")
	require.Equal(t, "p", raw["plain"], "a field without directives is untouched")
	require.Equal(t, "d!", raw["dflt"],
		"the schema default is applied, then the directive runs on it")
	require.Equal(t, []directiveCall{
		{
			name: "gate",
			args: map[string]any{"need": []any{[]any{"a"}}, "mode": "read"},
			path: "gated",
		},
		{name: "gate", args: map[string]any{"mode": "read"}, path: "dflt"},
	}, stripObj(calls))
}

func TestApplyInputDirectives_LastDirectiveIsOutermost(t *testing.T) {
	setupWalkerPlans(t)
	var calls []directiveCall
	ec := newWalkerEC(t, &calls)
	raw := map[string]any{"two": "v"}

	require.NoError(
		t,
		ApplyInputDirectives(context.Background(), ec, "s", "Top", maps.Clone(raw), raw),
	)

	require.Equal(t, "v!!", raw["two"])
	var two []directiveCall
	for _, c := range calls {
		if c.path == "two" {
			two = append(two, c)
		}
	}
	require.Len(t, two, 2)
	require.Equal(
		t,
		[]any{[]any{"2"}},
		two[0].args["need"],
		"the last declared directive wraps the first",
	)
	require.Equal(t, []any{[]any{"1"}}, two[1].args["need"])
}

func TestApplyInputDirectives_SkipsGeneratedFields(t *testing.T) {
	setupWalkerPlans(t)
	var calls []directiveCall
	ec := newWalkerEC(t, &calls)
	raw := map[string]any{"gen": map[string]any{"inner": "deny"}}

	require.NoError(
		t,
		ApplyInputDirectives(context.Background(), ec, "s", "Top", maps.Clone(raw), raw),
	)
	require.Equal(
		t,
		[]directiveCall{{name: "gate", args: map[string]any{"mode": "read"}, path: "dflt"}},
		stripObj(calls),
		"a field the generated arm owns is neither gated nor walked here; only the defaulted field fires",
	)
}

func TestApplyInputDirectives_RecursesIntoPlannedNestedInputs(t *testing.T) {
	setupWalkerPlans(t)
	var calls []directiveCall
	ec := newWalkerEC(t, &calls)
	raw := map[string]any{
		"kid":  map[string]any{"inner": "i", "deeper": map[string]any{"inner": "dd"}},
		"kids": []any{map[string]any{"inner": "k0"}, map[string]any{"inner": "deny"}},
	}

	err := ApplyInputDirectives(context.Background(), ec, "s", "Top", maps.Clone(raw), raw)

	require.ErrorContains(t, err, "denied")
	var gqlErr *gqlerror.Error
	require.ErrorAs(t, err, &gqlErr)
	require.Equal(
		t,
		ast.Path{ast.PathName("kids"), ast.PathIndex(1), ast.PathName("inner")},
		gqlErr.Path,
	)
	paths := make([]string, 0, len(calls))
	for _, c := range calls {
		paths = append(paths, c.path)
	}
	require.Equal(
		t,
		[]string{"dflt", "kid.inner", "kid.deeper.inner", "kids[0].inner", "kids[1].inner"},
		paths,
	)
	require.Equal(
		t,
		"i!",
		raw["kid"].(map[string]any)["inner"],
		"nested values are rewritten in place",
	)
	require.Equal(t, "dd!", raw["kid"].(map[string]any)["deeper"].(map[string]any)["inner"])
}

func TestApplyInputDirectives_DoesNotWalkUnplannedNestedInputs(t *testing.T) {
	setupWalkerPlans(t)
	var calls []directiveCall
	ec := newWalkerEC(t, &calls)
	raw := map[string]any{"plainKid": map[string]any{"x": "deny"}}

	require.NoError(
		t,
		ApplyInputDirectives(context.Background(), ec, "s", "Top", maps.Clone(raw), raw),
	)
	require.Equal(
		t,
		[]directiveCall{{name: "gate", args: map[string]any{"mode": "read"}, path: "dflt"}},
		stripObj(calls),
		"a nested input without a plan has no runtime decoder to protect; only the defaulted field fires",
	)
}

func TestApplyInputDirectives_MissingPlanIsAnError(t *testing.T) {
	resetInputDirectivePlansForTest()
	t.Cleanup(resetInputDirectivePlansForTest)
	var calls []directiveCall
	ec := newWalkerEC(t, &calls)

	err := ApplyInputDirectives(
		context.Background(),
		ec,
		"s",
		"Top",
		nil,
		map[string]any{"gated": "x"},
	)
	require.ErrorContains(t, err, "no directive plan registered")
}

// The directive object is what the generated arm passed: the input as the
// unmarshaler received it, before defaults and before sibling results, and a
// nested field sees its own nested map the same way.
func TestApplyInputDirectives_DirectivesSeeTheOriginalObject(t *testing.T) {
	setupWalkerPlans(t)
	var calls []directiveCall
	ec := newWalkerEC(t, &calls)
	orig := map[string]any{"gated": "x", "kid": map[string]any{"inner": "i"}}
	raw := maps.Clone(orig)

	require.NoError(t, ApplyInputDirectives(context.Background(), ec, "s", "Top", orig, raw))

	byPath := map[string]directiveCall{}
	for _, c := range calls {
		byPath[c.path] = c
	}
	require.Equal(t, orig, byPath["gated"].obj, "the top-level object is the caller's value")
	_, hasDefault := byPath["gated"].obj.(map[string]any)["dflt"]
	require.False(
		t,
		hasDefault,
		"defaults are applied to the decoded map, not to the directive object",
	)
	require.Equal(t, map[string]any{"inner": "i"}, byPath["kid.inner"].obj,
		"a nested field sees its nested map as received, before its own rewrite")
	require.Equal(t, "i!", raw["kid"].(map[string]any)["inner"])
}

func TestApplyInputDirectives_SkipsDirectivesOutsideTheRuntimeSet(t *testing.T) {
	setupWalkerPlans(t)
	RegisterRuntimeDirectives("s", "gate")
	var calls []directiveCall
	ec := newWalkerEC(t, &calls)
	raw := map[string]any{"tagged": "v"}

	require.NoError(
		t,
		ApplyInputDirectives(context.Background(), ec, "s", "Top", maps.Clone(raw), raw),
	)

	require.Equal(t, "v!", raw["tagged"], "the runtime directive still runs")
	for _, c := range calls {
		require.NotEqual(
			t,
			"codegenOnly",
			c.name,
			"a skip_runtime directive has no implementation to call",
		)
	}
}
