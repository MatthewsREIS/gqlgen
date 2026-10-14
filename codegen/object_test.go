package codegen

import (
	"go/token"
	"go/types"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/99designs/gqlgen/codegen/config"
	"github.com/99designs/gqlgen/codegen/templates"
)

func TestObjectInvalidsIncrement(t *testing.T) {
	// non-concurrent: all fields are non-resolver, non-method-with-context
	sequential := &Object{Definition: &ast.Definition{Name: "Query"}}
	sequential.Fields = []*Field{
		{FieldDefinition: &ast.FieldDefinition{Name: "foo"}, Object: sequential},
	}
	assert.Equal(t, "out.Invalids++", sequential.InvalidsIncrement("out"))
	assert.Equal(t, "fs.Invalids++", sequential.InvalidsIncrement("fs"))

	// concurrent: at least one resolver field
	obj := &Object{Definition: &ast.Definition{Name: "User"}}
	obj.Fields = []*Field{
		{
			FieldDefinition: &ast.FieldDefinition{Name: "name"},
			IsResolver:      true,
			Object:          obj,
		},
	}
	assert.Equal(t, "atomic.AddUint32(&out.Invalids, 1)", obj.InvalidsIncrement("out"))
	assert.Equal(t, "atomic.AddUint32(&fs.Invalids, 1)", obj.InvalidsIncrement("fs"))
}

func TestObjectInvalidsIncrement_DisableConcurrency(t *testing.T) {
	// DisableConcurrency=true makes IsConcurrent() false even with resolver fields
	obj := &Object{
		Definition:         &ast.Definition{Name: "Mutation"},
		DisableConcurrency: true,
	}
	obj.Fields = []*Field{
		{
			FieldDefinition: &ast.FieldDefinition{Name: "createUser"},
			IsResolver:      true,
			Object:          obj,
		},
	}
	assert.Equal(t, "out.Invalids++", obj.InvalidsIncrement("out"))
}

func mkInputObject(t *testing.T, methods ...string) *Object {
	t.Helper()
	pkg := types.NewPackage("example.com/x", "x")
	named := types.NewNamed(
		types.NewTypeName(token.NoPos, pkg, "In", nil),
		types.NewStruct(nil, nil),
		nil,
	)
	for _, method := range methods {
		sig := types.NewSignatureType(
			types.NewVar(token.NoPos, pkg, "i", types.NewPointer(named)),
			nil,
			nil,
			nil,
			nil,
			false,
		)
		named.AddMethod(types.NewFunc(token.NoPos, pkg, method, sig))
	}
	return &Object{Definition: &ast.Definition{Name: "In", Kind: ast.InputObject}, Type: named}
}

func TestObjectHasUnmarshal(t *testing.T) {
	assert.False(t, mkInputObject(t).HasUnmarshal())
	assert.True(t, mkInputObject(t, "UnmarshalGQL").HasUnmarshal())
	// UnmarshalGQLContext alone does not suppress generation; it gets a hybrid body.
	assert.False(t, mkInputObject(t, "UnmarshalGQLContext").HasUnmarshal())
	assert.False(t, mkInputObject(t, "MarshalGQL").HasUnmarshal())
	// The full context-marshaler pair binds as a graphql.ContextMarshaler, so the
	// codec calls UnmarshalGQLContext directly: no generated unmarshaler at all,
	// hybrid or otherwise. Generating one would make it dead code and silently drop
	// every field directive, resolver, default and nested enforcement.
	assert.True(t, mkInputObject(t, "MarshalGQLContext", "UnmarshalGQLContext").HasUnmarshal())
}

func TestObjectHasContextUnmarshal(t *testing.T) {
	assert.False(t, mkInputObject(t).HasContextUnmarshal())
	assert.True(t, mkInputObject(t, "UnmarshalGQLContext").HasContextUnmarshal())
	assert.False(t, mkInputObject(t, "UnmarshalGQL").HasContextUnmarshal())
	// UnmarshalGQL wins: upstream semantics, no generated function at all.
	assert.False(t, mkInputObject(t, "UnmarshalGQL", "UnmarshalGQLContext").HasContextUnmarshal())
	// So does the full context-marshaler pair, for the same reason.
	assert.False(
		t,
		mkInputObject(t, "MarshalGQLContext", "UnmarshalGQLContext").HasContextUnmarshal(),
	)
	// Only the *context* pair binds as a ContextMarshaler, so a non-context
	// MarshalGQL alongside it still gets the hybrid body.
	assert.True(t, mkInputObject(t, "MarshalGQL", "UnmarshalGQLContext").HasContextUnmarshal())
}

// mkInput builds an input Object with the given fields, wiring Field.Object.
func mkInput(name string, fields ...*Field) *Object {
	obj := &Object{Definition: &ast.Definition{Name: name, Kind: ast.InputObject}}
	obj.Fields = fields
	for _, f := range fields {
		f.Object = obj
	}
	return obj
}

