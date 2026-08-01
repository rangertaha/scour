// SPDX-License-Identifier: MIT

// Package match scores how strongly a node satisfies a schema property. It is
// the only place semantic judgement lives, so replacing the Matcher replaces
// the intelligence of the whole engine without touching graph construction or
// locator synthesis.
package match

import (
	"context"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/rangertaha/scour/internal/wom/internal/graph"
	"github.com/rangertaha/scour/internal/wom/internal/schema"
)

// Matcher scores how strongly a node satisfies a schema.Prop, on a scale from 0 (no
// evidence) to 1 (certain). It is the only place semantic judgement lives, so
// swapping the implementation swaps the intelligence of the whole engine
// without touching graph construction or locator synthesis.
//
// Implementations must be safe for concurrent use and should return quickly;
// Score is called once per candidate node per prop.
type Matcher interface {
	Score(ctx context.Context, p schema.Prop, n *graph.Node) float64
}

// MatcherFunc adapts a plain function to the Matcher interface.
type MatcherFunc func(ctx context.Context, p schema.Prop, n *graph.Node) float64

// Score implements Matcher.
func (f MatcherFunc) Score(ctx context.Context, p schema.Prop, n *graph.Node) float64 {
	return f(ctx, p, n)
}

// Heuristic is the built-in Matcher. It scores a node from four deterministic
// signals and needs no network access, no API key, and no training data:
//
//   - example match: does the node's text equal or contain one of the schema.Prop's
//     Examples? This is the strongest signal available and is weighted as such.
//   - label proximity: does a label attached to the node — an attribute name,
//     a JSON key, a table header, a preceding <label> or <dt>, or the class
//     and id of the owning element — match the schema.Prop's name or aliases?
//   - type validity: does the text actually parse as the declared Type?
//   - description overlap: do the schema.Prop's description words appear in the
//     node's label context?
//
// The zero value is ready to use and scores with the default weights. Set any
// weight and every weight is then taken literally, including zeros — which is
// how a signal is switched off. Start from DefaultHeuristic to adjust one
// weight without zeroing the rest.
type Heuristic struct {
	// ExampleWeight scores literal agreement with schema.Prop.Examples. Default 1.0.
	ExampleWeight float64
	// LabelWeight scores agreement between schema.Prop labels and the node's label
	// context. Default 0.8.
	LabelWeight float64
	// TypeWeight scores whether the value parses as schema.Prop.Type. Default 0.4.
	TypeWeight float64
	// DescriptionWeight scores description-word overlap. Default 0.2.
	DescriptionWeight float64
}

// Default weights, chosen so that a literal example hit alone clears any
// reasonable threshold while label agreement alone does not.
const (
	defaultExampleWeight     = 1.0
	defaultLabelWeight       = 0.8
	defaultTypeWeight        = 0.4
	defaultDescriptionWeight = 0.2
)

// DefaultHeuristic returns the built-in weights, as a starting point for
// adjusting one of them:
//
//	h := match.DefaultHeuristic()
//	h.DescriptionWeight = 0 // ignore description overlap entirely
func DefaultHeuristic() Heuristic {
	return Heuristic{
		ExampleWeight:     defaultExampleWeight,
		LabelWeight:       defaultLabelWeight,
		TypeWeight:        defaultTypeWeight,
		DescriptionWeight: defaultDescriptionWeight,
	}
}

// weights resolves the weights to score with. Only the wholly zero value means
// "unset": treating each zero field as unset would make it impossible to
// switch a signal off, since the obvious way to do that is to set its weight
// to zero.
func (h Heuristic) weights() (example, label, typ, desc float64) {
	if h == (Heuristic{}) {
		return defaultExampleWeight, defaultLabelWeight, defaultTypeWeight, defaultDescriptionWeight
	}
	return h.ExampleWeight, h.LabelWeight, h.TypeWeight, h.DescriptionWeight
}

