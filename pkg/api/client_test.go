package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
