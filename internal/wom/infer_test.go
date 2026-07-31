// SPDX-License-Identifier: MIT

package wom_test

import (
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/wom"
)

const listingHTML = `<!doctype html>
<html><body>
  <h1>Used cars</h1>
  <div class="listings">
    <div class="vehicle">
      <span class="make">Toyota</span>
      <span class="model">Corolla</span>
      <span class="year">2019</span>
      <span class="fuel">Hybrid</span>
    </div>
    <div class="vehicle">
      <span class="make">Honda</span>
      <span class="model">Civic</span>
      <span class="year">2021</span>
      <span class="fuel">Petrol</span>
    </div>
    <div class="vehicle">
      <span class="make">Ford</span>
      <span class="model">Focus</span>
      <span class="year">2018</span>
      <span class="fuel">Diesel</span>
    </div>
  </div>
</body></html>`

func vehicleSchema() wom.Schema {
	return wom.Schema{{
		Name:        "vehicles",
		Aliases:     []string{"car"},
		Description: "Automotive vehicles",
		Props: []wom.Prop{
			{Name: "make", Type: wom.TypeString, Examples: []string{"Toyota"}},
			{Name: "model", Type: wom.TypeString},
			{Name: "year", Type: wom.TypeNumber},
			{Name: "fuel", Type: wom.TypeString},
		},
	}}
}

// findItem returns the named child of an item, or a zero Item.
func findItem(items []wom.Item, name string) (wom.Item, bool) {
	for _, it := range items {
		if it.Name == name {
			return it, true
		}
	}
	return wom.Item{}, false
}

