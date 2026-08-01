// SPDX-License-Identifier: GPL-3.0-or-later

package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

func init() {
	Register("anthropic", NewAnthropic)
}

// DefaultAnthropicModel is used when a config names no model.
const DefaultAnthropicModel = "claude-opus-5"

// DefaultAPIKeyEnv is where the credential is read from when a config does not
// say. The key is never written to config, so a config file stays safe to
// commit and to ship in a package.
const DefaultAPIKeyEnv = "ANTHROPIC_API_KEY"

// Anthropic is the hosted provider.
//
// It is the accurate end of the range rather than the cheap one, which is why
// nothing calls it per link. Induction runs it a few dozen times over a corpus
// and caches every answer; a crawl never touches it.
type Anthropic struct {
	client anthropic.Client
	model  string
	effort string
	max    int64
}

// NewAnthropic builds the provider.
func NewAnthropic(cfg Config) (Provider, error) {
	model := cfg.Model
	if model == "" {
		model = DefaultAnthropicModel
	}

	var opts []option.RequestOption
	// An empty key is not an error here: the SDK also accepts a credential
	// from its own environment and from a logged-in profile, and failing now
	// would break a machine that is authenticated some other way.
	env := cfg.APIKeyEnv
	if env == "" {
		env = DefaultAPIKeyEnv
	}
	if key := os.Getenv(env); key != "" {
		opts = append(opts, option.WithAPIKey(key))
	}
	if cfg.Endpoint != "" {
		opts = append(opts, option.WithBaseURL(cfg.Endpoint))
	}
	if cfg.Timeout > 0 {
		opts = append(opts, option.WithRequestTimeout(cfg.timeout()))
	}

	return &Anthropic{
		client: anthropic.NewClient(opts...),
		model:  model,
		effort: cfg.Effort,
		max:    4096,
	}, nil
}

// Name implements [Provider].
func (a *Anthropic) Name() string { return "anthropic:" + a.model }

// JSON implements [Provider].
func (a *Anthropic) JSON(ctx context.Context, req Request) ([]byte, error) {
	if req.Schema == nil {
		return nil, fmt.Errorf("anthropic: a request needs a schema")
	}

	maxTokens := a.max
	if req.MaxTokens > 0 {
		maxTokens = int64(req.MaxTokens)
	}

	params := anthropic.MessageNewParams{
		Model:     a.model,
		MaxTokens: maxTokens,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(req.Prompt)),
		},
		// Structured outputs, so the answer validates against the schema
		// rather than being fished out of prose. A matcher that had to parse
		// prose would fail in exactly the cases where the judgement was
		// hardest, which is the worst possible place to lose data.
		OutputConfig: anthropic.OutputConfigParam{
			Format: anthropic.JSONOutputFormatParam{Schema: req.Schema},
		},
	}
	if req.System != "" {
		// A separate block because providers cache it, and because it is the
		// part that does not vary between the thousands of calls induction
		// makes.
		params.System = []anthropic.TextBlockParam{{Text: req.System}}
	}
	if a.effort != "" {
		params.OutputConfig.Effort = anthropic.OutputConfigEffort(strings.ToLower(a.effort))
	}

	resp, err := a.client.Messages.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}

	// A refusal is a successful HTTP response with no usable content, so it
	// has to be checked before reading any of it.
	if resp.StopReason == anthropic.StopReasonRefusal {
		return nil, fmt.Errorf("anthropic: request declined (%s)", resp.StopDetails.Category)
	}

	var out strings.Builder
	for _, block := range resp.Content {
		if text, ok := block.AsAny().(anthropic.TextBlock); ok {
			out.WriteString(text.Text)
		}
	}

	answer := strings.TrimSpace(out.String())
	if answer == "" {
		return nil, fmt.Errorf("anthropic: empty answer from %s", a.model)
	}
	if !json.Valid([]byte(answer)) {
		return nil, fmt.Errorf("anthropic: answer was not json: %q", answer)
	}
	return []byte(answer), nil
}
