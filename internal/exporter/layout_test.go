// SPDX-License-Identifier: GPL-3.0-or-later

package exporter_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/exporter"
	"github.com/rangertaha/scour/internal/record"
)

func shape(t *testing.T, src string) *engine.Item {
	t.Helper()

	doc, err := engine.Parse([]byte(src), "job.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return doc.Jobs[0].Items[0]
}

const oneItem = `
job "news" {
  domains = ["example.com"]
  start   = ["https://example.com/"]

  item "article" {
    property "title" {
      type = str
    }

    property "author" {
      type = object

      property "name" {
        type = str
      }
    }
  }
}
`

// TestColumnsAreTheFormatsThenTheShapesFlattened.
func TestColumnsAreTheFormatsThenTheShapesFlattened(t *testing.T) {
	l, err := exporter.NewLayout("csv", shape(t, oneItem), []string{"url", "fetched"})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"url", "fetched", "author.name", "title"}
	if got := l.Columns(); !slices.Equal(got, want) {
		t.Errorf("Columns() = %v, want %v", got, want)
	}
}

// TestAPropertyCollidingWithAFormatsColumnIsRefused.
//
// Every format used to answer this differently, because every format derived
// its columns itself. Parquet and sqlite refused it; the CSV exporter did not,
// so a job declaring a property named `url` produced a header with two `url`
// columns, both holding the page address, and the extracted value never reached
// the file. Nothing failed, and the same job was refused by the other two
// formats. The check is in the shared layout now, so a format cannot be missing
// it and a new one gets it without being told.
func TestAPropertyCollidingWithAFormatsColumnIsRefused(t *testing.T) {
	item := shape(t, strings.Replace(oneItem, `property "title" {`, `property "url" {`, 1))

	for _, format := range []struct {
		kind string
		own  []string
	}{
		{"csv", []string{"url", "fetched"}},
		{"parquet", []string{"url", "fetched", "spec"}},
		{"sqlite", []string{"url", "fetched", "spec"}},
	} {
		if _, err := exporter.NewLayout(format.kind, item, format.own); err == nil {
			t.Errorf("%s accepted a property named url", format.kind)
		} else if !strings.Contains(err.Error(), "rename it") {
			t.Errorf("%s: the error does not say what to do: %v", format.kind, err)
		}
	}
}

// TestAReservedNameCollidesWithoutBecomingAColumn.
//
// sqlite's `key` is its primary key, computed from the other columns rather
// than read from the record. A property of that name collides with it just as
// surely as one named `url` does, and it must not appear twice in the CREATE
// TABLE.
func TestAReservedNameCollidesWithoutBecomingAColumn(t *testing.T) {
	l, err := exporter.NewLayout("sqlite", shape(t, oneItem), []string{"url", "fetched", "spec"}, "key")
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(l.Columns(), "key") {
		t.Errorf("Columns() = %v, and a reserved name is not a column", l.Columns())
	}

	item := shape(t, strings.Replace(oneItem, `property "title" {`, `property "key" {`, 1))
	if _, err := exporter.NewLayout("sqlite", item, []string{"url", "fetched", "spec"}, "key"); err == nil {
		t.Error("a property named key was accepted alongside the primary key of that name")
	}
}

// TestEveryFormatRendersAFetchTimeTheSameWay.
//
// Two exports of one crawl that disagree about what a record said are worse
// than either alone: a join between them on `fetched` matches nothing.
func TestEveryFormatRendersAFetchTimeTheSameWay(t *testing.T) {
	r := &record.Record{
		Item:    "article",
		URL:     "https://example.com/a",
		Spec:    "abc",
		Fetched: when(t),
		Values:  map[string]string{"title": "One"},
	}

	l, err := exporter.NewLayout("csv", shape(t, oneItem), []string{"url", "fetched"})
	if err != nil {
		t.Fatal(err)
	}

	const want = "2026-08-05T12:23:45Z"
	if got := l.Value(r, "fetched"); got != want {
		t.Errorf("Value(fetched) = %q, want %q", got, want)
	}
	if got := exporter.Stamped(r.Fetched); got != want {
		t.Errorf("Stamped() = %q, want %q", got, want)
	}
}

// when is a fetch time in an offset that is not UTC, because the drift this
// pins was exactly a renderer that kept the machine's own offset.
func when(t *testing.T) time.Time {
	t.Helper()
	return time.Date(2026, 8, 5, 14, 23, 45, 123456789, time.FixedZone("CEST", 2*60*60))
}

// TestAPropertyIsNeverWrittenAsProvenance.
//
// One list says which columns are the format's own, and Value used to hold a
// second one that disagreed with it: it answered for url, spec, item and
// fetched whatever the format had registered. The formats register different
// sets - csv claims url and fetched, parquet and sqlite also claim spec, and
// none of them claims item.
//
// So a job declaring `property "spec"` collided with nothing csv owns, was
// accepted, was given a column, and was then filled with the shape fingerprint
// on every row while the extracted value never reached the file. Nothing
// errored. A property named `item` did it in all three formats.
//
// Walked over every format and every name Value knows, because the defect was
// the two lists disagreeing and a spot check only ever finds one pair.
func TestAPropertyIsNeverWrittenAsProvenance(t *testing.T) {
	formats := map[string][]string{
		"csv":     {"url", "fetched"},
		"parquet": {"url", "fetched", "spec"},
		"sqlite":  {"url", "fetched", "spec"},
	}

	for kind, own := range formats {
		for _, name := range []string{"url", "spec", "item", "fetched"} {
			t.Run(kind+"/"+name, func(t *testing.T) {
				src := `
job "news" {
  domains = ["example.com"]
  start   = ["https://example.com/"]

  item "article" {
    property "` + name + `" {
      type = str
    }
  }
}
`
				l, err := exporter.NewLayout(kind, shape(t, src), own)
				if err != nil {
					// Refused because it collides with a column this format
					// owns, which is the other right answer and the one the
					// operator can act on.
					return
				}

				r := &record.Record{
					Item:   "article",
					URL:    "https://example.com/a",
					Spec:   "abc123",
					Values: map[string]string{name: "what was extracted"},
				}
				if got := l.Value(r, name); got != "what was extracted" {
					t.Errorf("a property named %q was accepted and then written as %q, "+
						"so the extracted value never reaches the file", name, got)
				}
			})
		}
	}
}
