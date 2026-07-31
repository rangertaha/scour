// SPDX-License-Identifier: GPL-3.0-or-later

package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/content"
	"github.com/rangertaha/scour/internal/crawl"
	crawlqueue "github.com/rangertaha/scour/internal/crawl/queue"
	"github.com/rangertaha/scour/internal/score"
	"github.com/rangertaha/scour/internal/store"
)

// CrawlService fetches the URLs the store hands out.
//
// It holds no state about an entity. What is in scope, what has been visited
// and what is worth fetching next are all decided by the store, which is the
// component with the targets and the frontier. A crawler is therefore
// interchangeable with any other, and losing one costs the lease on whatever it
// was holding.
type CrawlService struct {
	bus     *bus.Bus
	crawler *crawl.Crawler
	types   *content.Set
	browser string

	mu    sync.Mutex
	feeds map[uint]*crawlqueue.Work
	stop  []func()
	wg    sync.WaitGroup
}

// NewCrawl returns the crawl service. The content types and browser policy come
// from this process's configuration rather than from the entity, because a
// stateless crawler cannot read the entity and these are properties of the
// machine doing the fetching.
func NewCrawl(b *bus.Bus, c *crawl.Crawler, types *content.Set, browser string) *CrawlService {
	return &CrawlService{
		bus: b, crawler: c, types: types, browser: browser,
		feeds: map[uint]*crawlqueue.Work{},
	}
}

// Role implements [Service].
func (c *CrawlService) Role() Role { return RoleCrawl }

// Start implements [Service]. It consumes handed-out work until ctx is
// cancelled.
//
// Every crawler shares one durable consumer, so the broker gives each URL to
// exactly one of them and a URL whose crawler dies is redelivered. Adding a
// second process is then the whole of scaling out.
func (c *CrawlService) Start(ctx context.Context) error {
	stop, err := c.bus.Consume(ctx, bus.StreamCrawl, "crawl-work",
		bus.AllEntities(bus.SubjectWork), c.handleWork)
	if err != nil {
		return err
	}
	defer stop()

	<-ctx.Done()

	// Closing every feed ends the colly loop behind it, which is what lets the
	// in-flight requests finish rather than being cut off.
	c.mu.Lock()
	for _, f := range c.feeds {
		f.Close()
	}
	stops := c.stop
	c.mu.Unlock()
	for _, s := range stops {
		s()
	}
	c.wg.Wait()
	return nil
}

// handleWork hands one URL to the colly loop for its entity.
//
// The message is acknowledged once the request is queued rather than once it is
// fetched. colly owns retries, redirects and the browser escalation from that
// point, and holding the acknowledgement across all of it would exceed the
// broker's ack deadline on any slow page. The frontier lease is what covers a
// crawler dying after this point: nothing reports the fetch, so the URL returns.
func (c *CrawlService) handleWork(ctx context.Context, data []byte) error {
	var ev bus.Work
	if err := json.Unmarshal(data, &ev); err != nil {
		return nil //nolint:nilerr // deliberate: poison message
	}
	if len(ev.Request) == 0 || ev.EntityID == 0 {
		return nil
	}

	feed, err := c.feedFor(ctx, ev)
	if err != nil {
		return err
	}
	feed.Offer(ev.Request)
	return nil
}

// feedFor returns the queue for an entity, starting its crawl loop the first
// time work arrives for it.
func (c *CrawlService) feedFor(ctx context.Context, ev bus.Work) (*crawlqueue.Work, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if feed, ok := c.feeds[ev.EntityID]; ok {
		return feed, nil
	}

	feed := crawlqueue.NewWork()
	c.feeds[ev.EntityID] = feed

	// The entity is reconstructed from the message rather than read: a crawler
	// does not touch the database, and its id and name are all the crawl needs.
	entity := &store.Entity{ID: ev.EntityID, Name: ev.Entity}
	crawler := c.crawler.
		WithSink(NewBusSink(c.bus, ev.Entity)).
		WithMeter(NewBusMeter(c.bus, ev.Entity))

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		_, err := crawler.Run(ctx, crawl.Options{
			Entity:   entity,
			Types:    c.types,
			Browser:  c.browser,
			Scorer:   score.Fixed(1),
			Frontier: feed,
		})
		if err != nil && ctx.Err() == nil {
			slog.Error("crawl loop stopped", "entity", ev.Entity, "err", err)
		}
	}()

	slog.Info("crawling for a new entity", "entity", ev.Entity)
	return feed, nil
}