// mkField builds a plain (scalar) input field.
func mkField(name string) *Field {
	return &Field{
		FieldDefinition: &ast.FieldDefinition{Name: name},
		GoFieldName:     templates.ToGo(name),
		TypeReference: &config.TypeReference{
			Definition: &ast.Definition{Name: "String", Kind: ast.Scalar},
		},
	}
}

// mkInputField builds an input field whose (unwrapped) type is the named input
// object. List and non-null wrapping is irrelevant: TypeReference.Definition is
// always the base named type.
func mkInputField(name, inputTypeName string) *Field {
	f := mkField(name)
	f.TypeReference.Definition = &ast.Definition{Name: inputTypeName, Kind: ast.InputObject}
	return f
}

func withDirective(f *Field) *Field {
	f.Directives = []*Directive{{
		Name: "gate",
		DirectiveDefinition: &ast.DirectiveDefinition{
			Name:      "gate",
			Locations: []ast.DirectiveLocation{ast.LocationInputFieldDefinition},
		},
	}}
	return f
}

func specialNames(o *Object) []string {
	var names []string
	for _, f := range o.HybridSpecialFields() {
		names = append(names, f.Name)
	}
	return names
}

// A field is special — it keeps its generated arm instead of being handed to
// UnmarshalGQLContext — when it has a directive, has a resolver, or its type is
// an input object that itself requires generated handling, transitively.
func TestHybridSpecialFieldsIsTransitive(t *testing.T) {
	resolverField := mkField("computed")
	resolverField.IsResolver = true

	gated := mkInput("Gated", withDirective(mkField("secret")))
	inert := mkInput("Inert", mkField("nothing"))

	// (e) self-referential cycle: A.and: [A!] and A has a directive field.
	selfRef := mkInput(
		"SelfRef",
		withDirective(mkField("gatedScalar")),
		mkInputField("and", "SelfRef"),
	)

	// (f) two-node cycle A <-> B where only B has a directive field.
	cycleA := mkInput("CycleA", mkField("plain"), mkInputField("b", "CycleB"))
	cycleB := mkInput("CycleB", withDirective(mkField("secret")), mkInputField("a", "CycleA"))

	// (e') a cycle with nothing special anywhere must terminate and stay plain.
	loopA := mkInput("LoopA", mkField("plain"), mkInputField("b", "LoopB"))
	loopB := mkInput("LoopB", mkField("plain"), mkInputField("a", "LoopA"))

	outer := mkInput("Outer",
		withDirective(mkField("directed")), // (a)
		resolverField,                      // (b)
		mkInputField("nested", "Gated"),    // (c)
		mkInputField("inert", "Inert"),     // (d)
		mkField("plain"),
		mkInputField("viaCycle", "CycleA"), // transitive through a cycle
		mkInputField("viaLoop", "LoopA"),   // cycle with nothing special
	)

	inputs := Objects{outer, gated, inert, selfRef, cycleA, cycleB, loopA, loopB}
	inputs.resolveHybridSpecialFields()

	assert.Equal(t, []string{"directed", "computed", "nested", "viaCycle"}, specialNames(outer))
	assert.Equal(t, []string{"secret"}, specialNames(gated))
	assert.Nil(t, specialNames(inert))
	assert.Equal(t, []string{"gatedScalar", "and"}, specialNames(selfRef))
	assert.Equal(t, []string{"b"}, specialNames(cycleA))
	assert.Equal(t, []string{"secret", "a"}, specialNames(cycleB))
	assert.Nil(t, specialNames(loopA))
	assert.Nil(t, specialNames(loopB))
}

// An input the decoder could not reproduce is not only one with directives or
// resolvers: a field default and an INPUT_OBJECT-level directive are dropped
// just as silently, so they too make the containing input require generated
// handling when it is reached from a parent.
func TestHybridSpecialFieldsCoversDefaultsAndInputObjectDirectives(t *testing.T) {
	defaulted := mkField("withDefault")
	defaulted.Default = "x"
	hasDefault := mkInput("HasDefault", defaulted)

	oneOf := mkInput("OneOf", mkField("a"), mkField("b"))
	oneOf.Directives = []*Directive{{
		Name: "oneOf",
		DirectiveDefinition: &ast.DirectiveDefinition{
			Name:      "oneOf",
			Locations: []ast.DirectiveLocation{ast.LocationInputObject},
		},
	}}

	outer := mkInput("Outer",
		mkInputField("defaulted", "HasDefault"),
		mkInputField("oneOf", "OneOf"),
	)

	inputs := Objects{outer, hasDefault, oneOf}
	inputs.resolveHybridSpecialFields()

	assert.Equal(t, []string{"defaulted", "oneOf"}, specialNames(outer))
	// A default on the input's own field is still plain: the hybrid body injects
	// defaults into the map it hands to the method.
	assert.Nil(t, specialNames(hasDefault))
}

