// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/rangertaha/scour/internal/fuzzy"
	"github.com/rangertaha/scour/internal/wom"
	"gorm.io/gorm/clause"
)

// CreateItem creates an item, or returns the existing one. Adding to an
// item is idempotent throughout, so `scour add` can be run repeatedly
// without a check-then-create dance.
func (s *Store) CreateItem(ctx context.Context, name string) (*Item, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("item name must not be empty")
	}

	e := Item{Name: name}
	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "name"}}, DoNothing: true}).
		Create(&e).Error
	if err != nil {
		return nil, fmt.Errorf("create item %q: %w", name, err)
	}
	if e.ID == 0 {
		return s.Item(ctx, name)
	}
	return &e, nil
}

// Item looks one up by name.
func (s *Store) Item(ctx context.Context, name string) (*Item, error) {
	var e Item
	err := s.db.WithContext(ctx).Where("name = ?", name).First(&e).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, s.noSuchItem(ctx, name)
	}
	if err != nil {
		return nil, fmt.Errorf("get item %q: %w", name, err)
	}
	return &e, nil
}

// ItemFull looks one up with its aliases, properties, targets and content
// types loaded.
func (s *Store) ItemFull(ctx context.Context, name string) (*Item, error) {
	var e Item
	err := s.db.WithContext(ctx).
		Preload("Aliases").Preload("Properties.Aliases").Preload("Properties").
		Preload("Targets").Preload("ContentTypes").
		Where("name = ?", name).First(&e).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, s.noSuchItem(ctx, name)
	}
	if err != nil {
		return nil, fmt.Errorf("get item %q: %w", name, err)
	}
	return &e, nil
}

// noSuchItem reports a name that is not there, naming the closest one that is.
//
// The name was typed, so a near miss is the likely explanation, and saying so
// saves re-reading the listing for the one wrong character. It lives in the
// store rather than in the command so the API and MCP answer the same way.
func (s *Store) noSuchItem(ctx context.Context, name string) error {
	if near := s.nearestItem(ctx, name); near != "" {
		return fmt.Errorf("item %q: %w (did you mean %q?)", name, ErrNotFound, near)
	}
	return fmt.Errorf("item %q: %w", name, ErrNotFound)
}

// nearestItem returns the existing item whose name is closest to the one
// asked for, or "" when none is close enough or the lookup fails.
//
// A failure here is silent on purpose: this runs only to improve a message that
// is already being returned, and reporting that the suggestion could not be
// made would replace a clear error with a confusing one.
func (s *Store) nearestItem(ctx context.Context, name string) string {
	var names []string
	if err := s.db.WithContext(ctx).Model(&Item{}).Pluck("name", &names).Error; err != nil {
		return ""
	}
	return fuzzy.Nearest(name, names)
}

// ItemSummary is one row of the item listing the API and MCP serve.
type ItemSummary struct {
	Name    string
	Matches int
}

// Items lists every item with its match count, ordered by name.
func (s *Store) Items(ctx context.Context) ([]ItemSummary, error) {
	var out []ItemSummary
	err := s.db.WithContext(ctx).
		Model(&Item{}).
		Select("items.name AS name, COUNT(records.id) AS matches").
		Joins("LEFT JOIN records ON records.item_id = items.id").
		Group("items.id").
		Order("items.name").
		Scan(&out).Error
	if err != nil {
		return nil, fmt.Errorf("list items: %w", err)
	}
	return out, nil
}

// DeleteItem removes an item and everything belonging to it.
func (s *Store) DeleteItem(ctx context.Context, name string) error {
	e, err := s.Item(ctx, name)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Records own values, so they go first; the rest are leaves.
		var ids []uint
		if err := tx.Model(&Record{}).Where("item_id = ?", e.ID).Pluck("id", &ids).Error; err != nil {
			return fmt.Errorf("collect records: %w", err)
		}
		if len(ids) > 0 {
			if err := tx.Where("record_id IN ?", ids).Delete(&Value{}).Error; err != nil {
				return fmt.Errorf("delete values: %w", err)
			}
		}

		// Property aliases hang off properties rather than the item, so
		// deleting by item_id would leave them behind for a property id that
		// no longer exists.
		var propIDs []uint
		if err := tx.Model(&Property{}).Where("item_id = ?", e.ID).Pluck("id", &propIDs).Error; err != nil {
			return fmt.Errorf("collect properties: %w", err)
		}
		if len(propIDs) > 0 {
			if err := tx.Where("property_id IN ?", propIDs).Delete(&PropertyAlias{}).Error; err != nil {
				return fmt.Errorf("delete property aliases: %w", err)
			}
		}
		for _, model := range []any{
			&Alias{}, &Property{}, &Target{}, &ContentType{},
			&URL{}, &Rule{}, &Record{}, &ModelMeta{}, &Chain{}, &PageRole{},
		} {
			if err := tx.Where("item_id = ?", e.ID).Delete(model).Error; err != nil {
				return fmt.Errorf("delete %T: %w", model, err)
			}
		}
		if err := tx.Delete(&Item{}, e.ID).Error; err != nil {
			return fmt.Errorf("delete item: %w", err)
		}
		return nil
	})
}

