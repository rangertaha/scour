// SPDX-License-Identifier: GPL-3.0-or-later

// Package defaults holds the models scour ships with.
//
// A fresh install knows nothing: no properties, no aliases, no examples, and
// so nothing to bootstrap labels from. That is a real cost, because the first
// crawl of a new entity is the one with the least to go on. Shipping starter
// schemas inside the binary removes it, and go:embed means they cannot go
// missing from a package, a container image, or a `go install`.
//
// What ships is only what transfers between sites. A schema describes what a
// vehicle is, which is true everywhere; an XPath describes where one site put
// it, which is true nowhere else. The models here therefore carry a schema and
// may carry a field-order chain, and never carry located items.
package defaults

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/rangertaha/scour/internal/wom"
)

//go:embed models/*.json
var files embed.FS

const dir = "models"

// ErrNoDefault is returned when nothing ships under that name.
var ErrNoDefault = errors.New("no default model by that name")

// shipped is the parsed contents of the embedded directory, read once.
type shipped struct {
	models map[string]*wom.Model
	names  []string
	err    error
}

var (
	once  sync.Once
	cache shipped
)

// load reads every embedded model.
//
// A malformed embedded file is a build-time mistake that would otherwise reach
// a user as a runtime one, so it is reported rather than skipped. A test walks
// the same path, which is what keeps the mistake from shipping at all.
func load() {
	models := map[string]*wom.Model{}
	var names []string

	entries, err := fs.ReadDir(files, dir)
	if err != nil {
		cache.err = fmt.Errorf("read embedded models: %w", err)
		return
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		raw, err := files.ReadFile(path.Join(dir, entry.Name()))
		if err != nil {
			cache.err = fmt.Errorf("read %s: %w", entry.Name(), err)
			return
		}

		var m wom.Model
		if err := json.Unmarshal(raw, &m); err != nil {
			cache.err = fmt.Errorf("parse %s: %w", entry.Name(), err)
			return
		}
		if len(m.Schema) == 0 {
			cache.err = fmt.Errorf("%s describes no properties", entry.Name())
			return
		}
		// Locators do not transfer between sites, so a shipped model must not
		// carry any. Checking here stops a well-meant copy of a trained model
		// from being shipped as a template.
		if len(m.Items) > 0 {
			cache.err = fmt.Errorf("%s ships located items, which are site specific", entry.Name())
			return
		}

		name := strings.TrimSuffix(entry.Name(), ".json")
		models[name] = &m
		names = append(names, name)
	}

	sort.Strings(names)
	cache = shipped{models: models, names: names}
}

// Names lists the models that ship, sorted.
func Names() ([]string, error) {
	once.Do(load)
	if cache.err != nil {
		return nil, cache.err
	}
	return append([]string(nil), cache.names...), nil
}

// Has reports whether a model ships under that name.
func Has(name string) bool {
	once.Do(load)
	_, ok := cache.models[name]
	return ok
}

// Model returns a shipped model by name. The result is a copy, so a caller may
// adapt it without affecting anyone else's.
func Model(name string) (*wom.Model, error) {
	once.Do(load)
	if cache.err != nil {
		return nil, cache.err
	}

	m, ok := cache.models[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q, have %s", ErrNoDefault, name, strings.Join(cache.names, ", "))
	}
	return clone(m)
}

// Schema returns just the properties a shipped model describes, which is what
// creating an entity from a template needs.
func Schema(name string) (wom.Schema, error) {
	m, err := Model(name)
	if err != nil {
		return nil, err
	}
	return m.Schema, nil
}

// clone deep copies through the serialization the model already defines, so a
// caller cannot mutate the shipped copy.
func clone(m *wom.Model) (*wom.Model, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("copy default model: %w", err)
	}
	var out wom.Model
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("copy default model: %w", err)
	}
	return &out, nil
}
