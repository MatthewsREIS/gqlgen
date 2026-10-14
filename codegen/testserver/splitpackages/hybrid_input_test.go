package splitpackages

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/99designs/gqlgen/client"
	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/transport"
)

// HybridInput's Go type has UnmarshalGQLContext, so gqlgen generates a hybrid
// unmarshaler: the @toUpper field directive runs from the schema at runtime
// before the method decodes the plain, defaulted and directed fields, and
// generated code still runs the `resolved` field resolver.
func TestHybridInputUnmarshaler(t *testing.T) {
	describe := func(in *HybridInput) string {
		if in == nil {
			return "<nil>"
		}
		return fmt.Sprintf("plain=%q default=%q directed=%q resolved=%q saw=%s",
			in.Plain, in.WithDefault, in.Directed, in.Resolved, strings.Join(in.SawKeys, ","))
	}

	resolvers := &Stub{}
	resolvers.QueryResolver.HybridInput = func(ctx context.Context, arg HybridInput) (string, error) {
		return describe(&arg), nil
	}
	resolvers.QueryResolver.HybridInputNullable = func(ctx context.Context, arg *HybridInput) (string, error) {
		return describe(arg), nil
	}
	resolvers.HybridInputResolver.Resolved = func(ctx context.Context, obj *HybridInput, data string) error {
		obj.Resolved = "resolver(" + data + ")"
		return nil
	}

	srv := handler.New(NewExecutableSchema(Config{
		Resolvers: resolvers,
		Directives: DirectiveRoot{
			ToUpper: func(ctx context.Context, obj any, next graphql.Resolver) (any, error) {
				res, err := next(ctx)
				if err != nil {
					return nil, err
				}
				return strings.ToUpper(res.(string)), nil
			},
		},
	}))
	srv.AddTransport(transport.POST{})
	c := client.New(srv)

	t.Run(
		"method decodes plain and directed fields, resolver fields are withheld from it",
		func(t *testing.T) {
			var resp struct{ HybridInput string }
			c.MustPost(
				`{ hybridInput(arg: {plain: "p", withDefault: "explicit", directed: "up", resolved: "r"}) }`,
				&resp,
			)
			require.Equal(
				t,
				`plain="p" default="explicit" directed="UP" resolved="resolver(r)" saw=directed,plain,withDefault`,
				resp.HybridInput,
			)
		},
	)

	t.Run("default is applied and reaches the method", func(t *testing.T) {
		var resp struct{ HybridInput string }
		c.MustPost(`{ hybridInput(arg: {plain: "p", directed: "up", resolved: "r"}) }`, &resp)
		require.Equal(
			t,
			`plain="p" default="fromDefault" directed="UP" resolved="resolver(r)" saw=directed,plain,withDefault`,
			resp.HybridInput,
		)
	})

	t.Run("explicit null input is not an error", func(t *testing.T) {
		var resp struct{ HybridInputNullable string }
		c.MustPost(`{ hybridInputNullable(arg: null) }`, &resp)
		require.Equal(t, "<nil>", resp.HybridInputNullable)
	})

	// A non-map value cannot reach the unmarshaler through a query (gqlparser
	// rejects it while coercing variables), but it can through
	// graphql.UnmarshalInputFromContext — the path ent's gqlcollections uses when
	// it re-feeds an already-typed input. That must error, not panic.
	t.Run("non-map input is an error, not a panic", func(t *testing.T) {
		defer func(orig func(context.Context, HybridInput) (string, error)) {
			resolvers.QueryResolver.HybridInput = orig
		}(resolvers.QueryResolver.HybridInput)
		resolvers.QueryResolver.HybridInput = func(ctx context.Context, arg HybridInput) (string, error) {
			var out HybridInput
			return "", graphql.UnmarshalInputFromContext(ctx, 42, &out)
		}

		var resp struct{ HybridInput string }
		err := c.Post(`{ hybridInput(arg: {plain: "p", directed: "up", resolved: "r"}) }`, &resp)
		require.ErrorContains(t, err, "unmarshalInputHybridInput: expected map[string]any, got int")
	})
}

