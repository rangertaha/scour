// SPDX-License-Identifier: GPL-3.0-or-later

package engine_test

import (
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

const (
	// engineImport is the package under examination.
	engineImport = "github.com/rangertaha/scour/internal/engine"

	// reportPath prints a job's settings back to whoever wrote them, so it
	// reads nearly all of them and cannot vouch for any. See reachedFromOutside.
	reportPath = "internal/cli/show.go"

	// checkPrefix names the validators, which read a setting to decide whether
	// it is allowed and then do nothing with it. See reachedFromOutside.
	checkPrefix = "validate"
)

// A setting the document accepts that nothing acts on.
//
// # Why this test exists
//
// Because the class came back. It was retired once, after `external_timeout`
// turned out to be parsed, defaulted, validated, carried into the resolved job,
// documented in the command-line reference, reported by `scour show` as "yes,
// waiting 5m0s", and
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
// Two more turned up the moment this test was made to hold, which is the
// argument for having made it hold: `external = true` in a `pipeline` block,
// which ran the pipeline here in silence, and `timeout` in every service block,
// which every handler ignored in favour of the bus package's own constant.
//
// # What it checks
//
// Every `hcl:"..."` field in this package has to be read by something outside
// it. Not read directly, necessarily: the module is type-checked, every use of
// an engine identifier is resolved to the object it names, and reachability is
// followed from the outside in. A field read by a method of the engine's own
// counts exactly when something outside can reach that method, however many
// hops away it is. Reading it in this package and going no further does not
// count, and nor does validating it: validation is what all three instances
// had.
//
// A write does not count either. `p.Field = v` and `Pipeline{Field: v}` set
// something nothing goes on to look at, which is the shape of the class.
//
// # Why this is type-checked rather than grepped
//
// It matched bare selector names once, and that did not hold: any identifier of
// the same name anywhere in the tree vouched for a field. 77 of the 94 tagged
// fields passed on that alone, and the passes were nonsense —
// `Monitoring.Level` was vouched for by parquet-go's `.Level(0, 0, i)`, and
// every service block's `Dir` by `filepath.Dir`. Two of the three instances
// this was written for would have slipped through, and `monitoring.level`
// demonstrably did: reverting its wiring left the test passing.
//
// Type-checking the module costs a few seconds, which is why this is the
// slowest test in the package. A check that passes when the thing it checks for
// is present is not a check, so the seconds are the price of the test meaning
// anything.
//
// # Why an allowlist rather than a cleverer check
//
// Following a field through engine-internal machinery to whatever it eventually
// affects is dataflow analysis, and a check nobody can predict the output of is
// a check people disable. Four fields are genuinely reachable in a way this
// cannot see, they are listed below with the reason, and anything new has to be
// argued for in the same place. That is the point: the exemption is visible.
func TestNoSettingIsAcceptedAndIgnored(t *testing.T) {
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

		// The next eight are one cause, not eight, and the same kind of
		// deliberate record as the line above.
		//
		// Nothing outside a test drives a crawl over the bus:
		// bus.Conn.NewDownloader and bus.Conn.NewSpider have no caller but
		// internal/bus's own tests, and both are always passed a zero wait. So
		// the length of time a document says an external stage may take is
		// never the length of time anything waits. That is the oldest instance
		// of this class and the one the test was written for, which is worth
		// saying plainly: it was retired, and it came back, because the fix
		// wired the value into `scour show` rather than into a crawl.
		//
		// `external` itself does act, so it is not on this list: a run that
		// cannot reach the stage refuses rather than crawling locally.
		"Downloader.ExternalTimeout": "no command runs a crawl over the bus yet: tracked, not resolved",
		"Spider.ExternalTimeout":     "no command runs a crawl over the bus yet: tracked, not resolved",
		"Pipeline.ExternalTimeout":   "an external pipeline is refused outright: tracked, not resolved",

		// Applying a change to a running job is unwired the same way. Diff and
		// the Effect it reports have no caller but this package's own tests, so
		// the policy for what to do about a change that is not free is never
		// consulted. Five fields, one cause.
		"Job.Mutation":           "nothing applies a change to a running job yet: tracked, not resolved",
		"Mutation.Costly":        "nothing applies a change to a running job yet: tracked, not resolved",
		"Mutation.OutOfScope":    "nothing applies a change to a running job yet: tracked, not resolved",
		"Mutation.StaleRecords":  "nothing applies a change to a running job yet: tracked, not resolved",
		"Mutation.OrphanedCache": "nothing applies a change to a running job yet: tracked, not resolved",
	}

	loaded := load(t)
	reached := reachedFromOutside(t, loaded)

	var inert []string
	for field, tag := range taggedFields(t, loaded) {
		if reached[field] {
			continue
		}
		key := owner(field) + "." + field.Name()
		if _, ok := exempt[key]; ok {
			continue
		}
		inert = append(inert, key+" (hcl "+tag+")")
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

// load type-checks the whole module, without its tests: a field only a test
// reads is a field nothing acts on.
func load(t *testing.T) []*packages.Package {
	t.Helper()

	loaded, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedSyntax | packages.NeedTypes |
			packages.NeedTypesInfo | packages.NeedDeps | packages.NeedImports,
		Tests: false,
	}, engineImport+"/...", "github.com/rangertaha/scour/...")
	if err != nil {
		t.Fatalf("loading the module: %v", err)
	}
	for _, pkg := range loaded {
		if len(pkg.Errors) > 0 {
			t.Fatalf("%s: %v", pkg.PkgPath, pkg.Errors[0])
		}
	}
	if len(loaded) < 2 {
		t.Fatalf("loaded %d packages, so this test asserts nothing", len(loaded))
	}
	return loaded
}

