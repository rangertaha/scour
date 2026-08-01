// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// tunnelSite is the case the crawl chain exists for.
//
// The pages holding records sit behind hubs whose own URLs share no words with
// the item: /listing/ and /p/N/ look nothing like "vehicle" or "car", and
// the hubs hold no records themselves. A scorer that judges a URL on its own
// tokens therefore rates them no higher than the noise, and a crawl that
// believes it never reaches the records at all.
func tunnelSite(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		p := r.URL.Path

		switch {
		case p == "/":
			fmt.Fprint(w, `<a href="/listing/">listing</a><a href="/legal/">legal</a><a href="/blog/">blog</a>`)

		case p == "/listing/":
			// A hub: links out to the records, holds none itself.
			for i := range 10 {
				fmt.Fprintf(w, `<a href="/p/%d/">item %d</a>`, i, i)
			}

		case strings.HasPrefix(p, "/p/"):
			id := strings.Trim(strings.TrimPrefix(p, "/p/"), "/")
			fmt.Fprintf(w, `<div class="vehicle"><dl>
<dt>Make</dt><dd class="make">Ford</dd>
<dt>Model</dt><dd class="model">M%s</dd>
<dt>Year</dt><dd class="year">201%s</dd></dl></div>`, id, id)

		case p == "/legal/" || p == "/blog/":
			section := strings.Trim(p, "/")
			for i := range 10 {
				fmt.Fprintf(w, `<a href="/%s/%d/">%s %d</a>`, section, i, section, i)
			}

		default:
			fmt.Fprint(w, `<h1>Boilerplate</h1><p>Nothing of interest on this page.</p>`)
		}
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// records counts how many pages a crawl fetched that actually hold records.
func recordPages(out string) (relevant, total int) {
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, `"URL"`) {
			continue
		}
		total++
		if strings.Contains(line, "/p/") {
			relevant++
		}
	}
	return relevant, total
}

// TestChainImprovesRecordsPerPage is the gate M4.5 was made conditional on. If
// the chain does not raise the share of fetched pages that hold records on a
// site where tunnelling matters, it earns nothing and should not ship.
func TestChainImprovesRecordsPerPage(t *testing.T) {
	srv := tunnelSite(t)

	setup := func(dir string) {
		runOK(t, dir, "item", "add", "vehicle", "--alias", "car", "-u", srv.URL+"/")
		runOK(t, dir, "item", "add", "vehicle", "-p", "make", "-e", "Ford")
		runOK(t, dir, "item", "add", "vehicle", "-p", "model", "-e", "M1")
		runOK(t, dir, "item", "add", "vehicle", "-p", "year", "-e", "2011")
	}

	// Both arms crawl the whole site once, train, then crawl again under a
	// budget. The only difference is whether the chain is allowed to weigh in.
	withChain := crawlDir(t)
	setup(withChain)
	runOK(t, withChain, "crawl", "vehicle", "--depth", "4")
	trained := runOK(t, withChain, "model", "train", "vehicle")
	if !strings.Contains(trained, "roles") {
		t.Fatalf("training did not decode any roles:\n%s", trained)
	}

	warm := runOK(t, withChain, "crawl", "vehicle", "--reset", "--depth", "4", "--max-pages", "14")
	if !strings.Contains(warm, "with crawl chain") {
		t.Fatalf("the chain was not in play on the second crawl:\n%s", warm)
	}
	chainRelevant, chainTotal := recordPages(runOK(t, withChain, "crawl", "vehicle", "--json"))

	// The comparison arm: same corpus, same training, chain discarded.
	noChain := crawlDir(t)
	setup(noChain)
	runOK(t, noChain, "crawl", "vehicle", "--depth", "4")
	runOK(t, noChain, "model", "train", "vehicle", "--no-chain")
	plain := runOK(t, noChain, "crawl", "vehicle", "--reset", "--depth", "4", "--max-pages", "14")
	if strings.Contains(plain, "with crawl chain") {
		t.Fatalf("--no-chain still used the chain:\n%s", plain)
	}
	plainRelevant, plainTotal := recordPages(runOK(t, noChain, "crawl", "vehicle", "--json"))

	t.Logf("with chain:    %d of %d fetched pages held records", chainRelevant, chainTotal)
	t.Logf("without chain: %d of %d fetched pages held records", plainRelevant, plainTotal)

	if chainTotal == 0 || plainTotal == 0 {
		t.Fatal("a crawl fetched nothing, so there is nothing to compare")
	}
	if chainRelevant <= plainRelevant {
		t.Errorf("the chain found %d record pages against %d without it: it earns nothing here",
			chainRelevant, plainRelevant)
	}
}
