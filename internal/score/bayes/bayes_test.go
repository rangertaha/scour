// SPDX-License-Identifier: GPL-3.0-or-later

package bayes

import (
	"errors"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/rangertaha/scour/internal/score"
)

func features(url string) score.Features { return score.Features{URL: url, Depth: 3} }

func TestEmptyModelIsUndecided(t *testing.T) {
	m := New()
	if got := m.Score(features("http://example.com/anything")); got != 0.5 {
		t.Errorf("score = %v, want 0.5: a model that knows nothing should say so", got)
	}
}

// A first crawl has no outcomes to learn from. Without the seed every link
// scores identically and the crawl is breadth-first over the whole site, which
// is the thing scour exists not to do.
func TestSeedGivesTheFirstCrawlADirection(t *testing.T) {
	m := New()
	m.Seed([]string{"vehicle", "car", "ford"})

	relevant := m.Score(features("http://example.com/cars/ford/"))
	irrelevant := m.Score(features("http://example.com/legal/privacy/"))

	if relevant <= irrelevant {
		t.Errorf("seeded scores: relevant %v, irrelevant %v; want the seeded words to win", relevant, irrelevant)
	}
	if relevant <= 0.5 {
		t.Errorf("a URL full of seeded words scored %v, want above the prior", relevant)
	}
}

func TestTrainingLearnsFromOutcomes(t *testing.T) {
	m := New()

	var obs []Observation
	for _, u := range []string{
		"http://example.com/cars/one/", "http://example.com/cars/two/",
		"http://example.com/cars/three/", "http://example.com/cars/four/",
	} {
		obs = append(obs, Observation{Features: features(u), Relevant: true})
	}
	for _, u := range []string{
		"http://example.com/careers/one/", "http://example.com/careers/two/",
		"http://example.com/legal/terms/", "http://example.com/legal/privacy/",
	} {
		obs = append(obs, Observation{Features: features(u), Relevant: false})
	}

	if err := m.Train(obs, 0); err != nil {
		t.Fatalf("Train: %v", err)
	}

	good := m.Score(features("http://example.com/cars/five/"))
	bad := m.Score(features("http://example.com/careers/three/"))
	if good <= bad {
		t.Errorf("scores: cars %v, careers %v; training did not separate them", good, bad)
	}
	if good < 0.6 {
		t.Errorf("an unseen page in the good section scored %v, want confidence above 0.6", good)
	}
}

func TestTrainingIsDeterministic(t *testing.T) {
	obs := []Observation{
		{Features: features("http://example.com/a/"), Relevant: true},
		{Features: features("http://example.com/b/"), Relevant: false},
		{Features: features("http://example.com/c/"), Relevant: true},
		{Features: features("http://example.com/d/"), Relevant: false},
	}

	first, second := New(), New()
	if err := first.Train(obs, 0.2); err != nil {
		t.Fatal(err)
	}
	// The same corpus in a different order must produce the same model, or a
	// crawl is not reproducible.
	reversed := make([]Observation, len(obs))
	for i, o := range obs {
		reversed[len(obs)-1-i] = o
	}
	if err := second.Train(reversed, 0.2); err != nil {
		t.Fatal(err)
	}

	probe := features("http://example.com/a/")
	if a, b := first.Score(probe), second.Score(probe); a != b {
		t.Errorf("scores differ by input order: %v and %v", a, b)
	}
}

func TestHoldoutMeasuresAccuracy(t *testing.T) {
	m := New()

	var obs []Observation
	for i := range 20 {
		obs = append(obs, Observation{
			Features: features("http://example.com/cars/" + string(rune('a'+i)) + "/"),
			Relevant: true,
		})
		obs = append(obs, Observation{
			Features: features("http://example.com/legal/" + string(rune('a'+i)) + "/"),
			Relevant: false,
		})
	}
	if err := m.Train(obs, 0.2); err != nil {
		t.Fatal(err)
	}
	if m.Accuracy == 0 {
		t.Error("no accuracy was measured despite a holdout")
	}
	if m.Examples >= len(obs) {
		t.Errorf("trained on %d of %d examples, want some held back", m.Examples, len(obs))
	}
}

