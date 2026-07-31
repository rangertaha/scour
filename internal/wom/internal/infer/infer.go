// SPDX-License-Identifier: MIT

// Package infer locates schema properties in a graph. It owns the structural
// half of the problem — grouping equivalent nodes and finding record
// containers — and delegates semantics to a Matcher and field ordering to a
// Sequence.
package infer

import (
	"context"
	"math"
	"sort"
	"strings"

	"github.com/rangertaha/scour/internal/wom/internal/graph"
	"github.com/rangertaha/scour/internal/wom/internal/match"
	"github.com/rangertaha/scour/internal/wom/internal/pattern"
	"github.com/rangertaha/scour/internal/wom/internal/schema"
	"github.com/rangertaha/scour/internal/wom/internal/seq"
)

// Inference proceeds in three stages.
//
//  1. Score. Every value-holding node is scored against the prop by the
//     Matcher. This is local evidence only: text, labels, and type.
//
//  2. Group. Matches are collapsed by their generalized path, so a value that
//     occurs once per list item across many pages becomes a single location
//     with high support rather than dozens of coincidences. This is the step
//     that turns instances into a pattern, and it is where a tree beats a
//     chain: repeated subtree shape is directly observable.
//
//  3. Synthesize. The winning group's nodes are turned into a schema.Locator — a URI
//     pattern, an XPath, a CSS selector, a native path, and an extraction
//     regex — each generalized over exactly the parts that varied.
//
// A prop with nested props adds a container stage between 2 and 3: the record
// container is the deepest node covering the most fields, and the fields are
// then re-scored within it by the sequence model before being addressed
// relative to it.

// Engine runs inference against a graph. It is configured once and reused;
// all of its state is read-only during a call, so one Engine serves concurrent
// callers.
type Engine struct {
	// Matcher supplies the semantic judgement and the sequence model's
	// emissions. Required.
	Matcher match.Matcher

	// Sequence refines per-node scores using field order. Nil disables it.
	Sequence seq.Sequence

	// MinProbability is the confidence below which a located item is dropped.
	MinProbability float64
}

// Infer locates each prop in the given documents and returns the items that
// cleared the confidence threshold. Props that could not be located are
// omitted rather than returned with a zero probability.
func (e *Engine) Infer(ctx context.Context, props []schema.Prop, docs []*graph.Node) ([]schema.Item, error) {
	if len(docs) == 0 {
		return nil, nil
	}
	return e.infer(ctx, props, candidatesIn(docs))
}

// candidatesUnder collects the value-holding nodes beneath a set of
// containers.
func candidatesUnder(containers []*graph.Node) []*graph.Node {
	out := make([]*graph.Node, 0, len(containers))
	for _, c := range containers {
		for _, n := range graph.ValueNodes(c) {
			if isCandidateValue(n) {
				out = append(out, n)
			}
		}
	}
	return out
}

// clamp confines a score to [0,1].
func clamp(f float64) float64 {
	switch {
	case f < 0:
		return 0
	case f > 1:
		return 1
	}
	return f
}

// scoreFloor is the local score below which a node is not considered a match
// at all. It is deliberately lower than the reported-probability threshold so
// that weak-but-consistent evidence can still accumulate into a confident
// group.
const scoreFloor = 0.15

// maxSampleValues caps how many observed values an schema.Item reports.
const maxSampleValues = 5

// result is an inferred item together with the nodes that produced it.
type result struct {
	item  schema.Item
	nodes []*graph.Node
}

// candidatesIn collects every value-holding node in the given documents.
func candidatesIn(docs []*graph.Node) []*graph.Node {
	var out []*graph.Node
	for _, d := range docs {
		d.Walk(func(n *graph.Node) bool {
			if n.Kind.HoldsValue() && isCandidateValue(n) {
				out = append(out, n)
			}
			return true
		})
	}
	return out
}

// maxValueLength is the longest text a single field value may be.
//
// A field holds a value, not a document. The bound exists because whole
// programs reach the graph as one node: a news page yielded a publisher of
// 28,610 characters, the entire contents of an inline `window.config = {...}`
// blob, which happened to mention the publisher's name and so outscored the
// real one. Every genuine value on that page was under 160 characters.
//
// It is set far above any real field rather than close to one, so it rules out
// documents-as-values without deciding how long a summary is allowed to be.
const maxValueLength = 4096

// isCandidateValue reports whether a node could hold a field's value.
func isCandidateValue(n *graph.Node) bool {
	text := n.Text()
	return text != "" && len(text) <= maxValueLength && !isProgram(n) && !isNamespace(n)
}

