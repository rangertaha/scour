// SPDX-License-Identifier: GPL-3.0-or-later

package service

import (
	"context"

	"github.com/rangertaha/scour/internal/bus"
)

// BusMeter publishes measurements to the broker.
//
// It is the crawl package's Meter satisfied by the bus, the same way BusSink is
// its Sink: the crawler measures things and does not know that anyone is
// listening, which is what keeps observability out of the crawl's own concerns.
type BusMeter struct {
	bus  *bus.Bus
	item string
}

// NewBusMeter returns a meter that publishes for one item.
func NewBusMeter(b *bus.Bus, item string) *BusMeter {
	return &BusMeter{bus: b, item: item}
}

// Measure implements crawl.Meter. It cannot fail: Emit is fire and forget, so a
// broker that is slow or gone costs a debug line and nothing else.
func (m *BusMeter) Measure(ctx context.Context, name string, value float64, unit string, labels map[string]string) {
	m.bus.Emit(ctx, m.item, bus.Metric{
		Name:   name,
		Value:  value,
		Unit:   unit,
		Labels: labels,
	})
}
