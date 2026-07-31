// SPDX-License-Identifier: GPL-3.0-or-later

package crawl

import (
	"context"
	"net/url"
	"strconv"
	"time"

	"github.com/rangertaha/scour/internal/bus"
)

// Meter records measurements taken while crawling.
//
// Declared here because this package is the consumer, and kept to one method so
// that adding a measurement never means changing an interface. Every call is
// fire and forget: an implementation must not return an error, block, or fail
// the crawl. Observability that can break the thing it observes is worse than
// none.
type Meter interface {
	Measure(ctx context.Context, name string, value float64, unit string, labels map[string]string)
}

// NopMeter measures nothing. It is the default, so a crawl with no observer
// costs one interface call per measurement and no branches at the call sites.
type NopMeter struct{}

// Measure implements [Meter].
func (NopMeter) Measure(context.Context, string, float64, string, map[string]string) {}

// WithMeter returns a crawler that reports what it measures.
func (c *Crawler) WithMeter(m Meter) *Crawler {
	clone := *c
	clone.meter = m
	return &clone
}

// measure reports one measurement, tolerating a crawler built without a meter.
func (c *Crawler) measure(ctx context.Context, name string, value float64, unit string, labels map[string]string) {
	if c.meter == nil {
		return
	}
	c.meter.Measure(ctx, name, value, unit, labels)
}

// measureFetch reports what one fetch cost.
//
// Latency, size and status together are what say whether a crawl is healthy:
// a rising latency means a site is straining, a falling size means pages are
// being served differently than expected, and the status distribution is where
// a block shows up first.
func (c *Crawler) measureFetch(ctx context.Context, entity, rawURL string, status int, latency time.Duration, size int64) {
	if c.meter == nil {
		return
	}
	labels := map[string]string{
		"host":   hostOf(rawURL),
		"status": strconv.Itoa(status),
	}
	c.measure(ctx, bus.MetricFetchLatency, float64(latency.Milliseconds()), "ms", labels)
	c.measure(ctx, bus.MetricFetchStatus, 1, "count", labels)
	if size > 0 {
		c.measure(ctx, bus.MetricFetchBytes, float64(size), "bytes", labels)
	}
}

// hostOf is the label a per-site measurement is grouped by. A URL that will not
// parse is measured without one rather than not measured.
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
