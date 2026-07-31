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
	bus    *bus.Bus
	entity string
}

// NewBusMeter returns a meter that publishes for one entity.
func NewBusMeter(b *bus.Bus, entity string) *BusMeter {
	return &BusMeter{bus: b, entity: entity}
}

// Measure implements crawl.Meter. It cannot fail: Emit is fire and forget, so a
// broker that is slow or gone costs a debug line and nothing else.
func (m *BusMeter) Measure(ctx context.Context, name string, value float64, unit string, labels map[string]string) {
	m.bus.Emit(ctx, m.entity, bus.Metric{
		Name:   name,
		Value:  value,
		Unit:   unit,
		Labels: labels,
	})
}
