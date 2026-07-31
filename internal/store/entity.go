// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CreateEntity creates an entity, or returns the existing one. Adding to an
// entity is idempotent throughout, so `scour add` can be run repeatedly
// without a check-then-create dance.
func (s *Store) CreateEntity(ctx context.Context, name string) (*Entity, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("entity name must not be empty")
	}

	e := Entity{Name: name}
	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "name"}}, DoNothing: true}).
		Create(&e).Error
	if err != nil {
		return nil, fmt.Errorf("create entity %q: %w", name, err)
	}
	if e.ID == 0 {
		return s.Entity(ctx, name)
	}
	return &e, nil
}

// Entity looks one up by name.
func (s *Store) Entity(ctx context.Context, name string) (*Entity, error) {
	var e Entity
	err := s.db.WithContext(ctx).Where("name = ?", name).First(&e).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("entity %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get entity %q: %w", name, err)
	}
	return &e, nil
}

// EntityFull looks one up with its aliases, properties, targets and content
// types loaded.
func (s *Store) EntityFull(ctx context.Context, name string) (*Entity, error) {
	var e Entity
	err := s.db.WithContext(ctx).
		Preload("Aliases").Preload("Properties.Aliases").Preload("Properties").
		Preload("Targets").Preload("ContentTypes").
		Where("name = ?", name).First(&e).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("entity %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get entity %q: %w", name, err)
	}
	return &e, nil
}

// EntitySummary is one row of the entity listing the API and MCP serve.
type EntitySummary struct {
	Name    string
	Matches int
}

// Entities lists every entity with its match count, ordered by name.
func (s *Store) Entities(ctx context.Context) ([]EntitySummary, error) {
	var out []EntitySummary
	err := s.db.WithContext(ctx).
		Model(&Entity{}).
		Select("entities.name AS name, COUNT(records.id) AS matches").
		Joins("LEFT JOIN records ON records.entity_id = entities.id").
		Group("entities.id").
		Order("entities.name").
		Scan(&out).Error
	if err != nil {
		return nil, fmt.Errorf("list entities: %w", err)
	}
	return out, nil
}

// DeleteEntity removes an entity and everything belonging to it.
func (s *Store) DeleteEntity(ctx context.Context, name string) error {
	e, err := s.Entity(ctx, name)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Records own values, so they go first; the rest are leaves.
		var ids []uint
		if err := tx.Model(&Record{}).Where("entity_id = ?", e.ID).Pluck("id", &ids).Error; err != nil {
			return fmt.Errorf("collect records: %w", err)
		}
		if len(ids) > 0 {
			if err := tx.Where("record_id IN ?", ids).Delete(&Value{}).Error; err != nil {
				return fmt.Errorf("delete values: %w", err)
			}
		}

		// Property aliases hang off properties rather than the entity, so
		// deleting by entity_id would leave them behind for a property id that
		// no longer exists.
		var propIDs []uint
		if err := tx.Model(&Property{}).Where("entity_id = ?", e.ID).Pluck("id", &propIDs).Error; err != nil {
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
			if err := tx.Where("entity_id = ?", e.ID).Delete(model).Error; err != nil {
				return fmt.Errorf("delete %T: %w", model, err)
			}
		}
		if err := tx.Delete(&Entity{}, e.ID).Error; err != nil {
			return fmt.Errorf("delete entity: %w", err)
		}
		return nil
	})
}

// AddAlias records another word for the entity.
func (s *Store) AddAlias(ctx context.Context, entityID uint, word string) error {
	word = strings.TrimSpace(word)
	if word == "" {
		return errors.New("alias must not be empty")
	}
	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&Alias{EntityID: entityID, Word: word}).Error
	if err != nil {
		return fmt.Errorf("add alias %q: %w", word, err)
	}
	return nil
}

// AddProperty records a property and its example value. Adding a property that
// already exists updates the example, so correcting one is a repeat of the
// original command.
func (s *Store) AddProperty(ctx context.Context, entityID uint, name, typ, example string) error {
	return s.AddPropertyDetail(ctx, entityID, PropertyDetail{Name: name, Type: typ, Example: example})
}

