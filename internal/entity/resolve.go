// SPDX-License-Identifier: GPL-3.0-or-later

package entity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Identity resolution: deciding that two names are one entity.
//
// # Nothing merges by itself
//
// Merging two people who are not the same person corrupts the graph in a way
// that is very hard to notice. The rows still look right, the counts go up, and
// the store's one claim, that two jobs agree who Acme is, has quietly become
// false. Failing to merge is visible: the same person is in the list twice and
// somebody says so. So the asymmetry is built in rather than tuned: proposing a
// merge and making one are two calls, [Store.Candidates] never writes, and
// [Store.Assert] is exactly as literal as it was before this file existed.
//
// # A merge is recorded, not applied
//
// Rewriting the losing entity's rows would be less code and would destroy the
// evidence: what the two spellings were, who asserted each, and that anybody
// ever thought them distinct. All three are what you need when the merge was
// wrong, which is the case worth designing for. So a merge is one row in
// `aliases` pointing at the canonical id, carrying its own provenance and the
// rule that proposed it, and every read joins through it.
//
// That keeps the two properties the rest of the store is built on. Every row is
// still an assertion, the merge included. One job's contribution is still one
// delete, because [Store.Retract] takes back that job's merges along with its
// assertions and rebuilds the counts from what is left.
//
// # Aliases are flattened, so reads are one join
//
// Merging into something already merged away merges into its canonical instead,
// and anything pointing at the loser is re-pointed. An alias therefore never
// points at another alias. The cost is that undoing a merge that absorbed a
// family returns the family to the survivor rather than to the shape it had;
// what it buys is that following a merge is one join in five queries instead of
// a recursive walk in each, and a readable SELECT is the argument that won every
// other storage decision here.
//
// # What is deliberately not here
//
// No edit distance. A threshold that merges "Jon Smith" with "Jan Smith" is one
// character away from a threshold that does not, nobody can say which side of
// it a given pair should fall, and the failures are exactly the silent kind
// above. Nothing that needs a corpus, a model or a network call either: this
// has to be cheap enough to run per record and explainable afterwards.

// The rules a merge can be made under, recorded on the alias so that a rule
// which turns out to be wrong can be found and undone as a class rather than a
// row at a time.
const (
	// RuleManual is a person saying so. Always safe in the sense that matters:
	// somebody is accountable for it.
	RuleManual = "manual"

	// RuleInitial is an initial and a surname against exactly one full name.
	// Safe only because of the "exactly one" (see [Store.Candidates]).
	RuleInitial = "initial"
)

// Candidate is a merge worth making, proposed and not made.
type Candidate struct {
	// From loses its identity, To keeps it. The fuller spelling is always the
	// one that keeps it: "Alex Doe" tells a reader who this is and "A. Doe"
	// does not, and the store's names are read by people.
	From string
	To   string

	// Entity is the store's side of the pair, so a caller can show what it is
	// about to merge with rather than an id.
	Entity *Entity

	// Rule is which rule proposed this, to be recorded on the merge.
	Rule string
}