// Score implements Matcher. The result is a weighted mean over the signals
// that actually apply to this prop, so a prop with no examples is not
// penalised for the example signal being silent.
func (h Heuristic) Score(_ context.Context, p schema.Prop, n *graph.Node) float64 {
	text := n.Text()
	if text == "" {
		return 0
	}
	// A declared pattern says what a valid value looks like, so text that fails
	// it is not this field however well everything else agrees. It is a veto
	// rather than a weighted signal, for the same reason a type mismatch nearly
	// is: a value of the wrong shape is not a weaker answer, it is a different
	// question.
	if !validates(p, text) {
		return 0
	}

	wExample, wLabel, wType, wDesc := h.weights()
	labels := labelContext(n)

	// The same veto on the naming side. A declared label pattern says which
	// names count, so a node named something else is not this field however
	// well its text reads.
	if !labelled(p, labels) {
		return 0
	}

	var sum, total float64

	if len(p.Examples) > 0 {
		sum += wExample * exampleScore(p.Examples, text)
		total += wExample
	}
	if strong := p.StrongLabels(); len(strong) > 0 && len(labels) > 0 {
		sum += wLabel * labelScore(strong, labels)
		total += wLabel
	}
	if t := p.Type.Normalize(); t != schema.TypeString {
		sum += wType * typeScore(t, text)
		total += wType
	}
	if p.Description != "" && len(labels) > 0 {
		sum += wDesc * labelScore(descriptionTokens(p), labels)
		total += wDesc
	}

	var score float64
	if total == 0 {
		// Nothing to go on but the prop name; fall back to comparing it with
		// the node's own name, which covers JSON keys and attributes.
		score = labelScore(p.StrongLabels(), labelContext(n)) * 0.5
	} else {
		score = sum / total
	}

	// A node whose text is just the field's own name is a label, not a value.
	// Pages write both `<dt>Make</dt><dd>Toyota</dd>` and
	// `<span class="make">Toyota</span>`, and in each case only the second
	// half is data — but the label scores well on exactly the signals that
	// find the value. Damping here is what keeps the returned locator pointed
	// at the value rather than at the word next to it.
	if isLabelText(p, text) || namingAttribute(n) {
		score *= labelTextPenalty
	}
	return clamp(score)
}

// wordHeading is what every heading tag means, named once so the six of them
// cannot drift apart.
const wordHeading = "heading"

// namingAttrs are attributes that exist to name something else rather than to
// carry data. <meta property="article:author" content="..."> is the clearest
// case: `property` scores well against a prop called "authors" precisely
// because it is that prop's label, and returning it would hand back the word
// "article:author" instead of the author.
var namingAttrs = map[string]bool{
	"property":   true,
	"itemprop":   true,
	"rel":        true,
	"http-equiv": true,
	"name":       true,
	"for":        true,
	"headers":    true,
	"class":      true,
	"id":         true,
}

// namingAttribute reports whether a node is an attribute whose value labels
// its element rather than being content of it.
func namingAttribute(n *graph.Node) bool {
	return n.Kind == graph.KindAttribute && namingAttrs[n.Name]
}

// labelTextPenalty is how much a node is discounted for holding the field's
// own name rather than a value.
const labelTextPenalty = 0.25

// isLabelText reports whether the node's text is one of the prop's own labels.
func isLabelText(p schema.Prop, text string) bool {
	norm := normalize(text)
	if norm == "" {
		return false
	}
	for _, l := range p.StrongLabels() {
		if normalize(l) == norm {
			return true
		}
	}
	return false
}

// exampleScore returns the best similarity between the node text and any
// example value.
func exampleScore(examples []string, text string) float64 {
	norm := normalize(text)
	if norm == "" {
		return 0
	}
	var best float64
	for _, ex := range examples {
		e := normalize(ex)
		if e == "" {
			continue
		}
		var s float64
		switch {
		case text == ex:
			s = 1.0
		case norm == e:
			s = 0.95
		case strings.Contains(norm, e):
			// The example is embedded in a longer run of text: still good
			// evidence, but the node is coarser than the value.
			s = 0.75 * lengthRatio(len(e), len(norm))
		case strings.Contains(e, norm):
			s = 0.6 * lengthRatio(len(norm), len(e))
		default:
			s = 0.9 * tokenOverlap(strings.Fields(e), strings.Fields(norm))
		}
		if s > best {
			best = s
		}
	}
	return clamp(best)
}

