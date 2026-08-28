// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2/hclwrite"
	ucli "github.com/urfave/cli/v3"
	"github.com/zclconf/go-cty/cty"

	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/classify"
	"github.com/rangertaha/scour/internal/classify/bayes"
	"github.com/rangertaha/scour/internal/classify/store"
	"github.com/rangertaha/scour/internal/decode"
	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/safefile"
)

// Topic manages what a crawl has been taught to recognise.
//
// # The loop
//
// A topic is trained from a labels document, which is a file somebody owns:
// which cached pages are the subject and which are not. `propose` uses seed
// terms to make a first pass at that file, a person corrects it, and `train`
// learns from what they decided.
//
// That is the same shape `scour job train` uses for locators, and it is the shape
// because the alternative is a model whose mistakes cannot be traced to the
// decision that caused them. A classifier trained from state inside the tool
// says a page is about climate and there is nowhere to go and look at why.
//
// # Versions are never replaced here
//
// Training writes the next version and prints it, so the next thing somebody
// does is paste `climate@8` into a job. A job pins a version precisely so that
// somebody else retraining cannot change what it does.
func Topic(a *App) *ucli.Command {
	var (
		dir    string
		corpus string
		write  bool
		limit  int
	)

	shared := []ucli.Flag{
		&ucli.StringFlag{Name: "dir", Usage: "where the trained topics live", Destination: &dir},
	}

	return &ucli.Command{
		Name:            "topic",
		HideHelpCommand: true,
		Category:        "Shared across jobs",
		Usage:           "Train and manage the subjects a crawl recognises",
		ArgsUsage:       "<command>",
		Description: "A topic is a trained classifier a job refers to by name and version,\n" +
			"as `climate@7`. It is learned from a labels document: the cached pages\n" +
			"somebody decided are and are not the subject.\n\n" +
			"Start with `scour topic propose labels.hcl --write`, correct what it\n" +
			"got wrong, then `scour topic train labels.hcl`.",
		Commands: []*ucli.Command{
			{
				Name:  "list",
				Usage: "What has been trained",
				Flags: shared,
				Action: func(ctx context.Context, cmd *ucli.Command) error {
					return listTopics(a, dir)
				},
			},
			{
				Name:      "show",
				Usage:     "What a trained topic learned",
				ArgsUsage: "<name@version>",
				Flags:     shared,
				Action: func(ctx context.Context, cmd *ucli.Command) error {
					return showTopic(ctx, a, dir, cmd.Args().First())
				},
			},
			{
				Name:      "delete",
				Usage:     "Remove one trained version",
				ArgsUsage: "<name@version>",
				Flags:     shared,
				Action: func(ctx context.Context, cmd *ucli.Command) error {
					return removeTopic(a, dir, cmd.Args().First())
				},
			},
			{
				Name:      "propose",
				Usage:     "Label the cached corpus from a topic's seed terms",
				ArgsUsage: "<labels.hcl>",
				Description: "Scores every cached page against the topic's terms and proposes the\n" +
					"ones that match as examples. This is a worse classifier than the one\n" +
					"being trained, on purpose: it is a first pass somebody corrects.",
				Flags: append([]ucli.Flag{
					&ucli.BoolFlag{Name: "write", Usage: "edit the document instead of printing what would change", Destination: &write},
					&ucli.StringFlag{Name: "corpus", Usage: "where the cached pages are", Destination: &corpus},
					&ucli.IntFlag{Name: "pages", Value: 500, Usage: "how many cached pages to look at", Destination: &limit},
				}, shared...),
				Action: oneFile(func(ctx context.Context, path string) error {
					return proposeLabels(ctx, a, path, corpus, limit, write)
				}),
			},
			{
				Name:      "train",
				Usage:     "Train a topic from a labels document",
				ArgsUsage: "<labels.hcl>",
				Flags: append([]ucli.Flag{
					&ucli.StringFlag{Name: "corpus", Usage: "where the cached pages are", Destination: &corpus},
				}, shared...),
				Action: oneFile(func(ctx context.Context, path string) error {
					return trainTopics(ctx, a, path, dir, corpus)
				}),
			},
		},
		Action: func(_ context.Context, cmd *ucli.Command) error {
			if name := cmd.Args().First(); name != "" {
				return Usagef("unknown topic command %q", name)
			}
			return ucli.ShowSubcommandHelp(cmd)
		},
	}
}

