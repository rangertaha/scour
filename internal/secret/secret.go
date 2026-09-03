// SPDX-License-Identifier: GPL-3.0-or-later

// Package secret is where a credential lives, and the one place it is ever in
// plain text.
//
// # A job document holds a call, not a value
//
// `secret("acme-s3-key")` is an unevaluated function call everywhere the job
// travels: the document somebody wrote, the copy stored in KV, the diff shown
// when it is resubmitted, the output of `scour job show`. It becomes a credential
// exactly once, on the node building the plugin that needs it, and nowhere it
// could be written down.
//
// That falls out of the plugin seam rather than being added to it. A plugin's
// configuration is left undecoded until the plugin is built, so nothing before
// that point has any reason to evaluate the call.
//
// # Sealed, with a key that is not in the store
//
// The values are encrypted before they reach NATS, with a key the cluster is
// given rather than one it keeps. A KV bucket is a file on somebody's disk and
// a stream anybody with the address can read; treating it as a safe would be
// treating "nobody has the address" as a security property.
//
// # No history, and no read
//
// The bucket keeps one revision, because a store that remembers every previous
// value is a store that leaks the one you rotated away from.
//
// There is no `Get` that returns a value to a person. Setting one reads from
// stdin rather than an argument, because an argument is in the shell history
// and in the process table. What a person can do is set one, list the names,
// and delete one; what a node can do is resolve one while building a plugin.
package secret

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/rangertaha/scour/internal/bus"
)

// Bucket is where sealed secrets live.
const Bucket = "SCOUR_SECRETS"

// KeyVar is the environment variable holding the cluster's sealing key, base64
// encoded. A file is the other way, and the better one for a service manager.
const KeyVar = "SCOUR_SECRET_KEY"

// KeySize is the sealing key's length. AES-256.
const KeySize = 32

// Errors this package produces.
var (
	// ErrNoKey reports a node with no sealing key, which cannot read or write
	// a secret and should say so rather than failing at the first plugin.
	ErrNoKey = errors.New("secret: no sealing key")

	// ErrNoSecret reports a name nobody has set.
	ErrNoSecret = errors.New("secret: no such secret")

	// ErrSealed reports a value that will not open with this key, which almost
	// always means the wrong key rather than a damaged store.
	ErrSealed = errors.New("secret: this key does not open that value")
)

// Store is the cluster's secrets.
type Store struct {
	kv   jetstream.KeyValue
	seal cipher.AEAD
}

// NewKey returns a fresh sealing key, base64 encoded, which is what `scour
// secret key` prints once and never again.
func NewKey() (string, error) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return "", fmt.Errorf("secret: %w", err)
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

// Key finds the sealing key: the environment first, then a file.
//
// A file is the better answer for a service manager, which can give one to a
// process without it appearing in `ps` or in an image layer. The environment is
// the convenient one, and it is first because a node that has been given both
// meant the explicit one.
func Key(path string) ([]byte, error) {
	if encoded := strings.TrimSpace(os.Getenv(KeyVar)); encoded != "" {
		return decodeKey(encoded, KeyVar)
	}
	if path == "" {
		return nil, ErrNoKey
	}

	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("secret: %w", err)
	}
	return decodeKey(strings.TrimSpace(string(body)), path)
}

func decodeKey(encoded, from string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("secret: %s is not base64: %w", from, err)
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("secret: %s is %d bytes, want %d", from, len(key), KeySize)
	}
	return key, nil
}

// Open returns the secret store, creating the bucket if it is not there.
func Open(ctx context.Context, conn *bus.Conn, key []byte) (*Store, error) {
	if len(key) != KeySize {
		return nil, ErrNoKey
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secret: %w", err)
	}
	seal, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secret: %w", err)
	}

	js, err := jetstream.New(conn.Conn)
	if err != nil {
		return nil, fmt.Errorf("secret: jetstream: %w", err)
	}

	kv, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:      Bucket,
		Description: "scour secrets, sealed",
		// One revision. A store that remembers every previous value is a store
		// that leaks the one you rotated away from.
		History: 1,
	})
	if err != nil {
		return nil, fmt.Errorf("secret: %s: %w", Bucket, err)
	}
	return &Store{kv: kv, seal: seal}, nil
}

// Set stores a value, sealed.
//
// The caller reads the value from stdin rather than an argument, because an
// argument is in the shell history and in the process table. Nothing here can
// enforce that, which is why it is said in the command's own documentation as
// well.
func (s *Store) Set(ctx context.Context, name string, value []byte) error {
	if err := checkName(name); err != nil {
		return err
	}
	if len(value) == 0 {
		return errors.New("secret: an empty secret is not one")
	}

	nonce := make([]byte, s.seal.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("secret: %w", err)
	}

	// The name is authenticated as well as the value, so a sealed value cannot
	// be moved from one name to another by whoever can write to the bucket.
	sealed := s.seal.Seal(nonce, nonce, value, []byte(name))

	if _, err := s.kv.Put(ctx, name, sealed); err != nil {
		return fmt.Errorf("secret: store %q: %w", name, err)
	}
	return nil
}

// Resolve opens a secret, which is what a node does while building a plugin.
//
// Not called Get, and not exposed to a person: the value goes into a plugin's
// configuration and nowhere else.
func (s *Store) Resolve(ctx context.Context, name string) ([]byte, error) {
	entry, err := s.kv.Get(ctx, name)
	switch {
	case errors.Is(err, jetstream.ErrKeyNotFound):
		return nil, fmt.Errorf("%w: %q", ErrNoSecret, name)
	case err != nil:
		return nil, fmt.Errorf("secret: read %q: %w", name, err)
	}

	sealed := entry.Value()
	if len(sealed) < s.seal.NonceSize() {
		return nil, fmt.Errorf("%w: %q", ErrSealed, name)
	}

	nonce, body := sealed[:s.seal.NonceSize()], sealed[s.seal.NonceSize():]
	value, err := s.seal.Open(nil, nonce, body, []byte(name))
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrSealed, name)
	}
	return value, nil
}

// Names lists what has been set, which is all a person can see.
func (s *Store) Names(ctx context.Context) ([]string, error) {
	names, err := s.kv.Keys(ctx)
	switch {
	case errors.Is(err, jetstream.ErrNoKeysFound):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("secret: list: %w", err)
	}
	return names, nil
}

// Delete removes a secret, and says so if there was none.
//
// Purge succeeds on a key that is not there, so this reported "removed" for any
// name at all: an operator rotating a credential mistyped it, was told it was
// gone, and the real secret stayed in the bucket and stayed resolvable by every
// node. That is the one mistake a delete has to be loud about, because the
// whole point of making it was that the value should stop working.
//
// Not idempotent on purpose, unlike the deletes elsewhere in this program that
// say so in as many words. Nothing here retries a delete, and a name that is
// not there is a name somebody typed wrongly.
func (s *Store) Delete(ctx context.Context, name string) error {
	if _, err := s.kv.Get(ctx, name); err != nil {
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			return fmt.Errorf("%w: %s", ErrNoSecret, name)
		}
		return fmt.Errorf("secret: delete %q: %w", name, err)
	}
	if err := s.kv.Purge(ctx, name); err != nil {
		return fmt.Errorf("secret: delete %q: %w", name, err)
	}
	return nil
}

func checkName(name string) error {
	switch {
	case name == "":
		return errors.New("secret: a secret needs a name")
	case strings.ContainsAny(name, " \t.*>/\\"):
		return fmt.Errorf("secret: %q cannot be a name: no spaces, dots, slashes or wildcards", name)
	}
	return nil
}