// AddAlias records another word for the item.
func (s *Store) AddAlias(ctx context.Context, itemID uint, word string) error {
	word = strings.TrimSpace(word)
	if word == "" {
		return errors.New("alias must not be empty")
	}
	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&Alias{ItemID: itemID, Word: word}).Error
	if err != nil {
		return fmt.Errorf("add alias %q: %w", word, err)
	}
	return nil
}

// AddProperty records a property and its example value. Adding a property that
// already exists updates the example, so correcting one is a repeat of the
// original command.
func (s *Store) AddProperty(ctx context.Context, itemID uint, name, typ, example string) error {
	return s.AddPropertyDetail(ctx, itemID, PropertyDetail{Name: name, Type: typ, Example: example})
}

// PropertyDetail is everything a property can be taught. It is a struct rather
// than a parameter list because most of it is optional strings, and seven of
// those in a row is an invitation to pass them in the wrong order.
type PropertyDetail struct {
	// Domain scopes the teaching to one site. Empty is the item's default.
	Domain string
	Name   string
	Type   string
	// Example is a value the site actually publishes, which is the strongest
	// signal the matcher has.
	Example string
	// Description says what the field means, in words a page might also use.
	Description string
	// Regex decides which node wins, by rejecting text it does not match, and
	// what that node yields, through capture group one.
	Regex string
	// Label decides which names count, on the naming side of the same choice.
	Label string
}

// AddPropertyDetail records a property along with what it means.
//
// The description is not documentation. The matcher scores how far a page's
// label context overlaps a property's description, so the words chosen here
// are read by the model that locates the field.
func (s *Store) AddPropertyDetail(ctx context.Context, itemID uint, d PropertyDetail) error {
	name := strings.TrimSpace(d.Name)
	if name == "" {
		return errors.New("property name must not be empty")
	}
	domain := NormaliseDomain(d.Domain)

	// A type the engine does not know must fail here too. It was accepted and
	// stored, and only refused when a schema was built out of it, so a typo in
	// --prop-type survived the crawl and surfaced at train time as a complaint
	// about a property nobody had touched since.
	if !wom.Type(d.Type).Valid() {
		return fmt.Errorf("property %q has unknown type %q, want one of: %s",
			name, d.Type, strings.Join(PropertyTypes(), ", "))
	}

	// A pattern that does not compile must fail here rather than mid-crawl,
	// where it would look like a site that stopped publishing the field.
	for what, pat := range map[string]string{"regex": d.Regex, "label": d.Label} {
		if pat == "" {
			continue
		}
		if _, err := regexp.Compile(pat); err != nil {
			return fmt.Errorf("property %q %s: %w", name, what, err)
		}
	}

	update := []string{"type", "example"}
	if d.Regex != "" {
		update = append(update, "regex")
	}
	if d.Label != "" {
		update = append(update, "label")
	}
	if d.Description != "" {
		// An empty description must not blank one already recorded: adding an
		// example to a templated property should not cost it its meaning.
		update = append(update, "description")
	}

	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "item_id"}, {Name: "domain"}, {Name: "name"}},
			DoUpdates: clause.AssignmentColumns(update),
		}).
		Create(&Property{
			ItemID: itemID, Domain: domain, Name: name, Type: d.Type,
			Example: d.Example, Description: d.Description,
			Regex: d.Regex, Label: d.Label,
		}).Error
	if err != nil {
		return fmt.Errorf("add property %q: %w", name, err)
	}
	return nil
}

