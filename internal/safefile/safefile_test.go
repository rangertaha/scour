// SPDX-License-Identifier: GPL-3.0-or-later

package safefile_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rangertaha/scour/internal/safefile"
)

// TestAReaderNeverSeesTheFileEmpty is the whole point.
//
// A writer that truncates in place destroys the contents before it writes the
// new ones, and any reader arriving in that window gets nothing. It is a small
// window, which is what makes it dangerous: it never shows up while somebody is
// testing by hand and it shows up under load, as a parse error on a file that
// is perfectly valid by the time anybody looks.
//
// So this reads while it writes and asserts that every read is one of the
// versions actually written. A truncating implementation fails it in a handful
// of iterations.
func TestAReaderNeverSeesTheFileEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "topic.json")

	old := []byte(strings.Repeat("a", 4096))
	new := []byte(strings.Repeat("b", 8192))
	if err := os.WriteFile(path, old, 0o640); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			got, err := os.ReadFile(path)
			if err != nil {
				// Never absent either: the rename means the path always
				// resolves to a whole file.
				t.Errorf("the file was not there at all: %v", err)
				return
			}
			if !bytes.Equal(got, old) && !bytes.Equal(got, new) {
				t.Errorf("read %d bytes, which is neither version: a reader saw a "+
					"file that was being written", len(got))
				return
			}
		}
	}()

	for range 200 {
		if err := safefile.Replace(path, new, 0o640); err != nil {
			t.Fatalf("replace: %v", err)
		}
		if err := safefile.Replace(path, old, 0o640); err != nil {
			t.Fatalf("replace: %v", err)
		}
	}
	close(stop)
	wg.Wait()
}

// TestConcurrentWritersDoNotShareATemporaryFile.
//
// A fixed temporary suffix is fine until two writers run at once, and then each
// writes half of the other's file and the rename publishes the mixture. The
// topic store is reachable from a bus where nothing serialises callers, so two
// at once is the ordinary case rather than the exotic one.
func TestConcurrentWritersDoNotShareATemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "topic.json")

	versions := [][]byte{
		bytes.Repeat([]byte("a"), 4096),
		bytes.Repeat([]byte("b"), 8192),
		bytes.Repeat([]byte("c"), 16384),
	}

	var wg sync.WaitGroup
	for _, want := range versions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				if err := safefile.Replace(path, want, 0o640); err != nil {
					t.Errorf("replace: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, versions[0]) && !bytes.Equal(got, versions[1]) && !bytes.Equal(got, versions[2]) {
		t.Errorf("the file is %d bytes, which is none of the three that were written", len(got))
	}

	// And nothing is left behind. The names are random, so a leak here is a
	// directory nobody can tidy by hand.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the directory holds %v, want only the file itself", names)
	}
}

// TestTheModeIsWhatWasAskedFor, because CreateTemp makes a private file and a
// model the service cannot read is the same outage as one that is not there.
func TestTheModeIsWhatWasAskedFor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "topic.json")

	if err := safefile.Replace(path, []byte("{}"), 0o640); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Errorf("mode = %v, want 0640: CreateTemp's 0600 survived the rename", got)
	}
}
