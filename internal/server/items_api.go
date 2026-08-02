// SPDX-License-Identifier: GPL-3.0-or-later

package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/rangertaha/scour/internal/defaults"
	"github.com/rangertaha/scour/internal/store"
)

// registerItemParts wires the sub-resources of an item, plus the model that is
// induced from it and the schemas it can be started from.
//
// They live apart from the four routes on the item itself because they are a
// different kind of thing. `GET /v1/items/{name}` reads the item; these edit the
// pieces it is assembled out of, one member at a time, which is how the command
// line edits them. A single PATCH carrying the whole item would have to be sent
// back in full to remove one word from one property, and would lose the
// difference between adding a word and declaring the set that `scour item tag`
// is careful about.
//
// It is a method rather than a free function because every handler needs the
// store, the config and the failure helpers hanging off the server. The caller
// wires it into the mux alongside the routes in server.go.
func (s *Server) registerItemParts(mux *http.ServeMux) {
	// A property is created and changed through different verbs because the
	// two acts differ: POST teaches a field the item did not have, PATCH
	// corrects one it did. DELETE takes the property away, which is not what
	// clearing one of its details means; see clearProperty.
	mux.HandleFunc("POST /v1/items/{name}/properties", s.addProperty)
	mux.HandleFunc("PATCH /v1/items/{name}/properties/{prop}", s.patchProperty)
	mux.HandleFunc("DELETE /v1/items/{name}/properties/{prop}", s.removeProperty)

	// Four verbs on a set of words, twice over: once for what the item is
	// called and once for what a page might call one of its properties.
	mux.HandleFunc("GET /v1/items/{name}/aliases", s.listAliases)
	mux.HandleFunc("POST /v1/items/{name}/aliases", s.addAliases)
	mux.HandleFunc("PUT /v1/items/{name}/aliases", s.setAliases)
	mux.HandleFunc("DELETE /v1/items/{name}/aliases/{word}", s.removeAlias)

	mux.HandleFunc("GET /v1/items/{name}/properties/{prop}/labels", s.listLabels)
	mux.HandleFunc("POST /v1/items/{name}/properties/{prop}/labels", s.addLabels)
	mux.HandleFunc("PUT /v1/items/{name}/properties/{prop}/labels", s.setLabels)
	mux.HandleFunc("DELETE /v1/items/{name}/properties/{prop}/labels/{word}", s.removeLabel)

	mux.HandleFunc("GET /v1/templates", s.templates)

	// The model is a sub-resource of the item rather than a noun of its own,
	// because there is exactly one per item and it has no name but the item's.
	// Training it is POST /v1/items/{name}/model/runs, which lives with the
	// other runs.
	mux.HandleFunc("GET /v1/items/{name}/model", s.showModel)
	mux.HandleFunc("GET /v1/items/{name}/model/rules", s.modelRules)
	mux.HandleFunc("DELETE /v1/items/{name}/model", s.removeModel)
}

// wordsRequest is what the two collections of words accept.
//
// An array rather than a scalar because the command line's flags repeat:
// `--add byline --add 'written by'` is one edit, and one request per word would
// leave a client that failed halfway with a set that is neither what it had nor
// what it asked for. A phrase is one word, so nothing is split on spaces.
type wordsRequest struct {
	Words []string `json:"words"`
}

// item reads the item named by the path, answering the request itself when
// there is not one. The store's miss carries the nearest name, so a typo comes
// back as a suggestion rather than as a bare 404.
func (s *Server) item(w http.ResponseWriter, r *http.Request) (*store.Item, bool) {
	item, err := s.store.Item(r.Context(), r.PathValue("name"))
	if err != nil {
		s.fail(w, r, err)
		return nil, false
	}
	return item, true
}

// scopeOf reads ?on=, which narrows an edit to one site.
//
// It is normalised here rather than trusted, because a property taught for a
// domain and a target added for that domain have to agree on what the domain
// is. example.com, www.example.com and https://example.com/ all name one site,
// and a scope that did not say so would write a second property row nothing
// ever reads.
func scopeOf(r *http.Request) string {
	return store.NormaliseDomain(r.URL.Query().Get("on"))
}

// propertyRequest is what creating a property accepts.
//
// The words a page might label the property with are not here, even though
// `scour item add -p author -a byline` teaches one in the same breath. They are
// their own collection, and a `labels` array beside this `label` regex would be
// two fields one letter apart meaning entirely different things: one is the
// pattern a name must match, the other is a list of names. That is a trap worth
// one extra request to avoid.
type propertyRequest struct {
	Name        string `json:"name"`
	Type        string `json:"type,omitempty"`
	Example     string `json:"example,omitempty"`
	Description string `json:"description,omitempty"`
	Regex       string `json:"regex,omitempty"`
	Label       string `json:"label,omitempty"`
}

