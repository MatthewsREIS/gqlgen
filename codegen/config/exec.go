package config

import (
	"errors"
	"fmt"
	"go/types"
	"path/filepath"
	"strings"

	"github.com/99designs/gqlgen/internal/code"
)

type ExecConfig struct {
	Package string     `yaml:"package,omitempty"`
	Layout  ExecLayout `yaml:"layout,omitempty"` // Default: single-file

	// Only for single-file layout:
	Filename string `yaml:"filename,omitempty"`

	// Only for follow-schema layout:
	FilenameTemplate string `yaml:"filename_template,omitempty"` // String template with {name} as placeholder for base name.
	DirName          string `yaml:"dir"`

	// Only for split-packages layout:
	ShardDir              string `yaml:"shard_dir,omitempty"`
	ShardFilenameTemplate string `yaml:"shard_filename_template,omitempty"` // String template with {name} as placeholder for base name.

	// EmbedSchemaDir, relative to the exec directory, receives a copy of every
	// schema file that sits outside that directory so the generated code can
	// go:embed it instead of inlining the SDL as a string literal.
	EmbedSchemaDir string `yaml:"embed_schema_dir,omitempty"`

	// Maximum number of goroutines in concurrency to use when running multiple child resolvers
	// Suppressing the number of goroutines generated can reduce memory consumption per request,
	// but processing time may increase due to the reduced number of concurrences
	// Default: 0 (unlimited)
	WorkerLimit uint `yaml:"worker_limit"`

	// UseGenericDispatcher swaps the per-type _<Type>(...) dispatcher emitted
	// by codegen/object.gotpl from a giant per-type switch into a per-type
	// FieldHandler table consumed by graphql.DispatchObject. The mechanism is
	// the same compile-time win as use_function_syntax_for_execution_context:
	// collapse many AST-distinct per-type bodies into one shared generic body
	// so the compiler holds the dispatcher's type-checked AST once instead of
	// hundreds of near-identical copies.
	//
	// Only affects single-file (and follow-schema) layouts; split-packages
	// already delegates dispatch through ec.ResolveField and is unaffected.
	// Default: false (emit the historical switch).
	UseGenericDispatcher bool `yaml:"use_generic_dispatcher,omitempty"`
}

type ExecLayout string

var (
	// Write all generated code to a single file.
	ExecLayoutSingleFile ExecLayout = "single-file"
	// Write generated code to a directory, generating one Go source file for each GraphQL schema
	// file.
	ExecLayoutFollowSchema ExecLayout = "follow-schema"
	// Write generated code to multiple internal shard packages and a small public gateway package.
	ExecLayoutSplitPackages ExecLayout = "split-packages"
)

func (r *ExecConfig) Check() error {
	if r.Layout == "" {
		r.Layout = ExecLayoutSingleFile
	}

	switch r.Layout {
	case ExecLayoutSingleFile:
		if r.Filename == "" {
			return errors.New("filename must be specified when using single-file layout")
		}
		if !strings.HasSuffix(r.Filename, ".go") {
			return errors.New(
				"filename should be path to a go source file when using single-file layout",
			)
		}
		r.Filename = abs(r.Filename)
	case ExecLayoutFollowSchema:
		if r.DirName == "" {
			return errors.New("dir must be specified when using follow-schema layout")
		}
		r.DirName = abs(r.DirName)
	case ExecLayoutSplitPackages:
		if r.Filename == "" {
			return errors.New("filename must be specified when using split-packages layout")
		}
		if !strings.HasSuffix(r.Filename, ".go") {
			return errors.New(
				"filename should be path to a go source file when using split-packages layout",
			)
		}
		r.Filename = abs(r.Filename)

		if r.ShardDir == "" {
			r.ShardDir = "internal/gqlgenexec/shards"
		}
		if !filepath.IsAbs(r.ShardDir) {
			r.ShardDir = filepath.Join(filepath.Dir(r.Filename), r.ShardDir)
		}
		r.ShardDir = abs(r.ShardDir)

		if r.ShardFilenameTemplate == "" {
			r.ShardFilenameTemplate = "{name}.generated.go"
		}
	default:
		return fmt.Errorf("invalid layout %s", r.Layout)
	}

	if r.EmbedSchemaDir != "" {
		cleaned := filepath.ToSlash(filepath.Clean(r.EmbedSchemaDir))
		if filepath.IsAbs(r.EmbedSchemaDir) || strings.HasPrefix(cleaned, "/") ||
			cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
			return fmt.Errorf(
				"embed_schema_dir must be a relative path inside the exec directory, got %q",
				r.EmbedSchemaDir,
			)
		}
		r.EmbedSchemaDir = cleaned
	}

	if strings.ContainsAny(r.Package, "./\\") {
		return errors.New(
			"package should be the output package name only, do not include the output filename",
		)
	}

	if r.Package == "" && r.Dir() != "" {
		r.Package = code.NameForDir(r.Dir())
	}

	return nil
}

func (r *ExecConfig) ImportPath() string {
	if r.Dir() == "" {
		return ""
	}
	return code.ImportPathForDir(r.Dir())
}

func (r *ExecConfig) Dir() string {
	switch r.Layout {
	case ExecLayoutSingleFile:
		if r.Filename == "" {
			return ""
		}
		return filepath.Dir(r.Filename)
	case ExecLayoutFollowSchema:
		return abs(r.DirName)
	case ExecLayoutSplitPackages:
		if r.Filename == "" {
			return ""
		}
		return filepath.Dir(r.Filename)
	default:
		panic("invalid layout " + r.Layout)
	}
}

func (r *ExecConfig) Pkg() *types.Package {
	if r.Dir() == "" {
		return nil
	}
	return types.NewPackage(r.ImportPath(), r.Package)
}

func (r *ExecConfig) IsDefined() bool {
	return r.Filename != "" || r.DirName != ""
}
