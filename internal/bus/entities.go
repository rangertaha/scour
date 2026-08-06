// SPDX-License-Identifier: GPL-3.0-or-later

package bus

import (
	"context"
	"time"

	"github.com/rangertaha/scour/internal/entity"
)

// The entity graph as a service: types, entities, properties and relations.
//
// The client below has the same method set as [entity.Store], so a caller holds
// one or the other and does not care which. That is the same seam the stages
// use, and it is what lets a crawl on one machine keep its graph in a file
// while a cluster shares one.

// EntityQueue is the queue group the entity service answers in.
//
// One graph, one group: a second service joining it is a standby that shares
// the load, not a second writer, and the store behind them has one writer
// whether or not anybody remembers that.
const EntityQueue = "scour-entity"

// EntitySubject is where one entity operation is asked for:
// scour.entity.<operation>.
func EntitySubject(op string) string { return "scour.entity." + op }

// Graph is what an entity store can do, here or somewhere else.
//
// Both [entity.Store] and [Entities] satisfy it. Declared here rather than in
// the entity package, because the store should not have to know that a bus
// exists: the store is the thing, and this is one way of reaching it.
type Graph interface {
	Assert(ctx context.Context, kind, name string, said entity.Provenance) (string, error)
	Describe(ctx context.Context, subject, name, value string, position int, said entity.Provenance) error
	Relate(ctx context.Context, from, to, kind, topic string, position int, said entity.Provenance) (string, error)

	Get(ctx context.Context, id string) (*entity.Entity, error)
	Find(ctx context.Context, kind, name string) (*entity.Entity, error)
	Kind(ctx context.Context, kind string) ([]*entity.Entity, error)
	Kinds(ctx context.Context) ([]entity.Kind, error)
	RelationKinds(ctx context.Context) ([]entity.RelationKind, error)
	Relations(ctx context.Context, id string) ([]entity.Relation, error)
	Properties(ctx context.Context, id string) ([]entity.Property, error)
	Related(ctx context.Context, id, kind, topic string) ([]*entity.Entity, error)
	Provenances(ctx context.Context, id string) ([]entity.Provenance, error)

	Candidates(ctx context.Context, kind, name string) ([]entity.Candidate, error)
	Merge(ctx context.Context, from, to, rule string, said entity.Provenance) error
	Unmerge(ctx context.Context, alias string) error
	Aliases(ctx context.Context, id string) ([]entity.Alias, error)

	Tag(ctx context.Context, subject, topic string, said entity.Provenance) error
	Topics(ctx context.Context, subject string) ([]entity.Property, error)
	About(ctx context.Context, topic string) ([]*entity.Entity, error)

	Retract(ctx context.Context, job string) (int64, error)
	Close() error
}

// The wire shapes: one ask per operation, named for it, so that a subject and
// what it carries are read together. Replies are the store's own types, which
// is what keeps the two sides from drifting into two vocabularies.
type (
	assertAsk struct {
		Kind string            `json:"kind"`
		Name string            `json:"name"`
		Said entity.Provenance `json:"said"`
	}
	describeAsk struct {
		Subject  string            `json:"subject"`
		Name     string            `json:"name"`
		Value    string            `json:"value"`
		Position int               `json:"position,omitempty"`
		Said     entity.Provenance `json:"said"`
	}
	relateAsk struct {
		From     string            `json:"from"`
		To       string            `json:"to"`
		Kind     string            `json:"kind"`
		Topic    string            `json:"topic,omitempty"`
		Position int               `json:"position,omitempty"`
		Said     entity.Provenance `json:"said"`
	}
	idAsk struct {
		ID string `json:"id"`
	}
	nameAsk struct {
		Kind string `json:"kind"`
		Name string `json:"name,omitempty"`
	}
	relatedAsk struct {
		ID    string `json:"id"`
		Kind  string `json:"kind,omitempty"`
		Topic string `json:"topic,omitempty"`
	}
	mergeAsk struct {
		From string            `json:"from"`
		To   string            `json:"to"`
		Rule string            `json:"rule,omitempty"`
		Said entity.Provenance `json:"said"`
	}
	aliasAsk struct {
		Alias string `json:"alias"`
	}
	tagAsk struct {
		Subject string            `json:"subject"`
		Topic   string            `json:"topic"`
		Said    entity.Provenance `json:"said"`
	}
	topicAsk struct {
		Topic string `json:"topic"`
	}
	jobAsk struct {
		Job string `json:"job"`
	}
	nothingAsk struct{}
)

