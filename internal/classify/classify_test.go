// SPDX-License-Identifier: GPL-3.0-or-later

package classify_test

import (
	"context"
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/classify"
	"github.com/rangertaha/scour/internal/classify/bayes"
	"github.com/rangertaha/scour/internal/classify/terms"
)

const (
	onTopic = `The government missed its 2030 emissions target, according to the
	climate committee. Carbon capture projects have stalled and the net zero
	pathway now depends on faster decarbonisation of transport.`

	offTopic = `The striker signed a four year contract after a record transfer
	fee. The manager said the squad was strong and the fixture list favourable
	for the opening month of the season.`
)

func climate(t *testing.T) classify.Classifier {
	t.Helper()
	c, err := terms.New(classify.Config{
		Name:    "climate",
		Version: 1,
		Terms:   []string{"climate", "emissions", "carbon capture", "net zero", "decarbonisation"},
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return c
}

func score(t *testing.T, c classify.Classifier, text string) float64 {
	t.Helper()
	got, err := c.Score(context.Background(), text)
	if err != nil {
		t.Fatalf("score: %v", err)
	}
	if got < 0 || got > 1 {
		t.Fatalf("score %v is outside the contract", got)
	}
	return got
}

// TestTermsSeparates is the least a classifier can do.
func TestTermsSeparates(t *testing.T) {
	c := climate(t)

	on := score(t, c, onTopic)
	off := score(t, c, offTopic)

	if on <= off {
		t.Errorf("on topic scored %.2f, off topic %.2f", on, off)
	}
	if on < 0.3 {
		t.Errorf("a page using most of the vocabulary scored %.2f", on)
	}
	if off > 0.1 {
		t.Errorf("a page using none of it scored %.2f", off)
	}
}

func TestTermsScoresPhrases(t *testing.T) {
	c := climate(t)

	// The words in the wrong order are not the phrase.
	inOrder := score(t, c, "carbon capture is expensive")
	scrambled := score(t, c, "capture the carbon")

	if inOrder <= scrambled {
		t.Errorf("phrase in order scored %.2f, scrambled %.2f", inOrder, scrambled)
	}
}

// TestRepetitionDoesNotWin is why occurrences saturate: one word in a menu on
// every page must not outscore a page that discusses the subject.
func TestRepetitionDoesNotWin(t *testing.T) {
	c := climate(t)

	menu := score(t, c, strings.Repeat("climate ", 200))
	article := score(t, c, onTopic)

	if menu >= article {
		t.Errorf("one word repeated scored %.2f, a real page %.2f", menu, article)
	}
}

func TestTermsHandlesOtherAlphabets(t *testing.T) {
	c, err := terms.New(classify.Config{
		Name:    "климат",
		Version: 1,
		Terms:   []string{"климат", "выбросы"},
	})
	if err != nil {
		t.Fatal(err)
	}

	on := score(t, c, "Изменение климата и выбросы углерода растут")
	off := score(t, c, "Футбольный матч закончился вничью")

	// The tokeniser is Unicode, not ASCII: a corpus that is Greek, Russian and
	// Arabic as often as English cannot be scored zero by definition.
	if on <= off {
		t.Errorf("Cyrillic on topic scored %.2f, off topic %.2f", on, off)
	}
}

func TestEmptyInputs(t *testing.T) {
	c := climate(t)
	if got := score(t, c, ""); got != 0 {
		t.Errorf("empty text scored %v", got)
	}

	// A subject nobody has described yet scores nothing, rather than refusing
	// to exist: a job referencing it should crawl nothing on topic, not fail.
	empty, err := terms.New(classify.Config{Name: "unnamed", Version: 1})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if got := score(t, empty, onTopic); got != 0 {
		t.Errorf("an empty vocabulary scored %v", got)
	}
}

func TestTermsPrintsItself(t *testing.T) {
	c := climate(t).(*terms.Terms)
	got := strings.Join(c.Vocabulary(), " ")

	for _, want := range []string{"climate", "carbon capture"} {
		if !strings.Contains(got, want) {
			t.Errorf("vocabulary does not include %q: %s", want, got)
		}
	}
}

// Bayes.

func trained(t *testing.T) *bayes.Bayes {
	t.Helper()

	docs := []bayes.Document{
		{Text: onTopic, About: true},
		{Text: "Emissions from shipping must fall to meet the carbon budget.", About: true},
		{Text: "The climate committee criticised the pace of decarbonisation.", About: true},
		{Text: "Net zero requires carbon capture at scale, the report said.", About: true},
		{Text: offTopic, About: false},
		{Text: "The manager named an unchanged squad for the cup fixture.", About: false},
		{Text: "Shares fell after the company cut its quarterly guidance.", About: false},
		{Text: "The film won three awards including best original screenplay.", About: false},
	}

	b, err := bayes.Train("climate", 1, docs)
	if err != nil {
		t.Fatalf("train: %v", err)
	}
	return b
}

func TestBayesSeparatesUnseenPages(t *testing.T) {
	b := trained(t)

	on := score(t, b, "Ministers delayed the decarbonisation plan despite rising emissions.")
	off := score(t, b, "The transfer window closed with the squad unchanged.")

	if on <= off {
		t.Errorf("on topic scored %.2f, off topic %.2f", on, off)
	}
	if on < 0.5 || off > 0.5 {
		t.Errorf("scores do not straddle the middle: %.2f and %.2f", on, off)
	}
}

// TestBayesLearnsWordsNobodyListed is the reason to prefer it over a term list.
func TestBayesLearnsWordsNobodyListed(t *testing.T) {
	b := trained(t)

	words := strings.Join(b.Words(30), " ")
	if !strings.Contains(words, "emissions") {
		t.Errorf("did not learn an obvious term: %s", words)
	}

	// And it learns what argues against, which is usually the surprising half.
	if len(b.Against(10)) == 0 {
		t.Error("learned nothing that argues against the subject")
	}
}

// TestBayesRefusesOneSidedExamples: shown only one side, every word looks like
// evidence for it, and the result is a confident classifier that is useless.
func TestBayesRefusesOneSidedExamples(t *testing.T) {
	_, err := bayes.Train("climate", 1, []bayes.Document{
		{Text: onTopic, About: true},
		{Text: "More emissions news.", About: true},
	})
	if err == nil {
		t.Fatal("trained on one side only")
	}
	if !strings.Contains(err.Error(), "both") {
		t.Errorf("the error does not say what is missing: %v", err)
	}

	if _, err := bayes.Train("climate", 1, nil); err == nil {
		t.Fatal("trained on nothing")
	}
}

// TestBayesIsNotFooledByLength: evidence is normalised, or every long page
// scores high whatever it says.
func TestBayesIsNotFooledByLength(t *testing.T) {
	b := trained(t)

	short := score(t, b, "The transfer fee was a record.")
	long := score(t, b, strings.Repeat("The transfer fee was a record. ", 60))

	if long > short+0.2 {
		t.Errorf("the same off-topic text scored %.2f short and %.2f long", short, long)
	}
}

func TestBayesRoundTrips(t *testing.T) {
	b := trained(t)

	raw, err := b.Bytes()
	if err != nil {
		t.Fatalf("serialise: %v", err)
	}

	back, err := bayes.Load(classify.Config{Name: "climate", Model: raw})
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	text := "Ministers delayed the decarbonisation plan."
	if score(t, b, text) != score(t, back, text) {
		t.Error("a reloaded classifier scores differently")
	}
	if back.Name() != "climate" || back.Version() != 1 {
		t.Errorf("identity lost: %s@%d", back.Name(), back.Version())
	}
}

func TestBayesWithoutAModel(t *testing.T) {
	if _, err := bayes.Load(classify.Config{Name: "climate"}); err == nil {
		t.Fatal("loaded a classifier with no model")
	}
}

// References.

func TestRefsRequireAVersion(t *testing.T) {
	got, err := classify.ParseRef("climate@7")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Name != "climate" || got.Version != 7 || got.String() != "climate@7" {
		t.Errorf("ref = %+v", got)
	}

	// Without one, retraining would change what every job crawls with nothing
	// in any document to show why.
	for _, bad := range []string{"climate", "climate@", "@7", "climate@x", "climate@0", ""} {
		if _, err := classify.ParseRef(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestBothAreRegistered(t *testing.T) {
	for _, kind := range []string{terms.Name, bayes.Name} {
		if !classify.Has(kind) {
			t.Errorf("%s is not registered", kind)
		}
	}
	if classify.Has("embeddings") {
		t.Error("something that needs a dependency is registered by default")
	}
}
