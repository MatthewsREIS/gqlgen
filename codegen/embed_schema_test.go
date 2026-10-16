package codegen

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAssignEmbedPaths(t *testing.T) {
	sources := []AugmentedSource{
		{RelativePath: "schema.graphqls", Embeddable: true},
		{RelativePath: "../graphql_schema/user.graphql"},
		{RelativePath: "../../shared/base.graphql"},
		{RelativePath: "prelude.graphql", BuiltIn: true},
	}
	require.NoError(t, assignEmbedPaths("schema", sources))

	require.Equal(t, "", sources[0].EmbedPath)
	require.Equal(t, "schema.graphqls", sources[0].EmbedFile())

	require.Equal(t, "schema/graphql_schema/user.graphql", sources[1].EmbedPath)
	require.True(t, sources[1].Embeddable)
	require.Equal(t, "schema/graphql_schema/user.graphql", sources[1].EmbedFile())
	require.Equal(t, "../graphql_schema/user.graphql", sources[1].RelativePath)

	require.Equal(t, "schema/shared/base.graphql", sources[2].EmbedPath)
	require.True(t, sources[2].Embeddable)

	require.Equal(t, "", sources[3].EmbedPath)
	require.False(t, sources[3].Embeddable)
}

func TestAssignEmbedPaths_Collision(t *testing.T) {
	sources := []AugmentedSource{{RelativePath: "../x.graphql"}, {RelativePath: "../../x.graphql"}}
	require.EqualError(
		t,
		assignEmbedPaths("schema", sources),
		"schema files ../x.graphql and ../../x.graphql both map to schema/x.graphql under embed_schema_dir",
	)
}

func TestAssignEmbedPaths_Unset(t *testing.T) {
	sources := []AugmentedSource{{RelativePath: "../x.graphql"}}
	require.NoError(t, assignEmbedPaths("", sources))
	require.False(t, sources[0].Embeddable)
	require.Equal(t, "", sources[0].EmbedPath)
}

func TestAssignEmbedPaths_AllInside(t *testing.T) {
	sources := []AugmentedSource{{RelativePath: "a.graphqls", Embeddable: true}, {RelativePath: "sub/b.graphqls", Embeddable: true}}
	require.NoError(t, assignEmbedPaths("schema", sources))
	for _, s := range sources {
		require.Equal(t, "", s.EmbedPath)
		require.Equal(t, s.RelativePath, s.EmbedFile())
	}
}

func TestAssignEmbedPaths_RejectsSourcesInsideEmbedDir(t *testing.T) {
	sources := []AugmentedSource{
		{RelativePath: "schema/real.graphqls", Embeddable: true},
		{RelativePath: "../outside.graphql"},
	}
	require.EqualError(
		t,
		assignEmbedPaths("schema", sources),
		"schema file schema/real.graphqls sits inside embed_schema_dir schema, which codegen recreates on every run",
	)
	require.Equal(t, "", sources[1].EmbedPath)
}
