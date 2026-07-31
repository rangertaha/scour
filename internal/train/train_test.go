// SPDX-License-Identifier: GPL-3.0-or-later

package train

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/config"
	"github.com/rangertaha/scour/internal/store"
)

// corpus is a small site whose car pages all share one markup shape, which is
// what induction needs to find a repeating record.
var corpus = map[string]string{
	"http://example.test/cars/one/":   page("Ford", "F-Series", "2026"),
	"http://example.test/cars/two/":   page("Chevrolet", "Silverado", "2025"),
	"http://example.test/cars/three/": page("Toyota", "Tacoma", "2026"),
	"http://example.test/cars/four/":  page("Ram", "1500", "2024"),
	"http://example.test/careers/":    `<html><body><h1>Jobs</h1><p>We are hiring.</p></body></html>`,
}

func page(make, model, year string) string {
	return fmt.Sprintf(`<html><body><div class="vehicle"><dl>
<dt>Make</dt><dd class="make">%s</dd>
<dt>Model</dt><dd class="model">%s</dd>
<dt>Year</dt><dd class="year">%s</dd>
</dl></div></body></html>`, make, model, year)
}

// harness builds an entity whose pages are already crawled and cached, which
// is the state training expects to find.
func harness(t *testing.T) (*Trainer, *store.Store, *store.Entity) {
	t.Helper()
	ctx := context.Background()

	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "scour.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	cfg := config.Default()
	cfg.Paths.Data = dir
	cfg.Paths.Cache = filepath.Join(dir, "cache")
	pages := cache.New(cfg.PagesDir())

	e, err := s.CreateEntity(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddAlias(ctx, e.ID, "car"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct{ name, example string }{
		{"make", "Ford"},
		{"model", "F-Series"},
		{"year", "2026"},
	} {
		if err := s.AddProperty(ctx, e.ID, p.name, "", p.example); err != nil {
			t.Fatal(err)
		}
	}

	for url, body := range corpus {
		key, err := pages.Put(url, []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		err = s.RecordFetch(ctx, store.Fetched{
			EntityID: e.ID, URL: url, Depth: 2, Score: 1,
			Status: store.URLFetched, StatusCode: 200,
			ContentType: "html", Size: int64(len(body)), CacheKey: key,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	full, err := s.EntityFull(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	return New(cfg, s, pages), s, full
}

func TestRunInducesRulesAndExtracts(t *testing.T) {
	tr, s, e := harness(t)
	ctx := context.Background()

	result, err := tr.Run(ctx, e, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Pages != len(corpus) {
		t.Errorf("read %d pages, want %d", result.Pages, len(corpus))
	}
	if result.Rules == 0 {
		t.Fatal("induction produced no rules")
	}
	if result.Records == 0 {
		t.Fatal("extraction produced no records")
	}

	rules, err := s.Rules(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}

	// The rules nest: one container, then a child per property addressed
	// relative to it.
	var parents, children int
	for _, r := range rules {
		if r.ParentID == nil {
			parents++
			continue
		}
		children++
		if r.XPath == "" && r.Path == "" {
			t.Errorf("rule %q has no locator", r.Prop)
		}
	}
	if parents != 1 {
		t.Errorf("got %d container rules, want 1", parents)
	}
	if children == 0 {
		t.Error("no field rules were induced")
	}
}

func TestExtractedValuesAreTheRightWayRound(t *testing.T) {
	tr, s, e := harness(t)
	ctx := context.Background()

	if _, err := tr.Run(ctx, e, Options{}); err != nil {
		t.Fatal(err)
	}

	rows, _, err := s.SearchRecords(ctx, e.ID, store.RecordQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("no records")
	}

	// A locator can match several nodes inside one record; the first is the
	// field's own value. Getting this wrong reports the last field's text
	// under the first field's name, which looks plausible and is nonsense.
	makes := map[string]bool{}
	for _, r := range rows {
		if got := r.Values["make"]; got != "" {
			makes[got] = true
		}
	}
	for _, want := range []string{"Ford", "Chevrolet", "Toyota", "Ram"} {
		if !makes[want] {
			t.Errorf("no record has make %q; got %v", want, keys(makes))
		}
	}
}

func TestRetrainingKeepsIDsAndLabels(t *testing.T) {
	tr, s, e := harness(t)
	ctx := context.Background()

	if _, err := tr.Run(ctx, e, Options{}); err != nil {
		t.Fatal(err)
	}
	before, _, err := s.SearchRecords(ctx, e.ID, store.RecordQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(before) < 2 {
		t.Fatalf("need at least two records to test labelling, got %d", len(before))
	}

	labelled := before[0].ID
	if _, err := s.LabelRecords(ctx, e.ID, []uint{labelled}, store.Invalid); err != nil {
		t.Fatal(err)
	}

	if _, err := tr.Run(ctx, e, Options{}); err != nil {
		t.Fatal(err)
	}

	after, _, err := s.SearchRecords(ctx, e.ID, store.RecordQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("record count changed from %d to %d", len(before), len(after))
	}

	// The ids a user just read off a search have to still mean the same
	// records, or labelling them does the wrong thing.
	ids := map[uint]bool{}
	for _, r := range after {
		ids[r.ID] = true
	}
	for _, r := range before {
		if !ids[r.ID] {
			t.Errorf("record %d was renumbered by retraining", r.ID)
		}
	}

	var found bool
	for _, r := range after {
		if r.ID == labelled {
			found = true
			if r.Label != store.Invalid {
				t.Errorf("label on record %d was lost: %q", r.ID, r.Label)
			}
		}
	}
	if !found {
		t.Errorf("labelled record %d disappeared", labelled)
	}
}

func TestRunWithoutPropertiesExplainsItself(t *testing.T) {
	tr, s, _ := harness(t)
	ctx := context.Background()

	bare, err := s.CreateEntity(ctx, "bare")
	if err != nil {
		t.Fatal(err)
	}
	full, err := s.EntityFull(ctx, "bare")
	if err != nil {
		t.Fatal(err)
	}
	_ = bare

	_, err = tr.Run(ctx, full, Options{})
	if err == nil {
		t.Fatal("training an entity with no properties must fail")
	}
}

func TestRunWithoutCachedPages(t *testing.T) {
	tr, s, _ := harness(t)
	ctx := context.Background()

	if _, err := s.CreateEntity(ctx, "fresh"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProperty(ctx, 2, "make", "", "Ford"); err != nil {
		t.Fatal(err)
	}
	full, err := s.EntityFull(ctx, "fresh")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := tr.Run(ctx, full, Options{}); err == nil {
		t.Error("training with nothing cached must fail rather than produce an empty model")
	}
}

func TestSchemaOfBuildsOneRecordProp(t *testing.T) {
	_, _, e := harness(t)

	props := schemaOf(e)
	if len(props) != 1 {
		t.Fatalf("got %d top-level props, want one record", len(props))
	}
	if props[0].Name != "vehicle" {
		t.Errorf("record name = %q, want the entity name", props[0].Name)
	}
	if len(props[0].Aliases) != 1 || props[0].Aliases[0] != "car" {
		t.Errorf("aliases = %v, want the entity's", props[0].Aliases)
	}
	if len(props[0].Props) != 3 {
		t.Errorf("got %d fields, want one per property", len(props[0].Props))
	}
	for _, p := range props[0].Props {
		if len(p.Examples) == 0 {
			t.Errorf("property %q lost its example, which is the strongest signal induction has", p.Name)
		}
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
