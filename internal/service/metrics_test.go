package service

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/store"
)

// Measurements must reach a subscriber without the pipeline waiting on one, and
// several subscribers must each see them: a work queue would let one dashboard
// consume a metric out from under every other.
func TestMetricsReachSubscribers(t *testing.T) {
	ctx := context.Background()
	b, err := bus.Open(ctx, bus.Options{Name: "metrics-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	db, err := store.Open(filepath.Join(t.TempDir(), "scour.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	seen := make(chan bus.Metric, 32)
	stop, err := b.Consume(ctx, bus.StreamMetrics, "watcher",
		bus.AllItems(bus.SubjectMetric), func(_ context.Context, data []byte) error {
			var m bus.Metric
			if err := json.Unmarshal(data, &m); err != nil {
				return err
			}
			seen <- m
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	m := NewBusMeter(b, "news")
	m.Measure(ctx, bus.MetricFetchLatency, 250, "ms",
		map[string]string{"host": "example.com", "status": "200"})

	select {
	case got := <-seen:
		if got.Name != bus.MetricFetchLatency || got.Value != 250 {
			t.Errorf("got %+v", got)
		}
		if got.Item != "news" {
			t.Errorf("item = %q, want news", got.Item)
		}
		if got.Labels["host"] != "example.com" {
			t.Errorf("labels = %v", got.Labels)
		}
		if got.At.IsZero() {
			t.Error("At was not stamped")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no measurement arrived")
	}
}

// Observability must not be able to break the thing it observes. A closed bus
// is the ordinary way for that to happen, and it must cost nothing.
func TestMeasuringOnADeadBusIsHarmless(t *testing.T) {
	ctx := context.Background()
	b, err := bus.Open(ctx, bus.Options{Name: "dead-bus-test"})
	if err != nil {
		t.Fatal(err)
	}
	m := NewBusMeter(b, "news")
	b.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 100 {
			m.Measure(ctx, bus.MetricFetchLatency, 1, "ms", nil)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("measuring blocked once the bus was gone")
	}
}
