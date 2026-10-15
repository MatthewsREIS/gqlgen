package codegen

import (
	"fmt"
	"go/types"
	"strconv"
	"strings"
	"unicode"

	"github.com/vektah/gqlparser/v2/ast"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/99designs/gqlgen/codegen/config"
)

type GoFieldType int

const (
	GoFieldUndefined GoFieldType = iota
	GoFieldMethod
	GoFieldVariable
	GoFieldMap
)

type Object struct {
	*ast.Definition

	Type                     types.Type
	ResolverInterface        types.Type
	Root                     bool
	Fields                   []*Field
	Implements               []*ast.Definition
	DisableConcurrency       bool
	Stream                   bool
	Directives               []*Directive
	PointersInUnmarshalInput bool

	// needsGeneratedInput is the input-graph classification shared by all inputs,
	// computed once by Objects.resolveHybridSpecialFields, which also sets
	// hybridClassified. Unclassified is not a safe default for a hybrid input —
	// it is the pre-fix, bypass-prone field set — so HybridSpecialFields panics
	// on it rather than failing open.
	needsGeneratedInput map[string]bool
	// needsGeneratedWalked is the same classification for split-packages
	// layout, where shardruntime.ApplyInputDirectives applies a hybrid input's
	// field directives and defaults at runtime: only nested inputs the walk
	// cannot reach keep a generated arm.
	needsGeneratedWalked map[string]bool
	// needsRuntimeResolvers marks the hybrid inputs of split-packages layout with
	// a resolver-backed field at or below them that no generated arm runs, so
	// shardruntime.ApplyInputFieldResolvers has work there.
	needsRuntimeResolvers map[string]bool
	hybridClassified      bool
}

func (b *builder) buildObject(typ *ast.Definition) (*Object, error) {
	dirs, err := b.getDirectives(typ.Directives)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", typ.Name, err)
	}
	caser := cases.Title(language.English, cases.NoLower)
	obj := &Object{
		Definition:               typ,
		Root:                     b.Config.IsRoot(typ),
		DisableConcurrency:       typ == b.Schema.Mutation,
		Stream:                   typ == b.Schema.Subscription,
		Directives:               dirs,
		PointersInUnmarshalInput: b.Config.ReturnPointersInUnmarshalInput,
		ResolverInterface: types.NewNamed(
			types.NewTypeName(0, b.Config.Exec.Pkg(), caser.String(typ.Name)+"Resolver", nil),
			nil,
			nil,
		),
	}

	if !obj.Root {
		goObject, err := b.Binder.DefaultUserObject(typ.Name)
		if err != nil {
			return nil, err
		}
		obj.Type = goObject
	}

	for _, intf := range b.Schema.GetImplements(typ) {
		obj.Implements = append(obj.Implements, b.Schema.Types[intf.Name])
	}

	for _, field := range typ.Fields {
		if strings.HasPrefix(field.Name, "__") {
			continue
		}

		var f *Field
		f, err = b.buildField(obj, field)
		if err != nil {
			return nil, err
		}

		obj.Fields = append(obj.Fields, f)
	}

	return obj, nil
}

func (o *Object) Reference() types.Type {
	if config.IsNilable(o.Type) {
		return o.Type
	}
	return types.NewPointer(o.Type)
}

type Objects []*Object

func (o *Object) Implementors() string {
	satisfiedBy := strconv.Quote(o.Name)
	var satisfiedBySb100 strings.Builder
	for _, s := range o.Implements {
		satisfiedBySb100.WriteString(", " + strconv.Quote(s.Name))
	}
	satisfiedBy += satisfiedBySb100.String()
	return "[]string{" + satisfiedBy + "}"
}

func (o *Object) HasResolvers() bool {
	for _, f := range o.Fields {
		if f.IsResolver || f.IsBatch() {
			return true
		}
	}
	return false
}

// HasUnmarshal reports whether the bound Go type decodes itself, so gqlgen must
// emit no unmarshaler for it at all.
func (o *Object) HasUnmarshal() bool {
	// A type carrying the full MarshalGQLContext/UnmarshalGQLContext pair binds as
	// a graphql.ContextMarshaler (config.Binder.TypeReference), so its codec calls
	// UnmarshalGQLContext directly and never reaches a generated unmarshaler. Say
	// so here too, or a hybrid body would be generated and silently never called,
	// dropping every field directive, resolver, default and nested enforcement.
	if o.hasMethod("MarshalGQLContext") && o.hasMethod("UnmarshalGQLContext") {
		return true
	}
	return o.hasMethod("UnmarshalGQL")
}

// HasContextUnmarshal reports whether the bound Go type decodes itself via
// UnmarshalGQLContext but not UnmarshalGQL. Such inputs still get a generated
// unmarshaler, with a hybrid body: the Go method decodes the plain fields while
// generated code keeps applying defaults and running field directives and field
// resolvers. A type with UnmarshalGQL, or with the full
// MarshalGQLContext/UnmarshalGQLContext pair, keeps upstream semantics instead
// (no generated function at all — see HasUnmarshal).
func (o *Object) HasContextUnmarshal() bool {
	return !o.HasUnmarshal() && o.hasMethod("UnmarshalGQLContext")
}

