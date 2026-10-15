package shardruntime

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"sync"

	"github.com/99designs/gqlgen/graphql"
)

// InputResolverField is one entry of a hybrid input's resolver plan, in schema
// order. Input == "" marks a resolver-backed field whose raw value UnmarshalCodec
// decodes for the resolver; otherwise GoField and Input name the nested hybrid
// input to descend into.
type InputResolverField struct {
	Name           string
	UnmarshalCodec string
	GoField        string
	Input          string
}

// inputResolverPlan is a registered plan with its resolver-backed field names,
// which ApplyInputDirectives consults to leave those fields to their codec.
type inputResolverPlan struct {
	fields        []InputResolverField
	resolverOwned map[string]bool
}

var (
	inputResolverPlansMu sync.RWMutex
	inputResolverPlans   = map[string]inputResolverPlan{}
)

// RegisterInputResolverPlan declares the fields of hybrid input inputName that
// ApplyInputFieldResolvers handles once UnmarshalGQLContext has decoded the
// input.
func RegisterInputResolverPlan(scope, inputName string, fields []InputResolverField) {
	inputResolverPlansMu.Lock()
	defer inputResolverPlansMu.Unlock()
	key := objectKey(scope, inputName)
	if _, exists := inputResolverPlans[key]; exists {
		panic("duplicate input resolver plan registration: " + key)
	}
	plan := inputResolverPlan{fields: fields, resolverOwned: map[string]bool{}}
	for _, f := range fields {
		if f.Input == "" {
			plan.resolverOwned[f.Name] = true
		}
	}
	inputResolverPlans[key] = plan
}

// StripInputResolverFields returns raw without the resolver-backed fields of
// hybrid input inputName, at this level and inside the nested hybrid inputs its
// plan names. UnmarshalGQLContext never decodes a resolver's field; a Go type
// that carries one as a struct member of a type it cannot coerce would fail on
// it. raw itself is left intact for ApplyInputFieldResolvers.
func StripInputResolverFields(scope, inputName string, raw map[string]any) map[string]any {
	plan, ok := lookupInputResolverPlan(scope, inputName)
	if !ok {
		return raw
	}
	out := maps.Clone(raw)
	for _, f := range plan.fields {
		v, present := out[f.Name]
		if !present {
			continue
		}
		if f.Input == "" {
			delete(out, f.Name)
			continue
		}
		out[f.Name] = stripNestedResolverFields(scope, f.Input, v)
	}
	return out
}

func stripNestedResolverFields(scope, input string, v any) any {
	switch x := v.(type) {
	case map[string]any:
		return StripInputResolverFields(scope, input, x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = stripNestedResolverFields(scope, input, e)
		}
		return out
	}
	return v
}

func lookupInputResolverPlan(scope, inputName string) (inputResolverPlan, bool) {
	inputResolverPlansMu.RLock()
	defer inputResolverPlansMu.RUnlock()
	plan, ok := inputResolverPlans[objectKey(scope, inputName)]
	return plan, ok
}

func isResolverOwnedField(scope, inputName, field string) bool {
	plan, ok := lookupInputResolverPlan(scope, inputName)
	return ok && plan.resolverOwned[field]
}

func resetInputResolverPlansForTest() {
	inputResolverPlansMu.Lock()
	defer inputResolverPlansMu.Unlock()
	inputResolverPlans = map[string]inputResolverPlan{}
}

// ApplyInputFieldResolvers runs the plan of inputName on typed, the pointer to
// the struct UnmarshalGQLContext decoded from raw, in schema order, depth first.
// An absent field is skipped; an explicit null reaches the resolver as nil data.
// The descent follows the decoded struct in lockstep with raw, so a nested
// resolver mutates the object its parent holds and an error names the full path;
// it relies on the decoder keeping list order and length, as gqlwhere does.
func ApplyInputFieldResolvers(
	ctx context.Context,
	ec ObjectExecutionContext,
	scope, inputName string,
	typed any,
	raw map[string]any,
) error {
	plan, ok := lookupInputResolverPlan(scope, inputName)
	if !ok {
		return fmt.Errorf("input %s has no resolver plan registered in scope %s", inputName, scope)
	}
	for _, f := range plan.fields {
		v, present := raw[f.Name]
		if !present {
			continue
		}
		fctx := graphql.WithPathContext(ctx, graphql.NewPathWithField(f.Name))
		if f.Input == "" {
			data, err := ec.UnmarshalCodec(fctx, f.UnmarshalCodec, v)
			if err != nil {
				return err
			}
			if _, err := ec.InvokeResolver(
				fctx,
				inputName,
				f.Name,
				[]any{typed, data},
			); err != nil {
				return graphql.ErrorOnPath(fctx, err)
			}
			continue
		}
		if v == nil {
			continue
		}
		fv, err := decodedField(typed, f.GoField)
		if err != nil {
			return graphql.ErrorOnPath(
				fctx,
				fmt.Errorf("input %s, field %s: %w", inputName, f.Name, err),
			)
		}
		if err := applyNestedResolvers(fctx, ec, scope, f.Input, fv, v); err != nil {
			return err
		}
	}
	return nil
}

func decodedField(typed any, goField string) (reflect.Value, error) {
	sv := reflect.ValueOf(typed)
	if sv.Kind() != reflect.Pointer || sv.IsNil() || sv.Elem().Kind() != reflect.Struct {
		return reflect.Value{}, fmt.Errorf("decoded value is %T, not a pointer to a struct", typed)
	}
	fv := sv.Elem().FieldByName(goField)
	if !fv.IsValid() {
		return reflect.Value{}, fmt.Errorf("%T has no field %s", typed, goField)
	}
	return fv, nil
}

// The list case indexes the path so an error carries the path the element codec
// gave it.
func applyNestedResolvers(
	ctx context.Context,
	ec ObjectExecutionContext,
	scope, input string,
	fv reflect.Value,
	v any,
) error {
	for fv.Kind() == reflect.Pointer {
		if fv.IsNil() {
			return graphql.ErrorOnPath(
				ctx,
				fmt.Errorf("input %s: decoded to nil, so its field resolvers cannot run", input),
			)
		}
		if fv.Type().Elem().Kind() == reflect.Struct {
			break
		}
		fv = fv.Elem()
	}
	if fv.Kind() == reflect.Slice {
		list := graphql.CoerceList(v)
		if fv.Len() != len(list) {
			return graphql.ErrorOnPath(
				ctx,
				fmt.Errorf("input %s: %d values decoded from %d", input, fv.Len(), len(list)),
			)
		}
		for i, e := range list {
			if e == nil {
				continue
			}
			ictx := graphql.WithPathContext(ctx, graphql.NewPathWithIndex(i))
			if err := applyNestedResolvers(ictx, ec, scope, input, fv.Index(i), e); err != nil {
				return err
			}
		}
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return graphql.ErrorOnPath(
			ctx,
			fmt.Errorf("input %s: expected map[string]any, got %T", input, v),
		)
	}
	switch fv.Kind() {
	case reflect.Pointer:
		return ApplyInputFieldResolvers(ctx, ec, scope, input, fv.Interface(), m)
	case reflect.Struct:
		return ApplyInputFieldResolvers(ctx, ec, scope, input, fv.Addr().Interface(), m)
	}
	return graphql.ErrorOnPath(
		ctx,
		fmt.Errorf("input %s: cannot descend into a %s field", input, fv.Kind()),
	)
}
