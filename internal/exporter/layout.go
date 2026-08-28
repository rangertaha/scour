// SPDX-License-Identifier: GPL-3.0-or-later

package exporter

import (
	"fmt"
	"time"

	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/record"
)

// Stamp is the one rendering of a fetch time.
//
// Every format writes it this way, so a join between two exports of one crawl
// matches. It had drifted: the table formats each formatted it like this while
// json and jsonlines marshalled [time.Time] directly, which emits RFC 3339 nano
// in whatever offset the machine was in. One file said
// 2026-08-05T14:23:45.123456789+02:00 and the other 2026-08-05T12:23:45Z for the
// same record, so joining them matched nothing at all.
//
// Seconds and UTC, because the exports are copies of one crawl and have to agree
// more than they have to be precise. [record.Record] carries the full time, so
// nothing is lost that a reader of the record cannot get.
const Stamp = "2006-01-02T15:04:05Z"

// Layout is what an exporter writes: which columns, in which order, and what
// each one holds for a given record.
//
// # Why this is shared
//
// Because it had been written three times, character for character, and the
// three copies drifted in exactly the way that costs a whole export. The CSV
// exporter was missing the collision check the other two had, so a job declaring
// a property named `url` produced a header with two `url` columns, both filled
// with the page address, and the extracted value never reached the file. The
// same job was refused at build time by parquet and by sqlite. The rendering of
// the fetch time had drifted a second way, between the table formats and the
// document ones.
//
// So there is one derivation now, and a new format gets the flattening, the
// sorting, the collision check and the rendering by asking for a layout rather
// than by remembering four things.
type Layout struct {
	builtin map[string]bool
	columns []string
}

// NewLayout builds the layout for a shape, with the columns the format owns
// first and the shape's properties after them.
//
// A shape whose property collides with one of the format's own columns is
// refused here, when the exporter is built, rather than worked around. Both ways
// round are wrong: giving the property the column loses the record's provenance,
// and keeping the provenance loses the property, and a person reading the output
// afterwards has no way to tell which happened. Renaming the property is a
// one-line fix they can make and this cannot.
//
// A nil shape is an exporter with no declared item, which writes the format's
// own columns and nothing else.
//
// Reserved names are ones the format owns without writing them as ordinary
// columns: sqlite's `key` is its primary key, computed rather than read, and a
// property of that name collides with it just as surely as one named `url`
// does. They are refused and they are not returned by [Layout.Columns].
func NewLayout(kind string, shape *engine.Item, own []string, reserved ...string) (*Layout, error) {
	l := &Layout{builtin: make(map[string]bool, len(own)+len(reserved))}

	for _, name := range append(append([]string{}, own...), reserved...) {
		if l.builtin[name] {
			return nil, fmt.Errorf("exporter/%s: %q is listed twice as a column this format owns", kind, name)
		}
		l.builtin[name] = true
	}
	l.columns = append(l.columns, own...)
	if shape == nil {
		return l, nil
	}

	for _, name := range shape.Names() {
		if l.builtin[name] {
			return nil, fmt.Errorf(
				"exporter/%s: a property named %q collides with a column this format writes; rename it", kind, name)
		}
		l.columns = append(l.columns, name)
	}
	return l, nil
}

// Columns are the names to write, in order.
func (l *Layout) Columns() []string { return l.columns }

// Value is what one record holds for one column.
//
// The format's own columns are answered from the record's provenance and
// everything else from its values, so a format cannot accidentally render the
// fetch time one way while another renders it a second way.
//
// # Only the columns this format actually claimed
//
// The check against l.builtin is what keeps that sentence true. The switch
// below used to answer for four names whatever the format had registered, and
// the formats register different sets: csv claims url and fetched, parquet and
// sqlite also claim spec, and none of them claims item.
//
// So a job declaring `property "spec"` was accepted by csv - it collides with
// nothing csv owns - given a column, and then filled with the shape
// fingerprint on every row, identical everywhere, while the extracted value
// never reached the file and nothing errored. A property named `item` did the
// same in all three formats. The one list that says which names are the
// format's own was in NewLayout, and this second list disagreed with it.
func (l *Layout) Value(r *record.Record, column string) string {
	if !l.builtin[column] {
		return r.Values[column]
	}

	switch column {
	case "url":
		return r.URL
	case "spec":
		return r.Spec
	case "item":
		return r.Item
	case "fetched":
		return r.Fetched.UTC().Format(Stamp)
	default:
		return r.Values[column]
	}
}

// Stamped is a fetch time as every format writes it.
func Stamped(t time.Time) string { return t.UTC().Format(Stamp) }
