// SPDX-License-Identifier: GPL-3.0-or-later

package matcher

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/ai"
	"github.com/rangertaha/scour/internal/wom"
)

// The corpus is a fixed set of judgements with known answers, chosen to be
// exactly the cases a heuristic finds hard: values whose markup gives no label,
// labels that agree while the value disagrees, and values that look like the
// field but are not.
//
// It is small and hand-labelled on purpose. A benchmark whose ground truth was
// itself generated would measure agreement, not accuracy.
type judgementCase struct {
	name  string
	prop  wom.Prop
	html  string
	value string
	// want is whether the value really is an instance of the field.
	want bool
}

var (
	make_ = wom.Prop{
		Name: "make", Aliases: []string{"manufacturer", "brand"},
		Description: "the company that built the vehicle",
		Examples:    []string{"Toyota"},
	}
	price = wom.Prop{
		Name: "price", Type: "number", Aliases: []string{"cost", "msrp"},
		Description: "what the vehicle sells for",
		Examples:    []string{"$31,500"},
	}
	year = wom.Prop{
		Name: "year", Type: "number", Aliases: []string{"model year"},
		Description: "the model year of the vehicle",
		Examples:    []string{"2019"},
	}
)

var corpus = []judgementCase{
	// Unlabelled values: the markup says nothing, so only knowing what a car
	// manufacturer is can settle it.
	{"bare make", make_, `<div><span>Ford</span></div>`, "Ford", true},
	{"bare make 2", make_, `<div><span>Chevrolet</span></div>`, "Chevrolet", true},
	{"bare non-make", make_, `<div><span>Sunroof</span></div>`, "Sunroof", false},
	{"bare non-make 2", make_, `<div><span>Leather Seats</span></div>`, "Leather Seats", false},
	{"bare city", make_, `<div><span>Dallas</span></div>`, "Dallas", false},

	// The label agrees but the value does not. A label-driven heuristic is
	// most confident exactly where it is most wrong.
	{"mislabelled make", make_, `<div><dt>Make</dt><dd>Call for details</dd></div>`, "Call for details", false},
	{"mislabelled price", price, `<div><dt>Price</dt><dd>Contact dealer</dd></div>`, "Contact dealer", false},
	{"empty labelled year", year, `<div><dt>Year</dt><dd>N/A</dd></div>`, "N/A", false},

	// The value is right but the label is absent or misleading.
	{"price in unrelated markup", price, `<div class="banner"><span>$42,000</span></div>`, "$42,000", true},
	{"year in unrelated markup", year, `<div class="tag"><span>2019</span></div>`, "2019", true},

	// Values that look like the field but are a different quantity.
	{"payment not price", price, `<div><dt>Monthly payment</dt><dd>$389</dd></div>`, "$389", false},
	{"mileage not price", price, `<div><dt>Mileage</dt><dd>42,000</dd></div>`, "42,000", false},
	{"phone not year", year, `<div><dt>Call</dt><dd>2019</dd></div>`, "2019", true},
	{"stock number not year", year, `<div><dt>Stock #</dt><dd>1998</dd></div>`, "1998", true},

	// Plainly labelled, which both should get right.
	{"labelled make", make_, `<div><dt>Make</dt><dd>Ford</dd></div>`, "Ford", true},
	{"labelled price", price, `<div><dt>Price</dt><dd>$42,000</dd></div>`, "$42,000", true},
	{"labelled year", year, `<div><dt>Year</dt><dd>2019</dd></div>`, "2019", true},
	{"labelled make, wrong field", price, `<div><dt>Make</dt><dd>Ford</dd></div>`, "Ford", false},
}

// bestThreshold finds the cut that scores a matcher highest on the corpus.
//
// Comparing two matchers at one fixed cut measures calibration as much as
// judgement, and they are not on the same scale: the heuristic's scores
// cluster low, so judging it at 0.5 would report a weakness it does not have.
// Giving each its best cut compares what each can actually distinguish.
func bestThreshold(scores []float64) (cut float64, correct int) {
	for step := 0; step <= 100; step++ {
		t := float64(step) / 100
		var n int
		for i, s := range scores {
			if (s >= t) == corpus[i].want {
				n++
			}
		}
		if n > correct {
			correct, cut = n, t
		}
	}
	return cut, correct
}

type outcome struct {
	name           string
	correct        int
	tp, fp, tn, fn int
	threshold      float64
	undecided      int
	calls          int
	elapsed        time.Duration
	wrong          []string
}

func (o outcome) accuracy() float64 { return float64(o.correct) / float64(len(corpus)) }

func (o outcome) precision() float64 {
	if o.tp+o.fp == 0 {
		return 0
	}
	return float64(o.tp) / float64(o.tp+o.fp)
}

func (o outcome) recall() float64 {
	if o.tp+o.fn == 0 {
		return 0
	}
	return float64(o.tp) / float64(o.tp+o.fn)
}

