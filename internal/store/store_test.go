// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// open returns a store backed by a file in a temp directory. A file rather
// than :memory: because that is what production uses, and sqlite's behaviour
// differs between the two.
func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "scour.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

func TestOpenCreatesMissingDirectories(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "nested", "deeper", "scour.db")
	s, err := Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if _, err := s.CreateEntity(context.Background(), "vehicle"); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
}

func TestCreateEntityIsIdempotent(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	first, err := s.CreateEntity(ctx, "vehicle")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	second, err := s.CreateEntity(ctx, "vehicle")
	if err != nil {
		t.Fatalf("CreateEntity again: %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("ids %d and %d differ; adding to an entity twice must not fork it", first.ID, second.ID)
	}

	rows, err := s.Entities(ctx)
	if err != nil {
		t.Fatalf("Entities: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d entities, want 1", len(rows))
	}
}

func TestEntityNotFound(t *testing.T) {
	s := open(t)
	_, err := s.Entity(context.Background(), "absent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestAddIsIdempotentThroughout(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	e, err := s.CreateEntity(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := s.AddAlias(ctx, e.ID, "car"); err != nil {
			t.Fatalf("AddAlias: %v", err)
		}
		if err := s.AddTarget(ctx, e.ID, TargetDomain, "example.com", true, 5); err != nil {
			t.Fatalf("AddTarget: %v", err)
		}
		if err := s.AddContentType(ctx, e.ID, "html"); err != nil {
			t.Fatalf("AddContentType: %v", err)
		}
	}

	full, err := s.EntityFull(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Aliases) != 1 {
		t.Errorf("aliases = %d, want 1", len(full.Aliases))
	}
	if len(full.Targets) != 1 {
		t.Errorf("targets = %d, want 1", len(full.Targets))
	}
	if len(full.ContentTypes) != 1 {
		t.Errorf("content types = %d, want 1", len(full.ContentTypes))
	}
}

