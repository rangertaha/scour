// SPDX-License-Identifier: GPL-3.0-or-later

package nats_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/exporter"
	"github.com/rangertaha/scour/internal/exporter/exportertest"
	"github.com/rangertaha/scour/internal/record"

	natsexport "github.com/rangertaha/scour/internal/exporter/nats"
)

var fetched = time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

// wait is how long a test may wait for a message that should already be there.
//
// Generous on purpose. These are wall-clock waits, and the whole suite runs
// several packages at once, two of which also start an embedded NATS: a bound
// tight enough to be "fast" is a bound that fails on a loaded machine and reads
// as flakiness rather than as the timing it is. Five seconds failed exactly
// once, in a full-suite run, and never on its own.
//
// A test that asserts something did NOT arrive uses [never] instead, which has
// to be short for the opposite reason.
const wait = 30 * time.Second

// never is how long to wait before concluding nothing is coming. Short, because
// every one of these is time the suite spends proving a negative.
const never = 200 * time.Millisecond

// serving starts an embedded server for one test.
//
// Its own store directory, always. Two embedded servers left to NATS's own
// default share one, so they would silently see each other's JetStream state,
// and a test that reads another test's messages fails in a way that reads as
// flakiness rather than as a bug.
func serving(t *testing.T) *bus.Conn {
	t.Helper()

	conn, err := bus.Connect(bus.Options{StoreDir: t.TempDir(), Name: t.Name()})
	if err != nil {
		t.Fatalf("bus: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

const items = `
job "markets" {
  start = ["https://example.com/"]

  item "headline" {
    property "title" {
      type = str
    }
  }

  item "price" {
    of   = "company"
    time = "observed"

    # Which company. "of" names the kind and this names the one, which is
    # what makes the measurement price,company=acme rather than a price of
    # nothing in particular.
    property "company" {
      entity = "company"
    }

    property "value" {
      type = float
    }

    property "observed" {
      type = date
    }
  }
`

func job(t *testing.T, blocks string) *engine.Job {
	t.Helper()

	doc, err := engine.Parse([]byte(items+blocks+"\n}\n"), "job.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return doc.Jobs[0]
}

func headline(url, title string) *record.Record {
	return &record.Record{
		Item: "headline", URL: url, Spec: "abc123", Fetched: fetched,
		Values: map[string]string{"title": title},
	}
}

func price(url, company, value string) *record.Record {
	return &record.Record{
		Item: "price", URL: url, Spec: "abc123", Fetched: fetched,
		Values: map[string]string{"company": company, "value": value, "observed": "2026-08-04T09:15:00Z"},
	}
}

// publish builds the exporters, writes the records and closes, which is a whole
// run as far as an exporter is concerned.
func publish(t *testing.T, j *engine.Job, records ...*record.Record) {
	t.Helper()

	set, err := exporter.New(context.Background(), j, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := set.Write(context.Background(), records...); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := set.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// measurement reads what was published back.
func measurement(t *testing.T, data []byte) record.Measurement {
	t.Helper()

	var m record.Measurement
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("published something that is not a measurement: %v\n%s", err, data)
	}
	return m
}

// TestAHeadlineHappensOnceAndGoesToOneSubject, so everything listening for
// headlines subscribes to one thing rather than to a wildcard it has to filter.
func TestAHeadlineHappensOnceAndGoesToOneSubject(t *testing.T) {
	conn := serving(t)

	sub, err := conn.SubscribeSync("events.news.headline")
	if err != nil {
		t.Fatal(err)
	}

	publish(t, job(t, `
  exporter "nats" "headline" {
    subject = "events.news.headline"
    url     = "`+conn.Address()+`"
  }
`),
		headline("https://example.com/a", "One"),
		headline("https://example.com/b", "Two"))

	for _, want := range []string{"One", "Two"} {
		msg, err := sub.NextMsg(wait)
		if err != nil {
			t.Fatalf("waiting for %q: %v", want, err)
		}
		if msg.Subject != "events.news.headline" {
			t.Errorf("subject = %q, want the one it was given", msg.Subject)
		}

		m := measurement(t, msg.Data)
		if m.Name != "headline" {
			t.Errorf("name = %q", m.Name)
		}
		if m.Fields["title"] != want {
			t.Errorf("fields = %v, want title %q", m.Fields, want)
		}
		if len(m.Tags) != 0 {
			t.Errorf("an item that observes nothing was given tags: %v", m.Tags)
		}
		// The moment of observation is all there is when the item declares no
		// time of its own.
		if !m.Time.Equal(fetched) {
			t.Errorf("time = %s, want the fetch time", m.Time)
		}
	}
}

// TestAPriceGetsOneSubjectPerCompany, which is what lets a consumer subscribe to
// one company rather than filter the firehose.
func TestAPriceGetsOneSubjectPerCompany(t *testing.T) {
	conn := serving(t)

	one, err := conn.SubscribeSync("events.markets.price.acme")
	if err != nil {
		t.Fatal(err)
	}
	all, err := conn.SubscribeSync("events.markets.price.*")
	if err != nil {
		t.Fatal(err)
	}

	publish(t, job(t, `
  exporter "nats" "price" {
    subject = "events.markets.price"
    url     = "`+conn.Address()+`"
  }
`),
		price("https://example.com/acme", "acme", "178.23"),
		price("https://example.com/globex", "globex", "12.10"))

	msg, err := one.NextMsg(wait)
	if err != nil {
		t.Fatalf("the company's own subject received nothing: %v", err)
	}
	m := measurement(t, msg.Data)
	if m.Tags["company"] != "acme" || m.Fields["value"] != "178.23" {
		t.Errorf("measurement = %+v", m)
	}

	// Event time, never ingest time.
	if !m.Time.Equal(time.Date(2026, 8, 4, 9, 15, 0, 0, time.UTC)) {
		t.Errorf("time = %s, want the item's own", m.Time)
	}

	// The other company must not be on this subject, or subscribing to one
	// company would be subscribing to all of them.
	if extra, err := one.NextMsg(never); err == nil {
		t.Errorf("another company arrived on this company's subject: %s", extra.Data)
	}

	// And both are still there for whoever wants the lot.
	subjects := map[string]bool{}
	for range 2 {
		msg, err := all.NextMsg(wait)
		if err != nil {
			t.Fatalf("the wildcard received %d: %v", len(subjects), err)
		}
		subjects[msg.Subject] = true
	}
	for _, want := range []string{"events.markets.price.acme", "events.markets.price.globex"} {
		if !subjects[want] {
			t.Errorf("nothing was published to %s: got %v", want, subjects)
		}
	}
}

// TestCloseFlushesOrARunThatEndsPublishesNothing: the client buffers, so a crawl
// that finishes immediately after its last record would otherwise exit with that
// record still in the buffer and deliver none of it.
func TestCloseFlushesOrARunThatEndsPublishesNothing(t *testing.T) {
	conn := serving(t)

	sub, err := conn.SubscribeSync("events.news.headline")
	if err != nil {
		t.Fatal(err)
	}

	set, err := exporter.New(context.Background(), job(t, `
  exporter "nats" "headline" {
    subject = "events.news.headline"
    url     = "`+conn.Address()+`"
  }
`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := set.Write(context.Background(), headline("https://example.com/a", "One")); err != nil {
		t.Fatal(err)
	}
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}

	// Close has returned, so the server has the record. A round trip on this
	// connection is all that stands between the server having it and this
	// subscription holding it, and after that no waiting should be needed.
	if err := conn.Flush(); err != nil {
		t.Fatal(err)
	}
	msg, err := sub.NextMsg(wait)
	if err != nil {
		t.Fatalf("nothing was published by the time the run ended: %v", err)
	}
	if m := measurement(t, msg.Data); m.Fields["title"] != "One" {
		t.Errorf("measurement = %+v", m)
	}
}

// TestAnItemWithNoShapeStillPublishes, with everything as a field: nothing has
// said which of its values are dimensions, and a guess would invent series that
// cannot be un-invented.
func TestAnItemWithNoShapeStillPublishes(t *testing.T) {
	conn := serving(t)

	sub, err := conn.SubscribeSync("events.news.mystery")
	if err != nil {
		t.Fatal(err)
	}

	// Parsed but not validated, because validation is what refuses an exporter
	// naming an item nobody declared. This is the shape being unknown, which is
	// the case the exporter has to survive rather than the case it is for.
	doc, err := engine.Parse([]byte(items+`
  exporter "nats" "mystery" {
    subject = "events.news.mystery"
    url     = "`+conn.Address()+`"
  }
`+"\n}\n"), "job.hcl")
	if err != nil {
		t.Fatal(err)
	}

	set, err := exporter.New(context.Background(), doc.Jobs[0], nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	err = set.Write(context.Background(), &record.Record{
		Item: "mystery", URL: "https://example.com/m", Fetched: fetched,
		Values: map[string]string{"of": "acme", "title": "One"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}

	msg, err := sub.NextMsg(wait)
	if err != nil {
		t.Fatalf("an item with no shape published nothing: %v", err)
	}

	// The root subject, because with no shape there is no `of` tag to put an
	// entity in the subject, and inventing one from a value that might be
	// anything is how a subject tree fills with rubbish.
	if msg.Subject != "events.news.mystery" {
		t.Errorf("subject = %q", msg.Subject)
	}
	m := measurement(t, msg.Data)
	if len(m.Tags) != 0 {
		t.Errorf("values became dimensions with nothing saying they were: %v", m.Tags)
	}
	if m.Fields["title"] != "One" || m.Fields["of"] != "acme" {
		t.Errorf("fields = %v, want every value", m.Fields)
	}
}

// TestAnEntityCannotAddASubjectLevel: a dot inside a company's name would push
// it down a level, and `events.markets.price.*` would quietly stop matching it.
func TestAnEntityCannotAddASubjectLevel(t *testing.T) {
	conn := serving(t)

	all, err := conn.SubscribeSync("events.markets.price.*")
	if err != nil {
		t.Fatal(err)
	}

	publish(t, job(t, `
  exporter "nats" "price" {
    subject = "events.markets.price"
    url     = "`+conn.Address()+`"
  }
`), price("https://example.com/a", "acme.co uk", "1.00"))

	msg, err := all.NextMsg(wait)
	if err != nil {
		t.Fatalf("the wildcard stopped matching: %v", err)
	}
	if strings.Count(msg.Subject, ".") != 3 {
		t.Errorf("subject = %q, want one level below the root", msg.Subject)
	}

	// The name itself is untouched in the measurement: the subject is where it
	// had to be made safe, not the data.
	if m := measurement(t, msg.Data); m.Tags["company"] != "acme.co uk" {
		t.Errorf("the entity's name was changed in the payload: %+v", m)
	}
}

// TestAnExporterPublishesOnTheConnectionTheNodeLent, so a node writing six items
// holds one connection rather than six.
func TestAnExporterPublishesOnTheConnectionTheNodeLent(t *testing.T) {
	conn := serving(t)

	natsexport.Use(conn.Conn)
	t.Cleanup(func() { natsexport.Use(nil) })

	sub, err := conn.SubscribeSync("events.news.headline")
	if err != nil {
		t.Fatal(err)
	}

	publish(t, job(t, `
  exporter "nats" "headline" {
    subject = "events.news.headline"
  }
`), headline("https://example.com/a", "One"))

	msg, err := sub.NextMsg(wait)
	if err != nil {
		t.Fatalf("nothing was published on the lent connection: %v", err)
	}
	if m := measurement(t, msg.Data); m.Fields["title"] != "One" {
		t.Errorf("measurement = %+v", m)
	}

	// And the lent connection is still open, because closing somebody else's
	// connection would take the bus away from the node still using it.
	if conn.IsClosed() {
		t.Error("an exporter closed the connection it had been lent")
	}
}

// TestAnExporterWithNowhereToPublishIsRefusedWhenBuilt, rather than running a
// whole crawl and delivering none of it.
func TestAnExporterWithNowhereToPublishIsRefusedWhenBuilt(t *testing.T) {
	natsexport.Use(nil)

	_, err := exporter.New(context.Background(), job(t, `
  exporter "nats" "headline" {
    subject = "events.news.headline"
  }
`), nil)
	if err == nil {
		t.Fatal("built an exporter with no server and no connection")
	}
	if !strings.Contains(err.Error(), "nats.headline") {
		t.Errorf("the error does not say which exporter: %v", err)
	}
}

// TestASubjectIsRequired, because a subject this invented would be a contract
// nobody had agreed to.
func TestASubjectIsRequired(t *testing.T) {
	conn := serving(t)

	_, err := exporter.New(context.Background(), job(t, `
  exporter "nats" "headline" {
    url = "`+conn.Address()+`"
  }
`), nil)
	if err == nil {
		t.Fatal("built an exporter with no subject")
	}
	if !strings.Contains(err.Error(), "subject") {
		t.Errorf("the error does not say what is missing: %v", err)
	}
}

// TestContract holds this format to what every exporter promises. See
// [exportertest]. The item is the suite's own, so the block declares it.
func TestContract(t *testing.T) {
	conn := serving(t)

	exportertest.Run(t, func(t *testing.T, dir string) exporter.Exporter {
		src := `
job "markets" {
  start = ["https://example.com/"]

  item "article" {
    property "title" {
      type = str
    }
  }

  exporter "nats" "article" {
    subject = "events.contract.article"
    url     = "` + conn.Address() + `"
  }
}
`
		doc, err := engine.Parse([]byte(src), "job.hcl")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if err := doc.Validate(); err != nil {
			t.Fatalf("validate: %v", err)
		}

		set, err := exporter.New(context.Background(), doc.Jobs[0], nil)
		if err != nil {
			t.Fatalf("new: %v", err)
		}
		return exportertest.Only(t, set)
	})
}
