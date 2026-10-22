package codegen

import (
	_ "embed"
	"fmt"
	"go/token"
	"hash/fnv"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/99designs/gqlgen/codegen/config"
	"github.com/99designs/gqlgen/codegen/templates"
	internalcode "github.com/99designs/gqlgen/internal/code"
)

//go:embed split_root_.gotpl
var splitRootTemplate string

//go:embed split_shard_.gotpl
var splitShardTemplate string

//go:embed split_fields_.gotpl
var splitFieldsTemplate string

//go:embed split_args_.gotpl
var splitArgsTemplate string

//go:embed split_complexity_.gotpl
var splitComplexityTemplate string

//go:embed split_inputs_.gotpl
var splitInputsTemplate string

//go:embed split_codecs_.gotpl
var splitCodecsTemplate string

//go:embed split_fieldcontext_.gotpl
var splitFieldContextTemplate string

//go:embed split_register_.gotpl
var splitRegisterTemplate string

//go:embed split_imports_.gotpl
var splitImportsTemplate string

//go:embed split_runtime_.gotpl
var splitRuntimeTemplate string

//go:embed directives.gotpl
var directivesTemplate string

//go:embed interface.gotpl
var interfaceTemplate string

type splitRootTemplateData struct {
	*Data
	Scope string
}

type splitShardTemplateData struct {
	*Data
	Scope               string
	ShardName           string
	Ownership           *splitOwnershipPlanner
	FieldByLookupKey    map[string]*Field
	FieldByArgsFunc     map[string]*Field
	InputByName         map[string]*Object
	CodecByFunc         map[string]*config.TypeReference
	OwnedFieldKeyChunks [][]string
}

type splitImportsTemplateData struct {
	Import string
}

func generateSplitPackages(data *Data) error {
	if err := cleanupSplitGeneratedOutputs(data); err != nil {
		return err
	}

	scope := splitScope(data)

	if err := generateSplitRootGateway(data, scope); err != nil {
		return err
	}

	if err := generateSplitRootRuntime(data); err != nil {
		return err
	}

	shardImports, err := generateSplitShardPackages(data, scope)
	if err != nil {
		return err
	}

	return generateSplitShardImports(data, shardImports)
}

func cleanupSplitGeneratedOutputs(data *Data) error {
	if err := removeSplitGeneratedByGlob(
		filepath.Join(data.Config.Exec.Dir(), "split_shard_import_*.generated.go"),
		"split import",
	); err != nil {
		return err
	}

	runtimePath := filepath.Join(data.Config.Exec.Dir(), "split_runtime.generated.go")
	if err := os.Remove(runtimePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale split runtime file %q: %w", runtimePath, err)
	}

	generatedShardFiles, err := listSplitShardGeneratedFiles(
		data.Config.Exec.ShardDir,
		data.Config.Exec.ShardFilenameTemplate,
	)
	if err != nil {
		return err
	}
	for _, path := range generatedShardFiles {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale split shard file %q: %w", path, err)
		}
	}

	return pruneEmptyDirs(data.Config.Exec.ShardDir)
}

func removeSplitGeneratedByGlob(pattern, kind string) error {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("invalid %s cleanup glob %q: %w", kind, pattern, err)
	}

	for _, match := range matches {
		if err := os.Remove(match); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale %s file %q: %w", kind, match, err)
		}
	}

	return nil
}

func listSplitShardGeneratedFiles(root, _ string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat split shard root %q: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("split shard root %q is not a directory", root)
	}

	var generated []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}

		owned, ownerErr := isSplitOwnedGeneratedFile(path, d.Name())
		if ownerErr != nil {
			return ownerErr
		}
		if owned {
			generated = append(generated, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk split shard root %q: %w", root, err)
	}

	sort.Strings(generated)
	return generated, nil
}

