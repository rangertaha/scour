// SPDX-License-Identifier: GPL-3.0-or-later

// Package decode turns a fetched body into UTF-8 text.
//
// # Why this is not middleware
//
// Decoding is what reading a body means, not a step in a chain. There are two
// readers: the downloader, on its way to writing the cache, and the spider,
// which reads the cache directly by key and never passes through the
// downloader's middleware at all. A decode that lived in that chain would apply
// to one of them and not the other, and the corpus would be UTF-8 only when it
// happened to be read the long way round.
//
// So it is a function both call, and there is one implementation of it.
//
// # What the cache holds
//
// What the server sent. Not this package's output.
//
// That is the more useful archive: detection improves, and a corpus of original
// bytes can be decoded again to get a better answer, while a corpus decoded on
// the way in has its mistakes baked in until somebody re-crawls. It is also
// smaller, and it keeps the response faithful enough to revalidate.
//
// The cost is that every read decodes. For a corpus that is re-analysed a few
// hundred pages at a time that is nothing, and it buys the ability to be wrong
// about an encoding and fix it later.
package decode

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html/charset"
)

// Result is a decoded body and what it was decoded from.
type Result struct {
	// Text is the body as UTF-8.
	Text []byte
	// Charset is the encoding that was used, as it is named in IANA's
	// registry: "utf-8", "windows-1251". Empty when the body was empty.
	Charset string
	// Declared reports whether the encoding was stated by the response's
	// Content-Type or by a byte order mark, rather than guessed.
	//
	// Not "or by the document", which this used to say. A meta element is read
	// and believed - see [detect] for the order - but the library reports it
	// the same way it reports a guess, so a page that stated its own encoding
	// is indistinguishable here from one that stated nothing. A caller must
	// not read a false as "the page said nothing".
	Declared bool
}

// Bytes decodes a body to UTF-8.
//
// The content type is the response's, and may be empty: the encoding is then
// taken from a byte order mark, a meta element, or failing both, from the shape
// of the bytes themselves.
//
// It does not fail on an undecodable body. A page that arrives in an encoding
// nothing recognises is still mostly readable once the unmappable bytes become
// replacement characters, and a crawl that dropped it would lose a page to make
// a point. The error is returned alongside the best effort so a caller can log
// it and carry on.
func Bytes(body []byte, contentType string) (Result, error) {
	if len(body) == 0 {
		return Result{Text: body}, nil
	}

	name, declared := detect(body, contentType)

	// The overwhelmingly common case, and worth not copying for.
	if isUTF8(name) && utf8.Valid(body) {
		return Result{Text: body, Charset: "utf-8", Declared: declared}, nil
	}

	reader, err := charset.NewReader(bytes.NewReader(body), contentType)
	if err != nil {
		// An encoding nothing implements. Keep the bytes rather than the page.
		return Result{Text: body, Charset: name, Declared: declared},
			fmt.Errorf("decode %q: %w", name, err)
	}

	text, err := io.ReadAll(reader)
	if err != nil {
		return Result{Text: body, Charset: name, Declared: declared},
			fmt.Errorf("decode %q: %w", name, err)
	}
	return Result{Text: text, Charset: name, Declared: declared}, nil
}

// Reader decodes a stream, for a body too big to want in memory twice.
//
// It reads enough to determine the encoding and then decodes the rest as it
// goes, so a large PDF or a long article costs one copy rather than two.
func Reader(r io.Reader, contentType string) (io.Reader, error) {
	decoded, err := charset.NewReader(r, contentType)
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return decoded, nil
}

// Charset reports the encoding a body would be decoded from, without decoding
// it. It is what a fetch records alongside the page.
func Charset(body []byte, contentType string) (name string, declared bool) {
	if len(body) == 0 {
		return "", false
	}
	return detect(body, contentType)
}

// detect names the encoding, and says whether it was declared in a way this can
// tell apart from a guess.
//
// The order is the one the HTML specification requires: what the response said,
// then a byte order mark, then a meta element, then the bytes. Sniffing the
// bytes is last because it is a guess, and a site that bothered to say should
// be believed over a detector that has seen a kilobyte.
//
// The second result is narrower than that order suggests: the library sets it
// for the response and for a byte order mark, and not for a meta element. So a
// document that stated its own encoding comes back with the encoding it stated
// and a false. See [Result.Declared].
func detect(body []byte, contentType string) (string, bool) {
	_, name, certain := charset.DetermineEncoding(body, contentType)
	return strings.ToLower(name), certain
}

// isUTF8 reports whether a name is one of the spellings of UTF-8.
func isUTF8(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "utf-8", "utf8":
		return true
	default:
		return false
	}
}