// Candidates proposes the entities a name might already be, and merges nothing.
//
// Two calls rather than one because the decision is not the store's to make: a
// crawl that merged as it went would make thousands of them unattended, and the
// first wrong one is invisible.
//
// The rules, and why each is or is not safe:
//
// **Exact match after normalisation** is not proposed, because it is not a
// merge. [ID] already derives one id from the normalised name, so two jobs
// writing "Alex Doe" and "  alex   doe  " are one row before anything asks. It
// is safe because case and whitespace carry no meaning in a name, which is the
// only claim it makes.
//
// **An initial and a surname against exactly one full name** is proposed:
// "A. Doe" and "Alex Doe" where the store knows no other Doe whose forename
// begins with A. It is safe only because of the counting. Two candidates means
// A. Doe is Alex or Anna and the evidence cannot say which, and guessing there
// is precisely how a graph gets poisoned: the wrong articles attach to the
// wrong person, and every later question is answered confidently and wrongly.
// So two candidates propose nothing at all, rather than proposing the more
// asserted one, which would be a popularity contest dressed as evidence.
//
// Middle names are ignored, first token and last token only. They appear and
// disappear between sources with no signal in either direction, so treating
// their absence as evidence of anything would be inventing information.
//
// Nothing here is fuzzy. See the note at the top of this file.
func (s *Store) Candidates(ctx context.Context, kind, name string) ([]Candidate, error) {
	asked, ok := parseName(name)
	if !ok {
		return nil, nil
	}

	// The whole kind, reasoned about in memory rather than in SQL, because the
	// rule is a question about the set (how many others could this be) and not
	// about a row. Entities are bounded by definition, which is why they are
	// safe to be dimensions and why this is safe to load.
	known, err := s.Kind(ctx, kind)
	if err != nil {
		return nil, err
	}

	self := ID(kind, name)

	var fulls, initials []*Entity
	for _, one := range known {
		if one.ID == self {
			continue
		}
		parsed, ok := parseName(one.Name)
		if !ok || parsed.surname != asked.surname || parsed.letter != asked.letter {
			continue
		}
		if parsed.initial {
			initials = append(initials, one)
		} else {
			fulls = append(fulls, one)
		}
	}

	if asked.initial {
		// The name asked about is the initial. It can only be merged away if
		// there is one full name it could belong to.
		if len(fulls) != 1 {
			return nil, nil
		}
		return []Candidate{{From: self, To: fulls[0].ID, Entity: fulls[0], Rule: RuleInitial}}, nil
	}

	// The name asked about is the full one, and any stored initial that could
	// be it is a candidate. The same counting applies from the other side: an
	// initial with another full name competing for it is ambiguous, and the
	// name asked about counts as one of the competitors whether or not it has
	// been asserted yet.
	if len(fulls) > 0 {
		return nil, nil
	}
	out := make([]Candidate, 0, len(initials))
	for _, one := range initials {
		out = append(out, Candidate{From: one.ID, To: self, Entity: one, Rule: RuleInitial})
	}
	return out, nil
}