func topicStore(dir string) (*store.Store, error) {
	if dir == "" {
		dir = store.DefaultDir
	}
	return store.Open(dir)
}

func listTopics(a *App, dir string) error {
	topics, err := topicStore(dir)
	if err != nil {
		return Failedf("%v", err)
	}

	names, err := topics.Names()
	if err != nil {
		return Failedf("%v", err)
	}
	if len(names) == 0 {
		a.Warnf("nothing trained yet. Start with `scour topic propose labels.hcl --write`\n")
		return nil
	}
	for _, name := range names {
		a.Printf("%s\n", name)
	}
	return nil
}

func showTopic(ctx context.Context, a *App, dir, ref string) error {
	if ref == "" {
		return Usagef("which topic? Give one as name@version")
	}
	parsed, err := classify.ParseRef(ref)
	if err != nil {
		return Invalidf("%v", err)
	}

	topics, err := topicStore(dir)
	if err != nil {
		return Failedf("%v", err)
	}

	one, err := topics.Load(parsed)
	if err != nil {
		return Failedf("%v", err)
	}

	a.Printf("%s@%d, a %s classifier\n", one.Name, one.Version, one.Kind)
	if len(one.Terms) > 0 {
		a.Printf("  terms     %s\n", strings.Join(one.Terms, ", "))
	}
	if len(one.Model) == 0 {
		return nil
	}

	// The words are the model, and printing them is most of what makes a
	// trained classifier reviewable rather than a number somebody trusts.
	var model bayes.Bayes
	if err := json.Unmarshal(one.Model, &model); err != nil {
		return Failedf("%v", err)
	}
	a.Printf("  examples  %d\n", model.Examples)
	a.Printf("  words     %d\n", len(model.LogOdds))

	words := model.Words(12)
	if len(words) > 0 {
		a.Printf("  strongest %s\n", strings.Join(words, ", "))
	}
	return nil
}

func removeTopic(a *App, dir, ref string) error {
	if ref == "" {
		return Usagef("which topic? Give one as name@version")
	}
	parsed, err := classify.ParseRef(ref)
	if err != nil {
		return Invalidf("%v", err)
	}

	topics, err := topicStore(dir)
	if err != nil {
		return Failedf("%v", err)
	}
	if err := topics.Delete(parsed); err != nil {
		return Failedf("%v", err)
	}
	a.Warnf("removed %s\n", parsed)
	return nil
}

// page is one cached page, by the URL a person would recognise.
type page struct {
	URL  string
	Text string
}

// pages reads the cached corpus, keyed by the URL each body came from.
//
// The sidecar the cache keeps beside every body is what makes that possible: a
// key is a hash of the URL and cannot be turned back into one, so a corpus
// keyed by key would produce a labels file full of hashes that nobody could
// review. A body whose sidecar is missing is skipped rather than labelled with
// its hash, because a label somebody cannot read is a label they cannot check.
// A limit of zero reads everything, which is what training wants: the labels
// decide the set, and a cut would silently change it.
func pages(ctx context.Context, dir string, limit int) ([]page, error) {
	if dir == "" {
		dir = filepath.Join(".scour", "cache")
	}

	bodies, err := cache.New(ctx, cache.Config{Dir: dir})
	if err != nil {
		return nil, err
	}
	defer bodies.Close()

	// The sidecars first, so a body can be given back its URL.
	urls := map[string]string{}
	for key, err := range bodies.Keys(ctx) {
		if err != nil {
			return nil, err
		}
		if !strings.HasSuffix(key, ".meta") {
			continue
		}
		raw, err := cache.GetBytes(ctx, bodies, key)
		if err != nil {
			continue
		}
		var side struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(raw, &side); err != nil || side.URL == "" {
			continue
		}
		urls[strings.TrimSuffix(key, ".meta")] = side.URL
	}

	var out []page
	for key, err := range bodies.Keys(ctx) {
		if err != nil {
			return nil, err
		}
		if limit > 0 && len(out) >= limit {
			break
		}
		if strings.HasSuffix(key, ".meta") {
			continue
		}
		url, ok := urls[key]
		if !ok {
			continue
		}
		body, err := cache.GetBytes(ctx, bodies, key)
		if err != nil {
			continue
		}
		text, _ := decode.Bytes(body, "")
		out = append(out, page{URL: url, Text: string(text.Text)})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].URL < out[j].URL })
	return out, nil
}

