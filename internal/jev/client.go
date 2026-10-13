// Package jev is a minimal client for TypeSafe's System One HTTP API
// (https://docs.typesafe.ai/api), covering the Choice and Score questions
// reflex uses.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// DefaultEndpoint is TypeSafe's evaluation endpoint.
const DefaultEndpoint = "https://api.typesafe.ai/v1/systemone"

// DefaultModel is TypeSafe's flagship model alias.
const DefaultModel = "jev-latest"

// ErrNoAPIKey is returned when the client has no API key configured.
var ErrNoAPIKey = errors.New("jev: no API key configured")

// Question is a typed question. Criteria is a map of option to description
// for a Choice, or an ordered slice of level descriptions for a Score.
type Question struct {
	Type         string `json:"type"`
	Instructions any    `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// Choice builds a Choice question.
func Choice(instructions string, options map[string]string) Question {
	return Question{Type: "choice", Instructions: instructions, Criteria: options}
}

// Score builds a Score question over ordered levels.
func Score(instructions string, levels []string) Question {
	return Question{Type: "score", Instructions: instructions, Criteria: levels}
}

// Request is the body of a System One evaluation.
type Request struct {
	State     any                 `json:"state"`
	Model     string              `json:"model"`
	Questions map[string]Question `json:"questions"`
}

// Answer holds the fields of any answer type; which are set depends on Type.
type Answer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Score         float64            `json:"score,omitempty"`
	Noul          float64            `json:"noul,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
}

// Usage is the token usage for a request.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Response is the body of a successful evaluation.
type Response struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   Usage             `json:"usage"`
}

// APIError is a non-2xx response from the API.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("jev: HTTP %d: %s", e.StatusCode, e.Body)
}

// Client calls the System One API. Timeouts come from the request context.
type Client struct {
	Endpoint   string
	APIKey     string
	Model      string
	HTTPClient *http.Client
}

// Evaluate sends one request. It does not retry: reflex has a tight latency
// budget and falls back to label routing instead.
func (c *Client) Evaluate(ctx context.Context, req Request) (*Response, error) {
	if c.APIKey == "" {
		return nil, ErrNoAPIKey
	}
	if req.Model == "" {
		req.Model = c.Model
	}
	if req.Model == "" {
		req.Model = DefaultModel
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("jev: encode request: %w", err)
	}

	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("jev: build request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	hc := c.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("jev: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(bytes.TrimSpace(msg))}
	}
	var out Response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("jev: decode response: %w", err)
	}
	return &out, nil
}
