package shardruntime

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/99designs/gqlgen/graphql"
)

type resolvedNode struct {
	Marks  []string
	Kid    *resolvedNode
	Kids   []*resolvedNode
	KidPP  **resolvedNode
	KidsP  *[]*resolvedNode
	Leaf   resolvedLeaf
	Leaves []resolvedLeaf
	Tag    *resolvedTag
	TagV   resolvedTag
}

type resolvedLeaf struct {
	Marks []string
}

// resolvedTag is a hybrid input modelled on a string, not a struct.
type resolvedTag string

type resolverCall struct {
	input, field, path string
	parent             any
	data               any
}

// newResolverEC decodes "str" by appending "!" so a test can see the codec ran,
// and records every resolver call while marking the parent it was handed.
func newResolverEC(calls *[]resolverCall) *fakeEC {
	return &fakeEC{
		unmarshalCodec: func(_ context.Context, name string, v any) (any, error) {
			if name != "str" {
				return nil, fmt.Errorf("unknown codec %s", name)
			}
			if v == nil {
				return nil, nil
			}
			return v.(string) + "!", nil
		},
		invokeResolver: func(ctx context.Context, input, field string, obj any) (any, error) {
			args := obj.([]any)
			*calls = append(*calls, resolverCall{
				input:  input,
				field:  field,
				path:   graphql.GetPath(ctx).String(),
				parent: args[0],
				data:   args[1],
			})
			if args[1] == "deny!" {
				return nil, errors.New("denied by resolver")
			}
			mark := fmt.Sprintf("%s=%v", field, args[1])
			switch p := args[0].(type) {
			case *resolvedNode:
				p.Marks = append(p.Marks, mark)
			case *resolvedLeaf:
				p.Marks = append(p.Marks, mark)
			case *resolvedTag:
				*p += resolvedTag(mark)
			}
			return nil, nil
		},
	}
}

func setupResolverPlans(t *testing.T) {
	t.Helper()
	resetInputResolverPlansForTest()
	t.Cleanup(resetInputResolverPlansForTest)
	RegisterInputResolverPlan("s", "Node", []InputResolverField{
		{Name: "mark", UnmarshalCodec: "str"},
		{Name: "kid", GoField: "Kid", Input: "Node"},
		{Name: "kids", GoField: "Kids", Input: "Node"},
		{Name: "kidpp", GoField: "KidPP", Input: "Node"},
		{Name: "kidsp", GoField: "KidsP", Input: "Node"},
		{Name: "leaf", GoField: "Leaf", Input: "Leaf"},
		{Name: "leaves", GoField: "Leaves", Input: "Leaf"},
		{Name: "tag", GoField: "Tag", Input: "Tag"},
		{Name: "tagv", GoField: "TagV", Input: "Tag"},
		{Name: "late", UnmarshalCodec: "str"},
	})
	RegisterInputResolverPlan(
		"s",
		"Tag",
		[]InputResolverField{{Name: "mark", UnmarshalCodec: "str"}},
	)
	RegisterInputResolverPlan(
		"s",
		"Leaf",
		[]InputResolverField{{Name: "mark", UnmarshalCodec: "str"}},
	)
}

func paths(calls []resolverCall) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.path)
	}
	return out
}

func TestApplyInputFieldResolvers_RunsInSchemaOrderOnTheDecodedObject(t *testing.T) {
	setupResolverPlans(t)
	var calls []resolverCall
	ec := newResolverEC(&calls)
	typed := &resolvedNode{Kid: &resolvedNode{}}
	raw := map[string]any{"late": "l", "mark": "m", "kid": map[string]any{"mark": "k"}}

	require.NoError(t, ApplyInputFieldResolvers(context.Background(), ec, "s", "Node", typed, raw))

	require.Equal(t, []string{"mark", "kid.mark", "late"}, paths(calls),
		"schema order, depth first, regardless of map order")
	require.Equal(
		t,
		[]string{"mark=m!", "late=l!"},
		typed.Marks,
		"the codec result reaches the resolver",
	)
	require.Equal(t, []string{"mark=k!"}, typed.Kid.Marks)
	require.Same(t, typed, calls[0].parent, "the top-level resolver gets the caller's pointer")
	require.Same(
		t,
		typed.Kid,
		calls[1].parent,
		"a nested resolver gets the pointer the parent holds",
	)
	require.Equal(t, "Node", calls[1].input)
}

func TestApplyInputFieldResolvers_SkipsAbsentFieldsAndPassesNullAsNilData(t *testing.T) {
	setupResolverPlans(t)
	var calls []resolverCall
	ec := newResolverEC(&calls)
	typed := &resolvedNode{}

	require.NoError(
		t,
		ApplyInputFieldResolvers(context.Background(), ec, "s", "Node", typed, map[string]any{}),
	)
	require.Empty(t, calls)

	require.NoError(t, ApplyInputFieldResolvers(
		context.Background(),
		ec,
		"s",
		"Node",
		typed,
		map[string]any{"mark": nil, "kid": nil, "kids": nil},
	))
	require.Len(t, calls, 1, "a null nested input is skipped, a null resolver field is not")
	require.Nil(t, calls[0].data)
	require.Equal(t, []string{"mark=<nil>"}, typed.Marks)
}

