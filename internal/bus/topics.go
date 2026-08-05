// SPDX-License-Identifier: GPL-3.0-or-later

package bus

import (
	"context"
	"time"

	"github.com/rangertaha/scour/internal/classify"
	"github.com/rangertaha/scour/internal/classify/store"
)

// Topics as a service: what a cluster has been taught to recognise.
//
// # What crosses, and what does not
//
// The model, not the page. The scheduler scores every URL it is offered and the
// spider scores every page it reads, so a request per page would put the
// network in the middle of the hottest loop in the crawl. A client fetches a
// topic once and scores locally, which is the same rule that keeps a fetched
// body off the bus and sends a cache key instead.
//
// That is why the surface is Load rather than Score. A [classify.Classifier] is
// behaviour and cannot travel; what a caller needs is the model it was built
// from, and building one out of that is a registry lookup it can do itself.
//
// # Why the cluster shares them at all
//
// Because a topic is what makes a focused crawl focused, and two nodes working
// one job have to agree about what the job is looking for. A node with an older
// training would score the same page differently and put it somewhere else in
// the frontier, which looks like the crawl being unlucky rather than like the
// nodes disagreeing.

// TopicQueue is the queue group the topic service answers in.
const TopicQueue = "scour-topic"

// TopicSubject is where one topic operation is asked for:
// scour.topic.<operation>.
func TopicSubject(op string) string { return "scour.topic." + op }

// Topics is what a store of trained topics can do, here or somewhere else.
//
// Both [store.Store] and [TopicClient] satisfy it.
type Topics interface {
	// Names lists what has been trained, as a job would write it: climate@7.
	Names() ([]string, error)

	// Latest is the highest version trained for a subject, which is what
	// somebody pastes into a job after training.
	Latest(name string) (int, error)

	// Load reads a trained topic. See the note above on why this is not Score.
	Load(ref classify.Ref) (store.Topic, error)

	// Put writes a trained topic. A version that already exists is refused
	// rather than replaced, because a job that pinned one must not start
	// behaving differently.
	Put(kind string, cfg classify.Config) error

	// Replace overwrites a version that is already there, which is the update
	// of CRUD and the one operation that can change what a pinned version
	// means. See [store.Store.Replace] for why it exists anyway.
	Replace(kind string, cfg classify.Config) error

	// Delete removes one version, and never a whole subject.
	Delete(ref classify.Ref) error
}

type (
	refAsk struct {
		Ref classify.Ref `json:"ref"`
	}
	subjectAsk struct {
		Name string `json:"name"`
	}
	putTopicAsk struct {
		Kind   string          `json:"kind"`
		Config classify.Config `json:"config"`
	}
)

// ServeTopics answers for a topic store until the returned service is closed.
func (c *Conn) ServeTopics(topics Topics) (*Service, error) {
	s := &Service{}

	serving(c, s, TopicSubject("names"), TopicQueue,
		func(_ context.Context, _ nothingAsk) ([]string, error) {
			return topics.Names()
		})
	serving(c, s, TopicSubject("latest"), TopicQueue,
		func(_ context.Context, a subjectAsk) (int, error) {
			return topics.Latest(a.Name)
		})
	serving(c, s, TopicSubject("load"), TopicQueue,
		func(_ context.Context, a refAsk) (store.Topic, error) {
			return topics.Load(a.Ref)
		})
	serving(c, s, TopicSubject("put"), TopicQueue,
		func(_ context.Context, a putTopicAsk) (none, error) {
			return none{}, topics.Put(a.Kind, a.Config)
		})
	serving(c, s, TopicSubject("replace"), TopicQueue,
		func(_ context.Context, a putTopicAsk) (none, error) {
			return none{}, topics.Replace(a.Kind, a.Config)
		})
	serving(c, s, TopicSubject("delete"), TopicQueue,
		func(_ context.Context, a refAsk) (none, error) {
			return none{}, topics.Delete(a.Ref)
		})

	if s.err != nil {
		s.Close()
		return nil, s.err
	}
	return s, nil
}

// TopicClient is a topic store that is somewhere else. It satisfies [Topics].
type TopicClient struct {
	conn *Conn
	wait time.Duration
}

// NewTopics returns a client for the topic service.
func (c *Conn) NewTopics(wait time.Duration) *TopicClient {
	return &TopicClient{conn: c, wait: wait}
}

func (t *TopicClient) Names() ([]string, error) {
	return call[nothingAsk, []string](context.Background(), t.conn,
		TopicSubject("names"), t.wait, nothingAsk{})
}

func (t *TopicClient) Latest(name string) (int, error) {
	return call[subjectAsk, int](context.Background(), t.conn,
		TopicSubject("latest"), t.wait, subjectAsk{Name: name})
}

func (t *TopicClient) Load(ref classify.Ref) (store.Topic, error) {
	return call[refAsk, store.Topic](context.Background(), t.conn,
		TopicSubject("load"), t.wait, refAsk{Ref: ref})
}

func (t *TopicClient) Put(kind string, cfg classify.Config) error {
	_, err := call[putTopicAsk, none](context.Background(), t.conn,
		TopicSubject("put"), t.wait, putTopicAsk{Kind: kind, Config: cfg})
	return err
}

func (t *TopicClient) Replace(kind string, cfg classify.Config) error {
	_, err := call[putTopicAsk, none](context.Background(), t.conn,
		TopicSubject("replace"), t.wait, putTopicAsk{Kind: kind, Config: cfg})
	return err
}

func (t *TopicClient) Delete(ref classify.Ref) error {
	_, err := call[refAsk, none](context.Background(), t.conn,
		TopicSubject("delete"), t.wait, refAsk{Ref: ref})
	return err
}

// Classifier fetches a topic and builds a scorer from it, locally.
//
// The convenience that makes the design above usable: a caller wants to score
// pages, and what it gets from the service is the model to score them with. The
// building is a registry lookup, so a kind this build does not have is refused
// by name here rather than producing a scorer that quietly says nothing.
func (t *TopicClient) Classifier(ctx context.Context, ref classify.Ref) (classify.Classifier, error) {
	one, err := t.Load(ref)
	if err != nil {
		return nil, err
	}
	return classify.New(ctx, one.Kind, classify.Config{
		Name:    one.Name,
		Version: one.Version,
		Terms:   one.Terms,
		Weights: one.Weights,
		Model:   one.Model,
	})
}
