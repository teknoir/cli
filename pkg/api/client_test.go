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
	"runtime"
	"strings"
	"testing"
	"time"

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

func TestRequest_RateLimitKeepsRetryAfter(t *testing.T) {
	for header, want := range map[string]int{"30": 30, "": 0, "soon": 0, "-5": 0} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if header != "" {
				w.Header().Set("Retry-After", header)
			}
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":{"name":"TooManyRequestsError","message":"slow down"}}`))
		}))
		err := Request(context.Background(), "PATCH", ts.URL+"/x", "t", nil)
		ts.Close()
		var ae *Error
		if !errors.As(err, &ae) || ae.Status != 429 || ae.RetryAfter != want {
			t.Errorf("Retry-After %q: got %+v", header, ae)
		}
	}
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	if got := parseRetryAfter(now.Add(42*time.Second).UTC().Format(http.TimeFormat), now); got != 42 {
		t.Errorf("http date: %d", got)
	}
}

func TestRequest_SendsUserAgent(t *testing.T) {
	var ua string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer ts.Close()
	Version = "v0.1.0-beta"
	defer func() { Version = "dev" }()
	if err := Request(context.Background(), "GET", ts.URL, "t", nil); err != nil {
		t.Fatal(err)
	}
	want := "tnctl/v0.1.0-beta (" + runtime.GOOS + "/" + runtime.GOARCH + ")"
	if ua != want {
		t.Errorf("User-Agent %q, want %q", ua, want)
	}
}

func TestRequest_401IsALoginProblem(t *testing.T) {
	cases := []struct {
		name      string
		challenge string
		body      string
		code      string
		contains  []string
	}{
		{"json message", `Bearer realm="backstage"`, `{"error":{"name":"AuthenticationError","message":"Invalid token"}}`, "AuthenticationError",
			[]string{"not logged in or token expired: run tnctl login", "Invalid token"}},
		{"challenge only", `Bearer realm="teknoir", error="invalid_token", error_description="The access token expired"`, ``, "invalid_token",
			[]string{"run tnctl login", "The access token expired"}},
		{"challenge error without description", `Bearer error=invalid_token`, ``, "invalid_token", []string{"run tnctl login", "(invalid_token)"}},
		{"nothing", ``, ``, "", []string{"not logged in or token expired: run tnctl login"}},
		{"html", ``, `<html>login</html>`, "", []string{"run tnctl login"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if c.challenge != "" {
					w.Header().Set("WWW-Authenticate", c.challenge)
				}
				w.WriteHeader(401)
				_, _ = w.Write([]byte(c.body))
			}))
			defer ts.Close()
			err := Request(context.Background(), "GET", ts.URL+"/x", "bad", nil)
			var ae *Error
			if !errors.As(err, &ae) {
				t.Fatalf("want *Error, got %T", err)
			}
			if ae.Status != 401 || ae.Kind != KindAuth || !ae.IsAuth() || ae.Code != c.code {
				t.Errorf("got %+v", ae)
			}
			for _, want := range c.contains {
				if !strings.Contains(ae.Error(), want) {
					t.Errorf("message lacks %q: %s", want, ae.Error())
				}
			}
			if strings.Contains(ae.Error(), "<html>") {
				t.Errorf("HTML in the message: %s", ae.Error())
			}
		})
	}
}

func TestRequest_403IsNotALoginProblem(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"json", `{"error":{"name":"NotAllowedError","message":"You do not have access to devices in teamspace acme"}}`,
			"not allowed for this account or teamspace: You do not have access to devices in teamspace acme"},
		{"fleet code", `{"code":"no_exec","message":"You may not open terminals here.","error":{"name":"X","message":"y"}}`,
			"not allowed for this account or teamspace: You may not open terminals here."},
		{"html", `<html><body>Forbidden</body></html>`, "not allowed for this account or teamspace"},
		{"empty", ``, "not allowed for this account or teamspace"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(403)
				_, _ = w.Write([]byte(c.body))
			}))
			defer ts.Close()
			err := Request(context.Background(), "GET", ts.URL+"/x", "tok", nil)
			var ae *Error
			if !errors.As(err, &ae) {
				t.Fatalf("want *Error, got %T", err)
			}
			if ae.Status != 403 || ae.Kind != KindForbidden || !ae.IsAuth() {
				t.Errorf("got %+v", ae)
			}
			if !strings.Contains(ae.Error(), c.want) || !strings.Contains(ae.Error(), "HTTP 403") {
				t.Errorf("message %q, want %q", ae.Error(), c.want)
			}
			if strings.Contains(ae.Error(), "tnctl login") || strings.Contains(ae.Error(), "<html>") {
				t.Errorf("message %q", ae.Error())
			}
		})
	}
}

func TestRequest_302StillMeansLoginForGatewaysNotYetChanged(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://teknoir.online/oauth2/start?rd=%2Fapi")
		w.WriteHeader(302)
		// the gateway's body does not match its Content-Length: it must not be read
		w.Header().Set("Content-Length", "9999")
	}))
	defer ts.Close()
	err := Request(context.Background(), "GET", ts.URL+"/x", "tok", nil)
	var ae *Error
	if !errors.As(err, &ae) || ae.Status != 302 || ae.Kind != KindAuth || !ae.IsAuth() {
		t.Fatalf("got %T %+v", err, err)
	}
	for _, want := range []string{"run tnctl login", "if that does not help, ask a teamspace owner for access"} {
		if !strings.Contains(ae.Error(), want) {
			t.Errorf("message lacks %q: %s", want, ae.Error())
		}
	}
	// a redirect elsewhere is not a login problem
	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusMovedPermanently)
	}))
	defer ts2.Close()
	err = Request(context.Background(), "GET", ts2.URL, "tok", nil)
	if !errors.As(err, &ae) || ae.Kind == KindAuth || !strings.Contains(ae.Error(), "unexpected redirect") {
		t.Errorf("got %+v", err)
	}
}

func TestParseChallenge(t *testing.T) {
	got := parseChallenge([]string{`Bearer realm="a \"b\"", error="invalid_token", error_description="expired, sorry", scope=openid`})
	want := map[string]string{"realm": `a "b"`, "error": "invalid_token", "error_description": "expired, sorry", "scope": "openid"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if g := parseChallenge([]string{"Bearer", ``, "Basic"}); len(g) != 0 {
		t.Errorf("no parameters: %v", g)
	}
	// the first challenge that carries parameters wins
	if g := parseChallenge([]string{"Negotiate", `Bearer error="invalid_token"`}); g["error"] != "invalid_token" {
		t.Errorf("%v", g)
	}
}