// run scores the whole corpus with one matcher, at the threshold that suits it
// best.
func run(t *testing.T, name string, m wom.Matcher) outcome {
	t.Helper()

	out := outcome{name: name}
	start := time.Now()

	scores := make([]float64, len(corpus))
	for i, c := range corpus {
		node := page(t, "<html><body>"+c.html+"</body></html>", c.value)
		scores[i] = m.Score(context.Background(), c.prop, node)
	}
	out.elapsed = time.Since(start)
	out.threshold, _ = bestThreshold(scores)

	for i, c := range corpus {
		got := scores[i] >= out.threshold

		switch {
		case got && c.want:
			out.tp++
		case got && !c.want:
			out.fp++
		case !got && !c.want:
			out.tn++
		default:
			out.fn++
		}
		if got == c.want {
			out.correct++
		} else {
			out.wrong = append(out.wrong, c.name)
		}
	}

	if l, ok := m.(*LLM); ok {
		s := l.Stats()
		out.undecided, out.calls = s.Misses, s.Calls
	}
	return out
}

func report(t *testing.T, results ...outcome) {
	t.Helper()

	t.Logf("%-12s %8s %10s %8s %6s %10s %7s %10s",
		"MATCHER", "ACCURACY", "PRECISION", "RECALL", "CUT", "UNDECIDED", "CALLS", "ELAPSED")
	for _, r := range results {
		t.Logf("%-12s %7.0f%% %9.2f %8.2f %6.2f %10d %7d %10s",
			r.name, r.accuracy()*100, r.precision(), r.recall(), r.threshold,
			r.undecided, r.calls, r.elapsed.Round(time.Millisecond))
	}
	for _, r := range results {
		if len(r.wrong) > 0 {
			sort.Strings(r.wrong)
			t.Logf("%s missed: %s", r.name, strings.Join(r.wrong, ", "))
		}
	}
}

// TestHeuristicBaseline measures the built-in matcher on the corpus. It always
// runs, so the number the model is compared against is never stale.
func TestHeuristicBaseline(t *testing.T) {
	result := run(t, "heuristic", wom.Heuristic{})
	report(t, result)

	// The corpus is deliberately hard, but a matcher that cannot beat a coin
	// toss on it is broken rather than merely limited.
	if result.accuracy() < 0.5 {
		t.Errorf("the heuristic scored %.0f%%, which is below chance", result.accuracy()*100)
	}
}

// TestMatcherBenchmark compares the heuristic against a real local model.
//
// It is skipped unless SCOUR_BENCH_MODEL names one, because a test suite that
// depends on a running model server is a test suite that fails on a laptop.
//
//	SCOUR_BENCH_MODEL=gemma3:270m go test ./internal/matcher/ -run Benchmark -v
func TestMatcherBenchmark(t *testing.T) {
	model := os.Getenv("SCOUR_BENCH_MODEL")
	if model == "" {
		t.Skip("set SCOUR_BENCH_MODEL to compare against a local model")
	}

	endpoint := os.Getenv("SCOUR_BENCH_ENDPOINT")
	provider, err := ai.NewOllama(ai.Config{
		Name: "bench", Model: model, Endpoint: endpoint,
		Timeout: 2 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	llm, err := NewLLM(Config{Provider: provider, Budget: -1})
	if err != nil {
		t.Fatal(err)
	}

	baseline := run(t, "heuristic", wom.Heuristic{})
	judged := run(t, "llm", llm)
	report(t, baseline, judged)

	saved := judged.undecided - judged.calls
	t.Logf("cache absorbed %d of %d undecided judgements", saved, judged.undecided)
	t.Logf("the model was consulted on %d of %d candidates (%.0f%%)",
		judged.calls, len(corpus), float64(judged.calls)/float64(len(corpus))*100)

	// The cascade is the claim being tested: a model that is asked about every
	// candidate is not the design, whatever its accuracy.
	if judged.calls >= len(corpus) {
		t.Errorf("the model was asked about %d of %d candidates; the cascade is not filtering",
			judged.calls, len(corpus))
	}

	if judged.accuracy() < baseline.accuracy() {
		t.Logf("NOTE: %s scored below the heuristic (%.0f%% against %.0f%%). "+
			"A small model is not automatically better than a good heuristic.",
			model, judged.accuracy()*100, baseline.accuracy()*100)
	}
}

// TestBenchmarkCorpusIsBalanced guards the benchmark itself: a corpus that is
// mostly one answer can be topped by a matcher that always gives it.
func TestBenchmarkCorpusIsBalanced(t *testing.T) {
	var yes int
	for _, c := range corpus {
		if c.want {
			yes++
		}
	}

	share := float64(yes) / float64(len(corpus))
	if share < 0.35 || share > 0.65 {
		t.Errorf("%d of %d cases are positive (%.0f%%); always guessing would score too well",
			yes, len(corpus), share*100)
	}

	// The same value in different markup is the point of the corpus, so
	// duplication is only a mistake when the whole case repeats.
	seen := map[string]bool{}
	for _, c := range corpus {
		key := fmt.Sprintf("%s|%s|%s", c.prop.Name, c.value, c.html)
		if seen[key] {
			t.Errorf("duplicate case %q", c.name)
		}
		seen[key] = true

		if seen["name:"+c.name] {
			t.Errorf("two cases named %q", c.name)
		}
		seen["name:"+c.name] = true
	}
}