// addProperty teaches an item a property it did not have.
//
// A property that is already there is a conflict rather than a silent update,
// because the design gives the two acts separate verbs: this one is `item add
// -p <p> -e <v>` where the property is new, and PATCH is the same command where
// it is not. A POST that quietly overwrote would make the distinction
// undetectable, and a client correcting a field would never learn that it had
// been describing a property somebody else had already defined differently.
func (s *Server) addProperty(w http.ResponseWriter, r *http.Request) {
	item, ok := s.item(w, r)
	if !ok {
		return
	}
	var req propertyRequest
	if !decode(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		s.badRequest(w, "name is required")
		return
	}
	if !s.wellFormed(w, req.Type, req.Regex, req.Label) {
		return
	}

	scope := scopeOf(r)
	if _, err := s.store.Property(r.Context(), item.ID, scope, name); err == nil {
		writeError(w, http.StatusConflict, fmt.Sprintf(
			"%s already has a property %q%s: PATCH it to change what it says",
			item.Name, name, onSite(scope)))
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, err)
		return
	}

	if err := s.store.AddPropertyDetail(r.Context(), item.ID, store.PropertyDetail{
		Domain: scope, Name: name, Type: req.Type, Example: req.Example,
		Description: req.Description, Regex: req.Regex, Label: req.Label,
	}); err != nil {
		s.fail(w, r, err)
		return
	}

	prop, err := s.store.Property(r.Context(), item.ID, scope, name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Location", propertyPath(item.Name, name, scope))
	writeJSON(w, http.StatusCreated, map[string]any{"property": prop})
}

// nullable is a field that can be absent, given, or explicitly null.
//
// A *string cannot tell those three apart: encoding/json sets a pointer to nil
// for a JSON null and leaves it nil when the key never appeared, so both arrive
// as the same nothing. The difference is the whole point of this resource.
// Clearing an example is `item rm -p price --clear example`, which keeps the
// property, and it has to be distinguishable from a request that simply did not
// mention the example.
type nullable struct {
	given bool
	value string
}

// UnmarshalJSON records that the key was present, which is what the pointer
// could not. encoding/json calls this even for a JSON null, which is why the
// null is visible here at all.
func (n *nullable) UnmarshalJSON(b []byte) error {
	n.given = true
	if string(b) == "null" {
		n.value = ""
		return nil
	}
	return json.Unmarshal(b, &n.value)
}

// The details a property carries, named rather than spelled out at each use.
//
// The same five words are the request's field names, the store's column names
// and the names ClearPropertyFields switches on, and the three have to agree
// exactly. Spelled literally in each place, a typo in one of them is a field
// that reports success and never changes, which is the failure this resource
// exists to make impossible.
const (
	fieldType        = "type"
	fieldExample     = "example"
	fieldDescription = "description"
	fieldRegex       = "regex"
	fieldLabel       = "label"
)

// propertyPatch is what changing a property accepts. Every field is a nullable,
// so a request can say "leave this alone", "make it this" and "empty it".
type propertyPatch struct {
	Type        nullable `json:"type"`
	Example     nullable `json:"example"`
	Description nullable `json:"description"`
	Regex       nullable `json:"regex"`
	Label       nullable `json:"label"`
}

