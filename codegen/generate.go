package codegen

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/99designs/gqlgen/codegen/config"
	"github.com/99designs/gqlgen/codegen/templates"
)

//go:embed *.gotpl
var codegenTemplates embed.FS

func GenerateCode(data *Data) error {
	if !data.Config.Exec.IsDefined() {
		return errors.New("missing exec config")
	}

	if data.Config.Exec.Layout == config.ExecLayoutSplitPackages {
		if err := checkRuntimeSchemaParity(data); err != nil {
			return err
		}
	}

	if err := writeEmbeddedSchemaCopies(data); err != nil {
		return err
	}

	switch data.Config.Exec.Layout {
	case config.ExecLayoutSingleFile:
		return generateSingleFile(data)
	case config.ExecLayoutFollowSchema:
		return generatePerSchema(data)
	case config.ExecLayoutSplitPackages:
		return generateSplitPackages(data)
	}

	return fmt.Errorf("unrecognized exec layout %s", data.Config.Exec.Layout)
}

const embedManifest = ".gqlgen-embedded"

// writeEmbeddedSchemaCopies keeps Exec.EmbedSchemaDir equal to the set of
// copies the sources need. The manifest lists the copies codegen wrote, and
// every other file in the directory stops the run, so a handwritten file is
// never deleted or overwritten.
func writeEmbeddedSchemaCopies(data *Data) error {
	dir := data.Config.Exec.EmbedSchemaDir
	if dir == "" {
		return nil
	}
	root := filepath.Join(data.Config.Exec.Dir(), filepath.FromSlash(dir))
	want := map[string][]byte{}
	for _, s := range data.AugmentedSources {
		if s.EmbedPath != "" {
			want[strings.TrimPrefix(s.EmbedPath, dir+"/")] = []byte(s.Source)
		}
	}
	if err := refuseSymlinkedAncestors(data.Config.Exec.Dir(), dir); err != nil {
		return err
	}
	owned, err := readEmbedManifest(root)
	if err != nil {
		return err
	}
	var stale, dirs []string
	if _, err := os.Stat(root); err == nil {
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				if p != root {
					dirs = append(dirs, p)
				}
				return nil
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if d.Type()&fs.ModeSymlink != 0 {
				return fmt.Errorf(
					"embed_schema_dir %s holds a symlink at %s; codegen writes only regular files there",
					dir,
					rel,
				)
			}
			if rel == embedManifest {
				return nil
			}
			if !owned[rel] {
				return fmt.Errorf(
					"embed_schema_dir %s holds %s, which codegen did not write; move it out of the directory",
					dir,
					rel,
				)
			}
			if _, keep := want[rel]; !keep {
				stale = append(stale, p)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	for _, p := range stale {
		if err := os.Remove(p); err != nil {
			return err
		}
	}
	for _, p := range slices.Backward(dirs) {
		entries, err := os.ReadDir(p)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			if err := os.Remove(p); err != nil {
				return err
			}
		}
	}
	for rel, content := range want {
		target := filepath.Join(root, filepath.FromSlash(rel))
		if current, err := os.ReadFile(target); err == nil && bytes.Equal(current, content) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("create embed_schema_dir for %s: %w", target, err)
		}
		if err := os.WriteFile(target, content, 0o644); err != nil {
			return fmt.Errorf("copy schema to %s: %w", target, err)
		}
	}
	return writeEmbedManifest(root, want)
}

func refuseSymlinkedAncestors(execDir, dir string) error {
	current := execDir
	parts := strings.Split(dir, "/")
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf(
				"embed_schema_dir %s passes through a symlink at %s; codegen writes only through real directories",
				dir,
				strings.Join(parts[:i+1], "/"),
			)
		}
	}
	return nil
}

func readEmbedManifest(root string) (map[string]bool, error) {
	raw, err := os.ReadFile(filepath.Join(root, embedManifest))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", embedManifest, err)
	}
	owned := map[string]bool{}
	for line := range strings.SplitSeq(string(raw), "\n") {
		if line = strings.TrimSuffix(line, "\r"); line != "" {
			owned[line] = true
		}
	}
	return owned, nil
}