// AddPropertyAlias records another word a page might label a property with.
func (s *Store) AddPropertyAlias(ctx context.Context, itemID uint, domain, propName, word string) error {
	word = strings.TrimSpace(word)
	if word == "" {
		return errors.New("property alias must not be empty")
	}
	domain = NormaliseDomain(domain)

	// An alias hangs off the property row, so a domain-scoped alias needs the
	// domain-scoped row to exist first. Teaching a word without having taught
	// an example is the ordinary case, so the row is created rather than
	// demanded.
	var prop Property
	err := s.db.WithContext(ctx).
		Where("item_id = ? AND domain = ? AND name = ?", itemID, domain, strings.TrimSpace(propName)).
		First(&prop).Error
	if errors.Is(err, gorm.ErrRecordNotFound) && domain != "" {
		if err = s.AddPropertyDetail(ctx, itemID, PropertyDetail{Domain: domain, Name: propName}); err != nil {
			return err
		}
		err = s.db.WithContext(ctx).
			Where("item_id = ? AND domain = ? AND name = ?", itemID, domain, strings.TrimSpace(propName)).
			First(&prop).Error
	}
	if err != nil {
		return fmt.Errorf("find property %q: %w", propName, err)
	}

	err = s.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&PropertyAlias{PropertyID: prop.ID, Word: word}).Error
	if err != nil {
		return fmt.Errorf("add alias %q to %q: %w", word, propName, err)
	}
	return nil
}

// AddTarget records a crawl target.
func (s *Store) AddTarget(ctx context.Context, itemID uint, kind TargetKind, value string, subdomains bool, depth int) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("target must not be empty")
	}
	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "item_id"}, {Name: "kind"}, {Name: "value"}},
			DoUpdates: clause.AssignmentColumns([]string{"subdomains", "depth"}),
		}).
		Create(&Target{
			ItemID: itemID, Kind: kind, Value: value,
			Subdomains: subdomains, Depth: depth,
		}).Error
	if err != nil {
		return fmt.Errorf("add target %q: %w", value, err)
	}
	return nil
}

// AddContentType restricts the item's crawls to a content type.
func (s *Store) AddContentType(ctx context.Context, itemID uint, typ string) error {
	typ = strings.TrimSpace(typ)
	if typ == "" {
		return errors.New("content type must not be empty")
	}
	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&ContentType{ItemID: itemID, Type: typ}).Error
	if err != nil {
		return fmt.Errorf("add content type %q: %w", typ, err)
	}
	return nil
}

// DeleteTarget removes one target by value, whichever kind it is.
func (s *Store) DeleteTarget(ctx context.Context, itemID uint, value string) error {
	res := s.db.WithContext(ctx).
		Where("item_id = ? AND value = ?", itemID, value).
		Delete(&Target{})
	if res.Error != nil {
		return fmt.Errorf("delete target %q: %w", value, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("target %q: %w", value, ErrNotFound)
	}
	return nil
}

// DeleteProperty removes one property by name, in every domain it was taught.
func (s *Store) DeleteProperty(ctx context.Context, itemID uint, name string) error {
	res := s.db.WithContext(ctx).
		Where("item_id = ? AND name = ?", itemID, name).
		Delete(&Property{})
	if res.Error != nil {
		return fmt.Errorf("delete property %q: %w", name, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("property %q: %w", name, ErrNotFound)
	}
	return nil
}

// DeleteRule removes one induced rule by id.
func (s *Store) DeleteRule(ctx context.Context, itemID, ruleID uint) error {
	res := s.db.WithContext(ctx).
		Where("item_id = ? AND id = ?", itemID, ruleID).
		Delete(&Rule{})
	if res.Error != nil {
		return fmt.Errorf("delete rule %d: %w", ruleID, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("rule %d: %w", ruleID, ErrNotFound)
	}
	return nil
}

// TargetBatch is how many targets are inserted per statement.
//
// SQLite binds a variable per column per row, so the batch has to stay well
// under the statement variable limit. Five hundred rows of five columns is
// comfortably inside it and already gets most of the benefit.
const TargetBatch = 500

// AddTargets records many targets in one transaction.
//
// One statement per row is fine for a handful and hopeless for a list: measured
// on a real file of news sites, inserting one at a time ran at 284 rows a
// second, which is nearly an hour for a million URLs, almost all of it spent
// committing a transaction per row rather than doing work.
func (s *Store) AddTargets(
	ctx context.Context,
	itemID uint,
	kind TargetKind,
	values []string,
	subdomains bool,
	depth int,
) (int, error) {
	if len(values) == 0 {
		return 0, nil
	}

	rows := make([]Target, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		// A duplicate inside one batch would make the upsert update a row it
		// is inserting in the same statement, which sqlite refuses.
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		rows = append(rows, Target{
			ItemID: itemID, Kind: kind, Value: value,
			Subdomains: subdomains, Depth: depth,
		})
	}
	if len(rows) == 0 {
		return 0, nil
	}

	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "item_id"}, {Name: "kind"}, {Name: "value"}},
			DoUpdates: clause.AssignmentColumns([]string{"subdomains", "depth"}),
		}).
		CreateInBatches(&rows, TargetBatch).Error
	if err != nil {
		return 0, fmt.Errorf("add %d targets: %w", len(rows), err)
	}
	return len(rows), nil
}

