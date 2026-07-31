// SPDX-License-Identifier: MIT

package pattern

import (
	"regexp"
	"testing"
)

func TestSynthesizeRegex(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		values []string
		want   string
	}{
		{"none", nil, AnyRegex},
		// One sample is not evidence of a pattern, so the caller is left to
		// fall back on the declared type's shape.
		{"seen once", []string{"Toyota"}, AnyRegex},
		// The same value on every page is a constant, and a literal is right.
		{"agreed across pages", []string{"Toyota", "Toyota", "Toyota"}, `^(Toyota)$`},
		{"same length digits", []string{"2019", "2021", "2018"}, `^(\d{4})$`},
		{"varying length letters", []string{"Ford", "Toyota"}, `^([A-Za-z]+)$`},
		{"shared punctuation", []string{"2019-01-02", "2021-12-31", "2020-06-07"}, `^(\d{4}-\d{2}-\d{2})$`},
		{"incompatible shapes", []string{"Toyota", "2019"}, AnyRegex},
		{"differing literals", []string{"a-b", "a_b"}, AnyRegex},
		{"shared label stays outside the capture", []string{"Make: Toyota", "Make: Honda"}, `^Make: ([A-Za-z]+)$`},
		{"shared suffix stays outside the capture", []string{"30000 USD", "42500 USD", "18750 USD"}, `^(\d{5}) USD$`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := SynthesizeRegex(tt.values)
			if got != tt.want {
				t.Fatalf("SynthesizeRegex(%q) = %q, want %q", tt.values, got, tt.want)
			}
			re, err := regexp.Compile(got)
			if err != nil {
				t.Fatalf("result does not compile: %v", err)
			}
			// Whatever it returns must actually match every input.
			for _, v := range tt.values {
				if !re.MatchString(v) {
					t.Errorf("pattern %q does not match its own input %q", got, v)
				}
			}
		})
	}
}

// TestSynthesizeRegexCapturesTheValue checks the point of the capture group:
// applying it to the observed text must yield the data, not the boilerplate
// around it.
func TestSynthesizeRegexCapturesTheValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		values []string
		text   string
		want   string
	}{
		{[]string{"Make: Toyota", "Make: Honda"}, "Make: Toyota", "Toyota"},
		{[]string{"Year: 2019", "Year: 2021", "Year: 2020"}, "Year: 2021", "2021"},
		{[]string{"30000 USD", "42500 USD", "18750 USD"}, "30000 USD", "30000"},
		{[]string{"Toyota", "Honda"}, "Toyota", "Toyota"},
	}

	for _, tt := range tests {
		re := regexp.MustCompile(SynthesizeRegex(tt.values))
		m := re.FindStringSubmatch(tt.text)
		if m == nil {
			t.Errorf("pattern %q did not match %q", re, tt.text)
			continue
		}
		if m[1] != tt.want {
			t.Errorf("pattern %q captured %q from %q, want %q", re, m[1], tt.text, tt.want)
		}
	}
}

// A width seen only across a small sample must not become a constraint: the
// next value is usually a different length, and a locator whose regex fails
// now yields nothing rather than the raw text.
func TestSynthesizeRegexDoesNotPinVaryingWidths(t *testing.T) {
	t.Parallel()

	got := SynthesizeRegex([]string{"ford", "toyota", "honda"})
	re := regexp.MustCompile(got)
	for _, unseen := range []string{"kia", "volkswagen", "a"} {
		if !re.MatchString(unseen) {
			t.Errorf("pattern %q rejects %q, a value of the same shape", got, unseen)
		}
	}

	// A width enough samples agree on is real evidence and is still pinned.
	years := SynthesizeRegex([]string{"2019", "2021", "2018"})
	if years != `^(\d{4})$` {
		t.Errorf("years = %q, want the agreed width kept", years)
	}
	if regexp.MustCompile(years).MatchString("219") {
		t.Error("a fixed four-digit width should not accept three digits")
	}
}

func TestSynthesizeRegexAlwaysCaptures(t *testing.T) {
	t.Parallel()

	for _, values := range [][]string{
		{"Toyota"},
		{"2019", "2020"},
		{"Toyota", "2019"},
	} {
		re := regexp.MustCompile(SynthesizeRegex(values))
		if re.NumSubexp() != 1 {
			t.Errorf("SynthesizeRegex(%q) has %d capture groups, want 1", values, re.NumSubexp())
		}
	}
}

