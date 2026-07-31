// SPDX-License-Identifier: MIT

package wom_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/wom"
)

// update regenerates the golden result files under testdata/results. Run
// `go test -run TestNewsSites -update` after a deliberate change to inference.
var update = flag.Bool("update", false, "regenerate golden result files")

// articleSchema is the schema both sites are asked for. Nothing in it is
// site-specific: the same four props are located in two unrelated CMSes.
func articleSchema() wom.Schema {
	return wom.Schema{{
		Name:        "article",
		Aliases:     []string{"story", "news"},
		Description: "A published news article",
		Props: []wom.Prop{
			{Name: "title", Aliases: []string{"headline"}, Type: wom.TypeString},
			{Name: "authors", Aliases: []string{"author", "byline"}, Type: wom.TypeString},
			{Name: "published", Aliases: []string{"datePublished", "published_time"}, Type: wom.TypeDate},
			{Name: "modified", Aliases: []string{"dateModified", "modified_time"}, Type: wom.TypeDate},
		},
	}}
}

// site describes one end-to-end case: fixtures captured from a real site, the
// URLs they were served at, and what the extracted data must contain.
type site struct {
	name string
	glob string
	urls map[string]string
	// wantValues maps a field name to a value that must appear among the
	// extracted results.
	wantValues map[string]string
	// wantLocator maps a field name to a substring its locator must contain.
	wantLocator map[string]string
}

func newsSites() []site {
	return []site{
		{
			name: "apnews",
			glob: "testdata/apnews-article-*.html",
			urls: map[string]string{
				"apnews-article-1.html": "https://apnews.com/article/alien-terrorist-removal-court-deportation-dormant-trump-4573fe653ae466e7c9540dc02ee970e5",
				"apnews-article-2.html": "https://apnews.com/article/alien-terrorist-removal-court-first-deportation-plot-112d39fcfd89f52ddc6b85cb04e0c934",
				"apnews-article-3.html": "https://apnews.com/article/america-250-time-capsule-8d869f8aa39ef61a5721c039c397464e",
			},
			wantValues: map[string]string{
				"published": "2026-07-30T17:04:39",
				"modified":  "2026-07-30T18:22:03",
				"authors":   "michael-kunzelman",
				"title":     "Afghan",
			},
			wantLocator: map[string]string{
				// AP names its authors with a semantic attribute, not a
				// position, so the locator has to carry a predicate.
				"authors": `@property="article:author"`,
			},
		},
		{
			name: "planetrugby",
			glob: "testdata/pr-article-*.html",
			urls: map[string]string{
				"pr-article-1.html": "https://www.planetrugby.com/news/all-blacks-greatest-rivalry-tour-squad-five-takeaways-from-dave-rennies-selections",
				"pr-article-2.html": "https://www.planetrugby.com/news/all-blacks-legend-cheslin-kolbe-is-up-there-as-one-of-the-greatest-players-of-all-time",
				"pr-article-3.html": "https://www.planetrugby.com/news/all-blacks-recalled-flyer-ready-to-make-up-for-lost-time-on-greatest-rivalry-tour",
			},
			wantValues: map[string]string{
				"published": "2026-07-28T04:01:47",
				"authors":   "Jared Wright",
				"title":     "All Blacks",
			},
			wantLocator: map[string]string{
				"authors": `@name="author"`,
			},
		},
	}
}

// load builds a graph from a site's fixtures.
func (s site) load(t *testing.T) *wom.WOM {
	t.Helper()
	files, err := filepath.Glob(s.glob)
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures for %s (glob %q): %v", s.name, s.glob, err)
	}
	sort.Strings(files)

	w := wom.New()
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		uri, ok := s.urls[filepath.Base(f)]
		if !ok {
			t.Fatalf("no url recorded for fixture %s", f)
		}
		if err := w.AddBody(uri, "text/html", body); err != nil {
			t.Fatalf("AddBody %s: %v", f, err)
		}
	}
	if w.Len() != len(files) {
		t.Fatalf("graph holds %d documents, want %d", w.Len(), len(files))
	}
	return w
}

// results is the analysis artifact written to testdata/results. It holds the
// full model alongside the data that model extracts, which is what makes it
// useful both as a golden file and as something to read.
type results struct {
	Site    string       `json:"site"`
	URLs    []string     `json:"urls"`
	Model   *wom.Model   `json:"model"`
	Records []wom.Record `json:"records"`
}

