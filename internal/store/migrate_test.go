// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/glebarez/go-sqlite"
)

// oldSchema builds a database the way scour wrote them when an item was called
// an entity, with enough of the shape to exercise the migration: the parent
// table, a child keyed on entity_id, and a grandchild that cascades from the
// child. The grandchild is the point. It is what a table rebuild destroys.
func oldSchema(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	stmts := []string{
		"PRAGMA foreign_keys = ON",
		"CREATE TABLE `entities` (`id` integer PRIMARY KEY AUTOINCREMENT, `name` text NOT NULL, `paused` numeric)",
		"CREATE UNIQUE INDEX `idx_entities_name` ON `entities`(`name`)",

		"CREATE TABLE `properties` (`id` integer PRIMARY KEY AUTOINCREMENT, `entity_id` integer NOT NULL," +
			" `name` text NOT NULL, `type` text, `example` text, `description` text, `domain` text DEFAULT \"\"," +
			" `regex` text, `label` text," +
			" CONSTRAINT `fk_entities_properties` FOREIGN KEY (`entity_id`) REFERENCES `entities`(`id`) ON DELETE CASCADE)",
		"CREATE UNIQUE INDEX `idx_prop_entity_name` ON `properties`(`entity_id`,`domain`,`name`)",

		"CREATE TABLE `property_aliases` (`id` integer PRIMARY KEY AUTOINCREMENT, `property_id` integer NOT NULL," +
			" `word` text NOT NULL," +
			" CONSTRAINT `fk_properties_aliases` FOREIGN KEY (`property_id`) REFERENCES `properties`(`id`) ON DELETE CASCADE)",
		"CREATE UNIQUE INDEX `idx_palias_prop_word` ON `property_aliases`(`property_id`,`word`)",

		"CREATE TABLE `aliases` (`id` integer PRIMARY KEY AUTOINCREMENT, `entity_id` integer NOT NULL," +
			" `word` text NOT NULL," +
			" CONSTRAINT `fk_entities_aliases` FOREIGN KEY (`entity_id`) REFERENCES `entities`(`id`) ON DELETE CASCADE)",
		"CREATE UNIQUE INDEX `idx_alias_entity_word` ON `aliases`(`entity_id`,`word`)",

		"CREATE TABLE `targets` (`id` integer PRIMARY KEY AUTOINCREMENT, `entity_id` integer NOT NULL," +
			" `kind` text NOT NULL, `value` text NOT NULL, `subdomains` numeric, `depth` integer," +
			" CONSTRAINT `fk_entities_targets` FOREIGN KEY (`entity_id`) REFERENCES `entities`(`id`) ON DELETE CASCADE)",
		"CREATE UNIQUE INDEX `idx_target_entity_value` ON `targets`(`entity_id`,`kind`,`value`)",

		"INSERT INTO entities (id, name) VALUES (7, 'news')",
		"INSERT INTO properties (id, entity_id, name, domain, example) VALUES (11, 7, 'author', '', 'Hannah McLeod')",
		"INSERT INTO properties (id, entity_id, name, domain, example) VALUES (12, 7, 'title', 'example.com', 'A headline')",
		"INSERT INTO property_aliases (property_id, word) VALUES (11, 'byline')",
		"INSERT INTO property_aliases (property_id, word) VALUES (11, 'written by')",
		"INSERT INTO property_aliases (property_id, word) VALUES (12, 'headline')",
		"INSERT INTO aliases (entity_id, word) VALUES (7, 'article')",
		"INSERT INTO targets (entity_id, kind, value, subdomains, depth) VALUES (7, 'domain', 'example.com', 0, 0)",
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

// A database written before the rename has to come across whole. The counts are
// the test: a migration that renames everything correctly and loses the rows is
// the failure mode that actually happened.
func TestMigrationFromTheEntitySchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scour.db")
	oldSchema(t, path)

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	item, err := s.Item(ctx, "news")
	if err != nil {
		t.Fatalf("the item did not survive: %v", err)
	}
	if item.ID != 7 {
		t.Errorf("id = %d, want 7: ids must not be renumbered, the frontier references them", item.ID)
	}

	// The grandchild rows. gorm rebuilds a table it cannot alter in place, and
	// dropping the original under enforced foreign keys cascades these away
	// before the rename puts the parent back.
	words, err := s.PropertyAliases(ctx, item.ID, "", "author")
	if err != nil {
		t.Fatalf("property aliases: %v", err)
	}
	if len(words) != 2 {
		t.Errorf("author kept %d of 2 words (%v): the rebuild cascaded them away", len(words), words)
	}

	scoped, err := s.PropertyAliases(ctx, item.ID, "example.com", "title")
	if err != nil {
		t.Fatalf("scoped property aliases: %v", err)
	}
	if len(scoped) != 1 {
		t.Errorf("the domain-scoped property kept %d of 1 words", len(scoped))
	}

	// Targets belong to a job now, and the migration made one per item that
	// had any, named after the item, so the frontier a crawl already built is
	// still reachable under the same name.
	job, err := s.Job(ctx, "news")
	if err != nil {
		t.Fatalf("the item got no job: %v", err)
	}
	targets, err := s.TargetsFor(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 {
		t.Errorf("kept %d of 1 targets", len(targets))
	}
}

// Nothing in the old vocabulary may be left behind, or the next AutoMigrate
// sees a column the model does not have and the two schemas drift apart.
func TestMigrationLeavesNoEntityNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scour.db")
	oldSchema(t, path)

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	var tables []string
	if err := s.db.Raw("SELECT name FROM sqlite_master WHERE type = 'table'").Scan(&tables).Error; err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		var cols []string
		if err := s.db.Raw("SELECT name FROM pragma_table_info(?)", table).Scan(&cols).Error; err != nil {
			t.Fatal(err)
		}
		for _, c := range cols {
			if c == "entity_id" {
				t.Errorf("%s still has an entity_id column", table)
			}
		}
		if table == "entities" {
			t.Error("the entities table is still there")
		}
	}

	var idx []string
	if err := s.db.Raw(
		"SELECT name FROM sqlite_master WHERE type = 'index' AND name LIKE '%entity%'").Scan(&idx).Error; err != nil {
		t.Fatal(err)
	}
	if len(idx) > 0 {
		t.Errorf("indexes still named for entities: %v", idx)
	}
}