// ServeEntities answers for a graph until the returned service is closed.
//
// The store is not closed with it: whoever opened it owns it, and a service
// that closed somebody else's store on shutdown would take the graph away from
// everything else in the process still using it.
// The wait bounds one request, and comes from the service document's
// `timeout`. Zero means [Timeout].
func (c *Conn) ServeEntities(store Graph, wait time.Duration) (*Service, error) {
	s := &Service{wait: wait}

	serving(c, s, EntitySubject("assert"), EntityQueue,
		func(ctx context.Context, a assertAsk) (string, error) {
			return store.Assert(ctx, a.Kind, a.Name, a.Said)
		})
	serving(c, s, EntitySubject("describe"), EntityQueue,
		func(ctx context.Context, a describeAsk) (none, error) {
			return none{}, store.Describe(ctx, a.Subject, a.Name, a.Value, a.Position, a.Said)
		})
	serving(c, s, EntitySubject("relate"), EntityQueue,
		func(ctx context.Context, a relateAsk) (string, error) {
			return store.Relate(ctx, a.From, a.To, a.Kind, a.Topic, a.Position, a.Said)
		})

	serving(c, s, EntitySubject("get"), EntityQueue,
		func(ctx context.Context, a idAsk) (*entity.Entity, error) {
			return store.Get(ctx, a.ID)
		})
	serving(c, s, EntitySubject("find"), EntityQueue,
		func(ctx context.Context, a nameAsk) (*entity.Entity, error) {
			return store.Find(ctx, a.Kind, a.Name)
		})
	serving(c, s, EntitySubject("kind"), EntityQueue,
		func(ctx context.Context, a nameAsk) ([]*entity.Entity, error) {
			return store.Kind(ctx, a.Kind)
		})
	serving(c, s, EntitySubject("kinds"), EntityQueue,
		func(ctx context.Context, _ nothingAsk) ([]entity.Kind, error) {
			return store.Kinds(ctx)
		})
	serving(c, s, EntitySubject("relationkinds"), EntityQueue,
		func(ctx context.Context, _ nothingAsk) ([]entity.RelationKind, error) {
			return store.RelationKinds(ctx)
		})
	serving(c, s, EntitySubject("relations"), EntityQueue,
		func(ctx context.Context, a idAsk) ([]entity.Relation, error) {
			return store.Relations(ctx, a.ID)
		})
	serving(c, s, EntitySubject("properties"), EntityQueue,
		func(ctx context.Context, a idAsk) ([]entity.Property, error) {
			return store.Properties(ctx, a.ID)
		})
	serving(c, s, EntitySubject("related"), EntityQueue,
		func(ctx context.Context, a relatedAsk) ([]*entity.Entity, error) {
			return store.Related(ctx, a.ID, a.Kind, a.Topic)
		})
	serving(c, s, EntitySubject("provenances"), EntityQueue,
		func(ctx context.Context, a idAsk) ([]entity.Provenance, error) {
			return store.Provenances(ctx, a.ID)
		})

	serving(c, s, EntitySubject("candidates"), EntityQueue,
		func(ctx context.Context, a nameAsk) ([]entity.Candidate, error) {
			return store.Candidates(ctx, a.Kind, a.Name)
		})
	serving(c, s, EntitySubject("merge"), EntityQueue,
		func(ctx context.Context, a mergeAsk) (none, error) {
			return none{}, store.Merge(ctx, a.From, a.To, a.Rule, a.Said)
		})
	serving(c, s, EntitySubject("unmerge"), EntityQueue,
		func(ctx context.Context, a aliasAsk) (none, error) {
			return none{}, store.Unmerge(ctx, a.Alias)
		})
	serving(c, s, EntitySubject("aliases"), EntityQueue,
		func(ctx context.Context, a idAsk) ([]entity.Alias, error) {
			return store.Aliases(ctx, a.ID)
		})

	serving(c, s, EntitySubject("tag"), EntityQueue,
		func(ctx context.Context, a tagAsk) (none, error) {
			return none{}, store.Tag(ctx, a.Subject, a.Topic, a.Said)
		})
	serving(c, s, EntitySubject("topics"), EntityQueue,
		func(ctx context.Context, a idAsk) ([]entity.Property, error) {
			return store.Topics(ctx, a.ID)
		})
	serving(c, s, EntitySubject("about"), EntityQueue,
		func(ctx context.Context, a topicAsk) ([]*entity.Entity, error) {
			return store.About(ctx, a.Topic)
		})

	serving(c, s, EntitySubject("retract"), EntityQueue,
		func(ctx context.Context, a jobAsk) (int64, error) {
			return store.Retract(ctx, a.Job)
		})

	if err := s.ready(c); err != nil {
		return nil, err
	}
	return s, nil
}

// Entities is an entity graph that is somewhere else.
//
// It satisfies [Graph], so the thing holding it cannot tell.
type Entities struct {
	conn *Conn
	wait time.Duration
}

// NewEntities returns a client for the entity service. Zero waits
// [engine.DefaultServiceTimeout] worth, which [Timeout] stands in for here.
func (c *Conn) NewEntities(wait time.Duration) *Entities {
	return &Entities{conn: c, wait: wait}
}

func (e *Entities) Assert(ctx context.Context, kind, name string, said entity.Provenance) (string, error) {
	return call[assertAsk, string](ctx, e.conn, EntitySubject("assert"), e.wait,
		assertAsk{Kind: kind, Name: name, Said: said})
}

