// SPDX-License-Identifier: GPL-3.0-or-later

package cache_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/rangertaha/scour/internal/registry/registrytest"
	"io"
	"iter"
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/cache"
)

// stub is the smallest thing that satisfies [cache.Store], so this package can
// be tested without dragging a backend in.
type stub struct {
	put    func(context.Context, string, io.Reader) error
	closed bool
}

func (s *stub) Put(ctx context.Context, key string, r io.Reader) error {
	if s.put != nil {
		return s.put(ctx, key, r)
	}
	return nil
}

func (s *stub) Get(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("body")), nil
}
func (s *stub) Has(context.Context, string) (bool, error) { return true, nil }
func (s *stub) Delete(context.Context, string) error      { return nil }
func (s *stub) Keys(context.Context) iter.Seq2[string, error] {
	return func(func(string, error) bool) {}
}
func (s *stub) Close() error { s.closed = true; return nil }

func TestKeyIsStableAndDistinct(t *testing.T) {
	a := cache.Key("https://example.com/one")
	b := cache.Key("https://example.com/two")

	if a == b {
		t.Error("two URLs share a key")
	}
	if a != cache.Key("https://example.com/one") {
		t.Error("the same URL produced two keys")
	}
	// A digest, so it is a safe filename whatever the URL was.
	if len(a) != 64 {
		t.Errorf("key is %d characters, want a sha256 hex digest", len(a))
	}
	if err := cache.CheckKey(a); err != nil {
		t.Errorf("Key produced something CheckKey refuses: %v", err)
	}
}

// TestKeyHandlesURLsAFilesystemWouldNot is why keys are hashed at all.
func TestKeyHandlesURLsAFilesystemWouldNot(t *testing.T) {
	for _, url := range []string{
		"https://example.com/a/b/c?q=1&r=2#frag",
		"https://example.com/" + strings.Repeat("long/", 200),
		"https://example.com/ünïcödé",
		"",
	} {
		if err := cache.CheckKey(cache.Key(url)); err != nil {
			t.Errorf("Key(%q) is not usable: %v", url, err)
		}
	}
}

func TestCheckKeyAcceptsWhatItShould(t *testing.T) {
	for _, key := range []string{"a", "readable-key", "with_underscore", "with.dot", "0123456789"} {
		if err := cache.CheckKey(key); err != nil {
			t.Errorf("CheckKey(%q) = %v, want nil", key, err)
		}
	}
}

// TestCheckKeyRefusesTraversal is the security case: a key can arrive from a
// database row written by an older version, so it is never trusted.
func TestCheckKeyRefusesTraversal(t *testing.T) {
	for name, key := range map[string]string{
		"empty":     "",
		"dot":       ".",
		"dotdot":    "..",
		"escape":    "../escape",
		"absolute":  "/etc/passwd",
		"slash":     "a/b",
		"backslash": `a\b`,
		"space":     "with space",
		"semicolon": "semi;colon",
		"null":      "null\x00byte",
		"tilde":     "~root",
		"toolong":   strings.Repeat("a", 513),
	} {
		t.Run(name, func(t *testing.T) {
			err := cache.CheckKey(key)
			if err == nil {
				t.Fatalf("CheckKey(%q) accepted it", key)
			}
			if !errors.Is(err, cache.ErrBadKey) {
				t.Errorf("err = %v, want ErrBadKey", err)
			}
		})
	}
}

func TestPutBytesAndGetBytes(t *testing.T) {
	ctx := context.Background()

	var got []byte
	s := &stub{put: func(_ context.Context, _ string, r io.Reader) error {
		b, err := io.ReadAll(r)
		got = b
		return err
	}}

	if err := cache.PutBytes(ctx, s, "k", []byte("hello")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("the store received %q", got)
	}

	body, err := cache.GetBytes(ctx, s, "k")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(body) != "body" {
		t.Errorf("got %q", body)
	}
}