func TestAddPropertyUpdatesTheExample(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	e, err := s.CreateEntity(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddProperty(ctx, e.ID, "make", "", "Ford"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProperty(ctx, e.ID, "make", "string", "Chevrolet"); err != nil {
		t.Fatal(err)
	}

	full, err := s.EntityFull(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Properties) != 1 {
		t.Fatalf("properties = %d, want 1", len(full.Properties))
	}
	if got := full.Properties[0].Example; got != "Chevrolet" {
		t.Errorf("example = %q, want the corrected value", got)
	}
	if got := full.Properties[0].Type; got != "string" {
		t.Errorf("type = %q, want string", got)
	}
}

func TestAddTargetUpdatesDepthAndSubdomains(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	e, err := s.CreateEntity(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddTarget(ctx, e.ID, TargetDomain, "example.com", false, 3); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTarget(ctx, e.ID, TargetDomain, "example.com", true, 7); err != nil {
		t.Fatal(err)
	}

	full, err := s.EntityFull(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(full.Targets))
	}
	if !full.Targets[0].Subdomains || full.Targets[0].Depth != 7 {
		t.Errorf("target = %+v, want the updated depth and subdomains", full.Targets[0])
	}
}

func TestEmptyNamesAreRejected(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	if _, err := s.CreateEntity(ctx, "  "); err == nil {
		t.Error("an empty entity name must be rejected")
	}
	e, err := s.CreateEntity(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddAlias(ctx, e.ID, " "); err == nil {
		t.Error("an empty alias must be rejected")
	}
	if err := s.AddProperty(ctx, e.ID, "", "", "Ford"); err == nil {
		t.Error("an empty property name must be rejected")
	}
	if err := s.AddTarget(ctx, e.ID, TargetURL, "", false, 0); err == nil {
		t.Error("an empty target must be rejected")
	}
}

func TestDeleteEntityRemovesEverythingBelowIt(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	e, err := s.CreateEntity(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddAlias(ctx, e.ID, "car"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProperty(ctx, e.ID, "make", "", "Ford"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTarget(ctx, e.ID, TargetDomain, "example.com", false, 0); err != nil {
		t.Fatal(err)
	}

	rec := Record{EntityID: e.ID, Fingerprint: "abc", Label: Unlabelled}
	if err := s.DB().Create(&rec).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB().Create(&Value{RecordID: rec.ID, Prop: "make", Text: "Ford"}).Error; err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteEntity(ctx, "vehicle"); err != nil {
		t.Fatalf("DeleteEntity: %v", err)
	}

	for _, tc := range []struct {
		name  string
		model any
	}{
		{"aliases", &Alias{}},
		{"properties", &Property{}},
		{"targets", &Target{}},
		{"records", &Record{}},
	} {
		var n int64
		if err := s.DB().Model(tc.model).Where("entity_id = ?", e.ID).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s left behind: %d rows", tc.name, n)
		}
	}

	var values int64
	if err := s.DB().Model(&Value{}).Where("record_id = ?", rec.ID).Count(&values).Error; err != nil {
		t.Fatal(err)
	}
	if values != 0 {
		t.Errorf("values left behind: %d rows", values)
	}
}

func TestDeleteMissingThingsReportNotFound(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	e, err := s.CreateEntity(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteTarget(ctx, e.ID, "absent.example"); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteTarget err = %v, want ErrNotFound", err)
	}
	if err := s.DeleteProperty(ctx, e.ID, "absent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteProperty err = %v, want ErrNotFound", err)
	}
	if err := s.DeleteRule(ctx, e.ID, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteRule err = %v, want ErrNotFound", err)
	}
	if err := s.DeleteEntity(ctx, "absent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteEntity err = %v, want ErrNotFound", err)
	}
}

func TestEntitiesCountsMatches(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	vehicle, err := s.CreateEntity(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateEntity(ctx, "article"); err != nil {
		t.Fatal(err)
	}

	for _, fp := range []string{"a", "b", "c"} {
		if err := s.DB().Create(&Record{EntityID: vehicle.ID, Fingerprint: fp, Label: Unlabelled}).Error; err != nil {
			t.Fatal(err)
		}
	}

	rows, err := s.Entities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	// Ordered by name, so article comes first with no matches.
	if rows[0].Name != "article" || rows[0].Matches != 0 {
		t.Errorf("row 0 = %+v", rows[0])
	}
	if rows[1].Name != "vehicle" || rows[1].Matches != 3 {
		t.Errorf("row 1 = %+v", rows[1])
	}
}

// Teaching is an override, not an addition: a site that names a field unusually
// changes what the field means there and nowhere else.
func TestPropertiesForResolvesPerDomain(t *testing.T) {
	s, ctx := open(t), context.Background()
	e, err := s.CreateEntity(ctx, "news")
	if err != nil {
		t.Fatal(err)
	}

	// The entity's default schema.
	if err := s.AddPropertyDetail(ctx, e.ID, PropertyDetail{
		Name: "author", Type: "string", Example: "Jane Doe", Description: "who wrote it"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddPropertyDetail(ctx, e.ID, PropertyDetail{
		Name: "title", Type: "string", Example: "A headline"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddPropertyAlias(ctx, e.ID, "", "author", "byline"); err != nil {
		t.Fatal(err)
	}

	// What one site taught.
	if err := s.AddPropertyDetail(ctx, e.ID, PropertyDetail{
		Domain: "example.com", Name: "author", Type: "string",
		Example: "Hannah McLeod", Regex: `^By (.*)$`}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddPropertyAlias(ctx, e.ID, "example.com", "author", "vcard"); err != nil {
		t.Fatal(err)
	}

	taught, err := s.PropertiesFor(ctx, e.ID, "https://www.example.com/some/page")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Property{}
	for _, p := range taught {
		byName[p.Name] = p
	}
	if len(taught) != 2 {
		t.Fatalf("got %d properties, want 2 (the default plus the override)", len(taught))
	}
	if got := byName["author"].Example; got != "Hannah McLeod" {
		t.Errorf("author example = %q, want the taught one", got)
	}
	if got := byName["author"].Regex; got != `^By (.*)$` {
		t.Errorf("author regex = %q, want the taught one", got)
	}
	if got := byName["title"].Example; got != "A headline" {
		t.Errorf("title example = %q, want the entity default", got)
	}

	// An untaught site keeps the defaults untouched.
	other, err := s.PropertiesFor(ctx, e.ID, "other.test")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range other {
		if p.Name == "author" && p.Example != "Jane Doe" {
			t.Errorf("teaching one site changed another: author example = %q", p.Example)
		}
	}
}

// The teaching and the crawl target have to agree on what a domain is, or the
// teaching silently applies to nothing.
func TestNormaliseDomain(t *testing.T) {
	for in, want := range map[string]string{
		"example.com":               "example.com",
		"www.example.com":           "example.com",
		"https://example.com/":      "example.com",
		"https://www.example.com/a": "example.com",
		"EXAMPLE.com":               "example.com",
		"example.com:8080":          "example.com",
		"":                          "",
	} {
		if got := NormaliseDomain(in); got != want {
			t.Errorf("NormaliseDomain(%q) = %q, want %q", in, got, want)
		}
	}
}

// A pattern that does not compile must be refused when it is taught.
func TestTaughtRegexIsValidated(t *testing.T) {
	s, ctx := open(t), context.Background()
	e, err := s.CreateEntity(ctx, "news")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddPropertyDetail(ctx, e.ID, PropertyDetail{
		Name: "title", Regex: "^(unclosed"}); err == nil {
		t.Error("an invalid regex should be rejected")
	}
}

// AutoMigrate adds a column but will not rebuild a unique index whose
// definition changed, so a database created before `domain` joined
// (entity_id, name) kept the two-column index and every property upsert failed
// with "ON CONFLICT clause does not match any PRIMARY KEY or UNIQUE
// constraint". Opening it again has to repair that.
func TestOpeningRepairsAStaleUniqueIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scour.db")

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Put the pre-domain index back, which is what an older database holds.
	if err := s.db.Exec("DROP INDEX IF EXISTS idx_prop_entity_name").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Exec(
		"CREATE UNIQUE INDEX idx_prop_entity_name ON properties(entity_id, name)").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	e, err := reopened.CreateEntity(ctx, "news")
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.AddPropertyDetail(ctx, e.ID, PropertyDetail{
		Name: "link", Example: "https://example.com/a"}); err != nil {
		t.Fatalf("upsert against a repaired database: %v", err)
	}
	// And the domain scoping the index exists for still works.
	if err := reopened.AddPropertyDetail(ctx, e.ID, PropertyDetail{
		Domain: "example.com", Name: "link", Example: "https://example.com/b"}); err != nil {
		t.Fatalf("domain-scoped upsert: %v", err)
	}
	props, err := reopened.PropertiesFor(ctx, e.ID, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(props) != 1 || props[0].Example != "https://example.com/b" {
		t.Errorf("got %+v, want the domain-scoped row to win", props)
	}
}
