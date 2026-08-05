// SPDX-License-Identifier: GPL-3.0-or-later

// Package spider turns a response into items and into new requests.
//
// It is the stage that closes the loop. Everything else moves work forwards;
// this is where a crawl finds more of it, and where the ranking that makes a
// focused crawl focused gets its input.
//
// # The chain wraps the parse
//
// A link sees the response on the way in and the result on the way back, which
// is what lets `httperror` refuse a page before anything parses it and lets a
// link that filters discovered links do so after they have been found. Low
// order is nearest the downloader that produced the response; high order is
// nearest the extraction itself.
//
// # What it does not decide
//
// Whether a discovered URL is worth fetching. The spider reports what a page
// points at; the scheduler decides what that means, because scope, budget,
// politeness and deduplication all live where the queue is. A spider that
// filtered on scope would be a second opinion about a rule that already has an
// owner.
package spider

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hashicorp/hcl/v2"

	"github.com/rangertaha/scour/internal/chain"
	"github.com/rangertaha/scour/internal/downloader"
	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/extract"
	"github.com/rangertaha/scour/internal/frontier"
	"github.com/rangertaha/scour/internal/plugin"
	"github.com/rangertaha/scour/internal/urls"
)

// Output is what one page produced.
type Output struct {
	// URL is the page, after any redirect: where the body actually came from,
	// because that is what its links are relative to.
	URL string

	// Depth is how far from a start URL this page was.
	Depth int

	// Spec is the fingerprint of the shape these items were read under. A
	// record attributed to the wrong shape is wrong in a way nothing
	// downstream can detect, so it travels with them.
	Spec string

	// Items are the shapes that were found.
	Items []*extract.Item

	// Links are the URLs this page points at, as requests ready for the
	// scheduler: one deeper than this page, and pointing back at it.
	Links []*frontier.Request
}

// The chain this stage runs.
type (
	// Handler reads a response into an output.
	Handler = chain.Handler[*downloader.Response, *Output]

	// Wrapper is what a middleware returns.
	Wrapper = chain.Wrapper[*downloader.Response, *Output]

	// Middleware builds one wrapper from its configuration.
	Middleware = plugin.Factory[*downloader.Response, *Output]

	// HandlerFunc adapts an ordinary function to [Handler].
	HandlerFunc = chain.Func[*downloader.Response, *Output]
)

// reg holds this stage's middleware.
var reg = plugin.NewRegistry[*downloader.Response, *Output](engine.StageSpider)

// Register adds a middleware, from an init function in its own package.
func Register(name string, m Middleware) { reg.Register(name, m) }

// Registered lists what this build has, sorted.
func Registered() []string { return reg.Names() }

// Has reports whether a middleware is registered.
func Has(name string) bool { return reg.Has(name) }

// Stage is a job's spider.
type Stage struct {
	job     string
	spec    *engine.Spec
	print   string
	items   *extract.Extractor
	handler Handler
	chain   *plugin.Chain[*downloader.Response, *Output]
	canon   urls.Options
}

// Options are what a caller supplies that the job document cannot.
type Options struct {
	// Eval resolves `secret()` in plugin configuration.
	Eval *hcl.EvalContext

	// Canon is how discovered links are made comparable. It should be the same
	// as the scheduler's, or the spider will report links in one spelling and
	// the frontier will dedupe them in another.
	Canon urls.Options
}

// New builds the spider a job configured.
//
// It takes a spec rather than a job, because that is all a spider needs: what
// shapes to extract. A spider somebody else wrote, subscribing to the bus in a
// language that is not Go, is handed the same thing as text.
func New(ctx context.Context, job *engine.Job, opts Options) (*Stage, error) {
	if job == nil {
		return nil, errors.New("spider: no job")
	}

	spec := job.Spec()
	items, err := extract.New(spec)
	if err != nil {
		return nil, fmt.Errorf("spider: job %q: %w", job.Name, err)
	}

	built, err := plugin.Build(ctx, reg, job, engine.StageSpider, opts.Eval)
	if err != nil {
		return nil, err
	}

	s := &Stage{
		job:   job.Name,
		spec:  spec,
		print: spec.Fingerprint(),
		items: items,
		chain: built,
		canon: opts.Canon,
	}
	s.handler = built.Handler(HandlerFunc(s.parse))
	return s, nil
}

// Handle implements [Handler]: one response through the whole chain.
func (s *Stage) Handle(ctx context.Context, resp *downloader.Response) (*Output, error) {
	if resp == nil {
		return nil, errors.New("spider: nothing to read")
	}
	return s.handler.Handle(ctx, resp)
}

// parse is the core the chain wraps.
func (s *Stage) parse(_ context.Context, resp *downloader.Response) (*Output, error) {
	// Decoded here rather than by the downloader, because the cache holds what
	// the server sent and two different readers turn it into text. See
	// [internal/decode] for why that is a function rather than a link.
	text, err := resp.Text()
	if err != nil {
		// Not a failure: an undecodable page is still mostly readable once the
		// unmappable bytes are replacement characters, and dropping it would
		// lose a page to make a point. The best effort came back alongside the
		// problem.
		text, _ = resp.Text()
	}

	result, err := s.items.Page(resp.URL, text)
	if err != nil {
		return nil, err
	}

	depth := 0
	if resp.Request != nil {
		depth = resp.Request.Depth
	}

	out := &Output{
		URL:   result.URL,
		Depth: depth,
		Spec:  s.print,
		Items: result.Items,
	}

	discovered := time.Now().UTC()
	for _, link := range result.Links {
		normalised, err := urls.Normalise(link, s.canon)
		if err != nil {
			// A link this cannot fetch is not a link. The page is entitled to
			// contain one and it is not worth reporting.
			continue
		}
		out.Links = append(out.Links, &frontier.Request{
			URL:        normalised,
			Host:       urls.Host(normalised),
			Hash:       urls.Hash(normalised),
			Depth:      depth + 1,
			Parent:     result.URL,
			Discovered: discovered,
		})
	}
	return out, nil
}

// Spec is what this spider extracts, which is what travels to one written in
// another language.
func (s *Stage) Spec() *engine.Spec { return s.spec }

// Fingerprint identifies the shape items are read under.
func (s *Stage) Fingerprint() string { return s.print }

// Middleware lists the chain in the order it runs.
func (s *Stage) Middleware() []string { return s.chain.Names() }

// Close releases what the middleware opened.
func (s *Stage) Close() error { return s.chain.Close() }

// Found is how many items an output holds, which is the number a run reports.
func (o *Output) Found() int { return len(o.Items) }

// Complete reports whether every item found every required property.
func (o *Output) Complete() bool {
	for _, item := range o.Items {
		if !item.Complete() {
			return false
		}
	}
	return true
}
