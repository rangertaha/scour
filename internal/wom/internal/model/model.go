// SPDX-License-Identifier: MIT

// Package model holds the reusable product of inference: a schema, the
// locations found for it, and optionally a trained chain. A model outlives the
// graph it was induced from, which is what splits expensive induction from
// cheap per-page extraction.
package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/rangertaha/scour/internal/wom/internal/graph"
	"github.com/rangertaha/scour/internal/wom/internal/pattern"
	"github.com/rangertaha/scour/internal/wom/internal/schema"
	"github.com/rangertaha/scour/internal/wom/internal/seq"
)

// Graph is the part of a wom graph a model needs: the documents to apply its
// locators to. Keeping it an interface is what lets a Model live below the
// package that owns the graph type.
type Graph interface {
	Documents() []*graph.Node
}

// ModelVersion is the current serialization format version.
const ModelVersion = modelVersion

// modelVersion is the schema version of a serialized Model. Loading refuses
// anything newer, so an old binary fails loudly rather than misreading a file.
const modelVersion = 1

// Errors returned by Model operations.
var (
	// ErrNoRecord is returned by Train when no item describes a record, which
	// leaves the chain with no field sequence to learn from.
	ErrNoRecord = errors.New("wom: no record item to train on")
	// ErrNoTrainingData is returned by Train when the model's locators match
	// nothing in the given graph.
	ErrNoTrainingData = errors.New("wom: locators matched nothing in the graph")
	// ErrModelVersion is returned when reading a model written by a newer
	// version of the package.
	ErrModelVersion = errors.New("wom: unsupported model version")
)

// Model is the reusable product of inference: the schema that was asked for,
// the locations found for it, and optionally a trained chain.
//
// It is deliberately separate from the graph. A WOM is per-crawl and
// disposable; a Model outlives it, is written to disk, and is applied to
// graphs it was never induced from. That split is what turns wom from a
// analysis tool into something a crawler can run — induction is expensive and
// happens once, extraction is cheap and happens per page.
//
// A Model is safe to share between goroutines as long as nothing mutates it.
type Model struct {
	// Version is the serialization format version.
	Version int `json:"version"`

	// Schema is the props the model was induced for.
	Schema schema.Schema `json:"schema"`

	// Items holds the located items, mirroring the shape of Schema. These are
	// site-specific and never portable: an XPath induced on one site means
	// nothing on another.
	Items []schema.Item `json:"items"`

	// Chain is the trained field-order model, if any. Unlike Items it does
	// transfer between sites, because it describes how records are written
	// rather than how one site marks them up.
	Chain *seq.ChainPrior `json:"chain,omitempty"`
}

// WriteTo serializes the model as indented JSON. It implements io.WriterTo.
func (m *Model) WriteTo(w io.Writer) (int64, error) {
	if m.Version == 0 {
		m.Version = modelVersion
	}
	buf, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("wom: encode model: %w", err)
	}
	buf = append(buf, '\n')
	n, err := w.Write(buf)
	return int64(n), err
}

// ReadModel decodes a model written by WriteTo.
func ReadModel(r io.Reader) (*Model, error) {
	var m Model
	if err := json.NewDecoder(r).Decode(&m); err != nil {
		return nil, fmt.Errorf("wom: decode model: %w", err)
	}
	if m.Version > modelVersion {
		return nil, fmt.Errorf("%w: file is version %d, this build understands %d",
			ErrModelVersion, m.Version, modelVersion)
	}
	return &m, nil
}

// Save writes the model to a file, replacing anything already there.
func (m *Model) Save(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("wom: save model: %w", err)
	}
	if _, err := m.WriteTo(f); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("wom: save model: %w", err)
	}
	return nil
}

// LoadModel reads a model from a file.
func LoadModel(path string) (*Model, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("wom: load model: %w", err)
	}
	defer f.Close()
	return ReadModel(f)
}

// Find returns the located items for the named props. A name may address a
// nested prop directly ("make") or by its full path ("vehicles.make"), and
// matching is case-insensitive.
//
// Naming a record returns it with all of its fields. Naming only some fields
// returns the record pruned to those fields, so the nesting that gives a
// locator its meaning is never lost. Calling Find with no names returns
// everything.
func (m *Model) Find(names ...string) []schema.Item {
	if len(names) == 0 {
		return m.Items
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
			want[n] = true
		}
	}
	if len(want) == 0 {
		return nil
	}
	return filterItems(m.Items, want, "")
}

