// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rangertaha/scour/internal/defaults"
	"github.com/rangertaha/scour/internal/store"
	"github.com/rangertaha/scour/internal/wom"
)

// fields returns the properties a template describes.
//
// A template's outermost prop names the record and its children are the
// fields, which is the shape wom wants. A flat template is taken as the fields
// themselves, so a simple one does not have to be nested to be valid.
func fields(schema wom.Schema) []wom.Prop {
	if len(schema) == 1 && len(schema[0].Props) > 0 {
		return schema[0].Props
	}
	return schema
}

// applyTemplate copies a shipped schema onto an entity.
//
// It is the same work `scour add -p ... -e ...` does by hand, once per
// property. The point is not the keystrokes but the aliases, descriptions and
// examples: those are what the matcher scores a page's labels against, and
// they are exactly what a user typing from memory leaves out.
func applyTemplate(ctx context.Context, s *store.Store, entityID uint, name string) ([]string, error) {
	schema, err := defaults.Schema(name)
	if err != nil {
		return nil, err
	}

	var changes []string
	for _, p := range fields(schema) {
		example := ""
		if len(p.Examples) > 0 {
			example = p.Examples[0]
		}

		if err := s.AddPropertyDetail(ctx, entityID, store.PropertyDetail{
			Name: p.Name, Type: string(p.Type), Example: example, Description: p.Description}); err != nil {
			return nil, err
		}
		for _, alias := range p.Aliases {
			if err := s.AddPropertyAlias(ctx, entityID, "", p.Name, alias); err != nil {
				return nil, err
			}
		}

		switch n := len(p.Aliases); n {
		case 0:
			changes = append(changes, "property "+p.Name)
		case 1:
			changes = append(changes, "property "+p.Name+" (1 alias)")
		default:
			changes = append(changes, fmt.Sprintf("property %s (%d aliases)", p.Name, n))
		}
	}
	return changes, nil
}

// newTemplatesCmd lists what ships in the binary.
func newTemplatesCmd(_ *app) *cobra.Command {
	return &cobra.Command{
		Use:   "templates",
		Short: "List the built-in schemas scour ships with",
		Long: "Templates are starting points, not answers. Each carries the properties,\n" +
			"aliases, descriptions and example values a kind of record usually has,\n" +
			"which is what bootstraps labelling before anything has been crawled.",
		Example: "  scour templates\n  scour add cars --template vehicle",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			names, err := defaults.Names()
			if err != nil {
				return err
			}

			t := newTable(
				[]string{"TEMPLATE", "PROPS", "FIELDS"},
				alignLeft, alignRight, alignLeft,
			)
			for _, name := range names {
				schema, err := defaults.Schema(name)
				if err != nil {
					return err
				}
				props := fields(schema)

				shown := make([]string, 0, len(props))
				for _, p := range props {
					shown = append(shown, p.Name)
				}
				t.add(name, fmt.Sprintf("%d", len(props)), truncate(strings.Join(shown, ", "), 60))
			}
			return t.render(cmd.OutOrStdout())
		},
	}
}