// Nested inputs reached through fields that carry no directive of their own
// (nested, nestedList, selfRef) must still get their own directives and field
// resolvers. Handing those values to UnmarshalGQLContext skips them silently:
// in a real schema the skipped directive is a scope check, so this is an
// authorization bypass, not a cosmetic difference.
func TestHybridInputNestedKeepsDirectivesAndResolvers(t *testing.T) {
	var describeNested func(n *HybridNested) string
	describeNested = func(n *HybridNested) string {
		if n == nil {
			return "<nil>"
		}
		s := fmt.Sprintf("{gated=%q resolved=%q", n.Gated, n.Resolved)
		if n.Deeper != nil {
			s += " deeper=" + describeNested(n.Deeper)
		}
		return s + "}"
	}
	var describe func(in *HybridInput) string
	describe = func(in *HybridInput) string {
		if in == nil {
			return "<nil>"
		}
		var b strings.Builder
		fmt.Fprintf(&b, "directed=%q resolved=%q saw=%s nested=%s",
			in.Directed, in.Resolved, strings.Join(in.SawKeys, ","), describeNested(in.Nested))
		for _, n := range in.NestedList {
			b.WriteString(" list=")
			b.WriteString(describeNested(n))
		}
		for _, n := range in.ResolvedNested {
			b.WriteString(" resolvedNested=")
			b.WriteString(describeNested(n))
		}
		for _, c := range in.SelfRef {
			b.WriteString(" self=(")
			b.WriteString(describe(c))
			b.WriteString(")")
		}
		return b.String()
	}

	resolvers := &Stub{}
	resolvers.QueryResolver.HybridInput = func(ctx context.Context, arg HybridInput) (string, error) {
		return describe(&arg), nil
	}
	resolvers.HybridInputResolver.Resolved = func(ctx context.Context, obj *HybridInput, data string) error {
		obj.Resolved = "resolver(" + data + ")"
		return nil
	}
	resolvers.HybridNestedResolver.Resolved = func(ctx context.Context, obj *HybridNested, data string) error {
		obj.Resolved = "nestedResolver(" + data + ")"
		return nil
	}
	resolvers.HybridInputResolver.ResolvedNested = func(ctx context.Context, obj *HybridInput, data []*HybridNested) error {
		obj.ResolvedNested = append(obj.ResolvedNested, data...)
		return nil
	}

	srv := handler.New(NewExecutableSchema(Config{
		Resolvers: resolvers,
		Directives: DirectiveRoot{
			ToUpper: func(ctx context.Context, obj any, next graphql.Resolver) (any, error) {
				res, err := next(ctx)
				if err != nil {
					return nil, err
				}
				// The consumer's directive is a scope check that REJECTS, so the
				// bypass this guards is a query that must fail and does not.
				if res.(string) == "deny" {
					return nil, errors.New("denied by directive")
				}
				return strings.ToUpper(res.(string)), nil
			},
		},
	}))
	srv.AddTransport(transport.POST{})
	c := client.New(srv)

	t.Run("nested object keeps its directive and resolver", func(t *testing.T) {
		var resp struct{ HybridInput string }
		c.MustPost(`{ hybridInput(arg: {plain: "p", directed: "d", resolved: "r",
			nested: {gated: "g", resolved: "n"}}) }`, &resp)
		require.Equal(
			t,
			`directed="D" resolved="resolver(r)" saw=directed,plain,withDefault nested={gated="G" resolved="nestedResolver(n)"}`,
			resp.HybridInput,
		)
	})

	t.Run("nested list keeps its directive and resolver", func(t *testing.T) {
		var resp struct{ HybridInput string }
		c.MustPost(`{ hybridInput(arg: {plain: "p", directed: "d", resolved: "r",
			nestedList: [{gated: "a", resolved: "n1"}, {gated: "b", resolved: "n2"}]}) }`, &resp)
		require.Equal(
			t,
			`directed="D" resolved="resolver(r)" saw=directed,plain,withDefault nested=<nil>`+
				` list={gated="A" resolved="nestedResolver(n1)"} list={gated="B" resolved="nestedResolver(n2)"}`,
			resp.HybridInput,
		)
	})

	t.Run("one level deeper keeps its directive and resolver", func(t *testing.T) {
		var resp struct{ HybridInput string }
		c.MustPost(`{ hybridInput(arg: {plain: "p", directed: "d", resolved: "r",
			nested: {gated: "g", resolved: "n", deeper: {gated: "dd", resolved: "nn"}}}) }`, &resp)
		require.Equal(t,
			`directed="D" resolved="resolver(r)" saw=directed,plain,withDefault`+
				` nested={gated="G" resolved="nestedResolver(n)" deeper={gated="DD" resolved="nestedResolver(nn)"}}`,
			resp.HybridInput)
	})

	t.Run("a nested directive that rejects fails the query", func(t *testing.T) {
		var resp struct{ HybridInput string }
		err := c.Post(`{ hybridInput(arg: {plain: "p", directed: "d", resolved: "r",
			nested: {gated: "deny", resolved: "n"}}) }`, &resp)
		require.ErrorContains(t, err, "denied by directive")
		require.ErrorContains(t, err, `"nested","gated"`)
	})

	t.Run(
		"self-referential list keeps directives and resolvers at every level",
		func(t *testing.T) {
			var resp struct{ HybridInput string }
			c.MustPost(`{ hybridInput(arg: {plain: "p", directed: "d", resolved: "r",
			selfRef: [{plain: "s", directed: "sd", resolved: "sr", nested: {gated: "sg", resolved: "sn"}}]}) }`, &resp)
			require.Equal(
				t,
				`directed="D" resolved="resolver(r)" saw=directed,plain,withDefault nested=<nil>`+
					` self=(directed="SD" resolved="resolver(sr)" saw=directed,plain,withDefault nested={gated="SG" resolved="nestedResolver(sn)"})`,
				resp.HybridInput,
			)
		},
	)

	t.Run(
		"a resolver-backed nested list runs its resolver once, after nested enforcement",
		func(t *testing.T) {
			var resp struct{ HybridInput string }
			c.MustPost(`{ hybridInput(arg: {plain: "p", directed: "d", resolved: "r",
			resolvedNested: [{gated: "x", resolved: "n1"}]}) }`, &resp)
			require.Equal(
				t,
				`directed="D" resolved="resolver(r)" saw=directed,plain,withDefault nested=<nil>`+
					` resolvedNested={gated="X" resolved="nestedResolver(n1)"}`,
				resp.HybridInput,
			)
		},
	)
}