func filterItems(items []schema.Item, want map[string]bool, prefix string) []schema.Item {
	var out []schema.Item
	for _, it := range items {
		full := it.Name
		if prefix != "" {
			full = prefix + "." + it.Name
		}
		if want[strings.ToLower(it.Name)] || want[strings.ToLower(full)] {
			// Naming a record asks for the record, fields and all.
			out = append(out, it)
			continue
		}
		if kids := filterItems(it.Items, want, full); len(kids) > 0 {
			pruned := it
			pruned.Items = kids
			out = append(out, pruned)
		}
	}
	return out
}

// Record is one instance of extracted data. A leaf prop yields a Record with
// Value set; a record prop yields one Record per container instance, with
// Items holding its fields.
type Record struct {
	Name  string   `json:"name"`
	Value string   `json:"value,omitempty"`
	Path  string   `json:"path,omitempty"`
	URI   string   `json:"uri,omitempty"`
	Items []Record `json:"items,omitempty"`
}

// Extract applies the model's locators to a graph and returns the data found.
// This is the cheap half of the split: no scoring, no inference, just the
// stored paths and regexes evaluated against whatever documents are present.
//
// Pass names to extract only some props, using the same matching rules as
// Find.
func (m *Model) Extract(g Graph, names ...string) []Record {
	if g == nil {
		return nil
	}
	items := m.Find(names...)
	if len(items) == 0 {
		return nil
	}
	docs := g.Documents()

	var out []Record
	for _, it := range items {
		out = append(out, extractItem(it, docs, nil)...)
	}
	return out
}

// extractItem resolves one item against a set of roots, returning a Record per
// matched node.
func extractItem(it schema.Item, roots []*graph.Node, base *graph.Node) []Record {
	leaf := len(it.Items) == 0
	nodes := matchNodes(roots, base, it.Path, it.Format, it.URI, it.XPath)
	if leaf {
		// A JSON field and the scalar inside it share one path, so a leaf
		// locator matches both. Induction only ever scored value-holding
		// nodes, and extraction has to agree or every field comes back twice.
		kept := nodes[:0]
		for _, n := range nodes {
			if n.Kind.HoldsValue() {
				kept = append(kept, n)
			}
		}
		nodes = kept
	}
	if len(nodes) == 0 {
		return nil
	}

	var value *regexp.Regexp
	if len(it.Items) == 0 && it.Regex != "" {
		value, _ = regexp.Compile(it.Regex)
	}

	// Pages repeat themselves: a byline can appear in four meta tags at once,
	// two of which match the locator. Distinct values are kept — a story with
	// two authors has two — but the same value twice is noise, not data.
	seen := make(map[string]bool, len(nodes))

	out := make([]Record, 0, len(nodes))
	for _, n := range nodes {
		rec := Record{Name: it.Name, Path: n.Path()}
		if u := n.URI(); u != nil {
			rec.URI = u.String()
		}
		if leaf {
			// A stored regex that no longer matches means the page changed
			// under the model. Returning the raw text would put a label, a
			// whole paragraph, or an unrelated string into a field the schema
			// says is a number — wrong data with no error anywhere. The node
			// is dropped instead, so the field is visibly absent from the
			// record rather than quietly wrong.
			matched, ok := applyRegex(value, n.Text())
			if !ok {
				continue
			}
			rec.Value = matched
			if key := rec.URI + "\x00" + rec.Value; seen[key] {
				continue
			} else {
				seen[key] = true
			}
		} else {
			for _, child := range it.Items {
				rec.Items = append(rec.Items, extractItem(child, []*graph.Node{n}, n)...)
			}
		}
		out = append(out, rec)
	}
	return out
}

// applyRegex pulls the captured value out of a node's text. It reports
// whether the pattern applied at all: a locator whose regex no longer matches
// has not found its value, and saying so is the difference between a missing
// field and a wrong one.
func applyRegex(re *regexp.Regexp, text string) (string, bool) {
	if re == nil {
		return text, true
	}
	m := re.FindStringSubmatch(text)
	switch {
	case m == nil:
		return "", false
	case len(m) > 1:
		return m[1], true
	default:
		// A pattern with no capture group still identifies the value.
		return m[0], true
	}
}

