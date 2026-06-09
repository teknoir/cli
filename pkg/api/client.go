package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"teknoir/cli/pkg/config"
)

// Request performs an authenticated API request and handles non-JSON responses gracefully.
func Request(ctx context.Context, method, url, token string, result any) error {
	return RequestWithBody(ctx, method, url, token, nil, result)
}

// RequestWithBody performs an authenticated API request with an optional JSON body.
func RequestWithBody(ctx context.Context, method, url, token string, body any, result any) error {
	isDebug := config.Debug
	if isDebug {
		fmt.Printf("DEBUG: Request: %s %s\n", method, url)
	}

	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBody)
		if isDebug {
			fmt.Printf("DEBUG: Request Body: %s\n", string(jsonBody))
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if isDebug {
			fmt.Printf("DEBUG: Response Status: %s\n", resp.Status)
		}
		return fmt.Errorf("API request failed with status: %s", resp.Status)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	if isDebug {
		fmt.Printf("DEBUG: Response Body: %s\n", string(respBody))
	}

	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "text/html") {
		if isDebug {
			fmt.Printf("DEBUG: Response Content-Type: %s\n", contentType)
		}
		return fmt.Errorf("received HTML instead of JSON. This usually means you are being redirected to a login page. Please refer to KEYCLOAK.md for infrastructure configuration")
	}

	// Double check for HTML if Content-Type was missing or misleading
	trimmedBody := strings.TrimSpace(string(respBody))
	if strings.HasPrefix(trimmedBody, "<") {
		return fmt.Errorf("received HTML instead of JSON. This usually means you are being redirected to a login page. Please refer to KEYCLOAK.md for infrastructure configuration")
	}

	if err := json.Unmarshal(respBody, result); err != nil {
		return fmt.Errorf("failed to decode JSON response: %w", err)
	}

	return nil
}
