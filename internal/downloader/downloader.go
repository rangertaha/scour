// SPDX-License-Identifier: GPL-3.0-or-later

// Package downloader fetches a page, wrapped in the middleware a job asked for.
//
// It is the first stage with a body of work behind it, and the first place the
// plugin seam is exercised by something that is not a test double.
//
// # What it is
//
// A core that does one HTTP request, and a chain around it. The core knows
// about timeouts, body limits and a user agent, because those are attributes of
// the downloader itself: there is no meaningful "off" for a request timeout and
// nowhere else it could be written. Everything that reorders, turns off, or
// could have been written by somebody else is a plugin: the cache, retries,
// redirects, a proxy.
//
// # What comes back
//
// A [Response] holds what the server sent, byte for byte. It is not decoded on
// the way in, because the cache holds these bytes and a corpus decoded on the
// way in has its mistakes baked in until somebody re-crawls. [Response.Text]
// decodes on demand, through the same function the spider uses when it reads a
// body back out of the cache. See [internal/decode] for why that is a function
// rather than a link in this chain.
//
// A status nobody wanted is still a response. A 404 comes back with its status
// and its body rather than as an error, because whether a 404 is a failure is
// the spider's decision and the `httperror` middleware is where it is made. An
// error from here means the fetch did not happen: no connection, no answer, a
// body over the limit, a context that ran out.
package downloader

import (
	"net/http"
	"time"

	"github.com/rangertaha/scour/internal/chain"
	"github.com/rangertaha/scour/internal/decode"
	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/plugin"
)

// Request is one thing to fetch.
//
// It is what the scheduler handed out, and what every downloader middleware
// sees on the way out.
type Request struct {
	// URL is what to fetch. Absolute, with a scheme.
	URL string

	// Method defaults to GET when empty.
	Method string

	// Header is sent as given. The user agent is filled in from the job unless
	// this already sets one, so a plugin may override it per request.
	Header http.Header

	// Body is what to send, for the requests that send anything.
	Body []byte

	// Job names the job this belongs to. Middleware that keeps anything per
	// job scopes it by this.
	Job string

	// Depth is how many links from a start URL this was found at. The spider's
	// `depth` middleware sets it; the downloader only carries it.
	Depth int
}

// Clone returns a copy safe to modify, which is what a middleware that changes
// a request does rather than editing the one it was handed. A retry that has
// already mutated the original has nothing to retry with.
func (r *Request) Clone() *Request {
	if r == nil {
		return nil
	}
	out := *r
	out.Header = r.Header.Clone()
	if r.Body != nil {
		out.Body = append([]byte(nil), r.Body...)
	}
	return &out
}

// Response is what came back.
type Response struct {
	// Request is what was asked for.
	Request *Request

	// URL is where the body actually came from, which differs from the
	// request's URL when something redirected.
	URL string

	// Status is the HTTP status code.
	Status int

	// Header is what the server sent back.
	Header http.Header

	// Body is what the server sent, byte for byte and still in whatever
	// encoding it arrived in. [Response.Text] decodes it.
	Body []byte

	// Fetched is when the body was fetched. For a cache hit that is when it
	// was originally fetched, not when it was read back, which is what makes
	// an age worth measuring.
	Fetched time.Time

	// Cached reports that this came from the cache rather than the network.
	Cached bool
}

// ContentType is what the server said the body was, which is the first and best
// evidence of its encoding.
func (r *Response) ContentType() string {
	if r == nil || r.Header == nil {
		return ""
	}
	return r.Header.Get("Content-Type")
}

// Text decodes the body to UTF-8.
//
// It decodes every time it is called rather than keeping the result, because a
// response is read once by the stage that wants it and holding both copies of
// every body doubles what a crawl carries in memory for no one's benefit.
//
// An undecodable body is not an error that loses the page: the best effort
// comes back alongside the problem, so a caller may log it and carry on. See
// [decode.Bytes].
func (r *Response) Text() ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	result, err := decode.Bytes(r.Body, r.ContentType())
	return result.Text, err
}

// OK reports whether the status is one that means the page arrived, which is
// what a caller wanting a body rather than a status asks.
func (r *Response) OK() bool { return r != nil && r.Status >= 200 && r.Status < 300 }

// The chain this stage runs. Named here so a middleware package writes
// downloader.Handler rather than three lines of type parameters.
type (
	// Handler fetches. The core is one, and so is every wrapping of it.
	Handler = chain.Handler[*Request, *Response]

	// Wrapper is what a middleware returns: the chain, replaced.
	Wrapper = chain.Wrapper[*Request, *Response]

	// Middleware builds one wrapper from its configuration.
	Middleware = plugin.Factory[*Request, *Response]

	// HandlerFunc adapts an ordinary function to [Handler], which is what
	// nearly every middleware wraps with.
	HandlerFunc = chain.Func[*Request, *Response]
)

// reg holds this stage's middleware. See [plugin] for how a name in a job
// document becomes something in here.
var reg = plugin.NewRegistry[*Request, *Response](engine.StageDownloader)

// Register adds a middleware, from an init function in its own package.
//
// A middleware is chosen by importing its package, the same way a cache backend
// is, which is what keeps a build that never wanted S3 from carrying its SDK.
func Register(name string, m Middleware) { reg.Register(name, m) }

// Registered lists what this build has, sorted. It is what a job naming
// something else is told about.
func Registered() []string { return reg.Names() }

// Has reports whether a middleware is registered.
func Has(name string) bool { return reg.Has(name) }

// normalise fills in what a request left out, so middleware and the core both
// see the same shape.
func (r *Request) normalise(agent string) *Request {
	out := r.Clone()
	if out.Method == "" {
		out.Method = http.MethodGet
	}
	if out.Header == nil {
		out.Header = http.Header{}
	}
	if out.Header.Get("User-Agent") == "" && agent != "" {
		out.Header.Set("User-Agent", agent)
	}
	return out
}
