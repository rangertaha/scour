// SPDX-License-Identifier: GPL-3.0-or-later

package source_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclparse"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/classify"
	"github.com/rangertaha/scour/internal/classify/bayes"
	"github.com/rangertaha/scour/internal/classify/source"
	"github.com/rangertaha/scour/internal/classify/store"
	"github.com/rangertaha/scour/internal/plugin"

	_ "github.com/rangertaha/scour/internal/classify/bayes"
)

// trained puts a small real model in a directory, so what is fetched is a model
// and not a fixture shaped like one.
func trained(t *testing.T, dir string) {
	t.Helper()

	model, err := bayes.Train("climate", 7, []bayes.Document{
		{Text: "The climate committee criticised the pace of decarbonisation.", About: true},
		{Text: "Emissions from shipping must fall to meet the carbon budget.", About: true},
		{Text: "The manager named an unchanged squad for the cup fixture.", About: false},
		{Text: "Shares fell after the company cut its quarterly guidance.", About: false},
	})
	if err != nil {
		t.Fatalf("train: %v", err)
	}
	encoded, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}

	topics, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := topics.Put("bayes", classify.Config{Name: "climate", Version: 7, Model: encoded}); err != nil {
		t.Fatal(err)
	}
}

// config is what the plugin loader hands a middleware.
func config(t *testing.T) plugin.Config {
	t.Helper()

	parsed, diags := hclparse.NewParser().ParseHCL([]byte("subject = \"climate@7\"\n"), "plugin.hcl")
	if diags.HasErrors() {
		t.Fatalf("parse: %v", diags)
	}
	return plugin.Config{Name: "topic", Job: "news", Body: parsed.Body}
}

// scores is what a classifier says about two clearly different pages.
func scores(t *testing.T, c classify.Classifier) (on, off float64) {
	t.Helper()
	ctx := context.Background()

	var err error
	if on, err = c.Score(ctx, "Ministers delayed the decarbonisation plan despite rising emissions."); err != nil {
		t.Fatal(err)
	}
	if off, err = c.Score(ctx, "The transfer window closed with the squad unchanged."); err != nil {
		t.Fatal(err)
	}
	return on, off
}

// TestAClassifierIsTheSameWhereverItCameFrom.
//
// The claim the whole seam rests on: a node that fetched a model over the bus
// and a node that read one off its disk score the same page the same way. A
// difference here would look like the crawl being unlucky on one node rather
// than like the two nodes disagreeing, which is the failure this exists to
// prevent.
func TestAClassifierIsTheSameWhereverItCameFrom(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	trained(t, dir)

	ref := classify.Ref{Name: "climate", Version: 7}

	local, err := source.Open(ctx, config(t), "", dir, ref)
	if err != nil {
		t.Fatalf("from a directory: %v", err)
	}

	conn, err := bus.Connect(bus.Options{StoreDir: t.TempDir(), Name: t.Name()})
	if err != nil {
		t.Fatalf("bus: %v", err)
	}
	defer conn.Close()

	topics, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	service, err := conn.ServeTopics(topics, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	remote, err := source.Open(ctx, config(t), conn.Address(), "", ref)
	if err != nil {
		t.Fatalf("from the bus: %v", err)
	}

	wantOn, wantOff := scores(t, local)
	gotOn, gotOff := scores(t, remote)

	if wantOn <= wantOff {
		t.Fatalf("the local classifier does not separate the two pages: %.3f and %.3f", wantOn, wantOff)
	}
	if gotOn != wantOn || gotOff != wantOff {
		t.Errorf("the fetched classifier scores differently: %.6f/%.6f, want %.6f/%.6f",
			gotOn, gotOff, wantOn, wantOff)
	}
}

// TestATopicNobodyTrainedIsRefusedWhenTheChainIsBuilt.
//
// Either way round. A job naming a classifier that is not there has to fail at
// the start of a run rather than on the first page, because a crawl that got
// halfway before refusing has already spent somebody's politeness budget.
func TestATopicNobodyTrainedIsRefusedWhenTheChainIsBuilt(t *testing.T) {
	ctx := context.Background()
	ref := classify.Ref{Name: "nosuchtopic", Version: 1}

	if _, err := source.Open(ctx, config(t), "", t.TempDir(), ref); err == nil {
		t.Error("a classifier nobody trained was opened from a directory")
	}

	conn, err := bus.Connect(bus.Options{StoreDir: t.TempDir(), Name: t.Name()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	topics, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service, err := conn.ServeTopics(topics, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	_, err = source.Open(ctx, config(t), conn.Address(), "", ref)
	if err == nil {
		t.Fatal("a classifier nobody trained was fetched")
	}
	if !strings.Contains(err.Error(), "nosuchtopic") {
		t.Errorf("the refusal does not name it: %v", err)
	}
}

// TestABusThatIsNotThereIsRefusedRatherThanFallingBackToDisk.
//
// Falling back would be the worst of both: a node told to use the cluster's
// classifier would quietly use whatever happened to be on its disk, which is
// how two nodes come to disagree about what a job is looking for without
// anybody being told.
func TestABusThatIsNotThereIsRefusedRatherThanFallingBackToDisk(t *testing.T) {
	dir := t.TempDir()
	trained(t, dir)

	_, err := source.Open(context.Background(), config(t),
		"nats://127.0.0.1:1", dir, classify.Ref{Name: "climate", Version: 7})
	if err == nil {
		t.Fatal("a url that answers nothing fell back to the directory")
	}
}
