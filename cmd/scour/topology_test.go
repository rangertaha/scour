// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// frontier is the part of a crawl's outcome that must not depend on how the
// components were wired together.
type frontier struct {
	URL         string
	Depth       int
	Status      string
	StatusCode  int
	ContentType string
	Size        int64
	HasParent   bool
}

// readFrontier runs `scour crawl --json` and reduces it to the fields that
// carry meaning. Timings and ids are left out: latency is wall-clock and row
// ids depend on insertion order, neither of which is a difference in what was
// crawled.
func readFrontier(t *testing.T, dir string) []frontier {
	t.Helper()

	out := runOK(t, dir, "crawl", "vehicle", "--json")
	var rows []struct {
		URL         string
		Depth       int
		Status      string
		StatusCode  int
		ContentType string
		Size        int64
		ParentID    *uint
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("decode frontier: %v\n%s", err, out)
	}

	got := make([]frontier, 0, len(rows))
	for _, r := range rows {
		got = append(got, frontier{
			URL:         r.URL,
			Depth:       r.Depth,
			Status:      r.Status,
			StatusCode:  r.StatusCode,
			ContentType: r.ContentType,
			Size:        r.Size,
			HasParent:   r.ParentID != nil,
		})
	}
	sort.Slice(got, func(i, j int) bool { return got[i].URL < got[j].URL })
	return got
}

// TestTopologyEquivalence is the requirement M5 was made conditional on: the
// same crawl, wired directly or through the bus, must leave the database in
// the same state. Without it the bus is a second implementation to keep in
// step by hand, which is exactly what the sink seam exists to avoid.
func TestTopologyEquivalence(t *testing.T) {
	srv := carSite(t)

	direct := crawlDir(t)
	runOK(t, direct, "add", "vehicle", "--alias", "car", "-u", srv.URL+"/")
	runOK(t, direct, "add", "vehicle", "-p", "make", "-e", "Ford")
	runOK(t, direct, "crawl", "vehicle", "--depth", "5")

	viaBus := crawlDir(t)
	runOK(t, viaBus, "add", "vehicle", "--alias", "car", "-u", srv.URL+"/")
	runOK(t, viaBus, "add", "vehicle", "-p", "make", "-e", "Ford")
	out := runOK(t, viaBus, "crawl", "vehicle", "--depth", "5", "--bus")

	if strings.Contains(out, "0 fetched") {
		t.Fatalf("the bus crawl fetched nothing:\n%s", out)
	}

	want := readFrontier(t, direct)
	got := readFrontier(t, viaBus)

	if len(want) != len(got) {
		t.Fatalf("direct crawled %d pages, the bus crawled %d\ndirect: %+v\nbus:    %+v",
			len(want), len(got), want, got)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Errorf("row %d differs between topologies:\ndirect: %+v\nbus:    %+v", i, want[i], got[i])
		}
	}
}

// The bus path must survive the pipeline it depends on being interrupted, and
// a redelivery must not double-write, which is what makes at-least-once
// delivery safe to build on.
func TestBusWritesAreIdempotent(t *testing.T) {
	srv := carSite(t)
	dir := crawlDir(t)

	runOK(t, dir, "add", "vehicle", "--alias", "car", "-u", srv.URL+"/")
	runOK(t, dir, "crawl", "vehicle", "--depth", "5", "--bus")
	first := readFrontier(t, dir)

	// Crawling again publishes the same pages a second time. The writes key on
	// the URL hash, so the frontier must not grow.
	runOK(t, dir, "crawl", "vehicle", "--reset", "--depth", "5", "--bus")
	second := readFrontier(t, dir)

	if len(first) != len(second) {
		t.Errorf("a second bus crawl changed the frontier from %d rows to %d", len(first), len(second))
	}
}

func TestBusCrawlReportsWhatItWrote(t *testing.T) {
	srv := carSite(t)
	dir := crawlDir(t)

	runOK(t, dir, "add", "vehicle", "-u", srv.URL+"/")
	out := runOK(t, dir, "crawl", "vehicle", "--depth", "5", "--bus")

	// The summary is read back from the database after the writer has caught
	// up. Printing it before would show an empty frontier that fills in a
	// moment later, which is the classic distributed-pipeline bug.
	if !strings.Contains(out, "PROBABILITY") {
		t.Errorf("the bus crawl printed no summary table:\n%s", out)
	}
	if !strings.Contains(out, "/cars/") {
		t.Errorf("the summary is missing pages the crawl fetched:\n%s", out)
	}
}

func TestRunRejectsUnknownRoles(t *testing.T) {
	dir := crawlDir(t)
	if _, err := run(t, dir, "run", "--role", "nonsense"); err == nil {
		t.Error("an unknown role must be rejected rather than starting fewer components")
	}
}
