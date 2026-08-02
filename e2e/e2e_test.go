// SPDX-License-Identifier: GPL-3.0-or-later

package e2e

import (
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"testing"
)

// answer is what a fetch produced, without the response it came in.
//
// Handing back an *http.Response whose body has already been closed reads as an
// open body escaping the function that opened it, to a linter and to a person.
// These two fields are everything the tests here ask of one.
type answer struct {
	StatusCode int
	Header     http.Header
}

// get fetches a path from the site and returns what came back and its body.
func get(t *testing.T, base, p string) (answer, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+p, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", p, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return answer{StatusCode: resp.StatusCode, Header: resp.Header}, string(body)
}

// A fixture that 404s is a fixture nobody notices has gone. Every embedded file
// has to be reachable at its own path.
func TestEveryFileIsServed(t *testing.T) {
	srv := Server(t)

	var checked int
	err := fs.WalkDir(FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		checked++
		// A directory index is served at the directory, not at index.html.
		request := "/" + p
		if path.Base(p) == "index.html" {
			request = "/" + path.Dir(p) + "/"
			request = strings.ReplaceAll(request, "/./", "/")
		}
		if resp, _ := get(t, srv.URL, request); resp.StatusCode != http.StatusOK {
			t.Errorf("%s = %d", request, resp.StatusCode)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 25 {
		t.Errorf("only %d files in the site, which is fewer than it had", checked)
	}
	t.Logf("%d files served", checked)
}

// Every page exists because something went wrong on it, and the comment saying
// what is the only thing keeping a fixture from becoming decoration.
func TestEveryPageSaysWhyItExists(t *testing.T) {
	err := fs.WalkDir(FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".html") {
			return err
		}
		body, err := fs.ReadFile(FS(), p)
		if err != nil {
			return err
		}
		if !strings.Contains(string(body), "<!--") {
			t.Errorf("%s carries no comment saying what it is for", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The header is the authority. Each of these announces something the filename
// or the body disagrees with, which is where the .xml-is-not-a-feed fault
// lived.
func TestMislabelledResponses(t *testing.T) {
	srv := Server(t)

	cases := []struct{ path, wantType, wantIn string }{
		{"/odd/feed-as-plain-xml", "application/xml", "<rss"},
		{"/odd/article.pdf", "text/html", "Harbour plan approved"},
		{"/odd/image-as-html", "text/html", "PNG"},
	}
	for _, c := range cases {
		resp, body := get(t, srv.URL, c.path)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s = %d", c.path, resp.StatusCode)
			continue
		}
		if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, c.wantType) {
			t.Errorf("%s served %q, want %s", c.path, ct, c.wantType)
		}
		if !strings.Contains(body, c.wantIn) {
			t.Errorf("%s body does not contain %q", c.path, c.wantIn)
		}
	}

	// A missing Content-Type is not a malformed one, and must not be invented.
	if resp, _ := get(t, srv.URL, "/odd/no-content-type"); resp.Header.Get("Content-Type") != "" {
		t.Errorf("no-content-type served %q", resp.Header.Get("Content-Type"))
	}
}

func TestFailingAndSlowResponses(t *testing.T) {
	srv := Server(t)

	for p, want := range map[string]int{
		"/dynamic/500":      http.StatusInternalServerError,
		"/dynamic/404":      http.StatusNotFound,
		"/dynamic/slow":     http.StatusOK,
		"/dynamic/redirect": http.StatusOK, // the client follows it
	} {
		if resp, _ := get(t, srv.URL, p); resp.StatusCode != want {
			t.Errorf("%s = %d, want %d", p, resp.StatusCode, want)
		}
	}

	// The redirect lands on the article rather than merely answering.
	if _, body := get(t, srv.URL, "/dynamic/redirect"); !strings.Contains(body, "Harbour plan approved") {
		t.Error("the redirect did not land on the article")
	}
}

// A page that changed is the reason to come back, and one that did not is the
// reason not to.
func TestAChangingPageChanges(t *testing.T) {
	Reset()
	srv := Server(t)

	_, first := get(t, srv.URL, "/dynamic/changes")
	_, second := get(t, srv.URL, "/dynamic/changes")
	if first == second {
		t.Errorf("two fetches returned the same body:\n%s", first)
	}
	if !strings.Contains(first, "fetch 1") || !strings.Contains(second, "fetch 2") {
		t.Errorf("the fetch counter did not advance:\n%s\n---\n%s", first, second)
	}
}

// The React app is empty until it is rendered, which is the whole point of it.
// Its content is not merely hidden in a script string: it is in a JSON document
// the shell does not name, fetched after load.
func TestTheReactAppIsEmptyWithoutABrowser(t *testing.T) {
	srv := Server(t)

	_, shell := get(t, srv.URL, "/app/")
	for _, absent := range []string{"Tide gates fitted", "Ana Duarte", "craned in overnight"} {
		if strings.Contains(shell, absent) {
			t.Errorf("%q is in the shell, so nothing needs rendering", absent)
		}
	}
	if !strings.Contains(shell, `id="root"`) {
		t.Error("there is no mount point for the app")
	}

	// React itself is vendored, so the fixture renders with no network.
	if resp, body := get(t, srv.URL, "/vendor/react-dom.js"); resp.StatusCode != http.StatusOK || len(body) < 10000 {
		t.Errorf("react-dom is not served: %d, %d bytes", resp.StatusCode, len(body))
	}

	// The content the app renders lives here, one fetch further on.
	_, data := get(t, srv.URL, "/api/articles.json")
	if !strings.Contains(data, "Tide gates fitted") {
		t.Errorf("the app's content is not behind the API either:\n%s", data)
	}
}

// The older innerHTML page is still here, because a page whose content is in a
// script string is a different case from one whose content is a fetch away.
func TestTheScriptedPageNeedsAScript(t *testing.T) {
	srv := Server(t)

	_, body := get(t, srv.URL, "/dynamic/js-rendered")
	if !strings.Contains(body, "<script>") {
		t.Error("there is no script to render")
	}
	// The headline is in the script's string, which is the case this page is:
	// present in the bytes, absent from the document until the script runs.
	if !strings.Contains(body, `<div id="app"></div>`) {
		t.Error("the mount point is not empty, so there is nothing for the script to fill")
	}
}

// The faults this site exists to hold, each pinned to the page carrying it, so
// deleting one by accident fails rather than quietly reducing the corpus.
func TestTheKnownFaultsAreStillHere(t *testing.T) {
	cases := []struct{ file, marker, why string }{
		{"news/per-page-id.html", "asset-59da10e1", "a per-page id a selector can overfit to"},
		{"news/utility-classes.html", "text-3xl", "utility classes as the only labels"},
		{"news/attribute-outscores.html", `title="The Gazette"`, "an attribute name outscoring the headline"},
		{"news/canonical-only.html", `rel="canonical"`, "canonical as the only declaring attribute"},
		{"news/published-vs-modified.html", "dateModified", "published and modified genuinely differing"},
		{"news/short-title.html", "<title>Ads</title>", "a section page with an article's shape"},
		{"feeds/rss.xml", "<image>", "a channel image competing with the items"},
		{"feeds/atom.xml", `link rel="alternate" href=`, "an Atom link in href rather than text"},
		{"places/harbour.html", "GeoCoordinates", "an address split across five properties"},
	}
	for _, c := range cases {
		body, err := fs.ReadFile(FS(), c.file)
		if err != nil {
			t.Errorf("%s is gone, and with it %s", c.file, c.why)
			continue
		}
		if !strings.Contains(string(body), c.marker) {
			t.Errorf("%s no longer carries %s (looked for %q)", c.file, c.why, c.marker)
		}
	}

	// The constant-value fault needs three pages agreeing, or it is not one.
	var kickers int
	for _, f := range []string{"news/constant-section-1.html", "news/constant-section-2.html", "news/constant-section-3.html"} {
		body, err := fs.ReadFile(FS(), f)
		if err == nil && strings.Contains(string(body), "Other items that may interest you") {
			kickers++
		}
	}
	if kickers < 3 {
		t.Errorf("only %d pages share the constant kicker; a value that never changes needs more than one page to show it", kickers)
	}
}
