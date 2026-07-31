// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"io"

	"github.com/urfave/cli/v3"
)

// helpOrder is the order the command groups are printed in.
//
// It is the order someone meets them: give scour a list of urls, say what to
// pull out of them, teach it where that lives, run the search, then put the
// whole thing on more than one machine. urfave sorts categories alphabetically,
// which would open the help on SEARCH and close it on URLS, so the order is
// stated rather than spelled into the names.
var helpOrder = []string{"URLS", "ITEMS", "TRAIN", "SEARCH", "SERVER"}

// orderedCategories returns the command groups in helpOrder, with anything not
// named there after them and the ungrouped commands last.
//
// Last rather than first because version and help are what you need least: the
// default template puts them above everything scour actually does.
func orderedCategories(cmd *cli.Command) []cli.CommandCategory {
	found := cmd.VisibleCategories()
	byName := make(map[string]cli.CommandCategory, len(found))
	for _, c := range found {
		byName[c.Name()] = c
	}

	out := make([]cli.CommandCategory, 0, len(found))
	taken := make(map[string]bool, len(found))
	for _, name := range helpOrder {
		if c, ok := byName[name]; ok {
			out = append(out, c)
			taken[name] = true
		}
	}
	// A group added later and not listed above still appears, in urfave's
	// order, rather than vanishing from the help.
	for _, c := range found {
		if c.Name() != "" && !taken[c.Name()] {
			out = append(out, c)
		}
	}
	for _, c := range found {
		if c.Name() == "" {
			out = append(out, c)
		}
	}
	return out
}

// installHelpOrder replaces the command listing with one that respects
// helpOrder. Everything else about the help is urfave's own.
func installHelpOrder() {
	cli.RootCommandHelpTemplate = `NAME:
   {{template "helpNameTemplate" .}}

USAGE:
   {{if .UsageText}}{{wrap .UsageText 3}}{{else}}{{.FullName}} {{if .VisibleFlags}}[global options]{{end}}{{if .VisibleCommands}} [command [command options]]{{end}}{{end}}{{if .Version}}{{if not .HideVersion}}

VERSION:
   {{.Version}}{{end}}{{end}}{{if .Description}}

DESCRIPTION:
   {{template "descriptionTemplate" .}}{{end}}{{if .VisibleCommands}}

COMMANDS:{{range ordered .}}{{if .Name}}

   {{.Name}}:{{range .VisibleCommands}}
     {{join .Names ", "}}{{"\t"}}{{.Usage}}{{end}}{{else}}
{{range .VisibleCommands}}
   {{join .Names ", "}}{{"\t"}}{{.Usage}}{{end}}{{end}}{{end}}{{end}}{{if .VisibleFlagCategories}}

GLOBAL OPTIONS:{{template "visibleFlagCategoryTemplate" .}}{{else if .VisibleFlags}}

GLOBAL OPTIONS:{{template "visibleFlagTemplate" .}}{{end}}
`

	// urfave treats UsageText as a replacement for the usage line, so a command
	// carrying examples stopped saying what its arguments were. Here the
	// signature is always shown and the examples get a section of their own,
	// which is what both are for.
	cli.CommandHelpTemplate = `NAME:
   {{template "helpNameTemplate" .}}

USAGE:
   {{.FullName}}{{if .VisibleFlags}} [options]{{end}}{{if .ArgsUsage}} {{.ArgsUsage}}{{end}}{{if .Description}}

DESCRIPTION:
   {{template "descriptionTemplate" .}}{{end}}{{if .UsageText}}

EXAMPLES:
{{.UsageText}}{{end}}{{if .VisibleCommands}}

COMMANDS:{{template "visibleCommandTemplate" .}}{{end}}{{if .VisibleFlagCategories}}

OPTIONS:{{template "visibleFlagCategoryTemplate" .}}{{else if .VisibleFlags}}

OPTIONS:{{template "visibleFlagTemplate" .}}{{end}}
`
	cli.SubcommandHelpTemplate = cli.CommandHelpTemplate

	cli.HelpPrinter = func(w io.Writer, templ string, data any) {
		cli.HelpPrinterCustom(w, templ, data, map[string]any{
			"ordered": orderedCategories,
		})
	}
}
