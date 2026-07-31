// SPDX-License-Identifier: GPL-3.0-or-later

package hmm

import (
	"encoding/json"
	"testing"

	"github.com/rangertaha/scour/internal/score"
)

func TestDefaultChainIsWellFormed(t *testing.T) {
	c := Default()
	if !c.Valid() {
		t.Fatal("the shipped prior is malformed")
	}

	sum := func(row []float64) float64 {
		var total float64
		for _, v := range row {
			total += v
		}
		return total
	}
	if got := sum(c.Start); got < 0.99 || got > 1.01 {
		t.Errorf("start probabilities sum to %v", got)
	}
	for i, row := range c.Trans {
		if got := sum(row); got < 0.99 || got > 1.01 {
			t.Errorf("transitions from %s sum to %v", Role(i), got)
		}
	}
	for i, row := range c.Emit {
		if got := sum(row); got < 0.99 || got > 1.01 {
			t.Errorf("emissions from %s sum to %v", Role(i), got)
		}
	}
}

func TestDecodeUsesContextNotJustThePage(t *testing.T) {
	c := Default()

	// The same observation, a page with links and no records, means different
	// things depending on what came before it. That is the whole reason for
	// decoding a path rather than classifying a page.
	fromSeed := c.Decode([]Observation{Links, Links, Records})
	if len(fromSeed) != 3 {
		t.Fatalf("decoded %d roles, want 3", len(fromSeed))
	}
	if fromSeed[0] != Seed {
		t.Errorf("the first page decoded as %s, want seed", fromSeed[0])
	}
	if fromSeed[2] != Detail {
		t.Errorf("a page holding records decoded as %s, want detail", fromSeed[2])
	}
	if fromSeed[1] != Hub && fromSeed[1] != Pagination {
		t.Errorf("the page between a seed and a record decoded as %s, want hub or pagination", fromSeed[1])
	}
}

func TestDecodeMarksDeadEnds(t *testing.T) {
	roles := Default().Decode([]Observation{Links, Barren, Failed})
	if len(roles) != 3 {
		t.Fatalf("decoded %d roles", len(roles))
	}
	if roles[2] != Dead {
		t.Errorf("a failed fetch decoded as %s, want dead", roles[2])
	}
	if roles[1] == Detail {
		t.Errorf("a page with nothing on it decoded as detail")
	}
}

func TestDecodeEmptyAndInvalid(t *testing.T) {
	if got := Default().Decode(nil); got != nil {
		t.Errorf("decoding nothing returned %v", got)
	}
	var broken *Chain
	if got := broken.Decode([]Observation{Links}); got != nil {
		t.Errorf("decoding with a nil chain returned %v", got)
	}
}

// A hub is the case the chain exists for: it holds no records at all, so every
// per-page signal says it is worthless, and yet it is the only way to the
// records.
func TestReachCreditsHubsOverDeadEnds(t *testing.T) {
	c := Default()

	hub := c.Reach(Hub, 2)
	boilerplate := c.Reach(Boilerplate, 2)
	dead := c.Reach(Dead, 2)

	if hub <= boilerplate {
		t.Errorf("hub reach %v, boilerplate reach %v: the hub must win", hub, boilerplate)
	}
	if boilerplate <= dead {
		t.Errorf("boilerplate reach %v, dead reach %v", boilerplate, dead)
	}
	if hub < 0.5 {
		t.Errorf("hub reach %v, want a hub to be worth following", hub)
	}
}

func TestReachIsBounded(t *testing.T) {
	c := Default()
	for r := range NumRoles {
		for _, hops := range []int{0, 1, 2, 5, 20} {
			got := c.Reach(Role(r), hops)
			if got < 0 || got > 1 {
				t.Errorf("Reach(%s, %d) = %v, outside [0,1]", Role(r), hops, got)
			}
		}
	}
	if got := c.Reach(Hub, 0); got != 0 {
		t.Errorf("Reach with no hops = %v, want 0", got)
	}
}