// lengthRatio damps partial containment so a one-word example inside a long
// paragraph does not score like a clean match.
func lengthRatio(short, long int) float64 {
	if long == 0 {
		return 0
	}
	r := float64(short) / float64(long)
	// Anything covering at least a third of the text is treated as full.
	if r > 0.33 {
		return 1
	}
	return r / 0.33
}

// minContainLen is the shortest string allowed to match by containment.
// Below it, substring matching degenerates into coincidence.
const minContainLen = 3

// labelScore returns the best agreement between the prop's labels and the
// node's label context. Context entries are weighted by how reliable their
// source is, which labelContext encodes by ordering.
func labelScore(labels []string, ctxLabels []weightedLabel) float64 {
	if len(labels) == 0 || len(ctxLabels) == 0 {
		return 0
	}
	var best float64
	for _, wl := range ctxLabels {
		cand := normalize(wl.text)
		if cand == "" {
			continue
		}
		for _, l := range labels {
			l = normalize(l)
			if l == "" {
				continue
			}
			var s float64
			switch {
			case cand == l:
				s = 1.0
			case len(cand) >= minContainLen && len(l) >= minContainLen &&
				(strings.Contains(cand, l) || strings.Contains(l, cand)):
				// Containment is damped by how much of the longer string the
				// shorter one accounts for. Without both the length floor and
				// the ratio, an SVG circle's @r attribute matches "author"
				// because "author" contains an "r" — which is exactly the
				// kind of confident nonsense a locator must never point at.
				short, long := len(cand), len(l)
				if short > long {
					short, long = long, short
				}
				s = 0.7 * lengthRatio(short, long)
			default:
				s = tokenOverlap(strings.Fields(l), strings.Fields(cand))
			}
			if s *= wl.weight; s > best {
				best = s
			}
		}
	}
	return clamp(best)
}

// descriptionTokens returns the description words of a prop, excluding the
// tokens already covered by its strong labels.
func descriptionTokens(p schema.Prop) []string {
	strong := make(map[string]bool)
	for _, s := range p.StrongLabels() {
		strong[s] = true
	}
	var out []string
	for _, l := range p.Labels() {
		if !strong[l] {
			out = append(out, l)
		}
	}
	return out
}

// labelled reports whether any name attached to the node satisfies the prop's
// declared label pattern. A prop with no pattern, or one that does not compile,
// accepts every name.
func labelled(p schema.Prop, labels []weightedLabel) bool {
	if p.Label == "" {
		return true
	}
	re := compiled(p.Label)
	if re == nil {
		return true
	}
	for _, l := range labels {
		if re.MatchString(strings.TrimSpace(l.text)) {
			return true
		}
	}
	return false
}

// patternCache compiles each declared pattern once. A schema is small and
// reused across every node in a graph, so compiling per call would dominate.
var patternCache sync.Map // string -> *regexp.Regexp

// validates reports whether text satisfies the prop's declared pattern. A prop
// with no pattern, or one that does not compile, validates everything: a schema
// mistake must not silently empty a field.
func validates(p schema.Prop, text string) bool {
	if p.Pattern == "" {
		return true
	}
	re := compiled(p.Pattern)
	if re == nil {
		return true
	}
	return re.MatchString(strings.TrimSpace(text))
}

// compiled returns the compiled form of a declared pattern, or nil when it does
// not compile. A schema mistake must not silently empty a field, so callers
// treat nil as "accepts everything".
func compiled(pattern string) *regexp.Regexp {
	if v, ok := patternCache.Load(pattern); ok {
		re, _ := v.(*regexp.Regexp)
		return re
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		patternCache.Store(pattern, (*regexp.Regexp)(nil))
		return nil
	}
	patternCache.Store(pattern, re)
	return re
}