func TestSynthesizeURI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		uris  []string
		match []string
	}{
		{
			name:  "single url is literal",
			uris:  []string{"https://example.com/cars"},
			match: []string{"https://example.com/cars"},
		},
		{
			name:  "varying segment generalizes",
			uris:  []string{"https://example.com/cars/1", "https://example.com/cars/2"},
			match: []string{"https://example.com/cars/1", "https://example.com/cars/99"},
		},
		{
			name:  "differing depth keeps common prefix",
			uris:  []string{"https://example.com/a/b", "https://example.com/a/b/c"},
			match: []string{"https://example.com/a/b", "https://example.com/a/b/c/d"},
		},
		{
			name:  "differing hosts generalize the host",
			uris:  []string{"https://a.example.com/x", "https://b.example.com/x"},
			match: []string{"https://c.example.com/x"},
		},
		{
			name:  "mixed schemes collapse",
			uris:  []string{"http://example.com/x", "https://example.com/x"},
			match: []string{"http://example.com/x", "https://example.com/x"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pattern := SynthesizeURI(tt.uris)
			re, err := regexp.Compile(pattern)
			if err != nil {
				t.Fatalf("SynthesizeURI(%q) = %q, does not compile: %v", tt.uris, pattern, err)
			}
			for _, u := range tt.uris {
				if !re.MatchString(u) {
					t.Errorf("pattern %q does not match its own input %q", pattern, u)
				}
			}
			for _, u := range tt.match {
				if !re.MatchString(u) {
					t.Errorf("pattern %q does not match %q", pattern, u)
				}
			}
		})
	}
}

func TestSynthesizeURIRejectsUnrelated(t *testing.T) {
	t.Parallel()

	re := regexp.MustCompile(SynthesizeURI([]string{
		"https://example.com/cars/1",
		"https://example.com/cars/2",
	}))
	for _, u := range []string{
		"https://example.com/boats/1",
		"https://other.com/cars/1",
		"https://example.com/cars/1/extra",
	} {
		if re.MatchString(u) {
			t.Errorf("pattern matched unrelated url %q", u)
		}
	}
}

func TestGeneralize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		paths []string
		want  string
	}{
		{"none", nil, ""},
		{"single is unchanged", []string{"/ul[1]/li[2]"}, "/ul[1]/li[2]"},
		{
			name:  "varying index dropped, stable index kept",
			paths: []string{"/ul[1]/li[1]/span[1]", "/ul[1]/li[2]/span[1]", "/ul[1]/li[3]/span[1]"},
			want:  "/ul[1]/li/span[1]",
		},
		{
			name:  "different structure falls back to indexless",
			paths: []string{"/ul[1]/li[1]", "/ol[1]/li[1]"},
			want:  "/ul/li",
		},
		{
			name:  "jsonpath indices generalize too",
			paths: []string{"$.vehicles[0].make", "$.vehicles[1].make"},
			want:  "$.vehicles.make",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Generalize(tt.paths); got != tt.want {
				t.Errorf("Generalize(%q) = %q, want %q", tt.paths, got, tt.want)
			}
		})
	}
}

func TestGeneralizeSelector(t *testing.T) {
	t.Parallel()

	got := GeneralizeSelector([]string{
		"div:nth-of-type(1) > span:nth-of-type(2)",
		"div:nth-of-type(2) > span:nth-of-type(2)",
	})
	want := "div > span:nth-of-type(2)"
	if got != want {
		t.Errorf("GeneralizeSelector = %q, want %q", got, want)
	}
}

// TestSynthesizeURIKeepsTrailingSlash guards a locator's ability to match the
// URLs it was induced from. Directory-style URLs end in a slash; trimming it
// while splitting the path and then anchoring with $ produced a pattern that
// matched none of its own inputs, so every model induced over such a site
// extracted nothing.
func TestSynthesizeURIKeepsTrailingSlash(t *testing.T) {
	tests := []struct {
		name  string
		uris  []string
		match []string
		skip  []string
	}{
		{
			name:  "all with a trailing slash",
			uris:  []string{"http://example.com/cars/one/", "http://example.com/cars/two/"},
			match: []string{"http://example.com/cars/three/"},
			skip:  []string{"http://example.com/cars/three/extra/"},
		},
		{
			name:  "none with a trailing slash",
			uris:  []string{"http://example.com/cars/one", "http://example.com/cars/two"},
			match: []string{"http://example.com/cars/three"},
			skip:  []string{"http://example.com/other/three"},
		},
		{
			name:  "mixed",
			uris:  []string{"http://example.com/cars/one/", "http://example.com/cars/two"},
			match: []string{"http://example.com/cars/three", "http://example.com/cars/three/"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expr := SynthesizeURI(tt.uris)
			re, err := regexp.Compile(expr)
			if err != nil {
				t.Fatalf("SynthesizeURI produced an invalid pattern %q: %v", expr, err)
			}
			// The pattern must match every URL it was derived from.
			for _, u := range tt.uris {
				if !re.MatchString(u) {
					t.Errorf("pattern %q does not match its own input %q", expr, u)
				}
			}
			for _, u := range tt.match {
				if !re.MatchString(u) {
					t.Errorf("pattern %q should match %q", expr, u)
				}
			}
			for _, u := range tt.skip {
				if re.MatchString(u) {
					t.Errorf("pattern %q should not match %q", expr, u)
				}
			}
		})
	}
}
