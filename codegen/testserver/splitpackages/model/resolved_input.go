package model

import (
	"context"
	"errors"
	"fmt"
)

// ResolvedWhereInput decodes itself the way an entgql input does: it reads the
// keys it owns, ignores the rest, and fails on the resolver-backed `ownedBy`,
// as a decoder that carries that field as a struct member of a type it cannot
// coerce would. The runtime hands `ownedBy` to the field resolver instead.
type ResolvedWhereInput struct {
	Name         *string
	Not          *ResolvedWhereInput
	And          []*ResolvedWhereInput
	HasChildWith []*ResolvedChildWhereInput

	// Predicates is what the ownedBy resolver appends to. Not a schema field.
	Predicates []string
}

type ResolvedChildWhereInput struct {
	Label *string
	Not   *ResolvedChildWhereInput

	Predicates []string
}

type ResolvedOwnerInput struct {
	Name string
}

func (i *ResolvedWhereInput) UnmarshalGQLContext(ctx context.Context, v any) error {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("ResolvedWhereInput: expected map[string]any, got %T", v)
	}
	for _, k := range []string{"ownedBy", "owner"} {
		if _, ok := m[k]; ok {
			return fmt.Errorf(
				"ResolvedWhereInput: %s is resolver-backed and must not reach the decoder",
				k,
			)
		}
	}
	i.Name = optionalString(m["name"])
	if raw, ok := m["not"].(map[string]any); ok {
		i.Not = &ResolvedWhereInput{}
		if err := i.Not.UnmarshalGQLContext(ctx, raw); err != nil {
			return err
		}
	}
	for _, e := range listOf(m["and"]) {
		child := &ResolvedWhereInput{}
		if err := child.UnmarshalGQLContext(ctx, e); err != nil {
			return err
		}
		i.And = append(i.And, child)
	}
	for _, e := range listOf(m["hasChildWith"]) {
		child := &ResolvedChildWhereInput{}
		if err := child.UnmarshalGQLContext(ctx, e); err != nil {
			return err
		}
		i.HasChildWith = append(i.HasChildWith, child)
	}
	return nil
}

func (c *ResolvedChildWhereInput) UnmarshalGQLContext(ctx context.Context, v any) error {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("ResolvedChildWhereInput: expected map[string]any, got %T", v)
	}
	if _, ok := m["ownedBy"]; ok {
		return errors.New(
			"ResolvedChildWhereInput: ownedBy is resolver-backed and must not reach the decoder",
		)
	}
	c.Label = optionalString(m["label"])
	if raw, ok := m["not"].(map[string]any); ok {
		c.Not = &ResolvedChildWhereInput{}
		return c.Not.UnmarshalGQLContext(ctx, raw)
	}
	return nil
}

func (o *ResolvedOwnerInput) UnmarshalGQLContext(_ context.Context, v any) error {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("ResolvedOwnerInput: expected map[string]any, got %T", v)
	}
	o.Name, _ = m["name"].(string)
	return nil
}

func optionalString(v any) *string {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	return &s
}

func listOf(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case map[string]any:
		return []any{x}
	}
	return nil
}
