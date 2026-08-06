// SPDX-License-Identifier: GPL-3.0-or-later

package engine_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// A setting the document accepts that nothing acts on.
//
// # Why this test exists
//
// Because the class came back. It was retired once, after `external_timeout`
// turned out to be parsed, defaulted, validated, carried into the resolved job,
// documented in CLI.md, reported by `scour show` as "yes, waiting 5m0s", and
// read by no running crawl. Then the service document's `url` arrived with the
// same shape: a service told to answer on nats://10.0.0.5:4222 started an
// embedded broker on an ephemeral port, said it was ready, and answered nobody.
// Then `monitoring.level`, which `scour show` reported while every run logged at
// warn.
//
// Three instances is not a coincidence, and none of them failed anything. That
// is what makes the class expensive: a setting that does nothing is worse than
// one that does not exist, because the operator has been told otherwise and
// will debug the thing it claims to control.
//
// # What it checks
//
// Every `hcl:"..."` field in this package has to be reachable from outside it:
// either the field itself is read elsewhere, or a method of the same type that
// mentions the field is called elsewhere. Reading it here does not count, and
// nor does validating it — validation is what all three instances had.
//
// # Why an allowlist rather than a cleverer check
//
// Because the honest alternatives are worse. Following a field through
// engine-internal machinery to whatever it eventually affects is dataflow
// analysis, and a check nobody can predict the output of is a check people
// disable. Four fields are genuinely reachable in a way this cannot see, they
// are listed below with the reason, and anything new has to be argued for in
// the same place. That is the point: the exemption is visible.
func TestNoSettingIsAcceptedAndIgnored(t *testing.T) {
	const dir = "."

	// exempt is what this check cannot see, and why. Adding to it is a claim
	// somebody can check.
	exempt := map[string]string{
		// Rendered into the spec by writeItem/writeProperty, which is what a
		// spider in another language reads.
		"Item.Description":     "written into the rendered spec",
		"Property.Description": "written into the rendered spec",

		// Read by Plugin.On and by the plugin fingerprint, both in this
		// package, and both of whose results are consumed outside it.
		"Plugin.Enabled": "read by Plugin.On and the chain fingerprint",

		// Resolved into the pipeline's waves by Job.Waves, in this package.
		// The waves are what the pipeline runs.
		"Step.Requires": "resolved into the pipeline's waves",

		// Nothing publishes measurements yet. This is a real instance of the
		// class, kept deliberately: the block is documented and the feature is
		// not built, and the honest options are to build it or to delete the
		// field. Recorded rather than hidden.
		"Monitoring.Metrics": "no metrics are published yet: tracked, not resolved",
	}

	fields, methods := declarations(t, dir)
	used := selectorsOutside(t, dir)

	var inert []string
	for key, field := range fields {
		if slices.Contains(used, field.name) {
			continue
		}
		reachable := false
		for _, m := range methods {
			if m.owner == field.owner && m.mentions[field.name] && slices.Contains(used, m.name) {
				reachable = true
				break
			}
		}
		if reachable {
			continue
		}
		if _, ok := exempt[key]; ok {
			continue
		}
		inert = append(inert, key+" (hcl "+field.hcl+")")
	}
	sort.Strings(inert)

	if len(inert) > 0 {
		t.Errorf(`these settings are accepted by a document and read by nothing outside this package:

  %s

A setting that does nothing is worse than one that does not exist, because
whoever wrote it has been told otherwise. Either make it act, delete it, or add
it to the exempt list above with a reason somebody can check.`,
			strings.Join(inert, "\n  "))
	}
}

type decl struct {
	owner, name, hcl string
	mentions         map[string]bool
}

var (
	typeLine  = regexp.MustCompile(`^type (\w+) struct`)
	fieldLine = regexp.MustCompile("^\\s*(\\w+)\\s+[\\w\\[\\]\\*\\.]+\\s+`hcl:\"([^\",]+)")
)

// declarations are the hcl-tagged fields and the methods that mention them.
func declarations(t *testing.T, dir string) (map[string]decl, []decl) {
	t.Helper()

	fields := map[string]decl{}
	var methods []decl

	for _, path := range goFiles(t, dir, false) {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		owner := ""
		for _, line := range strings.Split(string(body), "\n") {
			if m := typeLine.FindStringSubmatch(line); m != nil {
				owner = m[1]
				continue
			}
			if m := fieldLine.FindStringSubmatch(line); m != nil && owner != "" {
				fields[owner+"."+m[1]] = decl{owner: owner, name: m[1], hcl: m[2]}
			}
		}

		parsed, err := parser.ParseFile(token.NewFileSet(), path, body, 0)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for _, d := range parsed.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 || fn.Body == nil {
				continue
			}
			methods = append(methods, decl{
				owner:    receiverType(fn),
				name:     fn.Name.Name,
				mentions: selectorsIn(fn.Body),
			})
		}
	}
	return fields, methods
}

func receiverType(fn *ast.FuncDecl) string {
	switch t := fn.Recv.List[0].Type.(type) {
	case *ast.StarExpr:
		if named, ok := t.X.(*ast.Ident); ok {
			return named.Name
		}
	case *ast.Ident:
		return t.Name
	}
	return ""
}

// selectorsIn is every `x.Name` a function body mentions.
func selectorsIn(body *ast.BlockStmt) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			out[sel.Sel.Name] = true
		}
		return true
	})
	return out
}

// selectorsOutside is every `x.Name` mentioned outside this package, in
// production code. Tests do not count: a field only a test reads is a field
// nothing acts on.
func selectorsOutside(t *testing.T, dir string) []string {
	t.Helper()

	root, err := filepath.Abs(filepath.Join(dir, "..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	for _, where := range []string{"internal", "cmd"} {
		for _, path := range goFiles(t, filepath.Join(root, where), true) {
			if strings.Contains(path, filepath.Join("internal", "engine")) {
				continue
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			ast.Inspect(parsed, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok {
					seen[sel.Sel.Name] = true
				}
				return true
			})
		}
	}

	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func goFiles(t *testing.T, dir string, recurse bool) []string {
	t.Helper()

	var out []string
	walk := func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if !recurse && path != dir {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			out = append(out, path)
		}
		return nil
	}
	if err := filepath.WalkDir(dir, walk); err != nil {
		t.Fatal(err)
	}
	return out
}
