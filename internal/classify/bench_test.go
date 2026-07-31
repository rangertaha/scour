// SPDX-License-Identifier: GPL-3.0-or-later

package classify

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/ai"
)

// The corpus is the mix a real site has: the records, the index that lists
// them, and the furniture that surrounds both. The furniture is the point. A
// classifier that recognises a vehicle page is easy; one that declines to call
// the privacy notice a vehicle page is the whole feature.
type page struct {
	name  string
	title string
	text  string
	// want is whether the page is genuinely about the subject.
	want bool
}

var corpus = []page{
	{"listing", "car", "Ford F-150 $42,000 2026 back to index", true},
	{"listing2", "car", "Ram 1500 $39,500 2026 back to index", true},
	{"listing3", "car", "Chevrolet Silverado $44,250 2026 back to index", true},
	{"index", "Trucks", "Truck index Ford F-150 Ram 1500 Chevy Silverado About us Privacy Bakewell tart", true},
	{"detail-prose", "2019 Toyota RAV4", "Used 2019 Toyota RAV4 Hybrid LE AWD. Mileage 18,220. Asking $28,400. Silver exterior, black cloth interior. One owner, clean history report.", true},

	{"recipe", "Bakewell tart", "Bakewell Tart Preheat the oven to 180C. Cream the butter and sugar until pale, then fold in the ground almonds and flour. Spread raspberry jam over the pastry base and bake for 25 minutes until golden. home", false},
	{"legal", "Privacy Policy", "Privacy Policy We collect personal information when you use our services. This notice explains what we collect, how we use it, and the choices you have. We retain records for seven years. home", false},
	{"about", "About us", "About Us Family owned since 1974. Visit our showroom at 1400 Industrial Parkway. Open Monday to Friday, 9am to 6pm. Phone 555-0142. home", false},
	{"jobs", "Careers", "Senior Backend Engineer, Austin TX. Full-time. $140,000 to $180,000. We are looking for someone with five years of Go experience. Apply by email.", false},
	{"finance", "Finance", "Compare loan rates from twelve lenders. Terms from 24 to 72 months. Representative APR 7.9 percent. Apply online in three minutes.", false},
}

// TestBenchmarkCorpusIsBalanced guards the benchmark: a corpus that is mostly
// one answer can be topped by a classifier that always gives it.
func TestBenchmarkCorpusIsBalanced(t *testing.T) {
	var yes int
	for _, p := range corpus {
		if p.want {
			yes++
		}
	}
	if share := float64(yes) / float64(len(corpus)); share < 0.35 || share > 0.65 {
		t.Errorf("%d of %d pages are on topic (%.0f%%)", yes, len(corpus), share*100)
	}
}

// TestClassifierBenchmark measures a real model on the corpus.
//
//	SCOUR_BENCH_MODEL=gemma3:270m go test ./internal/classify/ -run Benchmark -v
//
// SCOUR_BENCH_SUBJECT sets the entity name, which matters more than it looks:
// an entity named after a real word and one named "api-cars" are not the same
// question to a model.
func TestClassifierBenchmark(t *testing.T) {
	model := os.Getenv("SCOUR_BENCH_MODEL")
	if model == "" {
		t.Skip("set SCOUR_BENCH_MODEL to measure a local model")
	}

	provider, err := ai.NewOllama(ai.Config{
		Name: "bench", Model: model,
		Endpoint: os.Getenv("SCOUR_BENCH_ENDPOINT"),
		Timeout:  2 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	subject := os.Getenv("SCOUR_BENCH_SUBJECT")
	if subject == "" {
		subject = "vehicle"
	}

	topics := []struct {
		label string
		topic Topic
	}{
		{"name alone", Topic{Name: subject}},
		{"name and aliases", Topic{Name: subject, Aliases: []string{"car", "truck", "automobile"}}},
	}

	for _, tt := range topics {
		c, err := NewLLM(Config{Provider: provider, Budget: -1})
		if err != nil {
			t.Fatal(err)
		}

		var correct, falsePos, falseNeg int
		var wrong []string
		start := time.Now()

		for _, p := range corpus {
			got, err := c.Classify(context.Background(), tt.topic, Page{Title: p.title, Text: p.text})
			if err != nil {
				t.Fatalf("%s: %v", p.name, err)
			}

			relevant := got.Relevant()
			switch {
			case relevant == p.want:
				correct++
			case relevant:
				falsePos++
				wrong = append(wrong, p.name+" (called on topic)")
			default:
				falseNeg++
				wrong = append(wrong, p.name+" (called off topic)")
			}
		}

		t.Logf("%-18s %d/%d correct, %d false positive, %d false negative, %s",
			tt.label, correct, len(corpus), falsePos, falseNeg,
			time.Since(start).Round(time.Millisecond))
		if len(wrong) > 0 {
			t.Logf("    missed: %s", strings.Join(wrong, ", "))
		}

		// A false positive is the expensive error here: it teaches the URL
		// scorer that a privacy notice was worth fetching, which is exactly the
		// noise the classifier exists to remove.
		if falsePos > len(corpus)/2 {
			t.Logf("    NOTE: %s calls most off-topic pages on topic, which would make "+
				"labels worse rather than better. Leave the classifier off for this model.", model)
		}
	}
}