func TestFitMovesTowardsTheEvidence(t *testing.T) {
	c := Default()
	before := c.Trans[Hub][Detail]

	// A site where hubs always lead to records.
	var paths [][]Observation
	for range 50 {
		paths = append(paths, []Observation{Links, Links, Records})
	}
	if err := c.Fit(paths); err != nil {
		t.Fatalf("Fit: %v", err)
	}

	if c.Observations != 50 {
		t.Errorf("observations = %d, want 50", c.Observations)
	}
	if c.Trans[Hub][Detail] <= before {
		t.Errorf("hub to detail went from %v to %v, want the evidence to raise it",
			before, c.Trans[Hub][Detail])
	}
	if !c.Valid() {
		t.Error("fitting produced a malformed chain")
	}
}

// The prior has to survive a handful of paths, or one unusual corner of a site
// rewrites what the crawler believes about every site.
func TestFitKeepsThePriorOnLittleData(t *testing.T) {
	c := Default()
	before := c.Trans[Hub][Detail]

	if err := c.Fit([][]Observation{{Barren, Barren}}); err != nil {
		t.Fatal(err)
	}

	if diff := c.Trans[Hub][Detail] - before; diff < -0.15 || diff > 0.15 {
		t.Errorf("one path moved hub to detail by %v, want the prior to hold", diff)
	}
}

func TestFitLeavesEmissionsAlone(t *testing.T) {
	c := Default()
	before := make([][]float64, len(c.Emit))
	for i, row := range c.Emit {
		before[i] = append([]float64(nil), row...)
	}

	var paths [][]Observation
	for range 100 {
		paths = append(paths, []Observation{Links, Records})
	}
	if err := c.Fit(paths); err != nil {
		t.Fatal(err)
	}

	// Emissions are what anchor a state to its name. Re-estimating them is how
	// an unsupervised chain ends up with six states that mean nothing.
	for i := range c.Emit {
		for j := range c.Emit[i] {
			if c.Emit[i][j] != before[i][j] {
				t.Fatalf("emissions for %s changed during fitting", Role(i))
			}
		}
	}
}

func TestFitOnNothing(t *testing.T) {
	c := Default()
	if err := c.Fit(nil); err != nil {
		t.Errorf("fitting nothing should be a no-op, got %v", err)
	}
}

