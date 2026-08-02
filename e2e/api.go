// SPDX-License-Identifier: GPL-3.0-or-later

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Product is one item in the catalogue the REST API serves.
//
// It carries links as well as fields, because a JSON document is a page like
// any other: something has to be able to get from a search result to the thing
// it found, and from the thing to the HTML that describes it.
type Product struct {
	SKU          string   `json:"sku"`
	Name         string   `json:"name"`
	Brand        string   `json:"brand"`
	Price        float64  `json:"price"`
	Currency     string   `json:"currency"`
	Availability string   `json:"availability"`
	Rating       float64  `json:"rating"`
	Tags         []string `json:"tags"`
	// Self is this product's own API address, Page the HTML about it, and
	// Related a sibling. All three are links out of a JSON body.
	Self    string `json:"self"`
	Page    string `json:"page"`
	Related string `json:"related,omitempty"`
}

// The vocabulary the fixture shares. A kind misspelled in the index and not in
// the filter is a search that silently returns nothing.
const (
	KindArticle  = "article"
	KindLongform = "longform"
	KindProduct  = "product"
	KindPerson   = "person"
	KindPlace    = "place"

	// GBP is the only currency here, named so the five rows cannot disagree.
	GBP = "GBP"
	// The availability values, which the catalogue and the in_stock filter both
	// have to spell the same way.
	InStock    = "InStock"
	OutOfStock = "OutOfStock"
	PreOrder   = "PreOrder"

	// PageHarbour and PageProducts are linked from more than one place.
	PageHarbour  = "/places/harbour.html"
	PageProducts = "/products/"
)

// catalogue is small on purpose. A search fixture wants enough rows to filter,
// sort and paginate over, and no more than can be read in one screen when a
// test says the wrong one came back.
var catalogue = []Product{
	{SKU: "FS-K2-0442", Name: "Kestrel 2 Trail Shoe", Brand: "Fellstride", Price: 129.00,
		Currency: GBP, Availability: InStock, Rating: 4.4,
		Tags: []string{"footwear", "trail"}, Page: "/products/ordinary.html"},
	{SKU: "FS-HD-1180", Name: "Harrier Down Jacket", Brand: "Fellstride", Price: 249.00,
		Currency: GBP, Availability: OutOfStock, Rating: 4.7,
		Tags: []string{"insulation", "winter"}, Page: "/products/json-ld.html"},
	{SKU: "MW-TT-0031", Name: "Tarn Tarp 2P", Brand: "Moorwind", Price: 89.50,
		Currency: GBP, Availability: InStock, Rating: 4.1,
		Tags: []string{"shelter", "camping"}, Page: PageProducts},
	{SKU: "MW-GG-0208", Name: "Gale Gloves", Brand: "Moorwind", Price: 34.00,
		Currency: GBP, Availability: PreOrder, Rating: 3.8,
		Tags: []string{"gloves", "winter"}, Page: PageProducts},
	{SKU: "AC-QB-7710", Name: "Quay Belt Pack", Brand: "Ardmore Co", Price: 42.00,
		Currency: GBP, Availability: InStock, Rating: 4.0,
		Tags: []string{"packs"}, Page: PageProducts},
}

// withLinks fills in the addresses that depend on where the product sits, so
// the catalogue above stays a list of facts rather than a list of URLs.
func withLinks(p Product, i int) Product {
	p.Self = "/api/products/" + p.SKU
	if i+1 < len(catalogue) {
		p.Related = "/api/products/" + catalogue[i+1].SKU
	}
	return p
}

// registerAPI adds the product search and detail endpoints.
//
// It is a REST API rather than a file because the questions worth asking of it
// are questions with parameters: what matches, in what order, how many at a
// time. A fixture that can only answer one of those teaches a crawler nothing
// about the rest.
func registerAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/products", searchProducts)
	mux.HandleFunc("GET /api/products/{sku}", getProduct)
}

// searchProducts filters, sorts and pages the catalogue.
//
//	?q=          matches the name, brand, sku or a tag
//	?brand=      exact, case-insensitive
//	?in_stock=   true keeps only what can be bought now
//	?sort=       price, -price, rating, -rating, name
//	?limit=      how many, default 10
//	?offset=     where to start
func searchProducts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	matches := make([]Product, 0, len(catalogue))

	term := strings.ToLower(strings.TrimSpace(q.Get("q")))
	brand := strings.ToLower(strings.TrimSpace(q.Get("brand")))
	inStock := q.Get("in_stock") == "true"

	for i, p := range catalogue {
		if term != "" && !productMatches(p, term) {
			continue
		}
		if brand != "" && strings.ToLower(p.Brand) != brand {
			continue
		}
		if inStock && p.Availability != InStock {
			continue
		}
		matches = append(matches, withLinks(p, i))
	}

	sortProducts(matches, q.Get("sort"))

	total := len(matches)
	offset := intParam(q.Get("offset"), 0)
	limit := intParam(q.Get("limit"), 10)
	if offset > len(matches) {
		offset = len(matches)
	}
	matches = matches[offset:]
	if limit > 0 && limit < len(matches) {
		matches = matches[:limit]
	}

	// The next page is a link rather than a number the caller has to compute,
	// which is the difference between an API a crawler can walk and one it
	// has to be taught.
	body := map[string]any{
		"total": total, "offset": offset, "limit": limit,
		"products": matches,
		"self":     r.URL.RequestURI(),
	}
	if next := offset + limit; limit > 0 && next < total {
		nextQuery := cloneQuery(q)
		nextQuery.Set("offset", strconv.Itoa(next))
		body["next"] = "/api/products?" + nextQuery.Encode()
	}
	writeJSON(w, http.StatusOK, body)
}

// getProduct is one product, by sku.
func getProduct(w http.ResponseWriter, r *http.Request) {
	sku := strings.ToUpper(r.PathValue("sku"))
	for i, p := range catalogue {
		if strings.ToUpper(p.SKU) == sku {
			writeJSON(w, http.StatusOK, map[string]any{KindProduct: withLinks(p, i)})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]any{
		"error": fmt.Sprintf("no product %q", sku),
		"index": "/api/products",
	})
}

func productMatches(p Product, term string) bool {
	haystack := strings.ToLower(p.Name + " " + p.Brand + " " + p.SKU + " " + strings.Join(p.Tags, " "))
	return strings.Contains(haystack, term)
}

func sortProducts(ps []Product, by string) {
	switch by {
	case "price":
		sort.SliceStable(ps, func(i, j int) bool { return ps[i].Price < ps[j].Price })
	case "-price":
		sort.SliceStable(ps, func(i, j int) bool { return ps[i].Price > ps[j].Price })
	case "rating":
		sort.SliceStable(ps, func(i, j int) bool { return ps[i].Rating < ps[j].Rating })
	case "-rating":
		sort.SliceStable(ps, func(i, j int) bool { return ps[i].Rating > ps[j].Rating })
	case "name":
		sort.SliceStable(ps, func(i, j int) bool { return ps[i].Name < ps[j].Name })
	}
}

func intParam(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return fallback
	}
	return n
}

func cloneQuery(q url.Values) url.Values {
	out := make(url.Values, len(q))
	for k, v := range q {
		out[k] = append([]string(nil), v...)
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(body)
}
