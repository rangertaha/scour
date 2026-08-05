// SPDX-License-Identifier: GPL-3.0-or-later

package entity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// What a thing in the graph is about.
//
// # Why a topic is stored as a property
//
// Because it is one. A topic is a statement about a subject, with a value, a
// count and provenance, which is exactly what [Store.Describe] already records:
// reusing it means topics inherit alias resolution, the ordering, Retract and
// the counts rebuilt from what is left, rather than getting a second
// implementation of each that would drift from the first.
//
// The name is reserved, and Describe refuses it, so a job cannot declare an
// ordinary property called "topic" and collide with this. That is the
// difference between a convention and an invariant: the wrong thing is not
// expressible rather than merely discouraged.
//
// # Why a type can carry one
//
// Because "person" being about something is a different claim from any
// particular person being about it, and both are worth making. A type is not an
// entity, so it has no id of its own; [KindID] gives it one, and from then on a
// type is a subject like anything else.
//
// # Why the value is a versioned reference
//
// "climate@7" and not "climate". A topic is a trained classifier, and what it
// recognises changes when somebody retrains it. Recording the bare name would
// mean the graph said an entity was about a subject without saying which
// training decided that, and nothing could be re-examined when the model turned
// out to be wrong. It is the same reason a job pins a version.

// TopicProperty is the reserved property name a topic is recorded under.
//
// Reserved rather than conventional: [Store.Describe] refuses it, so the only
// way a topic gets into the graph is [Store.Tag].
const TopicProperty = "topic"

// KindPrefix marks a subject that is a type rather than a thing.
const KindPrefix = "kind:"

// KindID is what a type resolves to as a subject.
//
// Readable rather than hashed, unlike [ID] and [RelationID]. Those are hashes
// because they have to converge without coordinating: two jobs asserting one
// person must reach the same id from a kind and a name. A type is already its
// own name, so hashing it would buy nothing and cost two things: a properties
// table nobody can read, and no way for the store to tell a real type from a
// mistyped entity id, which is what makes tagging a subject that does not exist
// refusable.
func KindID(kind string) string {
	return KindPrefix + strings.ToLower(strings.TrimSpace(kind))
}

// Tag records that a subject is about a topic.
//
// The subject is an entity id, a relation id, or a [KindID]. The topic is a
// versioned reference as a job writes it, "climate@7", for the reason given
// above.
func (s *graph) Tag(ctx context.Context, subject, topic string, said Provenance) error {
	if strings.TrimSpace(topic) == "" {
		return errors.New("entity: a tag needs a topic")
	}
	// Parsed rather than trusted, so a bare name is refused here instead of
	// becoming a row nothing can re-examine later.
	if _, _, found := strings.Cut(topic, "@"); !found {
		return fmt.Errorf(
			"entity: topic %q has no version, and a topic without one cannot say which training decided it", topic)
	}
	return s.describe(ctx, subject, TopicProperty, topic, 0, said)
}

// Topics is what a subject is about, most-asserted first.
//
// By count rather than by position, unlike properties: a topic has no place in
// a shape somebody wrote, and how often something was classified this way is
// the only ordering the graph knows. Ties break on the reference so two runs
// agree.
func (s *graph) Topics(ctx context.Context, subject string) ([]Property, error) {
	all, err := s.Properties(ctx, subject)
	if err != nil {
		return nil, err
	}

	var out []Property
	for _, one := range all {
		if one.Name == TopicProperty {
			out = append(out, one)
		}
	}
	// Properties come back in the shape's order; topics have none, so they are
	// ordered here by how often they were said.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			if out[j].Assertions > out[j-1].Assertions ||
				(out[j].Assertions == out[j-1].Assertions && out[j].Value < out[j-1].Value) {
				out[j], out[j-1] = out[j-1], out[j]
				continue
			}
			break
		}
	}
	return out, nil
}

// About is everything the graph says is about a topic.
//
// The reverse of [Store.Tag], and the question somebody actually has: not "what
// is this entity about" but "what do we know that is about climate". Entities
// only, because a relation or a type coming back from the same call would make
// the result a list of three different things a caller has to sort out.
func (s *graph) About(ctx context.Context, topic string) ([]*Entity, error) {
	rows, err := s.db.QueryContext(ctx, s.sql.Rebind(`
SELECT c.id, c.kind, c.name, MIN(c.first_seen), MAX(c.last_seen), SUM(p.assertions)
  FROM properties p
  JOIN resolved r ON r.id = p.subject
  JOIN entities c ON c.id = r.canonical
 WHERE p.name = ? AND p.value = ?
 GROUP BY c.id
 ORDER BY SUM(p.assertions) DESC, c.name`), TopicProperty, topic)
	if err != nil {
		return nil, fmt.Errorf("entity: about %s: %w", topic, err)
	}
	defer rows.Close()

	var out []*Entity
	for rows.Next() {
		var one Entity
		var first, last int64
		if err := rows.Scan(&one.ID, &one.Kind, &one.Name, &first, &last, &one.Assertions); err != nil {
			return nil, fmt.Errorf("entity: about %s: %w", topic, err)
		}
		one.First = time.Unix(0, first).UTC()
		one.Last = time.Unix(0, last).UTC()
		out = append(out, &one)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("entity: about %s: %w", topic, err)
	}
	return out, nil
}
