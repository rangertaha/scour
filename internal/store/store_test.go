// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/rangertaha/scour/internal/schedule"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

	if _, err := s.CreateItem(context.Background(), "vehicle"); err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
}

func TestCreateItemIsIdempotent(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	first, err := s.CreateItem(ctx, "vehicle")
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	second, err := s.CreateItem(ctx, "vehicle")
	if err != nil {
		t.Fatalf("CreateItem again: %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("ids %d and %d differ; adding to an item twice must not fork it", first.ID, second.ID)
	}

	rows, err := s.Items(ctx)
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d items, want 1", len(rows))
	}
}

func TestItemNotFound(t *testing.T) {
	s := open(t)
	_, err := s.Item(context.Background(), "absent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestAddIsIdempotentThroughout(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	e, err := s.CreateItem(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := s.AddAlias(ctx, e.ID, "car"); err != nil {
			t.Fatalf("AddAlias: %v", err)
		}
		if err := s.AddTarget(ctx, jobOf(t, s, e).ID, TargetDomain, "example.com", true, 5); err != nil {
			t.Fatalf("AddTarget: %v", err)
		}
		if err := s.AddContentType(ctx, jobOf(t, s, e).ID, "html"); err != nil {
			t.Fatalf("AddContentType: %v", err)
		}
	}

	full, err := s.ItemFull(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Aliases) != 1 {
		t.Errorf("aliases = %d, want 1", len(full.Aliases))
	}
	if got := full.AllTargets(); len(got) != 1 {
		t.Errorf("targets = %d, want 1", len(got))
	}
	if len(full.Jobs) != 1 {
		t.Fatalf("jobs = %d, want the one implicit job", len(full.Jobs))
	}
	if len(full.Jobs[0].ContentTypes) != 1 {
		t.Errorf("content types = %d, want 1", len(full.Jobs[0].ContentTypes))
	}
}

