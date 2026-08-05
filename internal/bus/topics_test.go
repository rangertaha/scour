// SPDX-License-Identifier: GPL-3.0-or-later

package bus_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/classify"
	"github.com/rangertaha/scour/internal/classify/bayes"
	"github.com/rangertaha/scour/internal/classify/store"
)

// trained is a small real model, so what travels is a model and not a fixture
// shaped like one.
func trained(t *testing.T, version int) classify.Config {
	t.Helper()

	model, err := bayes.Train("climate", version, []bayes.Document{
		{Text: "The climate committee criticised the pace of decarbonisation.", About: true},
		{Text: "Emissions from shipping must fall to meet the carbon budget.", About: true},
		{Text: "The manager named an unchanged squad for the cup fixture.", About: false},
		{Text: "Shares fell after the company cut its quarterly guidance.", About: false},
	})
	if err != nil {
		t.Fatalf("train: %v", err)
	}

	encoded, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	return classify.Config{Name: "climate", Version: version, Model: encoded}
}

// exercise runs the same CRUD against whatever it is given, and renders what it
// saw, so the direct store and the client are asked to do one thing.
func exercise(t *testing.T, topics bus.Topics) string {
	t.Helper()

	if err := topics.Put("bayes", trained(t, 1)); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := topics.Put("bayes", trained(t, 2)); err != nil {
		t.Fatalf("put: %v", err)
	}

	// A version that exists is refused rather than replaced, because a job
	// pinned it.
	if err := topics.Put("bayes", trained(t, 2)); err == nil {
		t.Error("putting over an existing version was allowed")
	}

	var out string

	names, err := topics.Names()
	if err != nil {
		t.Fatalf("names: %v", err)
	}
	out += "names " + strings.Join(names, ",") + "\n"

	latest, err := topics.Latest("climate")
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	out += "latest " + string(rune('0'+latest)) + "\n"

	one, err := topics.Load(classify.Ref{Name: "climate", Version: 2})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	out += "loaded " + one.Kind + " " + one.Name + "\n"

	// The model survives the trip, which is the whole point: a caller scores
	// with it locally.
	var model bayes.Bayes
	if err := json.Unmarshal(one.Model, &model); err != nil {
		t.Fatalf("the model did not survive: %v", err)
	}
	if len(model.LogOdds) == 0 {
		t.Error("the model arrived with no words in it")
	}
	out += "words " + string(rune('0'+min(len(model.LogOdds)/10, 9))) + "\n"

	// Update, and then removal.
	corrected := trained(t, 2)
	corrected.Terms = []string{"corrected"}
	if err := topics.Replace("bayes", corrected); err != nil {
		t.Fatalf("replace: %v", err)
	}
	after, err := topics.Load(classify.Ref{Name: "climate", Version: 2})
	if err != nil {
		t.Fatal(err)
	}
	out += "replaced " + strings.Join(after.Terms, ",") + "\n"

	if err := topics.Delete(classify.Ref{Name: "climate", Version: 1}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// Removing what is not there is not an error.
	if err := topics.Delete(classify.Ref{Name: "climate", Version: 1}); err != nil {
		t.Errorf("deleting what is already gone: %v", err)
	}

	left, err := topics.Names()
	if err != nil {
		t.Fatal(err)
	}
	out += "left " + strings.Join(left, ",") + "\n"
	return out
}

// TestTheSameTopicsComeBackEitherWay.
func TestTheSameTopicsComeBackEitherWay(t *testing.T) {
	conn := connect(t)

	direct, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	remote, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	service, err := conn.ServeTopics(remote)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	want := exercise(t, direct)
	got := exercise(t, conn.NewTopics(wait))

	if want == "" {
		t.Fatal("the direct store produced nothing, so this compares nothing")
	}
	if got != want {
		t.Errorf("topics differ over the bus:\n--- direct ---\n%s\n--- bus ---\n%s", want, got)
	}
}

// TestAClientScoresWithTheModelItFetched.
//
// The design in one test: the model crosses the bus once and the scoring
// happens where the pages are. A scheduler scores every URL it is offered, so a
// request per page would put the network in the hottest loop in the crawl.
func TestAClientScoresWithTheModelItFetched(t *testing.T) {
	conn := connect(t)

	remote, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Put("bayes", trained(t, 1)); err != nil {
		t.Fatal(err)
	}

	service, err := conn.ServeTopics(remote)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	ctx := context.Background()
	scorer, err := conn.NewTopics(wait).Classifier(ctx, classify.Ref{Name: "climate", Version: 1})
	if err != nil {
		t.Fatalf("building a classifier from a fetched topic: %v", err)
	}

	on, err := scorer.Score(ctx, "Ministers delayed the decarbonisation plan despite rising emissions.")
	if err != nil {
		t.Fatal(err)
	}
	off, err := scorer.Score(ctx, "The transfer window closed with the squad unchanged.")
	if err != nil {
		t.Fatal(err)
	}
	if on <= off {
		t.Errorf("on topic scored %.2f and off topic %.2f: the model did not survive the trip", on, off)
	}
}

// TestATopicNobodyTrainedIsRefusedByName.
func TestATopicNobodyTrainedIsRefusedByName(t *testing.T) {
	conn := connect(t)

	remote, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service, err := conn.ServeTopics(remote)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	_, err = conn.NewTopics(wait).Load(classify.Ref{Name: "nosuchtopic", Version: 1})
	if err == nil {
		t.Fatal("loading a topic nobody trained succeeded")
	}
	if !strings.Contains(err.Error(), "nosuchtopic") {
		t.Errorf("the refusal does not name what was asked for: %v", err)
	}
}