// typeScore reports whether the text parses as the declared type. A clear
// mismatch returns 0, which combined with a non-zero weight is enough to push
// wrong-typed candidates below the threshold.
func typeScore(t schema.Type, text string) float64 {
	s := strings.TrimSpace(text)
	if s == "" {
		return 0
	}
	switch t {
	case schema.TypeNumber:
		if _, err := strconv.ParseFloat(cleanNumber(s), 64); err == nil {
			return 1
		}
		// Text that merely contains a number is weak but not worthless, e.g.
		// "2019 model year".
		if extractNumber(s) != "" {
			return 0.5
		}
		return 0
	case schema.TypeBool:
		switch strings.ToLower(s) {
		case "true", "false", "yes", "no", "y", "n", "1", "0", "on", "off":
			return 1
		}
		return 0
	case schema.TypeDate:
		if parseDate(s) {
			return 1
		}
		return 0
	case schema.TypeURL:
		u, err := url.Parse(s)
		if err != nil {
			return 0
		}
		if u.Scheme != "" && u.Host != "" {
			return 1
		}
		if strings.HasPrefix(s, "/") || strings.Contains(s, "/") {
			return 0.5 // plausibly a relative URL
		}
		return 0
	case schema.TypeEmail:
		at := strings.IndexByte(s, '@')
		if at > 0 && strings.Contains(s[at:], ".") && !strings.ContainsAny(s, " \t") {
			return 1
		}
		return 0
	case schema.TypeString:
		return 0.5
	}
	return 0.5
}

// dateLayouts covers the formats that actually show up in page markup and
// feeds, ordered roughly by how common they are.
var dateLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02",
	"2006/01/02",
	"02/01/2006",
	"01/02/2006",
	"02-01-2006",
	"January 2, 2006",
	"Jan 2, 2006",
	"2 January 2006",
	"2 Jan 2006",
	time.RFC1123,
	time.RFC1123Z,
	time.RFC822,
	time.RFC822Z,
	"2006",
}

func parseDate(s string) bool {
	s = strings.TrimSpace(s)
	for _, layout := range dateLayouts {
		if _, err := time.Parse(layout, s); err == nil {
			return true
		}
	}
	return false
}

// weightedLabel is one piece of label context together with how much the
// source is trusted.
type weightedLabel struct {
	text   string
	weight float64
}

// Label is one name attached to a node, with how far its source is trusted.
type Label struct {
	// Text is the name as the page wrote it: an attribute value, a JSON key,
	// a heading, a class.
	Text string
	// Weight is how reliable that source is, from 0 to 1. A declared
	// vocabulary term is worth more than the class of an ancestor.
	Weight float64
}

// Labels returns the names that plausibly describe the value a node holds,
// ordered and weighted by how reliable each source is.
//
// It exists so that a matcher outside this package can ask the same question
// of a node that the heuristic does, and get the same answer. Which strings
// name a value is not a matter of opinion: it is which attributes carry
// meaning, which tags name their content, which classes are layout vocabulary
// rather than description, and which neighbour is a label rather than a
// sibling value. Every one of those was settled by measurement, and a second
// implementation would relitigate all of it and then drift, silently, because
// nothing would fail when it did.
func Labels(n *graph.Node) []Label {
	ctx := labelContext(n)
	out := make([]Label, 0, len(ctx))
	for _, wl := range ctx {
		out = append(out, Label{Text: wl.text, Weight: wl.weight})
	}
	return out
}

