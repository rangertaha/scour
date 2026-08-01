// SPDX-License-Identifier: MIT

package wom_test

import (
	"fmt"
	"testing"

	"github.com/rangertaha/scour/internal/wom"
)

// oneBrandCatalogue is a shop that sells one make. Every page carries the same
// brand, which is a fact about the shop and also a real field of every product.
func oneBrandCatalogue(n int) []string {
	pages := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		pages = append(pages, fmt.Sprintf(`<html><head>
  <meta property="og:title" content="Widget %d">
  <meta itemprop="brand" content="Acme">
  <meta itemprop="price" content="%d.00">
</head><body><h1>Widget %d</h1></body></html>`, i, 10+i, i))
	}
	return pages
}

// A field whose value never changes is discounted, because a field usually
// describes its record and so varies from one to the next. On the news corpus
// that is what caught `section` resolving to a related-articles heading.
//
// It is a belief about the data, though, and plenty of corpora break it: a shop
// that sells one make publishes the same brand on every page, and brand is a
// real field of every product. The discount must therefore stay a discount. A
// field the markup names outright has to survive it, or a generic engine would
// be refusing to read a site for being consistent.
func TestAConstantFieldSurvivesTheMonotonyDiscount(t *testing.T) {
	t.Parallel()

	w := wom.New()
	for i, page := range oneBrandCatalogue(8) {
		if err := w.AddBody(fmt.Sprintf("https://shop.example/p/%d", i), "text/html", []byte(page)); err != nil {
			t.Fatal(err)
		}
	}

	items, err := w.Schema(wom.Schema{{
		Name: "product",
		Props: []wom.Prop{
			{Name: "title", Type: wom.TypeString},
			{Name: "brand", Type: wom.TypeString, Examples: []string{"Acme"}},
			{Name: "price", Type: wom.TypeNumber},
		},
	}}...)
	if err != nil {
		t.Fatalf("Schema: %v", err)
	}

	var brand, price wom.Item
	var walk func([]wom.Item)
	walk = func(in []wom.Item) {
		for _, it := range in {
			switch it.Name {
			case "brand":
				brand = it
			case "price":
				price = it
			}
			walk(it.Items)
		}
	}
	walk(items)

	if brand.Support == 0 {
		t.Fatal("brand was not located at all: a constant field is still a field")
	}
	if len(brand.Values) != 1 || brand.Values[0] != "Acme" {
		t.Errorf("brand read %v, want just Acme", brand.Values)
	}
	// Eight records, one distinct value, and still confident: the discount
	// weighs against a location, it does not veto one the markup has named.
	if brand.Probability < 0.5 {
		t.Errorf("brand probability = %.3f, too low for a field named by itemprop", brand.Probability)
	}
	if price.Support != brand.Support {
		t.Errorf("brand covered %d records and price %d; both are on every page",
			brand.Support, price.Support)
	}
}
