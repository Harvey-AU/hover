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

// Client provides methods to interact with the Loops.so API.
type Client struct {
	apiKey     string
	httpClient *http.Client
}

// New creates a new Loops client with the given API key.
func New(apiKey string) *Client {
	return &Client{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: defaultTimeout,
		},
	}
}

// TransactionalRequest contains the fields for sending a transactional email.
type TransactionalRequest struct {
	// Email is the recipient's email address (required).
	Email string `json:"email"`
	// TransactionalID is the template ID from the Loops dashboard (required).
	TransactionalID string `json:"transactionalId"`
	// DataVariables are template variables to inject into the email (optional).
	DataVariables map[string]any `json:"dataVariables,omitempty"`
	// AddToAudience creates a contact if one doesn't exist (optional, default false).
	AddToAudience bool `json:"addToAudience,omitempty"`
	// IdempotencyKey prevents duplicate sends within 24 hours (optional).
	IdempotencyKey string `json:"-"`
}

// SendTransactional sends a transactional email via the Loops API.
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

// EventRequest contains the fields for sending an event.
type EventRequest struct {
	// Email is the contact's email address (required if UserID not set).
	Email string `json:"email,omitempty"`
	// UserID is the contact's user ID (required if Email not set).
	UserID string `json:"userId,omitempty"`
	// EventName is the name of the event to trigger (required).
	EventName string `json:"eventName"`
	// EventProperties are custom properties for the event (optional).
	EventProperties map[string]any `json:"eventProperties,omitempty"`
}

// SendEvent sends an event to trigger automations in Loops.
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

// ContactRequest contains the fields for creating or updating a contact.
type ContactRequest struct {
	// Email is the contact's email address (required).
	Email string `json:"email"`
	// UserID is an optional external identifier for the contact.
	UserID string `json:"userId,omitempty"`
	// FirstName is the contact's first name (optional).
	FirstName string `json:"firstName,omitempty"`
	// LastName is the contact's last name (optional).
	LastName string `json:"lastName,omitempty"`
	// Properties are custom contact properties (optional).
	Properties map[string]any `json:"properties,omitempty"`
}

// CreateContact creates a new contact in Loops.
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

// UpdateContact updates an existing contact in Loops.
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

// APIError represents an error response from the Loops API.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("loops: API error %d: %s", e.StatusCode, e.Message)
}

// setHeaders applies the standard auth and content-type headers.
func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
}

// do executes the request and handles the response.
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

	// Parse structured error if available
	var apiResp struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &apiResp) == nil && apiResp.Message != "" {
		return &APIError{StatusCode: resp.StatusCode, Message: apiResp.Message}
	}

	return &APIError{StatusCode: resp.StatusCode, Message: string(body)}
}
