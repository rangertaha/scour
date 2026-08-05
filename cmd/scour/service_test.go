// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/entity"
	"github.com/rangertaha/scour/internal/event"
)

// `scour service` is the only way to reach the entity graph and the event
// store, so if it does not start, they are built and unreachable: the class
// this repository has retired three times and found a fourth instance of.
//
// The process is the test. Everything under it has its own tests, and what
// nothing else covers is that a document on disk turns into two services
// answering on a bus that a separate process can talk to.

// TestTheServiceAnswersOnTheBus.
//
// It starts the command, connects to the embedded bus it prints, and asks both
// stores for something through the real clients. Nothing here reads a database
// file: what matters is that a caller who knows only the address gets answers.
func TestTheServiceAnswersOnTheBus(t *testing.T) {
	dir := t.TempDir()

	path := filepath.Join(dir, "service.hcl")
	if err := os.WriteFile(path, []byte(`
entity {
  dir = "`+filepath.Join(dir, "graph")+`"
}

event {
  dir = "`+filepath.Join(dir, "events")+`"
}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := start(t, dir, "service", path)

	address := waitFor(t, cmd, "listening on ")
	conn, err := bus.Connect(bus.Options{URL: address, Name: "service-test"})
	if err != nil {
		t.Fatalf("connecting to %q: %v", address, err)
	}
	defer conn.Close()

	ctx := context.Background()
	said := entity.Provenance{Job: "news", URL: "https://example.com/a"}

	graph := conn.NewEntities(20 * time.Second)
	id, err := graph.Assert(ctx, "person", "Alex Doe", said)
	if err != nil {
		t.Fatalf("assert: %v", err)
	}
	if err := graph.Describe(ctx, id, "role", "correspondent", said); err != nil {
		t.Fatalf("describe: %v", err)
	}

	kinds, err := graph.Kinds(ctx)
	if err != nil {
		t.Fatalf("kinds: %v", err)
	}
	if len(kinds) != 1 || kinds[0].Name != "person" || kinds[0].Entities != 1 {
		t.Errorf("kinds = %+v, want one person", kinds)
	}

	props, err := graph.Properties(ctx, id)
	if err != nil {
		t.Fatalf("properties: %v", err)
	}
	if len(props) != 1 || props[0].Value != "correspondent" {
		t.Errorf("properties = %+v", props)
	}

	events := conn.NewEvents(20 * time.Second)
	when := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)
	if _, err := events.Put(ctx, event.Event{
		Name: "price", Tags: map[string]string{"company": "acme"},
		Fields: map[string]string{"value": "9.99"},
		At:     when, Job: "markets", URL: "https://example.com/acme",
	}); err != nil {
		t.Fatalf("put: %v", err)
	}

	series, err := events.Names(ctx)
	if err != nil {
		t.Fatalf("names: %v", err)
	}
	if len(series) != 1 || series[0].Name != "price" || series[0].Events != 1 {
		t.Errorf("series = %+v", series)
	}
}

// TestTheServiceKeepsWhatItWasGiven.
//
// A store that vanished when the process did would be one every writer believed
// it had written to, which is why `dir` is required. This asserts the promise
// rather than the requirement: stop the service, start it again, and the graph
// is still there.
func TestTheServiceKeepsWhatItWasGiven(t *testing.T) {
	dir := t.TempDir()

	path := filepath.Join(dir, "service.hcl")
	if err := os.WriteFile(path, []byte(`
entity {
  dir = "`+filepath.Join(dir, "graph")+`"
}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	first := start(t, dir, "service", path)
	address := waitFor(t, first, "listening on ")

	conn, err := bus.Connect(bus.Options{URL: address, Name: "keeps-test"})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := conn.NewEntities(20*time.Second).Assert(ctx, "person", "Alex Doe",
		entity.Provenance{Job: "news", URL: "https://example.com/a"}); err != nil {
		t.Fatalf("assert: %v", err)
	}
	conn.Close()
	first.stop(t)

	// Again, against the same directory.
	second := start(t, dir, "service", path)
	address = waitFor(t, second, "listening on ")

	conn, err = bus.Connect(bus.Options{URL: address, Name: "keeps-test-2"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	found, err := conn.NewEntities(20*time.Second).Find(ctx, "person", "Alex Doe")
	if err != nil {
		t.Fatalf("the graph did not survive a restart: %v", err)
	}
	if found.Name != "Alex Doe" {
		t.Errorf("found %+v", found)
	}
}

// TestAServiceDocumentIsNotAJobDocument.
//
// The two are different files answering different questions, and reading one as
// the other has to say so by name rather than starting nothing and looking
// fine.
func TestAServiceDocumentIsNotAJobDocument(t *testing.T) {
	dir := t.TempDir()

	path := filepath.Join(dir, "job.hcl")
	if err := os.WriteFile(path, []byte(`
job "news" {
  domains = ["example.com"]
  start   = ["https://example.com/"]

  item "article" {
    property "title" {
      type = str
    }
  }
}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	got := scour(t, dir, "service", path)
	if got.code == 0 {
		t.Fatalf("a job document was accepted as a service document:\n%s%s", got.stdout, got.stderr)
	}
	if !strings.Contains(got.stderr, "entity") && !strings.Contains(got.stderr, "event") {
		t.Errorf("the message does not say what a service document holds: %s", got.stderr)
	}
}