func TestSchemaLocatesRepeatingRecord(t *testing.T) {
	t.Parallel()

	w := wom.New()
	if err := w.AddBody("https://example.com/cars", "text/html", []byte(listingHTML)); err != nil {
		t.Fatalf("AddBody: %v", err)
	}

	items, err := w.Schema(vehicleSchema()...)
	if err != nil {
		t.Fatalf("Schema: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("Schema returned no items")
	}

	vehicles, ok := findItem(items, "vehicles")
	if !ok {
		t.Fatalf("no \"vehicles\" item; got %s", itemsString(items))
	}
	t.Logf("result:\n%s", vehicles.Tree())

	// The container is the repeating .vehicle div, so the record should have
	// found all three instances.
	if vehicles.Support != 3 {
		t.Errorf("vehicles support = %d, want 3", vehicles.Support)
	}
	if !strings.Contains(vehicles.XPath, "div") {
		t.Errorf("vehicles xpath = %q, want it to address the repeating div", vehicles.XPath)
	}

	for _, field := range []struct{ name, want string }{
		{"make", "Toyota"},
		{"model", "Corolla"},
		{"year", "2019"},
		{"fuel", "Hybrid"},
	} {
		child, ok := findItem(vehicles.Items, field.name)
		if !ok {
			t.Errorf("no %q item under vehicles", field.name)
			continue
		}
		if child.Probability <= 0 || child.Probability > 1 {
			t.Errorf("%s probability = %v, want in (0,1]", field.name, child.Probability)
		}
		if !containsValue(child.Values, field.want) {
			t.Errorf("%s values = %v, want to include %q", field.name, child.Values, field.want)
		}
		// Fields are addressed relative to the record container.
		if child.XPath != "" && !strings.HasPrefix(child.XPath, ".") {
			t.Errorf("%s xpath = %q, want a container-relative path", field.name, child.XPath)
		}
	}
}

func TestSchemaGeneralizesAcrossPages(t *testing.T) {
	t.Parallel()

	w := wom.New()
	for _, path := range []string{"/cars/1", "/cars/2"} {
		if err := w.AddBody("https://example.com"+path, "text/html", []byte(listingHTML)); err != nil {
			t.Fatalf("AddBody %s: %v", path, err)
		}
	}

	items, err := w.Schema(vehicleSchema()...)
	if err != nil {
		t.Fatalf("Schema: %v", err)
	}
	vehicles, ok := findItem(items, "vehicles")
	if !ok {
		t.Fatalf("no \"vehicles\" item; got %s", itemsString(items))
	}

	// Six containers across two pages, addressed by one URI pattern that
	// generalizes the varying path segment.
	if vehicles.Support != 6 {
		t.Errorf("support = %d, want 6", vehicles.Support)
	}
	if !strings.Contains(vehicles.URI, "[^/]+") {
		t.Errorf("uri = %q, want the varying segment generalized", vehicles.URI)
	}
	if !strings.Contains(vehicles.URI, `example\.com`) {
		t.Errorf("uri = %q, want the shared host kept literal", vehicles.URI)
	}
}

func TestSchemaOnJSON(t *testing.T) {
	t.Parallel()

	const body = `{"vehicles":[
		{"make":"Toyota","model":"Corolla","year":2019,"fuel":"Hybrid"},
		{"make":"Honda","model":"Civic","year":2021,"fuel":"Petrol"}
	]}`

	w := wom.New()
	if err := w.AddBody("https://example.com/api/cars", "application/json", []byte(body)); err != nil {
		t.Fatalf("AddBody: %v", err)
	}

	items, err := w.Schema(vehicleSchema()...)
	if err != nil {
		t.Fatalf("Schema: %v", err)
	}
	vehicles, ok := findItem(items, "vehicles")
	if !ok {
		t.Fatalf("no \"vehicles\" item; got %s", itemsString(items))
	}
	t.Logf("result:\n%s", vehicles.Tree())

	if vehicles.Format != wom.FormatJSON {
		t.Errorf("format = %v, want json", vehicles.Format)
	}
	// JSON is addressed by JSONPath, not XPath.
	if vehicles.XPath != "" {
		t.Errorf("xpath = %q, want empty for json", vehicles.XPath)
	}
	if !strings.HasPrefix(vehicles.Path, "$") {
		t.Errorf("path = %q, want a JSONPath", vehicles.Path)
	}
	make, ok := findItem(vehicles.Items, "make")
	if !ok {
		t.Fatalf("no \"make\" item; got %s", itemsString(vehicles.Items))
	}
	if !containsValue(make.Values, "Toyota") {
		t.Errorf("make values = %v, want to include Toyota", make.Values)
	}
}

// TestSchemaOnPDF exercises the case the sequence model exists for: a PDF has
// no element tree, so a page is a flat run of lines and field order along that
// run is the only structural signal available.
func TestSchemaOnPDF(t *testing.T) {
	t.Parallel()

	body := buildPDF([][]string{
		{"Vehicle Specification", "Make: Toyota", "Model: Corolla", "Year: 2019", "Fuel: Hybrid"},
		{"Vehicle Specification", "Make: Honda", "Model: Civic", "Year: 2021", "Fuel: Petrol"},
	})

	w := wom.New()
	if err := w.AddBody("https://example.com/specs.pdf", "application/pdf", body); err != nil {
		t.Fatalf("AddBody: %v", err)
	}

	items, err := w.Schema(vehicleSchema()...)
	if err != nil {
		t.Fatalf("Schema: %v", err)
	}
	vehicles, ok := findItem(items, "vehicles")
	if !ok {
		t.Fatalf("no \"vehicles\" item; got %s", itemsString(items))
	}
	t.Logf("result:\n%s", vehicles.Tree())

	if vehicles.Format != wom.FormatPDF {
		t.Errorf("format = %v, want pdf", vehicles.Format)
	}
	// PDFs are addressed only by their native path dialect.
	if vehicles.XPath != "" || vehicles.Selector != "" {
		t.Errorf("xpath = %q, selector = %q, want both empty for pdf",
			vehicles.XPath, vehicles.Selector)
	}
	if !strings.HasPrefix(vehicles.Path, "page") {
		t.Errorf("path = %q, want a page path", vehicles.Path)
	}

	// Each field should land on its own line rather than all collapsing onto
	// one, which is what the chain buys over per-node scoring.
	seen := map[string]string{}
	for _, child := range vehicles.Items {
		if len(child.Values) > 0 {
			seen[child.Name] = child.Values[0]
		}
	}
	if len(seen) < 2 {
		t.Fatalf("located %d fields, want at least 2: %v", len(seen), seen)
	}
	for name, want := range map[string]string{
		"make":  "Toyota",
		"model": "Corolla",
		"year":  "2019",
		"fuel":  "Hybrid",
	} {
		if got, ok := seen[name]; ok && !strings.Contains(got, want) {
			t.Errorf("%s located line %q, want it to contain %q", name, got, want)
		}
	}
}

func TestSchemaRejectsBadInput(t *testing.T) {
	t.Parallel()

	w := wom.New()
	if err := w.AddBody("https://example.com/", "text/html", []byte(listingHTML)); err != nil {
		t.Fatalf("AddBody: %v", err)
	}

	if _, err := w.Schema(); err == nil {
		t.Error("Schema() with no props: want error")
	}
	if _, err := w.Schema(wom.Prop{Name: ""}); err == nil {
		t.Error("Schema() with unnamed prop: want error")
	}
	if _, err := w.Schema(wom.Prop{Name: "a"}, wom.Prop{Name: "A"}); err == nil {
		t.Error("Schema() with duplicate props: want error")
	}
	if _, err := w.Schema(wom.Prop{Name: "a", Type: "nonsense"}); err == nil {
		t.Error("Schema() with unknown type: want error")
	}
}

func TestSchemaEmptyGraph(t *testing.T) {
	t.Parallel()

	items, err := wom.New().Schema(vehicleSchema()...)
	if err != nil {
		t.Fatalf("Schema: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("items = %v, want none from an empty graph", items)
	}
}

func containsValue(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func itemsString(items []wom.Item) string {
	var b strings.Builder
	for _, it := range items {
		b.WriteString(it.Tree())
	}
	if b.Len() == 0 {
		return "(none)"
	}
	return b.String()
}
