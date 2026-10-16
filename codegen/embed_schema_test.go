package codegen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/99designs/gqlgen/codegen/config"
)

func TestAssignEmbedPaths(t *testing.T) {
	sources := []AugmentedSource{
		{RelativePath: "schema.graphqls", Embeddable: true},
		{RelativePath: "../graphql_schema/user.graphql"},
		{RelativePath: "../../shared/base.graphql"},
		{RelativePath: "prelude.graphql", BuiltIn: true},
	}
	require.NoError(t, assignEmbedPaths("schema", sources))

	require.Empty(t, sources[0].EmbedPath)
	require.Equal(t, "schema.graphqls", sources[0].EmbedFile())

	require.Equal(t, "schema/graphql_schema/user.graphql", sources[1].EmbedPath)
	require.True(t, sources[1].Embeddable)
	require.Equal(t, "schema/graphql_schema/user.graphql", sources[1].EmbedFile())
	require.Equal(t, "../graphql_schema/user.graphql", sources[1].RelativePath)

	require.Equal(t, "schema/shared/base.graphql", sources[2].EmbedPath)
	require.True(t, sources[2].Embeddable)

	require.Empty(t, sources[3].EmbedPath)
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
	require.Empty(t, sources[0].EmbedPath)
}

func TestAssignEmbedPaths_AllInside(t *testing.T) {
	sources := []AugmentedSource{
		{RelativePath: "a.graphqls", Embeddable: true},
		{RelativePath: "sub/b.graphqls", Embeddable: true},
	}
	require.NoError(t, assignEmbedPaths("schema", sources))
	for _, s := range sources {
		require.Empty(t, s.EmbedPath)
		require.Equal(t, s.RelativePath, s.EmbedFile())
	}
}

func TestAssignEmbedPaths_RejectsSourcesInsideEmbedDir(t *testing.T) {
	for _, inside := range []string{"schema/real.graphqls", "schema"} {
		sources := []AugmentedSource{
			{RelativePath: "../outside.graphql"},
			{RelativePath: inside, Embeddable: true},
		}
		require.EqualError(
			t,
			assignEmbedPaths("schema", sources),
			"schema file "+inside+" sits inside embed_schema_dir schema, where codegen removes schema files it did not copy",
		)
		require.Empty(t, sources[0].EmbedPath)
	}
}

func TestWriteEmbeddedSchemaCopies_ReconcilesWithoutDeletingForeignFiles(t *testing.T) {
	execDir := t.TempDir()
	data := &Data{
		Config: &config.Config{Exec: config.ExecConfig{
			Layout:         config.ExecLayoutSplitPackages,
			Filename:       filepath.Join(execDir, "generated.go"),
			EmbedSchemaDir: "schema",
		}},
		AugmentedSources: []AugmentedSource{
			{
				RelativePath: "../a.graphql",
				EmbedPath:    "schema/a.graphql",
				Embeddable:   true,
				Source:       "type Query { a: ID }\n",
			},
		},
	}
	require.NoError(t, writeEmbeddedSchemaCopies(data))
	copyPath := filepath.Join(execDir, "schema", "a.graphql")
	got, err := os.ReadFile(copyPath)
	require.NoError(t, err)
	require.Equal(t, "type Query { a: ID }\n", string(got))
	first, err := os.Stat(copyPath)
	require.NoError(t, err)

	stale := filepath.Join(execDir, "schema", "stale.graphqls")
	require.NoError(t, os.WriteFile(stale, []byte("type Stale { id: ID! }\n"), 0o644))
	require.NoError(t, writeEmbeddedSchemaCopies(data))
	require.NoFileExists(t, stale)
	second, err := os.Stat(copyPath)
	require.NoError(t, err)
	require.Equal(t, first.ModTime(), second.ModTime())

	foreign := filepath.Join(execDir, "schema", "resolver.go")
	require.NoError(t, os.WriteFile(foreign, []byte("package schema\n"), 0o644))
	err = writeEmbeddedSchemaCopies(data)
	require.ErrorContains(t, err, "which codegen did not write")
	require.FileExists(t, foreign)
	require.FileExists(t, copyPath)
}

func TestWriteEmbeddedSchemaCopies_ExtensionlessSourceKeepsForeignFiles(t *testing.T) {
	execDir := t.TempDir()
	data := &Data{
		Config: &config.Config{Exec: config.ExecConfig{
			Layout:         config.ExecLayoutSplitPackages,
			Filename:       filepath.Join(execDir, "generated.go"),
			EmbedSchemaDir: "schema",
		}},
		AugmentedSources: []AugmentedSource{
			{
				RelativePath: "../SCHEMA",
				EmbedPath:    "schema/SCHEMA",
				Embeddable:   true,
				Source:       "type Query { a: ID }\n",
			},
		},
	}
	require.NoError(t, writeEmbeddedSchemaCopies(data))

	foreign := filepath.Join(execDir, "schema", "README")
	require.NoError(t, os.WriteFile(foreign, []byte("notes\n"), 0o644))
	err := writeEmbeddedSchemaCopies(data)
	require.ErrorContains(t, err, "which codegen did not write")
	require.FileExists(t, foreign)
}