// isNamespace reports whether a node is an XML namespace declaration.
//
// A namespace is machinery, not data, and it competes with the value it
// annotates on equal terms. Every <dc:creator> in a feed carries an xmlns node
// holding "http://purl.org/dc/elements/1.1/", and because a namespaced element
// lends its name to everything inside it, that URI is labelled "creator" just as
// the byline is. The two scored identically for `author` on the Guardian's feed,
// and the attribute won the tie on nothing better than sorting before text().
func isNamespace(n *graph.Node) bool {
	return n.Kind == graph.KindAttribute &&
		(n.Name == "xmlns" || strings.HasPrefix(n.Name, "xmlns:"))
}

// isProgram reports whether a node is the source text of a script or a
// stylesheet.
//
// A page's JavaScript is not data about the page. Left in, it competes for
// every field and sometimes wins: on a real news site the publisher came back
// as "window.guardian = {"config":{"isDotcomRendering":true,...", which is one
// enormous text node that happens to mention the publisher's name.
//
// The structured data inside a script is not lost by this. A JSON-LD block is
// parsed into its own nested document when the page is read, so its fields
// remain candidates as parsed JSON, addressed by key rather than by scraping
// the source it arrived in.
func isProgram(n *graph.Node) bool {
	for c := n; c != nil; c = c.Parent {
		if c.Kind == graph.KindDocument {
			// Stop at the document boundary, so an embedded JSON-LD document
			// is judged on its own and not on the script element holding it.
			return false
		}
		if c.Kind == graph.KindElement && (c.Name == "script" || c.Name == "style") {
			return true
		}
	}
	return false
}

// infer locates each prop and returns the items that cleared the confidence
// threshold. Props that could not be located are omitted rather than returned
// with a zero probability.
func (e *Engine) infer(ctx context.Context, props []schema.Prop, cands []*graph.Node) ([]schema.Item, error) {
	items := make([]schema.Item, 0, len(props))
	for _, p := range props {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		res, ok := e.inferProp(ctx, p, cands, nil)
		if ok && res.item.Probability >= e.MinProbability {
			items = append(items, res.item)
		}
	}
	return items, nil
}

// inferProp dispatches on whether the prop is a record. bases, when non-nil,
// is the set of container nodes the resulting locators should be expressed
// relative to.
func (e *Engine) inferProp(ctx context.Context, p schema.Prop, cands []*graph.Node, bases map[*graph.Node]bool) (*result, bool) {
	if p.IsRecord() {
		return e.inferRecord(ctx, p, cands, bases)
	}
	return e.inferField(ctx, p, cands, bases)
}

// scored pairs a candidate node with its local score.
type scored struct {
	node  *graph.Node
	score float64
}

// scoreAll scores every candidate against p, keeping those above the floor.
func (e *Engine) scoreAll(ctx context.Context, p schema.Prop, cands []*graph.Node) []scored {
	out := make([]scored, 0, 16)
	for _, n := range cands {
		if s := e.Matcher.Score(ctx, p, n); s >= scoreFloor {
			out = append(out, scored{node: n, score: s})
		}
	}
	return out
}

// inferField locates a single-valued prop.
func (e *Engine) inferField(ctx context.Context, p schema.Prop, cands []*graph.Node, bases map[*graph.Node]bool) (*result, bool) {
	matches := e.scoreAll(ctx, p, cands)
	if len(matches) == 0 {
		return nil, false
	}
	best := bestGroup(groupMatches(matches, bases))
	if best == nil {
		return nil, false
	}
	return buildResult(p, best, bases), true
}

// group is a set of matches sharing a generalized location.
type group struct {
	key     string
	disc    pattern.Discriminator
	matches []scored
	// spread is how many record units the group was observed in: distinct
	// containers when the locator is relative to one, distinct documents
	// otherwise. It is the count of independent observations, which is not the
	// same as the number of matches.
	spread int
	// conflicts is how many of those units the group matched more than one
	// distinct value in.
	conflicts int
}

// groupMatches buckets matches by the location they generalize to. When bases
// is set the key is the path relative to the enclosing container, so the same
// field in different list items lands in one bucket.
func groupMatches(matches []scored, bases map[*graph.Node]bool) []group {
	byKey := make(map[string][]scored)
	discs := make(map[string]pattern.Discriminator)
	var order []string
	for _, m := range matches {
		disc := pattern.DiscriminatorFor(m.node)
		key := m.node.Format().String() + "|" + pattern.Coarse(pathFor(m.node, bases))
		if disc.Set() {
			key += "|@" + disc.Name + "=" + disc.Value
		}
		if _, seen := byKey[key]; !seen {
			order = append(order, key)
			discs[key] = disc
		}
		byKey[key] = append(byKey[key], m)
	}
	out := make([]group, 0, len(order))
	for _, k := range order {
		sp, cf := reach(byKey[k], bases)
		out = append(out, group{
			key: k, disc: discs[k], matches: byKey[k],
			spread: sp, conflicts: cf,
		})
	}
	return out
}

