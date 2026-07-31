// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"context"
	"testing"
	"time"
)

func TestHostTransportSurvivesTheCrawlThatFoundIt(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	if got, err := s.HostsByTransport(ctx, "webdriver"); err != nil || len(got) != 0 {
		t.Fatalf("HostsByTransport on a fresh store = %v, %v", got, err)
	}

	if err := s.SetHostTransport(ctx, "app.example.com", "webdriver"); err != nil {
		t.Fatal(err)
	}
	// Recording the same host twice must not duplicate it, since the same host
	// can be escalated by more than one crawl.
	if err := s.SetHostTransport(ctx, "app.example.com", "webdriver"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetHostTransport(ctx, "plain.example.com", "http"); err != nil {
		t.Fatal(err)
	}

	got, err := s.HostsByTransport(ctx, "webdriver")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "app.example.com" {
		t.Errorf("hosts needing a browser = %v", got)
	}

	if which, err := s.HostTransport(ctx, "app.example.com"); err != nil || which != "webdriver" {
		t.Errorf("HostTransport = %q, %v", which, err)
	}
	if which, err := s.HostTransport(ctx, "unknown.example.com"); err != nil || which != "" {
		t.Errorf("an unknown host should be empty, got %q, %v", which, err)
	}
}

// The host row carries the whole of a host's policy, so writing one field of
// it must not blank the others.
func TestRecordingATransportKeepsTheRestOfTheHostPolicy(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	existing := Host{Host: "app.example.com", Rate: 5 * time.Second, Concurrency: 2}
	if err := s.db.WithContext(ctx).Create(&existing).Error; err != nil {
		t.Fatal(err)
	}

	if err := s.SetHostTransport(ctx, "app.example.com", "webdriver"); err != nil {
		t.Fatal(err)
	}

	var rows []Host
	if err := s.db.WithContext(ctx).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("hosts = %+v, want one row", rows)
	}
	if rows[0].Transport != "webdriver" {
		t.Errorf("transport = %q", rows[0].Transport)
	}
	if rows[0].Rate != 5*time.Second || rows[0].Concurrency != 2 {
		t.Errorf("the politeness settings were lost: %+v", rows[0])
	}
}