func readLabels(path string) (*engine.Topics, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, Failedf("%v", err)
	}
	doc, err := engine.ParseTopics(src, filepath.Base(path))
	if err != nil {
		return nil, Invalidf("%v", err)
	}
	if err := doc.Validate(); err != nil {
		return nil, Invalidf("%v", err)
	}
	return doc, nil
}

func proposeLabels(ctx context.Context, a *App, path, corpusDir string, limit int, write bool) error {
	doc, err := readLabels(path)
	if err != nil {
		return err
	}

	corpus, err := pages(ctx, corpusDir, limit)
	if err != nil {
		return Failedf("%v", err)
	}
	if len(corpus) == 0 {
		return Invalidf("no cached pages to label. Run `scour crawl` or `scour scrape` first")
	}
	a.Warnf("read %d cached pages\n", len(corpus))

	var proposals int
	for _, one := range doc.Topics {
		if len(one.Terms) == 0 {
			a.Warnf("%s: no terms to bootstrap from, so nothing was proposed\n", one.Name)
			continue
		}

		scorer, err := classify.New(ctx, "terms", classify.Config{
			Name: one.Name, Version: 1, Terms: one.Terms,
		})
		if err != nil {
			return Failedf("%v", err)
		}

		// What somebody already decided is never overwritten: this proposes for
		// the pages nobody has ruled on, and a correction stays corrected.
		decided := map[string]bool{}
		for _, url := range append(append([]string{}, one.About...), one.Not...) {
			decided[url] = true
		}

		var about, not []string
		for _, p := range corpus {
			if decided[p.URL] {
				continue
			}
			score, err := scorer.Score(ctx, p.Text)
			if err != nil {
				return Failedf("%v", err)
			}
			if score >= engine.Least {
				about = append(about, p.URL)
			} else {
				not = append(not, p.URL)
			}
		}

		a.Printf("  %-20s %d proposed as the subject, %d as not\n", one.Name, len(about), len(not))
		one.About = append(one.About, about...)
		one.Not = append(one.Not, not...)
		proposals += len(about) + len(not)
	}

	if proposals == 0 {
		a.Warnf("nothing new to propose\n")
		return nil
	}
	if !write {
		a.Warnf("nothing written. Pass --write to edit %s, then correct what it got wrong\n", path)
		return nil
	}

	if err := writeLabels(path, doc); err != nil {
		return Failedf("%v", err)
	}
	a.Warnf("wrote %d proposals into %s. Correct them, then `scour topic train %s`\n",
		proposals, path, path)
	return nil
}

// hclString renders a string as HCL, which is not what %q renders.
//
// An HCL quoted string is a template: `${` opens an interpolation and `%{` a
// directive, and the escapes for them are `$${` and `%%{`, which Go's %q knows
// nothing about. A crawled URL carrying `${` is not exotic, and one written
// with %q turned the labels file into a document that no longer parsed, with an
// HCL diagnostic about an unknown variable rather than an error naming the URL.
// Since propose rewrites the file in place, the person's corrections were then
// in a file only a hand edit could recover. %q also emits Go's \xNN for bytes
// that are not valid UTF-8, which HCL's scanner rejects outright.
//
// hclwrite is the library that knows the answer, and it was already in the
// module and imported nowhere.
func hclString(value string) string {
	return string(hclwrite.TokensForValue(cty.StringVal(value)).Bytes())
}