// unitOf returns the record unit a node belongs to: its container when the
// locator is relative to one, its document otherwise.
func unitOf(n *graph.Node, bases map[*graph.Node]bool) *graph.Node {
	if b := baseOf(n, bases); b != nil {
		return b
	}
	return n.Document()
}

// reach reports how many record units a group was observed in, and in how many
// of them its matches disagreed with each other.
//
// The two are different questions and only the second says anything is wrong.
// A location matching twice in one record with the same text is duplicated
// markup, which is common and harmless: one real site publishes
// <meta name="author"> twice per page with the identical byline. A location
// matching twice with different text is not one location at all. The same site
// publishes <meta property="article:author"> twice, once with the byline and
// once with a Facebook URL, and half its records came back with the URL as the
// author.
func reach(matches []scored, bases map[*graph.Node]bool) (spread, conflicts int) {
	byUnit := make(map[*graph.Node]map[string]bool, len(matches))
	// Disagreement is only meaningful once the record's boundary is known.
	// Before a container is chosen the unit is the whole document, and a
	// document holding many records disagrees with itself by construction: a
	// feed of forty-five articles has forty-five different titles in one file,
	// which says nothing about the locator and everything about the feed.
	known := len(bases) > 0
	for _, m := range matches {
		u := unitOf(m.node, bases)
		if u == nil {
			continue
		}
		if byUnit[u] == nil {
			byUnit[u] = map[string]bool{}
		}
		byUnit[u][m.node.Text()] = true
	}
	if known {
		for _, texts := range byUnit {
			if len(texts) > 1 {
				conflicts++
			}
		}
	}
	if len(byUnit) == 0 {
		return 1, conflicts
	}
	return len(byUnit), conflicts
}

// confidence is the group's aggregate score.
func (g group) confidence() float64 { return aggregate(g.matches, g.spread, g.conflicts) }

// bestGroup ranks groups by aggregate confidence and returns the strongest.
func bestGroup(groups []group) *group {
	if len(groups) == 0 {
		return nil
	}
	// Reach has to count in the score, not only as a tiebreak. supportFactor
	// saturates at five observations, so once the whole page is in scope it
	// cannot tell a location found on 23 records from one found on 288: a body
	// div present on a single site outscored meta[@name="author"] present on
	// thirteen. Measuring reach against the best-reaching group keeps this
	// independent of how large the corpus is.
	var widest int
	for _, g := range groups {
		if g.spread > widest {
			widest = g.spread
		}
	}
	sort.SliceStable(groups, func(i, j int) bool {
		gi := groups[i].confidence() * reachFactor(groups[i].spread, widest)
		gj := groups[j].confidence() * reachFactor(groups[j].spread, widest)
		if gi != gj {
			return gi > gj
		}
		// Reach first: a location found on more documents is a better
		// description of the site than one found on fewer.
		di, dj := groups[i].spread, groups[j].spread
		if di != dj {
			return di > dj
		}
		if len(groups[i].matches) != len(groups[j].matches) {
			return len(groups[i].matches) > len(groups[j].matches)
		}
		return groups[i].key < groups[j].key
	})
	return &groups[0]
}

// aggregate converts a group's local scores into a confidence.
//
// The quantity being estimated is whether the *location* is right, not
// whether every instance matches. A group's nodes are structurally identical
// by construction, so one instance matching an example dead-on is strong
// evidence for the whole location — a schema listing "Toyota" as an example
// should not be penalised because the other rows say "Honda" and "Ford".
// Confidence is therefore an even blend of the mean and the best score, then
// damped by how much the location repeated.
func aggregate(matches []scored, spread, conflicts int) float64 {
	if len(matches) == 0 {
		return 0
	}
	var sum, best float64
	for _, m := range matches {
		sum += m.score
		if m.score > best {
			best = m.score
		}
	}
	mean := sum / float64(len(matches))
	return clamp((0.5*mean + 0.5*best) * supportFactor(spread) * agreement(spread, conflicts))
}

// weakFieldShare is how far below the strongest field a field may fall and
// still help locate the record. It is a share rather than an absolute score
// because scores are only comparable within one document: what matters is that
// this field is much less certain than the ones beside it.
const weakFieldShare = 0.5

