// SPDX-License-Identifier: GPL-3.0-or-later

package plugin

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
)

// decode is gohcl, kept behind a name so the one import of it is in one place.
func decode(body hcl.Body, eval *hcl.EvalContext, into any) hcl.Diagnostics {
	return gohcl.DecodeBody(body, eval, into)
}
