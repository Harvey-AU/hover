// Package loops provides a client for the Loops.so email API.
// See https://loops.so/docs/api-reference for full documentation.
package loops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	baseURL        = "https://app.loops.so/api/v1"
	defaultTimeout = 10 * time.Second
)

type Client struct {
	apiKey     string
	httpClient *http.Client
}

func New(apiKey string) *Client {
	return &Client{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: defaultTimeout,
		},
	}
}

type TransactionalRequest struct {
	Email           string         `json:"email"`
	TransactionalID string         `json:"transactionalId"`
	DataVariables   map[string]any `json:"dataVariables,omitempty"`
	AddToAudience   bool           `json:"addToAudience,omitempty"`
	IdempotencyKey  string         `json:"-"` // dedupes within 24h on the Loops side
}

func (c *Client) SendTransactional(ctx context.Context, req *TransactionalRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("loops: failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/transactional", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("loops: failed to create request: %w", err)
	}

	c.setHeaders(httpReq)
	if req.IdempotencyKey != "" {
		httpReq.Header.Set("Idempotency-Key", req.IdempotencyKey)
	}

	return c.do(httpReq)
}

// One of Email or UserID is required.
type EventRequest struct {
	Email           string         `json:"email,omitempty"`
	UserID          string         `json:"userId,omitempty"`
	EventName       string         `json:"eventName"`
	EventProperties map[string]any `json:"eventProperties,omitempty"`
}

func (c *Client) SendEvent(ctx context.Context, req *EventRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("loops: failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/events/send", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("loops: failed to create request: %w", err)
	}

	c.setHeaders(httpReq)
	return c.do(httpReq)
}

type ContactRequest struct {
	Email      string         `json:"email"`
	UserID     string         `json:"userId,omitempty"`
	FirstName  string         `json:"firstName,omitempty"`
	LastName   string         `json:"lastName,omitempty"`
	Properties map[string]any `json:"properties,omitempty"`
}

func (c *Client) CreateContact(ctx context.Context, req *ContactRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("loops: failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/contacts/create", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("loops: failed to create request: %w", err)
	}

	c.setHeaders(httpReq)
	return c.do(httpReq)
}

func (c *Client) UpdateContact(ctx context.Context, req *ContactRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("loops: failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, baseURL+"/contacts/update", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("loops: failed to create request: %w", err)
	}

	c.setHeaders(httpReq)
	return c.do(httpReq)
}

type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("loops: API error %d: %s", e.StatusCode, e.Message)
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
}

func (c *Client) do(req *http.Request) error {
	resp, err := c.httpClient.Do(req) //nolint:gosec // G704: all requests target hardcoded baseURL (app.loops.so)
	if err != nil {
		return fmt.Errorf("loops: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	body, _ := io.ReadAll(resp.Body)

	var apiResp struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &apiResp) == nil && apiResp.Message != "" {
		return &APIError{StatusCode: resp.StatusCode, Message: apiResp.Message}
	}

	return &APIError{StatusCode: resp.StatusCode, Message: string(body)}
}
