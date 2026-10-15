package model

import (
	"context"
	"fmt"
	"slices"
)

// HybridInput is the Go binding for the HybridInput input object. It decodes
// itself with UnmarshalGQLContext, so gqlgen emits a hybrid unmarshaler: this
// method sees the plain and defaulted fields, the runtime applies the @toUpper
// directive and the `resolved` resolver, and only the nested inputs below keep
// a generated arm.
type HybridInput struct {
	Plain       string
	WithDefault string
	Directed    string
	Resolved    string

	// Nested inputs. This decoder can populate them by reflection, but it cannot
	// run their field directives or field resolvers, so the generated unmarshaler
	// must keep these fields for itself.
	Nested         *HybridNested
	NestedList     []*HybridNested
	SelfRef        []*HybridInput
	ResolvedNested []*HybridNested

	// SawKeys records, sorted, the keys the hybrid body handed to
	// UnmarshalGQLContext. Not a schema field.
	SawKeys []string
}

// HybridNested has no custom unmarshaler: it gets a fully generated one, which
// runs its @toUpper directive and its `resolved` resolver.
type HybridNested struct {
	Gated    string
	Resolved string
	Deeper   *HybridNested
}

func (i *HybridInput) UnmarshalGQLContext(ctx context.Context, v any) error {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("HybridInput: expected map[string]any, got %T", v)
	}
	for k := range m {
		i.SawKeys = append(i.SawKeys, k)
	}
	slices.Sort(i.SawKeys)
	i.Plain, _ = m["plain"].(string)
	i.WithDefault, _ = m["withDefault"].(string)
	i.Directed, _ = m["directed"].(string)
	i.Resolved, _ = m["resolved"].(string)
	// The reflection decode a custom decoder can do, and the reason the
	// classification must be transitive: nothing here runs a directive.
	i.Nested = decodeHybridNested(m["nested"])
	if raw, ok := m["nestedList"].([]any); ok {
		for _, e := range raw {
			i.NestedList = append(i.NestedList, decodeHybridNested(e))
		}
	}
	if raw, ok := m["selfRef"].([]any); ok {
		for _, e := range raw {
			var child HybridInput
			if err := child.UnmarshalGQLContext(ctx, e); err != nil {
				return err
			}
			i.SelfRef = append(i.SelfRef, &child)
		}
	}
	return nil
}

func decodeHybridNested(v any) *HybridNested {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	n := &HybridNested{Deeper: decodeHybridNested(m["deeper"])}
	n.Gated, _ = m["gated"].(string)
	n.Resolved, _ = m["resolved"].(string)
	return n
}
