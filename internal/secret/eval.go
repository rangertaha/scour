// SPDX-License-Identifier: GPL-3.0-or-later

package secret

import (
	"context"
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// Resolver is what a `secret()` call is answered by. An interface so a test can
// answer without a cluster, and so that a node with no secrets at all can
// answer usefully rather than not answering.
type Resolver interface {
	Resolve(ctx context.Context, name string) ([]byte, error)
}

// Eval returns the evaluation context a plugin's configuration is decoded
// against.
//
// This is the whole of what makes a secret a secret. The function exists only
// here, so a document evaluated anywhere else, which is everywhere else, keeps
// `secret("name")` as an unevaluated call: the copy in KV, the diff, `scour
// show`, a plan. It is resolved on the node that builds the plugin and nowhere
// before.
func Eval(ctx context.Context, from Resolver) *hcl.EvalContext {
	return &hcl.EvalContext{
		Functions: map[string]function.Function{
			"secret": function.New(&function.Spec{
				Params: []function.Parameter{{Name: "name", Type: cty.String}},
				Type:   function.StaticReturnType(cty.String),
				Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
					name := args[0].AsString()
					if from == nil {
						return cty.NilVal, fmt.Errorf(
							"this node has no secrets, and %q was asked for", name)
					}

					value, err := from.Resolve(ctx, name)
					if err != nil {
						return cty.NilVal, err
					}
					return cty.StringVal(string(value)), nil
				},
			}),
		},
	}
}

// Missing is the evaluation context for a node that has no secret store.
//
// It refuses rather than returning an empty string, because an empty
// credential is a plugin that will fail somewhere later with a message about
// authentication rather than about configuration.
func Missing(ctx context.Context) *hcl.EvalContext { return Eval(ctx, nil) }