func (e *Entities) Describe(ctx context.Context, subject, name, value string, position int, said entity.Provenance) error {
	_, err := call[describeAsk, none](ctx, e.conn, EntitySubject("describe"), e.wait,
		describeAsk{Subject: subject, Name: name, Value: value, Position: position, Said: said})
	return err
}

func (e *Entities) Relate(ctx context.Context, from, to, kind, topic string, position int, said entity.Provenance) (string, error) {
	return call[relateAsk, string](ctx, e.conn, EntitySubject("relate"), e.wait,
		relateAsk{From: from, To: to, Kind: kind, Topic: topic, Position: position, Said: said})
}

func (e *Entities) Get(ctx context.Context, id string) (*entity.Entity, error) {
	return call[idAsk, *entity.Entity](ctx, e.conn, EntitySubject("get"), e.wait, idAsk{ID: id})
}

func (e *Entities) Find(ctx context.Context, kind, name string) (*entity.Entity, error) {
	return call[nameAsk, *entity.Entity](ctx, e.conn, EntitySubject("find"), e.wait,
		nameAsk{Kind: kind, Name: name})
}

func (e *Entities) Kind(ctx context.Context, kind string) ([]*entity.Entity, error) {
	return call[nameAsk, []*entity.Entity](ctx, e.conn, EntitySubject("kind"), e.wait,
		nameAsk{Kind: kind})
}

func (e *Entities) Kinds(ctx context.Context) ([]entity.Kind, error) {
	return call[nothingAsk, []entity.Kind](ctx, e.conn, EntitySubject("kinds"), e.wait, nothingAsk{})
}

func (e *Entities) RelationKinds(ctx context.Context) ([]entity.RelationKind, error) {
	return call[nothingAsk, []entity.RelationKind](ctx, e.conn,
		EntitySubject("relationkinds"), e.wait, nothingAsk{})
}

func (e *Entities) Relations(ctx context.Context, id string) ([]entity.Relation, error) {
	return call[idAsk, []entity.Relation](ctx, e.conn, EntitySubject("relations"), e.wait, idAsk{ID: id})
}

func (e *Entities) Properties(ctx context.Context, id string) ([]entity.Property, error) {
	return call[idAsk, []entity.Property](ctx, e.conn, EntitySubject("properties"), e.wait, idAsk{ID: id})
}

func (e *Entities) Related(ctx context.Context, id, kind, topic string) ([]*entity.Entity, error) {
	return call[relatedAsk, []*entity.Entity](ctx, e.conn, EntitySubject("related"), e.wait,
		relatedAsk{ID: id, Kind: kind, Topic: topic})
}

func (e *Entities) Provenances(ctx context.Context, id string) ([]entity.Provenance, error) {
	return call[idAsk, []entity.Provenance](ctx, e.conn, EntitySubject("provenances"), e.wait, idAsk{ID: id})
}

func (e *Entities) Candidates(ctx context.Context, kind, name string) ([]entity.Candidate, error) {
	return call[nameAsk, []entity.Candidate](ctx, e.conn, EntitySubject("candidates"), e.wait,
		nameAsk{Kind: kind, Name: name})
}

func (e *Entities) Merge(ctx context.Context, from, to, rule string, said entity.Provenance) error {
	_, err := call[mergeAsk, none](ctx, e.conn, EntitySubject("merge"), e.wait,
		mergeAsk{From: from, To: to, Rule: rule, Said: said})
	return err
}

func (e *Entities) Unmerge(ctx context.Context, alias string) error {
	_, err := call[aliasAsk, none](ctx, e.conn, EntitySubject("unmerge"), e.wait, aliasAsk{Alias: alias})
	return err
}

func (e *Entities) Aliases(ctx context.Context, id string) ([]entity.Alias, error) {
	return call[idAsk, []entity.Alias](ctx, e.conn, EntitySubject("aliases"), e.wait, idAsk{ID: id})
}

func (e *Entities) Tag(ctx context.Context, subject, topic string, said entity.Provenance) error {
	_, err := call[tagAsk, none](ctx, e.conn, EntitySubject("tag"), e.wait,
		tagAsk{Subject: subject, Topic: topic, Said: said})
	return err
}

func (e *Entities) Topics(ctx context.Context, subject string) ([]entity.Property, error) {
	return call[idAsk, []entity.Property](ctx, e.conn, EntitySubject("topics"), e.wait, idAsk{ID: subject})
}

func (e *Entities) About(ctx context.Context, topic string) ([]*entity.Entity, error) {
	return call[topicAsk, []*entity.Entity](ctx, e.conn, EntitySubject("about"), e.wait,
		topicAsk{Topic: topic})
}

func (e *Entities) Retract(ctx context.Context, job string) (int64, error) {
	return call[jobAsk, int64](ctx, e.conn, EntitySubject("retract"), e.wait, jobAsk{Job: job})
}

// Close releases the client and not the graph.
//
// A client that closed the store would be a client that could take the graph
// away from every other client, which is the whole reason the store is behind a
// service. The connection belongs to whoever opened it.
func (e *Entities) Close() error { return nil }
