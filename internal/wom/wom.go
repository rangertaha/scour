// SPDX-License-Identifier: MIT

// Package wom implements the Web Object Model: a single graph that unifies
// HTTP exchanges and the documents they carry into nodes, paths, and
// attributes.
//
// A graph is built by adding responses to it. HTML, XML, SVG, RSS/Atom, JSON,
// JavaScript, CSS, and PDF bodies all become subtrees of the same graph, so a
// whole crawl of a site can be traversed and queried as one structure rather
// than a pile of independent documents:
//
//	w := wom.New()
//	w.Add(resp) // as many responses as you like, in any format
//
// The graph is shaped root → domain → uri → document → content, and every node
// can report its own address via Node.XPath, Node.Selector, and Node.Path.
//
// The main operation is schema inference: given a description of the data you
// want, wom reports where that data lives.
//
//	items, err := w.Schema(wom.Prop{
//		Name:    "vehicles",
//		Aliases: []string{"car"},
//		Props: []wom.Prop{
//			{Name: "make", Examples: []string{"Toyota"}},
//			{Name: "model"},
//			{Name: "year", Type: wom.TypeNumber},
//			{Name: "fuel"},
//		},
//	})
//
// Each returned Item carries a probability and a Locator holding the URI
// pattern, XPath, CSS selector, native path, and extraction regex for the
// value it found. Use WOM.Model instead of WOM.Schema to get that result as a
// Model, which can be saved, reloaded, and applied to pages it was never
// induced from.
//
// # Structure
//
// This package is a facade. The implementation lives in internal packages,
// each owning one layer: graph (nodes and addressing), parse (format
// parsers), schema (the vocabulary of props and items), match (semantic
// scoring), seq (the sequence model), pattern (pattern synthesis and
// matching), infer (the inference engine), and model (the saved artifact).
package wom

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/rangertaha/scour/internal/wom/internal"
	"github.com/rangertaha/scour/internal/wom/internal/graph"
	"github.com/rangertaha/scour/internal/wom/internal/infer"
	"github.com/rangertaha/scour/internal/wom/internal/match"
	"github.com/rangertaha/scour/internal/wom/internal/model"
	"github.com/rangertaha/scour/internal/wom/internal/parse"
	"github.com/rangertaha/scour/internal/wom/internal/schema"
	"github.com/rangertaha/scour/internal/wom/internal/seq"
)

// The public vocabulary. These are aliases rather than wrappers, so a value
// produced by any layer is usable directly through this package.
type (
	// Node is a single vertex of the graph.
	Node = graph.Node
	// Kind classifies a node.
	Kind = graph.Kind
	// Format identifies a document dialect.
	Format = graph.Format

	// Prop describes one field to locate.
	Prop = schema.Prop
	// Schema is an ordered set of props.
	Schema = schema.Schema
	// Type is the value type a Prop is expected to hold.
	Type = schema.Type
	// Item is an inferred location for one Prop.
	Item = schema.Item
	// Locator is the address of a value, in every applicable dialect.
	Locator = schema.Locator

	// Matcher scores how strongly a node satisfies a Prop.
	Matcher = match.Matcher
	// MatcherFunc adapts a plain function to Matcher.
	MatcherFunc = match.MatcherFunc
	// Heuristic is the built-in deterministic Matcher.
	Heuristic = match.Heuristic

	// Sequence refines per-node scores using field order.
	Sequence = seq.Sequence
	// HMM is the built-in trainable Sequence.
	HMM = seq.HMM
	// ChainPrior is a serializable transition model.
	ChainPrior = seq.ChainPrior

	// Model is the reusable product of inference.
	Model = model.Model
	// Record is one instance of extracted data.
	Record = model.Record
)

// Node kinds.
const (
	KindRoot      = graph.KindRoot
	KindDomain    = graph.KindDomain
	KindURI       = graph.KindURI
	KindDocument  = graph.KindDocument
	KindElement   = graph.KindElement
	KindAttribute = graph.KindAttribute
	KindText      = graph.KindText
	KindObject    = graph.KindObject
	KindArray     = graph.KindArray
	KindField     = graph.KindField
	KindValue     = graph.KindValue
	KindRule      = graph.KindRule
	KindAtRule    = graph.KindAtRule
	KindDecl      = graph.KindDecl
	KindScope     = graph.KindScope
	KindBinding   = graph.KindBinding
	KindLiteral   = graph.KindLiteral
	KindPage      = graph.KindPage
	KindLine      = graph.KindLine
)

