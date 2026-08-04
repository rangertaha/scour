// SPDX-License-Identifier: GPL-3.0-or-later

package engine

import (
	"encoding/json"
	"fmt"
	"time"
)

// Duration is a [time.Duration] that a human writes as "30s" and a machine
// reads back as the same thing.
//
// time.Duration marshals to JSON as a count of nanoseconds, which is unreadable
// in a submitted job and impossible to write by hand without arithmetic. A job
// is a document somebody edits, so the wire form is the string form.
type Duration time.Duration

// String renders the duration the way it is written.
func (d Duration) String() string { return time.Duration(d).String() }

// Duration is the value as a [time.Duration].
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// MarshalJSON implements [json.Marshaler].
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON implements [json.Unmarshaler].
//
// A number is accepted as well as a string, and read as seconds. Not
// nanoseconds: a client that writes 30 means half a minute, and reading it as
// 30ns would produce a crawl that hammers a site rather than an obvious error.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}

	switch value := v.(type) {
	case string:
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("duration %q: %w", value, err)
		}
		*d = Duration(parsed)
		return nil
	case float64:
		*d = Duration(time.Duration(value) * time.Second)
		return nil
	case nil:
		*d = 0
		return nil
	default:
		return fmt.Errorf("duration: cannot read %T", v)
	}
}

// UnmarshalText implements [encoding.TextUnmarshaler], which is what TOML uses.
func (d *Duration) UnmarshalText(b []byte) error {
	parsed, err := time.ParseDuration(string(b))
	if err != nil {
		return fmt.Errorf("duration %q: %w", b, err)
	}
	*d = Duration(parsed)
	return nil
}

// MarshalText implements [encoding.TextMarshaler].
func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }
