// Package judge turns natural-language robot requests into typed
// movement plans using TypeSafe System One judgements.
//
// The model only interprets: it classifies the movement, the side and
// whether the request is bounded, and grades how far. Code owns the
// rest — the calibration from tobbie.StepDuration and
// tobbie.TurnCalibration180, the exclusivity of the movement modes,
// the caps, and the refusal to act when a judgement is too uncertain.
//
// The question wording in move.go was tuned and measured with
// tools/nl-eval; re-run that harness after changing it.
package judge

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
)

const (
	defaultEndpoint = "https://api.typesafe.ai/v1/systemone"
	defaultModel    = "jev-latest"
	maxBodySnippet  = 300
)

// Question is one TypeSafe System One question. Instructions and
// Criteria take the shapes the API accepts: strings, objects or
// arrays.
type Question struct {
	Type         string `json:"type"`
	Instructions any    `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// Answer is one typed answer. Noul is set for noul questions, Choice
// and Probabilities for choice questions; Confidence is only carried
// by choice and score answers.
type Answer struct {
	Type          string             `json:"type"`
	Noul          float64            `json:"noul"`
	Choice        string             `json:"choice"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
}

// Answers maps question ids to their answers.
type Answers map[string]Answer

// Client is a minimal TypeSafe System One client. NewClient reads the
// API key from TYPESAFE_API_KEY; Configured reports whether the client
// can be used.
type Client struct {
	apiKey   string
	model    string
	endpoint string
	http     *http.Client
}

// NewClient builds a client from the environment. TYPESAFE_MODEL pins
// the model (default "jev-latest"); TYPESAFE_BASE_URL points at a
// different TypeSafe-compatible deployment.
func NewClient() *Client {
	c := &Client{
		apiKey:   os.Getenv("TYPESAFE_API_KEY"),
		model:    defaultModel,
		endpoint: defaultEndpoint,
		http:     &http.Client{Timeout: 20 * time.Second},
	}
	if v := os.Getenv("TYPESAFE_MODEL"); v != "" {
		c.model = v
	}
	if v := os.Getenv("TYPESAFE_BASE_URL"); v != "" {
		c.endpoint = strings.TrimRight(v, "/") + "/v1/systemone"
	}
	return c
}

// Configured reports whether an API key is available.
func (c *Client) Configured() bool { return c.apiKey != "" }

// ask evaluates state against questions, retrying on the API's
// transient statuses.
func (c *Client) ask(ctx context.Context, state any, questions map[string]Question) (Answers, error) {
	if !c.Configured() {
		return nil, fmt.Errorf("judge: TYPESAFE_API_KEY is not set")
	}
	body, err := json.Marshal(struct {
		State     any                 `json:"state"`
		Model     string              `json:"model"`
		Questions map[string]Question `json:"questions"`
	}{state, c.model, questions})
	if err != nil {
		return nil, fmt.Errorf("judge: encode request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(1<<attempt) * 500 * time.Millisecond):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}

		switch resp.StatusCode {
		case http.StatusOK:
			var out struct {
				Answers Answers `json:"answers"`
			}
			if err := json.Unmarshal(raw, &out); err != nil {
				return nil, fmt.Errorf("judge: decode response: %w", err)
			}
			return out.Answers, nil
		case http.StatusTooManyRequests, http.StatusBadGateway,
			http.StatusServiceUnavailable, 529:
			lastErr = fmt.Errorf("judge: typesafe returned %s: %s", resp.Status, snippet(raw))
			continue
		default:
			return nil, fmt.Errorf("judge: typesafe returned %s: %s", resp.Status, snippet(raw))
		}
	}
	return nil, fmt.Errorf("judge: %w", lastErr)
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) <= maxBodySnippet {
		return s
	}
	return s[:maxBodySnippet] + "..."
}