func TestApplyInputFieldResolvers_DescendsIntoListsStructsAndSlicesOfStructs(t *testing.T) {
	setupResolverPlans(t)
	var calls []resolverCall
	ec := newResolverEC(&calls)
	typed := &resolvedNode{
		Kids:   []*resolvedNode{{}, {Kids: []*resolvedNode{{}}}},
		Leaves: []resolvedLeaf{{}},
	}
	raw := map[string]any{
		"kids": []any{
			map[string]any{"mark": "a"},
			map[string]any{"mark": "b", "kids": []any{map[string]any{"mark": "bb"}}},
		},
		"leaf":   map[string]any{"mark": "c"},
		"leaves": []any{map[string]any{"mark": "d"}},
	}

	require.NoError(t, ApplyInputFieldResolvers(context.Background(), ec, "s", "Node", typed, raw))

	require.Equal(
		t,
		[]string{
			"kids[0].mark",
			"kids[1].mark",
			"kids[1].kids[0].mark",
			"leaf.mark",
			"leaves[0].mark",
		},
		paths(calls),
	)
	require.Equal(t, []string{"mark=a!"}, typed.Kids[0].Marks)
	require.Equal(t, []string{"mark=b!"}, typed.Kids[1].Marks)
	require.Equal(t, []string{"mark=bb!"}, typed.Kids[1].Kids[0].Marks)
	require.Equal(t, []string{"mark=c!"}, typed.Leaf.Marks, "a struct value is addressed in place")
	require.Equal(
		t,
		[]string{"mark=d!"},
		typed.Leaves[0].Marks,
		"a slice element is addressed in place",
	)
	require.Equal(t, "Leaf", calls[3].input)
}

func TestApplyInputFieldResolvers_UnwrapsPointerToPointerAndPointerToSlice(t *testing.T) {
	setupResolverPlans(t)
	var calls []resolverCall
	ec := newResolverEC(&calls)
	kid := &resolvedNode{}
	kids := []*resolvedNode{{}}
	typed := &resolvedNode{KidPP: &kid, KidsP: &kids}
	raw := map[string]any{
		"kidpp": map[string]any{"mark": "a"},
		"kidsp": []any{map[string]any{"mark": "b"}},
	}

	require.NoError(t, ApplyInputFieldResolvers(context.Background(), ec, "s", "Node", typed, raw))

	require.Equal(t, []string{"kidpp.mark", "kidsp[0].mark"}, paths(calls))
	require.Equal(t, []string{"mark=a!"}, kid.Marks)
	require.Equal(t, []string{"mark=b!"}, kids[0].Marks)
	require.IsType(
		t,
		&resolvedNode{},
		calls[0].parent,
		"the resolver is handed the struct pointer, not the pointer to it",
	)

	err := ApplyInputFieldResolvers(
		context.Background(),
		ec,
		"s",
		"Node",
		&resolvedNode{},
		map[string]any{"kidsp": []any{map[string]any{"mark": "c"}}},
	)
	require.ErrorContains(t, err, "input Node: decoded to nil")
}

func TestApplyInputFieldResolvers_ReachesANonStructModelThroughItsReference(t *testing.T) {
	setupResolverPlans(t)
	var calls []resolverCall
	ec := newResolverEC(&calls)
	tag := resolvedTag("t:")
	typed := &resolvedNode{Tag: &tag, TagV: "v:"}
	raw := map[string]any{
		"tag":  map[string]any{"mark": "a"},
		"tagv": map[string]any{"mark": "b"},
	}

	require.NoError(t, ApplyInputFieldResolvers(context.Background(), ec, "s", "Node", typed, raw))

	require.Equal(t, []string{"tag.mark", "tagv.mark"}, paths(calls))
	require.Equal(t, resolvedTag("t:mark=a!"), *typed.Tag)
	require.Equal(t, resolvedTag("v:mark=b!"), typed.TagV, "a value field is addressed in place")
	require.IsType(
		t,
		&tag,
		calls[1].parent,
		"the invoker gets the pointer the model's resolvers take",
	)
}

func TestApplyInputFieldResolvers_ResolverErrorCarriesTheFullPath(t *testing.T) {
	setupResolverPlans(t)
	var calls []resolverCall
	ec := newResolverEC(&calls)
	typed := &resolvedNode{Kids: []*resolvedNode{{}, {}}}
	raw := map[string]any{
		"kids": []any{map[string]any{"mark": "ok"}, map[string]any{"mark": "deny"}},
	}

	err := ApplyInputFieldResolvers(context.Background(), ec, "s", "Node", typed, raw)

	require.ErrorContains(t, err, "denied by resolver")
	var gqlErr *gqlerror.Error
	require.ErrorAs(t, err, &gqlErr)
	require.Equal(
		t,
		ast.Path{ast.PathName("kids"), ast.PathIndex(1), ast.PathName("mark")},
		gqlErr.Path,
	)
}

