package splitpackages

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/99designs/gqlgen/client"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/transport"
)

func TestNullableRootFieldResolvingToNullKeepsData(t *testing.T) {
	resolvers := &Stub{}
	srv := handler.New(NewExecutableSchema(Config{Resolvers: resolvers}))
	srv.AddTransport(transport.POST{})
	c := client.New(srv)

	resolvers.QueryResolver.MaybeHello = func(_ context.Context, name string) (*string, error) {
		if name == "found" {
			return &name, nil
		}
		return nil, nil
	}
	resolvers.QueryResolver.Hello = func(_ context.Context, name string) (string, error) {
		return "hello " + name, nil
	}
	resolvers.MutationResolver.MaybeGreet = func(_ context.Context, name string) (*string, error) {
		return nil, nil
	}

	rawData := func(t *testing.T, query string) string {
		t.Helper()
		var raw map[string]any
		require.NoError(t, c.Post(query, &raw))
		data, err := json.Marshal(raw)
		require.NoError(t, err)
		return string(data)
	}

	t.Run("nullable query resolving to null keeps the data object", func(t *testing.T) {
		query := `query { maybeHello(name: "missing") }`
		require.JSONEq(t, `{"maybeHello":null}`, rawData(t, query))
	})

	t.Run("nullable query null alongside a sibling keeps the sibling", func(t *testing.T) {
		require.JSONEq(t,
			`{"maybeHello":null,"hello":"hello x"}`,
			rawData(t, `query { maybeHello(name: "missing") hello(name: "x") }`),
		)
	})

	t.Run("nullable query resolving to a value is unchanged", func(t *testing.T) {
		query := `query { maybeHello(name: "found") }`
		require.JSONEq(t, `{"maybeHello":"found"}`, rawData(t, query))
	})

	t.Run("non-null query that errors still nulls the whole data payload", func(t *testing.T) {
		resolvers.QueryResolver.Hello = func(_ context.Context, name string) (string, error) {
			return "", errors.New("boom")
		}
		var raw map[string]any
		err := c.Post(`query { hello(name: "x") maybeHello(name: "found") }`, &raw)
		require.ErrorContains(t, err, "boom")
		require.Nil(t, raw)
	})

	t.Run("non-null mutation that errors still nulls the whole data payload", func(t *testing.T) {
		resolvers.MutationResolver.Greet = func(_ context.Context, name string) (string, error) {
			return "", errors.New("boom")
		}
		var raw map[string]any
		err := c.Post(`mutation { greet(name: "x") maybeGreet(name: "x") }`, &raw)
		require.ErrorContains(t, err, "boom")
		require.Nil(t, raw)
	})

	t.Run("nullable mutation resolving to null keeps the data object", func(t *testing.T) {
		require.JSONEq(t, `{"maybeGreet":null}`, rawData(t, `mutation { maybeGreet(name: "x") }`))
	})
}
