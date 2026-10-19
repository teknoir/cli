package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/teknoir/cli/pkg/config"
)

// RequestTimeout bounds one API request (connect, send, receive).
const RequestTimeout = 30 * time.Second

// maxBody caps what is read from one response.
const maxBody = 64 << 20

// httpClient is shared by all API calls. It never follows redirects: behind the
// platform gateway a missing or rejected bearer token is a 302 to /oauth2/start,
// and following it would end in the HTML login page of Keycloak.
var httpClient = &http.Client{
	Timeout: RequestTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// Error is a failed API request: the HTTP status and the server's message.
type Error struct {
	Method string
	URL    string // without query string
	Status int
	// Kind is "auth" when the platform asked for a login (302 to /oauth2/start).
	Kind string
	// Code is the server's error code or error name when it sent one.
	Code    string
	Message string
	// RetryAfter is the Retry-After header of a 429, in seconds (0: none).
	RetryAfter int
}

// KindAuth marks an Error that needs a login (Error.Kind).
const KindAuth = "auth"

func (e *Error) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("%s %s: HTTP %d %s", e.Method, e.URL, e.Status, http.StatusText(e.Status))
	}
	if e.Kind == KindAuth {
		return e.Message
	}
	return fmt.Sprintf("%s (HTTP %d)", e.Message, e.Status)
}

// IsAuth reports whether the caller must log in or is not allowed to do this.
func (e *Error) IsAuth() bool {
	return e.Kind == KindAuth || e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
}

// debugf writes a debug line to stderr: data goes to stdout, nothing else does.
func debugf(format string, args ...any) {
	if config.Debug {
		fmt.Fprintf(os.Stderr, "DEBUG: "+format+"\n", args...)
	}
}

// Request performs an authenticated API request and handles non-JSON responses gracefully.
func Request(ctx context.Context, method, url, token string, result any) error {
	return RequestWithBody(ctx, method, url, token, nil, result)
}

// RequestWithBody performs an authenticated API request with an optional JSON body.
// Any 2xx is success. result may be nil; an empty body leaves it untouched.
func RequestWithBody(ctx context.Context, method, url, token string, body any, result any) error {
	raw, err := doRaw(ctx, method, url, token, body)
	if err != nil {
		return err
	}
	if result == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, result); err != nil {
		return fmt.Errorf("failed to decode JSON response: %w", err)
	}
	return nil
}

// doRaw performs the request and returns the raw response body of a 2xx.
func doRaw(ctx context.Context, method, rawURL, token string, body any) (json.RawMessage, error) {
	debugf("Request: %s %s", method, rawURL)

	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBody)
		debugf("Request Body: %s", string(jsonBody))
	}

	req, err := http.NewRequestWithContext(ctx, method, rawURL, bodyReader)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// The gateway answers a refused request (no or bad token, but also a 401/403 from the backend) with a 302 to
	// /oauth2/start and a body that does not match its Content-Length, so a redirect is decided before reading it.
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		debugf("Response Status: %s (Location %s)", resp.Status, resp.Header.Get("Location"))
		return nil, errorFromResponse(method, rawURL, resp, nil)
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}
	debugf("Response Status: %s", resp.Status)
	debugf("Response Body: %s", string(respBody))

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, errorFromResponse(method, rawURL, resp, respBody)
	}

	contentType := resp.Header.Get("Content-Type")
	trimmed := strings.TrimSpace(string(respBody))
	if strings.Contains(contentType, "text/html") || strings.HasPrefix(trimmed, "<") {
		return nil, fmt.Errorf("received HTML instead of JSON. This usually means you are being redirected to a login page. Please refer to KEYCLOAK.md for infrastructure configuration")
	}
	return json.RawMessage(respBody), nil
}

// errorFromResponse builds the Error of a non-2xx response.
func errorFromResponse(method, rawURL string, resp *http.Response, body []byte) *Error {
	e := &Error{Method: method, URL: stripQuery(rawURL), Status: resp.StatusCode}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		loc := resp.Header.Get("Location")
		if strings.Contains(loc, "/oauth2/") {
			e.Kind = KindAuth
			e.Message = "the platform refused the request: not logged in, the token was rejected, or your account may not do this here (the gateway answers all of these with a login redirect): run tnctl login, and if that does not help, ask a teamspace owner for access"
			return e
		}
		e.Message = "unexpected redirect from the platform (HTTP " + fmt.Sprint(resp.StatusCode) + ")"
		return e
	}
	e.Code, e.Message = parseErrorBody(body)
	if resp.StatusCode == http.StatusTooManyRequests {
		e.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	}
	return e
}

// parseRetryAfter reads a Retry-After header: seconds, or an HTTP date.
func parseRetryAfter(h string, now time.Time) int {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if n, err := strconv.Atoi(h); err == nil {
		return max(n, 0)
	}
	if t, err := http.ParseTime(h); err == nil {
		return max(int(t.Sub(now).Seconds()+0.5), 0)
	}
	return 0
}

func stripQuery(u string) string {
	if p, err := url.Parse(u); err == nil {
		p.RawQuery, p.Fragment = "", ""
		return p.String()
	}
	return u
}

// parseErrorBody extracts (code, message) from the error shapes of the Backstage
// backend: {"error":{"name","message"}}, or the fleet access shape {"code","message"}.
func parseErrorBody(body []byte) (code, message string) {
	var v struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Error   *struct {
			Name    string `json:"name"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &v); err == nil {
		code, message = v.Code, v.Message
		if v.Error != nil {
			if message == "" {
				message = v.Error.Message
			}
			if code == "" {
				code = v.Error.Name
			}
		}
		if message != "" {
			return code, message
		}
	}
	text := strings.TrimSpace(string(body))
	if strings.HasPrefix(text, "<") {
		return code, ""
	}
	if len(text) > 300 {
		text = text[:300] + "..."
	}
	return code, text
}
