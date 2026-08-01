// SPDX-License-Identifier: GPL-3.0-or-later

package train

import (
	"testing"

	"github.com/rangertaha/scour/internal/score/hmm"
	"github.com/rangertaha/scour/internal/store"
)

// The chain's observations must not be derived from extraction, or the role it
// decodes is a restatement of what was extracted rather than evidence about it.
//
// This is how the section-pages fault survived a plan to fix it. extract writes
// a match count per URL, trainChain then fits from those counts, and
// observationsOf tests Matches before Links, so any page a record came out of
// is observed as Records and decoded as Detail. On the news2 corpus that made
// 866 of 867 records come from pages labelled detail, including all 118 whose
// titles are section names.
//
// The test does not assert the current behaviour is right. It pins the shape of
// the problem so that a change to observationsOf has to face it: a page with
// links and a spurious record must be distinguishable from a page with a real
// record and none.
func TestChainObservationsCannotTellAHubFromADetailPage(t *testing.T) {
	hub := store.Path{
		URLs:     []string{"http://example.com/news/community/"},
		Statuses: []int{200},
		Links:    []int{48}, // a section index, full of links
		Matches:  []int{1},  // and one record extraction should not have made
	}
	article := store.Path{
		URLs:     []string{"http://example.com/news/a-story/"},
		Statuses: []int{200},
		Links:    []int{2},
		Matches:  []int{1},
	}

	got, want := observationsOf(hub), observationsOf(article)
	if got[0] != want[0] {
		t.Fatalf("hub observed as %v, article as %v: they already differ, so "+
			"the circularity described above has been fixed and this test should "+
			"be rewritten to assert the new rule", got[0], want[0])
	}
	if got[0] != hmm.Records {
		t.Errorf("observed %v, want Records: a match outranks 48 links today", got[0])
	}
}
