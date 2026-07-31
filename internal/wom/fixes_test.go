// SPDX-License-Identifier: MIT

package wom_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/wom"
)

// A stored regex that no longer matches means the locator has not found its
// value. Returning the raw text instead would put a label into a field the
// schema says is a number, with no error anywhere.
func TestExtractDropsNodesWhoseRegexNoLongerMatches(t *testing.T) {
	t.Parallel()

	model := &wom.Model{
		Version: 1,
		Items: []wom.Item{{
			Name: "year",
			Locator: wom.Locator{
				Format: wom.FormatHTML,
				URI:    `^.*$`,
				Path:   "/html[1]/body[1]/p[1]/text()[1]",
				XPath:  "/html[1]/body[1]/p[1]/text()[1]",
				Regex:  `^(\d{4})$`,
			},
		}},
	}

	t.Run("still matching", func(t *testing.T) {
		w := wom.New()
		if err := w.AddBody("https://example.com/a", "text/html",
			[]byte(`<html><body><p>2019</p></body></html>`)); err != nil {
			t.Fatal(err)
		}
		records := model.Extract(w)
		if len(records) != 1 || records[0].Value != "2019" {
			t.Fatalf("records = %+v, want one holding 2019", records)
		}
	})

	t.Run("page changed", func(t *testing.T) {
		w := wom.New()
		if err := w.AddBody("https://example.com/a", "text/html",
			[]byte(`<html><body><p>Year: 2019</p></body></html>`)); err != nil {
			t.Fatal(err)
		}
		for _, r := range model.Extract(w) {
			t.Errorf("extracted %q from text the regex does not match; want no record", r.Value)
		}
	})
}

// A locator naming a host element that is no longer there must find nothing.
// Falling back to searching the whole document would match the same path
// inside a different JSON-LD block and return another record's data.
func TestExtractDoesNotFallBackToTheWholeDocument(t *testing.T) {
	t.Parallel()

	const body = `<html><head>
<script type="application/ld+json">{"datePublished":"2026-01-01T00:00:00Z"}</script>
<script type="application/ld+json">{"datePublished":"1999-12-31T00:00:00Z"}</script>
</head><body><p>x</p></body></html>`

	w := wom.New()
	if err := w.AddBody("https://example.com/a", "text/html", []byte(body)); err != nil {
		t.Fatal(err)
	}

	model := &wom.Model{
		Version: 1,
		Items: []wom.Item{{
			Name: "published",
			Locator: wom.Locator{
				Format: wom.FormatJSON,
				URI:    `^.*$`,
				Path:   "$.datePublished",
				// The block this model was induced from is not on this page.
				XPath: "/html[1]/head[1]/script[9]",
				Regex: `^(.*)$`,
			},
		}},
	}

	for _, r := range model.Extract(w) {
		t.Errorf("extracted %q from a block the locator does not name", r.Value)
	}

	// Naming a block that is present must still work, and must pick that one.
	model.Items[0].XPath = "/html[1]/head[1]/script[2]"
	records := model.Extract(w)
	if len(records) != 1 {
		t.Fatalf("records = %+v, want exactly one", records)
	}
	if !strings.HasPrefix(records[0].Value, "1999") {
		t.Errorf("value = %q, want the second block's date", records[0].Value)
	}
}

// A body that is not CSS at all must be rejected rather than accepted as an
// empty document, which is how every other format behaves.
func TestAddBodyRejectsNonCSS(t *testing.T) {
	t.Parallel()

	w := wom.New()
	err := w.AddBody("https://example.com/x.css", "text/css", []byte("\x00\x01 not css at all ][}{"))
	if err == nil {
		t.Fatalf("garbage accepted as css; graph holds %d documents", w.Len())
	}
	if w.Len() != 0 {
		t.Errorf("Len() = %d, want the rejected document not to be stored", w.Len())
	}

	// Real stylesheets contain mistakes; a partial parse is still useful.
	ok := wom.New()
	if err := ok.AddBody("https://example.com/y.css", "text/css",
		[]byte(`.a { color: red; } .b { ; broken`)); err != nil {
		t.Errorf("a slightly broken stylesheet should still parse: %v", err)
	}
	// An empty stylesheet is valid.
	if err := ok.AddBody("https://example.com/z.css", "text/css", []byte("  \n")); err != nil {
		t.Errorf("an empty stylesheet should be accepted: %v", err)
	}
}

// A trained chain describes how records are written, not how one site marks
// them up, so it has to be reusable against a different site.
func TestChainPriorIsReusable(t *testing.T) {
	t.Parallel()

	w := wom.New()
	if err := w.AddBody("https://example.com/a", "text/html",
		[]byte(`<html><body><div class="v">`+
			`<span class="make">Toyota</span><span class="year">2019</span></div>`+
			`<div class="v"><span class="make">Honda</span><span class="year">2021</span></div>`+
			`</body></html>`)); err != nil {
		t.Fatal(err)
	}
	props := wom.Prop{Name: "vehicles", Props: []wom.Prop{
		{Name: "make", Examples: []string{"Toyota"}},
		{Name: "year", Type: wom.TypeNumber},
	}}

	model, err := w.Model(props)
	if err != nil {
		t.Fatal(err)
	}
	if trainErr := model.Train(w); trainErr != nil {
		t.Fatalf("Train: %v", trainErr)
	}
	if model.Chain == nil {
		t.Fatal("Train produced no chain")
	}

	// The chain must load into a fresh engine and still produce results.
	reused := wom.New(wom.WithChainPrior(model.Chain))
	if addErr := reused.AddBody("https://other.example/b", "text/html",
		[]byte(`<html><body><div class="v">`+
			`<span class="make">Ford</span><span class="year">2018</span></div>`+
			`<div class="v"><span class="make">Kia</span><span class="year">2022</span></div>`+
			`</body></html>`)); addErr != nil {
		t.Fatal(addErr)
	}
	items, err := reused.Schema(props)
	if err != nil {
		t.Fatalf("Schema with a reused chain: %v", err)
	}
	if len(items) == 0 {
		t.Error("a reused chain produced no results")
	}
}

// Errors stay comparable through the facade.
func TestErrorsAreComparable(t *testing.T) {
	t.Parallel()

	w := wom.New()
	if err := w.AddBody("https://example.com/b", "", []byte("\x00\x01\x02")); !errors.Is(err, wom.ErrUnknownFormat) {
		t.Errorf("AddBody = %v, want ErrUnknownFormat", err)
	}
	if _, err := w.Schema(); !errors.Is(err, wom.ErrEmptySchema) {
		t.Errorf("Schema() = %v, want ErrEmptySchema", err)
	}
	m := &wom.Model{Version: 1}
	if err := m.Train(w); !errors.Is(err, wom.ErrNoRecord) {
		t.Errorf("Train with no record = %v, want ErrNoRecord", err)
	}
}
