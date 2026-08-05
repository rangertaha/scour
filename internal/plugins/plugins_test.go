// SPDX-License-Identifier: GPL-3.0-or-later

package plugins_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestEveryPluginInThisRepositoryIsOnTheList.
//
// The class test for a shape that had already bitten once: a registry filled by
// an `init` in somebody else's package exists only if something imported that
// package, and nothing fails when nothing does. The `topic` middleware was
// written, tested and committed, and every binary refused a job that used it.
//
// A helper nobody has to remember to call retires nothing, so this does not
// check that the list is tidy. It walks the repository for packages that
// register something and fails if one is missing from the list, which makes
// forgetting a failing build rather than a feature that quietly does not exist.
func TestEveryPluginInThisRepositoryIsOnTheList(t *testing.T) {
	root := repoRoot(t)

	registering := registrars(t, root)
	if len(registering) == 0 {
		t.Fatal("found no packages that register anything, so this test is not checking what it claims")
	}

	listed := imports(t, filepath.Join(root, "internal", "plugins", "plugins.go"))

	var missing []string
	for _, pkg := range registering {
		if !listed[pkg] {
			missing = append(missing, pkg)
		}
	}
	sort.Strings(missing)

	if len(missing) > 0 {
		t.Errorf("these packages register something and are not imported by internal/plugins:\n  %s\n\n"+
			"A plugin nothing imports does not exist: a job naming it is refused with\n"+
			"\"nothing on this node implements it\". Add it to internal/plugins/plugins.go.",
			strings.Join(missing, "\n  "))
	}
}

// TestTheListHasNothingThatIsNotThere, so a package renamed or removed does not
// leave the list claiming a plugin the build does not have.
func TestTheListHasNothingThatIsNotThere(t *testing.T) {
	root := repoRoot(t)
	registering := map[string]bool{}
	for _, pkg := range registrars(t, root) {
		registering[pkg] = true
	}

	for pkg := range imports(t, filepath.Join(root, "internal", "plugins", "plugins.go")) {
		if !registering[pkg] {
			t.Errorf("internal/plugins imports %s, which registers nothing", pkg)
		}
	}
}

// registrars finds the packages whose init functions fill a registry.
//
// By parsing rather than by grepping, so that the word "Register" in a comment
// or a test does not count, and so that a call outside an init does not either:
// what makes a package a plugin is that importing it has an effect.
func registrars(t *testing.T, root string) []string {
	t.Helper()

	const module = "github.com/rangertaha/scour"
	found := map[string]bool{}

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".claude", "docs", "testdata", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			// A file that does not parse is the compiler's problem, not this
			// test's.
			return nil
		}

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "init" || fn.Recv != nil {
				continue
			}
			ast.Inspect(fn, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "Register" {
					return true
				}
				// A qualified call, so `Register` defined in this same package
				// does not count: what matters is filling somebody else's
				// registry.
				if _, ok := selector.X.(*ast.Ident); !ok {
					return true
				}

				relative, err := filepath.Rel(root, filepath.Dir(path))
				if err != nil {
					return true
				}
				found[module+"/"+filepath.ToSlash(relative)] = true
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	out := make([]string, 0, len(found))
	for pkg := range found {
		out = append(out, pkg)
	}
	sort.Strings(out)
	return out
}

// imports reads the import paths of one file.
func imports(t *testing.T, path string) map[string]bool {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	out := map[string]bool{}
	for _, one := range file.Imports {
		unquoted, err := strconv.Unquote(one.Path.Value)
		if err != nil {
			continue
		}
		out[unquoted] = true
	}
	return out
}

// repoRoot walks up from the test's directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's directory")
		}
		dir = parent
	}
}