// confidentHits drops the hits of fields far less certain than their siblings.
func confidentHits(hits [][]*graph.Node, confidence []float64) [][]*graph.Node {
	var strongest float64
	for _, c := range confidence {
		if c > strongest {
			strongest = c
		}
	}
	if strongest == 0 {
		return hits
	}

	out := make([][]*graph.Node, len(hits))
	var kept int
	for i, nodes := range hits {
		if confidence[i] >= strongest*weakFieldShare {
			out[i] = nodes
			kept++
		}
	}
	// If that leaves nothing to go on, the fields are uniformly weak rather
	// than one being an outlier, and the original evidence is all there is.
	if kept == 0 {
		return hits
	}
	return out
}

// rivalShare is how close to the best group an alternative location must score
// to stay in contention while the record's container is being chosen.
const rivalShare = 0.6

// rivalGroups returns the locations a field might plausibly occupy: its best
// group and any scoring within rivalShare of it.
//
// Committing each field to one location before the container is known makes a
// local mistake fatal. The Guardian's feed shows why. Its channel carries an
// <image> describing the feed's own logo, with a <title> and a <link> inside
// it, and those outscored the real summary and link sitting in every <item>.
// Two fields then lived under <image> and four under <item>, so the only
// ancestor holding all six was the channel: one record where there were
// forty-five.
//
// Neither field was uncertain enough for confidentHits to drop, and neither
// could be dropped on its own, because removing one still leaves the other
// outside the item. They are only wrong together, and only in the light of a
// container neither of them had seen yet.
//
// So the choice is deferred. A field keeps its rivals through the container
// stage, and an ancestor counts the field as covered if any rival lies beneath
// it. The container that explains the most fields therefore wins on the reading
// that suits it, and the second pass re-scores every field inside that
// container anyway, which is where the field's location is actually settled.
func rivalGroups(groups []group) []group {
	best := bestGroup(groups)
	if best == nil {
		return nil
	}
	// bestGroup ranks in place, so the strongest group is first and the rivals
	// that survive follow it in descending order.
	floor := best.confidence() * rivalShare
	out := make([]group, 0, len(groups))
	for _, g := range groups {
		if g.confidence() >= floor {
			out = append(out, g)
		}
	}
	return out
}

// expansionGain is how much more a record must repeat once a field is set
// aside before that field is judged to have been matching outside it.
//
// It is a multiple rather than a difference because the question is one of
// kind, not degree: a container occurring once where another occurs thirty-seven
// times is not a slightly worse container, it is a different thing: the page
// that holds the records rather than a record.
const expansionGain = 2

// dropExpanders sets aside fields that match only outside the repeating unit.
//
// Rivals settle the case where a field had a reading inside the record and lost
// it locally. They cannot settle the case where the record has no such reading
// at all, and a feed shows that plainly. The BBC publishes lastBuildDate on the
// channel and nothing resembling it on an <item>, so a `modified` property
// matches once, correctly, above every record. The only ancestor holding it
// together with the item's own fields is the channel: one record where there
// were thirty-seven.
//
// Confidence cannot catch this either. The channel's date is a real date under
// a real label and scores as well as anything inside an item, so confidentHits,
// which drops strays that are uncertain, leaves it alone.
//
// What marks it is what happens when it is set aside: the record starts
// repeating. So each field is tried as absent in turn, and one whose absence
// lets the container repeat far more is treated as absent for the purpose of
// locating it. The field keeps its own locator if it can still be found inside
// the container; it just does not get to say where the container is.
func dropExpanders(hits [][]*graph.Node) [][]*graph.Node {
	out := hits
	// Each pass removes at most one field, and a field is never restored, so
	// the loop cannot run longer than there are fields.
	for range hits {
		base := len(containerGroup(out))
		bestI, bestN := -1, base
		for i, nodes := range out {
			if len(nodes) == 0 {
				continue
			}
			trial := make([][]*graph.Node, len(out))
			copy(trial, out)
			trial[i] = nil

			// A container set of zero means the remaining fields no longer make
			// a record at all, which is a reason to keep the field, not drop it.
			if n := len(containerGroup(trial)); n > bestN && n >= base*expansionGain {
				bestI, bestN = i, n
			}
		}
		if bestI < 0 {
			return out
		}
		next := make([][]*graph.Node, len(out))
		copy(next, out)
		next[bestI] = nil
		out = next
	}
	return out
}

// conflictPenalty is how much of a location's confidence disagreement inside a
// single record can cost it.
//
// It is bounded rather than proportional because some fields really do hold
// several values: a feed item carries half a dozen categories and they are all
// different, so a location whose matches always disagree is not automatically
// wrong. What it is, reliably, is a worse way to address one value than a
// location that does not disagree, and the bound leaves it able to win when
// nothing better exists.
const conflictPenalty = 0.35

