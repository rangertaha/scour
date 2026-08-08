// SPDX-License-Identifier: GPL-3.0-or-later

package robots_test

import (
	"go/ast"
	"go/types"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

const self = "github.com/rangertaha/scour/internal/robots"

// TestEverythingThisFileSaysIsActedOn.
//
// # The class
//
// A value parsed out of somebody else's input that nothing acts on.
//
// It is the same class [internal/engine.TestNoSettingIsAcceptedAndIgnored]
// holds, one step further out, and that test could not see this one: it follows
// the fields of a job document, and this is not a document field. It is a fact
// read off the network, which is the other half of what a crawler is configured
// by, and nothing was watching that half at all.
//
// # The instance that made it a test
//
// `Crawl-delay`. This package parsed it, and its own documentation argued at
// length for keeping it: "the same kind of thing as the rest of the file, an
// instruction about how to behave toward this host". `Rules.Delay` then had no
// caller anywhere in the module. Every site was crawled at whatever
// `scheduler.rate` said, a site asking for thirty seconds got one, and the file
// that said so had been read, parsed and understood on the way past.
//
// It is worse here than in a document. A setting a job wrote and scour ignores
// is a promise broken to the operator, who can at least see the crawl. An
// instruction a site wrote and scour ignores is a promise broken to somebody
// who is not watching, on their machine, under our name, and the only evidence
// is in their logs.
//
// # Why methods, and why the whole exported surface
//
// Because a parser's answers are its exported methods, and one that nothing
// asks is one nobody is obeying. There is no third possibility worth allowing:
// a rule this file states is either enforced somewhere or it is decoration, and
// this package's whole argument for being written by hand rather than imported
// is that it is not decoration.
//
// Test files are excluded on purpose. A test calling Delay proves the parser
// works and says nothing about whether a crawl obeys it, which is exactly the
// gap the instance above sat in for as long as it did.
func TestEverythingThisFileSaysIsActedOn(t *testing.T) {
	loaded, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedSyntax | packages.NeedTypes |
			packages.NeedTypesInfo | packages.NeedDeps | packages.NeedImports,

		// No test files. A test does not vouch for a rule being obeyed.
		Tests: false,
	}, "github.com/rangertaha/scour/...")
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

	answers := methodsOf(t, loaded, "Rules")
	if len(answers) == 0 {
		t.Fatal("found no methods on Rules, so this test asserts nothing")
	}

	// Every call anywhere but here. A caller inside this package is the parser
	// talking to itself, which is not somebody obeying it.
	for _, pkg := range loaded {
		if pkg.PkgPath == self || strings.HasPrefix(pkg.PkgPath, self+"/") {
			continue
		}
		for _, file := range pkg.Syntax {
			ast.Inspect(file, func(node ast.Node) bool {
				sel, ok := node.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if fn, ok := pkg.TypesInfo.Uses[sel.Sel].(*types.Func); ok {
					delete(answers, fn)
				}
				return true
			})
		}
	}

	for fn := range answers {
		t.Errorf("robots.Rules.%s is parsed out of somebody's robots.txt and no "+
			"non-test code in this module ever asks for it, so whatever it says "+
			"is being read and ignored.\n"+
			"  Either something acts on it, or it stops being parsed. A rule a "+
			"site states and this crawler does not enforce is worse than one it "+
			"never read: the site has been told otherwise by our own user agent.",
			fn.Name())
	}
}

// methodsOf is the exported methods declared on a named type in this package.
func methodsOf(t *testing.T, loaded []*packages.Package, name string) map[*types.Func]bool {
	t.Helper()

	for _, pkg := range loaded {
		if pkg.PkgPath != self {
			continue
		}
		obj := pkg.Types.Scope().Lookup(name)
		if obj == nil {
			t.Fatalf("%s declares no %s", self, name)
		}

		named, ok := obj.Type().(*types.Named)
		if !ok {
			t.Fatalf("%s is not a named type", name)
		}

		// The pointer's method set, which is how this type is used everywhere
		// and which contains the value receivers too, so neither kind of method
		// can be declared into a blind spot.
		out := map[*types.Func]bool{}
		set := types.NewMethodSet(types.NewPointer(named))
		for i := range set.Len() {
			if fn, ok := set.At(i).Obj().(*types.Func); ok && fn.Exported() {
				out[fn] = true
			}
		}
		return out
	}
	t.Fatalf("%s was not loaded", self)
	return nil
}
