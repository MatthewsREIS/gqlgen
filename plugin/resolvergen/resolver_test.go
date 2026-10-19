package resolvergen

import (
	"go/types"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/99designs/gqlgen/codegen"
	"github.com/99designs/gqlgen/codegen/config"
)

func TestLayoutSingleFile(t *testing.T) {
	_ = syscall.Unlink("testdata/singlefile/out/resolver.go")

	cfg, err := config.LoadConfig("testdata/singlefile/gqlgen.yml")
	require.NoError(t, err)
	p := Plugin{}

	require.NoError(t, cfg.Init())

	data, err := codegen.BuildData(cfg)
	require.NoError(t, err)

	require.NoError(t, p.GenerateCode(data))
	assertNoErrors(t, "github.com/99designs/gqlgen/plugin/resolvergen/testdata/singlefile/out")
}

func TestLayoutSingleFileWithEnableRewrite(t *testing.T) {
	// Ensure the resolver file exists before running the test
	resolverFilePath := "testdata/singlefile_preserve/out/resolver.go"
	_, err := os.Stat(resolverFilePath)
	if os.IsNotExist(err) {
		t.Fatalf("Expected resolver file does not exist: %s", resolverFilePath)
	}
	require.NoError(t, err)

	cfg, err := config.LoadConfig("testdata/singlefile_preserve/gqlgen.yml")
	require.NoError(t, err)
	p := Plugin{}

	require.NoError(t, cfg.Init())

	data, err := codegen.BuildData(cfg)
	require.NoError(t, err)

	require.NoError(t, p.GenerateCode(data))
	assertNoErrors(
		t,
		"github.com/99designs/gqlgen/plugin/resolvergen/testdata/singlefile_preserve/out",
	)
}

func TestLayoutFollowSchema(t *testing.T) {
	testFollowSchemaPersistence(t, "testdata/followschema")

	resolverFilePath := "testdata/followschema/out/schema.resolvers.go"
	overWriteFile(t, resolverFilePath+".txt", resolverFilePath)

	b, err := os.ReadFile(resolverFilePath)
	require.NoError(t, err)
	source := string(b)

	require.Contains(t, source, "(_ *customresolver.Resolver, err error)")
	require.Contains(t, source, "// Named return values are supported.")
	require.Contains(t, source, "// CustomerResolverType.Name implementation")
	require.Contains(t, source, "// AUserHelperFunction implementation")
}

func TestLayoutFollowSchemaWithCustomFilename(t *testing.T) {
	testFollowSchemaPersistence(t, "testdata/filetemplate")

	resolverFilePath := "testdata/filetemplate/out/schema.custom.go"
	overWriteFile(t, resolverFilePath+".txt", resolverFilePath)
	b, err := os.ReadFile(resolverFilePath)
	require.NoError(t, err)
	source := string(b)

	require.Contains(t, source, "// CustomerResolverType.Resolver implementation")
	require.Contains(t, source, "// CustomerResolverType.Name implementation")
	require.Contains(t, source, "// AUserHelperFunction implementation")
}

func TestLayoutInvalidModelPath(t *testing.T) {
	cfg, err := config.LoadConfig("testdata/invalid_model_path/gqlgen.yml")
	require.NoError(t, err)

	require.NoError(t, cfg.Init())

	_, err = codegen.BuildData(cfg)
	require.Error(t, err)
}

func TestOmitTemplateComment(t *testing.T) {
	_ = syscall.Unlink("testdata/omit_template_comment/resolver.go")

	cfg, err := config.LoadConfig("testdata/omit_template_comment/gqlgen.yml")
	require.NoError(t, err)
	p := Plugin{}

	require.NoError(t, cfg.Init())

	data, err := codegen.BuildData(cfg)
	require.NoError(t, err)

	require.NoError(t, p.GenerateCode(data))
	assertNoErrors(
		t,
		"github.com/99designs/gqlgen/plugin/resolvergen/testdata/omit_template_comment/out",
	)
}