// agreement discounts a location by how often its matches disagreed inside one
// record.
//
// A field holds one value per record. Matching twice with the same text is
// duplicated markup and harmless: one real site publishes <meta name="author">
// twice per page with the identical byline. Matching twice with different text
// is not one location at all. The same site publishes
// <meta property="article:author"> twice, once with the byline and once with a
// Facebook URL, and it beat the unambiguous name="author" on score alone, so
// half that site's records came back with the URL as the author.
func agreement(spread, conflicts int) float64 {
	if spread <= 0 || conflicts <= 0 {
		return 1
	}
	share := float64(conflicts) / float64(spread)
	if share > 1 {
		share = 1
	}
	return 1 - conflictPenalty*share
}

// reachWeight is how much of a location's standing rests on how much of the
// corpus it covers, rather than on how well it reads where it does.
const reachWeight = 0.5

// reachFactor discounts a location by how far short of the widest-reaching
// rival it falls.
func reachFactor(spread, widest int) float64 {
	if widest <= 0 || spread >= widest {
		return 1
	}
	return math.Pow(float64(spread)/float64(widest), reachWeight)
}

// supportFactor rises from 0.85 at a single observation towards 1.0 as
// support grows, saturating around five observations.
func supportFactor(support int) float64 {
	if support <= 0 {
		return 0
	}
	const saturation = 5.0
	return 0.85 + 0.15*math.Min(1, math.Log1p(float64(support))/math.Log1p(saturation))
}

// buildResult turns a winning group into an schema.Item.
func buildResult(p schema.Prop, g *group, bases map[*graph.Node]bool) *result {
	nodes := make([]*graph.Node, 0, len(g.matches))
	for _, m := range g.matches {
		nodes = append(nodes, m.node)
	}
	return &result{
		item: schema.Item{
			Name:        p.Name,
			Probability: g.confidence(),
			Locator:     locatorFor(nodes, bases, p.Type, g.disc),
			Support:     len(nodes),
			Values:      sampleValues(nodes),
		},
		nodes: nodes,
	}
}