// HybridSpecialFields returns the fields a hybrid unmarshaler must still handle
// itself, because UnmarshalGQLContext cannot: those with INPUT_FIELD_DEFINITION
// directives, those backed by a field resolver, and those whose type is an input
// object that itself needs generated handling — handing such a value to the
// method would skip the nested input's directives and resolvers silently.
//
// The nested case needs Objects.resolveHybridSpecialFields to have run over the
// whole input graph; BuildData does that.
func (o *Object) HybridSpecialFields() []*Field {
	if !o.hybridClassified && o.HasContextUnmarshal() {
		panic(fmt.Errorf(
			"input %s: HybridSpecialFields called before Objects.resolveHybridSpecialFields; "+
				"the nested-input classification would fail open and skip nested directives and resolvers",
			o.Name))
	}
	var special []*Field
	for _, f := range o.Fields {
		if hybridSpecialField(f, o.needsGeneratedInput) {
			special = append(special, f)
		}
	}
	return special
}

// HybridGeneratedFields returns the fields a hybrid unmarshaler in
// split-packages layout still decodes with a generated arm: nested inputs the
// runtime cannot reach — an input without a runtime decoder that carries
// enforcement anywhere below it, a hybrid input that itself needs an arm (an
// INPUT_OBJECT directive, transitively), or a nested input bound to
// graphql.Omittable, whose value the runtime walk cannot address. See
// HybridResolverFields for the rest.
func (o *Object) HybridGeneratedFields() []*Field {
	if !o.hybridClassified && o.HasContextUnmarshal() {
		panic(fmt.Errorf(
			"input %s: HybridGeneratedFields called before Objects.resolveHybridSpecialFields; "+
				"the nested-input classification would fail open and skip nested directives and resolvers",
			o.Name))
	}
	var generated []*Field
	for _, f := range o.Fields {
		if hybridGeneratedField(f, o.needsGeneratedWalked) {
			generated = append(generated, f)
		}
	}
	return generated
}

func hybridGeneratedField(f *Field, needsGenerated map[string]bool) bool {
	def, ok := nestedInput(f)
	return ok && (needsGenerated[def.Name] || f.TypeReference.IsOmittable)
}

// HybridResolverFields returns, in schema order, the fields of a hybrid input in
// split-packages layout that shardruntime.ApplyInputFieldResolvers handles once
// UnmarshalGQLContext has decoded the input: the resolver-backed fields, and the
// nested hybrid inputs with a resolver-backed field at or below them that have
// no generated arm of their own to run it.
func (o *Object) HybridResolverFields() []*Field {
	if !o.hybridClassified && o.HasContextUnmarshal() {
		panic(fmt.Errorf(
			"input %s: HybridResolverFields called before Objects.resolveHybridSpecialFields; "+
				"the nested-input classification would fail open and skip nested resolvers",
			o.Name))
	}
	var fields []*Field
	for _, f := range o.Fields {
		if hybridResolverField(f, o.needsRuntimeResolvers, o.needsGeneratedWalked) {
			fields = append(fields, f)
		}
	}
	return fields
}

// A field that keeps a generated arm runs its own resolver inside that arm.
func hybridResolverField(f *Field, resolvers, walked map[string]bool) bool {
	if hybridGeneratedField(f, walked) {
		return false
	}
	if f.IsResolver {
		return true
	}
	def, ok := nestedInput(f)
	return ok && resolvers[def.Name] && !boundTypeDecodesItself(f)
}

// The field codec hands such a value to the bound type's own UnmarshalGQL or
// external unmarshal function, never to the nested input's generated decoder,
// so the decoded value is not the nested input's model and cannot be descended.
func boundTypeDecodesItself(f *Field) bool {
	return f.TypeReference.IsMarshaler || f.TypeReference.Unmarshaler != nil
}

// nestedInput is the input object behind a field's type, past any list and
// non-null wrapping, so [X!]! and X classify alike.
func nestedInput(f *Field) (*ast.Definition, bool) {
	if f.TypeReference == nil || f.TypeReference.Definition == nil {
		return nil, false
	}
	def := f.TypeReference.Definition
	return def, def.Kind == ast.InputObject
}

func hybridSpecialField(f *Field, needsGenerated map[string]bool) bool {
	if len(f.ImplDirectives()) > 0 || f.IsResolver {
		return true
	}
	def, ok := nestedInput(f)
	return ok && needsGenerated[def.Name]
}

