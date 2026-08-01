// SPDX-License-Identifier: GPL-3.0-or-later

package nodeclass

import (
	"context"
	"errors"
	"testing"
)

// A node carries one answer per question, so classifiers of different kinds
// both run and a second kind arriving does not displace the first.
func TestOfKindSelectsByQuestion(t *testing.T) {
	got, err := OfKind(KindRecency, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name() != "recency" {
		t.Fatalf("OfKind(recency) = %v", names(got))
	}
	if got, err = OfKind(KindTopic, Config{}); err != nil || len(got) != 1 {
		t.Fatalf("OfKind(topic) = %v, %v", names(got), err)
	}
	// Nothing answers this yet, and asking is not an error.
	if got, err = OfKind(KindRole, Config{}); err != nil || len(got) != 0 {
		t.Fatalf("OfKind(role) = %v, %v", names(got), err)
	}
}

// A registered but unwritten classifier must say so, rather than resolving to
// "unknown", which reads as a typo and sends someone looking for the spelling.
func TestPlannedClassifiersAnswerNothingAndSaySo(t *testing.T) {
	for _, name := range []string{"recency", "topic"} {
		c, err := New(name, Config{})
		if err != nil {
			t.Fatalf("%s is not registered: %v", name, err)
		}
		if len(c.Labels()) == 0 {
			t.Errorf("%s declares no vocabulary, which is the point of registering it early", name)
		}
		out, err := c.Classify(context.Background(), []Node{{URL: "http://example.com/"}})
		if !errors.Is(err, ErrNotImplemented) {
			t.Errorf("%s.Classify err = %v, want ErrNotImplemented", name, err)
		}
		if out != nil {
			t.Errorf("%s returned verdicts it cannot have: %v", name, out)
		}
	}
}

// An unknown name is a different answer from an unwritten one.
func TestUnknownClassifierIsNotThePlannedOne(t *testing.T) {
	if _, err := New("nonsense", Config{}); err == nil {
		t.Fatal("an unregistered name must fail")
	} else if errors.Is(err, ErrNotImplemented) {
		t.Error("an unknown name must not look like a planned classifier")
	}
	if !Has("recency") || Has("nonsense") {
		t.Error("Has disagrees with what is registered")
	}
}

func names(cs []Classifier) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Name())
	}
	return out
}
