package codegen

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// writeEmbeddedSchemaCopies recreates Exec.EmbedSchemaDir from scratch, so a
// schema file removed from the config leaves no stale copy behind.
func writeEmbeddedSchemaCopies(data *Data) error {
	dir := data.Config.Exec.EmbedSchemaDir
	if dir == "" {
		return nil
	}
	execDir := data.Config.Exec.Dir()
	if err := os.RemoveAll(filepath.Join(execDir, filepath.FromSlash(dir))); err != nil {
		return fmt.Errorf("clear embed_schema_dir %s: %w", dir, err)
	}
	for _, s := range data.AugmentedSources {
		if s.EmbedPath == "" {
			continue
		}
		target := filepath.Join(execDir, filepath.FromSlash(s.EmbedPath))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, []byte(s.Source), 0o644); err != nil {
			return fmt.Errorf("copy schema %s to %s: %w", s.RelativePath, target, err)
		}
	}
	return nil
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