func TestGetBytesPassesTheErrorThrough(t *testing.T) {
	_, err := cache.GetBytes(context.Background(), &failing{}, "k")
	if !errors.Is(err, cache.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

type failing struct{ stub }

func (*failing) Get(context.Context, string) (io.ReadCloser, error) { return nil, cache.ErrNotFound }

// The registry wrappers. Backends register themselves from their own packages,
// so this one registers its own to avoid depending on which are imported.

func TestRegisterAndNew(t *testing.T) {
	const name = "test-backend-registered"

	built := false
	register(t, name, func(context.Context, cache.Config) (cache.Store, error) {
		built = true
		return &stub{}, nil
	})

	if !cache.Has(name) {
		t.Fatal("a registered backend is not there")
	}
	if _, err := cache.New(context.Background(), cache.Config{Backend: name}); err != nil {
		t.Fatalf("new: %v", err)
	}
	if !built {
		t.Error("the factory was not called")
	}

	var found bool
	for _, b := range cache.Backends() {
		if b == name {
			found = true
		}
	}
	if !found {
		t.Errorf("Backends() does not list it: %v", cache.Backends())
	}
}

func TestNewRefusesAnUnknownBackend(t *testing.T) {
	_, err := cache.New(context.Background(), cache.Config{Backend: "carrier-pigeon"})
	if err == nil {
		t.Fatal("built a backend that does not exist")
	}
	if !strings.Contains(err.Error(), "carrier-pigeon") {
		t.Errorf("error does not name it: %v", err)
	}
}

func TestDefaultBackendIsLocal(t *testing.T) {
	if cache.DefaultBackend != "local" {
		t.Errorf("DefaultBackend = %q, want local so a laptop needs nothing installed", cache.DefaultBackend)
	}
}

// TestAConfigDoesNotPrintItsCredentials.
//
// A config is exactly the sort of thing that ends up in a log line, an error
// message or a debugger's output, and the default formatting of a struct prints
// every field. This is the guard that makes the wrong thing not merely
// discouraged but not what happens.
func TestAConfigDoesNotPrintItsCredentials(t *testing.T) {
	// Obvious placeholders. A test that carried a real-looking key would be a
	// test somebody eventually copies.
	cfg := cache.Config{
		Backend:      "s3",
		Bucket:       "pages",
		Region:       "eu-west-2",
		AccessKey:    "PLACEHOLDER-ACCESS-KEY",
		SecretKey:    "PLACEHOLDER-SECRET-KEY",
		SessionToken: "PLACEHOLDER-SESSION-TOKEN",
		Credentials:  `{"type":"service_account","private_key":"PLACEHOLDER-PRIVATE-KEY"}`,
	}

	for name, printed := range map[string]string{
		"%v":                fmt.Sprintf("%v", cfg),
		"%s":                fmt.Sprintf("%s", cfg),
		"%+v":               fmt.Sprintf("%+v", cfg),
		"%#v":               fmt.Sprintf("%#v", cfg),
		"String":            cfg.String(),
		"in a slog message": fmt.Sprint(cfg),
	} {
		t.Run(name, func(t *testing.T) {
			for _, secret := range []string{
				"PLACEHOLDER-ACCESS-KEY", "PLACEHOLDER-SECRET-KEY",
				"PLACEHOLDER-SESSION-TOKEN", "PLACEHOLDER-PRIVATE-KEY",
			} {
				if strings.Contains(printed, secret) {
					t.Errorf("a credential was printed:\n%s", printed)
				}
			}
			// And what is safe is still there, or the redaction would have
			// made the config useless to debug with.
			for _, want := range []string{"s3", "pages", "eu-west-2"} {
				if !strings.Contains(printed, want) {
					t.Errorf("%q was redacted along with the credentials:\n%s", want, printed)
				}
			}
		})
	}
}

// TestAConfigKnowsWhetherItCarriesACredential, which is what decides whether a
// backend builds its own client or lets the SDK find one.
func TestAConfigKnowsWhetherItCarriesACredential(t *testing.T) {
	if (cache.Config{Bucket: "pages", Region: "eu-west-2"}).Secret() {
		t.Error("a config with no credential says it has one")
	}
	for name, cfg := range map[string]cache.Config{
		"an access key": {AccessKey: "PLACEHOLDER"},
		"a secret key":  {SecretKey: "PLACEHOLDER"},
		"a google key":  {Credentials: "PLACEHOLDER"},
	} {
		if !cfg.Secret() {
			t.Errorf("a config with %s says it has none", name)
		}
	}
}

// register puts a cache backend in the global table for the length of one test.
//
// Every test that needs one of its own goes through this rather than calling
// [cache.Register] directly, because the table is global and registering the
// same name twice panics: a test that registered without removing made this
// whole package impossible to run under `go test -count=2` or, once shuffling
// reordered it, under `-shuffle=on` either. Running the suite repeatedly is how
// a flaky test is found, so a package that cannot be is a package whose
// flakiness nobody will see. The gate runs -count=2 for that reason, which is
// what makes the next test that forgets fail the build rather than ship.
func register(t *testing.T, name string, f cache.Factory) {
	t.Helper()
	cache.Register(name, f)
	t.Cleanup(func() { cache.Unregister(name) })
}

// TestMain fails the package if a test left a name in the global table. See
// [registrytest].
func TestMain(m *testing.M) { registrytest.Main(m, cache.Backends) }