func TestNewsSites(t *testing.T) {
	for _, s := range newsSites() {
		t.Run(s.name, func(t *testing.T) {
			w := s.load(t)

			model, err := w.Model(articleSchema()...)
			if err != nil {
				t.Fatalf("Model: %v", err)
			}
			if len(model.Items) == 0 {
				t.Fatal("model located nothing")
			}

			article, ok := findItem(model.Items, "article")
			if !ok {
				t.Fatalf("no \"article\" item; got %s", itemsString(model.Items))
			}
			t.Logf("\n%s", article.Tree())

			// Every field in the schema must have been located.
			for _, want := range []string{"title", "authors", "published", "modified"} {
				if _, ok := findItem(article.Items, want); !ok {
					t.Errorf("field %q was not located", want)
				}
			}

			// Locators must be specific enough to identify the value, not
			// just the neighbourhood it lives in.
			for field, substr := range s.wantLocator {
				child, ok := findItem(article.Items, field)
				if !ok {
					continue
				}
				loc := child.XPath + " " + child.Selector
				if !strings.Contains(loc, substr) {
					t.Errorf("%s locator = %q, want it to contain %q", field, loc, substr)
				}
			}

			// Applying the model back to the graph must yield the real data.
			records := model.Extract(w)
			if len(records) == 0 {
				t.Fatal("Extract returned nothing")
			}
			byField := collectValues(records)
			for field, want := range s.wantValues {
				if !anyContains(byField[field], want) {
					t.Errorf("%s extracted %v, want one containing %q", field, byField[field], want)
				}
			}

			// One record per article.
			if len(records) != len(s.urls) {
				t.Errorf("extracted %d records, want %d", len(records), len(s.urls))
			}

			writeResults(t, s, model, records)
		})
	}
}

// TestNewsModelRoundTrip checks that a model survives a trip through disk and
// still extracts the same data — the property the whole save/load split rests
// on.
func TestNewsModelRoundTrip(t *testing.T) {
	s := newsSites()[0]
	w := s.load(t)

	model, err := w.Model(articleSchema()...)
	if err != nil {
		t.Fatalf("Model: %v", err)
	}
	before := collectValues(model.Extract(w))

	path := filepath.Join(t.TempDir(), "model.json")
	if saveErr := model.Save(path); saveErr != nil {
		t.Fatalf("Save: %v", saveErr)
	}
	loaded, err := wom.LoadModel(path)
	if err != nil {
		t.Fatalf("LoadModel: %v", err)
	}

	if len(loaded.Items) != len(model.Items) {
		t.Fatalf("loaded %d items, want %d", len(loaded.Items), len(model.Items))
	}
	if len(loaded.Schema) != len(model.Schema) {
		t.Errorf("loaded %d schema props, want %d", len(loaded.Schema), len(model.Schema))
	}

	// The loaded model never saw the induction; it works purely from stored
	// locators applied to a fresh graph.
	fresh := s.load(t)
	after := collectValues(loaded.Extract(fresh))
	for field, want := range before {
		if strings.Join(after[field], "|") != strings.Join(want, "|") {
			t.Errorf("%s after round trip = %v, want %v", field, after[field], want)
		}
	}
}

// TestNewsFind checks the lookup half of the API: naming a record returns it
// whole, naming fields prunes to those fields.
func TestNewsFind(t *testing.T) {
	s := newsSites()[0]
	model, err := s.load(t).Model(articleSchema()...)
	if err != nil {
		t.Fatalf("Model: %v", err)
	}

	if got := model.Find(); len(got) != len(model.Items) {
		t.Errorf("Find() returned %d items, want all %d", len(got), len(model.Items))
	}

	whole := model.Find("article")
	if len(whole) != 1 || len(whole[0].Items) != 4 {
		t.Fatalf("Find(\"article\") = %d items with %d fields, want 1 with 4",
			len(whole), len(whole[0].Items))
	}

	pruned := model.Find("title", "published")
	if len(pruned) != 1 {
		t.Fatalf("Find(\"title\", \"published\") = %d items, want 1", len(pruned))
	}
	if len(pruned[0].Items) != 2 {
		t.Errorf("pruned record has %d fields, want 2: %s", len(pruned[0].Items), pruned[0].Tree())
	}
	// Nesting is preserved, so the pruned locator still means something.
	if pruned[0].Name != "article" {
		t.Errorf("pruned root = %q, want the enclosing record", pruned[0].Name)
	}

	if dotted := model.Find("article.authors"); len(dotted) != 1 || len(dotted[0].Items) != 1 {
		t.Errorf("Find(\"article.authors\") did not resolve the dotted name: %v", dotted)
	}
	if none := model.Find("nonexistent"); len(none) != 0 {
		t.Errorf("Find(\"nonexistent\") = %v, want nothing", none)
	}
}