// Document formats.
const (
	FormatUnknown = graph.FormatUnknown
	FormatHTML    = graph.FormatHTML
	FormatXML     = graph.FormatXML
	FormatSVG     = graph.FormatSVG
	FormatFeed    = graph.FormatFeed
	FormatJSON    = graph.FormatJSON
	FormatJS      = graph.FormatJS
	FormatCSS     = graph.FormatCSS
	FormatPDF     = graph.FormatPDF
)

// Property types.
const (
	TypeString = schema.TypeString
	TypeNumber = schema.TypeNumber
	TypeBool   = schema.TypeBool
	TypeDate   = schema.TypeDate
	TypeURL    = schema.TypeURL
	TypeEmail  = schema.TypeEmail
)

// Errors callers can test for.
var (
	// ErrNoURL is returned when a response carries no request URL, which
	// leaves wom with nothing to key the document on.
	ErrNoURL = errors.New("wom: response has no request URL")
	// ErrUnknownFormat is returned when a body's format could not be
	// determined from its Content-Type, URL, or contents.
	ErrUnknownFormat = errors.New("wom: unrecognized document format")
	// ErrEmptySchema is returned when Schema is called with no props.
	ErrEmptySchema = schema.ErrEmptySchema
	// ErrNoRecord is returned by Model.Train when no item describes a record.
	ErrNoRecord = model.ErrNoRecord
	// ErrNoTrainingData is returned by Model.Train when the model's locators
	// match nothing in the given graph.
	ErrNoTrainingData = model.ErrNoTrainingData
	// ErrModelVersion is returned when reading a model written by a newer
	// version of the package.
	ErrModelVersion = model.ErrModelVersion
)

// DetectFormat resolves the format of a body from its Content-Type header, the
// extension of its URL, and finally its leading bytes.
func DetectFormat(contentType, uri string, body []byte) Format {
	return graph.DetectFormat(contentType, uri, body)
}

// DefaultHeuristic returns the built-in Matcher weights, as a starting point
// for adjusting one of them without zeroing the rest.
func DefaultHeuristic() Heuristic { return match.DefaultHeuristic() }

// DefaultChainPrior returns the built-in transition prior for a record of n
// fields. It is the only model-shaped thing wom ships: field ordering
// transfers between sites, locators never do.
func DefaultChainPrior(n int) *ChainPrior { return seq.DefaultChainPrior(n) }

// ReadModel decodes a model written by Model.WriteTo.
func ReadModel(r io.Reader) (*Model, error) { return model.ReadModel(r) }

// LoadModel reads a model from a file.
func LoadModel(path string) (*Model, error) { return model.LoadModel(path) }

// Version reports the version of the wom module. For release builds it is
// injected at link time; otherwise it is derived from the Go build info.
func Version() string { return internal.Version() }

// WOM is a graph of domains, URIs, and the documents served at them. The zero
// value is not usable; build one with New. A WOM is safe for concurrent use.
type WOM struct {
	mu      sync.RWMutex
	root    *Node
	domains map[string]*Node
	uris    map[string]*Node
	opts    options
}

// New returns an empty graph configured by opts.
func New(opts ...Option) *WOM {
	o := defaultOptions()
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	return &WOM{
		root:    &Node{Kind: KindRoot},
		domains: make(map[string]*Node),
		uris:    make(map[string]*Node),
		opts:    o,
	}
}

// Add ingests an HTTP response, reading and closing its body. The response
// must carry a Request with a URL, which is how http.Client populates it.
//
// Re-adding a URL already in the graph replaces the document previously parsed
// for it.
func (w *WOM) Add(resp *http.Response) error {
	if resp == nil {
		return ErrNoURL
	}
	if resp.Body != nil {
		defer resp.Body.Close()
	}
	if resp.Request == nil || resp.Request.URL == nil {
		return ErrNoURL
	}

	var body []byte
	if resp.Body != nil {
		var err error
		body, err = io.ReadAll(io.LimitReader(resp.Body, w.opts.maxBody))
		if err != nil {
			return fmt.Errorf("wom: read body of %s: %w", resp.Request.URL, err)
		}
	}
	return w.AddBody(resp.Request.URL.String(), resp.Header.Get("Content-Type"), body)
}

