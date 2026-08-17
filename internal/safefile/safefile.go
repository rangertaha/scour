// SPDX-License-Identifier: GPL-3.0-or-later

// Package safefile replaces a file's contents without anybody seeing it empty.
//
// # Why this exists
//
// Because three places in this repository needed it and one of them got it
// wrong, which is the shape worth naming rather than the bug worth patching.
//
// [internal/train] rewrites a job document after induction, and [internal/cli]
// rewrites a labels document after a correction. Both wrote to a temporary file
// and renamed it over the target, and both said why in a comment: so that a
// failure halfway leaves the original. [internal/classify/store] replaces a
// trained model and opened the live file with O_TRUNC, so the contents were
// destroyed before the new ones were written.
//
// That third one had a reader. `scour service` subscribes `load` and `replace`
// for the topic store on one connection, and NATS dispatches each request on
// its own goroutine, so a node asking for a model while an operator corrects it
// read a file that had been truncated and not yet rewritten. The node's chain
// build failed with "unexpected end of JSON input" for a topic that exists and
// is perfectly valid. Replace's own documentation said it existed to avoid
// exactly that, describing the delete-then-put it was written to replace.
//
// # Renaming is the whole mechanism
//
// A rename within a filesystem is atomic: a reader opening the path gets the
// old file or the new one, and there is no instant at which it gets neither.
// Writing in place has no such moment, however fast the write is, because
// truncation and writing are two operations and a reader can arrive between
// them.
//
// So the temporary file is created in the target's own directory. Somewhere
// else and the rename becomes a copy across filesystems, which is not atomic
// and puts the window straight back.
//
// # What this does not promise
//
// Durability across a power cut. Nothing here calls fsync, so a machine that
// loses power mid-rename may come back to either version or to a truncated
// temporary file that nothing will ever read. That is the same promise the two
// call sites already made, and the concern they were written for is a
// concurrent reader rather than a crash. Adding fsync is a decision about cost
// that should be made deliberately rather than inherited from this sentence.
package safefile

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Replace writes data to path, so that a reader sees either what was there
// before or what is being written and never a mixture.
//
// The file is created if it is not there. A caller that needs to know whether
// it already existed wants os.OpenFile with O_EXCL instead: this is for
// replacing, and it will overwrite whatever it finds.
func Replace(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)

	// In the target's own directory, and with a random name rather than a
	// fixed suffix. A fixed one is fine until two writers run at once, and
	// then they share a temporary file and each writes half of the other's.
	temporary, err := os.CreateTemp(dir, filepath.Base(path)+".scour-*")
	if err != nil {
		return fmt.Errorf("safefile: %s: %w", path, err)
	}
	name := temporary.Name()

	// Every failure from here takes the temporary file with it. Leaving them
	// behind turns a full disk into a directory nobody can clean up by hand,
	// because the names are random.
	fail := func(err error) error {
		_ = temporary.Close()
		_ = os.Remove(name)
		return fmt.Errorf("safefile: %s: %w", path, err)
	}

	if _, err := temporary.Write(data); err != nil {
		return fail(err)
	}
	// Before the rename, because the mode is part of what is being replaced
	// and CreateTemp makes a private file. Afterwards there is a moment when
	// the new contents are in place under the wrong permissions.
	if err := temporary.Chmod(perm); err != nil {
		return fail(err)
	}
	if err := temporary.Close(); err != nil {
		temporary = nil
		_ = os.Remove(name)
		return fmt.Errorf("safefile: %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("safefile: %s: %w", path, err)
	}
	return nil
}