// patchProperty changes what a property says, keeping the property.
//
// This is the one row of the parity table that changes shape rather than
// collapsing. `item rm -p price --clear example` is a PATCH setting a field to
// null, not a DELETE, because it throws away one detail and keeps everything
// else the property knows: its type, its example, and every word taught for it.
// DELETE on this path removes the property itself, which is a different act
// with different consequences.
//
// A field given as null or as an empty string is cleared. The two are one act
// here because the store cannot write an empty string through the setting path:
// AddPropertyDetail writes only what it is given, so that describing a property
// more fully does not cost it what it already knew. Accepting `""` and doing
// nothing with it would be a request that reported success and changed nothing.
func (s *Server) patchProperty(w http.ResponseWriter, r *http.Request) {
	item, ok := s.item(w, r)
	if !ok {
		return
	}
	var req propertyPatch
	if !decode(w, r, &req) {
		return
	}
	if !s.wellFormed(w, req.Type.value, req.Regex.value, req.Label.value) {
		return
	}

	scope := scopeOf(r)
	name := r.PathValue("prop")

	// The property has to be there already. AddPropertyDetail creates what it
	// does not find, which would turn a misspelled name in the path into a
	// second property rather than into the 404 the caller needs to see.
	if _, err := s.store.Property(r.Context(), item.ID, scope, name); err != nil {
		s.fail(w, r, err)
		return
	}

	set := store.PropertyDetail{Domain: scope, Name: name}
	var clear []string
	for _, f := range []struct {
		column string
		into   *string
		given  nullable
	}{
		{fieldType, &set.Type, req.Type},
		{fieldExample, &set.Example, req.Example},
		{fieldDescription, &set.Description, req.Description},
		{fieldRegex, &set.Regex, req.Regex},
		{fieldLabel, &set.Label, req.Label},
	} {
		if !f.given.given {
			continue
		}
		if f.given.value == "" {
			clear = append(clear, f.column)
			continue
		}
		*f.into = f.given.value
	}

	// Setting runs first so that a request doing both ends with the emptied
	// fields empty. They are disjoint sets today, and ordering them anyway
	// means a future request that names one field twice cannot depend on which
	// loop happened to run last.
	if err := s.store.AddPropertyDetail(r.Context(), item.ID, set); err != nil {
		s.fail(w, r, err)
		return
	}
	if len(clear) > 0 {
		if err := s.store.ClearPropertyFields(r.Context(), item.ID, scope, name, clear); err != nil {
			s.fail(w, r, err)
			return
		}
	}

	prop, err := s.store.Property(r.Context(), item.ID, scope, name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"property": prop})
}