// inferRecord locates a prop that describes a repeating record. It first finds
// where the fields co-occur, then re-scores them inside that container with
// the sequence model, and finally addresses each field relative to it.
func (e *Engine) inferRecord(ctx context.Context, p schema.Prop, cands []*graph.Node, bases map[*graph.Node]bool) (*result, bool) {
	flat, nested := splitProps(p.Props)

	// First pass: find each simple field independently, purely to discover
	// where they live.
	hits := make([][]*graph.Node, len(flat))
	confidence := make([]float64, len(flat))
	found := 0
	// present counts the fields the corpus has any candidate for at all. It is
	// the denominator of coverage below, and it is deliberately not the number
	// of fields declared: a schema may describe a field this corpus never
	// publishes, and absent data is not evidence against the record.
	present := len(nested)
	for i, cp := range flat {
		matches := e.scoreAll(ctx, cp, cands)
		if len(matches) == 0 {
			continue
		}
		present++
		rivals := rivalGroups(groupMatches(matches, bases))
		if len(rivals) == 0 {
			continue
		}
		for _, g := range rivals {
			for _, m := range g.matches {
				hits[i] = append(hits[i], m.node)
			}
		}
		// The field's confidence is its best reading, not the average of the
		// readings it is still considering.
		confidence[i] = rivals[0].confidence()
		found++
	}

	// A field the document does not have still matches something, faintly, and
	// that faint match votes on where the record is. It votes badly: the only
	// ancestor enclosing both the real fields and a stray one is far above the
	// record. On a news feed, a six-field schema whose author and section are
	// absent chose the document root, support 1, where the four fields actually
	// present chose the repeating item, support 36. One unfindable field cost
	// thirty-five records out of thirty-six.
	//
	// So a field that is far less certain than its siblings is treated as
	// absent for the purpose of locating the record. It keeps its own locator
	// if one can be found inside the container; it simply does not get to say
	// where the container is.
	hits = confidentHits(hits, confidence)
	if found == 0 && len(nested) == 0 {
		return nil, false
	}

	// Rivals only help a field that has a reading inside the record. A field
	// with no reading there at all is still free to drag the container out, so
	// it is tested by its absence instead.
	hits = dropExpanders(hits)

	containers := containerGroup(hits)
	if len(containers) == 0 {
		// The fields never co-occur under a shared ancestor, so there is no
		// record to speak of. Fall back to reporting them as independent
		// locations under the documents that hold them.
		containers = documentsOf(hits)
	}
	if len(containers) == 0 {
		return nil, false
	}
	containerSet := make(map[*graph.Node]bool, len(containers))
	for _, c := range containers {
		containerSet[c] = true
	}

	items := make([]schema.Item, 0, len(p.Props))
	var covered int

	// Second pass: score the simple fields within each container, letting the
	// sequence model use field order to settle ambiguous positions.
	if len(flat) > 0 {
		perField := e.scoreContainers(ctx, flat, containers)
		for i, cp := range flat {
			if len(perField[i]) == 0 {
				continue
			}
			if g := bestGroup(groupMatches(perField[i], containerSet)); g != nil {
				res := buildResult(cp, g, containerSet)
				if res.item.Probability >= e.MinProbability {
					items = append(items, res.item)
					covered++
				}
			}
		}
	}

	// Nested records recurse, restricted to the containers just found and
	// addressed relative to them.
	if len(nested) > 0 {
		sub := candidatesUnder(containers)
		for _, cp := range nested {
			if res, ok := e.inferProp(ctx, cp, sub, containerSet); ok && res.item.Probability >= e.MinProbability {
				items = append(items, res.item)
				covered++
			}
		}
	}

	if covered == 0 {
		return nil, false
	}

	// The record is only as good as the share of its fields that were found
	// and how confidently each was located.
	var sum float64
	for _, it := range items {
		sum += it.Probability
	}
	// Coverage is the share of the findable fields that were located, not the
	// share of the declared ones. A schema wider than the site it is applied
	// to should locate fewer fields, not report the ones it did find with less
	// confidence: otherwise adding a property the site never publishes would
	// quietly weaken every record, and a thorough schema would be punished for
	// its thoroughness.
	findable := present
	if findable < covered {
		findable = covered
	}
	if findable == 0 {
		return nil, false
	}

	// The penalty is softened because it is multiplied into an already
	// fractional confidence. Applied linearly, a record that recovered a
	// quarter of its fields lost three quarters of its probability and fell
	// under any useful threshold, which made a nine-field schema locate
	// nothing on a site where a three-field one located everything. Taking the
	// root keeps the ordering, so more coverage still means more confidence,
	// without turning a partial record into no record at all.
	coverage := math.Sqrt(float64(covered) / float64(findable))
	prob := clamp(sum / float64(covered) * coverage * supportFactor(len(containers)))

	item := schema.Item{
		Name:        p.Name,
		Probability: prob,
		Locator:     locatorFor(containers, bases, schema.TypeString, pattern.Discriminator{}),
		Support:     len(containers),
		Items:       items,
	}
	// A container spans a whole record, so there is no single value to
	// extract from it.
	item.Regex = pattern.AnyRegex
	return &result{item: item, nodes: containers}, true
}

// splitProps separates simple fields from nested records, preserving order.
func splitProps(props []schema.Prop) (flat, nested []schema.Prop) {
	for _, p := range props {
		if p.IsRecord() {
			nested = append(nested, p)
		} else {
			flat = append(flat, p)
		}
	}
	return flat, nested
}

// scoreContainers scores every field against the leaves of each container and
// runs the sequence model over each container independently. It returns, per
// field, the nodes that the refined scores assign to it.
func (e *Engine) scoreContainers(ctx context.Context, fields []schema.Prop, containers []*graph.Node) [][]scored {
	out := make([][]scored, len(fields))
	perRegion := make([][]*graph.Node, 0, len(containers))
	regions := make([][][]float64, 0, len(containers))
	for _, c := range containers {
		// The same filter as the first pass. Without it the second pass sees
		// nodes the first one ruled out, so a namespace declaration or an inline
		// script can win a field it was never eligible for: the Guardian's
		// author came back as the xmlns on <dc:creator> rather than the byline
		// beside it.
		leaves := make([]*graph.Node, 0, 16)
		for _, n := range graph.ValueNodes(c) {
			if isCandidateValue(n) {
				leaves = append(leaves, n)
			}
		}
		if len(leaves) == 0 {
			continue
		}

		raw := make([][]float64, len(leaves))
		for i, leaf := range leaves {
			row := make([]float64, len(fields))
			for j, f := range fields {
				row[j] = e.Matcher.Score(ctx, f, leaf)
			}
			raw[i] = row
		}
		perRegion = append(perRegion, leaves)
		regions = append(regions, raw)
	}
	if len(regions) == 0 {
		return out
	}

	// Every region is handed over at once: a sequence model that trains needs
	// to see the whole set before it decodes any of it.
	if e.Sequence != nil {
		if refined := e.Sequence.Refine(regions, len(fields)); sameShape(refined, regions) {
			regions = refined
		}
	}

	// Each leaf is claimed by at most one field: its best scoring one.
	for r, leaves := range perRegion {
		for i, leaf := range leaves {
			bestJ, bestV := -1, scoreFloor
			for j := range fields {
				if regions[r][i][j] > bestV {
					bestJ, bestV = j, regions[r][i][j]
				}
			}
			if bestJ >= 0 {
				out[bestJ] = append(out[bestJ], scored{node: leaf, score: bestV})
			}
		}
	}
	return out
}