// TestNewsTrain checks that training produces a usable chain and leaves the
// locators alone — the chain is the only part of a model that transfers.
func TestNewsTrain(t *testing.T) {
	s := newsSites()[0]
	w := s.load(t)

	model, err := w.Model(articleSchema()...)
	if err != nil {
		t.Fatalf("Model: %v", err)
	}
	if model.Chain != nil {
		t.Error("a freshly induced model should carry no trained chain")
	}
	locatorsBefore := locatorSummary(model.Items)

	if err := model.Train(w); err != nil {
		t.Fatalf("Train: %v", err)
	}
	if model.Chain == nil {
		t.Fatal("Train produced no chain")
	}
	if model.Chain.Fields != 4 {
		t.Errorf("chain has %d fields, want 4", model.Chain.Fields)
	}
	for i, row := range model.Chain.Trans {
		var sum float64
		for _, p := range row {
			if p < 0 || p > 1 {
				t.Errorf("trans[%d] holds %v, outside [0,1]", i, p)
			}
			sum += p
		}
		if sum < 0.999 || sum > 1.001 {
			t.Errorf("trans row %d sums to %v, want 1", i, sum)
		}
	}
	if got := locatorSummary(model.Items); got != locatorsBefore {
		t.Error("Train changed the locators; it must only learn the chain")
	}

	// A trained chain is portable: it must load into a fresh engine.
	reused := wom.New(wom.WithChainPrior(model.Chain))
	if reused == nil {
		t.Fatal("WithChainPrior produced no engine")
	}
}

func TestNewsTrainWithoutRecord(t *testing.T) {
	t.Parallel()

	s := newsSites()[0]
	w := s.load(t)
	// A flat schema has no record, so there is no field sequence to learn.
	model, err := w.Model(wom.Prop{Name: "title", Aliases: []string{"headline"}})
	if err != nil {
		t.Fatalf("Model: %v", err)
	}
	if err := model.Train(w); err == nil {
		t.Error("Train on a schema with no record: want an error")
	}
}

// writeResults stores the model and extracted data as JSON, and compares
// locators against the stored copy so a change in inference shows up as a test
// failure rather than a silent drift.
func writeResults(t *testing.T, s site, model *wom.Model, records []wom.Record) {
	t.Helper()

	urls := make([]string, 0, len(s.urls))
	for _, u := range s.urls {
		urls = append(urls, u)
	}
	sort.Strings(urls)

	path := filepath.Join("testdata", "results", s.name+".json")
	if *update {
		buf, err := json.MarshalIndent(results{
			Site: s.name, URLs: urls, Model: model, Records: records,
		}, "", "  ")
		if err != nil {
			t.Fatalf("encode results: %v", err)
		}
		if err := os.WriteFile(path, append(buf, '\n'), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		t.Logf("wrote %s", path)
		return
	}

	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s (run `go test -run TestNewsSites -update` to create it): %v", path, err)
	}
	var golden results
	if err := json.Unmarshal(buf, &golden); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	// Probabilities move when scoring is tuned; locators are the contract.
	if got, want := locatorSummary(model.Items), locatorSummary(golden.Model.Items); got != want {
		t.Errorf("locators changed.\n got: %s\nwant: %s\n(run with -update if this is intended)", got, want)
	}
}

// locatorSummary renders the addressing parts of an item tree, ignoring the
// scores that legitimately move between tuning passes.
func locatorSummary(items []wom.Item) string {
	var b strings.Builder
	var walk func([]wom.Item, int)
	walk = func(items []wom.Item, depth int) {
		for _, it := range items {
			b.WriteString(strings.Repeat(" ", depth))
			b.WriteString(it.Name + " | " + it.Format.String() + " | " + it.URI + " | " +
				it.XPath + " | " + it.Selector + " | " + it.Path + " | " + it.Regex + "\n")
			walk(it.Items, depth+1)
		}
	}
	walk(items, 0)
	return b.String()
}

// collectValues flattens extracted records into field name -> values.
func collectValues(records []wom.Record) map[string][]string {
	out := make(map[string][]string)
	var walk func([]wom.Record)
	walk = func(rs []wom.Record) {
		for _, r := range rs {
			if r.Value != "" {
				out[r.Name] = append(out[r.Name], r.Value)
			}
			walk(r.Items)
		}
	}
	walk(records)
	for k := range out {
		sort.Strings(out[k])
	}
	return out
}

func anyContains(values []string, want string) bool {
	for _, v := range values {
		if strings.Contains(v, want) {
			return true
		}
	}
	return false
}