func TestAddPropertyUpdatesTheExample(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	e, err := s.CreateItem(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddProperty(ctx, e.ID, "make", "", "Ford"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProperty(ctx, e.ID, "make", "string", "Chevrolet"); err != nil {
		t.Fatal(err)
	}

	full, err := s.ItemFull(ctx, "vehicle")
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

	e, err := s.CreateItem(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddTarget(ctx, jobOf(t, s, e).ID, TargetDomain, "example.com", false, 3); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTarget(ctx, jobOf(t, s, e).ID, TargetDomain, "example.com", true, 7); err != nil {
		t.Fatal(err)
	}

	full, err := s.ItemFull(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	if len(full.AllTargets()) != 1 {
		t.Fatalf("targets = %d, want 1", len(full.AllTargets()))
	}
	if !full.AllTargets()[0].Subdomains || full.AllTargets()[0].Depth != 7 {
		t.Errorf("target = %+v, want the updated depth and subdomains", full.AllTargets()[0])
	}
}

func TestEmptyNamesAreRejected(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	if _, err := s.CreateItem(ctx, "  "); err == nil {
		t.Error("an empty item name must be rejected")
	}
	e, err := s.CreateItem(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddAlias(ctx, e.ID, " "); err == nil {
		t.Error("an empty alias must be rejected")
	}
	if err := s.AddProperty(ctx, e.ID, "", "", "Ford"); err == nil {
		t.Error("an empty property name must be rejected")
	}
	if err := s.AddTarget(ctx, jobOf(t, s, e).ID, TargetURL, "", false, 0); err == nil {
		t.Error("an empty target must be rejected")
	}
}

func TestDeleteItemRemovesEverythingBelowIt(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	e, err := s.CreateItem(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddAlias(ctx, e.ID, "car"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProperty(ctx, e.ID, "make", "", "Ford"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTarget(ctx, jobOf(t, s, e).ID, TargetDomain, "example.com", false, 0); err != nil {
		t.Fatal(err)
	}

	rec := Record{ItemID: e.ID, Fingerprint: "abc", Label: Unlabelled}
	if err := s.DB().Create(&rec).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB().Create(&Value{RecordID: rec.ID, Prop: "make", Text: "Ford"}).Error; err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteItem(ctx, "vehicle"); err != nil {
		t.Fatalf("DeleteItem: %v", err)
	}

	for _, tc := range []struct {
		name  string
		model any
	}{
		{"aliases", &Alias{}},
		{"properties", &Property{}},
		{"jobs", &Job{}},
		{"records", &Record{}},
	} {
		var n int64
		if err := s.DB().Model(tc.model).Where("item_id = ?", e.ID).Count(&n).Error; err != nil {
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

	e, err := s.CreateItem(ctx, "vehicle")
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
	if err := s.DeleteItem(ctx, "absent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteItem err = %v, want ErrNotFound", err)
	}
}

func TestItemsCountsMatches(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	vehicle, err := s.CreateItem(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateItem(ctx, "article"); err != nil {
		t.Fatal(err)
	}

	for _, fp := range []string{"a", "b", "c"} {
		if err := s.DB().Create(&Record{ItemID: vehicle.ID, Fingerprint: fp, Label: Unlabelled}).Error; err != nil {
			t.Fatal(err)
		}
	}

	rows, err := s.Items(ctx)
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
	e, err := s.CreateItem(ctx, "news")
	if err != nil {
		t.Fatal(err)
	}

	// The item's default schema.
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
		t.Errorf("title example = %q, want the item default", got)
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
	e, err := s.CreateItem(ctx, "news")
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
// (item_id, name) kept the two-column index and every property upsert failed
// with "ON CONFLICT clause does not match any PRIMARY KEY or UNIQUE
// constraint". Opening it again has to repair that.
func TestOpeningRepairsAStaleUniqueIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scour.db")

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Put the pre-domain index back, which is what an older database holds.
	if err := s.db.Exec("DROP INDEX IF EXISTS idx_prop_item_name").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Exec(
		"CREATE UNIQUE INDEX idx_prop_item_name ON properties(item_id, name)").Error; err != nil {
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
	e, err := reopened.CreateItem(ctx, "news")
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

// A column added to an existing table is NULL for every row already in it,
// while every new write stores "" for "no domain". In sqlite those differ, so
// the upsert missed the original and inserted a duplicate, and PropertiesFor
// asked for domain = ” and could not see the original at all. On a real
// database that became `wom: duplicate prop "link"` and a silently empty
// schema.
func TestOpeningSettlesDomainsAddedToExistingRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scour.db")

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	e, err := s.CreateItem(ctx, "news")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddPropertyDetail(ctx, e.ID, PropertyDetail{
		Name: "link", Example: "https://example.com/a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddPropertyAlias(ctx, e.ID, "", "link", "canonical"); err != nil {
		t.Fatal(err)
	}

	// Put the database back into the shape an upgrade leaves it in: the
	// original row with a NULL domain, and the duplicate the missed upsert
	// then inserted.
	if err := s.db.Exec("UPDATE properties SET domain = NULL WHERE name = 'link'").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Exec(
		"INSERT INTO properties (item_id, domain, name, example) VALUES (?, '', 'link', '')",
		e.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	props, err := reopened.PropertiesFor(ctx, e.ID, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(props) != 1 {
		t.Fatalf("got %d link properties, want the duplicate merged away: %+v", len(props), props)
	}
	if props[0].Example != "https://example.com/a" {
		t.Errorf("example = %q, want the original row kept", props[0].Example)
	}
	// Aliases are the expensive part, so they move to the survivor.
	if len(props[0].Aliases) != 1 || props[0].Aliases[0].Word != "canonical" {
		t.Errorf("aliases = %+v, want canonical carried over", props[0].Aliases)
	}
}

// Handing an item out used to delete it, so a crawler that died between taking
// a URL and fetching it lost that URL with no trace. A lease makes that cost a
// retry instead.
func TestALeasedItemComesBackIfNothingReportsIt(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	e, err := s.CreateItem(ctx, "news")
	if err != nil {
		t.Fatal(err)
	}
	hash := URLHash(e.ID, "http://example.com/a")
	if err := s.PushQueue(ctx, e.ID, 1, hash, []byte("req")); err != nil {
		t.Fatal(err)
	}

	// Taken, and now in flight: not handed out twice and not counted as
	// waiting.
	if _, err := s.LeaseQueue(ctx, e.ID, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LeaseQueue(ctx, e.ID, time.Minute); !errors.Is(err, ErrQueueEmpty) {
		t.Errorf("a leased item was handed out twice: %v", err)
	}
	if n, err := s.QueueSize(ctx, e.ID); err != nil || n != 0 {
		t.Errorf("QueueSize = %d (err %v), want 0 while in flight", n, err)
	}

	// The crawler dies. The lease expires and the URL is available again.
	past := time.Now().UTC().Add(-time.Second)
	if err := s.DB().Model(&QueueItem{}).Where("hash = ?", hash).
		Update("leased_until", past).Error; err != nil {
		t.Fatal(err)
	}
	if n, err := s.QueueSize(ctx, e.ID); err != nil || n != 1 {
		t.Errorf("QueueSize = %d (err %v), want 1 once the lease expired", n, err)
	}
	got, err := s.LeaseQueue(ctx, e.ID, time.Minute)
	if err != nil {
		t.Fatalf("an expired lease should be handed out again: %v", err)
	}
	if string(got) != "req" {
		t.Errorf("got %q, want the original request back", got)
	}
}

// Recording the outcome is what finishes a frontier item, and both a successful
// fetch and a failed one arrive through RecordFetch.
func TestRecordingAFetchReleasesTheFrontierItem(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	e, err := s.CreateItem(ctx, "news")
	if err != nil {
		t.Fatal(err)
	}

	for _, status := range []URLStatus{URLFetched, URLFailed} {
		raw := "http://example.com/" + string(status)
		hash := URLHash(e.ID, raw)
		if err := s.PushQueue(ctx, e.ID, 1, hash, []byte("req")); err != nil {
			t.Fatal(err)
		}
		if _, err := s.LeaseQueue(ctx, e.ID, time.Minute); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordFetch(ctx, Fetched{
			ItemID: e.ID, URL: raw, Status: status, StatusCode: 200,
		}); err != nil {
			t.Fatal(err)
		}
		var n int64
		if err := s.DB().Model(&QueueItem{}).Where("hash = ?", hash).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s: the frontier item survived its own outcome", status)
		}
	}
}

// Not every hand-out ends in a fetch: colly declines a request it has already
// visited, and a declined request reports no outcome, so nothing releases it.
// Without a limit it would return on every lease expiry and be declined again
// for as long as the crawl ran.
func TestAnItemNothingReportsIsEventuallyAbandoned(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	e, err := s.CreateItem(ctx, "news")
	if err != nil {
		t.Fatal(err)
	}
	hash := URLHash(e.ID, "http://example.com/declined")
	if err := s.PushQueue(ctx, e.ID, 1, hash, []byte("req")); err != nil {
		t.Fatal(err)
	}

	expire := func() {
		past := time.Now().UTC().Add(-time.Second)
		if err := s.DB().Model(&QueueItem{}).Where("hash = ?", hash).
			Update("leased_until", past).Error; err != nil {
			t.Fatal(err)
		}
	}

	for i := range MaxAttempts {
		if _, err := s.LeaseQueue(ctx, e.ID, time.Minute); err != nil {
			t.Fatalf("hand-out %d: %v", i+1, err)
		}
		expire()
	}
	if _, err := s.LeaseQueue(ctx, e.ID, time.Minute); !errors.Is(err, ErrQueueEmpty) {
		t.Errorf("err = %v, want the item abandoned after %d attempts", err, MaxAttempts)
	}
}

// Politeness is owed to a server, not to a crawl. A rate limit inside a crawler
// bounds what that process does; with several crawlers a site sees the sum, and
// no crawler can see the others. Handing work out is the one place that can
// bound what a site actually receives.
func TestLeasingSkipsHostsAskedTooRecently(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	e, err := s.CreateItem(ctx, "news")
	if err != nil {
		t.Fatal(err)
	}

	push := func(raw string) {
		t.Helper()
		data, err := json.Marshal(map[string]any{"URL": raw})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PushQueue(ctx, e.ID, 1, URLHash(e.ID, raw), data); err != nil {
			t.Fatal(err)
		}
	}
	push("http://busy.example/a")
	push("http://busy.example/b")
	push("http://other.example/a")

	// The host is read from the request, which is what makes pacing possible.
	var item QueueItem
	if err := s.DB().Where("hash = ?", URLHash(e.ID, "http://busy.example/a")).
		First(&item).Error; err != nil {
		t.Fatal(err)
	}
	if item.Host != "busy.example" {
		t.Fatalf("host = %q, want busy.example", item.Host)
	}

	// With that host cooling, only the other one is handed out, however many
	// of its URLs are waiting and whatever their score.
	data, err := s.LeaseQueueSkipping(ctx, e.ID, time.Minute, []string{"busy.example"})
	if err != nil {
		t.Fatalf("nothing handed out while another host was ready: %v", err)
	}
	if got := hostOfRequest(data); got != "other.example" {
		t.Fatalf("handed out %q while it was cooling", got)
	}
	if _, err := s.LeaseQueueSkipping(ctx, e.ID, time.Minute, []string{"busy.example"}); !errors.Is(err, ErrQueueEmpty) {
		t.Errorf("err = %v, want nothing left once the ready host is exhausted", err)
	}

	// Once it has cooled, its work is available again.
	got, err := s.LeaseQueueSkipping(ctx, e.ID, time.Minute, nil)
	if err != nil {
		t.Fatalf("a cooled host was not handed out: %v", err)
	}
	if h := hostOfRequest(got); h != "busy.example" {
		t.Errorf("handed out %q, want busy.example", h)
	}
}

// Pausing keeps everything: the frontier, its order and its leases. It stops
// work being handed out, and nothing else.
func TestPausingHidesAnItemFromTheDispatcher(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	busy, err := s.CreateItem(ctx, "busy")
	if err != nil {
		t.Fatal(err)
	}
	quiet, err := s.CreateItem(ctx, "quiet")
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range []*Item{busy, quiet} {
		raw := "http://example.com/" + e.Name
		if err := s.PushQueue(ctx, e.ID, 1, URLHash(e.ID, raw), []byte(`{"URL":"`+raw+`"}`)); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := s.QueuedItems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("got %d items with work, want 2", len(ids))
	}

	if err := s.SetPaused(ctx, quiet.ID, true); err != nil {
		t.Fatal(err)
	}
	ids, err = s.QueuedItems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != busy.ID {
		t.Errorf("got %v, want only the item that is not paused", ids)
	}

	// The frontier is untouched, so resuming carries on rather than restarting.
	var queued int64
	if err := s.DB().Model(&QueueItem{}).Where("item_id = ?", quiet.ID).
		Count(&queued).Error; err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Errorf("pausing discarded %d queued items", 1-queued)
	}

	if err := s.SetPaused(ctx, quiet.ID, false); err != nil {
		t.Fatal(err)
	}
	if ids, err = s.QueuedItems(ctx); err != nil || len(ids) != 2 {
		t.Errorf("resuming did not bring the item back: %v %v", ids, err)
	}
}

// A live view has to work with no broker configured, which is the ordinary case
// on one machine, so the rate comes from the fetch timestamps.
func TestFetchRateComesFromTheDatabase(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	e, err := s.CreateItem(ctx, "news")
	if err != nil {
		t.Fatal(err)
	}

	if rate, err := s.FetchRate(ctx, e.ID, 10*time.Second); err != nil || rate != 0 {
		t.Errorf("rate = %v (err %v), want 0 before anything is fetched", rate, err)
	}

	for i := range 20 {
		raw := fmt.Sprintf("http://example.com/%d", i)
		if err := s.RecordFetch(ctx, Fetched{
			ItemID: e.ID, URL: raw, Status: URLFetched, StatusCode: 200,
		}); err != nil {
			t.Fatal(err)
		}
	}

	rate, err := s.FetchRate(ctx, e.ID, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if rate != 2 {
		t.Errorf("rate = %v, want 20 pages over a 10s window to read as 2/s", rate)
	}

	// A window that predates them all sees nothing, which is what makes the
	// number a rate rather than a total.
	old, err := s.FetchRate(ctx, e.ID, time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	if old != 0 {
		t.Errorf("rate over a past window = %v, want 0", old)
	}
}

// A property type the engine does not know must be refused where it is given.
//
// It used to be stored and only refused when a schema was built out of it, so a
// typo in --prop-type survived the crawl and surfaced at train time as a
// complaint about a property nobody had touched since.
func TestUnknownPropertyTypeIsRefused(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	item, err := s.CreateItem(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}

	err = s.AddPropertyDetail(ctx, item.ID, PropertyDetail{Name: "make", Type: "strng"})
	if err == nil {
		t.Fatal("a misspelled type must be refused")
	}
	if !strings.Contains(err.Error(), "string") {
		t.Errorf("the refusal should name the types that work: %v", err)
	}

	// Nothing was written.
	props, err := s.PropertiesFor(ctx, item.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(props) != 0 {
		t.Errorf("the property was stored despite the bad type: %+v", props)
	}

	// Every advertised type is accepted, and so is none at all.
	for _, ty := range append(PropertyTypes(), "") {
		if err := s.AddPropertyDetail(ctx, item.ID, PropertyDetail{Name: "p" + ty, Type: ty}); err != nil {
			t.Errorf("type %q was refused: %v", ty, err)
		}
	}
}

// Teaching a property one thing must not cost it the others.
//
// Adding a label used to blank the example, because type and example were
// written on every upsert whether or not they were given. The example is what
// the first round of matching is bootstrapped from, so a property could be
// quietly demoted to a bare name by being told something new about it.
func TestTeachingOneDetailKeepsTheRest(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	item, err := s.CreateItem(ctx, "news")
	if err != nil {
		t.Fatal(err)
	}

	full := PropertyDetail{
		Name: "title", Type: "string", Example: "A headline",
		Regex: `^.{5,}$`, Description: "the headline",
	}
	if err := s.AddPropertyDetail(ctx, item.ID, full); err != nil {
		t.Fatal(err)
	}

	// Each of these adds one detail and names nothing else.
	for _, add := range []PropertyDetail{
		{Name: "title", Label: `^(og:|twitter:)?title$`},
		{Name: "title", Example: "Another headline"},
		{Name: "title", Type: "string"},
		{Name: "title"},
	} {
		if err := s.AddPropertyDetail(ctx, item.ID, add); err != nil {
			t.Fatalf("%+v: %v", add, err)
		}
	}

	props, err := s.PropertiesFor(ctx, item.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(props) != 1 {
		t.Fatalf("got %d properties, want 1", len(props))
	}
	got := props[0]
	for _, tc := range []struct{ what, got, want string }{
		{"type", got.Type, "string"},
		{"example", got.Example, "Another headline"},
		{"regex", got.Regex, `^.{5,}$`},
		{"label", got.Label, `^(og:|twitter:)?title$`},
		{"description", got.Description, "the headline"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.what, tc.got, tc.want)
		}
	}
}

// The scheduling order has to reach the query, or a pluggable scheduler is a
// setting that changes nothing.
func TestLeaseOrderFollowsTheSchedulingPolicy(t *testing.T) {
	ctx := context.Background()

	// A frontier each: leasing consumes, since ReleaseQueue means the URL is
	// done rather than put back, so one queue cannot answer three questions.
	for _, tc := range []struct {
		order schedule.Order
		want  string
	}{
		{schedule.ByScore, "http://example.com/best"},
		{schedule.Breadth, "http://example.com/first"},
		{schedule.Depth, "http://example.com/best"},
	} {
		t.Run(tc.order.String(), func(t *testing.T) {
			s := open(t)
			e, err := s.CreateItem(ctx, "vehicle")
			if err != nil {
				t.Fatal(err)
			}
			// Queued oldest first with the best score last, so insertion order
			// and score order disagree and the answer says which was used.
			for _, q := range []struct {
				url   string
				score float64
			}{
				{"http://example.com/first", 0.1},
				{"http://example.com/second", 0.5},
				{"http://example.com/best", 0.9},
			} {
				data, err := json.Marshal(map[string]any{"URL": q.url})
				if err != nil {
					t.Fatal(err)
				}
				if err := s.PushQueue(ctx, e.ID, q.score, URLHash(e.ID, q.url), data); err != nil {
					t.Fatal(err)
				}
			}

			data, err := s.LeaseQueueOrdered(ctx, e.ID, time.Minute, nil, tc.order)
			if err != nil {
				t.Fatalf("lease: %v", err)
			}
			var got struct{ URL string }
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if got.URL != tc.want {
				t.Errorf("%s handed out %s, want %s", tc.order, got.URL, tc.want)
			}
		})
	}
}

// The unchanged entry point must still mean what it always did, or every crawl
// changes behaviour on upgrade.
func TestLeaseQueueStillMeansBestFirst(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	e, err := s.CreateItem(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct {
		url   string
		score float64
	}{
		{"http://example.com/first", 0.1},
		{"http://example.com/best", 0.9},
	} {
		data, err := json.Marshal(map[string]any{"URL": q.url})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PushQueue(ctx, e.ID, q.score, URLHash(e.ID, q.url), data); err != nil {
			t.Fatal(err)
		}
	}
	data, err := s.LeaseQueue(ctx, e.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ URL string }
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.URL != "http://example.com/best" {
		t.Errorf("LeaseQueue handed out %s, want the highest score", got.URL)
	}
}

// jobOf returns the implicit job of an item, which is where its targets and
// content types live.
func jobOf(t *testing.T, s *Store, item *Item) *Job {
	t.Helper()
	job, err := s.JobForItem(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	return job
}