// sameShape reports whether a refined score set still lines up with the one it
// came from. A Sequence is third-party code, so its output is checked rather
// than trusted.
func sameShape(got, want [][][]float64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if len(got[i]) != len(want[i]) {
			return false
		}
		for j := range got[i] {
			if len(got[i][j]) != len(want[i][j]) {
				return false
			}
		}
	}
	return true
}

// containerGroup finds the repeating node that holds a record. It picks the
// ancestors covering the most distinct fields, keeps only the deepest of
// them — a shallower ancestor covers the same fields but is less specific —
// and returns the largest set of those that share a generalized path.
func containerGroup(hits [][]*graph.Node) []*graph.Node {
	cov := make(map[*graph.Node]map[int]bool)
	var order []*graph.Node

	for field, nodes := range hits {
		for _, n := range nodes {
			for a := n.Parent; a != nil; a = a.Parent {
				if a.Kind == graph.KindURI || a.Kind == graph.KindDomain || a.Kind == graph.KindRoot {
					break
				}
				m, ok := cov[a]
				if !ok {
					m = make(map[int]bool)
					cov[a] = m
					order = append(order, a)
				}
				m[field] = true
				// Keep climbing out of an embedded document: a JSON-LD block
				// holds some of the record's fields and the surrounding page
				// holds the rest, so the container spans both.
				if a.IsTopDocument() {
					break
				}
			}
		}
	}
	if len(order) == 0 {
		return nil
	}

	maxCov := 0
	for _, a := range order {
		if c := len(cov[a]); c > maxCov {
			maxCov = c
		}
	}
	if maxCov < 2 {
		// A single field does not make a record; let the caller fall back.
		return nil
	}

	inSet := make(map[*graph.Node]bool)
	var best []*graph.Node
	for _, a := range order {
		if len(cov[a]) == maxCov {
			inSet[a] = true
			best = append(best, a)
		}
	}

	// Discard any container that encloses another container: the inner one is
	// the actual repeating unit.
	encloses := make(map[*graph.Node]bool)
	for _, n := range best {
		for a := n.Parent; a != nil; a = a.Parent {
			if inSet[a] {
				encloses[a] = true
			}
			if a.IsTopDocument() {
				break
			}
		}
	}
	deepest := best[:0:0]
	for _, n := range best {
		if !encloses[n] {
			deepest = append(deepest, n)
		}
	}
	if len(deepest) == 0 {
		return nil
	}

	// Containers that generalize to the same path are instances of one
	// pattern; the largest such set is the record.
	byKey := make(map[string][]*graph.Node)
	var keys []string
	for _, n := range deepest {
		key := n.Format().String() + "|" + pattern.Coarse(n.Path())
		if _, seen := byKey[key]; !seen {
			keys = append(keys, key)
		}
		byKey[key] = append(byKey[key], n)
	}
	sort.SliceStable(keys, func(i, j int) bool {
		if len(byKey[keys[i]]) != len(byKey[keys[j]]) {
			return len(byKey[keys[i]]) > len(byKey[keys[j]])
		}
		// Prefer the more specific pattern when counts tie.
		di, dj := byKey[keys[i]][0].Depth(), byKey[keys[j]][0].Depth()
		if di != dj {
			return di > dj
		}
		return keys[i] < keys[j]
	})

	winners := byKey[keys[0]]

	// Tightening only buys something when the record repeats.
	//
	// A container occurring exactly once per document is not separating one
	// record from its neighbours, because it has none. All it does is put the
	// rest of the page out of reach, since the second pass scores only the
	// leaves inside the container. On every news site in the corpus that
	// container was /html/head, chosen because metadata is the part of a page
	// that carries strong labels, and the consequence was that the article's
	// own markup was never a candidate for anything: the headline in the <h1>,
	// the byline, the <time> were all outside the record.
	//
	// A feed is the opposite case, and the reason the tightening exists at all:
	// forty-five <item> containers in one document, where widening to the
	// document would collapse forty-five records into one.
	//
	// So the question is not which ancestor is deepest, it is whether the
	// record repeats within a document. When it does not, the record is the
	// document.
	docs := make(map[*graph.Node]bool, len(winners))
	for _, n := range winners {
		d := n.Document()
		if d == nil {
			return winners
		}
		docs[d] = true
	}
	if len(docs) != len(winners) {
		return winners
	}

	// Widen to the outermost element, not to the document. A document has no
	// path, so addressing the record by it produces an empty locator that
	// matches nothing: the fields resolve correctly and no record is ever
	// extracted.
	seen := make(map[*graph.Node]bool, len(winners))
	wide := make([]*graph.Node, 0, len(winners))
	for _, n := range winners {
		top := outermost(n)
		if !seen[top] {
			seen[top] = true
			wide = append(wide, top)
		}
	}
	return wide
}