func TestApplyInputFieldResolvers_CoercesASingleValueToAList(t *testing.T) {
	setupResolverPlans(t)
	var calls []resolverCall
	ec := newResolverEC(&calls)
	typed := &resolvedNode{Kids: []*resolvedNode{{}}}

	require.NoError(t, ApplyInputFieldResolvers(
		context.Background(),
		ec,
		"s",
		"Node",
		typed,
		map[string]any{"kids": map[string]any{"mark": "a"}},
	))
	require.Equal(t, []string{"kids[0].mark"}, paths(calls))
}

func TestApplyInputFieldResolvers_SkipsNullListElements(t *testing.T) {
	setupResolverPlans(t)
	var calls []resolverCall
	ec := newResolverEC(&calls)
	typed := &resolvedNode{Kids: []*resolvedNode{nil, {}}}

	require.NoError(t, ApplyInputFieldResolvers(
		context.Background(),
		ec,
		"s",
		"Node",
		typed,
		map[string]any{"kids": []any{nil, map[string]any{"mark": "a"}}},
	))
	require.Equal(t, []string{"kids[1].mark"}, paths(calls))
}

// A decoder that did not produce what the raw value says is there would leave
// nested resolvers silently unrun, so the walk fails instead of skipping.
func TestApplyInputFieldResolvers_FailsClosedWhenTypedAndRawDisagree(t *testing.T) {
	setupResolverPlans(t)
	var calls []resolverCall
	ec := newResolverEC(&calls)

	err := ApplyInputFieldResolvers(
		context.Background(),
		ec,
		"s",
		"Node",
		&resolvedNode{},
		map[string]any{"kid": map[string]any{"mark": "a"}},
	)
	require.ErrorContains(t, err, "input Node: decoded to nil")

	err = ApplyInputFieldResolvers(
		context.Background(), ec, "s", "Node", &resolvedNode{Kids: []*resolvedNode{{}}},
		map[string]any{"kids": []any{map[string]any{}, map[string]any{}}},
	)
	require.ErrorContains(t, err, "input Node: 1 values decoded from 2")

	err = ApplyInputFieldResolvers(
		context.Background(),
		ec,
		"s",
		"Node",
		&resolvedNode{Kid: &resolvedNode{}},
		map[string]any{"kid": "notamap"},
	)
	require.ErrorContains(t, err, "expected map[string]any, got string")

	err = ApplyInputFieldResolvers(
		context.Background(),
		ec,
		"s",
		"Node",
		resolvedNode{},
		map[string]any{"kid": map[string]any{}},
	)
	require.ErrorContains(t, err, "not a pointer to a struct")
	require.Empty(t, calls)
}

func TestApplyInputFieldResolvers_MissingPlanIsAnError(t *testing.T) {
	resetInputResolverPlansForTest()
	t.Cleanup(resetInputResolverPlansForTest)
	var calls []resolverCall
	ec := newResolverEC(&calls)

	err := ApplyInputFieldResolvers(
		context.Background(),
		ec,
		"s",
		"Node",
		&resolvedNode{},
		map[string]any{"mark": "m"},
	)
	require.ErrorContains(t, err, "no resolver plan registered")

	RegisterInputResolverPlan(
		"s",
		"Node",
		[]InputResolverField{{Name: "nope", GoField: "Nope", Input: "Node"}},
	)
	err = ApplyInputFieldResolvers(
		context.Background(),
		ec,
		"s",
		"Node",
		&resolvedNode{},
		map[string]any{"nope": map[string]any{}},
	)
	require.ErrorContains(t, err, "has no field Nope")
}

func TestStripInputResolverFields_RemovesResolverKeysAtEveryLevelAndKeepsRaw(t *testing.T) {
	setupResolverPlans(t)
	raw := map[string]any{
		"mark":  "m",
		"plain": "p",
		"kid": map[string]any{
			"mark": "k",
			"kids": []any{map[string]any{"mark": "kk", "plain": "q"}},
		},
		"leaves": []any{map[string]any{"mark": "l", "x": 1}, nil},
	}

	got := StripInputResolverFields("s", "Node", raw)

	require.Equal(t, map[string]any{
		"plain":  "p",
		"kid":    map[string]any{"kids": []any{map[string]any{"plain": "q"}}},
		"leaves": []any{map[string]any{"x": 1}, nil},
	}, got)
	require.Equal(t, "m", raw["mark"], "raw keeps the resolver keys for ApplyInputFieldResolvers")
	require.Equal(t, "k", raw["kid"].(map[string]any)["mark"])
	require.Equal(t, "kk", raw["kid"].(map[string]any)["kids"].([]any)[0].(map[string]any)["mark"])
}

func TestStripInputResolverFields_WithoutAPlanReturnsRawItself(t *testing.T) {
	resetInputResolverPlansForTest()
	t.Cleanup(resetInputResolverPlansForTest)
	raw := map[string]any{"mark": "m"}

	got := StripInputResolverFields("s", "Node", raw)

	got["probe"] = 1
	require.Contains(t, raw, "probe", "no plan, no copy")
}
