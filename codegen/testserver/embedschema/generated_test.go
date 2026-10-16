//go:generate rm -rf exec
//go:generate rm -f resolver.go
//go:generate go run ../../../testdata/gqlgen.go -config gqlgen.yml -stub stub.go

package embedschema

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/99designs/gqlgen/client"
	"github.com/99designs/gqlgen/codegen/testserver/embedschema/exec"
	"github.com/99designs/gqlgen/graphql/handler"
)

func TestEmbeddedSchemaIsNotInlined(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("exec", "generated.go"))
	require.NoError(t, err)
	text := string(src)
	require.Contains(t, text, `//go:embed "schema/schema.graphql"`)
	require.Contains(t, text, `{Name: "../schema.graphql", Input: sourceData("schema/schema.graphql"), BuiltIn: false}`)
	require.NotContains(t, text, "type Query {")
}

func TestEmbeddedSchemaCopyMatchesSource(t *testing.T) {
	want, err := os.ReadFile("schema.graphql")
	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join("exec", "schema", "schema.graphql"))
	require.NoError(t, err)
	require.Equal(t, string(want), string(got))
}

func TestEmbeddedSchemaExecutes(t *testing.T) {
	resolvers := &Stub{}
	resolvers.QueryResolver.Ping = func(context.Context) (string, error) { return "pong", nil }
	c := client.New(handler.NewDefaultServer(exec.NewExecutableSchema(exec.Config{Resolvers: resolvers})))

	var resp struct{ Ping string }
	c.MustPost(`{ ping }`, &resp)
	require.Equal(t, "pong", resp.Ping)
}