// labelContext gathers the strings that plausibly name the value held by n.
// The sources are ordered by reliability: an explicit key or attribute name is
// far better evidence than the class of a distant ancestor.
func labelContext(n *graph.Node) []weightedLabel {
	var out []weightedLabel
	add := func(s string, w float64) {
		if s = strings.TrimSpace(s); s != "" && len(s) < 120 {
			out = append(out, weightedLabel{text: s, weight: w})
		}
	}

	// The node's own name is the most direct label there is: a JSON key, an
	// attribute name, or a CSS property.
	switch n.Kind {
	case graph.KindAttribute, graph.KindDecl, graph.KindBinding:
		add(n.Name, attrNameWeight(n))
	case graph.KindValue, graph.KindText, graph.KindLiteral, graph.KindLine:
		// handled via parents below
	}

	// Text frequently carries its own label — "Make: Toyota". In a PDF, or in
	// any run of prose, this is the only label there is: there is no key, no
	// attribute, and no element to hang one on.
	if n.Kind.HoldsValue() {
		if label, _, ok := splitLabelled(n.Text()); ok {
			add(label, 0.95)
		}
	}

	// A JSON scalar is named by the field that holds it.
	for c := n.Parent; c != nil && c.Kind != graph.KindDocument; c = c.Parent {
		if c.Kind == graph.KindField {
			add(c.Name, 1.0)
			break
		}
	}

	// Markup: the owning element's identifying attributes, then its label-ish
	// neighbours.
	el := n
	for el != nil && el.Kind != graph.KindElement && el.Kind != graph.KindDocument {
		el = el.Parent
	}
	if el != nil && el.Kind == graph.KindElement {
		// In XML the element name is the label. HTML tags are structural, so
		// <div> and <span> say nothing about the value inside them, but an
		// author writing XML chooses <pubDate> and <dc:creator> precisely to
		// name what they hold. Ignoring the tag name meant nothing could be
		// located in a feed at all: every field in an RSS item is named by its
		// element and by nothing else.
		switch el.Format() {
		case graph.FormatXML, graph.FormatFeed, graph.FormatSVG:
			add(el.Name, 1.0)
		default:
			// Most HTML tags are structural, but not all of them. A handful
			// name their content as plainly as any XML element does, and those
			// are the most portable labels on the web: measured over thirteen
			// news sites in four languages, <h1> appeared on all thirteen and
			// <time datetime> on ten, while itemprop="author" appeared on one.
			//
			// The tag is not the label, though. Adding the name would add the
			// token "h1", which matches neither "title" nor "heading". What a
			// tag carries is a meaning, so it contributes the word it means.
			if t, ok := semanticTags[el.Name]; ok {
				add(t.word, t.weight)
			}
		}

		// A declared vocabulary is added both as written and stripped of its
		// namespace, so og:title also reads as "title". These prefixes are the
		// one part of markup that survives translation: a Greek or Russian page
		// still writes og:description, because the vocabulary is English by
		// specification even when nothing else on the page is.
		addQualified := func(v string, w float64) {
			add(v, w)
			if i := strings.LastIndexAny(v, ":."); i >= 0 && i+1 < len(v) {
				add(v[i+1:], w)
			}
		}

		if v, ok := el.Attr("itemprop"); ok {
			addQualified(v, 1.0)
		}
		if v, ok := el.Attr("data-field"); ok {
			add(v, 1.0)
		}
		if v, ok := el.Attr("property"); ok {
			// <meta property="article:published_time" content="...">: the
			// sibling attribute is the only thing naming the value.
			addQualified(v, 1.0)
		}
		if v, ok := el.Attr("name"); ok {
			addQualified(v, 0.95)
		}
		if v, ok := el.Attr("rel"); ok {
			// rel is a declaring attribute: it is how <link> and <a> say what
			// they point at. It was never read as a label, which is why
			// <link rel="canonical"> went unused despite appearing on ten of
			// thirteen news sites with no competition for the meaning.
			//
			// Values that name nothing, preconnect and stylesheet and nofollow,
			// simply match no property. That is the point: rel says which of
			// these elements is worth reading, and the ones that mislead are
			// the ones where rel was ignored in favour of a generic slot.
			addQualified(v, 1.0)
		}
		if v, ok := el.Attr("id"); ok {
			addNamed(add, v, 0.9)
		}
		if v, ok := el.Attr("class"); ok {
			for _, cls := range strings.Fields(v) {
				addNamed(add, cls, 0.8)
			}
		}
		if v, ok := el.Attr("aria-label"); ok {
			add(v, 0.9)
		}
		out = append(out, neighbourLabels(el)...)
		// One level of ancestor class context, damped further.
		if p := el.Parent; p != nil && p.Kind == graph.KindElement {
			if v, ok := p.Attr("class"); ok {
				for _, cls := range strings.Fields(v) {
					addNamed(add, cls, 0.5)
				}
			}
		}
	}
	return out
}