func TestResolver_Implementation(t *testing.T) {
	_ = syscall.Unlink("testdata/resolver_implementor/resolver.go")

	cfg, err := config.LoadConfig("testdata/resolver_implementor/gqlgen.yml")
	require.NoError(t, err)
	p := Plugin{}

	require.NoError(t, cfg.Init())

	data, err := codegen.BuildData(cfg, &implementorTest{})
	require.NoError(t, err)

	require.NoError(t, p.GenerateCode(data))
	assertNoErrors(
		t,
		"github.com/99designs/gqlgen/plugin/resolvergen/testdata/resolver_implementor/out",
	)
}

func TestCustomResolverTemplate(t *testing.T) {
	_ = syscall.Unlink("testdata/resolvertemplate/out/resolver.go")
	cfg, err := config.LoadConfig("testdata/resolvertemplate/gqlgen.yml")
	require.NoError(t, err)
	p := Plugin{}

	require.NoError(t, cfg.Init())

	data, err := codegen.BuildData(cfg)
	require.NoError(t, err)

	require.NoError(t, p.GenerateCode(data))
}

func TestCommentDirective(t *testing.T) {
	_ = syscall.Unlink("testdata/comment_directive/resolver.go")

	cfg, err := config.LoadConfig("testdata/comment_directive/gqlgen.yml")
	require.NoError(t, err)
	p := Plugin{}

	require.NoError(t, cfg.Init())

	data, err := codegen.BuildData(cfg)
	require.NoError(t, err)

	require.NoError(t, p.GenerateCode(data))
	assertNoErrors(
		t,
		"github.com/99designs/gqlgen/plugin/resolvergen/testdata/comment_directive/out",
	)

	b, err := os.ReadFile("testdata/comment_directive/out/schema.resolvers.go")
	require.NoError(t, err)
	source := string(b)

	require.Contains(t, source, "//nolint:test // test")
}

func testFollowSchemaPersistence(t *testing.T, dir string) {
	_ = syscall.Unlink(dir + "/out/resolver.go")

	cfg, err := config.LoadConfig(dir + "/gqlgen.yml")
	require.NoError(t, err)
	p := Plugin{}

	require.NoError(t, cfg.Init())

	data, err := codegen.BuildData(cfg)
	require.NoError(t, err)

	require.NoError(t, p.GenerateCode(data))
	assertNoErrors(t, "github.com/99designs/gqlgen/plugin/resolvergen/"+dir+"/out")
}

func overWriteFile(t *testing.T, sourceFile, destinationFile string) {
	input, err := os.ReadFile(sourceFile)
	require.NoError(t, err)

	err = os.WriteFile(destinationFile, input, 0o644)
	require.NoError(t, err)
}

func assertNoErrors(t *testing.T, pkg string) {
	t.Helper()
	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedCompiledGoFiles |
			packages.NeedImports |
			packages.NeedTypes |
			packages.NeedTypesSizes,
	}, pkg)
	require.NoError(t, err)

	var errFilePos []string
	var errors []packages.Error
	for _, pkg := range pkgs {
		errors = append(errors, pkg.Errors...)
		for _, err := range pkg.Errors {
			errFilePos = append(errFilePos, err.Pos+":"+err.Msg)
		}
	}
	require.Emptyf(t, errors, "There are compilation errors:\n"+
		strings.Join(errFilePos, "\n"))
}

type implementorTest struct{}

func (i *implementorTest) Implement(_ string, _ *codegen.Field) string {
	return "panic(\"implementor implemented me\")"
}

