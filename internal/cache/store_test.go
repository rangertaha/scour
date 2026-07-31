// SPDX-License-Identifier: GPL-3.0-or-later

package cache

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"testing"
)

// A driver is chosen by name, and an unknown one has to fail where it is
// configured rather than on the first page fetched.
func TestDriversAreNamed(t *testing.T) {
	for _, want := range []string{"local", "s3", "gcs"} {
		if !Has(want) {
			t.Errorf("driver %q is not registered; have %v", want, Names())
		}
	}
	if _, err := New("nowhere", Config{URL: "/tmp/x"}); err == nil {
		t.Error("an unknown driver should be refused")
	}
}

// An empty name is the local one, which is what an unconfigured scour uses.
func TestEmptyDriverIsLocal(t *testing.T) {
	dir := t.TempDir()
	s, err := New("", Config{URL: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.(*Cache); !ok {
		t.Fatalf("got %T, want the local cache", s)
	}
}

// file:// is the same place as a bare path, so a configuration can say either.
func TestLocalAcceptsAFileURL(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	for _, url := range []string{dir, "file://" + dir} {
		s, err := New("local", Config{URL: url})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Put(ctx, "http://example.com/a", []byte("body")); err != nil {
			t.Fatalf("%s: %v", url, err)
		}
		got, err := s.Get(ctx, "http://example.com/a")
		if err != nil || string(got) != "body" {
			t.Errorf("%s: got %q, %v", url, got, err)
		}
	}
}

// A body that was never stored is absent, not an error to be retried, and every
// driver has to say so the same way.
func TestAMissingBodyIsNotExist(t *testing.T) {
	s, err := New("local", Config{URL: filepath.Join(t.TempDir(), "pages")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Get(context.Background(), "http://example.com/never")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want fs.ErrNotExist", err)
	}
}

// An object store needs somewhere to put things, and saying so at startup beats
// discovering it mid-crawl. Without the cloud build it refuses for a different
// reason, which is also the right answer: a name from a configuration file is
// not a typo, and "this build does not include it" is not "no such thing".
func TestAnObjectStoreNeedsABucket(t *testing.T) {
	for _, driver := range []string{"s3", "gcs"} {
		if _, err := New(driver, Config{}); err == nil {
			t.Errorf("%s accepted an empty url", driver)
		}
	}
}

func TestNamesAreSorted(t *testing.T) {
	got := Names()
	if !slices.IsSorted(got) {
		t.Errorf("Names() = %v, want sorted", got)
	}
}