// addNamed adds a class or id as a label unless it is purely presentational.
//
// A class is added whole, because "entry-title" is a better label than "entry"
// and "title" separately, and it is dropped only when every word in it is
// presentational.
func addNamed(add func(string, float64), value string, weight float64) {
	if presentational(value) {
		return
	}
	add(value, weight)
}

// presentational reports whether every word of a class or id is layout
// vocabulary rather than a description of content.
func presentational(value string) bool {
	seen := false
	for _, w := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		// A single character names nothing. Utility frameworks abbreviate their
		// axes this way, so max-w-full and py-2 are entirely presentational and
		// would otherwise survive on the strength of one letter.
		if len(w) <= 1 {
			seen = true
			continue
		}
		if !stopLabels[w] {
			return false
		}
		seen = true
	}
	return seen
}

// stopLabels is CSS vocabulary: words describing how something looks or where
// it sits, never what it holds.
//
// Utility frameworks put dozens of these on every element, and they were being
// read as labels at 0.8. Measured on nineteen news sites, class:text, class:font,
// class:hover and class:flex attached themselves to every field indiscriminately,
// and class:brand, class:blue and class:dark looked perfectly discriminating for
// `author` only because five of the sites shared one theme.
//
// Nothing here may be a word that could name a value. `title`, `date`, `author`,
// `byline`, `content`, `summary`, `section`, `category`, `link`, `item` and
// `price` are all absent on purpose: several are property names in the shipped
// templates, and a stop word that swallows one would cost the field it names.
var stopLabels = map[string]bool{
	// Size and spacing.
	"xs": true, "sm": true, "md": true, "lg": true, "xl": true, "xxl": true,
	"auto": true, "full": true, "half": true, "max": true, "min": true,
	"pad": true, "padding": true, "margin": true, "gap": true, "space": true,
	"width": true, "height": true, "size": true, "px": true, "rem": true,

	// Layout.
	"flex": true, "grid": true, "block": true, "inline": true, "float": true,
	"clear": true, "wrap": true, "nowrap": true, "col": true, "cols": true,
	"row": true, "rows": true, "container": true, "wrapper": true,
	"inner": true, "outer": true, "justify": true, "align": true,
	"items": true, "center": true, "middle": true, "start": true, "end": true,
	"top": true, "bottom": true, "left": true, "right": true,
	"absolute": true, "relative": true, "fixed": true, "sticky": true,
	"static": true, "overflow": true, "hidden": true, "visible": true,
	"order": true, "basis": true, "grow": true, "shrink": true,

	// Typography as presentation.
	"font": true, "text": true, "bold": true, "italic": true,
	"uppercase": true, "lowercase": true, "capitalize": true,
	"underline": true, "leading": true, "tracking": true,
	"antialiased": true, "truncate": true, "nowrap2": true,

	// Colour and theme.
	"color": true, "colour": true, "bg": true, "background": true,
	"dark": true, "light": true, "white": true, "black": true,
	"gray": true, "grey": true, "blue": true, "red": true, "green": true,
	"yellow": true, "orange": true, "purple": true, "pink": true,
	"brand": true, "primary": true, "secondary": true, "accent": true,
	"muted": true, "theme": true, "invert": true,

	// Effects and state.
	"hover": true, "focus": true, "active": true, "disabled": true,
	"transition": true, "transform": true, "duration": true, "ease": true,
	"shadow": true, "rounded": true, "border": true, "opacity": true,
	"scale": true, "rotate": true, "translate": true, "cursor": true,
	"pointer": true, "select": true, "outline": true, "ring": true,

	// Responsive and accessibility helpers.
	"mobile": true, "desktop": true, "tablet": true, "screen": true,
	"print": true, "sr": true, "only": true, "visually": true,
	"clearfix": true, "js": true, "no": true,
}

