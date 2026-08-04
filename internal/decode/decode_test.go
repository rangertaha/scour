// SPDX-License-Identifier: GPL-3.0-or-later

package decode_test

import (
	"io"
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"

	"github.com/rangertaha/scour/internal/decode"
)

// encoded renders text in a legacy encoding, which is what the sites this has
// to cope with actually send.
func encoded(t *testing.T, e *charmap.Charmap, text string) []byte {
	t.Helper()
	out, err := e.NewEncoder().Bytes([]byte(text))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return out
}

func TestDeclaredEncoding(t *testing.T) {
	for name, tc := range map[string]struct {
		charmap     *charmap.Charmap
		contentType string
		text        string
	}{
		"russian": {charmap.Windows1251, "text/html; charset=windows-1251", "Привет мир"},
		"greek":   {charmap.ISO8859_7, "text/html; charset=iso-8859-7", "Γειά σου κόσμε"},
		"turkish": {charmap.Windows1254, "text/html; charset=windows-1254", "Merhaba dünya"},
		"western": {charmap.Windows1252, "text/html; charset=windows-1252", "Grüße"},
	} {
		t.Run(name, func(t *testing.T) {
			body := encoded(t, tc.charmap, tc.text)

			// The bytes are not the text. That is the whole problem.
			if string(body) == tc.text {
				t.Fatal("the fixture is already UTF-8, so this proves nothing")
			}

			got, err := decode.Bytes(body, tc.contentType)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if string(got.Text) != tc.text {
				t.Errorf("got %q, want %q", got.Text, tc.text)
			}
			if !got.Declared {
				t.Error("an encoding the response stated was reported as a guess")
			}
		})
	}
}

// TestMetaElement covers the page that says what it is in its markup and not in
// its headers, which is most of the old web.
func TestMetaElement(t *testing.T) {
	body := encoded(t, charmap.Windows1251,
		`<html><head><meta charset="windows-1251"></head><body>Привет</body></html>`)

	got, err := decode.Bytes(body, "")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(string(got.Text), "Привет") {
		t.Errorf("got %q", got.Text)
	}
}

func TestUTF8PassesThrough(t *testing.T) {
	body := []byte("Hello, 世界")

	got, err := decode.Bytes(body, "text/html; charset=utf-8")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(got.Text) != string(body) {
		t.Errorf("got %q, want it unchanged", got.Text)
	}
	if got.Charset != "utf-8" {
		t.Errorf("charset = %q", got.Charset)
	}
}

func TestEmptyBody(t *testing.T) {
	got, err := decode.Bytes(nil, "text/html")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Text) != 0 || got.Charset != "" {
		t.Errorf("got %+v", got)
	}
}

// TestUndecodableKeepsThePage: a crawl that dropped a page over an encoding it
// did not recognise would lose evidence to make a point.
func TestUndecodableKeepsThePage(t *testing.T) {
	body := []byte("some bytes")

	got, err := decode.Bytes(body, "text/html; charset=x-nonesuch-9000")
	if len(got.Text) == 0 {
		t.Error("the page was thrown away")
	}
	_ = err // reported for the log, not fatal
}

func TestCharsetWithoutDecoding(t *testing.T) {
	body := encoded(t, charmap.Windows1251, "Привет")

	name, declared := decode.Charset(body, "text/html; charset=windows-1251")
	if name != "windows-1251" {
		t.Errorf("charset = %q", name)
	}
	if !declared {
		t.Error("a declared encoding was reported as a guess")
	}

	if name, _ := decode.Charset(nil, "text/html"); name != "" {
		t.Errorf("an empty body has charset %q", name)
	}
}

// TestReaderDecodesAStream is for bodies too big to want in memory twice.
func TestReaderDecodesAStream(t *testing.T) {
	body := encoded(t, charmap.Windows1251, "Привет мир")

	r, err := decode.Reader(strings.NewReader(string(body)), "text/html; charset=windows-1251")
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "Привет мир" {
		t.Errorf("got %q", got)
	}
}

// TestTheCacheHoldsWhatTheServerSent is the property the whole design rests on:
// decoding is something a reader does, so the same bytes decode the same way
// however many times they are read.
func TestTheCacheHoldsWhatTheServerSent(t *testing.T) {
	stored := encoded(t, charmap.Windows1251, "Привет мир")

	first, err := decode.Bytes(stored, "text/html; charset=windows-1251")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	second, err := decode.Bytes(stored, "text/html; charset=windows-1251")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if string(first.Text) != string(second.Text) {
		t.Error("two reads of the same bytes disagreed")
	}
	// And the stored bytes are untouched, so a better decoder can be run over
	// them later.
	if string(stored) == string(first.Text) {
		t.Error("decoding mutated what was stored")
	}
}
