// SPDX-License-Identifier: GPL-3.0-or-later

package engine_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/engine"
)

// The four fields that grow to thousands of entries can be read from a file
// beside the document. What these hold is the rule that makes that safe: the
// file is read where the document is, and what a cluster stores is the list
// rather than the reference, because nothing on the far side can see the
// author's files.

// beside writes a file next to a document and returns the directory.
func beside(t *testing.T, files map[string]string) string {
	t.Helper()

	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const withLines = `
job "news" {
  domains  = lines("domains.txt")
  start    = lines("seeds.txt")
  excluded = lines("excluded.txt")

  item "article" {
    property "title" {
      type = str
    }
  }
}
`

func TestLinesReadsAListBesideTheDocument(t *testing.T) {
	dir := beside(t, map[string]string{
		"domains.txt":  "example.com\nexample.org\n",
		"seeds.txt":    "https://example.com/\nhttps://example.org/\n",
		"excluded.txt": "/admin\n",
	})

	doc, err := engine.ParseIn([]byte(withLines), "job.hcl", dir)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	job := doc.Jobs[0]

	if got := strings.Join(job.Domains, ","); got != "example.com,example.org" {
		t.Errorf("domains are %q", got)
	}
	if len(job.Start) != 2 || job.Start[0] != "https://example.com/" {
		t.Errorf("start is %v", job.Start)
	}
	if len(job.Excluded) != 1 || job.Excluded[0] != "/admin" {
		t.Errorf("excluded is %v", job.Excluded)
	}
}

// TestLinesSkipsBlanksAndComments.
//
// The comments are the reason the format is not just "split on newlines": a
// seed list somebody maintains has notes in it saying why a URL is there, and a
// crawler that tried to fetch `# the old site, retired in March` would report
// it as a failure rather than as a line it should not have read.
func TestLinesSkipsBlanksAndComments(t *testing.T) {
	dir := beside(t, map[string]string{
		"domains.txt": "# the sites we cover\nexample.com\n\n  example.org  \n\n# example.net, retired\n",
		"seeds.txt":   "https://example.com/\n",
	})

	doc, err := engine.ParseIn([]byte(`
job "news" {
  domains = lines("domains.txt")
  start   = lines("seeds.txt")
}
`), "job.hcl", dir)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if got := strings.Join(doc.Jobs[0].Domains, ","); got != "example.com,example.org" {
		t.Errorf("domains are %q, so a comment or a blank line was read as an entry", got)
	}
}

// TestAMissingFileIsRefusedByName.
//
// A list that silently came back empty would be a crawl with no scope, which
// scope treats as "everything": a typo in a filename would widen the crawl to
// the whole web rather than stopping it.
func TestAMissingFileIsRefusedByName(t *testing.T) {
	_, err := engine.ParseIn([]byte(`
job "news" {
  domains = lines("nope.txt")
}
`), "job.hcl", beside(t, nil))
	if err == nil {
		t.Fatal("a document naming a file that is not there was accepted")
	}
	if !strings.Contains(err.Error(), "nope.txt") {
		t.Errorf("the error does not name the file: %v", err)
	}
}

// TestADocumentWithNoDirectoryRefusesLines.
//
// The case a node is in when it reads a stored job. Resolving against whatever
// working directory the node happens to have is the silent wrong answer this
// design exists to avoid: two nodes would crawl different sites and nothing
// would say so.
func TestADocumentWithNoDirectoryRefusesLines(t *testing.T) {
	_, err := engine.Parse([]byte(`
job "news" {
  domains = lines("domains.txt")
}
`), "job.hcl")
	if err == nil {
		t.Fatal("a stored document was allowed to read a file")
	}
	if !strings.Contains(err.Error(), "lines") {
		t.Errorf("the error does not name the function: %v", err)
	}
}

// TestExpandingWritesTheListIntoTheDocument.
//
// What makes a job submittable. The stored document has to carry the entries,
// because the cluster cannot see the file they came from.
func TestExpandingWritesTheListIntoTheDocument(t *testing.T) {
	dir := beside(t, map[string]string{
		"domains.txt":  "example.com\nexample.org\n",
		"seeds.txt":    "https://example.com/\n",
		"excluded.txt": "/admin\n",
	})

	expanded, err := engine.ExpandFiles([]byte(withLines), "job.hcl", dir)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if strings.Contains(string(expanded), "lines(") {
		t.Errorf("the expanded document still refers to a file:\n%s", expanded)
	}
	for _, want := range []string{"example.com", "example.org", "https://example.com/", "/admin"} {
		if !strings.Contains(string(expanded), want) {
			t.Errorf("%q is not in the expanded document:\n%s", want, expanded)
		}
	}

	// The point of expanding: what comes out parses where a node reads it,
	// with no directory and no files.
	doc, err := engine.Parse(expanded, "job.hcl")
	if err != nil {
		t.Fatalf("the expanded document does not parse as a stored job: %v", err)
	}
	if got := strings.Join(doc.Jobs[0].Domains, ","); got != "example.com,example.org" {
		t.Errorf("the expanded job has domains %q", got)
	}
}

// TestExpandingLeavesTheRestOfTheDocumentAlone.
//
// A resubmission is reviewed by its diff against the last one, so a submission
// path that reformatted the document would make every diff unreadable. Only the
// attributes that read a file may change.
//
// The indentation here is four spaces on purpose. The first version of this
// rebuilt the document with hclwrite, whose Bytes reformats every token it
// holds, and a fixture already in canonical form would have let that through:
// what it actually did was re-indent the whole file to two spaces, change the
// raw text of every plugin body, and make [Diff] report a cache move on a
// document whose cache had not moved.
func TestExpandingLeavesTheRestOfTheDocumentAlone(t *testing.T) {
	dir := beside(t, map[string]string{"domains.txt": "example.com\n"})

	source := "job \"news\" {\n" +
		"    # Where the crawl starts. This comment must survive.\n" +
		"    domains = lines(\"domains.txt\")\n" +
		"    start   = [\"https://example.com/\"]\n" +
		"\n" +
		"    downloader {\n" +
		"        plugin \"cache\" {\n" +
		"            backend = \"s3\"   # and so must this one\n" +
		"        }\n" +
		"    }\n" +
		"}\n"

	expanded, err := engine.ExpandFiles([]byte(source), "job.hcl", dir)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	for _, want := range []string{
		"    # Where the crawl starts. This comment must survive.",
		"            backend = \"s3\"   # and so must this one",
		"    start   = [\"https://example.com/\"]",
		"        plugin \"cache\" {",
	} {
		if !strings.Contains(string(expanded), want) {
			t.Errorf("expanding changed something it should not have, %q is gone:\n%s", want, expanded)
		}
	}
}

// TestExpandingDoesNotLookLikeAChangeToTheJob.
//
// The consequence of the above, and the reason it is not cosmetic. Plugin
// bodies are fingerprinted by their raw source text, so a submission path that
// re-indented the document reported that every plugin's configuration had
// changed. The cache plugin's counts as [EffectCacheMoved], which the default
// mutation policy refuses: a running job whose only edit was to read its
// domains from a file was rejected as though the operator had pointed the crawl
// at a different bucket.
func TestExpandingDoesNotLookLikeAChangeToTheJob(t *testing.T) {
	dir := beside(t, map[string]string{"domains.txt": "example.com\n"})

	body := func(scope string) string {
		return "job \"news\" {\n" +
			"    " + scope + "\n" +
			"    start = [\"https://example.com/\"]\n\n" +
			"    item \"article\" {\n" +
			"        property \"title\" {\n" +
			"            type = str\n" +
			"        }\n" +
			"    }\n\n" +
			"    downloader {\n" +
			"        plugin \"cache\" {\n" +
			"            backend = \"s3\"\n" +
			"            bucket  = \"pages\"\n" +
			"        }\n" +
			"    }\n}\n"
	}

	running, err := engine.Parse([]byte(body(`domains = ["example.com"]`)), "job.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	expanded, err := engine.ExpandFiles([]byte(body(`domains = lines("domains.txt")`)), "job.hcl", dir)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	submitted, err := engine.Parse(expanded, "job.hcl")
	if err != nil {
		t.Fatalf("the expanded document does not parse: %v", err)
	}

	if changes := engine.Diff(running.Jobs[0], submitted.Jobs[0]); changes.Any() {
		t.Errorf("expanding a list into the same list is reported as %d changes to the job: %v",
			len(changes), changes)
	}
	if review := submitted.Jobs[0].Review(running.Jobs[0]); !review.OK() {
		t.Errorf("resubmitting it would be refused: %v", review.Refused)
	}
}

// TestALongListIsWrittenOnePerLine.
//
// These lists are the long ones by definition, and the stored document is what
// a resubmission's diff is read against. Thousands of entries on one line is a
// diff nobody can review, and a one-entry change in it shows as the whole line.
func TestALongListIsWrittenOnePerLine(t *testing.T) {
	var many strings.Builder
	for i := range 8 {
		fmt.Fprintf(&many, "example%d.com\n", i)
	}
	dir := beside(t, map[string]string{"domains.txt": many.String()})

	expanded, err := engine.ExpandFiles([]byte("job \"news\" {\n  domains = lines(\"domains.txt\")\n}\n"),
		"job.hcl", dir)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if !strings.Contains(string(expanded), "\n    \"example0.com\",\n") {
		t.Errorf("a long list was not written one per line:\n%s", expanded)
	}

	doc, err := engine.Parse(expanded, "job.hcl")
	if err != nil {
		t.Fatalf("the expanded document does not parse: %v", err)
	}
	if len(doc.Jobs[0].Domains) != 8 {
		t.Errorf("the expanded list has %d entries", len(doc.Jobs[0].Domains))
	}
}

// TestADocumentThatReadsNoFilesIsUntouched.
//
// Byte for byte, because every submission passes through this and almost none
// of them use the function. Returning hclwrite's rendering instead would
// reformat every document in the world on its way to the cluster.
func TestADocumentThatReadsNoFilesIsUntouched(t *testing.T) {
	source := []byte("job \"news\" {\n  domains = [\"example.com\"]\n     start=[\"https://example.com/\"]\n}\n")

	expanded, err := engine.ExpandFiles(source, "job.hcl", t.TempDir())
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if string(expanded) != string(source) {
		t.Errorf("a document that reads no files was rewritten:\nwant %q\ngot  %q", source, expanded)
	}
}

// TestExpandingReportsAMissingFile.
func TestExpandingReportsAMissingFile(t *testing.T) {
	_, err := engine.ExpandFiles([]byte(`
job "news" {
  domains = lines("nope.txt")
}
`), "job.hcl", beside(t, nil))
	if err == nil {
		t.Fatal("expanding a document naming a file that is not there succeeded")
	}
	if !strings.Contains(err.Error(), "nope.txt") || !strings.Contains(err.Error(), "domains") {
		t.Errorf("the error names neither the file nor the field: %v", err)
	}
}

// TestAnEmptyListFileIsAnEmptyList.
//
// Not an error here. Whether a field may be empty is [Document.Validate]'s to
// say, and it should say it in the same words whether the list was written out
// or read in.
func TestAnEmptyListFileIsAnEmptyList(t *testing.T) {
	dir := beside(t, map[string]string{
		"domains.txt": "# nothing yet\n\n",
		"seeds.txt":   "https://example.com/\n",
	})

	doc, err := engine.ParseIn([]byte(`
job "news" {
  domains = lines("domains.txt")
  start   = lines("seeds.txt")
}
`), "job.hcl", dir)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(doc.Jobs[0].Domains) != 0 {
		t.Errorf("an empty file produced %v", doc.Jobs[0].Domains)
	}
}
