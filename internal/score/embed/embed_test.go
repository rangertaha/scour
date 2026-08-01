// SPDX-License-Identifier: GPL-3.0-or-later

package embed

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/score"
)

// A hand-built vector space, so the tests assert on meaning rather than on
// whatever a downloaded file happens to encode. Three dimensions stand for
// three unrelated topics; a word's vector says how much of each it is.
//
//	dim 0: vehicles   dim 1: recruitment   dim 2: cookery
const synthetic = `12 3
car 1.0 0.0 0.0
cars 0.98 0.02 0.0
vehicle 0.95 0.05 0.0
saloon 0.9 0.0 0.1
automobile 0.97 0.01 0.02
truck 0.93 0.0 0.05
job 0.0 1.0 0.0
jobs 0.02 0.98 0.0
careers 0.0 0.95 0.05
vacancy 0.01 0.97 0.02
recipe 0.0 0.0 1.0
pudding 0.05 0.0 0.95
`

func vectors(t *testing.T) *Vectors {
	t.Helper()
	v, err := Read(strings.NewReader(synthetic))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func scorer(t *testing.T, seed ...string) *Scorer {
	t.Helper()
	s, err := New(vectors(t), seed)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestReadingVectors(t *testing.T) {
	v := vectors(t)

	if v.Dim() != 3 {
		t.Errorf("dim = %d, want 3", v.Dim())
	}
	// The "12 3" header is a count and a width, not a word.
	if v.Len() != 12 {
		t.Errorf("loaded %d words, want 12", v.Len())
	}
	if _, ok := v.Vector("12"); ok {
		t.Error("the word2vec header was loaded as a word")
	}

	if vec, ok := v.Vector("CAR"); !ok || vec[0] != 1 {
		t.Errorf("lookup is not case insensitive: %v, %v", vec, ok)
	}
}

func TestUnknownWordsAreSkippedNotZeroed(t *testing.T) {
	v := vectors(t)

	known, n := v.Mean([]string{"car"})
	mixed, m := v.Mean([]string{"car", "qwertyuiop", "zxcvbnm"})

	if n != 1 || m != 1 {
		t.Fatalf("known counts = %d and %d, want 1 each", n, m)
	}
	// Averaging an unknown word in as zeros would halve every component.
	for i := range known {
		if math.Abs(float64(known[i]-mixed[i])) > 1e-6 {
			t.Errorf("unknown words moved the mean: %v against %v", mixed, known)
			break
		}
	}
}

func TestAllUnknownHasNoMean(t *testing.T) {
	if _, n := vectors(t).Mean([]string{"qwerty", "zxcvb"}); n != 0 {
		t.Errorf("known = %d, want 0", n)
	}
}

// The point of the whole package: a word the counting model has never seen pay
// off still ranks correctly, because it means the same thing.
func TestASynonymOutranksAnUnrelatedWord(t *testing.T) {
	s := scorer(t, "car", "vehicle")

	related := s.Score(score.Features{URL: "http://example.com/saloon/", Anchor: "automobile"})
	unrelated := s.Score(score.Features{URL: "http://example.com/recipe/", Anchor: "pudding"})

	if related <= unrelated {
		t.Errorf("a synonym scored %.3f, an unrelated page %.3f; the ranking is backwards",
			related, unrelated)
	}
	if related < 0.9 {
		t.Errorf("a near synonym scored only %.3f", related)
	}
}

func TestScoresStayInRange(t *testing.T) {
	s := scorer(t, "car")

	for _, f := range []score.Features{
		{URL: "http://example.com/cars/"},
		{URL: "http://example.com/recipe/", Anchor: "pudding"},
		{URL: "http://example.com/"},
		{URL: "://nonsense"},
		{URL: "http://example.com/2019/12/31/"},
	} {
		got := s.Score(f)
		if got < 0 || got > 1 {
			t.Errorf("%s scored %v, outside [0,1]", f.URL, got)
		}
	}
}

// Not recognising a word is ignorance, and ignorance must not read as evidence
// of irrelevance, or the crawl would refuse to explore unfamiliar vocabulary.
func TestUnknownVocabularyScoresNeutral(t *testing.T) {
	s := scorer(t, "car")

	got := s.Score(score.Features{URL: "http://example.com/qwertyuiop/", Anchor: "zxcvbnm"})
	if got != Neutral {
		t.Errorf("an entirely unknown link scored %v, want the neutral %v", got, Neutral)
	}
}

func TestAnItemWithNoKnownWordsIsAnError(t *testing.T) {
	_, err := New(vectors(t), []string{"qwertyuiop", "zxcvbnm"})
	if err == nil {
		t.Error("a topic averaged from nothing should be an error, not a scorer that ranks at random")
	}
}

func TestRepeatedLinksAreScoredOnce(t *testing.T) {
	s := scorer(t, "car")
	f := score.Features{URL: "http://example.com/cars/1/", Anchor: "details"}

	first := s.Score(f)
	for range 100 {
		if got := s.Score(f); got != first {
			t.Fatalf("cached score drifted: %v against %v", got, first)
		}
	}

	if len(s.cache) != 1 {
		t.Errorf("cache holds %d entries for one distinct link", len(s.cache))
	}
}

// Prefixed tokens are features of the counting model, not words. Sending
// "anchor:car" to a vector file would find nothing.
func TestTokensArrivePlainAndWithoutNumbers(t *testing.T) {
	got := plainTokens(score.Features{
		URL:    "http://example.com/cars/2019/",
		Anchor: "used cars",
		Depth:  3,
	})

	for _, tok := range got {
		if strings.Contains(tok, ":") {
			t.Errorf("token %q still carries its source prefix", tok)
		}
		if isNumber(tok) {
			t.Errorf("token %q is a number, not vocabulary", tok)
		}
	}
	if !contains(got, "cars") {
		t.Errorf("tokens = %v, want the path and anchor words", got)
	}
}

func contains(all []string, want string) bool {
	for _, s := range all {
		if s == want {
			return true
		}
	}
	return false
}

func TestCosine(t *testing.T) {
	tests := []struct {
		name string
		a, b []float32
		want float64
	}{
		{"identical", []float32{1, 0}, []float32{1, 0}, 1},
		{"opposite", []float32{1, 0}, []float32{-1, 0}, -1},
		{"orthogonal", []float32{1, 0}, []float32{0, 1}, 0},
		{"scale invariant", []float32{1, 0}, []float32{7, 0}, 1},
		{"zero vector", []float32{0, 0}, []float32{1, 0}, 0},
		{"mismatched widths", []float32{1, 0, 0}, []float32{1, 0}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cosine(tt.a, tt.b); math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("cosine = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRescaleIsMonotone(t *testing.T) {
	prev := -1.0
	for c := -1.0; c <= 1.0; c += 0.1 {
		got := rescale(c)
		if got < 0 || got > 1 {
			t.Fatalf("rescale(%v) = %v, outside [0,1]", c, got)
		}
		if got < prev {
			t.Fatalf("rescale is not monotone at %v", c)
		}
		prev = got
	}
}

func TestRaggedFileIsRejected(t *testing.T) {
	_, err := Read(strings.NewReader("car 1.0 0.0 0.0\nvan 1.0 0.0\n"))
	if err == nil {
		t.Error("a file whose rows disagree on width must be an error, not a silent partial load")
	}
}

func TestNonNumericIsRejected(t *testing.T) {
	if _, err := Read(strings.NewReader("car 1.0 abc 0.0\n")); err == nil {
		t.Error("a non-numeric value should be reported")
	}
}

func TestEmptyFileIsRejected(t *testing.T) {
	if _, err := Read(strings.NewReader("\n\n")); err == nil {
		t.Error("a file with no vectors should be an error")
	}
}

func TestLoadFromDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vectors.txt")
	if err := os.WriteFile(path, []byte(synthetic), 0o600); err != nil {
		t.Fatal(err)
	}

	v, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if v.Len() != 12 {
		t.Errorf("loaded %d words", v.Len())
	}

	if _, err := Load(filepath.Join(t.TempDir(), "missing.txt")); err == nil {
		t.Error("a missing vectors file should be reported")
	}
}

func TestRegisteredButNeedsVectors(t *testing.T) {
	if !score.Has("embed") {
		t.Fatal("importing this package should register the scorer")
	}

	// Without a path there is nothing to load, and saying so is better than
	// silently ranking every link the same.
	if _, err := score.New("embed", score.Config{Item: "vehicle"}); err == nil {
		t.Error("the embed scorer should refuse to start with no vectors configured")
	}

	path := filepath.Join(t.TempDir(), "vectors.txt")
	if err := os.WriteFile(path, []byte(synthetic), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := score.New("embed", score.Config{
		Item: "vehicle", Seed: []string{"car"}, Vectors: path,
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "embed" {
		t.Errorf("name = %q", s.Name())
	}
	// It never learns from a crawl, and must not claim otherwise.
	if trained, ok := s.(score.Trained); !ok || trained.Trained() {
		t.Error("the embed scorer should report itself untrained")
	}
}