// NormaliseDomain reduces a domain to its bare host, so example.com,
// www.example.com and https://example.com/ all scope to the same site.
//
// It mirrors the crawler's own normalisation, because a property taught for a
// domain and a target added for that domain have to agree on what the domain
// is, or the teaching silently applies to nothing.
func NormaliseDomain(raw string) string {
	host := strings.TrimSpace(strings.ToLower(raw))
	if host == "" {
		return ""
	}
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	return strings.TrimPrefix(host, "www.")
}

// PropertiesFor returns the schema to apply on one domain: the item's
// properties, with anything taught for that domain replacing its default.
//
// Teaching is an override rather than an addition. A site that calls the byline
// something unusual should change what `author` means there and nowhere else,
// and a property taught on a domain the crawl never reaches should change
// nothing at all.
func (s *Store) PropertiesFor(ctx context.Context, itemID uint, domain string) ([]Property, error) {
	domain = NormaliseDomain(domain)

	var rows []Property
	err := s.db.WithContext(ctx).
		Preload("Aliases").
		Where("item_id = ? AND (domain = ? OR domain = ?)", itemID, "", domain).
		Order("name").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("properties for %q: %w", domain, err)
	}

	byName := make(map[string]Property, len(rows))
	var order []string
	for _, r := range rows {
		if _, seen := byName[r.Name]; !seen {
			order = append(order, r.Name)
		}
		// The domain-scoped row wins, whichever order they arrived in.
		if cur, seen := byName[r.Name]; !seen || r.Domain != "" || cur.Domain == "" {
			if !seen || r.Domain != "" {
				byName[r.Name] = r
			}
		}
	}
	out := make([]Property, 0, len(order))
	for _, n := range order {
		out = append(out, byName[n])
	}
	return out, nil
}

// TargetsFor returns an item's crawl targets.
func (s *Store) TargetsFor(ctx context.Context, itemID uint) ([]Target, error) {
	var out []Target
	err := s.db.WithContext(ctx).
		Where("item_id = ?", itemID).
		Order("id").
		Find(&out).Error
	if err != nil {
		return nil, fmt.Errorf("targets for item %d: %w", itemID, err)
	}
	return out, nil
}

// ItemByID looks one up by id.
func (s *Store) ItemByID(ctx context.Context, id uint) (*Item, error) {
	var e Item
	err := s.db.WithContext(ctx).First(&e, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("item %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get item %d: %w", id, err)
	}
	return &e, nil
}

// SetPaused stops or resumes work for an item.
//
// Pausing does not discard anything: the frontier keeps its order and its
// leases, so resuming carries on rather than starting again. It is the same
// promise a spent budget makes.
func (s *Store) SetPaused(ctx context.Context, itemID uint, paused bool) error {
	err := s.db.WithContext(ctx).
		Model(&Item{}).
		Where("id = ?", itemID).
		Update("paused", paused).Error
	if err != nil {
		return fmt.Errorf("set paused for item %d: %w", itemID, err)
	}
	return nil
}

// IsPaused reports whether an item is paused.
func (s *Store) IsPaused(ctx context.Context, itemID uint) (bool, error) {
	var e Item
	err := s.db.WithContext(ctx).Select("paused").First(&e, itemID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read paused for item %d: %w", itemID, err)
	}
	return e.Paused, nil
}