// taggedFields is every hcl-tagged field the engine declares, and its tag name.
func taggedFields(t *testing.T, loaded []*packages.Package) map[*types.Var]string {
	t.Helper()

	out := map[*types.Var]string{}
	for _, pkg := range loaded {
		if pkg.PkgPath != engineImport {
			continue
		}
		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			declared, ok := scope.Lookup(name).(*types.TypeName)
			if !ok {
				continue
			}
			structure, ok := declared.Type().Underlying().(*types.Struct)
			if !ok {
				continue
			}
			for i := range structure.NumFields() {
				tag, ok := reflect.StructTag(structure.Tag(i)).Lookup("hcl")
				if !ok {
					continue
				}
				out[structure.Field(i)] = strings.Split(tag, ",")[0]
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("no hcl-tagged fields were found, so this test asserts nothing")
	}
	return out
}

// owner is the type a field belongs to, for the report.
func owner(field *types.Var) string {
	for _, pkg := range []*types.Scope{field.Pkg().Scope()} {
		for _, name := range pkg.Names() {
			declared, ok := pkg.Lookup(name).(*types.TypeName)
			if !ok {
				continue
			}
			structure, ok := declared.Type().Underlying().(*types.Struct)
			if !ok {
				continue
			}
			for i := range structure.NumFields() {
				if structure.Field(i) == field {
					return name
				}
			}
		}
	}
	return "?"
}

// reachedFromOutside is every engine object something outside the engine can
// get to.
//
// Uses outside the engine are the roots. Inside it, each function is a node and
// the engine objects its body reads are its edges, so a field read by a method
// counts once anything outside can reach that method, at any depth. Uses at
// package level are roots too, because initialisation always runs.
//
// With two exclusions, and they are what make this test work at all. Both are
// paths that read a setting without acting on it, and every instance of the
// class so far had both:
//
//   - `scour show` prints the document back, so it reads nearly every setting,
//     and through Job.Resolved it reaches nearly every one it does not read
//     directly. All three instances were reported correctly by `scour show`
//     and acted on by nothing, which is what made them so hard to see.
//   - The validators check a value is allowed and then drop it. `external`,
//     `url` and `level` were all validated, and a job that failed validation
//     was refused for the right reason while a job that passed it was run as
//     though the setting were not there.
//
// A setting whose only readers are the thing that describes settings and the
// thing that checks them has not been wired up.
func reachedFromOutside(t *testing.T, loaded []*packages.Package) map[types.Object]bool {
	t.Helper()

	edges := map[types.Object][]types.Object{}
	var roots []types.Object

	for _, pkg := range loaded {
		inside := pkg.PkgPath == engineImport
		for _, file := range pkg.Syntax {
			if strings.HasSuffix(filepath.ToSlash(pkg.Fset.Position(file.Pos()).Filename), reportPath) {
				continue
			}
			if !inside {
				roots = append(roots, engineReads(pkg.TypesInfo, file)...)
				continue
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					roots = append(roots, engineReads(pkg.TypesInfo, decl)...)
					continue
				}
				if strings.HasPrefix(strings.ToLower(fn.Name.Name), checkPrefix) {
					continue
				}
				if defined := pkg.TypesInfo.Defs[fn.Name]; defined != nil {
					edges[defined] = append(edges[defined], engineReads(pkg.TypesInfo, fn.Body)...)
				}
			}
		}
	}

	reached := map[types.Object]bool{}
	for len(roots) > 0 {
		next := roots[len(roots)-1]
		roots = roots[:len(roots)-1]
		if reached[next] {
			continue
		}
		reached[next] = true
		roots = append(roots, edges[next]...)
	}
	return reached
}

// engineReads is every engine object a piece of syntax reads.
//
// Assignment targets and composite-literal keys are not reads: setting a field
// nothing goes on to look at is the class this test is for, so counting the
// write would let the class vouch for itself.
func engineReads(info *types.Info, node ast.Node) []types.Object {
	written := map[*ast.Ident]bool{}
	mark := func(target ast.Expr) {
		switch target := target.(type) {
		case *ast.SelectorExpr:
			written[target.Sel] = true
		case *ast.Ident:
			written[target] = true
		}
	}
	ast.Inspect(node, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			// Only a plain assignment. `x.N += 1` reads before it writes.
			if n.Tok == token.ASSIGN || n.Tok == token.DEFINE {
				for _, target := range n.Lhs {
					mark(target)
				}
			}
		case *ast.CompositeLit:
			for _, element := range n.Elts {
				if pair, ok := element.(*ast.KeyValueExpr); ok {
					mark(pair.Key)
				}
			}
		}
		return true
	})

	var out []types.Object
	ast.Inspect(node, func(n ast.Node) bool {
		name, ok := n.(*ast.Ident)
		if !ok || written[name] {
			return true
		}
		used := info.Uses[name]
		if used == nil || used.Pkg() == nil || used.Pkg().Path() != engineImport {
			return true
		}
		out = append(out, used)
		return true
	})
	return out
}