func TestRoundTrip(t *testing.T) {
	c := Default()
	if err := c.Fit([][]Observation{{Links, Records}}); err != nil {
		t.Fatal(err)
	}

	encoded, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Parse(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if back.Reach(Hub, 2) != c.Reach(Hub, 2) {
		t.Error("a chain changed across a round trip")
	}
}

func TestParseFallsBackRatherThanFailing(t *testing.T) {
	// An empty or unusable chain must not stop a crawl; the prior works.
	c, err := Parse(nil)
	if err != nil || !c.Valid() {
		t.Errorf("Parse(nil) = %v, %v", c, err)
	}
	c, err = Parse([]byte(`{"start":[0.5,0.5]}`))
	if err != nil || !c.Valid() {
		t.Errorf("a malformed chain should fall back to the prior, got %v, %v", c, err)
	}
	if _, err := Parse([]byte("not json")); err == nil {
		t.Error("unparseable input should be an error")
	}
}

func TestRoleNames(t *testing.T) {
	for i := range NumRoles {
		name := Role(i).String()
		back, ok := ParseRole(name)
		if !ok || back != Role(i) {
			t.Errorf("role %d did not round trip through %q", i, name)
		}
	}
	if got := Role(99).String(); got != "unknown" {
		t.Errorf("out of range role = %q", got)
	}
}

// The scorer is the point of contact between the chain and the crawl: a link
// on a hub must beat the same link on a dead end.
func TestScorerCreditsLinksByWhereTheySit(t *testing.T) {
	base := score.FuncScorer(func(score.Features) float64 { return 0.3 })
	s := NewScorer(base, Default(), map[string]Role{
		"http://example.com/cars/":  Hub,
		"http://example.com/legal/": Dead,
	})

	onHub := s.Score(score.Features{URL: "http://example.com/x", Parent: "http://example.com/cars/"})
	onDead := s.Score(score.Features{URL: "http://example.com/x", Parent: "http://example.com/legal/"})
	unknown := s.Score(score.Features{URL: "http://example.com/x", Parent: "http://example.com/never-seen/"})

	if onHub <= onDead {
		t.Errorf("a link on a hub scored %v, on a dead end %v", onHub, onDead)
	}
	if onHub <= 0.3 {
		t.Errorf("a link on a hub scored %v, want the chain to raise it above the base %v", onHub, 0.3)
	}
	if onDead >= 0.3 {
		t.Errorf("a link on a dead end scored %v, want the chain to lower it", onDead)
	}
	if unknown != 0.3 {
		t.Errorf("a link whose parent has no role scored %v, want the base score untouched", unknown)
	}
}

func TestScorerStaysInRange(t *testing.T) {
	for _, base := range []float64{0, 0.001, 0.5, 0.999, 1} {
		s := NewScorer(score.FuncScorer(func(score.Features) float64 { return base }),
			Default(), map[string]Role{"p": Hub})
		got := s.Score(score.Features{URL: "http://example.com/", Parent: "p"})
		if got < 0 || got > 1 {
			t.Errorf("base %v produced %v, outside [0,1]", base, got)
		}
	}
}

func TestCombineIsSymmetricAndNeutral(t *testing.T) {
	if got := combine(0.5, 0.5); got < 0.49 || got > 0.51 {
		t.Errorf("combining two coin flips = %v, want to stay at 0.5", got)
	}
	if a, b := combine(0.8, 0.3), combine(0.3, 0.8); a != b {
		t.Errorf("combine is not symmetric: %v and %v", a, b)
	}
	if got := combine(0.9, 0.9); got <= 0.9 {
		t.Errorf("two agreeing signals = %v, want more than either alone", got)
	}
}

// A URL whose own role is known is direct observation, not inference: the
// crawler saw what that page was last time. It has to beat the base scorer
// outright, because a hub's per-URL signals all say to skip it.
func TestKnownRoleOfTheURLItselfWins(t *testing.T) {
	base := score.FuncScorer(func(score.Features) float64 { return 0.02 })
	s := NewScorer(base, Default(), map[string]Role{
		"http://example.com/listing/": Hub,
		"http://example.com/p/1/":     Detail,
		"http://example.com/legal/":   Boilerplate,
	})

	hub := s.Score(score.Features{URL: "http://example.com/listing/"})
	detail := s.Score(score.Features{URL: "http://example.com/p/1/"})
	boiler := s.Score(score.Features{URL: "http://example.com/legal/"})
	unseen := s.Score(score.Features{URL: "http://example.com/new/"})

	if detail < 0.99 {
		t.Errorf("a known detail page scored %v, want near certainty", detail)
	}
	if hub <= 0.5 {
		t.Errorf("a known hub scored %v, want it worth following despite holding nothing", hub)
	}
	if hub >= detail {
		t.Errorf("hub %v should rank below detail %v", hub, detail)
	}
	if boiler >= hub {
		t.Errorf("boilerplate %v should rank below the hub %v", boiler, hub)
	}
	if unseen != 0.02 {
		t.Errorf("an unseen URL with no known parent scored %v, want the base score", unseen)
	}
}

func TestKnownRoleNeverLowersAConfidentBase(t *testing.T) {
	base := score.FuncScorer(func(score.Features) float64 { return 0.95 })
	s := NewScorer(base, Default(), map[string]Role{"http://example.com/x": Boilerplate})

	// The chain says the page was boilerplate; the scorer is sure it is not.
	// Taking the lower of the two would throw away what the scorer learned.
	if got := s.Score(score.Features{URL: "http://example.com/x"}); got != 0.95 {
		t.Errorf("score = %v, want the confident base to stand", got)
	}
}
