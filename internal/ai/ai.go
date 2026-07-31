// SPDX-License-Identifier: GPL-3.0-or-later

// Package ai is scour's access to language models.
//
// It deliberately knows nothing about crawling or extraction. A provider
// answers one question, "given this prompt, return JSON matching this schema",
// and everything that wants a model's judgement builds on that. Keeping the
// interface this narrow is what lets a local model and a hosted one be the
// same thing to the rest of scour, and what keeps the expensive, unpredictable
// part of the system behind a seam that can be tested with a stub.
package ai

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Config describes one provider, corresponding to a [[ai]] block.
type Config struct {
	// Name is what [model] refers to this block by.
	Name string
	// Provider selects the implementation: "ollama", "anthropic".
	Provider string
	// Model is the provider's model identifier.
	Model string
	// Effort is a provider-specific hint for how hard to think.
	Effort string
	// Endpoint overrides where the provider is reached, which is how a local
	// model on another machine is used.
	Endpoint string
	// Path points at a model file, for providers that load one.
	Path string
	// APIKeyEnv names the environment variable holding the credential. The
	// key itself is never written to config, so a config file stays safe to
	// commit.
	APIKeyEnv string
	// Timeout bounds a single call.
	Timeout time.Duration
}

func (c Config) timeout() time.Duration {
	if c.Timeout <= 0 {
		return 60 * time.Second
	}
	return c.Timeout
}

// Request is one question for a model.
type Request struct {
	// System sets the model's role. It is separate from Prompt because
	// providers cache it, and because it is the part that does not vary
	// between calls.
	System string
	// Prompt is the question.
	Prompt string
	// Schema is the JSON Schema the answer must satisfy. Requiring one is
	// deliberate: a caller that wanted prose would be asking the wrong package.
	Schema map[string]any
	// MaxTokens caps the answer. Zero means the provider's default.
	MaxTokens int
}

// Provider answers a Request with JSON matching its schema.
//
// Implementations must be safe for concurrent use. The raw bytes are returned
// rather than a decoded value so that the caller owns the shape it expects;
// providers should not need changing when a new kind of question is asked.
type Provider interface {
	// Name identifies the provider for logs and errors.
	Name() string
	// JSON answers req, returning the model's JSON.
	JSON(ctx context.Context, req Request) ([]byte, error)
}

// Embedder is implemented by providers that can also turn text into vectors.
//
// It is a separate interface because most providers do one or the other well,
// and a caller that needs embeddings should fail to find them rather than
// receive something improvised.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Factory builds a provider from its config.
type Factory func(Config) (Provider, error)

var (
	mu       sync.RWMutex
	registry = map[string]Factory{}
)

// Register adds a provider implementation, from init.
func Register(provider string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	registry[provider] = f
}

// New builds the provider a config names.
func New(cfg Config) (Provider, error) {
	if cfg.Provider == "" {
		return nil, fmt.Errorf("ai block %q has no provider", cfg.Name)
	}

	mu.RLock()
	f, ok := registry[cfg.Provider]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown ai provider %q, have %v", cfg.Provider, Providers())
	}
	return f(cfg)
}

// Providers lists the registered implementations.
func Providers() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Has reports whether a provider is registered.
func Has(provider string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := registry[provider]
	return ok
}
