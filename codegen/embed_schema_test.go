package codegen

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
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

func TestAssignEmbedPaths_RejectsTheManifestName(t *testing.T) {
	sources := []AugmentedSource{{RelativePath: "../.gqlgen-embedded"}}
	require.EqualError(
		t,
		assignEmbedPaths("schema", sources),
		"schema file ../.gqlgen-embedded would be copied to schema/.gqlgen-embedded, which embed_schema_dir reserves for its manifest",
	)
}

func TestAssignEmbedPaths_RejectsACopyBeneathTheManifest(t *testing.T) {
	sources := []AugmentedSource{{RelativePath: "../.gqlgen-embedded/types.graphql"}}
	require.EqualError(
		t,
		assignEmbedPaths("schema", sources),
		"schema file ../.gqlgen-embedded/types.graphql would be copied to schema/.gqlgen-embedded/types.graphql, which embed_schema_dir reserves for its manifest",
	)
}

func TestAssignEmbedPaths_RejectsAnAncestorCollision(t *testing.T) {
	sources := []AugmentedSource{
		{RelativePath: "../shared"},
		{RelativePath: "../../shared/types.graphql"},
	}
	require.EqualError(
		t,
		assignEmbedPaths("schema", sources),
		"schema files ../shared and ../../shared/types.graphql map to schema/shared and schema/shared/types.graphql under embed_schema_dir, and one is a directory of the other",
	)
}

func TestAssignEmbedPaths_AncestorCollisionSurvivesASiblingBetween(t *testing.T) {
	sources := []AugmentedSource{
		{RelativePath: "../shared"},
		{RelativePath: "../shared-x/types.graphql"},
		{RelativePath: "../../shared/types.graphql"},
	}
	require.EqualError(
		t,
		assignEmbedPaths("schema", sources),
		"schema files ../shared and ../../shared/types.graphql map to schema/shared and schema/shared/types.graphql under embed_schema_dir, and one is a directory of the other",
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
			"schema file "+inside+" sits inside embed_schema_dir schema, which must hold only the copies codegen writes",
		)
		require.Empty(t, sources[0].EmbedPath)
	}
}

func embedTestData(execDir string, embedPaths ...string) *Data {
	data := &Data{Config: &config.Config{Exec: config.ExecConfig{
		Layout:         config.ExecLayoutSplitPackages,
		Filename:       filepath.Join(execDir, "generated.go"),
		EmbedSchemaDir: "schema",
	}}}
	for _, p := range embedPaths {
		data.AugmentedSources = append(data.AugmentedSources, AugmentedSource{
			RelativePath: "../" + filepath.Base(p),
			EmbedPath:    p,
			Embeddable:   true,
			Source:       "type Query { a: ID }\n",
		})
	}
	return data
}

func TestWriteEmbeddedSchemaCopies_ReconcilesWithoutDeletingForeignFiles(t *testing.T) {
	execDir := t.TempDir()
	copyPath := filepath.Join(execDir, "schema", "a.graphql")
	old := filepath.Join(execDir, "schema", "old.graphqls")
	manifest := filepath.Join(execDir, "schema", ".gqlgen-embedded")

	require.NoError(
		t,
		writeEmbeddedSchemaCopies(
			embedTestData(execDir, "schema/a.graphql", "schema/old.graphqls"),
		),
	)
	got, err := os.ReadFile(copyPath)
	require.NoError(t, err)
	require.Equal(t, "type Query { a: ID }\n", string(got))
	require.FileExists(t, old)
	listed, _ := os.ReadFile(manifest)
	assert.Equal(t, "a.graphql\nold.graphqls\n", string(listed))
	first, err := os.Stat(copyPath)
	require.NoError(t, err)

	data := embedTestData(execDir, "schema/a.graphql")
	require.NoError(t, writeEmbeddedSchemaCopies(data))
	require.NoFileExists(t, old)
	second, err := os.Stat(copyPath)
	require.NoError(t, err)
	require.Equal(t, first.ModTime(), second.ModTime())
	listed, _ = os.ReadFile(manifest)
	assert.Equal(t, "a.graphql\n", string(listed))

	foreign := filepath.Join(execDir, "schema", "README")
	require.NoError(t, os.WriteFile(foreign, []byte("notes\n"), 0o644))
	err = writeEmbeddedSchemaCopies(data)
	require.ErrorContains(t, err, "which codegen did not write")
	require.FileExists(t, foreign)
	require.FileExists(t, copyPath)
}