// genericHTMLAttrs are HTML's general-purpose attributes: slots that hold a
// value without saying anything about what it means.
//
// Everywhere else a name is a description. A JSON key, an XML attribute and a
// CSS property were each written to say what they hold, which is why the name
// alone is worth a full-weight label. HTML's globals are the exception. `title`
// is a tooltip, `content` is wherever a <meta> puts its payload, `value` is
// whatever a control holds. They name a place, not a thing.
//
// Read as descriptions they are false friends, and the strongest one on the
// news corpus: <link rel="alternate" title="West Florida News"> scored 1.000
// against the `title` property, beating the <h1> holding the actual headline,
// on every one of nineteen sites. The result was 503 records sharing ten
// titles, one per site.
//
// They keep a reduced weight rather than none. An <abbr title="..."> really
// does describe its content, so the signal is weak rather than absent.
var genericHTMLAttrs = map[string]bool{
	"title": true, "alt": true, "value": true, "content": true,
	"label": true, "name": true, "id": true, "class": true,
	"href": true, "src": true, "type": true, "role": true,
	"style": true, "target": true, "placeholder": true,
}

// genericAttrWeight is what an HTML general-purpose attribute's own name is
// worth as a label. Below `class` at 0.8, since a class at least names a role
// the author chose.
const genericAttrWeight = 0.6

// attrNameWeight reports how strong a label an attribute's own name is.
func attrNameWeight(n *graph.Node) float64 {
	switch n.Format() {
	case graph.FormatXML, graph.FormatFeed, graph.FormatSVG, graph.FormatJSON:
		// The name was chosen to describe the value.
		return 1.0
	}
	if genericHTMLAttrs[n.Name] {
		return genericAttrWeight
	}
	// Specific HTML attributes still describe: datetime, hreflang, charset.
	return 1.0
}

// tagLabel is the word an element name means, and how strong a claim it is.
type tagLabel struct {
	word   string
	weight float64
}

// semanticTags are the HTML elements that name their own content.
//
// The weights say how specific the claim is rather than how sure we are of the
// mapping. There is one <h1> and it is the page's subject, so it is nearly as
// strong as a declared itemprop. There are many <h2> and <h3>, on related
// stories and sidebars and nav, so a subheading is a heading without being the
// title, and it is weighted to lose to anything better.
//
// Deliberately small. Every entry has to be an element whose meaning is fixed
// by the HTML specification, because that is what makes it hold on a Greek or
// Russian page where nothing else in the markup is a word we know.
var semanticTags = map[string]tagLabel{
	"h1":         {wordHeading, 0.95},
	"h2":         {wordHeading, 0.60},
	"h3":         {wordHeading, 0.55},
	"h4":         {wordHeading, 0.50},
	"h5":         {wordHeading, 0.50},
	"h6":         {wordHeading, 0.50},
	"time":       {"date", 0.95},
	"address":    {"author", 0.90},
	"figcaption": {"caption", 0.90},
	"cite":       {"citation", 0.90},
	"blockquote": {"quote", 0.85},
}

// labelTags are elements whose text names a sibling value.
var labelTags = map[string]bool{
	"label": true, "dt": true, "th": true, "legend": true, "caption": true,
	"strong": true, "b": true, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true,
}

// neighbourLabels finds label text positioned next to an element: a preceding
// <label>/<dt>/<th>, or a "Name:" prefix in the preceding text run. These are
// how pages actually annotate values that carry no useful attributes.
func neighbourLabels(el *graph.Node) []weightedLabel {
	var out []weightedLabel
	parent := el.Parent
	if parent == nil {
		return nil
	}

	// Find the element among its parent's children, then look backwards.
	idx := -1
	for i, c := range parent.Children {
		if c == el {
			idx = i
			break
		}
	}
	if idx <= 0 {
		return nil
	}
	for i := idx - 1; i >= 0 && i >= idx-3; i-- {
		sib := parent.Children[i]
		switch {
		case sib.Kind == graph.KindElement && labelTags[sib.Name]:
			out = append(out, weightedLabel{text: trimLabel(sib.Text()), weight: 0.9})
		case sib.Kind == graph.KindText:
			if t := trimLabel(sib.Value); t != "" && strings.HasSuffix(strings.TrimSpace(sib.Value), ":") {
				out = append(out, weightedLabel{text: t, weight: 0.85})
			}
		}
	}

	// A table cell is named by the header in the same column.
	if el.Name == "td" {
		if h := columnHeader(el); h != "" {
			out = append(out, weightedLabel{text: trimLabel(h), weight: 0.9})
		}
	}
	return out
}

