package splitpackages

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/99designs/gqlgen/client"
	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/transport"
)

func describeOptional(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func describeResolvedChild(c *ResolvedChildWhereInput) string {
	if c == nil {
		return "<nil>"
	}
	s := fmt.Sprintf("{label=%s preds=%v", describeOptional(c.Label), c.Predicates)
	if c.Not != nil {
		s += " not=" + describeResolvedChild(c.Not)
	}
	return s + "}"
}

func describeResolved(in *ResolvedWhereInput) string {
	if in == nil {
		return "<nil>"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "{name=%s preds=%v", describeOptional(in.Name), in.Predicates)
	if in.Not != nil {
		b.WriteString(" not=" + describeResolved(in.Not))
	}
	for _, a := range in.And {
		b.WriteString(" and=" + describeResolved(a))
	}
	for _, c := range in.HasChildWith {
		b.WriteString(" child=" + describeResolvedChild(c))
	}
	b.WriteString("}")
	return b.String()
}

// newResolvedServer wires an ownedBy resolver that appends a predicate to the
// object it is handed, rejects "DENY", and records the path of every call; the
// @toUpper directive records its own paths into directiveCalls.
func newResolvedServer(t *testing.T, calls, directiveCalls *[]string) *client.Client {
	t.Helper()
	ownedBy := func(ctx context.Context, data *string) (string, error) {
		*calls = append(*calls, graphql.GetPath(ctx).String())
		if data != nil && *data == "DENY" {
			return "", errors.New("denied by resolver")
		}
		return "owned_by=" + describeOptional(data), nil
	}
	resolvers := &Stub{}
	resolvers.QueryResolver.ResolvedWhere = func(ctx context.Context, arg ResolvedWhereInput) (string, error) {
		return describeResolved(&arg), nil
	}
	resolvers.ResolvedWhereInputResolver.OwnedBy = func(ctx context.Context, obj *ResolvedWhereInput, data *string) error {
		pred, err := ownedBy(ctx, data)
		if err != nil {
			return err
		}
		obj.Predicates = append(obj.Predicates, pred)
		return nil
	}
	resolvers.ResolvedChildWhereInputResolver.OwnedBy = func(ctx context.Context, obj *ResolvedChildWhereInput, data *string) error {
		pred, err := ownedBy(ctx, data)
		if err != nil {
			return err
		}
		obj.Predicates = append(obj.Predicates, pred)
		return nil
	}
	resolvers.ResolvedWhereInputResolver.Owner = func(ctx context.Context, obj *ResolvedWhereInput, data *ResolvedOwnerInput) error {
		obj.Predicates = append(obj.Predicates, "owner="+data.Name)
		return nil
	}
	srv := handler.New(NewExecutableSchema(Config{
		Resolvers: resolvers,
		Directives: DirectiveRoot{
			ToUpper: func(ctx context.Context, obj any, next graphql.Resolver) (any, error) {
				*directiveCalls = append(*directiveCalls, graphql.GetPath(ctx).String())
				res, err := next(ctx)
				if err != nil || res == nil {
					return res, err
				}
				return strings.ToUpper(res.(string)), nil
			},
		},
	}))
	srv.AddTransport(transport.POST{})
	return client.New(srv)
}

// The resolver runs at the top level, inside the pointer nesting (not), inside
// the list nesting (and, hasChildWith) and two levels down each, and every
// predicate lands on the object that carried the field. The directive on the
// resolver-backed ownedBy runs in the walk, so the resolver sees its result; the
// directive inside the resolver-backed owner runs once, in the field's codec.
func TestResolvedInput_ResolversRunAtEveryLevelOnTheRightObject(t *testing.T) {
	var calls, directiveCalls []string
	c := newResolvedServer(t, &calls, &directiveCalls)
	var resp struct{ ResolvedWhere string }
	c.MustPost(`{ resolvedWhere(arg: {
		name: "top", ownedBy: "me", owner: {name: "bob"},
		not: {ownedBy: "you", not: {name: "deep", ownedBy: "them"}},
		and: [{name: "a0"}, {ownedBy: "a1", and: [{ownedBy: "a1.0"}]}],
		hasChildWith: [{label: "c0", ownedBy: "c"}, {not: {ownedBy: null}}]}) }`, &resp)

	require.Equal(
		t,
		`{name=top preds=[owned_by=ME owner=BOB]`+
			` not={name=<nil> preds=[owned_by=YOU] not={name=deep preds=[owned_by=THEM]}}`+
			` and={name=a0 preds=[]}`+
			` and={name=<nil> preds=[owned_by=A1] and={name=<nil> preds=[owned_by=A1.0]}}`+
			` child={label=c0 preds=[owned_by=c]}`+
			` child={label=<nil> preds=[] not={label=<nil> preds=[owned_by=<nil>]}}}`,
		resp.ResolvedWhere,
	)
	require.Equal(t, []string{
		"resolvedWhere.arg.ownedBy",
		"resolvedWhere.arg.not.ownedBy",
		"resolvedWhere.arg.not.not.ownedBy",
		"resolvedWhere.arg.and[1].ownedBy",
		"resolvedWhere.arg.and[1].and[0].ownedBy",
		"resolvedWhere.arg.hasChildWith[0].ownedBy",
		"resolvedWhere.arg.hasChildWith[1].not.ownedBy",
	}, calls, "schema order, depth first, with list indexes in the path")
	require.Equal(t, []string{
		"resolvedWhere.arg.ownedBy",
		"resolvedWhere.arg.not.ownedBy",
		"resolvedWhere.arg.not.not.ownedBy",
		"resolvedWhere.arg.and[1].ownedBy",
		"resolvedWhere.arg.and[1].and[0].ownedBy",
		"resolvedWhere.arg.owner.name",
	}, directiveCalls, "the walk runs each field directive once; owner.name runs once, in the codec")
}

// An explicit null on a directive-bearing resolver-backed field reaches the
// resolver as nil data.
func TestResolvedInput_NullDirectedResolverFieldReachesTheResolver(t *testing.T) {
	var calls, directiveCalls []string
	c := newResolvedServer(t, &calls, &directiveCalls)
	var resp struct{ ResolvedWhere string }
	c.MustPost(`{ resolvedWhere(arg: {ownedBy: null}) }`, &resp)
	require.Equal(t, `{name=<nil> preds=[owned_by=<nil>]}`, resp.ResolvedWhere)
	require.Equal(t, []string{"resolvedWhere.arg.ownedBy"}, directiveCalls)
}

// A rejection deep in a list fails the query and names the full path.
func TestResolvedInput_NestedResolverErrorNamesThePath(t *testing.T) {
	var calls, directiveCalls []string
	c := newResolvedServer(t, &calls, &directiveCalls)
	var resp struct{ ResolvedWhere string }

	err := c.Post(`{ resolvedWhere(arg: {and: [{name: "ok"}, {ownedBy: "deny"}]}) }`, &resp)
	require.ErrorContains(t, err, "denied by resolver")
	require.ErrorContains(t, err, `"arg","and",1,"ownedBy"`)

	err = c.Post(`{ resolvedWhere(arg: {not: {not: {ownedBy: "deny"}}}) }`, &resp)
	require.ErrorContains(t, err, "denied by resolver")
	require.ErrorContains(t, err, `"arg","not","not","ownedBy"`)
}

// The point of the runtime step is the generated code it removes: the
// unmarshaler hands the method the map minus the resolver-backed keys and
// carries no case arm.
func TestResolvedInput_GeneratedUnmarshalerHasNoFieldArms(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(
		"internal", "gqlgenexec", "shards", "hybrid_resolved", "hybrid_resolved.generated.go",
	))
	require.NoError(t, err)
	normalized := strings.ReplaceAll(string(src), "\r\n", "\n")
	for _, name := range []string{"ResolvedWhereInput", "ResolvedChildWhereInput"} {
		body := walkedUnmarshalerBody(t, normalized, "__splitInput_"+name)
		require.Contains(
			t,
			body,
			`it.UnmarshalGQLContext(ctx, shardruntime.StripInputResolverFields(splitScope, "`+name+`", asMap))`,
		)
		require.Contains(
			t,
			body,
			`shardruntime.ApplyInputFieldResolvers(ctx, ec, splitScope, "`+name+`", &it, asMap)`,
		)
		require.NotContains(t, body, `case "`)
		require.NotContains(t, body, `fieldsInOrder`)
	}
}
