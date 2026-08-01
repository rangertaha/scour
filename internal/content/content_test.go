// SPDX-License-Identifier: GPL-3.0-or-later

package content

import (
	"slices"
	"testing"
)

func set(t *testing.T, allow, deny []string) *Set {
	t.Helper()
	s, err := New(allow, deny)
	if err != nil {
		t.Fatalf("New(%v, %v): %v", allow, deny, err)
	}
	return s
}

func TestShorthandsExpand(t *testing.T) {
	s := set(t, []string{"html"}, nil)

	if !s.AllowsMIME("text/html") {
		t.Error("html should allow text/html")
	}
	if !s.AllowsMIME("text/html; charset=utf-8") {
		t.Error("a charset parameter must not change the verdict")
	}
	if !s.AllowsMIME("application/xhtml+xml") {
		t.Error("html should also allow xhtml")
	}
	if s.AllowsMIME("application/pdf") {
		t.Error("html must not allow pdf")
	}
}

func TestWildcardSubtype(t *testing.T) {
	s := set(t, []string{"image"}, nil)
	if !s.AllowsMIME("image/png") || !s.AllowsMIME("image/svg+xml") {
		t.Error("image/* should match any image subtype")
	}
	if s.AllowsMIME("text/html") {
		t.Error("image/* must not match html")
	}
}

func TestEmptyAllowMeansEverything(t *testing.T) {
	s := set(t, nil, nil)
	for _, ct := range []string{"text/html", "application/pdf", "image/png", "video/mp4"} {
		if !s.AllowsMIME(ct) {
			t.Errorf("an empty allow list should permit %s", ct)
		}
	}
}

func TestDenyBeatsAllow(t *testing.T) {
	s := set(t, []string{"text/*"}, []string{"text/css"})
	if !s.AllowsMIME("text/html") {
		t.Error("text/html should still be allowed")
	}
	if s.AllowsMIME("text/css") {
		t.Error("the deny list must win")
	}
}

func TestAbsentOrMalformedTypeIsAllowed(t *testing.T) {
	s := set(t, []string{"html"}, nil)
	if !s.AllowsMIME("") {
		t.Error("a missing Content-Type must not be grounds for rejection")
	}
	if !s.AllowsMIME("nonsense") {
		t.Error("an unparseable Content-Type must not be grounds for rejection")
	}
}

func TestAllowsPathOnlyRulesOutTheCertain(t *testing.T) {
	s := set(t, []string{"html"}, nil)

	// Ruled out by extension, so no request is made at all.
	for _, p := range []string{"/logo.png", "/style.css", "/app.js", "/doc.pdf", "/data.json"} {
		if s.AllowsPath(p) {
			t.Errorf("%s should be skipped before the request", p)
		}
	}
	// Everything else gets the benefit of the doubt: the header decides.
	for _, p := range []string{"/cars/", "/cars/one", "/page.html", "/search?q=x", "/odd.wat"} {
		if !s.AllowsPath(p) {
			t.Errorf("%s should be fetched and judged by its header", p)
		}
	}
}

func TestAllowsPathWithPDFAllowed(t *testing.T) {
	s := set(t, []string{"html", "pdf"}, nil)
	if !s.AllowsPath("/brochure.pdf") {
		t.Error("a pdf should be requested once pdf is allowed")
	}
	if s.AllowsPath("/logo.png") {
		t.Error("an image should still be skipped")
	}
}

func TestShorthandNamesAContentType(t *testing.T) {
	tests := map[string]string{
		"text/html; charset=utf-8": "html",
		"application/pdf":          "pdf",
		"application/ld+json":      "json",
		"image/png":                "image",
		"application/zip":          "application/zip",
		"":                         "",
	}
	for in, want := range tests {
		if got := Shorthand(in); got != want {
			t.Errorf("Shorthand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBadPatternIsAnError(t *testing.T) {
	if _, err := New([]string{"not a mime type"}, nil); err == nil {
		t.Error("an unknown shorthand that is not a MIME type must be rejected")
	}
}

func TestExtractableCoversWhatWeCanParse(t *testing.T) {
	for _, name := range []string{"html", "pdf", "json", "xml", "text"} {
		if !Extractable[name] {
			t.Errorf("%s should be extractable", name)
		}
	}
	if Extractable["image"] {
		t.Error("images carry no text to extract")
	}
}

// Feeds are the case the xml shorthand quietly missed. Sampled across a real
// list of news feeds, eight in twelve arrived as application/rss+xml, so a
// crawl restricted to xml would have skipped most of them.
func TestFeedTypes(t *testing.T) {
	set, err := New([]string{"feed"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, mime := range []string{
		"application/rss+xml",
		"application/rss+xml; charset=UTF-8",
		"application/atom+xml",
		"application/rdf+xml",
	} {
		if !set.AllowsMIME(mime) {
			t.Errorf("feed does not allow %q", mime)
		}
	}

	// Generic xml is deliberately a different shorthand, so asking for feeds
	// does not silently pull in every sitemap and config file on a site.
	if set.AllowsMIME("application/xml") {
		t.Error("feed allows plain application/xml")
	}
	if set.AllowsMIME("text/html") {
		t.Error("feed allows html")
	}
}

func TestFeedExtensions(t *testing.T) {
	for ext, want := range map[string][]string{
		".rss": {"feed"}, ".atom": {"feed"}, ".rdf": {"feed"},
		// .xml is both, and must be, because feed.xml is how the web
		// overwhelmingly publishes feeds.
		".xml": {"xml", "feed"},
	} {
		if got := extensions[ext]; !slices.Equal(got, want) {
			t.Errorf("%s maps to %q, want %q", ext, got, want)
		}
	}
}

// feed.xml is the single most common feed URL on the web. A crawl asked for
// feeds that skips it by its filename, before ever seeing a Content-Type,
// fetches nothing at all.
func TestAFeedDotXMLIsNotSkippedByItsExtension(t *testing.T) {
	feeds := set(t, []string{"feed"}, nil)
	for _, p := range []string{"/feed.xml", "/rss.xml", "/atom.xml", "/blog/index.xml"} {
		if !feeds.AllowsPath(p) {
			t.Errorf("a crawl asked for feeds skipped %s by its extension", p)
		}
	}
}

// The ambiguity must not become a hole: asked only for html, an .xml URL is
// still certainly unwanted.
func TestAnXMLPathIsStillSkippedWhenNeitherTypeIsWanted(t *testing.T) {
	if set(t, []string{"html"}, nil).AllowsPath("/feed.xml") {
		t.Error("a crawl asked only for html did not skip /feed.xml")
	}
}

// A feed scour cannot read is a feed it cannot extract articles from.
func TestFeedsAreExtractable(t *testing.T) {
	if !Extractable["feed"] {
		t.Error("feeds are not extractable, so nothing would be pulled out of them")
	}
}
