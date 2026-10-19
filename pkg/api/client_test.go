package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/teknoir/cli/pkg/config"
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

	// Capture stderr: debug output never mixes with the data on stdout
	old := os.Stderr
	read, write, _ := os.Pipe()
	os.Stderr = write

	var result map[string]any
	_ = Request(context.Background(), "GET", ts.URL, "token", &result)

	write.Close()
	os.Stderr = old

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

	// Capture stderr: debug output never mixes with the data on stdout
	old := os.Stderr
	read, write, _ := os.Pipe()
	os.Stderr = write

	var result map[string]any
	_ = Request(context.Background(), "GET", ts.URL, "token", &result)

	write.Close()
	os.Stderr = old

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, read)
	output := buf.String()

	if strings.Contains(output, "DEBUG:") {
		t.Errorf("expected no debug output, but found: %q", output)
	}
}

func TestRequest_RedirectToLoginIsAuthError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://teknoir.cloud/oauth2/start?rd=%2Fapi%2Fdevice", http.StatusFound)
	}))
	defer ts.Close()

	err := Request(context.Background(), "GET", ts.URL+"/x", "bad", nil)
	var ae *Error
	if !errors.As(err, &ae) {
		t.Fatalf("want *Error, got %T %v", err, err)
	}
	if !ae.IsAuth() || ae.Status != http.StatusFound {
		t.Errorf("got %+v", ae)
	}
	if !strings.Contains(ae.Error(), "run tnctl login") {
		t.Errorf("message: %s", ae.Error())
	}
}

func TestRequest_ErrorCarriesStatusAndMessage(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		code   string
		msg    string
	}{
		{"backstage", 404, `{"error":{"name":"NotFoundError","message":"no robot called x"},"response":{"statusCode":404}}`, "NotFoundError", "no robot called x"},
		{"access", 409, `{"code":"conflict","message":"changed by someone else","error":{"name":"AccessError","message":"changed by someone else"}}`, "conflict", "changed by someone else"},
		{"plain", 400, "bad things\n", "", "bad things"},
		{"empty", 500, "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			defer ts.Close()
			err := Request(context.Background(), "GET", ts.URL+"/a/b?secret=1", "t", nil)
			var ae *Error
			if !errors.As(err, &ae) {
				t.Fatalf("want *Error, got %T", err)
			}
			if ae.Status != c.status || ae.Code != c.code || ae.Message != c.msg {
				t.Errorf("got %+v", ae)
			}
			if strings.Contains(ae.URL, "secret") {
				t.Errorf("query string must not be kept: %s", ae.URL)
			}
			if !strings.Contains(ae.Error(), "500") && c.status == 500 {
				t.Errorf("empty message should show the status: %s", ae.Error())
			}
		})
	}
}

func TestRequest_Accepts2xxAndEmptyBody(t *testing.T) {
	for _, status := range []int{200, 201, 202, 204} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		var res map[string]any
		if err := Request(context.Background(), "DELETE", ts.URL, "t", &res); err != nil {
			t.Errorf("status %d: %v", status, err)
		}
		ts.Close()
	}
}

func TestRequest_SendsBearerAndJSONBody(t *testing.T) {
	var gotAuth, gotCT, gotMethod string
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotCT, gotMethod = r.Header.Get("Authorization"), r.Header.Get("Content-Type"), r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()
	var res struct{ OK bool }
	if err := RequestWithBody(context.Background(), "PATCH", ts.URL, "tok", map[string]any{"a": 1}, &res); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer tok" || gotCT != "application/json" || gotMethod != "PATCH" || gotBody["a"] != 1.0 || !res.OK {
		t.Errorf("auth=%q ct=%q method=%q body=%v res=%v", gotAuth, gotCT, gotMethod, gotBody, res)
	}
}
