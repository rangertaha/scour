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

	shown := runOK(t, dir, "list", "vehicle")
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

	shown := runOK(t, dir, "list", "vehicle")
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

	shown := runOK(t, dir, "list", "vehicle")
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

func TestHeaderDetection(t *testing.T) {
	tests := []struct {
		name   string
		record []string
		want   bool
	}{
		{"full header", []string{"name", "type", "example"}, true},
		{"name only", []string{"name"}, true},
		{"reordered", []string{"example", "name"}, true},
		{"mixed case", []string{"Name", "Example"}, true},
		{"data row", []string{"make", "Ford"}, false},
		{"no name column", []string{"type", "example"}, false},
		{"empty", []string{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := headerOf(tt.record) != nil; got != tt.want {
				t.Errorf("headerOf(%v) detected = %v, want %v", tt.record, got, tt.want)
			}
		})
	}
}
