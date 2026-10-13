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

func describeWalkedChild(c *WalkedChild) string {
	if c == nil {
		return "<nil>"
	}
	s := fmt.Sprintf("{gated=%q default=%q", c.Gated, c.ChildDefault)
	if c.Deeper != nil {
		s += " deeper=" + describeWalkedChild(c.Deeper)
	}
	return s + "}"
}

func describeWalked(in *WalkedInput) string {
	if in == nil {
		return "<nil>"
	}
	var b strings.Builder
	fmt.Fprintf(
		&b,
		"plain=%q directed=%q default=%q saw=%s child=%s",
		in.Plain,
		in.Directed,
		in.WithDefault,
		strings.Join(in.SawKeys, ","),
		describeWalkedChild(in.Child),
	)
	for _, c := range in.Children {
		b.WriteString(" list=")
		b.WriteString(describeWalkedChild(c))
	}
	for _, s := range in.SelfRef {
		b.WriteString(" self=(")
		b.WriteString(describeWalked(s))
		b.WriteString(")")
	}
	return b.String()
}

func newWalkedServer(t *testing.T) *client.Client {
	t.Helper()
	resolvers := &Stub{}
	resolvers.QueryResolver.WalkedInput = func(ctx context.Context, arg WalkedInput) (string, error) {
		return describeWalked(&arg), nil
	}
	srv := handler.New(NewExecutableSchema(Config{
		Resolvers: resolvers,
		Directives: DirectiveRoot{
			ToUpper: func(ctx context.Context, obj any, next graphql.Resolver) (any, error) {
				res, err := next(ctx)
				if err != nil {
					return nil, err
				}
				if res.(string) == "deny" {
					return nil, errors.New("denied by directive")
				}
				return strings.ToUpper(res.(string)), nil
			},
		},
	}))
	srv.AddTransport(transport.POST{})
	return client.New(srv)
}

// Every field, directive-bearing ones included, reaches UnmarshalGQLContext
// (saw= lists them), and the directive and default still apply at every level.
func TestWalkedInput_DirectivesAndDefaultsApplyAtEveryLevel(t *testing.T) {
	c := newWalkedServer(t)
	var resp struct{ WalkedInput string }
	c.MustPost(`{ walkedInput(arg: {plain: "p", directed: "d",
		child: {gated: "g", deeper: {gated: "dd", childDefault: "explicit"}},
		children: [{gated: "a"}, {gated: "b"}],
		selfRef: [{plain: "s", directed: "sd", child: {gated: "sg"}}]}) }`, &resp)
	require.Equal(
		t,
		`plain="p" directed="D" default="fromDefault" saw=child,children,directed,plain,selfRef,withDefault`+
			` child={gated="G" default="childDefault" deeper={gated="DD" default="explicit"}}`+
			` list={gated="A" default="childDefault"} list={gated="B" default="childDefault"}`+
			` self=(plain="s" directed="SD" default="fromDefault" saw=child,directed,plain,withDefault child={gated="SG" default="childDefault"})`,
		resp.WalkedInput,
	)
}

// The directive is a gate: a rejection deep in a list must fail the query and
// name the path, exactly as the generated arms did.
func TestWalkedInput_NestedRejectionFailsWithPath(t *testing.T) {
	c := newWalkedServer(t)
	var resp struct{ WalkedInput string }
	err := c.Post(`{ walkedInput(arg: {plain: "p", directed: "d",
		children: [{gated: "ok"}, {gated: "deny"}]}) }`, &resp)
	require.ErrorContains(t, err, "denied by directive")
	require.ErrorContains(t, err, `"children",1,"gated"`)

	err = c.Post(`{ walkedInput(arg: {plain: "p", directed: "deny"}) }`, &resp)
	require.ErrorContains(t, err, "denied by directive")
	require.ErrorContains(t, err, `"arg","directed"`)
}

// The point of the walk is the generated code it removes: the unmarshaler for
// WalkedInput must carry no per-field arm, only the runtime walk.
func TestWalkedInput_GeneratedUnmarshalerHasNoFieldArms(t *testing.T) {
	src, err := os.ReadFile(
		filepath.Join(
			"internal",
			"gqlgenexec",
			"shards",
			"hybrid_walked",
			"hybrid_walked.generated.go",
		),
	)
	require.NoError(t, err)
	body := walkedUnmarshalerBody(
		t,
		strings.ReplaceAll(string(src), "\r\n", "\n"),
		"__splitInput_WalkedInput",
	)
	require.Contains(
		t,
		body,
		`shardruntime.ApplyInputDirectives(ctx, ec, splitScope, "WalkedInput", obj, asMap)`,
	)
	require.NotContains(t, body, `case "directed":`)
	require.NotContains(t, body, `case "child":`)
	require.NotContains(t, body, `fieldsInOrder`)
	require.Contains(t, body, `it.UnmarshalGQLContext(ctx, asMap)`)
}

func walkedUnmarshalerBody(t *testing.T, src, fn string) string {
	t.Helper()
	start := strings.Index(src, "func "+fn+"(")
	require.NotEqual(t, -1, start, "%s not generated", fn)
	end := strings.Index(src[start:], "\n}\n")
	require.NotEqual(t, -1, end)
	return src[start : start+end]
}

// plain carries @codegenOnly, a skip_runtime directive with no implementation.
// The walk must leave it alone; invoking it would fail every query with
// "unknown directive".
func TestWalkedInput_SkipRuntimeDirectiveIsNotInvoked(t *testing.T) {
	c := newWalkedServer(t)
	var resp struct{ WalkedInput string }
	c.MustPost(`{ walkedInput(arg: {plain: "p", directed: "d"}) }`, &resp)
	require.Contains(t, resp.WalkedInput, `plain="p" directed="D"`)
}
