// SPDX-License-Identifier: GPL-3.0-or-later

package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/content"
	"github.com/rangertaha/scour/internal/crawl"
	crawlqueue "github.com/rangertaha/scour/internal/crawl/queue"
	"github.com/rangertaha/scour/internal/score"
	"github.com/rangertaha/scour/internal/store"
)

// CrawlService fetches the URLs the store hands out.
//
// It holds no state about an item. What is in scope, what has been visited
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
	// work is what the colly loops run on, and it deliberately outlives the
	// context that stops the service. See [CrawlService.Start].
	work context.Context //nolint:containedctx // deliberate, see Start

	// fetched is every page this process has fetched, for every item. It is
	// what this node's throughput is differenced from, so it counts across
	// items rather than per item: a node is a machine, and a machine has one
	// rate however many things it is hunting.
	fetched atomic.Int64
}

// NewCrawl returns the crawl service. The content types and browser policy come
// from this process's configuration rather than from the item, because a
// stateless crawler cannot read the item and these are properties of the
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
	// The colly loops run on a context of their own, which outlives the one
	// that stops this service.
	//
	// Stopping a loop is what closing its feed does, not what cancelling a
	// context does, and the difference is everything a drain is for. Sharing
	// one context threw away the pages the crawler was in the middle of: the
	// fetch finished, the publish failed with "context canceled", and the URL
	// went back to the frontier to be fetched all over again by somebody else.
	// `scour node leave` is supposed to cost nothing, and that cost every page
	// in flight.
	//
	// Bounded all the same, because a crawler stuck on a host that never
	// answers must not be able to hold the process open for ever.
	work, giveUp := context.WithCancel(context.WithoutCancel(ctx))
	defer giveUp()
	c.mu.Lock()
	c.work = work
	c.mu.Unlock()

	stop, err := c.bus.Consume(ctx, bus.StreamCrawl, "crawl-work",
		bus.AllItems(bus.SubjectWork), c.handleWork)
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

	patience := time.AfterFunc(drainGrace, giveUp)
	defer patience.Stop()
	c.wg.Wait()
	return nil
}

// drainGrace is how long the fetches already running have to finish and report
// once the service has been told to stop.
//
// Long enough for a slow page and the write that follows it, short enough that
// a crawler nobody can rescue is not the reason a machine will not reboot.
const drainGrace = 30 * time.Second

// handleWork hands one URL to the colly loop for its item.
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
	if len(ev.Request) == 0 || ev.ItemID == 0 {
		return nil
	}

	feed, err := c.feedFor(ctx, ev)
	if err != nil {
		return err
	}
	feed.Offer(ev.Request)
	return nil
}

// feedFor returns the queue for an item, starting its crawl loop the first
// time work arrives for it.
func (c *CrawlService) feedFor(ctx context.Context, ev bus.Work) (*crawlqueue.Work, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if feed, ok := c.feeds[ev.ItemID]; ok {
		return feed, nil
	}

	feed := crawlqueue.NewWork()
	c.feeds[ev.ItemID] = feed

	// The item is reconstructed from the message rather than read: a crawler
	// does not touch the database, and its id and name are all the crawl needs.
	item := &store.Item{ID: ev.ItemID, Name: ev.Item}
	crawler := c.crawler.
		WithSink(NewBusSink(c.bus, ev.Item).Counting(&c.fetched)).
		WithMeter(NewBusMeter(c.bus, ev.Item))

	// The loop's own context, not the message's: this goroutine outlives the
	// delivery that started it, and it has to outlive the stop signal too.
	work := c.work
	if work == nil {
		work = ctx
	}

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		_, err := crawler.Run(work, crawl.Options{
			Item:     item,
			Types:    c.types,
			Browser:  c.browser,
			Scorer:   score.Fixed(1),
			Frontier: feed,
		})
		if err != nil && work.Err() == nil {
			slog.Error("crawl loop stopped", "item", ev.Item, "err", err)
		}
	}()

	slog.Info("crawling for a new item", "item", ev.Item)
	return feed, nil
}