// Merge records that two entities are one.
//
// Neither row is rewritten and neither loses its assertions. What is written is
// that `from` is now read as `to`, by whom, on what evidence, and under which
// rule. [Store.Unmerge] takes it back.
//
// Merging something already merged merges its whole family, and merging a pair
// that is already one entity does nothing, so a caller acting on a proposal it
// made a moment ago does not have to hold a lock to be correct.
//
// Two kinds are never merged. A person who is also a company is a mistake
// somewhere upstream, and collapsing them would spread it.
func (s *Store) Merge(ctx context.Context, from, to, rule string, said Provenance) error {
	if from == "" || to == "" {
		return errors.New("entity: a merge needs two entities")
	}
	if said.Job == "" || said.URL == "" {
		return errors.New("entity: an assertion needs to say who said it and where")
	}
	if said.At.IsZero() {
		said.At = time.Now().UTC()
	}
	if rule == "" {
		rule = RuleManual
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("entity: %w", err)
	}
	defer tx.Rollback()

	loser, loserKind, err := canonical(ctx, tx, from)
	if err != nil {
		return err
	}
	keeper, keeperKind, err := canonical(ctx, tx, to)
	if err != nil {
		return err
	}

	if loser == keeper {
		return nil
	}
	if loserKind != keeperKind {
		return fmt.Errorf("entity: refusing to merge a %s into a %s", loserKind, keeperKind)
	}

	// Anything that pointed at the loser now points at the keeper, which is
	// what keeps an alias one join from its canonical.
	if _, err := tx.ExecContext(ctx,
		`UPDATE aliases SET canonical = ? WHERE canonical = ?`, keeper, loser); err != nil {
		return fmt.Errorf("entity: merge: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO aliases (alias, canonical, rule, job, url, spec, said_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		loser, keeper, rule, said.Job, said.URL, said.Spec, said.At.UnixNano()); err != nil {
		return fmt.Errorf("entity: merge: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("entity: %w", err)
	}
	return nil
}

// Unmerge takes a merge back, which is the point of recording it as one row.
//
// The entity gets its own identity back with its assertions and its provenance
// intact, because neither was ever moved. What does not come back is the shape
// of a chain: a family that was re-pointed when this merge was made stays with
// the entity it was re-pointed to, since flattening happened on write.
func (s *Store) Unmerge(ctx context.Context, alias string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM aliases WHERE alias = ?`, alias)
	if err != nil {
		return fmt.Errorf("entity: unmerge: %w", err)
	}
	if removed, _ := result.RowsAffected(); removed == 0 {
		return fmt.Errorf("entity: %q was not merged into anything", alias)
	}
	return nil
}

// Alias is one spelling that was merged into another, with the evidence.
type Alias struct {
	// ID and Name are the entity that lost its identity, kept so that what a
	// merge collapsed is still legible afterwards.
	ID   string
	Name string

	// Canonical is what it is read as now.
	Canonical string

	// Rule is which rule proposed it, and Said is who made it and when.
	Rule string
	Said Provenance
}

// Aliases lists the spellings merged into an entity, most recent first.
//
// What somebody reviewing a merge reads, and what makes [Store.Unmerge]
// callable: the ids to undo are here rather than in whatever log the merging
// process happened to write.
func (s *Store) Aliases(ctx context.Context, id string) ([]Alias, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT a.alias, e.name, a.canonical, a.rule, a.job, a.url, a.spec, a.said_at
  FROM aliases a JOIN entities e ON e.id = a.alias
 WHERE a.canonical = (SELECT canonical FROM resolved WHERE id = ?)
 ORDER BY a.said_at DESC, e.name ASC`, id)
	if err != nil {
		return nil, fmt.Errorf("entity: aliases: %w", err)
	}
	defer rows.Close()

	var out []Alias
	for rows.Next() {
		var (
			one Alias
			at  int64
		)
		if err := rows.Scan(&one.ID, &one.Name, &one.Canonical, &one.Rule,
			&one.Said.Job, &one.Said.URL, &one.Said.Spec, &at); err != nil {
			return nil, fmt.Errorf("entity: aliases: %w", err)
		}
		one.Said.At = time.Unix(0, at).UTC()
		out = append(out, one)
	}
	return out, rows.Err()
}

// Canonical is what an id is read as, which is itself unless it was merged.
func (s *Store) Canonical(ctx context.Context, id string) (string, error) {
	var out string
	err := s.db.QueryRowContext(ctx,
		`SELECT canonical FROM resolved WHERE id = ?`, id).Scan(&out)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", fmt.Errorf("entity: no such entity %q", id)
	case err != nil:
		return "", fmt.Errorf("entity: %w", err)
	}
	return out, nil
}

// canonical resolves an id inside a transaction, and reports its kind so a
// merge can refuse to cross one.
func canonical(ctx context.Context, tx *sql.Tx, id string) (string, string, error) {
	var found, kind string
	err := tx.QueryRowContext(ctx, `
SELECT c.id, c.kind
  FROM resolved r JOIN entities c ON c.id = r.canonical
 WHERE r.id = ?`, id).Scan(&found, &kind)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", "", fmt.Errorf("entity: no such entity %q", id)
	case err != nil:
		return "", "", fmt.Errorf("entity: %w", err)
	}
	return found, kind, nil
}

// parsed is as much of a name as the initial rule looks at.
type parsed struct {
	// surname is the last token, letter is the first token's first rune, and
	// initial says the first token was only that letter.
	surname string
	letter  rune
	initial bool
}

// parseName splits a name for the initial rule, and reports whether it is the
// shape the rule can say anything about.
//
// One token is not: "Cher" and "Chertoff" share nothing the rule could use, and
// a surname on its own has no forename to compare. That is a refusal rather
// than a fallback, because a rule that quietly widened when it ran out of
// evidence would be the fuzzy matching this file exists to avoid.
func parseName(name string) (parsed, bool) {
	fields := strings.Fields(normalise(name))
	if len(fields) < 2 {
		return parsed{}, false
	}

	fore := []rune(strings.TrimSuffix(fields[0], "."))
	if len(fore) == 0 {
		return parsed{}, false
	}
	return parsed{
		surname: fields[len(fields)-1],
		letter:  fore[0],
		initial: len(fore) == 1,
	}, true
}