func TestWriteEmbeddedSchemaCopies_RefusesUnownedFiles(t *testing.T) {
	t.Run("unlisted file", func(t *testing.T) {
		execDir := t.TempDir()
		handwritten := filepath.Join(execDir, "schema", "handwritten.graphqls")
		require.NoError(t, os.MkdirAll(filepath.Dir(handwritten), 0o755))
		require.NoError(t, os.WriteFile(handwritten, []byte("type Mine { id: ID! }\n"), 0o644))

		err := writeEmbeddedSchemaCopies(embedTestData(execDir, "schema/a.graphql"))
		assert.FileExists(t, handwritten)
		require.ErrorContains(t, err, "holds handwritten.graphqls, which codegen did not write")
	})

	t.Run("handwritten file at a target path", func(t *testing.T) {
		execDir := t.TempDir()
		target := filepath.Join(execDir, "schema", "a.graphql")
		require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
		require.NoError(t, os.WriteFile(target, []byte("type Mine { id: ID! }\n"), 0o644))

		err := writeEmbeddedSchemaCopies(embedTestData(execDir, "schema/a.graphql"))
		got, readErr := os.ReadFile(target)
		require.NoError(t, readErr)
		assert.Equal(t, "type Mine { id: ID! }\n", string(got))
		require.ErrorContains(t, err, "holds a.graphql, which codegen did not write")
	})
}

func TestWriteEmbeddedSchemaCopies_RemovesManifestWhenNothingIsWanted(t *testing.T) {
	execDir := t.TempDir()
	copyPath := filepath.Join(execDir, "schema", "a.graphql")
	manifest := filepath.Join(execDir, "schema", ".gqlgen-embedded")

	require.NoError(t, writeEmbeddedSchemaCopies(embedTestData(execDir, "schema/a.graphql")))
	require.FileExists(t, copyPath)
	require.FileExists(t, manifest)

	require.NoError(t, writeEmbeddedSchemaCopies(embedTestData(execDir)))
	assert.NoFileExists(t, copyPath)
	assert.NoFileExists(t, manifest)
}

func TestWriteEmbeddedSchemaCopies_RefusesBeforeRemoving(t *testing.T) {
	execDir := t.TempDir()
	old := filepath.Join(execDir, "schema", "old.graphqls")
	require.NoError(
		t,
		writeEmbeddedSchemaCopies(
			embedTestData(execDir, "schema/a.graphql", "schema/old.graphqls"),
		),
	)
	foreign := filepath.Join(execDir, "schema", "zzz-foreign.txt")
	require.NoError(t, os.WriteFile(foreign, []byte("notes\n"), 0o644))

	err := writeEmbeddedSchemaCopies(embedTestData(execDir, "schema/a.graphql"))
	assert.FileExists(t, old)
	assert.FileExists(t, foreign)
	require.ErrorContains(t, err, "holds zzz-foreign.txt, which codegen did not write")
}

func TestWriteEmbeddedSchemaCopies_ReadsCRLFManifest(t *testing.T) {
	execDir := t.TempDir()
	root := filepath.Join(execDir, "schema")
	require.NoError(t, os.MkdirAll(root, 0o755))
	for _, name := range []string{"a.graphql", "old.graphqls"} {
		require.NoError(
			t,
			os.WriteFile(filepath.Join(root, name), []byte("type Query { a: ID }\n"), 0o644),
		)
	}
	require.NoError(t, os.WriteFile(
		filepath.Join(root, ".gqlgen-embedded"),
		[]byte("a.graphql\r\nold.graphqls\r\n"),
		0o644,
	))

	err := writeEmbeddedSchemaCopies(
		embedTestData(execDir, "schema/a.graphql", "schema/old.graphqls"),
	)
	assert.FileExists(t, filepath.Join(root, "a.graphql"))
	assert.FileExists(t, filepath.Join(root, "old.graphqls"))
	require.NoError(t, err)
}

