package api

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"teknoir/cli/pkg/config"
)

func TestRequest_HTMLDetection(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>Login</body></html>"))
	}))
	defer ts.Close()

	var result map[string]any
	err := Request(context.Background(), "GET", ts.URL, "token", &result)

	if err == nil {
		t.Fatal("expected error when receiving HTML, got nil")
	}

	expected := "received HTML instead of JSON"
	if !strings.Contains(err.Error(), expected) {
		t.Errorf("expected error message to contain %q, got %q", expected, err.Error())
	}
}

func TestRequest_DebugLogging(t *testing.T) {
	// Set debug to true
	config.Debug = true
	defer func() { config.Debug = false }()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"foo":"bar"}`))
	}))
	defer ts.Close()

	// Capture stdout
	old := os.Stdout
	read, write, _ := os.Pipe()
	os.Stdout = write

	var result map[string]any
	_ = Request(context.Background(), "GET", ts.URL, "token", &result)

	write.Close()
	os.Stdout = old

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, read)
	output := buf.String()

	if !strings.Contains(output, "DEBUG: Request:") {
		t.Error("expected debug output, but didn't find 'DEBUG: Request:'")
	}
}

func TestRequest_NoDebugLogging(t *testing.T) {
	// Set debug to false
	config.Debug = false

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"foo":"bar"}`))
	}))
	defer ts.Close()

	// Capture stdout
	old := os.Stdout
	read, write, _ := os.Pipe()
	os.Stdout = write

	var result map[string]any
	_ = Request(context.Background(), "GET", ts.URL, "token", &result)

	write.Close()
	os.Stdout = old

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, read)
	output := buf.String()

	if strings.Contains(output, "DEBUG:") {
		t.Errorf("expected no debug output, but found: %q", output)
	}
}
