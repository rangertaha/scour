// SPDX-License-Identifier: GPL-3.0-or-later

package export

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rangertaha/scour/internal/store"
)

func init() {
	Register("webhook", newWebhook)
}

// webhook posts records to an HTTP endpoint.
//
// It exists so an export can feed something that is not a filesystem: a queue,
// an ingest endpoint, another service. It posts in batches rather than one
// record per request, because a crawl can produce thousands and a request each
// would be slower than the crawl that found them.
type webhook struct {
	url    string
	token  string
	client *http.Client
}

func newWebhook(cfg Config) (Exporter, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("the webhook exporter needs a url")
	}
	if !strings.HasPrefix(cfg.URL, "http://") && !strings.HasPrefix(cfg.URL, "https://") {
		return nil, fmt.Errorf("webhook url %q is not http or https", cfg.URL)
	}

	token := ""
	if cfg.TokenEnv != "" {
		token = strings.TrimSpace(os.Getenv(cfg.TokenEnv))
		if token == "" {
			// Naming a variable and leaving it unset is almost always a
			// deployment mistake, and posting unauthenticated is a worse
			// answer than saying so.
			return nil, fmt.Errorf("%s is empty, so the webhook would post unauthenticated", cfg.TokenEnv)
		}
	}

	return &webhook{
		url:    cfg.URL,
		token:  token,
		client: &http.Client{Timeout: webhookTimeout},
	}, nil
}

const (
	webhookTimeout = 30 * time.Second
	// batchSize is how many records go in one request. Large enough that a big
	// export is not thousands of round trips, small enough that a receiver
	// does not have to buffer an entire crawl.
	batchSize = 500
)

// Name implements [Exporter].
func (h *webhook) Name() string { return "webhook" }

// payload is what a receiver gets.
type payload struct {
	Item    string       `json:"item"`
	Domain  string       `json:"domain"`
	Batch   int          `json:"batch"`
	Batches int          `json:"batches"`
	Records []jsonRecord `json:"records"`
}

// Export implements [Exporter].
func (h *webhook) Export(ctx context.Context, item string, rows []store.RecordRow) (*Result, error) {
	result := &Result{}
	groups := byDomain(rows)

	for _, domain := range domains(groups) {
		batch := groups[domain]
		total := (len(batch) + batchSize - 1) / batchSize

		for i := 0; i < len(batch); i += batchSize {
			end := min(i+batchSize, len(batch))

			body := payload{
				Item:    item,
				Domain:  domain,
				Batch:   i/batchSize + 1,
				Batches: total,
				Records: make([]jsonRecord, 0, end-i),
			}
			for _, row := range batch[i:end] {
				values := row.Values
				if values == nil {
					values = map[string]string{}
				}
				body.Records = append(body.Records, jsonRecord{
					ID: row.ID, URL: row.URL, Confidence: row.Confidence,
					Format: row.Format, Label: string(row.Label), Values: values,
				})
			}

			if err := h.post(ctx, body); err != nil {
				// Reporting what did go out before the failure matters: a
				// caller retrying blindly would double-deliver everything
				// already accepted.
				return result, err
			}
			result.Records += len(body.Records)
		}
		result.Destinations = append(result.Destinations, fmt.Sprintf("%s (%s)", h.url, domain))
	}
	return result, nil
}

func (h *webhook) post(ctx context.Context, body payload) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode batch: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if h.token != "" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("post to %s: %w", h.url, err)
	}
	defer resp.Body.Close()

	// Read a little of the body on failure: a receiver that rejects a batch
	// usually says why, and discarding that would leave the operator guessing.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("%s returned %s: %s", h.url, resp.Status, strings.TrimSpace(string(detail)))
	}
	// Drained so the connection can be reused. Nothing here needs the body, and
	// a failed drain costs a connection rather than a delivery.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return nil
}
