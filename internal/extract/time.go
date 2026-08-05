// SPDX-License-Identifier: GPL-3.0-or-later

package extract

import (
	"strings"
	"time"
)

// layouts are the shapes a date arrives in, most machine-readable first.
//
// A published date is the field most likely to be written four ways on one
// site: once in the Open Graph tag as RFC 3339, once in a `<time datetime>` as
// a date, and once in the visible text as something a person reads. Every one
// of them is the same instant, and a records table with three spellings of it
// is a table nobody can sort.
var layouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04",
	"2006-01-02",
	"2006/01/02",
	"02/01/2006",
	"01/02/2006",
	time.RFC1123Z,
	time.RFC1123,
	time.RFC822Z,
	time.RFC822,
	"Mon, 02 Jan 2006 15:04:05 -0700",
	"2 January 2006",
	"02 January 2006",
	"January 2, 2006",
	"Jan 2, 2006",
	"2 Jan 2006",
	"20060102",
}

// parseTime reads a date in whatever shape a page wrote it and returns RFC 3339
// in UTC.
//
// It reports whether it understood the value rather than returning an error,
// because a date it cannot read is not a failure: the raw text is kept, and a
// value that is unparseable today is evidence about a format worth adding.
func parseTime(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}

	for _, layout := range layouts {
		if when, err := time.Parse(layout, value); err == nil {
			return when.UTC().Format(time.RFC3339), true
		}
	}

	// A timestamp in seconds, which is what an API embedded in a page writes.
	// Bounded to a range that is plainly a date rather than a page's word
	// count: 1990 to 2100.
	if seconds, ok := digits(value); ok && seconds > 631152000 && seconds < 4102444800 {
		return time.Unix(seconds, 0).UTC().Format(time.RFC3339), true
	}
	return "", false
}

func digits(s string) (int64, bool) {
	if s == "" || len(s) > 19 {
		return 0, false
	}

	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int64(r-'0')
	}
	return n, true
}
