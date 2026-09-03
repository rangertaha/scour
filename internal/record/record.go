// SPDX-License-Identifier: GPL-3.0-or-later

// Package record is what an extracted item becomes once it leaves the spider.
//
// The spider's output is about a page: what was found, where on the page it was
// found, and which of four ways found it. That is exactly what somebody
// debugging extraction wants and exactly what nothing downstream needs. A
// pipeline step sorts records, an exporter writes them, and neither has any use
// for the provenance of a headline.
//
// So this is the flat form: names to values, and the four things that say which
// crawl produced it.
//
// # Nested values are flattened
//
// An author object becomes `author.name` and `author.profile`. A CSV cannot
// express a tree, a pipeline step that had to walk one would be walking one in
// every implementation, and a dotted name is what everybody writes when they
// flatten by hand anyway.
package record

import (
	"encoding/json"
	"maps"
	"sort"
	"time"

	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/extract"
)

// Record is one extracted item, ready to be worked on and written out.
type Record struct {
	// Item is the shape's name, as the job declared it.
	Item string `json:"item"`

	// URL is the page it came from.
	URL string `json:"url"`

	// Spec is the fingerprint of the shape it was read under.
	//
	// A record attributed to the wrong shape is wrong in a way nothing
	// downstream can detect, so every one of them says which shape it is,
	// and a resubmission that changes the shape can find the stale ones.
	Spec string `json:"spec"`

	// Fetched is when the page was fetched, not when this was extracted. Two
	// runs over one cached corpus should produce the same records, and a
	// timestamp that moved between them would make that untestable.
	Fetched time.Time `json:"fetched"`

	// Values are the properties that found something, keyed by name, nested
	// ones flattened with a dot.
	Values map[string]string `json:"values"`
}

// From turns one page's items into records.
func From(url, spec string, fetched time.Time, items []*extract.Item) []*Record {
	out := make([]*Record, 0, len(items))

	for _, item := range items {
		r := &Record{
			Item:    item.Name,
			URL:     url,
			Spec:    spec,
			Fetched: fetched,
			Values:  map[string]string{},
		}
		// Every depth, not one. See [extract.Value.Each]: the shape decides
		// how deep the tree is, and engine.Item.Fields names a leaf at any
		// depth, so a one-level walk here made a declared field that was
		// extracted absent from the record and from every export built on it.
		for name, value := range item.Values {
			value.Each(name, func(name string, value *extract.Value) {
				r.Values[name] = value.Text
			})
		}
		out = append(out, r)
	}
	return out
}

// Stamp is the one rendering of a fetch time, and [Record.MarshalJSON] is what
// makes it the only one.
//
// Every export of a crawl writes it this way, so a join between two of them
// matches. It had drifted twice. The table formats each formatted it and the
// document formats marshalled [time.Time] directly, which emits RFC 3339 nano
// in whatever offset the machine was in: one file said
// 2026-08-05T14:23:45.123456789+02:00 and the other 2026-08-05T12:23:45Z for
// the same record, and joining them matched nothing at all. That was noticed,
// a shared renderer was written, and the document formats went on marshalling
// the record - so the drift was still there, with a constant and a test above
// it saying otherwise.
//
// So it lives on the record rather than beside the formats. A format cannot
// write this field a different way without saying so, because marshalling the
// record is what produces it.
//
// Seconds and UTC, because the exports are copies of one crawl and have to
// agree more than they have to be precise. The record carries the full time in
// memory, so nothing is lost that a reader of the record cannot get.
const Stamp = "2006-01-02T15:04:05Z"

// Stamped is a fetch time as every format writes it.
func Stamped(t time.Time) string { return t.UTC().Format(Stamp) }

// MarshalJSON writes the fetch time as [Stamp] and everything else as usual.
func (r *Record) MarshalJSON() ([]byte, error) {
	// An alias, so marshalling the shadow does not call this again.
	type plain Record

	return json.Marshal(struct {
		*plain
		Fetched string `json:"fetched"`
	}{
		plain:   (*plain)(r),
		Fetched: Stamped(r.Fetched),
	})
}

// Identity is what makes two records the same record.
//
// The item and the page, and deliberately not the values. A pipeline step
// transforms values, so an identity derived from them changes when a step runs,
// and anything comparing a record before and after would conclude it was
// looking at two records. That is not hypothetical: the wave merge did exactly
// that, and a pipeline of two independent `clean` steps discarded every record
// it was given, silently, reporting success.
//
// One page produces at most one record per item, so this is unique within a
// run. A step that invented a second record for one item on one page would
// break that, which is why the pipeline refuses a wave whose input holds two
// records with one identity rather than merging them wrongly.
func (r *Record) Identity() string { return r.Item + "\x00" + r.URL }

// Names lists the value names, sorted, which is what a writer with columns
// needs and what makes any two runs produce the same header.
func (r *Record) Names() []string {
	out := make([]string, 0, len(r.Values))
	for name := range r.Values {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Get returns one value.
func (r *Record) Get(name string) string { return r.Values[name] }

// Clone returns a copy safe to modify, which is what a pipeline step does
// rather than editing what it was handed. A step that mutated in place would
// make the graph's order observable, and the whole point of a graph is that
// independent steps are not ordered against each other.
func (r *Record) Clone() *Record {
	out := *r
	out.Values = maps.Clone(r.Values)
	if out.Values == nil {
		out.Values = map[string]string{}
	}
	return &out
}

// Measurement is a record in the shape a time-series store takes: a name, the
// dimensions to group by, the numbers measured, and when.
//
// Derived rather than declared. [engine.Item.Tags] and [engine.Item.Fields]
// already say which is which, because entity references are bounded by
// definition and free text is not.
type Measurement struct {
	Name   string            `json:"name"`
	Tags   map[string]string `json:"tags,omitempty"`
	Fields map[string]string `json:"fields,omitempty"`
	Time   time.Time         `json:"time"`
}

// Measure renders a record as a measurement, against the shape it was read
// under.
//
// The time is the item's own if it declared one and could parse it, and the
// fetch time otherwise. Event time, never ingest time: a headline published at
// nine and crawled at half eleven is an event at nine, and getting that wrong
// makes replay produce series that are wrong in a way nobody notices for
// months.
func (r *Record) Measure(shape *engine.Item) *Measurement {
	m := &Measurement{
		Name:   r.Item,
		Tags:   map[string]string{},
		Fields: map[string]string{},
		Time:   r.Fetched,
	}

	if shape == nil {
		for name, value := range r.Values {
			m.Fields[name] = value
		}
		return m
	}

	for _, name := range shape.Tags() {
		if value := r.Values[name]; value != "" {
			m.Tags[name] = value
		}
	}
	for _, name := range shape.Fields() {
		if value := r.Values[name]; value != "" {
			m.Fields[name] = value
		}
	}

	if shape.Time != "" {
		if when, err := time.Parse(time.RFC3339, r.Values[shape.Time]); err == nil {
			m.Time = when
		}
	}
	return m
}
