package model

import (
	"context"
	"fmt"
	"slices"
)

// WalkedInput decodes itself and has no field resolver anywhere below it, so
// the generated hybrid unmarshaler delegates every field to this method and
// applies field directives and defaults from the schema at runtime.
type WalkedInput struct {
	Plain       string
	Directed    string
	WithDefault string
	Child       *WalkedChild
	Children    []*WalkedChild
	SelfRef     []*WalkedInput

	// SawKeys records, sorted, the keys handed to UnmarshalGQLContext. Not a
	// schema field.
	SawKeys []string
}

type WalkedChild struct {
	Gated        string
	ChildDefault string
	Deeper       *WalkedChild
}

func (i *WalkedInput) UnmarshalGQLContext(ctx context.Context, v any) error {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("WalkedInput: expected map[string]any, got %T", v)
	}
	for k := range m {
		i.SawKeys = append(i.SawKeys, k)
	}
	slices.Sort(i.SawKeys)
	i.Plain, _ = m["plain"].(string)
	i.Directed, _ = m["directed"].(string)
	i.WithDefault, _ = m["withDefault"].(string)
	if raw, ok := m["child"]; ok && raw != nil {
		i.Child = &WalkedChild{}
		if err := i.Child.UnmarshalGQLContext(ctx, raw); err != nil {
			return err
		}
	}
	if raw, ok := m["children"].([]any); ok {
		for _, e := range raw {
			c := &WalkedChild{}
			if err := c.UnmarshalGQLContext(ctx, e); err != nil {
				return err
			}
			i.Children = append(i.Children, c)
		}
	}
	if raw, ok := m["selfRef"].([]any); ok {
		for _, e := range raw {
			var child WalkedInput
			if err := child.UnmarshalGQLContext(ctx, e); err != nil {
				return err
			}
			i.SelfRef = append(i.SelfRef, &child)
		}
	}
	return nil
}

func (c *WalkedChild) UnmarshalGQLContext(ctx context.Context, v any) error {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("WalkedChild: expected map[string]any, got %T", v)
	}
	c.Gated, _ = m["gated"].(string)
	c.ChildDefault, _ = m["childDefault"].(string)
	if raw, ok := m["deeper"]; ok && raw != nil {
		c.Deeper = &WalkedChild{}
		return c.Deeper.UnmarshalGQLContext(ctx, raw)
	}
	return nil
}
