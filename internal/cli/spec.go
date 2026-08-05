// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	ucli "github.com/urfave/cli/v3"
)

// Spec prints what a spider is handed.
//
// On stdout and nothing else, so `scour spec job.hcl > spec.hcl` writes a spec
// rather than a spec with a progress line in the middle of it.
func Spec(a *App) *ucli.Command {
	var jobName string

	return &ucli.Command{
		Name:      "spec",
		Usage:     "Print the extraction spec a spider is handed",
		ArgsUsage: "<document.hcl>",
		Description: "A spider is given the shapes to extract and nothing else: not where\n" +
			"bodies are cached, not the budget, not the exporters. This prints\n" +
			"that, as the HCL a person would have written, which is what a spider\n" +
			"in another language receives.\n\n" +
			"The fingerprint identifies the shape. It changes when the shape does\n" +
			"and not when the document is merely reordered or reformatted.",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "job", Usage: "which job, if the document holds several", Destination: &jobName},
		},
		Action: oneFile(func(_ context.Context, path string) error {
			doc, err := Accept(path)
			if err != nil {
				return err
			}
			job, err := OneJob(doc, jobName)
			if err != nil {
				return err
			}

			spec := job.Spec()
			a.Warnf("fingerprint %s\n", spec.Fingerprint())
			a.Printf("%s", spec.HCL())
			return nil
		}),
	}
}
