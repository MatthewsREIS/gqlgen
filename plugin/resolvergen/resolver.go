package resolvergen

import (
	_ "embed"
	"errors"
	"fmt"
	"go/ast"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/99designs/gqlgen/codegen"
	"github.com/99designs/gqlgen/codegen/config"
	"github.com/99designs/gqlgen/codegen/templates"
	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/internal/rewrite"
	"github.com/99designs/gqlgen/plugin"
)

//go:embed resolver.gotpl
var resolverTemplate string

func New() plugin.Plugin {
	return &Plugin{}
}

type Plugin struct{}

var _ plugin.CodeGenerator = &Plugin{}

func (m *Plugin) Name() string {
	return "resolvergen"
}

func (m *Plugin) GenerateCode(data *codegen.Data) error {
	if !data.Config.Resolver.IsDefined() {
		return nil
	}
	if err := checkRootType(data); err != nil {
		return err
	}

	switch data.Config.Resolver.Layout {
	case config.LayoutSingleFile:
		return m.generateSingleFile(data)
	case config.LayoutFollowSchema:

		return m.generatePerSchema(data)
	}

	return nil
}

func (m *Plugin) generateSingleFile(data *codegen.Data) error {
	file := File{}

	if fileExists(data.Config.Resolver.Filename) &&
		data.Config.Resolver.PreserveResolver {
		// file already exists and config says not to update resolver
		// so just return
		return nil
	}

	rewriter, err := rewrite.New(data.Config.Resolver.Dir())
	if err != nil {
		return err
	}
	if root := data.Config.Resolver.RootType; root != "" {
		rewriter.MarkStructCopied(root)
	}

	for _, o := range data.Objects {
		if o.HasResolvers() {
			caser := cases.Title(language.English, cases.NoLower)
			rewriter.MarkStructCopied(
				templates.LcFirst(o.Name) + templates.UcFirst(data.Config.Resolver.Type),
			)
			rewriter.GetMethodBody(data.Config.Resolver.Type, caser.String(o.Name))
			if root := data.Config.Resolver.RootType; root != "" {
				rewriter.GetMethodBody(root, caser.String(o.Name))
			}

			file.Objects = append(file.Objects, o)
		}

		for _, f := range o.Fields {
			if !f.IsResolver {
				continue
			}

			structName := templates.LcFirst(o.Name) + templates.UcFirst(data.Config.Resolver.Type)
			comment := strings.TrimSpace(
				strings.TrimLeft(rewriter.GetMethodComment(structName, f.GoFieldName), `\`),
			)
			implementation := strings.TrimSpace(rewriter.GetMethodBody(structName, f.GoFieldName))
			if implementation != "" {
				resolver := Resolver{
					o,
					f,
					rewriter.GetPrevDecl(structName, f.GoFieldName),
					comment,
					implementation,
					nil,
				}
				file.Resolvers = append(file.Resolvers, &resolver)
			} else {
				resolver := Resolver{o, f, nil, "", `panic("not implemented")`, nil}
				file.Resolvers = append(file.Resolvers, &resolver)
			}
		}
	}

	if fileExists(data.Config.Resolver.Filename) {
		file.name = data.Config.Resolver.Filename
		file.imports = rewriter.ExistingImports(file.name)
		file.RemainingSource = rewriter.RemainingSource(file.name)
	}

	resolverBuild := &ResolverBuild{
		File:                &file,
		PackageName:         data.Config.Resolver.Package,
		ResolverType:        data.Config.Resolver.Type,
		RootType:            data.Config.Resolver.RootType,
		HasRoot:             true,
		OmitTemplateComment: data.Config.Resolver.OmitTemplateComment,
	}

	newResolverTemplate := resolverTemplate
	if data.Config.Resolver.ResolverTemplate != "" {
		newResolverTemplate = readResolverTemplate(data.Config.Resolver.ResolverTemplate)
	}

	fileNotice := `// THIS CODE WILL BE UPDATED WITH SCHEMA CHANGES. PREVIOUS IMPLEMENTATION FOR SCHEMA CHANGES WILL BE KEPT IN THE COMMENT SECTION. IMPLEMENTATION FOR UNCHANGED SCHEMA WILL BE KEPT.`
	if data.Config.Resolver.PreserveResolver {
		fileNotice = `// THIS CODE IS A STARTING POINT ONLY. IT WILL NOT BE UPDATED WITH SCHEMA CHANGES.`
	}

	return templates.Render(templates.Options{
		PackageName:  data.Config.Resolver.Package,
		FileNotice:   fileNotice,
		Filename:     data.Config.Resolver.Filename,
		Data:         resolverBuild,
		Packages:     data.Config.Packages,
		Template:     newResolverTemplate,
		PruneOptions: data.Config.GetPruneOptions(),
	})
}

func (m *Plugin) generatePerSchema(data *codegen.Data) error {
	rewriter, err := rewrite.New(data.Config.Resolver.Dir())
	if err != nil {
		return err
	}
	if root := data.Config.Resolver.RootType; root != "" {
		rewriter.MarkStructCopied(root)
	}

	files := map[string]*File{}

	objects := make(codegen.Objects, len(data.Objects)+len(data.Inputs))
	copy(objects, data.Objects)
	copy(objects[len(data.Objects):], data.Inputs)

	for _, o := range objects {
		if o.HasResolvers() {
			fnCase := gqlToResolverName(
				data.Config.Resolver.Dir(),
				o.Position.Src.Name,
				data.Config.Resolver.FilenameTemplate,
			)
			fn := strings.ToLower(fnCase)
			if files[fn] == nil {
				files[fn] = &File{
					name: fnCase,
				}
			}

			caser := cases.Title(language.English, cases.NoLower)
			rewriter.MarkStructCopied(
				templates.LcFirst(o.Name) + templates.UcFirst(data.Config.Resolver.Type),
			)
			rewriter.GetMethodBody(data.Config.Resolver.Type, caser.String(o.Name))
			if root := data.Config.Resolver.RootType; root != "" {
				rewriter.GetMethodBody(root, caser.String(o.Name))
			}
			files[fn].Objects = append(files[fn].Objects, o)
		}
		for _, f := range o.Fields {
			if !f.IsResolver {
				continue
			}
			structName := templates.LcFirst(o.Name) + templates.UcFirst(data.Config.Resolver.Type)
			// TODO(steve): Why do we need to trimLeft "\" here? Some bazel thing?
			comment := strings.TrimSpace(
				strings.TrimLeft(rewriter.GetMethodComment(structName, f.GoFieldName), `\`),
			)
			implementation := strings.TrimSpace(rewriter.GetMethodBody(structName, f.GoFieldName))
			resolver := Resolver{
				o,
				f,
				rewriter.GetPrevDecl(structName, f.GoFieldName),
				comment,
				implementation,
				nil,
			}
			var implExists bool
			for _, p := range data.Plugins {
				rImpl, ok := p.(plugin.ResolverImplementer)
				if !ok {
					continue
				}
				if implExists {
					return errors.New("multiple plugins implement ResolverImplementer")
				}
				implExists = true
				resolver.ImplementationRender = rImpl.Implement
			}
			fnCase := gqlToResolverName(
				data.Config.Resolver.Dir(),
				f.Position.Src.Name,
				data.Config.Resolver.FilenameTemplate,
			)
			fn := strings.ToLower(fnCase)
			if files[fn] == nil {
				files[fn] = &File{
					name: fnCase,
				}
			}

			files[fn].Resolvers = append(files[fn].Resolvers, &resolver)
		}
	}

	var allImports []string
	for _, file := range files {
		file.imports = rewriter.ExistingImports(file.name)
		file.RemainingSource = rewriter.RemainingSource(file.name)

		for _, i := range file.imports {
			allImports = append(allImports, i.ImportPath)
		}
	}
	data.Config.Packages.LoadAllNames(
		allImports...) // Preload all names in one Load call for performance reasons

	newResolverTemplate := resolverTemplate
	if data.Config.Resolver.ResolverTemplate != "" {
		newResolverTemplate = readResolverTemplate(data.Config.Resolver.ResolverTemplate)
	}

	for _, file := range files {
		if fileExists(file.name) &&
			data.Config.Resolver.PreserveResolver {
			// file already exists and config says not to update resolver
			continue
		}
		resolverBuild := &ResolverBuild{
			File:                file,
			PackageName:         data.Config.Resolver.Package,
			ResolverType:        data.Config.Resolver.Type,
			RootType:            data.Config.Resolver.RootType,
			OmitTemplateComment: data.Config.Resolver.OmitTemplateComment,
		}

		var fileNotice strings.Builder
		if !data.Config.OmitGQLGenFileNotice {
			fileNotice.WriteString(`
			// This file will be automatically regenerated based on the schema, any resolver
			// implementations
			// will be copied through when generating and any unknown code will be moved to the end.
			// Code generated by github.com/99designs/gqlgen`,
			)
			if !data.Config.OmitGQLGenVersionInFileNotice {
				fileNotice.WriteString(` version `)
				fileNotice.WriteString(graphql.Version)
			}
		}

		err := templates.Render(templates.Options{
			PackageName:  data.Config.Resolver.Package,
			FileNotice:   fileNotice.String(),
			Filename:     file.name,
			Data:         resolverBuild,
			Packages:     data.Config.Packages,
			Template:     newResolverTemplate,
			PruneOptions: data.Config.GetPruneOptions(),
		})
		if err != nil {
			return err
		}
	}

	if root := data.Config.Resolver.RootType; root != "" {
		err := templates.Render(templates.Options{
			PackageName:     data.Config.Resolver.Package,
			GeneratedHeader: true,
			Template:        `type {{.RootType}} struct { *{{.Type}} }`,
			Filename:        rootFilename(data.Config.Resolver.Filename),
			Data:            data.Config.Resolver,
			Packages:        data.Config.Packages,
			PruneOptions:    data.Config.GetPruneOptions(),
		})
		if err != nil {
			return err
		}
	}

	if !fileExists(data.Config.Resolver.Filename) {
		err := templates.Render(templates.Options{
			PackageName: data.Config.Resolver.Package,
			FileNotice: `
				// This file will not be regenerated automatically.
				//
				// It serves as dependency injection for your app, add any dependencies you require
				// here.`,
			Template:     `type {{.}} struct {}`,
			Filename:     data.Config.Resolver.Filename,
			Data:         data.Config.Resolver.Type,
			Packages:     data.Config.Packages,
			PruneOptions: data.Config.GetPruneOptions(),
		})
		if err != nil {
			return err
		}
	}
	return nil
}

type ResolverBuild struct {
	*File
	HasRoot             bool
	PackageName         string
	ResolverType        string
	RootType            string
	OmitTemplateComment bool
}

type File struct {
	name string
	// These are separated because the type definition of the resolver object may live in a
	// different file from the resolver method implementations, for example when extending a type in
	// a different graphql schema file
	Objects         []*codegen.Object
	Resolvers       []*Resolver
	imports         []rewrite.Import
	RemainingSource string
}

func (f *File) Imports() string {
	for _, imp := range f.imports {
		if imp.Alias == "" {
			_, _ = templates.CurrentImports.Reserve(imp.ImportPath)
		} else {
			_, _ = templates.CurrentImports.Reserve(imp.ImportPath, imp.Alias)
		}
	}
	return ""
}

type Resolver struct {
	Object               *codegen.Object
	Field                *codegen.Field
	PrevDecl             *ast.FuncDecl
	Comment              string
	ImplementationStr    string
	ImplementationRender func(prevImplementation string, r *codegen.Field) string
}

func (r *Resolver) Implementation() string {
	if r.ImplementationRender != nil {
		// use custom implementation
		return r.ImplementationRender(r.ImplementationStr, r.Field)
	}
	// if not implementation was previously used, use default implementation
	if r.ImplementationStr == "" {
		// use default implementation, if no implementation was previously used
		return fmt.Sprintf(
			"panic(fmt.Errorf(\"not implemented: %v - %v\"))",
			r.Field.GoFieldName,
			r.Field.Name,
		)
	}
	// use previously used implementation
	return r.ImplementationStr
}

func rootFilename(resolverFilename string) string {
	return strings.TrimSuffix(resolverFilename, ".go") + "_root.go"
}

func checkRootType(data *codegen.Data) error {
	cfg := data.Config.Resolver
	if cfg.RootType == "" {
		return nil
	}
	rootFile := rootFilename(cfg.Filename)
	samePath := func(a, b string) bool {
		return strings.EqualFold(filepath.ToSlash(a), filepath.ToSlash(b))
	}
	for _, out := range []struct{ name, path string }{
		{"exec", data.Config.Exec.Filename},
		{"model", data.Config.Model.Filename},
		{"federation", data.Config.Federation.Filename},
	} {
		if out.path != "" && samePath(out.path, rootFile) {
			return fmt.Errorf(
				"resolver root_type: its file %s is also the %s output file; change resolver.filename",
				rootFile,
				out.name,
			)
		}
	}
	collides := func(src string) bool {
		return cfg.Layout == config.LayoutFollowSchema &&
			samePath(gqlToResolverName(cfg.Dir(), src, cfg.FilenameTemplate), rootFile)
	}
	for _, objects := range []codegen.Objects{data.Objects, data.Inputs} {
		for _, o := range objects {
			if o.HasResolvers() {
				if templates.UcFirst(o.Name) == cfg.Type {
					return fmt.Errorf(
						"resolver root_type: the %s accessor would share its name with the %s field embedded in %s; rename resolver.type",
						templates.UcFirst(o.Name),
						cfg.Type,
						cfg.RootType,
					)
				}
				if collides(o.Position.Src.Name) {
					return fmt.Errorf(
						"resolver root_type: its file %s is also the resolver file for schema %s; change filename_template or resolver.filename",
						rootFile,
						o.Position.Src.Name,
					)
				}
			}
			for _, f := range o.Fields {
				if f.IsResolver && collides(f.Position.Src.Name) {
					return fmt.Errorf(
						"resolver root_type: its file %s is also the resolver file for schema %s; change filename_template or resolver.filename",
						rootFile,
						f.Position.Src.Name,
					)
				}
			}
		}
	}
	return nil
}

func gqlToResolverName(base, gqlname, filenameTmpl string) string {
	gqlname = filepath.Base(gqlname)
	ext := filepath.Ext(gqlname)
	if filenameTmpl == "" {
		filenameTmpl = "{name}.resolvers.go"
	}
	filename := strings.ReplaceAll(filenameTmpl, "{name}", strings.TrimSuffix(gqlname, ext))
	return filepath.Join(base, filename)
}

func readResolverTemplate(customResolverTemplate string) string {
	contentBytes, err := os.ReadFile(customResolverTemplate)
	if err != nil {
		panic(err)
	}
	return string(contentBytes)
}

func fileExists(fileName string) bool {
	if _, err := os.Stat(fileName); err == nil {
		return true
	}
	return false
}
