package splitpackages

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/99designs/gqlgen/client"
	"github.com/99designs/gqlgen/codegen/testserver/splitpackages/model"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/transport"
)

func newAbstractServer(t *testing.T, resolvers *Stub) *client.Client {
	t.Helper()
	srv := handler.New(NewExecutableSchema(Config{Resolvers: resolvers}))
	srv.AddTransport(transport.POST{})
	return client.New(srv)
}

// Abstract types (union/interface) have no owning shard: the shard codec that
// marshals one resolves its dispatcher through shardruntime.LookupObject, so
// the gateway must register a handler for it or every selection panics with
// "missing object shard handler for <Name>".
func TestAbstractTypes(t *testing.T) {
	t.Run("union member A", func(t *testing.T) {
		resolvers := &Stub{}
		resolvers.QueryResolver.AbstractUnion = func(ctx context.Context) (model.AbstractUnion, error) {
			return &model.UnionMemberA{A: "hello"}, nil
		}
		var resp struct {
			AbstractUnion struct {
				Typename string `json:"__typename"`
				A        string
			}
		}
		err := newAbstractServer(t, resolvers).Post(
			`query { abstractUnion { __typename ... on UnionMemberA { a } } }`, &resp)
		require.NoError(t, err)
		require.Equal(t, "UnionMemberA", resp.AbstractUnion.Typename)
		require.Equal(t, "hello", resp.AbstractUnion.A)
	})

	t.Run("union member B", func(t *testing.T) {
		resolvers := &Stub{}
		resolvers.QueryResolver.AbstractUnion = func(ctx context.Context) (model.AbstractUnion, error) {
			return &model.UnionMemberB{B: 42}, nil
		}
		var resp struct {
			AbstractUnion struct {
				Typename string `json:"__typename"`
				B        int
			}
		}
		err := newAbstractServer(t, resolvers).Post(
			`query { abstractUnion { __typename ... on UnionMemberB { b } } }`, &resp)
		require.NoError(t, err)
		require.Equal(t, "UnionMemberB", resp.AbstractUnion.Typename)
		require.Equal(t, 42, resp.AbstractUnion.B)
	})

	t.Run("interface implementor", func(t *testing.T) {
		resolvers := &Stub{}
		resolvers.QueryResolver.AbstractInterface = func(ctx context.Context) (model.AbstractInterface, error) {
			return &model.InterfaceMemberB{ID: "id-b", B: 7}, nil
		}
		var resp struct {
			AbstractInterface struct {
				Typename string `json:"__typename"`
				ID       string `json:"id"`
				B        int
			}
		}
		err := newAbstractServer(t, resolvers).Post(
			`query { abstractInterface { __typename id ... on InterfaceMemberB { b } } }`, &resp)
		require.NoError(t, err)
		require.Equal(t, "InterfaceMemberB", resp.AbstractInterface.Typename)
		require.Equal(t, "id-b", resp.AbstractInterface.ID)
		require.Equal(t, 7, resp.AbstractInterface.B)
	})

	t.Run("nil abstract value marshals to null", func(t *testing.T) {
		resolvers := &Stub{}
		resolvers.QueryResolver.AbstractUnion = func(ctx context.Context) (model.AbstractUnion, error) {
			return nil, nil
		}
		resolvers.QueryResolver.AbstractInterface = func(ctx context.Context) (model.AbstractInterface, error) {
			return (*model.InterfaceMemberA)(nil), nil
		}
		var resp struct {
			AbstractUnion *struct {
				Typename string `json:"__typename"`
			}
			AbstractInterface *struct {
				Typename string `json:"__typename"`
			}
		}
		err := newAbstractServer(t, resolvers).Post(
			`query { abstractUnion { __typename } abstractInterface { __typename } }`, &resp)
		require.NoError(t, err)
		require.Nil(t, resp.AbstractUnion)
		require.Nil(t, resp.AbstractInterface)
	})
}