// columnHeader returns the text of the <th> occupying the same column as a
// <td>, looking in the first row of the enclosing table.
func columnHeader(td *graph.Node) string {
	row := td.Parent
	if row == nil || row.Name != "tr" {
		return ""
	}
	col := 0
	for _, c := range row.Children {
		if c.Kind != graph.KindElement || (c.Name != "td" && c.Name != "th") {
			continue
		}
		if c == td {
			break
		}
		col++
	}

	// Walk up to the table, then find the first row containing headers.
	table := row
	for table != nil && table.Name != "table" && table.Kind == graph.KindElement {
		table = table.Parent
	}
	if table == nil || table.Name != "table" {
		return ""
	}
	var header string
	table.Walk(func(c *graph.Node) bool {
		if header != "" || c.Kind != graph.KindElement || c.Name != "tr" {
			return header == ""
		}
		i := 0
		for _, cell := range c.Children {
			if cell.Kind != graph.KindElement || cell.Name != "th" {
				continue
			}
			if i == col {
				header = cell.Text()
				return false
			}
			i++
		}
		return header == ""
	})
	return header
}

// splitLabelled splits self-labelled text such as "Make: Toyota" into its
// label and value. It only reports a split when the part before the colon
// looks like a label rather than the start of a sentence: short, few words,
// and followed by something.
func splitLabelled(text string) (label, value string, ok bool) {
	i := strings.IndexByte(text, ':')
	if i <= 0 || i > 40 {
		return "", "", false
	}
	label = strings.TrimSpace(text[:i])
	value = strings.TrimSpace(text[i+1:])
	if label == "" || value == "" || len(strings.Fields(label)) > 4 {
		return "", "", false
	}
	return label, value, true
}

// trimLabel strips the punctuation pages put around labels.
func trimLabel(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, ":*-–—  \t")
	if len(s) > 60 {
		return ""
	}
	return strings.TrimSpace(s)
}

// normalize lowercases, collapses whitespace, and drops punctuation so that
// "Fuel Type:" and "fuel_type" compare equal.
func normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := true
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			prevSpace = false
		case !prevSpace:
			b.WriteByte(' ')
			prevSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

// tokenOverlap returns the Jaccard-style overlap of two token sets, measured
// against the smaller set so a short label can fully match a long one.
func tokenOverlap(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	set := make(map[string]bool, len(b))
	for _, t := range b {
		set[t] = true
	}
	hits := 0
	for _, t := range a {
		if set[t] {
			hits++
		}
	}
	if hits == 0 {
		return 0
	}
	minLen := len(a)
	if len(b) < minLen {
		minLen = len(b)
	}
	return float64(hits) / float64(minLen)
}

// cleanNumber strips the grouping, currency, and unit decoration that pages
// wrap around numbers so ParseFloat has a chance.
func cleanNumber(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r == '.', r == '-', r == '+':
			b.WriteRune(r)
		case r == ',':
			// Thousands separator; drop it.
		}
	}
	return b.String()
}

// extractNumber returns the first numeric run in s, or "" if there is none.
func extractNumber(s string) string {
	start := -1
	for i, r := range s {
		if r >= '0' && r <= '9' {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 && r != '.' && r != ',' {
			return s[start:i]
		}
	}
	if start >= 0 {
		return s[start:]
	}
	return ""
}

func clamp(f float64) float64 {
	switch {
	case f < 0:
		return 0
	case f > 1:
		return 1
	}
	return f
}
