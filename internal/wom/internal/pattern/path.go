// SPDX-License-Identifier: MIT

package pattern

import (
	"regexp"
	"strings"

	"github.com/rangertaha/scour/internal/wom/internal/graph"
)

// A locator addresses a set of nodes that are equivalent for the caller's
// purposes: the same field in every row of a list, on every page of a site.
// Turning concrete node paths into one pattern means deciding what varied by
// accident and what identifies the value.
//
// Two mechanisms do that. Positional indices that differ across the group are
// dropped and ones that agree are kept, which is what turns
// /ul[1]/li[1..n]/span[1] into /ul[1]/li/span[1]. Where position is not what
// distinguishes the elements — a run of <meta> tags, say — a semantic
// attribute becomes a predicate instead.

// indexRe matches the positional index shared by every path dialect wom emits:
// XPath /div[2], JSONPath [0], and PDF page[1].
var indexRe = regexp.MustCompile(`\[\d+\]`)

// nthRe matches the CSS equivalent.
var nthRe = regexp.MustCompile(`:nth-of-type\(\d+\)`)

// predicateRe matches an attribute predicate in a generalized XPath.
var predicateRe = regexp.MustCompile(`\[@([A-Za-z_:][-\w:.]*)=(?:"([^"]*)"|'([^']*)')\]`)

// Coarse strips every positional index from a path, producing the key that
// groups instances of one pattern together.
func Coarse(path string) string {
	return indexRe.ReplaceAllString(path, "")
}

// Generalize merges the paths of a group into one pattern, keeping the indices
// the instances agreed on and dropping the ones that varied.
func Generalize(paths []string) string {
	return generalizeWith(paths, indexRe, stripFirst)
}

// GeneralizeSelector is Generalize for CSS selectors, where the positional
// marker is :nth-of-type().
func GeneralizeSelector(selectors []string) string {
	return generalizeWith(selectors, nthRe, commonDescendantSuffix)
}

// fallback decides what a group generalizes to when its instances do not
// decompose into the same literals. It is dialect specific, so it is passed in.
type fallback func(paths []string, marker *regexp.Regexp) string

func generalizeWith(paths []string, marker *regexp.Regexp, onDiffer fallback) string {
	paths = dedupe(paths)
	switch len(paths) {
	case 0:
		return ""
	case 1:
		return paths[0]
	}

	lits := make([][]string, len(paths))
	idxs := make([][]string, len(paths))
	for i, p := range paths {
		lits[i] = marker.Split(p, -1)
		idxs[i] = marker.FindAllString(p, -1)
	}

	// Instances that do not decompose identically cannot be merged
	// position-by-position.
	for i := 1; i < len(paths); i++ {
		if len(lits[i]) != len(lits[0]) || len(idxs[i]) != len(idxs[0]) {
			return onDiffer(paths, marker)
		}
		for j := range lits[i] {
			if lits[i][j] != lits[0][j] {
				return onDiffer(paths, marker)
			}
		}
	}

	var b strings.Builder
	for j, lit := range lits[0] {
		b.WriteString(lit)
		if j >= len(idxs[0]) {
			continue
		}
		same := true
		for i := 1; i < len(paths); i++ {
			if idxs[i][j] != idxs[0][j] {
				same = false
				break
			}
		}
		if same {
			b.WriteString(idxs[0][j])
		}
	}
	return b.String()
}

// stripFirst is the fallback for path dialects: drop every index from the first
// instance.
//
// It is only a generalization when the instances differ by position alone,
// which for XPath and JSONPath is the ordinary case, because there the varying
// part is the index and the index is what gets dropped.
func stripFirst(paths []string, marker *regexp.Regexp) string {
	return marker.ReplaceAllString(paths[0], "")
}

// commonDescendantSuffix is the fallback for CSS, where the varying part is a
// literal rather than an index.
//
// A site that wraps each article in an id unique to the page, #asset-<uuid>,
// gives every instance a different first segment. Keeping the first instance
// there is not a generalization at all: it is a selector matching exactly the
// one page it was induced from, while the XPath for the same field stays
// generic. That is how the two dialects came to disagree on a corpus where the
// XPath worked across 660 records.
//
// What the group does share is its tail, and a CSS selector is already
// descendant-anchored, so the shared tail selects the same elements without
// claiming anything about what encloses them. With no shared tail there is
// nothing to say, and no selector is better than one that fits a single page.
func commonDescendantSuffix(paths []string, marker *regexp.Regexp) string {
	segs := make([][]string, len(paths))
	for i, p := range paths {
		segs[i] = splitSelector(marker.ReplaceAllString(p, ""))
	}

	n := 0
	for {
		var want string
		for i, s := range segs {
			if n >= len(s) {
				return joinSelector(segs[0][len(segs[0])-n:])
			}
			seg := s[len(s)-1-n]
			if i == 0 {
				want = seg
				continue
			}
			if seg != want {
				return joinSelector(segs[0][len(segs[0])-n:])
			}
		}
		n++
	}
}