func isSplitOwnedGeneratedFile(path, name string) (bool, error) {
	if name == "register.generated.go" {
		return true, nil
	}

	if filepath.Ext(name) != ".go" {
		return false, nil
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read split shard candidate %q: %w", path, err)
	}

	if strings.Contains(string(contents), "const splitScope =") {
		return true, nil
	}

	return false, nil
}

func pruneEmptyDirs(root string) error {
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat split shard root for prune %q: %w", root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("split shard root %q is not a directory", root)
	}

	var dirs []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk split shard root for prune %q: %w", root, err)
	}

	sort.Slice(dirs, func(i, j int) bool {
		if len(dirs[i]) == len(dirs[j]) {
			return dirs[i] > dirs[j]
		}
		return len(dirs[i]) > len(dirs[j])
	})

	for _, dir := range dirs {
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				continue
			}
			return fmt.Errorf("read split shard dir %q: %w", dir, readErr)
		}
		if len(entries) > 0 {
			continue
		}
		if removeErr := os.Remove(dir); removeErr != nil && !os.IsNotExist(removeErr) {
			return fmt.Errorf("remove empty split shard dir %q: %w", dir, removeErr)
		}
	}

	return nil
}

func generateSplitRootGateway(data *Data, scope string) error {
	return templates.Render(templates.Options{
		PackageName:     data.Config.Exec.Package,
		Template:        splitRootTemplate + "\n" + directivesTemplate + "\n" + interfaceTemplate,
		Filename:        data.Config.Exec.Filename,
		Data:            splitRootTemplateData{Data: data, Scope: scope},
		RegionTags:      false,
		GeneratedHeader: true,
		Packages:        data.Config.Packages,
		TemplateFS:      codegenTemplates,
	})
}

func generateSplitRootRuntime(data *Data) error {
	path := filepath.Join(data.Config.Exec.Dir(), "split_runtime.generated.go")
	return templates.Render(templates.Options{
		PackageName:     data.Config.Exec.Package,
		Template:        splitRuntimeTemplate,
		Filename:        path,
		Data:            data,
		RegionTags:      false,
		GeneratedHeader: true,
		Packages:        data.Config.Packages,
		TemplateFS:      codegenTemplates,
	})
}

// buildOwnedFieldKeyChunksForShard returns the field-owner keys belonging to
// shardName, split into chunks of chunkSize. The result is deterministic
// (input keys are already sorted by planSplitOwnership). Used by the register
// template to emit one init() per chunk instead of one giant init(), which
// exposes function-level parallelism to the Go compiler.
func buildOwnedFieldKeyChunksForShard(
	ownership *splitOwnershipPlanner,
	shardName string,
	chunkSize int,
) [][]string {
	var owned []string
	for _, key := range ownership.FieldOwnerKeys {
		if ownership.FieldOwner[key] == shardName {
			owned = append(owned, key)
		}
	}
	if len(owned) == 0 {
		return nil
	}
	var chunks [][]string
	for i := 0; i < len(owned); i += chunkSize {
		end := min(i+chunkSize, len(owned))
		chunks = append(chunks, owned[i:end])
	}
	return chunks
}