// matchNodes returns the nodes under roots whose path conforms to pattern.
// When base is non-nil the pattern is treated as relative to it, matching how
// the locator was synthesized.
func matchNodes(roots []*graph.Node, base *graph.Node, pat string, format graph.Format, uriPattern, hostXPath string) []*graph.Node {
	if pat == "" {
		return nil
	}
	var uriRe *regexp.Regexp
	if uriPattern != "" && uriPattern != pattern.AnyRegex {
		uriRe, _ = regexp.Compile(uriPattern)
	}
	pat, preds := pattern.SplitPredicates(pat)
	hostXPath, hostPreds := pattern.SplitPredicates(hostXPath)

	var out []*graph.Node
	for _, root := range roots {
		if uriRe != nil {
			if u := root.URI(); u == nil || !uriRe.MatchString(u.String()) {
				continue
			}
		}
		for _, scope := range scopesFor(root, format, hostXPath, hostPreds) {
			scope.Walk(func(n *graph.Node) bool {
				if n == base || n.Format() != format {
					return true
				}
				if pattern.Conforms(graph.RelPath(n, base), pat) && pattern.Satisfies(n, preds) {
					out = append(out, n)
				}
				return true
			})
		}
	}
	return out
}

// scopesFor narrows the search to the elements a non-markup locator names.
// A JSON-LD block lives inside an HTML page, so its Path is a JSONPath while
// its XPath points at the <script> holding it; without that narrowing, a page
// with several JSON-LD blocks would match the same path in every one.
func scopesFor(root *graph.Node, format graph.Format, hostXPath string, hostPreds []pattern.Discriminator) []*graph.Node {
	if hostXPath == "" || format.Markup() {
		return []*graph.Node{root}
	}
	var out []*graph.Node
	root.Walk(func(n *graph.Node) bool {
		if n.Kind == graph.KindElement && pattern.Conforms(n.XPath(), hostXPath) && pattern.Satisfies(n, hostPreds) {
			out = append(out, n)
		}
		return true
	})
	// No fallback to the whole document on purpose. If the host element has
	// moved, searching everywhere would happily match the same path inside a
	// different JSON-LD block and return another record's data. Finding
	// nothing is the correct answer, and a visible one.
	return out
}

// Train fits the model's chain to records located in a graph, using items as
// ground truth. Pass corrected items to teach it; pass none to self-train from
// the model's own locators.
//
// Training is supervised counting rather than Baum-Welch: when labels exist,
// counting cannot land in a local optimum and cannot permute the states away
// from the props they stand for. The built-in prior is mixed in as
// pseudo-counts, so a correction covering three records adjusts the prior
// instead of replacing it — which is what makes training on very little data
// safe rather than merely possible.
//
// Only the chain is learned. Locators come from induction and corrections, not
// from the chain, and the chain is the only part of a model that transfers to
// another site.
func (m *Model) Train(g Graph, items ...schema.Item) error {
	if g == nil {
		return errors.New("wom: Train needs a graph")
	}
	if len(items) > 0 {
		// Corrections are authoritative: adopt them.
		m.Items = items
	}

	rec := largestRecord(m.Items)
	if rec == nil {
		return ErrNoRecord
	}
	fields := rec.Items

	containers := matchNodes(g.Documents(), nil, rec.Path, rec.Format, rec.URI, rec.XPath)
	sequences := make([][]int, 0, len(containers))
	for _, c := range containers {
		leaves := graph.ValueNodes(c)
		if len(leaves) == 0 {
			continue
		}
		states := make([]int, len(leaves))
		for i, leaf := range leaves {
			states[i] = seq.BackgroundState
			rel := graph.RelPath(leaf, c)
			for j, f := range fields {
				if f.Path != "" && pattern.Conforms(rel, f.Path) {
					states[i] = j + 1
					break
				}
			}
		}
		sequences = append(sequences, states)
	}

	if len(sequences) == 0 {
		return ErrNoTrainingData
	}
	m.Chain = seq.TrainLabeled(len(fields), m.Chain, sequences)
	if m.Version == 0 {
		m.Version = modelVersion
	}
	return nil
}

// largestRecord returns the item with the most fields, which is the one that
// constrains the chain most.
func largestRecord(items []schema.Item) *schema.Item {
	var best *schema.Item
	for i := range items {
		if len(items[i].Items) == 0 {
			continue
		}
		if best == nil || len(items[i].Items) > len(best.Items) {
			best = &items[i]
		}
		if inner := largestRecord(items[i].Items); inner != nil && len(inner.Items) > len(best.Items) {
			best = inner
		}
	}
	return best
}