// resolveHybridSpecialFields classifies the input graph for the hybrid
// unmarshaler: an input "needs generated handling" when it has a field a custom
// UnmarshalGQLContext cannot reproduce — one with a directive, a resolver or a
// default, an INPUT_OBJECT-level directive on the input itself, or a field whose
// type is an input that needs generated handling. That last clause makes the
// relation transitive, and the input graph is cyclic (`and: [XWhereInput!]`), so
// it is computed as a fixpoint rather than by recursion.
//
// The result is shared with every input so HybridSpecialFields, which the
// templates call per input, can see past its own fields.
func (os Objects) resolveHybridSpecialFields() {
	// ponytail: plain repeated passes, no worklist — O(passes x inputs x fields),
	// two or three passes on real schemas. Add a reverse-edge worklist only if a
	// large schema shows this in a codegen profile.
	needsGenerated := make(map[string]bool, len(os))
	for changed := true; changed; {
		changed = false
		for _, in := range os {
			if needsGenerated[in.Name] || !in.requiresGeneratedUnmarshal(needsGenerated) {
				continue
			}
			needsGenerated[in.Name] = true
			changed = true
		}
	}
	// Split-packages: directives, defaults and resolvers run at runtime, so only
	// INPUT_OBJECT directives and nested inputs without a runtime decoder force
	// an arm; a non-hybrid input keeps the hybrid-agnostic result because the
	// walk cannot pass through it.
	walked := make(map[string]bool, len(os))
	for _, in := range os {
		if !in.HasContextUnmarshal() {
			walked[in.Name] = needsGenerated[in.Name]
		}
	}
	for changed := true; changed; {
		changed = false
		for _, in := range os {
			if walked[in.Name] || !in.HasContextUnmarshal() || !in.requiresGeneratedWalked(walked) {
				continue
			}
			walked[in.Name] = true
			changed = true
		}
	}
	resolvers := make(map[string]bool, len(os))
	for changed := true; changed; {
		changed = false
		for _, in := range os {
			if resolvers[in.Name] || !in.HasContextUnmarshal() ||
				!in.requiresRuntimeResolvers(resolvers, walked) {
				continue
			}
			resolvers[in.Name] = true
			changed = true
		}
	}
	for _, in := range os {
		in.needsGeneratedInput = needsGenerated
		in.needsGeneratedWalked = walked
		in.needsRuntimeResolvers = resolvers
		in.hybridClassified = true
	}
}

func (o *Object) requiresRuntimeResolvers(resolvers, walked map[string]bool) bool {
	for _, f := range o.Fields {
		if hybridResolverField(f, resolvers, walked) {
			return true
		}
	}
	return false
}

func (o *Object) requiresGeneratedWalked(walked map[string]bool) bool {
	if len(o.InputObjectDirectives()) > 0 {
		return true
	}
	for _, f := range o.Fields {
		if hybridGeneratedField(f, walked) {
			return true
		}
	}
	return false
}

func (o *Object) requiresGeneratedUnmarshal(needsGenerated map[string]bool) bool {
	if len(o.InputObjectDirectives()) > 0 {
		return true
	}
	for _, f := range o.Fields {
		// A default is applied to the map before the method is called, so it does
		// not make the field special here — but a parent handing this whole input
		// to its own decoder would drop it.
		if f.Default != nil || hybridSpecialField(f, needsGenerated) {
			return true
		}
	}
	return false
}

func (o *Object) hasMethod(name string) bool {
	if o.IsMap() {
		return false
	}
	named, ok := o.Type.(*types.Named)
	if !ok {
		return false
	}
	for method := range named.Methods() {
		if method.Name() == name {
			return true
		}
	}
	return false
}

func (o *Object) HasDirectives() bool {
	if len(o.Directives) > 0 {
		return true
	}
	for _, f := range o.Fields {
		if f.HasDirectives() {
			return true
		}
	}

	return false
}

// InputObjectDirectives returns directives that should be executed at the INPUT_OBJECT level.
// This is used for input types to execute @directives placed on the input object itself,
// after all fields have been unmarshaled.
// See: https://github.com/99designs/gqlgen/issues/2281
func (o *Object) InputObjectDirectives() []*Directive {
	if o.Kind != ast.InputObject {
		return nil
	}
	var d []*Directive
	for _, dir := range o.Directives {
		if !dir.SkipRuntime && dir.IsLocation(ast.LocationInputObject) {
			d = append(d, dir)
		}
	}
	return d
}

func (o *Object) IsConcurrent() bool {
	for _, f := range o.Fields {
		if f.IsConcurrent() {
			return true
		}
	}
	return false
}

// InvalidsIncrement returns the Go statement that increments the invalids
// counter for this object's field set. Concurrent objects require atomic
// access; sequential objects use a plain increment.
func (o *Object) InvalidsIncrement(fieldSetVar string) string {
	if o.IsConcurrent() {
		return fmt.Sprintf("atomic.AddUint32(&%s.Invalids, 1)", fieldSetVar)
	}
	return fieldSetVar + ".Invalids++"
}

func (o *Object) IsReserved() bool {
	return strings.HasPrefix(o.Name, "__")
}

func (o *Object) IsMap() bool {
	return o.Type == config.MapType
}

func (o *Object) Description() string {
	return o.Definition.Description
}

func (o *Object) HasField(name string) bool {
	for _, f := range o.Fields {
		if f.Name == name {
			return true
		}
	}

	return false
}

func (os Objects) ByName(name string) *Object {
	for i, o := range os {
		if strings.EqualFold(o.Name, name) {
			return os[i]
		}
	}
	return nil
}

func ucFirst(s string) string {
	if s == "" {
		return ""
	}

	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}