// Unclassified is not a safe default for a hybrid input: it is the pre-fix field
// set, which skips nested directives and resolvers. Fail loudly instead.
func TestHybridSpecialFieldsPanicsWhenUnclassified(t *testing.T) {
	hybrid := mkInputObject(t, "UnmarshalGQLContext")
	hybrid.Fields = []*Field{mkField("plain")}
	hybrid.Fields[0].Object = hybrid
	assert.PanicsWithError(t,
		"input In: HybridSpecialFields called before Objects.resolveHybridSpecialFields; "+
			"the nested-input classification would fail open and skip nested directives and resolvers",
		func() { hybrid.HybridSpecialFields() })

	// Classified: no panic.
	Objects{hybrid}.resolveHybridSpecialFields()
	assert.Nil(t, specialNames(hybrid))

	// A non-hybrid input never consults the classification, so it is unaffected.
	assert.NotPanics(t, func() { mkInput("Plain", mkField("a")).HybridSpecialFields() })
}

// mkHybridInput builds an input Object whose Go type has UnmarshalGQLContext.
func mkHybridInput(t *testing.T, name string, fields ...*Field) *Object {
	t.Helper()
	obj := mkInputObject(t, "UnmarshalGQLContext")
	obj.Name = name
	obj.Fields = fields
	for _, f := range fields {
		f.Object = obj
	}
	return obj
}

func fieldNames(fields []*Field) []string {
	var names []string
	for _, f := range fields {
		names = append(names, f.Name)
	}
	return names
}

// Split-packages layout: a resolver-backed field is the runtime's, never an
// arm. A nested hybrid input is a runtime descent when something below it needs
// a resolver and it has no arm; one that keeps an arm (it reaches a plain input
// with enforcement) runs its own resolvers inside that arm and is not descended.
func TestHybridResolverFieldsFollowTheArmlessPath(t *testing.T) {
	resolverField := func(name string) *Field {
		f := mkField(name)
		f.IsResolver = true
		return f
	}
	leaf := mkHybridInput(
		t,
		"Leaf",
		mkField("plain"),
		resolverField("ownedBy"),
		mkInputField("not", "Leaf"),
	)
	parent := mkHybridInput(t, "Parent", mkField("plain"), mkInputField("and", "Leaf"))
	quiet := mkHybridInput(t, "Quiet", mkField("plain"), mkInputField("and", "Quiet"))
	gated := mkInput("Gated", withDirective(mkField("secret")))
	teams := mkInputField("teams", "Gated")
	teams.IsResolver = true
	walker := mkHybridInput(t, "Walker",
		resolverField("ownedBy"),
		mkInputField("nested", "Gated"),
		mkInputField("self", "Walker"),
		teams,
	)
	carrier := mkHybridInput(
		t,
		"Carrier",
		mkInputField("w", "Walker"),
		mkInputField("leaf", "Leaf"),
	)

	Objects{leaf, parent, quiet, gated, walker, carrier}.resolveHybridSpecialFields()

	assert.Equal(t, []string{"ownedBy", "not"}, fieldNames(leaf.HybridResolverFields()))
	assert.Nil(t, leaf.HybridGeneratedFields())
	assert.Equal(
		t,
		[]string{"and"},
		fieldNames(parent.HybridResolverFields()),
		"transitive through and",
	)
	assert.Nil(t, parent.HybridGeneratedFields())
	assert.Nil(
		t,
		quiet.HybridResolverFields(),
		"a cycle with no resolver below terminates and needs nothing",
	)
	assert.Nil(t, quiet.HybridGeneratedFields())
	assert.Equal(t, []string{"nested", "self", "teams"}, fieldNames(walker.HybridGeneratedFields()),
		"a plain input with enforcement, and the walked self reference, keep their arms")
	assert.Equal(t, []string{"ownedBy"}, fieldNames(walker.HybridResolverFields()),
		"fields with an arm are not planned")
	assert.Equal(t, []string{"w"}, fieldNames(carrier.HybridGeneratedFields()))
	assert.Equal(t, []string{"leaf"}, fieldNames(carrier.HybridResolverFields()))
}

func TestHybridResolverFieldsPanicsWhenUnclassified(t *testing.T) {
	hybrid := mkInputObject(t, "UnmarshalGQLContext")
	assert.PanicsWithError(t,
		"input In: HybridResolverFields called before Objects.resolveHybridSpecialFields; "+
			"the nested-input classification would fail open and skip nested resolvers",
		func() { hybrid.HybridResolverFields() })
	Objects{hybrid}.resolveHybridSpecialFields()
	assert.Nil(t, hybrid.HybridResolverFields())
}