// FetchRate is how many pages an item fetched in the last window, as a rate
// per second.
//
// Derived from the fetch timestamps rather than from the metric stream, so a
// live view works with no broker configured. That is the ordinary case on one
// machine, where each process would otherwise embed a broker of its own and see
// nothing published by anyone else.
func (s *Store) FetchRate(ctx context.Context, itemID uint, window time.Duration) (float64, error) {
	if window <= 0 {
		return 0, nil
	}
	var n int64
	err := s.db.WithContext(ctx).
		Model(&URL{}).
		Where("item_id = ? AND fetched_at IS NOT NULL AND fetched_at >= ?",
			itemID, time.Now().UTC().Add(-window)).
		Count(&n).Error
	if err != nil {
		return 0, fmt.Errorf("fetch rate for item %d: %w", itemID, err)
	}
	return float64(n) / window.Seconds(), nil
}

// findProperty locates one property row exactly, without the domain fallback
// PropertiesFor applies. Editing tags has to act on the row that was named:
// falling back to the unscoped row would let `--delete` on a domain silently
// strip a word every other domain still relies on.
func (s *Store) findProperty(ctx context.Context, itemID uint, domain, name string) (*Property, error) {
	domain = NormaliseDomain(domain)
	name = strings.TrimSpace(name)

	var prop Property
	err := s.db.WithContext(ctx).
		Where("item_id = ? AND domain = ? AND name = ?", itemID, domain, name).
		First(&prop).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		where := ""
		if domain != "" {
			where = " on " + domain
		}
		return nil, fmt.Errorf("property %q%s: %w", name, where, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("find property %q: %w", name, err)
	}
	return &prop, nil
}

// PropertyAliases returns the words taught for one property, in order.
func (s *Store) PropertyAliases(ctx context.Context, itemID uint, domain, propName string) ([]string, error) {
	prop, err := s.findProperty(ctx, itemID, domain, propName)
	if err != nil {
		return nil, err
	}
	var rows []PropertyAlias
	if err := s.db.WithContext(ctx).
		Where("property_id = ?", prop.ID).Order("word").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("aliases for %q: %w", propName, err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Word)
	}
	return out, nil
}

// RemovePropertyAliases deletes words from a property, returning how many went.
func (s *Store) RemovePropertyAliases(ctx context.Context, itemID uint, domain, propName string, words []string) (int64, error) {
	prop, err := s.findProperty(ctx, itemID, domain, propName)
	if err != nil {
		return 0, err
	}
	cleaned := trimAll(words)
	if len(cleaned) == 0 {
		return 0, nil
	}
	res := s.db.WithContext(ctx).
		Where("property_id = ? AND word IN ?", prop.ID, cleaned).
		Delete(&PropertyAlias{})
	if res.Error != nil {
		return 0, fmt.Errorf("remove aliases from %q: %w", propName, res.Error)
	}
	return res.RowsAffected, nil
}

// SetPropertyAliases replaces a property's words with exactly the ones given.
//
// It is one transaction because the intermediate state is a property with no
// words at all, which a crawl running alongside would read as "match nothing"
// and quietly stop extracting the field.
func (s *Store) SetPropertyAliases(ctx context.Context, itemID uint, domain, propName string, words []string) error {
	prop, err := s.findProperty(ctx, itemID, domain, propName)
	if err != nil {
		return err
	}
	cleaned := trimAll(words)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("property_id = ?", prop.ID).Delete(&PropertyAlias{}).Error; err != nil {
			return fmt.Errorf("clear aliases on %q: %w", propName, err)
		}
		if len(cleaned) == 0 {
			return nil
		}
		rows := make([]PropertyAlias, 0, len(cleaned))
		for _, w := range cleaned {
			rows = append(rows, PropertyAlias{PropertyID: prop.ID, Word: w})
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error; err != nil {
			return fmt.Errorf("set aliases on %q: %w", propName, err)
		}
		return nil
	})
}

// trimAll drops blanks and repeats, so a caller can pass whatever the command
// line gave it.
func trimAll(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, w := range in {
		w = strings.TrimSpace(w)
		if w == "" || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}

// PropertyTypes are the value types a property may declare, in the order the
// help lists them.
//
// Named here rather than spelled into a flag's usage string, so the thing that
// checks and the thing that documents cannot drift apart.
func PropertyTypes() []string {
	return []string{
		string(wom.TypeString), string(wom.TypeNumber), string(wom.TypeBool),
		string(wom.TypeDate), string(wom.TypeURL), string(wom.TypeEmail),
	}
}
