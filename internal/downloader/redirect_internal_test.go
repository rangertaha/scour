// SPDX-License-Identifier: GPL-3.0-or-later

package downloader

import (
	"net/http"
	"net/url"
	"testing"
)

// Not every response comes from net/http. A cache hit is rebuilt from a sidecar
// written some time ago, possibly by an older version, and a middleware may
// synthesise one from nothing. So the client having already parsed a Location
// header is not something this can assume, and the cases where it did not are
// pinned here rather than through a server that cannot produce them.

func response(status int, url, location string) *Response {
	header := http.Header{}
	if location != "" {
		header.Set("Location", location)
	}
	return &Response{Status: status, URL: url, Header: header}
}

func TestRedirected(t *testing.T) {
	for name, tc := range map[string]struct {
		resp *Response
		want string // "" means: not a redirect
	}{
		"absolute":     {response(302, "http://a.example/old", "http://b.example/new"), "http://b.example/new"},
		"relative":     {response(302, "http://a.example/a/b/old", "new"), "http://a.example/a/b/new"},
		"rooted":       {response(301, "http://a.example/a/b/old", "/new"), "http://a.example/new"},
		"303":          {response(303, "http://a.example/", "/new"), "http://a.example/new"},
		"307":          {response(307, "http://a.example/", "/new"), "http://a.example/new"},
		"308":          {response(308, "http://a.example/", "/new"), "http://a.example/new"},
		"not a 3xx":    {response(200, "http://a.example/", "/new"), ""},
		"304":          {response(304, "http://a.example/", "/new"), ""},
		"no location":  {response(302, "http://a.example/", ""), ""},
		"bad location": {response(302, "http://a.example/", "://not a url"), ""},
		"bad base":     {response(302, "://not a url", "/new"), ""},
	} {
		t.Run(name, func(t *testing.T) {
			target, ok := redirected(tc.resp)
			if tc.want == "" {
				if ok {
					t.Errorf("followed it to %s", target)
				}
				return
			}
			if !ok {
				t.Fatal("not followed")
			}
			if got := target.String(); got != tc.want {
				t.Errorf("target = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSameHost(t *testing.T) {
	for name, tc := range map[string]struct {
		a, b string
		want bool
	}{
		"same":            {"http://a.example/one", "http://a.example/two", true},
		"case":            {"http://A.example/one", "http://a.example/two", true},
		"across schemes":  {"http://a.example/one", "https://a.example/two", true},
		"different":       {"http://a.example/", "http://b.example/", false},
		"different ports": {"http://a.example:80/", "http://a.example:8080/", false},
		"first is junk":   {"://not a url", "http://a.example/", false},
		"second is junk":  {"http://a.example/", "://not a url", false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := sameHost(tc.a, tc.b); got != tc.want {
				t.Errorf("sameHost(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// TestForwardDropsCredentialsWhenTheHostCannotBeRead. A URL that will not parse
// is not the same host as anything, so the cautious answer is the one that
// falls out: the credentials do not travel.
func TestForwardDropsCredentialsWhenTheHostCannotBeRead(t *testing.T) {
	req := &Request{
		URL:    "http://a.example/one",
		Method: http.MethodGet,
		Header: http.Header{"Authorization": {"Bearer secret"}, "X-Kept": {"yes"}},
	}
	resp := response(302, "://not a url", "/new")

	next := forward(req, resp, mustParse(t, "http://a.example/new"))
	if next.Header.Get("Authorization") != "" {
		t.Error("credentials travelled to a host nobody could identify")
	}
	if next.Header.Get("X-Kept") != "yes" {
		t.Error("an ordinary header was dropped")
	}
	if req.Header.Get("Authorization") == "" {
		t.Error("the original request was edited rather than copied")
	}
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()

	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return parsed
}
