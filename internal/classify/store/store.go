// SPDX-License-Identifier: GPL-3.0-or-later

// Package store keeps trained classifiers where every job can reach one.
//
// # Why they are shared
//
// A classifier is a thing about the world rather than about a job. Two jobs
// crawling different sites for climate coverage want the same answer to "is
// this about climate", and training one each would mean two answers, two
// corpora and two things to retrain. So a classifier has a name and a version,
// jobs reference it as `climate@7`, and nothing about it belongs to a job.
//
// # Why a version is required
//
// Referring to a subject without one would mean a job's behaviour changing when
// somebody retrains, with nothing in the document to show why. Retraining
// produces a new version rather than changing what an existing one means, and a
// job moves to it by editing a number that appears in a diff.
//
// # Files, not a database
//
// One file per version, named for what it is. A trained classifier is a few
// hundred kilobytes of counts that never change once written, which is what a
// file is for; and an operator who wants to know which classifiers a node has
// can look.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/rangertaha/scour/internal/classify"
)

// ErrNotTrained reports a classifier nobody has trained.
var ErrNotTrained = errors.New("classify: not trained")

// DefaultDir is where classifiers live when a job does not say.
//
// It lives here rather than in either `topic` middleware because both of them
// need it and neither may import the other: borrowing the constant from the
// spider's package would register the spider's middleware on a node that asked
// only for the scheduler's. This is the package they already share.
const DefaultDir = ".scour/topics"

// Store is a directory of trained classifiers.
type Store struct {
	dir string

	mu     sync.Mutex
	loaded map[string]classify.Classifier
}

// Open returns a store rooted at dir, creating it if it is not there.
func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("classify: no directory for the classifiers")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("classify: %w", err)
	}
	return &Store{dir: dir, loaded: map[string]classify.Classifier{}}, nil
}

// saved is what a file holds: enough to rebuild the classifier, and enough for
// a person opening it to know what they are looking at.
type saved struct {
	Kind    string             `json:"kind"`
	Name    string             `json:"name"`
	Version int                `json:"version"`
	Terms   []string           `json:"terms,omitempty"`
	Weights map[string]float64 `json:"weights,omitempty"`
	Model   json.RawMessage    `json:"model,omitempty"`
}

// Put writes a trained classifier.
//
// A version that already exists is refused rather than replaced. What a version
// means has to be fixed, or a job that pinned one would silently start
// behaving differently, which is the whole thing versions exist to prevent.
func (s *Store) Put(kind string, cfg classify.Config) error {
	ref := classify.Ref{Name: cfg.Name, Version: cfg.Version}
	if err := check(ref); err != nil {
		return err
	}

	path := s.path(ref)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("classify: %s already exists. Train a new version rather than replacing one", ref)
	}

	body, err := json.MarshalIndent(saved{
		Kind:    kind,
		Name:    cfg.Name,
		Version: cfg.Version,
		Terms:   cfg.Terms,
		Weights: cfg.Weights,
		Model:   cfg.Model,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("classify: %w", err)
	}

	if err := os.WriteFile(path, body, 0o640); err != nil {
		return fmt.Errorf("classify: %w", err)
	}
	return nil
}

// Get returns a classifier, building it the first time and keeping it after.
//
// Kept because one classifier serves every job that references it, from every
// goroutine that is scoring a page, and rebuilding it per page would be reading
// a file per page.
func (s *Store) Get(ctx context.Context, ref classify.Ref) (classify.Classifier, error) {
	if err := check(ref); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if built, ok := s.loaded[ref.String()]; ok {
		return built, nil
	}

	body, err := os.ReadFile(s.path(ref))
	switch {
	case os.IsNotExist(err):
		return nil, fmt.Errorf("%w: %s. Train it with `scour topic train`", ErrNotTrained, ref)
	case err != nil:
		return nil, fmt.Errorf("classify: %w", err)
	}

	var from saved
	if err := json.Unmarshal(body, &from); err != nil {
		return nil, fmt.Errorf("classify: %s: %w", ref, err)
	}

	built, err := classify.New(ctx, from.Kind, classify.Config{
		Name:    from.Name,
		Version: from.Version,
		Terms:   from.Terms,
		Weights: from.Weights,
		Model:   from.Model,
	})
	if err != nil {
		return nil, fmt.Errorf("classify: %s: %w", ref, err)
	}

	s.loaded[ref.String()] = built
	return built, nil
}

// Names lists what has been trained, as jobs would write it.
func (s *Store) Names() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("classify: %w", err)
	}

	var out []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		if ref, ok := parseFile(strings.TrimSuffix(name, ".json")); ok {
			out = append(out, ref.String())
		}
	}
	sort.Strings(out)
	return out, nil
}

// Latest is the highest version trained for a subject, which is what `scour
// topic train` prints so the next thing somebody does is paste it into a job.
func (s *Store) Latest(name string) (int, error) {
	refs, err := s.Names()
	if err != nil {
		return 0, err
	}

	latest := 0
	for _, one := range refs {
		ref, err := classify.ParseRef(one)
		if err != nil || ref.Name != name {
			continue
		}
		latest = max(latest, ref.Version)
	}
	return latest, nil
}

func (s *Store) path(ref classify.Ref) string {
	return filepath.Join(s.dir, ref.Name+"@"+strconv.Itoa(ref.Version)+".json")
}

func parseFile(name string) (classify.Ref, bool) {
	ref, err := classify.ParseRef(name)
	if err != nil {
		return classify.Ref{}, false
	}
	return ref, true
}

// check refuses a reference that would not be a filename.
func check(ref classify.Ref) error {
	switch {
	case strings.TrimSpace(ref.Name) == "":
		return errors.New("classify: a classifier needs a name")
	case ref.Version < 1:
		return fmt.Errorf("classify: %q needs a version, as in %s@1", ref.Name, ref.Name)
	case strings.ContainsAny(ref.Name, `/\@. `):
		return fmt.Errorf("classify: %q cannot be a name: no slashes, dots, spaces or at signs", ref.Name)
	}
	return nil
}