func generateSplitShardPackages(data *Data, scope string) ([]string, error) {
	ownership, err := planSplitOwnership(data)
	if err != nil {
		return nil, err
	}

	builds := map[string]*Data{}
	if err := addObjects(data, &builds); err != nil {
		return nil, err
	}

	var filenames []string
	for filename := range builds {
		if filename != "" {
			filenames = append(filenames, filename)
		}
	}
	sort.Strings(filenames)

	var imports []string
	usedShardNames := map[string]string{}
	inputByName := buildInputLookupMap(data)
	codecByFunc := buildCodecLookupMap(data)

	for _, filename := range filenames {
		build := builds[filename]
		if build == nil || len(build.Objects) == 0 {
			continue
		}

		shardName := splitShardName(filename, build, usedShardNames)
		shardDir := filepath.Join(data.Config.Exec.ShardDir, shardName)
		shardFile := strings.ReplaceAll(data.Config.Exec.ShardFilenameTemplate, "{name}", shardName)
		shardPath := filepath.Join(shardDir, shardFile)

		if err := os.MkdirAll(shardDir, 0o755); err != nil {
			return nil, fmt.Errorf("create split shard dir %q: %w", shardDir, err)
		}

		pkg := internalcode.NameForDir(shardDir)
		if pkg == "" {
			pkg = shardName
		}
		build.Config.Exec.Package = pkg

		if err := templates.Render(templates.Options{
			PackageName: pkg,
			Template:    splitShardTemplate + "\n" + splitFieldsTemplate + "\n" + splitFieldContextTemplate + "\n" + splitArgsTemplate + "\n" + splitComplexityTemplate + "\n" + splitInputsTemplate + "\n" + splitCodecsTemplate,
			Filename:    shardPath,
			Data: splitShardTemplateData{
				Data:             build,
				Scope:            scope,
				ShardName:        shardName,
				Ownership:        ownership,
				FieldByLookupKey: buildFieldLookupMap(build),
				FieldByArgsFunc:  buildArgsFuncLookupMap(build),
				InputByName:      inputByName,
				CodecByFunc:      codecByFunc,
			},
			RegionTags:      false,
			GeneratedHeader: true,
			Packages:        data.Config.Packages,
		}); err != nil {
			return nil, err
		}

		registerPath := filepath.Join(shardDir, "register.generated.go")
		if err := templates.Render(templates.Options{
			PackageName: pkg,
			Template:    splitFieldsTemplate + "\n" + splitRegisterTemplate,
			Filename:    registerPath,
			Data: splitShardTemplateData{
				Data:                build,
				Scope:               scope,
				ShardName:           shardName,
				Ownership:           ownership,
				FieldByLookupKey:    buildFieldLookupMap(build),
				FieldByArgsFunc:     buildArgsFuncLookupMap(build),
				InputByName:         inputByName,
				CodecByFunc:         codecByFunc,
				OwnedFieldKeyChunks: buildOwnedFieldKeyChunksForShard(ownership, shardName, 50),
			},
			RegionTags:      false,
			GeneratedHeader: true,
			Packages:        data.Config.Packages,
		}); err != nil {
			return nil, err
		}

		importPath := internalcode.ImportPathForDir(shardDir)
		if importPath == "" {
			return nil, fmt.Errorf("unable to determine import path for shard dir %s", shardDir)
		}
		imports = append(imports, importPath)
	}

	sort.Strings(imports)
	dedup := make([]string, 0, len(imports))
	for _, imp := range imports {
		if len(dedup) == 0 || dedup[len(dedup)-1] != imp {
			dedup = append(dedup, imp)
		}
	}

	return dedup, nil
}

func buildFieldLookupMap(data *Data) map[string]*Field {
	fieldByLookupKey := make(map[string]*Field)
	for _, object := range data.Objects {
		for _, field := range object.Fields {
			fieldByLookupKey[object.Name+"."+field.Name] = field
		}
	}

	return fieldByLookupKey
}

func buildArgsFuncLookupMap(data *Data) map[string]*Field {
	fieldByArgsFunc := make(map[string]*Field)
	for _, object := range data.Objects {
		for _, field := range object.Fields {
			if argsFunc := field.ArgsFunc(); argsFunc != "" {
				fieldByArgsFunc[argsFunc] = field
			}
		}
	}

	return fieldByArgsFunc
}

func buildInputLookupMap(data *Data) map[string]*Object {
	inputByName := make(map[string]*Object)
	for _, input := range data.Inputs {
		inputByName[input.Name] = input
	}

	return inputByName
}

