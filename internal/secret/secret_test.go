// SPDX-License-Identifier: GPL-3.0-or-later

package secret_test

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclparse"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/secret"
)

func store(t *testing.T) *secret.Store {
	t.Helper()
	s, _ := storeOn(t)
	return s
}

// storeOn is [store] and also the connection under it, for the test that has to
// write into the bucket the way an attacker who has it would.
func storeOn(t *testing.T) (*secret.Store, *bus.Conn) {
	t.Helper()

	conn, err := bus.Connect(bus.Options{Name: "test", StoreDir: t.TempDir()})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	encoded, err := secret.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}

	s, err := secret.Open(context.Background(), conn, key)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s, conn
}

func TestASecretGoesInSealedAndComesOutWhole(t *testing.T) {
	s := store(t)
	ctx := context.Background()

	if err := s.Set(ctx, "acme-s3-key", []byte("hunter2")); err != nil {
		t.Fatalf("set: %v", err)
	}

	value, err := s.Resolve(ctx, "acme-s3-key")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if string(value) != "hunter2" {
		t.Errorf("value = %q", value)
	}
}

// TestWhatIsStoredIsNotTheValue. A KV bucket is a file on somebody's disk and a
// stream anybody with the address can read.
func TestWhatIsStoredIsNotTheValue(t *testing.T) {
	conn, err := bus.Connect(bus.Options{Name: "test", StoreDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	encoded, _ := secret.NewKey()
	key, _ := base64.StdEncoding.DecodeString(encoded)

	ctx := context.Background()
	s, err := secret.Open(ctx, conn, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set(ctx, "acme", []byte("hunter2")); err != nil {
		t.Fatal(err)
	}

	// Another store on the same bucket with a different key: what it sees is
	// what anybody with the address sees.
	otherEncoded, _ := secret.NewKey()
	other, _ := base64.StdEncoding.DecodeString(otherEncoded)

	stranger, err := secret.Open(ctx, conn, other)
	if err != nil {
		t.Fatal(err)
	}
	_, err = stranger.Resolve(ctx, "acme")
	if !errors.Is(err, secret.ErrSealed) {
		t.Errorf("err = %v, want the wrong key to be refused", err)
	}

	// The names are not secret and the values are.
	names, err := stranger.Names(ctx)
	if err != nil || len(names) != 1 || names[0] != "acme" {
		t.Errorf("names = %v, %v", names, err)
	}
}

// TestASealedValueCannotBeMovedBetweenNames, so whoever can write to the bucket
// cannot swap one credential for another.
func TestASealedValueCannotBeMovedBetweenNames(t *testing.T) {
	s, conn := storeOn(t)
	ctx := context.Background()

	if err := s.Set(ctx, "staging", []byte("staging-key")); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(ctx, "production", []byte("production-key")); err != nil {
		t.Fatal(err)
	}

	// Both open under their own names, which holds whether or not the name is
	// authenticated and so says nothing on its own. That was the whole of this
	// test, and removing the name from both Seal and Open left it passing.
	if value, err := s.Resolve(ctx, "staging"); err != nil || string(value) != "staging-key" {
		t.Errorf("staging = %q, %v", value, err)
	}
	if value, err := s.Resolve(ctx, "production"); err != nil || string(value) != "production-key" {
		t.Errorf("production = %q, %v", value, err)
	}

	// The claim is about somebody who can write to the bucket, so this writes
	// to the bucket: staging's sealed bytes, stored under production's name.
	// Nothing about them is forged - they are exactly what the store wrote -
	// and the only thing standing between them and being served as the
	// production credential is that the name is authenticated.
	js, err := jetstream.New(conn.Conn)
	if err != nil {
		t.Fatal(err)
	}
	kv, err := js.KeyValue(ctx, secret.Bucket)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := kv.Get(ctx, "staging")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kv.Put(ctx, "production", entry.Value()); err != nil {
		t.Fatal(err)
	}

	value, err := s.Resolve(ctx, "production")
	if err == nil {
		t.Errorf("a value sealed as %q was served as %q: %q", "staging", "production", value)
	}
	if !errors.Is(err, secret.ErrSealed) {
		t.Errorf("moving a sealed value between names reports %v, want ErrSealed", err)
	}
}

func TestASecretNobodySetSaysSo(t *testing.T) {
	if _, err := store(t).Resolve(context.Background(), "nothing"); !errors.Is(err, secret.ErrNoSecret) {
		t.Errorf("err = %v, want ErrNoSecret", err)
	}
}

func TestNamesAndDelete(t *testing.T) {
	s := store(t)
	ctx := context.Background()

	if names, _ := s.Names(ctx); len(names) != 0 {
		t.Errorf("a fresh store holds %v", names)
	}
	if err := s.Set(ctx, "acme", []byte("hunter2")); err != nil {
		t.Fatal(err)
	}
	if names, _ := s.Names(ctx); len(names) != 1 {
		t.Errorf("names = %v", names)
	}
	if err := s.Delete(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve(ctx, "acme"); !errors.Is(err, secret.ErrNoSecret) {
		t.Errorf("the secret survived being deleted: %v", err)
	}
}

func TestWhatIsNotASecret(t *testing.T) {
	s := store(t)
	ctx := context.Background()

	if err := s.Set(ctx, "acme", nil); err == nil {
		t.Error("an empty secret was accepted")
	}
	for _, name := range []string{"", "two words", "with.dots", "a/b"} {
		if err := s.Set(ctx, name, []byte("x")); err == nil {
			t.Errorf("accepted %q as a name", name)
		}
	}
}

func TestAStoreNeedsARealKey(t *testing.T) {
	conn, err := bus.Connect(bus.Options{Name: "test", StoreDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if _, err := secret.Open(context.Background(), conn, []byte("too short")); !errors.Is(err, secret.ErrNoKey) {
		t.Errorf("err = %v, want ErrNoKey", err)
	}
}

// TestTheKeyComesFromOutsideTheStore, which is the point: a bucket sealed with
// a key kept in the bucket is a bucket that is not sealed.
func TestTheKeyComesFromOutsideTheStore(t *testing.T) {
	encoded, err := secret.NewKey()
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv(secret.KeyVar, encoded)
	key, err := secret.Key("")
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	if len(key) != secret.KeySize {
		t.Errorf("key is %d bytes", len(key))
	}

	// A file is the other way, and the better one for a service manager: it can
	// give a key to a process without it appearing in ps or in an image layer.
	t.Setenv(secret.KeyVar, "")
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte(encoded+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fromFile, err := secret.Key(path)
	if err != nil {
		t.Fatalf("key from file: %v", err)
	}
	if string(fromFile) != string(key) {
		t.Error("the file and the environment disagree about the key")
	}

	if _, err := secret.Key(""); !errors.Is(err, secret.ErrNoKey) {
		t.Errorf("a node with no key anywhere said %v", err)
	}
	if _, err := secret.Key(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("a key file that is not there was accepted")
	}
}

func TestAKeyThatIsNotOne(t *testing.T) {
	t.Setenv(secret.KeyVar, "not base64 at all!!")
	if _, err := secret.Key(""); err == nil {
		t.Error("accepted a key that is not base64")
	}

	t.Setenv(secret.KeyVar, base64.StdEncoding.EncodeToString([]byte("short")))
	if _, err := secret.Key(""); err == nil {
		t.Error("accepted a key of the wrong length")
	}
}

// TestSecretResolvesWhereThePluginIsBuiltAndNowhereElse.
//
// The whole claim. The same document, evaluated with the plugin-building
// context and with every other one, and only the first turns the call into a
// credential.
func TestSecretResolvesWhereThePluginIsBuiltAndNowhereElse(t *testing.T) {
	s := store(t)
	ctx := context.Background()

	if err := s.Set(ctx, "acme-s3-key", []byte("hunter2")); err != nil {
		t.Fatal(err)
	}

	body := configOf(t, `
bucket = "pages"
key    = secret("acme-s3-key")
`)

	var config struct {
		Bucket string `hcl:"bucket,optional"`
		Key    string `hcl:"key,optional"`
	}

	// The node building the plugin.
	if diags := gohcl.DecodeBody(body, secret.Eval(ctx, s), &config); diags.HasErrors() {
		t.Fatalf("decode: %v", diags)
	}
	if config.Key != "hunter2" {
		t.Errorf("key = %q", config.Key)
	}

	// Anywhere else: no secret function at all, which is every context but the
	// one above.
	var elsewhere struct {
		Bucket string `hcl:"bucket,optional"`
		Key    string `hcl:"key,optional"`
	}
	diags := gohcl.DecodeBody(body, nil, &elsewhere)
	if !diags.HasErrors() {
		t.Error("a context with no secret function resolved one")
	}
	if elsewhere.Key != "" {
		t.Errorf("the value leaked into a context that should not have it: %q", elsewhere.Key)
	}
}

// TestANodeWithNoSecretsRefusesRatherThanReturningNothing. An empty credential
// is a plugin that fails later with a message about authentication rather than
// about configuration.
func TestANodeWithNoSecretsRefusesRatherThanReturningNothing(t *testing.T) {
	var config struct {
		Key string `hcl:"key,optional"`
	}

	diags := gohcl.DecodeBody(configOf(t, `key = secret("acme")`), secret.Missing(context.Background()), &config)
	if !diags.HasErrors() {
		t.Fatal("a node with no secrets answered a secret")
	}
	if !strings.Contains(diags.Error(), "acme") {
		t.Errorf("the error does not say which secret: %v", diags)
	}
	if config.Key != "" {
		t.Errorf("key = %q, want nothing", config.Key)
	}
}

func TestASecretNobodySetFailsThePluginRatherThanTheDocument(t *testing.T) {
	var config struct {
		Key string `hcl:"key,optional"`
	}

	diags := gohcl.DecodeBody(configOf(t, `key = secret("never-set")`),
		secret.Eval(context.Background(), store(t)), &config)
	if !diags.HasErrors() {
		t.Fatal("a secret nobody set resolved to something")
	}
	if !strings.Contains(diags.Error(), "never-set") {
		t.Errorf("the error does not name it: %v", diags)
	}
}

func configOf(t *testing.T, src string) hcl.Body {
	t.Helper()

	parsed, diags := hclparse.NewParser().ParseHCL([]byte(src), "job.hcl")
	if diags.HasErrors() {
		t.Fatalf("parse: %v", diags)
	}
	return parsed.Body
}
