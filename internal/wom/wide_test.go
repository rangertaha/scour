// SPDX-License-Identifier: MIT

package wom_test

import (
	"testing"

	"github.com/rangertaha/scour/internal/wom"
)

// The page publishes three of the fields a full vehicle schema describes,
// which is the ordinary case: no site carries every field anyone might want.
const carPage = `<html><body><div id="root"><h1>Ford F-150</h1>
<div class="price">$42,000</div><div class="year">2026</div>
<a href="/">back</a></div></body></html>`

func carCorpus(t *testing.T) *wom.WOM {
	t.Helper()

	w := wom.New()
	for _, u := range []string{
		"http://example.com/cars/1/",
		"http://example.com/cars/2/",
		"http://example.com/cars/3/",
	} {
		if err := w.AddBody(u, "text/html", []byte(carPage)); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

func narrowProps() []wom.Prop {
	return []wom.Prop{
		{Name: "make", Examples: []string{"Ford"}},
		{Name: "model", Examples: []string{"F-150"}},
		{Name: "price", Type: "number", Examples: []string{"$42,000"}},
	}
}

// wideProps is the narrow schema plus five fields this site never publishes,
// which is what applying a general template to a specific site looks like.
func wideProps() []wom.Prop {
	return append(narrowProps(),
		wom.Prop{Name: "mileage", Type: "number", Aliases: []string{"odometer"}, Examples: []string{"42,000"}},
		wom.Prop{Name: "vin", Aliases: []string{"chassis number"}, Examples: []string{"1HGBH41JXMN109186"}},
		wom.Prop{Name: "body", Aliases: []string{"body style"}, Examples: []string{"Sedan"}},
		wom.Prop{Name: "fuel", Aliases: []string{"fuel type"}, Examples: []string{"Diesel"}},
		wom.Prop{Name: "transmission", Aliases: []string{"gearbox"}, Examples: []string{"Manual"}},
	)
}

func locate(t *testing.T, props []wom.Prop) []wom.Item {
	t.Helper()

	items, err := carCorpus(t).Schema(wom.Prop{Name: "car", Props: props})
	if err != nil {
		t.Fatal(err)
	}
	return items
}

// A schema describing fields a site does not publish must not cost the fields
// it does publish.
//
// Coverage was once the share of declared fields that were located, applied as
// a straight multiplier. A nine-field schema that recovered three fields
// therefore kept a quarter of its confidence and fell under the threshold, so
// it located nothing at all on a site where a three-field schema located
// everything. That is backwards: it means the more carefully a schema is
// written, the less it finds.
func TestAWideSchemaStillLocatesTheFieldsThatArePresent(t *testing.T) {
	wide := locate(t, wideProps())
	if len(wide) == 0 {
		t.Fatal("a schema wider than the site located nothing at all")
	}

	var fields int
	for _, item := range wide {
		fields += len(item.Items)
	}
	if fields < 2 {
		t.Errorf("located %d fields, want the ones the page actually carries", fields)
	}
}

// The discount still has to exist, or coverage would carry no signal.
func TestAWiderSchemaIsLessConfident(t *testing.T) {
	narrow := locate(t, narrowProps())
	wide := locate(t, wideProps())

	if len(narrow) == 0 || len(wide) == 0 {
		t.Fatalf("narrow located %d items, wide %d", len(narrow), len(wide))
	}

	if wide[0].Probability >= narrow[0].Probability {
		t.Errorf("wide %.3f is not below narrow %.3f; recovering less of a record should still mean less confidence",
			wide[0].Probability, narrow[0].Probability)
	}
}