// PropertyDetail is everything a property can be taught. It is a struct rather
// than a parameter list because most of it is optional strings, and seven of
// those in a row is an invitation to pass them in the wrong order.
type PropertyDetail struct {
	// Domain scopes the teaching to one site. Empty is the entity's default.
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
func (s *Store) AddPropertyDetail(ctx context.Context, entityID uint, d PropertyDetail) error {
	name := strings.TrimSpace(d.Name)
	if name == "" {
		return errors.New("property name must not be empty")
	}
	domain := NormaliseDomain(d.Domain)

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
			Columns:   []clause.Column{{Name: "entity_id"}, {Name: "domain"}, {Name: "name"}},
			DoUpdates: clause.AssignmentColumns(update),
		}).
		Create(&Property{
			EntityID: entityID, Domain: domain, Name: name, Type: d.Type,
			Example: d.Example, Description: d.Description,
			Regex: d.Regex, Label: d.Label,
		}).Error
	if err != nil {
		return fmt.Errorf("add property %q: %w", name, err)
	}
	return nil
}

// AddPropertyAlias records another word a page might label a property with.
func (s *Store) AddPropertyAlias(ctx context.Context, entityID uint, domain, propName, word string) error {
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
		Where("entity_id = ? AND domain = ? AND name = ?", entityID, domain, strings.TrimSpace(propName)).
		First(&prop).Error
	if errors.Is(err, gorm.ErrRecordNotFound) && domain != "" {
		if err = s.AddPropertyDetail(ctx, entityID, PropertyDetail{Domain: domain, Name: propName}); err != nil {
			return err
		}
		err = s.db.WithContext(ctx).
			Where("entity_id = ? AND domain = ? AND name = ?", entityID, domain, strings.TrimSpace(propName)).
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
func (s *Store) AddTarget(ctx context.Context, entityID uint, kind TargetKind, value string, subdomains bool, depth int) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("target must not be empty")
	}
	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "entity_id"}, {Name: "kind"}, {Name: "value"}},
			DoUpdates: clause.AssignmentColumns([]string{"subdomains", "depth"}),
		}).
		Create(&Target{
			EntityID: entityID, Kind: kind, Value: value,
			Subdomains: subdomains, Depth: depth,
		}).Error
	if err != nil {
		return fmt.Errorf("add target %q: %w", value, err)
	}
	return nil
}

// AddContentType restricts the entity's crawls to a content type.
func (s *Store) AddContentType(ctx context.Context, entityID uint, typ string) error {
	typ = strings.TrimSpace(typ)
	if typ == "" {
		return errors.New("content type must not be empty")
	}
	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&ContentType{EntityID: entityID, Type: typ}).Error
	if err != nil {
		return fmt.Errorf("add content type %q: %w", typ, err)
	}
	return nil
}

// DeleteTarget removes one target by value, whichever kind it is.
func (s *Store) DeleteTarget(ctx context.Context, entityID uint, value string) error {
	res := s.db.WithContext(ctx).
		Where("entity_id = ? AND value = ?", entityID, value).
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
func (s *Store) DeleteProperty(ctx context.Context, entityID uint, name string) error {
	res := s.db.WithContext(ctx).
		Where("entity_id = ? AND name = ?", entityID, name).
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
func (s *Store) DeleteRule(ctx context.Context, entityID, ruleID uint) error {
	res := s.db.WithContext(ctx).
		Where("entity_id = ? AND id = ?", entityID, ruleID).
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
	entityID uint,
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
			EntityID: entityID, Kind: kind, Value: value,
			Subdomains: subdomains, Depth: depth,
		})
	}
	if len(rows) == 0 {
		return 0, nil
	}

	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "entity_id"}, {Name: "kind"}, {Name: "value"}},
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

// PropertiesFor returns the schema to apply on one domain: the entity's
// properties, with anything taught for that domain replacing its default.
//
// Teaching is an override rather than an addition. A site that calls the byline
// something unusual should change what `author` means there and nowhere else,
// and a property taught on a domain the crawl never reaches should change
// nothing at all.
func (s *Store) PropertiesFor(ctx context.Context, entityID uint, domain string) ([]Property, error) {
	domain = NormaliseDomain(domain)

	var rows []Property
	err := s.db.WithContext(ctx).
		Preload("Aliases").
		Where("entity_id = ? AND (domain = ? OR domain = ?)", entityID, "", domain).
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

// TargetsFor returns an entity's crawl targets.
func (s *Store) TargetsFor(ctx context.Context, entityID uint) ([]Target, error) {
	var out []Target
	err := s.db.WithContext(ctx).
		Where("entity_id = ?", entityID).
		Order("id").
		Find(&out).Error
	if err != nil {
		return nil, fmt.Errorf("targets for entity %d: %w", entityID, err)
	}
	return out, nil
}

// EntityByID looks one up by id.
func (s *Store) EntityByID(ctx context.Context, id uint) (*Entity, error) {
	var e Entity
	err := s.db.WithContext(ctx).First(&e, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("entity %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get entity %d: %w", id, err)
	}
	return &e, nil
}