func TestWriteEmbeddedSchemaCopies_RefusesSymlinks(t *testing.T) {
	for _, name := range []string{"a.graphql", ".gqlgen-embedded"} {
		t.Run(name, func(t *testing.T) {
			execDir := t.TempDir()
			require.NoError(
				t,
				writeEmbeddedSchemaCopies(embedTestData(execDir, "schema/a.graphql")),
			)

			outside := filepath.Join(t.TempDir(), "outside.txt")
			require.NoError(t, os.WriteFile(outside, []byte("a.graphql\nkept.graphql\n"), 0o644))
			link := filepath.Join(execDir, "schema", name)
			require.NoError(t, os.Remove(link))
			require.NoError(t, os.Symlink(outside, link))

			data := embedTestData(execDir, "schema/a.graphql")
			data.AugmentedSources[0].Source = "type Query { b: ID }\n"
			err := writeEmbeddedSchemaCopies(data)
			got, readErr := os.ReadFile(outside)
			require.NoError(t, readErr)
			assert.Equal(t, "a.graphql\nkept.graphql\n", string(got))
			require.ErrorContains(t, err, "holds a symlink at "+name)
		})
	}
}

func TestWriteEmbeddedSchemaCopies_RefusesASymlinkedAncestor(t *testing.T) {
	execDir := t.TempDir()
	first := embedTestData(execDir, "assets/schema/a.graphql", "assets/schema/stale.graphql")
	first.Config.Exec.EmbedSchemaDir = "assets/schema"
	require.NoError(t, writeEmbeddedSchemaCopies(first))

	movedAssets := filepath.Join(t.TempDir(), "assets")
	require.NoError(t, os.Rename(filepath.Join(execDir, "assets"), movedAssets))
	require.NoError(t, os.Symlink(movedAssets, filepath.Join(execDir, "assets")))
	copyPath := filepath.Join(movedAssets, "schema", "a.graphql")
	stalePath := filepath.Join(movedAssets, "schema", "stale.graphql")
	manifestPath := filepath.Join(movedAssets, "schema", ".gqlgen-embedded")
	manifestBefore, err := os.ReadFile(manifestPath)
	require.NoError(t, err)

	data := embedTestData(execDir, "assets/schema/a.graphql")
	data.Config.Exec.EmbedSchemaDir = "assets/schema"
	data.AugmentedSources[0].Source = "type Query { b: ID }\n"
	err = writeEmbeddedSchemaCopies(data)

	require.ErrorContains(t, err, "passes through a symlink at assets")
	got, readErr := os.ReadFile(copyPath)
	require.NoError(t, readErr)
	assert.Equal(t, "type Query { a: ID }\n", string(got))
	got, readErr = os.ReadFile(stalePath)
	require.NoError(t, readErr)
	assert.Equal(t, "type Query { a: ID }\n", string(got))
	manifestAfter, readErr := os.ReadFile(manifestPath)
	require.NoError(t, readErr)
	assert.Equal(t, string(manifestBefore), string(manifestAfter))
}

func TestWriteEmbeddedSchemaCopies_PrunesNestedEmptiedDirectories(t *testing.T) {
	execDir := t.TempDir()
	require.NoError(
		t,
		writeEmbeddedSchemaCopies(embedTestData(execDir, "schema/a/b/c.graphql")),
	)
	require.FileExists(t, filepath.Join(execDir, "schema", "a", "b", "c.graphql"))

	require.NoError(t, writeEmbeddedSchemaCopies(embedTestData(execDir, "schema/d.graphql")))

	_, err := os.Lstat(filepath.Join(execDir, "schema", "a"))
	require.ErrorIs(t, err, fs.ErrNotExist)
	assert.FileExists(t, filepath.Join(execDir, "schema", "d.graphql"))
}

func TestWriteEmbeddedSchemaCopies_ReplacesADirectoryWithAFile(t *testing.T) {
	execDir := t.TempDir()
	require.NoError(
		t,
		writeEmbeddedSchemaCopies(embedTestData(execDir, "schema/shared/types.graphql")),
	)

	data := embedTestData(execDir, "schema/shared")
	data.AugmentedSources[0].Source = "type Query { shared: ID }\n"
	require.NoError(t, writeEmbeddedSchemaCopies(data))

	target := filepath.Join(execDir, "schema", "shared")
	info, err := os.Stat(target)
	require.NoError(t, err)
	require.True(t, info.Mode().IsRegular())
	got, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "type Query { shared: ID }\n", string(got))
	assert.NoFileExists(t, filepath.Join(target, "types.graphql"))
}
