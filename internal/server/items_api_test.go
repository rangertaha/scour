// SPDX-License-Identifier: GPL-3.0-or-later

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/store"
)

// part runs one request against the sub-resource routes alone.
//
// They are wired into the same mux as everything else in the running server,
// but server.go is not this file's to edit, so the tests build a mux of their
// own out of the one registration function. What that costs is the middleware,
// which the tests in server_test.go already cover, and what it buys is that a
// route these tests pass against is a route registerItemParts really declares
// rather than one a test helper invented.
func part(t *testing.T, srv *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	srv.registerItemParts(mux)

	var r *http.Request
	if body == "" {
		r = httptest.NewRequestWithContext(t.Context(), method, path, nil)
	} else {
		r = httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// item creates an item to hang sub-resources off, through the store rather than
// through POST /v1/items: these tests are about the parts, and routing the
// setup through a route they do not register would tie them to it.
func item(t *testing.T, srv *Server, name string) *store.Item {
	t.Helper()

	created, err := srv.store.CreateItem(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

// words reads a list of strings out of an envelope under one key.
func words(t *testing.T, w *httptest.ResponseRecorder, key string) []string {
	t.Helper()

	var envelope map[string][]string
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	got, ok := envelope[key]
	if !ok {
		t.Fatalf("no %q in the response: %s", key, w.Body.String())
	}
	return got
}

// Creating a property is one act and correcting one is another, and the design
// gives them separate verbs so a client can tell which it is doing.
func TestAPropertyIsCreatedOnceAndPatchedAfter(t *testing.T) {
	srv := newServer(t, nil)
	item(t, srv, "vehicle")

	w := part(t, srv, http.MethodPost, "/v1/items/vehicle/properties",
		`{"name":"price","type":"number","example":"$42,000"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", w.Code, w.Body)
	}
	if loc := w.Header().Get("Location"); loc != "/v1/items/vehicle/properties/price" {
		t.Errorf("Location = %q, want the property's address", loc)
	}

	// The second POST is the same property, which PATCH is for.
	again := part(t, srv, http.MethodPost, "/v1/items/vehicle/properties", `{"name":"price"}`)
	if again.Code != http.StatusConflict {
		t.Errorf("a repeated create = %d, want 409: %s", again.Code, again.Body)
	}

	patched := part(t, srv, http.MethodPatch, "/v1/items/vehicle/properties/price",
		`{"example":"$8,500"}`)
	if patched.Code != http.StatusOK {
		t.Fatalf("patch = %d: %s", patched.Code, patched.Body)
	}

	prop, err := srv.store.Property(t.Context(), item(t, srv, "vehicle").ID, "", "price")
	if err != nil {
		t.Fatal(err)
	}
	if prop.Example != "$8,500" {
		t.Errorf("example = %q, want the patched one", prop.Example)
	}
	// The type was never mentioned by the patch, so it must have survived it.
	if prop.Type != "number" {
		t.Errorf("type = %q, want number: a patch must not cost a property what it knew", prop.Type)
	}
}

// The row of the parity table that changes shape rather than collapsing:
// clearing a detail keeps the property, and removing the property does not.
func TestClearingADetailIsNotRemovingTheProperty(t *testing.T) {
	srv := newServer(t, nil)
	vehicle := item(t, srv, "vehicle")

	if w := part(t, srv, http.MethodPost, "/v1/items/vehicle/properties",
		`{"name":"price","type":"number","example":"$42,000"}`); w.Code != http.StatusCreated {
		t.Fatal(w.Body.String())
	}

	// Null clears the field and keeps everything else the property knows.
	w := part(t, srv, http.MethodPatch, "/v1/items/vehicle/properties/price", `{"example":null}`)
	if w.Code != http.StatusOK {
		t.Fatalf("clearing = %d: %s", w.Code, w.Body)
	}
	prop, err := srv.store.Property(t.Context(), vehicle.ID, "", "price")
	if err != nil {
		t.Fatalf("the property went with its example: %v", err)
	}
	if prop.Example != "" {
		t.Errorf("example = %q, want it emptied", prop.Example)
	}
	if prop.Type != "number" {
		t.Errorf("type = %q, want number: clearing one detail took another", prop.Type)
	}

	// DELETE is the other act, and it does take the property.
	if w := part(t, srv, http.MethodDelete, "/v1/items/vehicle/properties/price", ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204: %s", w.Code, w.Body)
	}
	if _, err := srv.store.Property(t.Context(), vehicle.ID, "", "price"); err == nil {
		t.Error("the property is still there after a DELETE")
	}
}

// A patch that mentions nothing changes nothing, which is what an absent key
// has to mean if null is to mean anything.
func TestAnAbsentFieldIsNotAClear(t *testing.T) {
	srv := newServer(t, nil)
	vehicle := item(t, srv, "vehicle")

	if w := part(t, srv, http.MethodPost, "/v1/items/vehicle/properties",
		`{"name":"price","example":"$42,000","description":"what it costs"}`); w.Code != http.StatusCreated {
		t.Fatal(w.Body.String())
	}
	if w := part(t, srv, http.MethodPatch, "/v1/items/vehicle/properties/price",
		`{"description":null}`); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}

	prop, err := srv.store.Property(t.Context(), vehicle.ID, "", "price")
	if err != nil {
		t.Fatal(err)
	}
	if prop.Description != "" {
		t.Errorf("description = %q, want it cleared", prop.Description)
	}
	if prop.Example != "$42,000" {
		t.Errorf("example = %q, want it untouched: it was never mentioned", prop.Example)
	}
}

// A patch cannot invent the property it was asked to change, or a misspelled
// name in the path becomes a second property instead of an error.
func TestPatchingAPropertyThatIsNotThere(t *testing.T) {
	srv := newServer(t, nil)
	item(t, srv, "vehicle")

	w := part(t, srv, http.MethodPatch, "/v1/items/vehicle/properties/prcie", `{"example":"x"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", w.Code, w.Body)
	}
}

// A value the engine does not know is the caller's mistake, and it has to fail
// on the request that made it rather than at train time as a complaint about a
// property nobody has touched since.
func TestABadTypeOrPatternIsRejected(t *testing.T) {
	srv := newServer(t, nil)
	item(t, srv, "vehicle")

	for _, body := range []string{
		`{"name":"price","type":"currency"}`,
		`{"name":"price","regex":"([unclosed"}`,
		`{"name":""}`,
	} {
		if w := part(t, srv, http.MethodPost, "/v1/items/vehicle/properties", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400: %s", body, w.Code, w.Body)
		}
	}
}

// An alias is added, declared and dropped through three verbs, because adding
// and declaring the whole set are different things a client has to be able to
// ask for separately.
func TestTheFourVerbsOnAnItemsAliases(t *testing.T) {
	srv := newServer(t, nil)
	item(t, srv, "vehicle")

	if w := part(t, srv, http.MethodPost, "/v1/items/vehicle/aliases",
		`{"words":["car","pickup truck"]}`); w.Code != http.StatusOK {
		t.Fatalf("post = %d: %s", w.Code, w.Body)
	}

	got := words(t, part(t, srv, http.MethodGet, "/v1/items/vehicle/aliases", ""), "aliases")
	if len(got) != 2 {
		t.Fatalf("aliases = %v, want both words kept whole", got)
	}
	// A phrase is one word, so nothing was split on the space in it.
	if got[1] != "pickup truck" {
		t.Errorf("aliases = %v, want the phrase intact", got)
	}

	if w := part(t, srv, http.MethodDelete, "/v1/items/vehicle/aliases/car", ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204: %s", w.Code, w.Body)
	}
	// Removing a word that was not there is a miss, because reporting a removal
	// that removed nothing is how someone believes a word is gone while a crawl
	// still matches on it.
	if w := part(t, srv, http.MethodDelete, "/v1/items/vehicle/aliases/car", ""); w.Code != http.StatusNotFound {
		t.Errorf("removing it twice = %d, want 404: %s", w.Code, w.Body)
	}

	after := words(t, part(t, srv, http.MethodPut, "/v1/items/vehicle/aliases",
		`{"words":["automobile"]}`), "aliases")
	if len(after) != 1 || after[0] != "automobile" {
		t.Errorf("after PUT = %v, want exactly what was declared", after)
	}
}

// An item's aliases have no domain, so scoping one is refused rather than
// ignored: a 200 would tell the caller an edit was confined to one site when it
// had been applied to all of them.
func TestScopingAnItemsAliasesIsRefused(t *testing.T) {
	srv := newServer(t, nil)
	item(t, srv, "vehicle")

	w := part(t, srv, http.MethodGet, "/v1/items/vehicle/aliases?on=example.com", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body)
	}
}

// A property's words do have a domain, which is the whole point: what one site
// calls a byline must not overwrite what the next one calls it.
func TestPropertyLabelsAreScopedPerSite(t *testing.T) {
	srv := newServer(t, nil)
	item(t, srv, "news")

	if w := part(t, srv, http.MethodPost, "/v1/items/news/properties", `{"name":"author"}`); w.Code != http.StatusCreated {
		t.Fatal(w.Body.String())
	}
	if w := part(t, srv, http.MethodPost, "/v1/items/news/properties/author/labels",
		`{"words":["byline"]}`); w.Code != http.StatusOK {
		t.Fatalf("post = %d: %s", w.Code, w.Body)
	}
	if w := part(t, srv, http.MethodPost, "/v1/items/news/properties/author/labels?on=www.example.com",
		`{"words":["staff writer"]}`); w.Code != http.StatusOK {
		t.Fatalf("scoped post = %d: %s", w.Code, w.Body)
	}

	// The domain is normalised, so www.example.com and example.com are the one
	// site the teaching was meant for.
	scoped := words(t, part(t, srv, http.MethodGet,
		"/v1/items/news/properties/author/labels?on=example.com", ""), "labels")
	if len(scoped) != 1 || scoped[0] != "staff writer" {
		t.Errorf("on example.com = %v, want only what that site taught", scoped)
	}

	unscoped := words(t, part(t, srv, http.MethodGet,
		"/v1/items/news/properties/author/labels", ""), "labels")
	if len(unscoped) != 1 || unscoped[0] != "byline" {
		t.Errorf("unscoped = %v, want the default untouched by the site's teaching", unscoped)
	}

	// Declaring the set on one site leaves every other site alone.
	if w := part(t, srv, http.MethodPut, "/v1/items/news/properties/author/labels?on=example.com",
		`{"words":["contributor"]}`); w.Code != http.StatusOK {
		t.Fatalf("scoped put = %d: %s", w.Code, w.Body)
	}
	still := words(t, part(t, srv, http.MethodGet,
		"/v1/items/news/properties/author/labels", ""), "labels")
	if len(still) != 1 || still[0] != "byline" {
		t.Errorf("unscoped = %v, want a scoped PUT to have left it alone", still)
	}
}

// Removing a property on one site leaves the schema every other site is read
// with intact.
func TestRemovingAScopedPropertyLeavesTheDefault(t *testing.T) {
	srv := newServer(t, nil)
	news := item(t, srv, "news")

	for _, path := range []string{
		"/v1/items/news/properties",
		"/v1/items/news/properties?on=example.com",
	} {
		if w := part(t, srv, http.MethodPost, path, `{"name":"author"}`); w.Code != http.StatusCreated {
			t.Fatalf("%s = %d: %s", path, w.Code, w.Body)
		}
	}

	if w := part(t, srv, http.MethodDelete,
		"/v1/items/news/properties/author?on=example.com", ""); w.Code != http.StatusNoContent {
		t.Fatalf("scoped delete = %d, want 204: %s", w.Code, w.Body)
	}
	if _, err := srv.store.Property(t.Context(), news.ID, "", "author"); err != nil {
		t.Errorf("the default went with the site's: %v", err)
	}
	if _, err := srv.store.Property(t.Context(), news.ID, "example.com", "author"); err == nil {
		t.Error("the scoped property is still there")
	}
}

// The words of a property that was never taught are a miss, so all four verbs
// fail the same way on a name that is not there.
func TestLabelsOfAnUnknownProperty(t *testing.T) {
	srv := newServer(t, nil)
	item(t, srv, "news")

	w := part(t, srv, http.MethodGet, "/v1/items/news/properties/nosuch/labels", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", w.Code, w.Body)
	}
}

// Templates belong to no item, so they are read without naming one.
func TestTemplatesAreReadWithoutAnItem(t *testing.T) {
	srv := newServer(t, nil)

	w := part(t, srv, http.MethodGet, "/v1/templates", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}

	var out templatesOut
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Templates) == 0 {
		t.Fatalf("no templates ship in the binary: %s", w.Body.String())
	}
	for _, tpl := range out.Templates {
		if tpl.Name == "" || len(tpl.Fields) == 0 {
			t.Errorf("template %+v has nothing to start an item from", tpl)
		}
	}
}

// A model is a resource, and one that has never been trained does not exist
// yet. The miss says how to make it, since that is the only useful next move.
func TestAnUntrainedModelIsAMiss(t *testing.T) {
	srv := newServer(t, nil)
	item(t, srv, "vehicle")

	w := part(t, srv, http.MethodGet, "/v1/items/vehicle/model", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "model/runs") {
		t.Errorf("the miss does not say how to train one: %s", w.Body.String())
	}
}

// The counts are the point of the model view: a model trained on forty pages
// and one trained on twelve hundred are the same file with very different
// standing.
func TestTheModelViewCarriesWhatItWasTrainedFrom(t *testing.T) {
	srv := newServer(t, nil)
	vehicle := item(t, srv, "vehicle")

	if err := srv.store.SaveModelMeta(t.Context(), store.ModelMeta{
		ItemID: vehicle.ID, Algorithm: "logistic", Accuracy: 0.91, Observations: 412,
	}); err != nil {
		t.Fatal(err)
	}

	w := part(t, srv, http.MethodGet, "/v1/items/vehicle/model", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}

	var envelope struct {
		Model modelSummary `json:"model"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if envelope.Model.Item != "vehicle" || envelope.Model.Algorithm != "logistic" {
		t.Errorf("model = %+v, want the item and what fitted it", envelope.Model)
	}
	if envelope.Model.Observations != 412 {
		t.Errorf("observations = %d, want what it was fitted from", envelope.Model.Observations)
	}
}

// Rules are induced per format, because a feed and the HTML it links to are not
// the same shape. A listing that did not say which format a rule came from
// would show two sets of XPaths that look like they contradict each other.
func TestRulesSayWhichFormatTheyCameFrom(t *testing.T) {
	srv := newServer(t, nil)
	vehicle := item(t, srv, "vehicle")

	if err := srv.store.ReplaceRules(t.Context(), vehicle.ID, []store.Rule{
		{Format: "text/html", Prop: "vehicle", XPath: "//div[@class='car']",
			URIPattern: `https://example\.com/cars/[^/]+`, Probability: 0.9},
		{Format: "application/rss+xml", Prop: "vehicle", XPath: "//item",
			URIPattern: `https://other\.example/feed`, Probability: 0.8},
	}); err != nil {
		t.Fatal(err)
	}

	w := part(t, srv, http.MethodGet, "/v1/items/vehicle/model/rules", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}

	var envelope struct {
		Rules   []ruleOut      `json:"rules"`
		Total   int            `json:"total"`
		Formats map[string]int `json:"formats"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if envelope.Total != 2 {
		t.Fatalf("total = %d, want both rules: %s", envelope.Total, w.Body.String())
	}
	for _, rule := range envelope.Rules {
		if rule.Format == "" {
			t.Errorf("rule %+v does not say which format it was induced from", rule)
		}
	}
	if envelope.Formats["text/html"] != 1 || envelope.Formats["application/rss+xml"] != 1 {
		t.Errorf("formats = %v, want one of each", envelope.Formats)
	}
}

// Scoping the listing to a site reads the site back out of the rule's URI
// pattern, whose dots are escaped because it is a regex.
func TestRulesCanBeScopedToOneSite(t *testing.T) {
	srv := newServer(t, nil)
	vehicle := item(t, srv, "vehicle")

	if err := srv.store.ReplaceRules(t.Context(), vehicle.ID, []store.Rule{
		{Format: "text/html", Prop: "vehicle", URIPattern: `https://example\.com/cars/[^/]+`},
		{Format: "text/html", Prop: "vehicle", URIPattern: `https://other\.example/cars/[^/]+`},
	}); err != nil {
		t.Fatal(err)
	}

	w := part(t, srv, http.MethodGet, "/v1/items/vehicle/model/rules?on=example.com", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	var envelope struct {
		Rules []ruleOut `json:"rules"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(envelope.Rules) != 1 {
		t.Fatalf("rules on example.com = %d, want the one addressed there: %s",
			len(envelope.Rules), w.Body.String())
	}
}

// A child rule pulls one property out of a record its parent located and can
// carry no pattern of its own, so filtering by site has to follow the parent
// chain or it returns the containers and drops every field inside them.
func TestScopedRulesKeepTheirChildren(t *testing.T) {
	srv := newServer(t, nil)
	vehicle := item(t, srv, "vehicle")

	parent := uint(0)
	if err := srv.store.ReplaceRules(t.Context(), vehicle.ID, []store.Rule{
		{Format: "text/html", Prop: "vehicle", URIPattern: `https://example\.com/cars/[^/]+`},
		{Format: "text/html", Prop: "price", ParentID: &parent},
	}); err != nil {
		t.Fatal(err)
	}

	w := part(t, srv, http.MethodGet, "/v1/items/vehicle/model/rules?on=example.com", "")
	var envelope struct {
		Rules []ruleOut `json:"rules"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(envelope.Rules) != 2 {
		t.Fatalf("rules = %d, want the container and the field inside it: %s",
			len(envelope.Rules), w.Body.String())
	}
}

// Discarding the model takes the rules with it, because they are what training
// produced, and leaves the marks, because a person made them.
func TestRemovingTheModelKeepsTheMarks(t *testing.T) {
	srv := newServer(t, nil)
	vehicle := item(t, srv, "vehicle")

	if err := srv.store.SaveModelMeta(t.Context(), store.ModelMeta{
		ItemID: vehicle.ID, Algorithm: "logistic",
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.ReplaceRules(t.Context(), vehicle.ID, []store.Rule{
		{Format: "text/html", Prop: "vehicle"},
	}); err != nil {
		t.Fatal(err)
	}

	if w := part(t, srv, http.MethodDelete, "/v1/items/vehicle/model", ""); w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", w.Code, w.Body)
	}

	rules, err := srv.store.Rules(t.Context(), vehicle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 0 {
		t.Errorf("rules = %d, want them gone with the model", len(rules))
	}
	if w := part(t, srv, http.MethodGet, "/v1/items/vehicle/model", ""); w.Code != http.StatusNotFound {
		t.Errorf("the model reads back as %d after a delete: %s", w.Code, w.Body)
	}
}

// Every route on these paths names an item, and one that is not there is the
// caller's business rather than the server's failure.
func TestAnUnknownItemIsAMissEverywhere(t *testing.T) {
	srv := newServer(t, nil)

	for _, probe := range []struct{ method, path string }{
		{http.MethodGet, "/v1/items/nosuch/aliases"},
		{http.MethodPost, "/v1/items/nosuch/properties"},
		{http.MethodGet, "/v1/items/nosuch/properties/price/labels"},
		{http.MethodGet, "/v1/items/nosuch/model"},
		{http.MethodGet, "/v1/items/nosuch/model/rules"},
		{http.MethodDelete, "/v1/items/nosuch/model"},
	} {
		body := ""
		if probe.method == http.MethodPost {
			body = `{"name":"price"}`
		}
		if w := part(t, srv, probe.method, probe.path, body); w.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404: %s", probe.method, probe.path, w.Code, w.Body)
		}
	}
}

// A typo in a client's body fails loudly rather than silently doing nothing.
func TestUnknownFieldsAreRefused(t *testing.T) {
	srv := newServer(t, nil)
	item(t, srv, "vehicle")

	// Plural where the field is singular, which is the shape a real typo takes:
	// close enough to look right in a client and wrong enough to do nothing.
	w := part(t, srv, http.MethodPost, "/v1/items/vehicle/properties",
		`{"name":"price","examples":"$42,000"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400: %s", w.Code, w.Body)
	}
}
