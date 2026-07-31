// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/crawl"
	"github.com/rangertaha/scour/internal/service"
)

// busCrawler puts the bus between the crawler and the database.
//
// It starts an embedded broker and the store service in this process, so a
// single-process run behaves like a distributed one without anything to
// install. The returned function waits for the writer to catch up, and must be
// called before reading back what the crawl produced.
func (a *App) BusCrawler(ctx context.Context, crawler *crawl.Crawler, item string) (*crawl.Crawler, func() error, error) {
	s, err := a.Store()
	if err != nil {
		return nil, nil, err
	}

	b, err := bus.Open(ctx, bus.Options{URL: a.Cfg.Bus.URL, Name: "scour-crawl"})
	if err != nil {
		return nil, nil, err
	}

	// The store service runs alongside the crawl rather than after it, so
	// writes happen while pages are still being fetched.
	serviceCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- service.New(service.NewStore(b, s)).Run(serviceCtx)
	}()

	var settled bool
	settle := func() error {
		if settled {
			return nil
		}
		settled = true

		defer func() {
			stop()
			<-done
			b.Close()
		}()

		if err := b.Flush(); err != nil {
			return err
		}

		drainCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := b.Drain(drainCtx, bus.StreamCrawl); err != nil {
			return fmt.Errorf("waiting for the store service: %w", err)
		}
		return nil
	}

	return crawler.
		WithSink(service.NewBusSink(b, item)).
		WithMeter(service.NewBusMeter(b, item)), settle, nil
}