// splitSelector breaks a selector into its combinator-separated steps. Only the
// child and descendant combinators are produced by the emitter, and both are
// generalized the same way, so both are treated as one separator.
func splitSelector(sel string) []string {
	fields := strings.FieldsFunc(sel, func(r rune) bool { return r == '>' })
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, strings.Fields(f)...)
	}
	return out
}

func joinSelector(segs []string) string { return strings.Join(segs, " > ") }

// Discriminator is a semantic attribute that tells sibling elements apart when
// their position cannot. Meta tags are the clearest case: every one of them is
// at the same path and only @property or @name says which is which.
type Discriminator struct {
	Name  string
	Value string
}

// Set reports whether a discriminator was found.
func (d Discriminator) Set() bool { return d.Name != "" }

// discriminatorAttrs are the attributes that carry meaning rather than
// styling, in preference order. class and id are excluded on purpose: they
// vary for presentational reasons and would fragment real repeating records.
var discriminatorAttrs = []string{"property", "itemprop", "name", "rel"}

// DiscriminatorFor returns the semantic attribute identifying the element that
// owns a node, if it has one.
func DiscriminatorFor(n *graph.Node) Discriminator {
	el := graph.OwnerElement(n)
	if el == nil {
		return Discriminator{}
	}
	for _, name := range discriminatorAttrs {
		if v, ok := el.Attr(name); ok && v != "" && len(v) < 120 {
			return Discriminator{Name: name, Value: v}
		}
	}
	return Discriminator{}
}

// Predicated splices an attribute predicate into the last element step of an
// XPath whose positional index had to be dropped. A group of meta tags
// generalizes to ./meta/@content, which matches every meta on the page;
// ./meta[@property="article:author"]/@content is what a person would have
// written and is what actually identifies the value.
func Predicated(xpath string, disc Discriminator) string {
	if xpath == "" || !disc.Set() {
		return xpath
	}
	elemPath, leaf := splitLeafStep(xpath)
	cut := strings.LastIndex(elemPath, "/") + 1
	seg := elemPath[cut:]
	if seg == "" || strings.Contains(seg, "@") {
		// Already carries a predicate; a second one would say no more.
		return xpath
	}

	// A positional index is not the same kind of claim as a predicate, and it
	// used to block one. `./meta[1]/@content` is only the title on the pages
	// that happened to be sampled: it says the value is the first <meta>, which
	// is a fact about this template on this day. `./meta[@property="og:title"]`
	// says which meta, and holds on every page and every site that publishes
	// OpenGraph.
	//
	// So when both are available the predicate replaces the index rather than
	// deferring to it. This only arises when the index survived generalization,
	// which means every sampled page agreed on it, and a narrow sample agreeing
	// is exactly the situation in which an index looks more reliable than it is.
	if i := strings.IndexByte(seg, '['); i >= 0 {
		elemPath = elemPath[:cut] + seg[:i]
		xpath = elemPath + leaf
	}

	predicated := elemPath + "[@" + disc.Name + "=" + quoteXPath(disc.Value) + "]" + leaf

	// A locator is only worth emitting if it can be read back. Values holding
	// both quote characters need XPath 1.0's concat(), which SplitPredicates
	// cannot parse — emitting one would produce a precise-looking locator that
	// matches nothing. Verifying the round trip here means any future change
	// to quoting is checked automatically rather than trusted.
	if bare, preds := SplitPredicates(predicated); len(preds) != 1 ||
		preds[0].Name != disc.Name || preds[0].Value != disc.Value || bare != xpath {
		return xpath
	}
	return predicated
}

// PredicatedSelector is Predicated for CSS, where the equivalent is an
// attribute selector.
func PredicatedSelector(selector string, disc Discriminator) string {
	if selector == "" || !disc.Set() {
		return selector
	}
	cut := strings.LastIndex(selector, ">") + 1
	last := selector[cut:]
	if strings.ContainsAny(last, "[#") {
		return selector
	}
	// :nth-of-type() is CSS's positional index, and it gives way to the
	// attribute selector for the same reason the XPath index does: it describes
	// where the element sat in the pages that were sampled, not which element
	// it is. The two dialects have to agree, or one locator contradicts the
	// other.
	if i := strings.Index(last, ":nth-of-type("); i >= 0 {
		selector = selector[:cut] + last[:i]
	} else if strings.Contains(last, ":") {
		return selector
	}
	return selector + "[" + disc.Name + "=" + quoteCSS(disc.Value) + "]"
}

