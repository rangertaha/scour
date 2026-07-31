// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestImportingTargetsAndProperties(t *testing.T) {
	dir := t.TempDir()

	urls := writeFile(t, dir, "urls.txt", strings.Join([]string{
		"# gathered by hand",
		"http://www.example.com/cars/",
		"",
		"example.org/others/",
	}, "\n"))
	props := writeFile(t, dir, "props.csv",
		"name,type,example,description,aliases\n"+
			"make,string,Ford,the company that built it,manufacturer;brand\n"+
			`price,number,"$42,000",what it sells for,cost;asking price`+"\n")

	out := runOK(t, dir, "import", "vehicle", "--urls", urls, "--props", props)
	if !strings.Contains(out, "2 urls") || !strings.Contains(out, "2 properties") {
		t.Fatalf("output = %s", out)
	}
	// A comment and a blank line are notes, not data.
	if strings.Contains(out, "3 urls") {
		t.Errorf("comments or blanks were imported: %s", out)
	}

	shown := runOK(t, dir, "item", "ls", "vehicle")
	if !strings.Contains(shown, "2 properties") {
		t.Errorf("status = %s", shown)
	}
}

// These files are assembled by hand or by another tool, so one bad line must
// not cost the thousand good ones around it.
func TestABadLineIsSkippedNotFatal(t *testing.T) {
	dir := t.TempDir()
	urls := writeFile(t, dir, "urls.txt", "http://www.example.com/\nnot a url\nhttp://example.org/\n")

	out := runOK(t, dir, "import", "vehicle", "--urls", urls)
	if !strings.Contains(out, "2 urls") || !strings.Contains(out, "1 skipped") {
		t.Errorf("output = %s", out)
	}
}

// Every other form of add is idempotent, and re-importing a list that has grown
// should add only what is new.
func TestImportingTwiceAddsNothing(t *testing.T) {
	dir := t.TempDir()
	urls := writeFile(t, dir, "urls.txt", "http://www.example.com/\nhttp://example.org/\n")

	runOK(t, dir, "import", "vehicle", "--urls", urls)
	runOK(t, dir, "import", "vehicle", "--urls", urls)

	shown := runOK(t, dir, "item", "ls", "vehicle")
	if !strings.Contains(shown, "targets     2") {
		t.Errorf("re-importing duplicated targets: %s", shown)
	}
}

// Batching is the difference between usable and not on a real list, so it has
// to survive a file longer than one batch and one that repeats itself after
// normalisation.
func TestImportingMoreThanOneBatch(t *testing.T) {
	dir := t.TempDir()

	var b strings.Builder
	const lines = 1200
	for i := range lines {
		fmt.Fprintf(&b, "http://example.com/%d/\n", i)
	}
	// Two URLs differing only by a fragment are one page. Collapsing them
	// inside a single batch must not make the insert fail, which is exactly
	// what happened on the real list that prompted the batching.
	b.WriteString("http://example.com/1/#a\nhttp://example.com/1/#b\n")

	urls := writeFile(t, dir, "many.txt", b.String())
	runOK(t, dir, "import", "vehicle", "--urls", urls)

	shown := runOK(t, dir, "item", "ls", "vehicle")
	if !strings.Contains(shown, fmt.Sprintf("targets     %d", lines)) {
		t.Errorf("status = %s", shown)
	}
}

// A schema file is edited by people, so columns are named rather than
// positional, and a file without a header still has to work.
func TestBothCSVForms(t *testing.T) {
	dir := t.TempDir()

	plain := writeFile(t, dir, "plain.csv", "mileage,42000\nvin,1HGBH41JXMN109186\n")
	if out := runOK(t, dir, "import", "plain", "--props", plain); !strings.Contains(out, "2 properties") {
		t.Errorf("headerless form = %s", out)
	}

	// Columns in an unusual order must still land in the right fields.
	shuffled := writeFile(t, dir, "shuffled.csv", "example,name,type\nFord,make,string\n")
	if out := runOK(t, dir, "import", "shuffled", "--props", shuffled); !strings.Contains(out, "1 properties") {
		t.Errorf("header form = %s", out)
	}
}

func TestImportNeedsSomethingToDo(t *testing.T) {
	dir := t.TempDir()
	if _, err := run(t, dir, "import", "vehicle"); err == nil {
		t.Error("an import with no files should say so rather than silently do nothing")
	}
}

func TestImportReportsAMissingFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := run(t, dir, "import", "vehicle", "--urls", filepath.Join(dir, "absent.txt")); err == nil {
		t.Error("a missing file should be an error")
	}
}

// export and import are a pair, so what one writes the other has to read back
// to the same rows. Comparing the printed output is not enough: a marker the
// importer does not understand comes back as part of the hostname and the two
// listings match while the data is wrong.
func TestExportImportRoundTrip(t *testing.T) {
	dir := t.TempDir()
	runOK(t, dir, "item", "add", "news", "-d", "example.com", "-u", "http://example.com/a/")
	runOK(t, dir, "item", "add", "news", "-d", "other.test", "--subdomains")
	runOK(t, dir, "item", "add", "news", "-p", "author", "-e", "Hannah McLeod")

	urls := filepath.Join(dir, "u.txt")
	domains := filepath.Join(dir, "d.txt")
	props := filepath.Join(dir, "p.csv")
	runOK(t, dir, "export", "news", "--urls", urls, "--domains", domains, "--props", props)

	// The subdomain flag belongs to the row, so it has to survive the file.
	written, err := os.ReadFile(domains)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "*.other.test") {
		t.Errorf("a subdomain target was not marked in the file:\n%s", written)
	}

	runOK(t, dir, "item", "add", "copy")
	runOK(t, dir, "import", "copy", "--urls", urls, "--domains", domains, "--props", props)

	before := runOK(t, dir, "--json", "item", "ls", "news")
	after := runOK(t, dir, "--json", "item", "ls", "copy")
	for _, field := range []string{`"Targets"`, `"Properties"`} {
		if pick(before, field) != pick(after, field) {
			t.Errorf("%s differs after a round trip: %q vs %q",
				field, pick(before, field), pick(after, field))
		}
	}
}

// pick pulls one "Field": value line out of the json listing.
func pick(out, field string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, field) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// A hostname has no spaces and no #. Accepting one wrote a target that could
// never match anything, and it printed back unchanged so it looked fine.
func TestDomainWithACommentIsRejected(t *testing.T) {
	dir := t.TempDir()
	if _, err := run(t, dir, "item", "add", "news", "-d", "bad.test  # note"); err == nil {
		t.Error("a domain with a trailing comment must be rejected")
	}
	if _, err := run(t, dir, "item", "add", "news", "-d", "two words.test"); err == nil {
		t.Error("a domain with a space must be rejected")
	}
}
