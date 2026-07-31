// SPDX-License-Identifier: GPL-3.0-or-later

package transport

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"golang.org/x/net/html"
)

// Policy decides when a request goes to a browser instead of the network.
type Policy string

// The escalation policies.
const (
	// Never uses plain HTTP only.
	Never Policy = "never"
	// Auto fetches over HTTP first and escalates when the response looks
	// client-rendered. This is the default: most of the web does not need a
	// browser, and paying for one on every page would be absurd.
	Auto Policy = "auto"
	// Always skips the HTTP attempt, for hosts already known to need a
	// browser.
	Always Policy = "always"
)

// ParsePolicy reads a configured policy, defaulting to Auto.
func ParsePolicy(s string) Policy {
	switch Policy(strings.ToLower(strings.TrimSpace(s))) {
	case Never:
		return Never
	case Always:
		return Always
	default:
		return Auto
	}
}

// Escalating fetches over one transport and retries over another when the
// first returns a page with nothing in it.
//
// Living at the transport layer is the whole point. colly sees one response,
// so cookies, robots, depth, retries, callbacks and metrics all keep working
// with no second path through the crawler, and a page that needed a browser is
// indistinguishable downstream from one that did not.
type Escalating struct {
	// Base is the ordinary network transport.
	Base http.RoundTripper
	// Browser renders the page. A nil browser disables escalation, so a build
	// without one degrades to plain HTTP rather than failing.
	Browser http.RoundTripper
	// Policy decides when the browser is used.
	Policy Policy
	// OnEscalate, when set, is called with the host each time a page is
	// re-fetched in the browser, so the decision can be remembered.
	OnEscalate func(host string)

	mu     sync.Mutex
	sticky map[string]bool // hosts that have needed the browser before
}

// RoundTrip implements [http.RoundTripper].
func (e *Escalating) RoundTrip(req *http.Request) (*http.Response, error) {
	if e.Browser == nil || e.Policy == Never {
		return e.Base.RoundTrip(req)
	}

	if e.Policy == Always || e.known(req.URL.Host) {
		return e.Browser.RoundTrip(req)
	}

	resp, err := e.Base.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}

	body, ok := readBody(resp)
	if !ok {
		return resp, nil
	}
	if !clientRendered(resp, body) {
		return resp, nil
	}

	slog.Debug("escalating to the browser", "url", req.URL.String())
	rendered, rerr := e.Browser.RoundTrip(req)
	if rerr != nil {
		// The browser failing is not a reason to lose a response we already
		// have: an empty page beats no page.
		slog.Warn("browser fetch failed, keeping the plain response", "url", req.URL.String(), "err", rerr)
		return resp, nil
	}

	e.remember(req.URL.Host)
	return rendered, nil
}

// Prime marks hosts as already known to need the browser, so a crawl does not
// have to rediscover what an earlier one already paid to learn.
//
// It deliberately does not call OnEscalate: nothing new has been discovered,
// and reporting it again would rewrite a row that already says this.
func (e *Escalating) Prime(hosts ...string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sticky == nil {
		e.sticky = make(map[string]bool, len(hosts))
	}
	for _, host := range hosts {
		if host != "" {
			e.sticky[host] = true
		}
	}
}

func (e *Escalating) known(host string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sticky[host]
}

// remember records that a host needed the browser, so the rest of the crawl
// stops paying for the HTTP attempt that will be thrown away.
func (e *Escalating) remember(host string) {
	e.mu.Lock()
	if e.sticky == nil {
		e.sticky = map[string]bool{}
	}
	already := e.sticky[host]
	e.sticky[host] = true
	e.mu.Unlock()

	if !already && e.OnEscalate != nil {
		e.OnEscalate(host)
	}
}

// readBody drains a response and puts the bytes back, so the caller still gets
// a readable body.
func readBody(resp *http.Response) ([]byte, bool) {
	if resp.Body == nil {
		return nil, false
	}
	// A page worth judging is small; anything larger is not the shape of a
	// client-rendered shell, and reading it all to find out would be wasteful.
	const maxInspect = 1 << 20

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxInspect))
	resp.Body.Close()
	if err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return nil, false
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return body, true
}

// substantialText is how many characters of visible text a page needs before
// it counts as having content. A nav bar and a copyright line clear a hundred
// characters; an article does not stop there.
const substantialText = 200

// clientRendered reports whether a response looks like a shell waiting for
// JavaScript to fill it: HTML that parses, carries scripts, and yet offers
// neither links nor text worth reading.
//
// Each condition on its own is too eager. A page with no links might be a leaf
// article; a page with little text might be an index. Together, on a document
// that ships JavaScript, they are the signature of a page whose content has
// not arrived yet.
func clientRendered(resp *http.Response, body []byte) bool {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.Contains(strings.ToLower(ct), "html") {
		return false
	}

	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return false
	}

	var links, scripts, text int
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		switch {
		case n.Type == html.ElementNode && n.Data == "a":
			for _, a := range n.Attr {
				if a.Key == "href" && strings.TrimSpace(a.Val) != "" && !strings.HasPrefix(a.Val, "#") {
					links++
					break
				}
			}
		case n.Type == html.ElementNode && n.Data == "script":
			scripts++
			return // script bodies are not page text
		case n.Type == html.ElementNode && (n.Data == "style" || n.Data == "noscript"):
			return
		case n.Type == html.TextNode:
			text += len(strings.TrimSpace(n.Data))
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	return scripts > 0 && links == 0 && text < substantialText
}
