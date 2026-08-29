// SPDX-License-Identifier: GPL-3.0-or-later

package extract_test

import (
	"slices"
	"strings"
	"testing"
)

// The input here is the open web, so a page holding something unexpected is the
// ordinary case rather than the exception. These are the three ways it was not.

// TestAnEmptyDeclaredValueDoesNotHideTheRealOne.
//
// A page that declares a name and leaves it blank - `<span itemprop="author">`
// with nothing in it, which is what a template renders before its data arrives
// - used to end the search. The remaining vocabularies and the
// well-known-element and class-or-id fallbacks were never reached, so the
// `<div class="author">` holding the actual name was never looked at and a
// required property was reported missing on a page that plainly had it.
func TestAnEmptyDeclaredValueDoesNotHideTheRealOne(t *testing.T) {
	item := one(t, `
  item "article" {
    property "title" {
      type = str
    }

    property "author" {
      type = str
    }
  }
`, `<html><head><title>A story</title></head><body>
  <span itemprop="author"></span>
  <div class="author">Alex Doe</div>
</body></html>`)

	got, ok := item.Values["author"]
	if !ok {
		t.Fatalf("author found nothing, though the page says it: %v", item.Values)
	}
	if got.Text != "Alex Doe" {
		t.Errorf("author is %q", got.Text)
	}
}

// TestAnEmptyWellKnownElementDoesNotEndTheSearchEither.
//
// The fix above stopped an empty declared value ending the search, and stopped
// one fallback too early: byWellKnownElement returned the node it found
// whatever was in it, so an empty <title> ended the search just as the empty
// itemprop had. The test written with it used `author`, which is the one name
// byWellKnownElement does not claim, so it could not see this.
//
// <title> is the element a template leaves for last, which makes this the
// commonest arrangement there is rather than an exotic one.
func TestAnEmptyWellKnownElementDoesNotEndTheSearchEither(t *testing.T) {
	item := one(t, `
  item "article" {
    property "title" {
      type = str
    }
  }
`, `<html><head><title></title></head><body>
  <span itemprop="title"></span>
  <div class="title">A story</div>
</body></html>`)

	got, ok := item.Values["title"]
	if !ok {
		t.Fatalf("title found nothing, though the page says it: %v", item.Values)
	}
	if got.Text != "A story" {
		t.Errorf("title is %q", got.Text)
	}
}

// TestAPlaceholderDoesNotShadowTheValueBesideIt.
//
// A page may carry an empty element for a name and the filled one later: a
// template's slot and the hydrated byline, or a masthead widget above the
// article. The first itemprop for a name won whatever it held, so the page
// stated the value in microdata and nothing could read it - and no fallback
// could recover it, because the real value was never recorded anywhere.
func TestAPlaceholderDoesNotShadowTheValueBesideIt(t *testing.T) {
	item := one(t, `
  item "article" {
    property "title" {
      type = str
    }

    property "author" {
      type = str
    }
  }
`, `<html><head><title>A story</title></head><body>
  <span itemprop="author"></span>
  <article><span itemprop="author">Alex Doe</span></article>
</body></html>`)

	got, ok := item.Values["author"]
	if !ok {
		t.Fatalf("author found nothing, though the page states it in microdata: %v", item.Values)
	}
	if got.Text != "Alex Doe" {
		t.Errorf("author is %q", got.Text)
	}
}

// TestAnXPathComparisonAgainstNonsenseDoesNotPanic.
//
// The XPath library panics rather than erroring when a comparison meets
// something that is not a number: it calls ParseFloat and panics on the error.
// Nothing in the process recovers, so one page with an unexpected attribute
// killed the crawler in the middle of a crawl.
//
// No match is also what XPath itself specifies, since a comparison against NaN
// is false.
func TestAnXPathComparisonAgainstNonsenseDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("extraction panicked on ordinary page content: %v", r)
		}
	}()

	item := one(t, `
  item "article" {
    property "title" {
      xpath = ["//div[@data-score > 0.5]//h1"]
    }

    property "kicker" {
      css = [".kicker"]
    }
  }
`, `<html><body>
  <div data-score="n/a"><h1>A story</h1></div>
  <p class="kicker">A kicker</p>
</body></html>`)

	// And the rest of the page is still read: a locator that cannot match must
	// not take the properties around it down with it.
	if _, ok := item.Values["kicker"]; !ok {
		t.Errorf("the page was abandoned after the xpath could not match: %v", item.Values)
	}
}

// TestADeclaredBaseDecidesWhatARelativeLinkMeans.
//
// A page may say what its relative links are relative to, and it was ignored.
// That is not a near miss: it is a different path, so every relative link on
// such a page was queued pointing somewhere the site does not serve, and the
// pages it meant to point at were never queued at all.
func TestADeclaredBaseDecidesWhatARelativeLinkMeans(t *testing.T) {
	result := run(t, `
  item "article" {
    property "title" {
      type = str
    }

    property "next" {
      css        = ["a.next"]
      transforms = [absurl]
    }
  }
`, `<html><head><title>A story</title><base href="https://example.com/blog/"></head>
<body>
  <a href="post1">A post</a>
  <a class="next" href="post2">The next one</a>
</body></html>`)

	if !slices.ContainsFunc(result.Links, func(l string) bool {
		return strings.Contains(l, "/blog/post1")
	}) {
		t.Errorf("a relative link resolved against the page rather than its declared base: %v",
			result.Links)
	}

	// The absurl transform resolves against the same thing, or a value and a
	// link on one page would disagree about what the page means.
	if len(result.Items) == 1 {
		if next, ok := result.Items[0].Values["next"]; ok && !strings.Contains(next.Text, "/blog/post2") {
			t.Errorf("absurl resolved against the page rather than its declared base: %q", next.Text)
		}
	}
}