// writeLabels rewrites a labels document.
//
// Rendered rather than edited in place, unlike the job document. A job document
// is a person's own file with their comments and their ordering in it, and
// `scour job train` edits its text precisely so that a diff stays reviewable. This
// file is mostly a list this command generated, so rendering it keeps the
// ordering stable and the diff small, which is what makes the corrections
// visible.
//
// Written to a copy and renamed, so a failure halfway leaves the original.
func writeLabels(path string, doc *engine.Topics) error {
	var b strings.Builder
	b.WriteString("// Labels for `scour topic train`. Generated by `scour topic propose`\n")
	b.WriteString("// and corrected by hand: what you move between the lists is what wins.\n")

	for _, one := range doc.Topics {
		fmt.Fprintf(&b, "\ntopic %s {\n", hclString(one.Name))
		if len(one.Terms) > 0 {
			fmt.Fprintf(&b, "  terms = [\n")
			for _, term := range one.Terms {
				fmt.Fprintf(&b, "    %s,\n", hclString(term))
			}
			fmt.Fprintf(&b, "  ]\n")
		}
		for _, list := range []struct {
			name string
			urls []string
		}{{"about", one.About}, {"not", one.Not}} {
			if len(list.urls) == 0 {
				continue
			}
			sorted := append([]string{}, list.urls...)
			sort.Strings(sorted)

			fmt.Fprintf(&b, "\n  %s = [\n", list.name)
			for _, url := range sorted {
				fmt.Fprintf(&b, "    %s,\n", hclString(url))
			}
			fmt.Fprintf(&b, "  ]\n")
		}
		b.WriteString("}\n")
	}

	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	// A failure halfway must leave the original: a corrected labels document
	// is somebody's work. See [internal/safefile], shared with the two other
	// places that rewrite a file.
	return safefile.Replace(path, []byte(b.String()), info.Mode().Perm())
}

func trainTopics(ctx context.Context, a *App, path, dir, corpusDir string) error {
	doc, err := readLabels(path)
	if err != nil {
		return err
	}

	// Unlimited, whatever --pages says, because the labels are what decide the
	// training set and the flag decides how much to look at when proposing.
	//
	// Truncating first meant a labelled page beyond the cut was dropped from
	// training and reported as though the cache did not hold it, which it did.
	// The cut is by hash order, so it was also one-sided: the same labels file
	// produced a differently balanced corpus, and therefore a differently
	// calibrated model, depending on a paging flag. It bit with default flags
	// whenever a crawl grew the cache between propose and train.
	corpus, err := pages(ctx, corpusDir, 0)
	if err != nil {
		return Failedf("%v", err)
	}
	if len(corpus) == 0 {
		return Invalidf("no cached pages to learn from. Run `scour crawl` or `scour scrape` first")
	}

	text := make(map[string]string, len(corpus))
	for _, p := range corpus {
		text[p.URL] = p.Text
	}

	topics, err := topicStore(dir)
	if err != nil {
		return Failedf("%v", err)
	}

	for _, one := range doc.Topics {
		var docs []bayes.Document
		var missing int

		for _, url := range one.About {
			if body, ok := text[url]; ok {
				docs = append(docs, bayes.Document{Text: body, About: true})
			} else {
				missing++
			}
		}
		for _, url := range one.Not {
			if body, ok := text[url]; ok {
				docs = append(docs, bayes.Document{Text: body, About: false})
			} else {
				missing++
			}
		}

		if missing > 0 {
			a.Warnf("%s: %d labelled pages are not in the cache and were skipped\n", one.Name, missing)
		}

		latest, err := topics.Latest(one.Name)
		if err != nil {
			return Failedf("%v", err)
		}
		version := latest + 1

		model, err := bayes.Train(one.Name, version, docs)
		if err != nil {
			// The refusal names the subject and says what it needs, which is
			// almost always examples on both sides.
			return Invalidf("%v", err)
		}

		encoded, err := json.Marshal(model)
		if err != nil {
			return Failedf("%v", err)
		}
		if err := topics.Put("bayes", classify.Config{
			Name: one.Name, Version: version, Terms: one.Terms, Model: encoded,
		}); err != nil {
			return Failedf("%v", err)
		}

		a.Printf("%s@%d trained on %d examples\n", one.Name, version, model.Examples)
		if words := model.Words(8); len(words) > 0 {
			a.Printf("  strongest %s\n", strings.Join(words, ", "))
		}
		a.Warnf("  use it with: subject = \"%s@%d\"\n", one.Name, version)
	}
	return nil
}