func buildCodecLookupMap(data *Data) map[string]*config.TypeReference {
	codecByFunc := make(map[string]*config.TypeReference)
	for _, ref := range data.ReferencedTypes {
		addCodecLookup(codecByFunc, ref)
	}

	// Also include types from object fields and their args.
	for _, object := range data.Objects {
		for _, field := range object.Fields {
			addCodecLookup(codecByFunc, field.TypeReference)
			for _, arg := range field.Args {
				addCodecLookup(codecByFunc, arg.TypeReference)
			}
		}
	}

	// Also include types from input fields.
	for _, input := range data.Inputs {
		for _, field := range input.Fields {
			addCodecLookup(codecByFunc, field.TypeReference)
		}
	}

	return codecByFunc
}

func addCodecLookup(codecByFunc map[string]*config.TypeReference, ref *config.TypeReference) {
	if ref == nil {
		return
	}

	if marshal := ref.MarshalFunc(); marshal != "" {
		if _, exists := codecByFunc[marshal]; !exists {
			codecByFunc[marshal] = ref
		}
	}
	if unmarshal := ref.UnmarshalFunc(); unmarshal != "" {
		if _, exists := codecByFunc[unmarshal]; !exists {
			codecByFunc[unmarshal] = ref
		}
	}

	// Also include element types for slices/pointers, since parent codecs
	// dispatch to element codecs via MarshalCodec/UnmarshalCodec.
	if elem := ref.Elem(); elem != nil {
		addCodecLookup(codecByFunc, elem)
	}
}

func generateSplitShardImports(data *Data, shardImports []string) error {
	if len(shardImports) == 0 {
		return nil
	}

	for i, shardImport := range shardImports {
		path := filepath.Join(
			data.Config.Exec.Dir(),
			fmt.Sprintf("split_shard_import_%d.generated.go", i),
		)
		if err := templates.Render(templates.Options{
			PackageName:     data.Config.Exec.Package,
			Template:        splitImportsTemplate,
			Filename:        path,
			Data:            splitImportsTemplateData{Import: shardImport},
			RegionTags:      false,
			GeneratedHeader: true,
			Packages:        data.Config.Packages,
			TemplateFS:      codegenTemplates,
		}); err != nil {
			return err
		}
	}
	return nil
}

func splitScope(data *Data) string {
	if path := data.Config.Exec.ImportPath(); path != "" {
		return path
	}

	fallback := data.Config.Exec.Package + ":" + filepath.Base(data.Config.Exec.Filename)
	if data.Config.Exec.Filename == "" {
		return fallback
	}

	return fallback + ":" + splitShortHash(data.Config.Exec.Filename)
}

var splitNameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_]+`)

func splitShardName(filename string, build *Data, used map[string]string) string {
	raw := splitRawShardName(filename, build)
	candidate := splitSanitizeName(raw)

	if prev, exists := used[candidate]; exists && prev != filename {
		candidate = candidate + "_" + splitShortHash(filename)
	}
	used[candidate] = filename
	return candidate
}

func splitRawShardName(filename string, build *Data) string {
	if len(build.Config.Sources) > 0 && build.Config.Sources[0] != nil {
		src := build.Config.Sources[0]
		name := filepath.Base(src.Name)
		ext := filepath.Ext(name)
		return strings.TrimSuffix(name, ext)
	}
	base := filepath.Base(filename)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	base = strings.TrimSuffix(base, ".generated")
	if base == "" {
		base = "common"
	}
	return base
}

func splitSanitizeName(name string) string {
	name = splitNameSanitizer.ReplaceAllString(name, "_")
	name = strings.Trim(name, "_")
	name = strings.ToLower(name)
	if name == "" {
		name = "shard"
	}
	if name[0] >= '0' && name[0] <= '9' {
		name = "s_" + name
	}
	if token.Lookup(name).IsKeyword() {
		name = "s_" + name
	}
	return name
}

func splitShortHash(s string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	sum := fmt.Sprintf("%x", h.Sum32())
	if len(sum) >= 6 {
		return sum[:6]
	}
	return strings.Repeat("0", 6-len(sum)) + sum
}