// splitLeafStep separates the element part of an XPath from a trailing
// attribute or text step.
func splitLeafStep(xpath string) (elemPath, leaf string) {
	if i := strings.LastIndex(xpath, "/@"); i >= 0 {
		return xpath[:i], xpath[i:]
	}
	if i := strings.LastIndex(xpath, "/text()"); i >= 0 {
		return xpath[:i], xpath[i:]
	}
	return xpath, ""
}

// quoteXPath renders a string literal for an XPath predicate. XPath 1.0 has no
// escape mechanism, so a value containing both quote characters is split with
// concat().
func quoteXPath(s string) string {
	if !strings.Contains(s, `"`) {
		return `"` + s + `"`
	}
	if !strings.Contains(s, `'`) {
		return `'` + s + `'`
	}
	parts := strings.Split(s, `"`)
	quoted := make([]string, 0, len(parts)*2)
	for i, p := range parts {
		if i > 0 {
			quoted = append(quoted, `'"'`)
		}
		if p != "" {
			quoted = append(quoted, `"`+p+`"`)
		}
	}
	return "concat(" + strings.Join(quoted, ",") + ")"
}

// quoteCSS renders a string literal for a CSS attribute selector.
func quoteCSS(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

// SplitPredicates strips attribute predicates from a pattern, returning the
// bare structural path and the predicates to check separately. Node paths
// never carry predicates, so only the pattern needs this.
func SplitPredicates(pattern string) (string, []Discriminator) {
	if !strings.Contains(pattern, "[@") {
		return pattern, nil
	}
	matches := predicateRe.FindAllStringSubmatch(pattern, -1)
	preds := make([]Discriminator, 0, len(matches))
	for _, m := range matches {
		value := m[2]
		if value == "" {
			value = m[3]
		}
		preds = append(preds, Discriminator{Name: m[1], Value: value})
	}
	return predicateRe.ReplaceAllString(pattern, ""), preds
}

// Satisfies reports whether the element owning n carries every predicate.
func Satisfies(n *graph.Node, preds []Discriminator) bool {
	if len(preds) == 0 {
		return true
	}
	el := graph.OwnerElement(n)
	if el == nil {
		return false
	}
	for _, p := range preds {
		if v, ok := el.Attr(p.Name); !ok || v != p.Value {
			return false
		}
	}
	return true
}

// pathIdx records a positional index and the length of the literal text that
// preceded it, which is what lets a pattern's indices be aligned against a
// node's even though the pattern omits some of them.
type pathIdx struct {
	at  int
	val string
}

// decomposePath splits a path into its literal text and its positional
// indices. "/ul[1]/li[2]" becomes "/ul/li" plus indices [1] at offset 3 and
// [2] at offset 6.
func decomposePath(p string) (literal string, idx []pathIdx) {
	locs := indexRe.FindAllStringIndex(p, -1)
	if len(locs) == 0 {
		return p, nil
	}
	var b strings.Builder
	b.Grow(len(p))
	prev := 0
	idx = make([]pathIdx, 0, len(locs))
	for _, loc := range locs {
		b.WriteString(p[prev:loc[0]])
		idx = append(idx, pathIdx{at: b.Len(), val: p[loc[0]:loc[1]]})
		prev = loc[1]
	}
	b.WriteString(p[prev:])
	return b.String(), idx
}

// Conforms reports whether a concrete node path is an instance of a
// generalized pattern.
//
// Generalization drops the indices that varied across a group, so
// "/ul[1]/li/span[1]" stands for every li in that list. A path conforms when
// its literal structure is identical and it agrees on every index the pattern
// chose to keep — an index the pattern dropped may take any value.
func Conforms(path, pattern string) bool {
	if path == pattern {
		return true
	}
	patLit, patIdx := decomposePath(pattern)
	nodeLit, nodeIdx := decomposePath(path)
	if patLit != nodeLit {
		return false
	}

	j := 0
	for _, want := range patIdx {
		for j < len(nodeIdx) && nodeIdx[j].at < want.at {
			j++
		}
		if j >= len(nodeIdx) || nodeIdx[j].at != want.at || nodeIdx[j].val != want.val {
			return false
		}
		j++
	}
	return true
}