// Opening twice must not migrate twice, and must not lose anything the second
// time either.
func TestMigrationIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scour.db")
	oldSchema(t, path)

	for i := range 3 {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i+1, err)
		}
		item, err := s.Item(context.Background(), "news")
		if err != nil {
			t.Fatalf("open %d: item lost: %v", i+1, err)
		}
		words, err := s.PropertyAliases(context.Background(), item.ID, "", "author")
		if err != nil {
			t.Fatalf("open %d: %v", i+1, err)
		}
		if len(words) != 2 {
			t.Fatalf("open %d: author kept %d of 2 words", i+1, len(words))
		}
		s.Close()
	}
}

// Foreign keys have to be enforced once the migrations are done, or the cascade
// constraints in the models silently do nothing for the rest of the process.
func TestForeignKeysAreOnAfterMigrating(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scour.db")
	oldSchema(t, path)

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var on int
	if err := s.db.Raw("PRAGMA foreign_keys").Scan(&on).Error; err != nil {
		t.Fatal(err)
	}
	if on != 1 {
		t.Error("foreign keys are off after migrating, so the cascades do nothing")
	}

	// And they actually bite: removing the item takes its properties and their
	// words with it.
	ctx := context.Background()
	item, err := s.Item(ctx, "news")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteItem(ctx, item.Name); err != nil {
		t.Fatal(err)
	}
	var left int64
	if err := s.db.Raw("SELECT count(*) FROM property_aliases").Scan(&left).Error; err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("%d alias rows outlived the item they belonged to", left)
	}
}