func TestLayoutFollowSchemaWithRootType(t *testing.T) {
	_ = syscall.Unlink("testdata/root_type/out/resolver.go")
	_ = syscall.Unlink("testdata/root_type/out/resolver_root.go")

	cfg, err := config.LoadConfig("testdata/root_type/gqlgen.yml")
	require.NoError(t, err)
	require.NoError(t, cfg.Init())
	p := Plugin{}

	data, err := codegen.BuildData(cfg)
	require.NoError(t, err)
	require.NoError(t, p.GenerateCode(data))

	first, err := os.ReadFile("testdata/root_type/out/schema.resolvers.go")
	require.NoError(t, err)
	source := string(first)
	require.Contains(t, source, "func (r ResolverRoot) Query() ")
	require.Contains(t, source, "return &queryCustomResolverType{r.CustomResolverType}")
	require.NotContains(t, source, "func (r *CustomResolverType) Query()")
	require.NotContains(t, source, "!!! WARNING !!!")

	root, err := os.ReadFile("testdata/root_type/out/resolver_root.go")
	require.NoError(t, err)
	require.Contains(t, string(root), "type ResolverRoot struct{ *CustomResolverType }")

	pkg := "github.com/99designs/gqlgen/plugin/resolvergen/testdata/root_type/out"
	assertNoErrors(t, pkg)
	methods := pointerMethodNames(t, pkg, "queryCustomResolverType")
	require.Contains(t, methods, "Resolver")
	require.NotContains(t, methods, "Query")

	data, err = codegen.BuildData(cfg)
	require.NoError(t, err)
	require.NoError(t, p.GenerateCode(data))
	second, err := os.ReadFile("testdata/root_type/out/schema.resolvers.go")
	require.NoError(t, err)
	require.Equal(t, string(first), string(second))
}

func pointerMethodNames(t *testing.T, pkgPath, typeName string) []string {
	t.Helper()
	pkgs, err := packages.Load(
		&packages.Config{Mode: packages.NeedName | packages.NeedTypes},
		pkgPath,
	)
	require.NoError(t, err)
	require.Len(t, pkgs, 1)
	obj := pkgs[0].Types.Scope().Lookup(typeName)
	require.NotNil(t, obj, "type %s not found in %s", typeName, pkgPath)
	set := types.NewMethodSet(types.NewPointer(obj.Type()))
	names := make([]string, 0, set.Len())
	for sel := range set.Methods() {
		names = append(names, sel.Obj().Name())
	}
	return names
}

func TestLayoutSingleFileWithRootType(t *testing.T) {
	_ = syscall.Unlink("testdata/root_type_singlefile/out/resolver.go")

	cfg, err := config.LoadConfig("testdata/root_type_singlefile/gqlgen.yml")
	require.NoError(t, err)
	require.NoError(t, cfg.Init())
	p := Plugin{}

	data, err := codegen.BuildData(cfg)
	require.NoError(t, err)
	require.NoError(t, p.GenerateCode(data))

	first, err := os.ReadFile("testdata/root_type_singlefile/out/resolver.go")
	require.NoError(t, err)
	source := string(first)
	require.Contains(t, source, "type ResolverRoot struct{ *CustomResolverType }")
	require.Contains(t, source, "func (r ResolverRoot) Query() ")
	require.NotContains(t, source, "func (r *CustomResolverType) Query()")

	pkg := "github.com/99designs/gqlgen/plugin/resolvergen/testdata/root_type_singlefile/out"
	assertNoErrors(t, pkg)
	methods := pointerMethodNames(t, pkg, "queryCustomResolverType")
	require.Contains(t, methods, "Resolver")
	require.NotContains(t, methods, "Query")

	data, err = codegen.BuildData(cfg)
	require.NoError(t, err)
	require.NoError(t, p.GenerateCode(data))
	second, err := os.ReadFile("testdata/root_type_singlefile/out/resolver.go")
	require.NoError(t, err)
	require.Equal(
		t,
		1,
		strings.Count(string(second), "type ResolverRoot struct{ *CustomResolverType }"),
	)
	require.Equal(t, 1, strings.Count(string(second), "func (r ResolverRoot) Query() "))
}