func writeEmbedManifest(root string, want map[string][]byte) error {
	if len(want) == 0 {
		err := os.Remove(filepath.Join(root, embedManifest))
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	rels := slices.Sorted(maps.Keys(want))
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	return os.WriteFile(
		filepath.Join(root, embedManifest),
		[]byte(strings.Join(rels, "\n")+"\n"),
		0o644,
	)
}

func generateSingleFile(data *Data) error {
	return templates.Render(templates.Options{
		PackageName:     data.Config.Exec.Package,
		Filename:        data.Config.Exec.Filename,
		Data:            data,
		RegionTags:      true,
		GeneratedHeader: true,
		Packages:        data.Config.Packages,
		TemplateFS:      codegenTemplates,
		PruneOptions:    data.Config.GetPruneOptions(),
	})
}

func generatePerSchema(data *Data) error {
	err := generateRootFile(data)
	if err != nil {
		return err
	}

	builds := map[string]*Data{}

	err = addObjects(data, &builds)
	if err != nil {
		return err
	}

	err = addInputs(data, &builds)
	if err != nil {
		return err
	}

	err = addInterfaces(data, &builds)
	if err != nil {
		return err
	}

	err = addReferencedTypes(data, &builds)
	if err != nil {
		return err
	}

	err = addDirectives(data, &builds)
	if err != nil {
		return err
	}

	for filename, build := range builds {
		if filename == "" {
			continue
		}

		dir := data.Config.Exec.DirName
		path := filepath.Join(dir, filename)

		err = templates.Render(templates.Options{
			PackageName:     data.Config.Exec.Package,
			Filename:        path,
			Data:            build,
			RegionTags:      true,
			GeneratedHeader: true,
			Packages:        data.Config.Packages,
			TemplateFS:      codegenTemplates,
			PruneOptions:    data.Config.GetPruneOptions(),
		})
		if err != nil {
			return err
		}
	}

	return nil
}

func filename(p *ast.Position, config *config.Config) string {
	name := "common!"
	if p != nil && p.Src != nil {
		gqlname := filepath.Base(p.Src.Name)
		ext := filepath.Ext(p.Src.Name)
		name = strings.TrimSuffix(gqlname, ext)
	}

	filenameTempl := config.Exec.FilenameTemplate
	if filenameTempl == "" {
		filenameTempl = "{name}.generated.go"
	}

	return strings.ReplaceAll(filenameTempl, "{name}", name)
}

func addBuild(filename string, p *ast.Position, data *Data, builds *map[string]*Data) {
	buildConfig := *data.Config
	if p != nil {
		buildConfig.Sources = []*ast.Source{p.Src}
	}

	(*builds)[filename] = &Data{
		Config:           &buildConfig,
		QueryRoot:        data.QueryRoot,
		MutationRoot:     data.MutationRoot,
		SubscriptionRoot: data.SubscriptionRoot,
		AllDirectives:    data.AllDirectives,
	}
}

//go:embed root_.gotpl
var rootTemplate string

// Root file contains top-level definitions that should not be duplicated across the generated
// files for each schema file.
func generateRootFile(data *Data) error {
	dir := data.Config.Exec.DirName
	path := filepath.Join(dir, "root_.generated.go")

	return templates.Render(templates.Options{
		PackageName:     data.Config.Exec.Package,
		Template:        rootTemplate,
		Filename:        path,
		Data:            data,
		RegionTags:      false,
		GeneratedHeader: true,
		Packages:        data.Config.Packages,
		TemplateFS:      codegenTemplates,
		PruneOptions:    data.Config.GetPruneOptions(),
	})
}

func addObjects(data *Data, builds *map[string]*Data) error {
	for _, o := range data.Objects {
		if data.Config.Exec.Layout == config.ExecLayoutSplitPackages && o.Root &&
			len(o.Fields) > 0 {
			addRootObjectSlices(data, o, builds)
			continue
		}

		filename := filename(o.Position, data.Config)
		if (*builds)[filename] == nil {
			addBuild(filename, o.Position, data, builds)
		}

		(*builds)[filename].Objects = append((*builds)[filename].Objects, o)
	}
	return nil
}

// addRootObjectSlices slices a root Object (Query, Mutation, Subscription) by
// the declaring .graphql file of each of its fields, and buckets one slice
// into the build for each file. Root types are routinely extended across many
// .graphql files; without this, the whole root's machinery (every field's
// resolver, args parser, fieldContext, codec) lands in the build whose
// position the parser happened to assign — typically the alphabetically-first
// extend. The slice copies the Object's metadata (name, type, root flags,
// directives, implements set) but overrides Fields to that file's fields only.
// The root dispatcher itself is emitted by the gateway template (split_root_)
// rather than in any shard, and shards skip RegisterObject for root types, so
// shard slices can coexist without conflicting on the registry key.
func addRootObjectSlices(data *Data, o *Object, builds *map[string]*Data) {
	fieldsByFile := map[string][]*Field{}
	for _, f := range o.Fields {
		fname := filename(f.Position, data.Config)
		fieldsByFile[fname] = append(fieldsByFile[fname], f)
	}
	for fname, fields := range fieldsByFile {
		representativePos := fields[0].Position
		if (*builds)[fname] == nil {
			addBuild(fname, representativePos, data, builds)
		}
		slice := *o
		slice.Fields = fields
		(*builds)[fname].Objects = append((*builds)[fname].Objects, &slice)
	}
}

func addInputs(data *Data, builds *map[string]*Data) error {
	for _, in := range data.Inputs {
		filename := filename(in.Position, data.Config)
		if (*builds)[filename] == nil {
			addBuild(filename, in.Position, data, builds)
		}

		(*builds)[filename].Inputs = append((*builds)[filename].Inputs, in)
	}
	return nil
}

func addInterfaces(data *Data, builds *map[string]*Data) error {
	for k, inf := range data.Interfaces {
		filename := filename(inf.Position, data.Config)
		if (*builds)[filename] == nil {
			addBuild(filename, inf.Position, data, builds)
		}
		build := (*builds)[filename]

		if build.Interfaces == nil {
			build.Interfaces = map[string]*Interface{}
		}
		if build.Interfaces[k] != nil {
			return errors.New("conflicting interface keys")
		}

		build.Interfaces[k] = inf
	}
	return nil
}

// addDirectives ensures a per-schema build exists for every source file that
// declares directives with arguments. Without this, a .graphql file containing
// only directive declarations (no type definitions) would produce no generated
// file, leaving the dir_*_args argument-parsing functions undefined.
//
// No directive data is added to the build struct: Data.Args() calls
// Directives(), which is scoped to the build's Config.Sources, so it
// automatically picks up the right directive args for each per-schema file.
func addDirectives(data *Data, builds *map[string]*Data) error {
	for _, directive := range data.Directives() {
		if len(directive.Args) == 0 {
			continue
		}
		fname := filename(directive.Position, data.Config)
		if (*builds)[fname] == nil {
			addBuild(fname, directive.Position, data, builds)
		}
	}
	return nil
}

func addReferencedTypes(data *Data, builds *map[string]*Data) error {
	for k, rt := range data.ReferencedTypes {
		filename := filename(rt.Definition.Position, data.Config)
		if (*builds)[filename] == nil {
			addBuild(filename, rt.Definition.Position, data, builds)
		}
		build := (*builds)[filename]

		if build.ReferencedTypes == nil {
			build.ReferencedTypes = map[string]*config.TypeReference{}
		}
		if build.ReferencedTypes[k] != nil {
			return errors.New("conflicting referenced type keys")
		}

		build.ReferencedTypes[k] = rt
	}
	return nil
}