// AddBody ingests a body directly, for callers that already have the bytes or
// are replaying a fixture. contentType may be empty, in which case the format
// is inferred from the URL and the body itself.
func (w *WOM) AddBody(rawURL, contentType string, body []byte) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("wom: parse url %q: %w", rawURL, err)
	}
	if u.Host == "" {
		return fmt.Errorf("wom: %w: url %q has no host", ErrNoURL, rawURL)
	}

	format := DetectFormat(contentType, rawURL, body)
	if format == FormatUnknown {
		return fmt.Errorf("wom: %w for %s (content-type %q)", ErrUnknownFormat, rawURL, contentType)
	}

	// Parse into a detached document first. A body that fails to parse then
	// leaves no trace in the graph at all — not a partial subtree, and not an
	// empty domain or URI node either.
	doc := graph.NewDocument(format)
	if err := parse.Into(doc, format, body); err != nil {
		return fmt.Errorf("wom: %s: %w", rawURL, err)
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	uriNode := w.uriNode(u)
	uriNode.Reset() // replace any document already parsed for this URL
	uriNode.Append(doc)
	return nil
}

// uriNode returns the node for a URL, creating it and its domain if needed.
// The caller must hold the write lock.
func (w *WOM) uriNode(u *url.URL) *Node {
	key := u.String()
	if n, ok := w.uris[key]; ok {
		return n
	}

	host := strings.ToLower(u.Host)
	domain, ok := w.domains[host]
	if !ok {
		domain = w.root.Append(graph.New(KindDomain, host, ""))
		w.domains[host] = domain
	}

	n := domain.Append(graph.New(KindURI, key, ""))
	n.SetURI(u)
	w.uris[key] = n
	return n
}

// Root returns the graph root. The returned tree is live; treat it as
// read-only while other goroutines may be adding responses.
func (w *WOM) Root() *Node {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.root
}

// Len reports how many documents are in the graph.
func (w *WOM) Len() int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return len(w.uris)
}

// Domains returns the domain nodes in the graph, in insertion order.
func (w *WOM) Domains() []*Node {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return append([]*Node(nil), w.root.Children...)
}

// Documents returns every document node in the graph, in insertion order. It
// is also what satisfies the interface Model.Extract and Model.Train expect.
func (w *WOM) Documents() []*Node {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.documents()
}

// documents collects document nodes. The caller must hold at least the read
// lock.
func (w *WOM) documents() []*Node {
	var out []*Node
	for _, domain := range w.root.Children {
		for _, uri := range domain.Children {
			for _, doc := range uri.Children {
				if doc.Kind == KindDocument {
					out = append(out, doc)
				}
			}
		}
	}
	return out
}

// Walk calls fn for every node in the graph in document order. Returning false
// from fn skips that node's children.
func (w *WOM) Walk(fn func(*Node) bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	w.root.Walk(fn)
}

// Find returns every node in the graph for which pred reports true.
func (w *WOM) Find(pred func(*Node) bool) []*Node {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.root.Find(pred)
}

// Schema locates the described props in the graph and reports where each one
// lives. See SchemaContext to supply a context, which matters when the Matcher
// performs I/O, and Model to get the result in a form that can be saved.
func (w *WOM) Schema(props ...Prop) ([]Item, error) {
	return w.SchemaContext(context.Background(), props...)
}

// SchemaContext is Schema with a caller-supplied context. The context is
// passed to every Matcher.Score call and is checked between props, so a
// cancelled context stops inference promptly.
func (w *WOM) SchemaContext(ctx context.Context, props ...Prop) ([]Item, error) {
	if len(props) == 0 {
		return nil, ErrEmptySchema
	}
	if err := schema.Validate(props, 0); err != nil {
		return nil, err
	}

	w.mu.RLock()
	docs := w.documents()
	engine := &infer.Engine{
		Matcher:        w.opts.matcher,
		Sequence:       w.opts.sequence,
		MinProbability: w.opts.minProb,
	}
	w.mu.RUnlock()

	return engine.Infer(ctx, props, docs)
}

// Model locates the described props and returns the result as a reusable
// Model: Schema with the output packaged for saving and reapplying.
func (w *WOM) Model(props ...Prop) (*Model, error) {
	items, err := w.Schema(props...)
	if err != nil {
		return nil, err
	}
	return &Model{
		Version: model.ModelVersion,
		Schema:  Schema(props),
		Items:   items,
	}, nil
}