func TestTinyHoldoutIsIgnored(t *testing.T) {
	m := New()
	obs := []Observation{
		{Features: features("http://example.com/a/"), Relevant: true},
		{Features: features("http://example.com/b/"), Relevant: false},
	}
	if err := m.Train(obs, 0.2); err != nil {
		t.Fatal(err)
	}
	// Reserving a fraction of two examples measures nothing, so everything is
	// used for training instead.
	if m.Examples != len(obs) {
		t.Errorf("trained on %d of %d, want all of a corpus this small", m.Examples, len(obs))
	}
}

func TestTrainRejectsBadInput(t *testing.T) {
	m := New()
	if err := m.Train(nil, 0); err == nil {
		t.Error("training on nothing must fail")
	}
	if err := m.Train([]Observation{{Features: features("http://x/")}}, 1); err == nil {
		t.Error("a holdout of 1 leaves nothing to train on and must fail")
	}
}

func TestScoresStayInRange(t *testing.T) {
	m := New()
	var obs []Observation
	for i := range 50 {
		obs = append(obs, Observation{
			Features: score.Features{URL: "http://example.com/cars/one/", Depth: i%8 + 1},
			Relevant: true,
		})
	}
	if err := m.Train(obs, 0); err != nil {
		t.Fatal(err)
	}

	for _, u := range []string{
		"http://example.com/cars/one/",
		"http://example.com/utterly/unrelated/",
		"http://other.test/",
	} {
		got := m.Score(features(u))
		if got < 0 || got > 1 {
			t.Errorf("score(%s) = %v, outside [0,1]", u, got)
		}
	}
}

func TestTopReportsWhatWasLearned(t *testing.T) {
	m := New()
	var obs []Observation
	for range 5 {
		obs = append(obs,
			Observation{Features: features("http://example.com/cars/one/"), Relevant: true},
			Observation{Features: features("http://example.com/careers/one/"), Relevant: false},
		)
	}
	if err := m.Train(obs, 0); err != nil {
		t.Fatal(err)
	}

	top, worst := m.Top(5)
	if len(top) == 0 {
		t.Fatal("nothing was learned to chase")
	}
	if len(worst) == 0 {
		t.Fatal("nothing was learned to avoid")
	}
	if top[0].Weight <= 0 {
		t.Errorf("the strongest positive has weight %v", top[0].Weight)
	}
	if worst[0].Weight >= 0 {
		t.Errorf("the strongest negative has weight %v", worst[0].Weight)
	}
}

func TestSaveThenLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models", "vehicle.score.json")

	m := New()
	m.Entity = "vehicle"
	m.Seed([]string{"car"})
	if err := m.Train([]Observation{
		{Features: features("http://example.com/cars/one/"), Relevant: true},
		{Features: features("http://example.com/legal/"), Relevant: false},
	}, 0); err != nil {
		t.Fatal(err)
	}
	if err := m.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Entity != "vehicle" {
		t.Errorf("entity = %q", loaded.Entity)
	}

	probe := features("http://example.com/cars/two/")
	if a, b := m.Score(probe), loaded.Score(probe); a != b {
		t.Errorf("score changed across a round trip: %v then %v", a, b)
	}
}

func TestLoadMissingReportsNotExist(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "never-trained.json"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist so a cold start is distinguishable", err)
	}
}

func TestRegistered(t *testing.T) {
	s, err := score.New("bayes", score.Config{Entity: "vehicle"})
	if err != nil {
		t.Fatalf("bayes should be registered: %v", err)
	}
	if s.Name() != "bayes" {
		t.Errorf("name = %q", s.Name())
	}
}
