// SPDX-License-Identifier: GPL-3.0-or-later

package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func init() {
	Register("ollama", NewOllama)
}

// DefaultOllamaEndpoint is where ollama listens unless told otherwise.
const DefaultOllamaEndpoint = "http://localhost:11434"

// Ollama talks to a local model server.
//
// It is the offline path, and it matters for more than privacy: induction asks
// the same question thousands of times, and a local model turns a per-call
// bill into a fixed cost in electricity. It constrains the answer with
// ollama's `format` field, which is a JSON Schema the server enforces during
// decoding, so a small model that could not reliably be talked into clean JSON
// still returns clean JSON.
type Ollama struct {
	endpoint string
	model    string
	client   *http.Client
}

// NewOllama builds the provider.
func NewOllama(cfg Config) (Provider, error) {
	if cfg.Model == "" {
		return nil, fmt.Errorf("ai block %q needs a model, such as gemma3:270m", cfg.Name)
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = DefaultOllamaEndpoint
	}
	return &Ollama{
		endpoint: strings.TrimRight(endpoint, "/"),
		model:    cfg.Model,
		client:   &http.Client{Timeout: cfg.timeout()},
	}, nil
}

// Name implements [Provider].
func (o *Ollama) Name() string { return "ollama:" + o.model }

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Format   map[string]any  `json:"format,omitempty"`
	Options  map[string]any  `json:"options,omitempty"`
}

type ollamaChatResponse struct {
	Message ollamaMessage `json:"message"`
	Error   string        `json:"error"`
}

// JSON implements [Provider].
func (o *Ollama) JSON(ctx context.Context, req Request) ([]byte, error) {
	if req.Schema == nil {
		return nil, fmt.Errorf("ollama: a request needs a schema")
	}

	body := ollamaChatRequest{
		Model:  o.model,
		Stream: false,
		Format: req.Schema,
		// Judgement should be reproducible: the same page scored twice must
		// not induce two different models.
		Options: map[string]any{"temperature": 0},
	}
	if req.System != "" {
		body.Messages = append(body.Messages, ollamaMessage{Role: "system", Content: req.System})
	}
	body.Messages = append(body.Messages, ollamaMessage{Role: "user", Content: req.Prompt})
	if req.MaxTokens > 0 {
		body.Options["num_predict"] = req.MaxTokens
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("ollama: encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint+"/api/chat", bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("ollama: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: %w", err)
	}
	defer resp.Body.Close()

	// Bounded because a runaway model should not be able to exhaust memory.
	const maxAnswer = 1 << 20
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return nil, fmt.Errorf("ollama: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama: %s: %s", resp.Status, strings.TrimSpace(string(answer)))
	}

	var out ollamaChatResponse
	if err := json.Unmarshal(answer, &out); err != nil {
		return nil, fmt.Errorf("ollama: decode response: %w", err)
	}
	if out.Error != "" {
		return nil, fmt.Errorf("ollama: %s", out.Error)
	}

	content := strings.TrimSpace(out.Message.Content)
	if content == "" {
		return nil, fmt.Errorf("ollama: empty answer from %s", o.model)
	}
	return []byte(content), nil
}

type ollamaEmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type ollamaEmbedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
	Error      string      `json:"error"`
}

// Embed implements [Embedder].
func (o *Ollama) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	raw, err := json.Marshal(ollamaEmbedRequest{Model: o.model, Input: texts})
	if err != nil {
		return nil, fmt.Errorf("ollama: encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint+"/api/embed", bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("ollama: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: %w", err)
	}
	defer resp.Body.Close()

	answer, err := io.ReadAll(io.LimitReader(resp.Body, 1<<24))
	if err != nil {
		return nil, fmt.Errorf("ollama: read response: %w", err)
	}

	var out ollamaEmbedResponse
	if err := json.Unmarshal(answer, &out); err != nil {
		return nil, fmt.Errorf("ollama: decode response: %w", err)
	}
	if out.Error != "" {
		// Worth passing through verbatim: the usual cause is a server started
		// without embedding support, and the server says so plainly.
		return nil, fmt.Errorf("ollama: %s", out.Error)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama: %s", resp.Status)
	}
	if len(out.Embeddings) != len(texts) {
		return nil, fmt.Errorf("ollama: asked for %d embeddings, got %d", len(texts), len(out.Embeddings))
	}
	return out.Embeddings, nil
}