func TestRootTypeRejectsAnAccessorNamedLikeTheResolverType(t *testing.T) {
	cfg, err := config.LoadConfig("testdata/root_type_name_collision/gqlgen.yml")
	require.NoError(t, err)
	require.NoError(t, cfg.Init())

	data, err := codegen.BuildData(cfg)
	require.NoError(t, err)
	require.EqualError(
		t,
		(&Plugin{}).GenerateCode(data),
		"resolver root_type: the Resolver accessor would share its name with the Resolver field embedded in ResolverRoot; rename resolver.type",
	)
}

func TestRootTypeRejectsAWrapperFileThatIsAResolverFile(t *testing.T) {
	cfg, err := config.LoadConfig("testdata/root_type_path_collision/gqlgen.yml")
	require.NoError(t, err)
	require.NoError(t, cfg.Init())

	data, err := codegen.BuildData(cfg)
	require.NoError(t, err)
	require.ErrorContains(
		t,
		(&Plugin{}).GenerateCode(data),
		"resolver_root.go is also the resolver file for schema testdata/root_type_path_collision/resolver_root.graphqls",
	)
}

func TestRootTypeRejectsAWrapperFileThatIsTheExecFile(t *testing.T) {
	cfg, err := config.LoadConfig("testdata/root_type_exec_collision/gqlgen.yml")
	require.NoError(t, err)
	require.NoError(t, cfg.Init())

	data, err := codegen.BuildData(cfg)
	require.NoError(t, err)
	require.ErrorContains(
		t,
		(&Plugin{}).GenerateCode(data),
		"resolver_root.go is also the exec output file",
	)
}

func TestRootTypeRejectsAWrapperFileThatIsAFollowSchemaExecFile(t *testing.T) {
	cfg, err := config.LoadConfig("testdata/root_type_exec_fs_collision/gqlgen.yml")
	require.NoError(t, err)
	require.NoError(t, cfg.Init())

	data, err := codegen.BuildData(cfg)
	require.NoError(t, err)
	require.ErrorContains(
		t,
		(&Plugin{}).GenerateCode(data),
		"resolver_root.go is also the exec output file",
	)
}

func TestRootTypeIgnoresOutputFilesInSingleFileLayout(t *testing.T) {
	cfg := &config.Config{
		Resolver: config.ResolverConfig{
			Layout:   config.LayoutSingleFile,
			Filename: "graph/resolver.go",
			Type:     "Resolver",
			RootType: "RootResolver",
		},
		Exec: config.ExecConfig{Filename: "graph/resolver_root.go"},
	}
	require.NoError(t, checkRootType(&codegen.Data{Config: cfg}))
}

func TestRootTypeRefusesToOverwriteAHandwrittenWrapperFile(t *testing.T) {
	const handwritten = "testdata/root_type_handwritten/out/resolver_root.go"
	before, err := os.ReadFile(handwritten)
	require.NoError(t, err)

	cfg, err := config.LoadConfig("testdata/root_type_handwritten/gqlgen.yml")
	require.NoError(t, err)
	require.NoError(t, cfg.Init())

	data, err := codegen.BuildData(cfg)
	require.NoError(t, err)
	require.ErrorContains(
		t,
		(&Plugin{}).GenerateCode(data),
		"resolver_root.go exists and was not generated by gqlgen",
	)

	after, err := os.ReadFile(handwritten)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))
	require.NoFileExists(t, "testdata/root_type_handwritten/out/schema.resolvers.go")
}

func TestRootTypeRejectsAWrapperFileThatIsThePreludeExecFile(t *testing.T) {
	cfg, err := config.LoadConfig("testdata/root_type_exec_prelude_collision/gqlgen.yml")
	require.NoError(t, err)
	require.NoError(t, cfg.Init())

	data, err := codegen.BuildData(cfg)
	require.NoError(t, err)
	require.ErrorContains(
		t,
		(&Plugin{}).GenerateCode(data),
		"prelude_root.go is also the exec output file",
	)
}
