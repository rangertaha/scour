// SPDX-License-Identifier: GPL-3.0-or-later

package score

import (
	"slices"
	"strings"
	"testing"
)

func TestTokensPrefixByOrigin(t *testing.T) {
	got := Tokens(Features{
		URL:    "http://www.example.com/cars/used/f-series.html?sort=price",
		Anchor: "Ford F-Series",
		Depth:  3,
	})

	// The same word in a path and in anchor text is different evidence: sites
	// that put "cars" in the URL are not the same signal as pages that link to
	// one, so the prefixes must keep them apart.
	for _, want := range []string{
		"host:www.example.com",
		"path:cars", "path:used", "path:series",
		"ext:html",
		"query:sort",
		"anchor:ford", "anchor:series",
		"depth:4",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("missing token %q in %v", want, got)
		}
	}
}

func TestTokensDropNoise(t *testing.T) {
	got := Tokens(Features{URL: "http://example.com/a/b/", Anchor: "a of the"})
	for _, token := range got {
		if strings.HasPrefix(token, "depth:") {
			continue // a bucket number, not a word
		}
		word := token[strings.IndexByte(token, ':')+1:]
		if len(word) == 1 {
			t.Errorf("single-character token %q survived", token)
		}
	}
}

func TestTokensOnRubbishURL(t *testing.T) {
	if got := Tokens(Features{URL: "://not a url"}); len(got) != 0 {
		t.Errorf("tokens = %v, want none for an unparseable URL", got)
	}
}

func TestDepthIsBucketed(t *testing.T) {
	// Level 7 and level 8 are the same kind of deep; level 1 and level 5 are
	// not, so the buckets have to separate those and merge these.
	seven := Tokens(Features{URL: "http://example.com/", Depth: 7})
	eight := Tokens(Features{URL: "http://example.com/", Depth: 8})
	one := Tokens(Features{URL: "http://example.com/", Depth: 1})

	if !slices.Equal(seven, eight) {
		t.Errorf("depths 7 and 8 tokenised differently: %v and %v", seven, eight)
	}
	if slices.Equal(seven, one) {
		t.Errorf("depths 1 and 7 tokenised the same: %v", one)
	}
}

func TestFixedIsHonestAboutItself(t *testing.T) {
	f := Fixed(1)
	if f.Name() != "fixed" {
		t.Errorf("name = %q, want a name that says it is not a model", f.Name())
	}
	if got := f.Score(Features{URL: "http://example.com/"}); got != 1 {
		t.Errorf("score = %v, want 1", got)
	}
}

func TestRegistryRejectsUnknown(t *testing.T) {
	if _, err := New("no-such-scorer", Config{}); err == nil {
		t.Error("an unknown scorer must be an error, not a silent default")
	}
}

func TestRegistryRoundTrip(t *testing.T) {
	Register("test-scorer", func(Config) (Scorer, error) { return Fixed(0.25), nil })

	s, err := New("test-scorer", Config{})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Score(Features{}); got != 0.25 {
		t.Errorf("score = %v", got)
	}
	if !slices.Contains(Names(), "test-scorer") {
		t.Errorf("Names() = %v, missing the registered scorer", Names())
	}
}
