// Package storage provides a client for Supabase Storage API
package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client provides methods to interact with Supabase Storage
type Client struct {
	baseURL        string
	publishableKey string // identifies the project (apikey header)
	secretKey      string // authenticates as service_role (Authorization header)
	httpClient     *http.Client
}

// UploadOptions configures a storage upload request.
type UploadOptions struct {
	ContentType     string
	ContentEncoding string
}

// New creates a new Storage client.
// publishableKey is used for the apikey header (project identification).
// secretKey is used for the Authorization header (service_role access).
// If publishableKey is empty, secretKey is used for both headers (legacy behaviour).
func New(supabaseURL, publishableKey, secretKey string) *Client {
	if publishableKey == "" {
		publishableKey = secretKey
	}
	return &Client{
		baseURL:        supabaseURL + "/storage/v1",
		publishableKey: publishableKey,
		secretKey:      secretKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *Client) setAuthHeaders(req *http.Request) {
	if c.secretKey == "" {
		return
	}

	req.Header.Set("apikey", c.publishableKey)
	req.Header.Set("Authorization", "Bearer "+c.secretKey)
}

// Upload uploads a file to the specified bucket and path
// Returns the full path of the uploaded file
func (c *Client) Upload(ctx context.Context, bucket, path string, data []byte, contentType string) (string, error) {
	return c.UploadWithOptions(ctx, bucket, path, data, UploadOptions{ContentType: contentType})
}

// UploadWithOptions uploads a file to the specified bucket and path with optional headers.
// Returns the full path of the uploaded file.
func (c *Client) UploadWithOptions(ctx context.Context, bucket, path string, data []byte, options UploadOptions) (string, error) {
	url := fmt.Sprintf("%s/object/%s/%s", c.baseURL, bucket, path)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	c.setAuthHeaders(req)
	if options.ContentType == "" {
		options.ContentType = "application/octet-stream"
	}
	req.Header.Set("Content-Type", options.ContentType)
	if options.ContentEncoding != "" {
		req.Header.Set("Content-Encoding", options.ContentEncoding)
	}
	req.Header.Set("x-upsert", "true") // Overwrite if exists

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to upload file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("upload failed with status %d: %s", resp.StatusCode, string(body))
	}

	return fmt.Sprintf("%s/%s", bucket, path), nil
}

// GetPublicURL returns the public URL for a file (if bucket is public)
func (c *Client) GetPublicURL(bucket, path string) string {
	return fmt.Sprintf("%s/object/public/%s/%s", c.baseURL, bucket, path)
}

// GetSignedURL returns a signed URL for temporary access to a private file
func (c *Client) GetSignedURL(ctx context.Context, bucket, path string, expiresIn int) (string, error) {
	url := fmt.Sprintf("%s/object/sign/%s/%s", c.baseURL, bucket, path)

	body := fmt.Sprintf(`{"expiresIn":%d}`, expiresIn)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte(body)))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	c.setAuthHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to get signed URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("get signed URL failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse the signed URL from response
	var result struct {
		SignedURL string `json:"signedURL"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to parse signed URL response: %w", err)
	}

	// SignedURL is relative path, prepend base URL
	return c.baseURL + result.SignedURL, nil
}

// Download retrieves a file's contents from the specified bucket and path.
func (c *Client) Download(ctx context.Context, bucket, path string) ([]byte, error) {
	url := fmt.Sprintf("%s/object/%s/%s", c.baseURL, bucket, path)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	c.setAuthHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to download file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("download failed with status %d: %s", resp.StatusCode, string(body))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read download response: %w", err)
	}

	return data, nil
}

// Delete removes a file from storage
func (c *Client) Delete(ctx context.Context, bucket, path string) error {
	url := fmt.Sprintf("%s/object/%s/%s", c.baseURL, bucket, path)

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	c.setAuthHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to delete file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}