// outermost returns the highest element enclosing n within its document.
func outermost(n *graph.Node) *graph.Node {
	top := n
	for a := n.Parent; a != nil && a.Kind == graph.KindElement; a = a.Parent {
		top = a
	}
	return top
}

// documentsOf returns the distinct documents holding the given nodes, used as
// containers of last resort when no tighter one exists.
func documentsOf(hits [][]*graph.Node) []*graph.Node {
	seen := make(map[*graph.Node]bool)
	var out []*graph.Node
	for _, nodes := range hits {
		for _, n := range nodes {
			if d := n.Document(); d != nil && !seen[d] {
				seen[d] = true
				out = append(out, d)
			}
		}
	}
	return out
}

// locatorFor synthesizes the address of a set of equivalent nodes, in every
// dialect that applies to them.
func locatorFor(nodes []*graph.Node, bases map[*graph.Node]bool, t schema.Type, disc pattern.Discriminator) schema.Locator {
	if len(nodes) == 0 {
		return schema.Locator{}
	}

	loc := schema.Locator{Format: nodes[0].Format()}
	paths := make([]string, 0, len(nodes))
	selectors := make([]string, 0, len(nodes))
	xpaths := make([]string, 0, len(nodes))
	uris := make([]string, 0, len(nodes))
	values := make([]string, 0, len(nodes))

	for _, n := range nodes {
		base := baseOf(n, bases)
		paths = append(paths, graph.RelPath(n, base))
		switch {
		case n.Format().Markup():
			xpaths = append(xpaths, graph.RelXPath(n, base))
			selectors = append(selectors, graph.RelSelector(n, base))
		default:
			// A non-markup value may still sit inside a page, as JSON-LD
			// does. Record the element hosting it so the locator says which
			// block it came from.
			if host := graph.HostElement(n); host != nil {
				xpaths = append(xpaths, host.XPath())
				selectors = append(selectors, host.Selector())
			}
		}
		if u := n.URI(); u != nil {
			uris = append(uris, u.String())
		}
		values = append(values, n.Text())
	}

	loc.Path = pattern.Generalize(paths)
	loc.XPath = pattern.Predicated(pattern.Generalize(xpaths), disc)
	loc.Selector = pattern.PredicatedSelector(pattern.GeneralizeSelector(selectors), disc)
	if loc.Format.Markup() && loc.XPath != "" {
		// For markup the native dialect is XPath, so the two must agree —
		// including the predicate, which is what makes the locator precise.
		loc.Path = loc.XPath
	}
	loc.URI = pattern.SynthesizeURI(uris)
	loc.Regex = pattern.SynthesizeRegex(values)
	if loc.Regex == pattern.AnyRegex {
		// The observed values had no shared shape, but the declared type still
		// says something about what a valid one looks like.
		loc.Regex = pattern.ShapePrior(t)
	}
	return loc
}

// baseOf returns the container a node should be addressed relative to, or nil
// for absolute addressing.
func baseOf(n *graph.Node, bases map[*graph.Node]bool) *graph.Node {
	if len(bases) == 0 {
		return nil
	}
	for a := n; a != nil; a = a.Parent {
		if bases[a] {
			return a
		}
		if a.IsTopDocument() {
			break
		}
	}
	return nil
}

// pathFor returns the path used to group a node: relative to its container
// when one applies, absolute otherwise.
func pathFor(n *graph.Node, bases map[*graph.Node]bool) string {
	return graph.RelPath(n, baseOf(n, bases))
}

// sampleValues returns a few distinct observed values, for eyeballing whether
// a match is real.
func sampleValues(nodes []*graph.Node) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, maxSampleValues)
	for _, n := range nodes {
		v := n.Text()
		if v == "" || seen[v] {
			continue
		}
		if len(v) > 120 {
			v = v[:120] + "…"
		}
		seen[v] = true
		if out = append(out, v); len(out) == maxSampleValues {
			break
		}
	}
	return out
}