// removeProperty takes the property away, along with every word taught for it.
//
// Unscoped it removes the name from every domain it was taught on, which is
// what `scour item rm -p <p>` means. With ?on= it removes only that site's, so
// dropping a correction made for one paper leaves the schema every other paper
// is read with intact.
func (s *Server) removeProperty(w http.ResponseWriter, r *http.Request) {
	item, ok := s.item(w, r)
	if !ok {
		return
	}

	var err error
	if scope := scopeOf(r); scope != "" {
		err = s.store.DeletePropertyOn(r.Context(), item.ID, scope, r.PathValue("prop"))
	} else {
		err = s.store.DeleteProperty(r.Context(), item.ID, r.PathValue("prop"))
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listAliases is the other words the item goes by.
func (s *Server) listAliases(w http.ResponseWriter, r *http.Request) {
	item, ok := s.item(w, r)
	if !ok || !s.unscoped(w, r) {
		return
	}
	words, err := s.store.Aliases(r.Context(), item.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"aliases": words})
}

// addAliases teaches the item more words, leaving the ones it has.
func (s *Server) addAliases(w http.ResponseWriter, r *http.Request) {
	item, ok := s.item(w, r)
	if !ok || !s.unscoped(w, r) {
		return
	}
	var req wordsRequest
	if !decode(w, r, &req) {
		return
	}
	for _, word := range req.Words {
		if strings.TrimSpace(word) == "" {
			continue
		}
		if err := s.store.AddAlias(r.Context(), item.ID, word); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	s.aliasesNow(w, r, item)
}

// setAliases declares the whole set, these words and no others.
//
// A separate verb from POST because the two mean different things and a client
// has to be able to say which it wants. Adding is what a crawl that learned a
// new phrase does; declaring is what a person correcting a bad list does, and
// asking them to work out which words to delete first would make the second act
// depend on knowing the current state exactly.
func (s *Server) setAliases(w http.ResponseWriter, r *http.Request) {
	item, ok := s.item(w, r)
	if !ok || !s.unscoped(w, r) {
		return
	}
	var req wordsRequest
	if !decode(w, r, &req) {
		return
	}
	if err := s.store.SetAliases(r.Context(), item.ID, req.Words); err != nil {
		s.fail(w, r, err)
		return
	}
	s.aliasesNow(w, r, item)
}

// removeAlias drops one word.
//
// A word that was not there is a 404 rather than a quiet success, because
// reporting a removal that removed nothing is how someone ends up believing a
// word is gone while a crawl still matches on it.
func (s *Server) removeAlias(w http.ResponseWriter, r *http.Request) {
	item, ok := s.item(w, r)
	if !ok || !s.unscoped(w, r) {
		return
	}
	word := r.PathValue("word")
	n, err := s.store.RemoveAliases(r.Context(), item.ID, []string{word})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, fmt.Sprintf("%s is not tagged %q", item.Name, word))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// aliasesNow answers an edit with the set it ended up as, which is what the
// caller wanted to know and what the command line prints for the same reason.
func (s *Server) aliasesNow(w http.ResponseWriter, r *http.Request, item *store.Item) {
	words, err := s.store.Aliases(r.Context(), item.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"aliases": words})
}

// unscoped rejects ?on= where it cannot mean anything, answering the request
// itself when it does.
//
// An item's aliases have no domain. A property's do, because a schema describes
// what is wanted and a site describes how it says it, so what one paper calls a
// byline must not overwrite the next. The item is the thing being looked for
// rather than a name a page uses, so there is no per-site version of it to
// scope to. Ignoring the parameter would be worse than refusing it: the caller
// would get a 200 and believe an edit had been confined to one site when it had
// just been applied to all of them.
func (s *Server) unscoped(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Query().Get("on") == "" {
		return true
	}
	s.badRequest(w, "an item's aliases are not per-site, so ?on= means nothing here: "+
		"scope a property's labels instead")
	return false
}

// listLabels is the words a page might label one property with.
//
// "Labels" here is the set of names, which the store calls the property's
// aliases, and it is not the property's own `label` field. That field is a
// pattern saying which names count, written once; these are the names
// themselves, added one at a time. They answer the same question from opposite
// ends, and the API names the collection after the CLI's `item tag`.
func (s *Server) listLabels(w http.ResponseWriter, r *http.Request) {
	item, ok := s.item(w, r)
	if !ok {
		return
	}
	s.labelsNow(w, r, item)
}

// addLabels teaches a property more words, leaving the ones it has.
func (s *Server) addLabels(w http.ResponseWriter, r *http.Request) {
	item, ok := s.item(w, r)
	if !ok {
		return
	}
	var req wordsRequest
	if !decode(w, r, &req) {
		return
	}
	scope, prop := scopeOf(r), r.PathValue("prop")
	for _, word := range req.Words {
		if strings.TrimSpace(word) == "" {
			continue
		}
		if err := s.store.AddPropertyAlias(r.Context(), item.ID, scope, prop, word); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	s.labelsNow(w, r, item)
}

// setLabels declares the whole set for one property.
func (s *Server) setLabels(w http.ResponseWriter, r *http.Request) {
	item, ok := s.item(w, r)
	if !ok {
		return
	}
	var req wordsRequest
	if !decode(w, r, &req) {
		return
	}
	err := s.store.SetPropertyAliases(r.Context(), item.ID, scopeOf(r), r.PathValue("prop"), req.Words)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.labelsNow(w, r, item)
}

// removeLabel drops one word from one property.
func (s *Server) removeLabel(w http.ResponseWriter, r *http.Request) {
	item, ok := s.item(w, r)
	if !ok {
		return
	}
	scope, prop, word := scopeOf(r), r.PathValue("prop"), r.PathValue("word")

	n, err := s.store.RemovePropertyAliases(r.Context(), item.ID, scope, prop, []string{word})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, fmt.Sprintf(
			"%s%s is not tagged %q", prop, onSite(scope), word))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// labelsNow answers with the set as it now stands, scoped the way the request
// was. The lookup is what reports an unknown property, so the four verbs all
// fail the same way on a name that is not there.
func (s *Server) labelsNow(w http.ResponseWriter, r *http.Request, item *store.Item) {
	words, err := s.store.PropertyAliases(r.Context(), item.ID, scopeOf(r), r.PathValue("prop"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"labels": words})
}

// templates is the schemas that ship in the binary.
//
// Not under /v1/items, because a template belongs to no item: it is what an
// item can be started from, and the same one starts as many as you like. It is
// a read with no state behind it, which is why it needs no store at all.
func (s *Server) templates(w http.ResponseWriter, r *http.Request) {
	out, err := templateList()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// templateList reads every shipped schema and the fields it describes.
//
// Shared with the MCP tool that answers the same question, so the two surfaces
// cannot come to disagree about which templates exist or what is in them.
func templateList() (templatesOut, error) {
	names, err := defaults.Names()
	if err != nil {
		return templatesOut{}, err
	}

	out := templatesOut{Templates: make([]templateInfo, 0, len(names))}
	for _, name := range names {
		schema, err := defaults.Schema(name)
		if err != nil {
			return templatesOut{}, err
		}

		// A template's outermost prop names the record and its children are the
		// fields. A flat template is taken as the fields themselves, so a simple
		// one does not have to be nested to be valid.
		props := schema
		if len(schema) == 1 && len(schema[0].Props) > 0 {
			props = schema[0].Props
		}

		fields := make([]string, 0, len(props))
		for _, p := range props {
			fields = append(fields, p.Name)
		}
		out.Templates = append(out.Templates, templateInfo{Name: name, Fields: fields})
	}
	return out, nil
}

// modelSummary is what was learned, and from how much.
//
// The counts are the point. A model trained on forty pages and one trained on
// twelve hundred are the same file with very different standing, and nothing
// else in the surface says which one you are holding.
type modelSummary struct {
	Item      string `json:"item"`
	Algorithm string `json:"algorithm"`
	Trained   string `json:"trained"`
	// Accuracy is only measured when there were enough examples to hold some
	// back, so it is omitted rather than reported as zero: a model that was
	// never scored did not score nothing.
	Accuracy     float64 `json:"accuracy,omitempty"`
	Observations int     `json:"observations"`
	Rules        int64   `json:"rules"`
	Records      int64   `json:"records"`
	Marked       int64   `json:"marked"`
	Pages        int64   `json:"pages"`
	Path         string  `json:"path,omitempty"`
	// Missing says the meta claims a model that is not on disk. A training run
	// would silently replace it and a crawl would silently score without it,
	// so it is worth a field rather than a shrug.
	Missing bool `json:"missing,omitempty"`
}

// showModel reports the model for one item.
//
// An item with no model is a 404 rather than an empty body, because a model is
// a resource here and one that has never been trained does not exist yet. The
// message says how to make it, since that is the only useful next move.
func (s *Server) showModel(w http.ResponseWriter, r *http.Request) {
	item, ok := s.item(w, r)
	if !ok {
		return
	}
	st, err := s.store.Status(r.Context(), item.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if st.Model == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf(
			"%s has no model yet: POST /v1/items/%s/model/runs to train one", item.Name, item.Name))
		return
	}

	out := modelSummary{
		Item:         item.Name,
		Algorithm:    st.Model.Algorithm,
		Trained:      st.Model.TrainedAt.UTC().Format(timeFormat),
		Accuracy:     st.Model.Accuracy,
		Observations: st.Model.Observations,
		Rules:        st.Rules,
		Records:      st.Matches,
		Marked:       st.Valid + st.Invalid,
		Pages:        st.Visited,
		Path:         st.Model.Path,
	}
	if out.Path != "" {
		if _, err := os.Stat(out.Path); err != nil {
			out.Missing = true
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"model": out})
}

// timeFormat is how the API writes a timestamp. RFC 3339 in UTC, so a client
// parses one thing whatever the server's clock is set to.
const timeFormat = "2006-01-02T15:04:05Z"

// ruleOut is one induced extraction rule.
//
// Format is here because a rule set describes a shape, and a feed and the HTML
// it links to are not the same shape. Rules are induced per format for that
// reason, and a listing that did not say which format a rule came from would
// show two sets of XPaths that look like they contradict each other when in
// fact each is right about its own kind of page.
type ruleOut struct {
	ID     uint   `json:"id"`
	Parent *uint  `json:"parent"`
	Prop   string `json:"prop"`
	Format string `json:"format"`
	XPath  string `json:"xpath,omitempty"`
	// Selector, Path and Regex are the other three ways a rule can locate or
	// trim a value, and any of them can be empty on a given rule.
	Selector    string  `json:"selector,omitempty"`
	Path        string  `json:"path,omitempty"`
	Regex       string  `json:"regex,omitempty"`
	URL         string  `json:"url,omitempty"`
	Probability float64 `json:"probability"`
	Support     int     `json:"support"`
}

// modelRules lists what the model learned to pull out of a page.
//
// This replaces GET /v1/items/{name}/rules. Rules are what training produced,
// so they belong under the model rather than beside the records: `model rm`
// takes them with it, and a path that said otherwise would suggest an item can
// have rules without having a model.
func (s *Server) modelRules(w http.ResponseWriter, r *http.Request) {
	item, ok := s.item(w, r)
	if !ok {
		return
	}
	rows, err := s.store.Rules(r.Context(), item.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rows = rulesOn(rows, scopeOf(r))

	// Counted per format alongside the listing, because the first question a
	// thin result raises is whether the crawl met the kind of page the rules
	// were induced from at all.
	formats := make(map[string]int, 2)
	out := make([]ruleOut, 0, len(rows))
	for _, rule := range rows {
		formats[rule.Format]++
		out = append(out, ruleOut{
			ID: rule.ID, Parent: rule.ParentID, Prop: rule.Prop, Format: rule.Format,
			XPath: rule.XPath, Selector: rule.Selector, Path: rule.Path,
			Regex: rule.Regex, URL: rule.URIPattern,
			Probability: rule.Probability, Support: rule.Support,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rules": out, "total": len(out), "formats": formats,
	})
}

// rulesOn keeps the rules induced for one site, and everything when no site was
// named.
//
// A rule addresses pages by a URI pattern rather than by a domain column, so
// the site is read back out of that pattern. The pattern is a regex with its
// dots escaped, which is why the backslashes come out before the comparison:
// example\.com and example.com are the same site written two ways.
//
// A child rule pulls one property out of a record its parent located, and
// carries no pattern of its own when the parent already said where to look. So
// the parent chain is followed rather than the rule read alone, or filtering by
// site would return the containers and drop every field inside them.
func rulesOn(rows []store.Rule, domain string) []store.Rule {
	if domain == "" {
		return rows
	}

	byID := make(map[uint]store.Rule, len(rows))
	for _, rule := range rows {
		byID[rule.ID] = rule
	}

	// Bounded so a parent chain that somehow loops cannot hang the request.
	// Rules nest a handful deep at most; anything beyond that is corruption.
	const maxDepth = 32

	out := make([]store.Rule, 0, len(rows))
	for _, rule := range rows {
		for at, depth := rule, 0; depth < maxDepth; depth++ {
			if strings.Contains(strings.ToLower(strings.ReplaceAll(at.URIPattern, `\`, "")), domain) {
				out = append(out, rule)
				break
			}
			if at.ParentID == nil {
				break
			}
			parent, ok := byID[*at.ParentID]
			if !ok {
				break
			}
			at = parent
		}
	}
	return out
}

// removeModel discards what was learned, keeping the pages and the marks.
//
// The rules and the fitted chain go with it, because they are what training
// produced. The cached pages stay, so retraining costs the induction again and
// not the crawl, and the marks stay, because they are the expensive part and a
// person made them.
func (s *Server) removeModel(w http.ResponseWriter, r *http.Request) {
	item, ok := s.item(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteModel(r.Context(), item.ID); err != nil {
		s.fail(w, r, err)
		return
	}

	// The files on disk are the model and the rows are what point at them, so
	// leaving the files would let a later run load a model the database no
	// longer knows about. Extraction is one file per format and which formats a
	// crawl met is not knowable from here, so they are matched rather than
	// named.
	extracts, err := filepath.Glob(s.cfg.ExtractModelGlob(item.Name))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, path := range append(extracts, s.cfg.ScoreModelPath(item.Name)) {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.fail(w, r, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// wellFormed checks a property's type and patterns before the store sees them,
// answering the request itself when one is wrong.
//
// The store checks the same things and returns a plain error for each, which
// arrives here indistinguishable from a database failure. Deciding between a
// 400 and a 500 by reading an error's text is how a reworded message silently
// starts blaming the wrong side, so the two mistakes a caller can actually make
// are caught up front and everything the store still refuses is a fault on this
// side. The check is not the store's safety net: the CLI reaches the store
// without passing here, so the store keeps its own.
func (s *Server) wellFormed(w http.ResponseWriter, typ, regex, label string) bool {
	if typ != "" && !slices.Contains(store.PropertyTypes(), typ) {
		s.badRequest(w, fmt.Sprintf("type %q is not one of: %s",
			typ, strings.Join(store.PropertyTypes(), ", ")))
		return false
	}
	// A pattern that does not compile has to fail here rather than mid-crawl,
	// where it would look like a site that stopped publishing the field.
	for what, pat := range map[string]string{fieldRegex: regex, fieldLabel: label} {
		if pat == "" {
			continue
		}
		if _, err := regexp.Compile(pat); err != nil {
			s.badRequest(w, fmt.Sprintf("%s: %s", what, err))
			return false
		}
	}
	return true
}

// onSite renders a domain for a message, or nothing when there is not one.
func onSite(domain string) string {
	if domain == "" {
		return ""
	}
	return " on " + domain
}

// propertyPath is where a property answers, carrying the scope it was taught
// with so the Location header addresses the row that was written rather than
// the item-wide one that shares its name.
func propertyPath(item, prop, domain string) string {
	path := "/v1/items/" + url.PathEscape(item) + "/properties/" + url.PathEscape(prop)
	if domain != "" {
		path += "?on=" + url.QueryEscape(domain)
	}
	return path
}
