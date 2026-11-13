package cmd

// Tests of "tnctl mqtt sample": finite captures of whole messages, and their summary.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teknoir/cli/pkg/api"
)

// mqttSampleCLIEnv is a cliEnv whose fleet streams what reply writes.
func mqttSampleCLIEnv(t *testing.T, reply func(w http.ResponseWriter)) *cliEnv {
	t.Helper()
	noBotMode(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		reply(w)
	}))
	t.Cleanup(srv.Close)
	e, _ := testEnv(t, newFakePlatform(t), nil)
	e.fleet = func() (*api.Fleet, string, string, error) {
		return &api.Fleet{Base: srv.URL + "/fleet/teknoir-demo", Token: func(context.Context) (string, error) { return "tok", nil }},
			"teknoir.cloud", "teknoir-demo", nil
	}
	return e
}

// mqttSampleSummaryOf decodes the last line of a --json capture: its summary.
func mqttSampleSummaryOf(t *testing.T, out string) (mqttSampleSummary, string) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	last := lines[len(lines)-1]
	var v struct {
		Summary mqttSampleSummary `json:"summary"`
	}
	if err := json.Unmarshal([]byte(last), &v); err != nil {
		t.Fatalf("the summary line %q: %v", last, err)
	}
	return v.Summary, strings.Join(lines[:len(lines)-1], "\n")
}

func TestMqttSampleStopsAtCount(t *testing.T) {
	e := mqttSampleCLIEnv(t, func(w http.ResponseWriter) {
		for i := 0; i < 10; i++ {
			_, _ = io.WriteString(w, msgEvent(fmt.Sprintf("/devices/d1/state/x%d", i), "m"))
		}
	})
	out, errs, err := runCLI(t, e, "mqtt", "sample", "/devices/+/state/#", "--count", "3")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out, "\n"); n != 3 {
		t.Errorf("%d lines: %q", n, out)
	}
	if !strings.Contains(errs, "tnctl: 3 messages, 3 bytes, stopped by count") {
		t.Errorf("stderr %q", errs)
	}
}

func TestMqttSampleStopsAtSeconds(t *testing.T) {
	e := mqttSampleCLIEnv(t, func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, msgEvent("/devices/d1/state/x", "one"))
		w.(http.Flusher).Flush()
		time.Sleep(2 * time.Second)
	})
	start := time.Now()
	out, errs, err := runCLI(t, e, "mqtt", "sample", "/devices/+/state/#", "--count", "1000", "--seconds", "1")
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 1900*time.Millisecond {
		t.Errorf("--seconds 1 took %s", d)
	}
	if !strings.Contains(out, "one") {
		t.Errorf("output %q", out)
	}
	if !strings.Contains(errs, "stopped by seconds") {
		t.Errorf("stderr %q", errs)
	}
}

func TestMqttSampleStopsAtSize(t *testing.T) {
	payload := strings.Repeat("m", 600)
	e := mqttSampleCLIEnv(t, func(w http.ResponseWriter) {
		for i := 0; i < 10; i++ {
			_, _ = io.WriteString(w, msgEvent(fmt.Sprintf("/devices/d1/state/x%d", i), payload))
		}
	})
	out, errs, err := runCLI(t, e, "mqtt", "sample", "/devices/+/state/#", "--max-bytes", "2KiB")
	if err != nil {
		t.Fatal(err)
	}
	// 3 messages of 600 bytes fit 2048; the fourth does not.
	if n := strings.Count(out, "\n"); n != 3 {
		t.Errorf("%d lines, want 3: %q", n, out)
	}
	// The refused message's size is what says how near the cap the capture came.
	if want := "stopped by size (the next message has 600 bytes)"; !strings.Contains(errs, want) {
		t.Errorf("stderr %q lacks %q", errs, want)
	}

	// --json reports the refused message's size.
	out, _, err = runCLI(t, e, "mqtt", "sample", "/devices/+/state/#", "--max-bytes", "2KiB", "--json")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := mqttSampleSummaryOf(t, out)
	if s.StoppedBy != "size" || s.NextMessageBytes != 600 || s.Messages != 3 {
		t.Errorf("summary %+v, want 3 messages stopped by size with a 600-byte next", s)
	}
}

func TestMqttSamplePrintsABinaryPayloadWhole(t *testing.T) {
	raw := []byte{0, 1, 2, 250, 251}
	enc := base64.StdEncoding.EncodeToString(raw)
	e := mqttSampleCLIEnv(t, func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, mcpSSEEvent("message", map[string]any{"topic": "/devices/d1/state/x", "payloadBase64": enc, "ts": "t"}))
	})
	out, _, err := runCLI(t, e, "mqtt", "sample", "/devices/+/state/#")
	if err != nil {
		t.Fatal(err)
	}
	if want := "/devices/d1/state/x base64:" + enc + "\n"; out != want {
		t.Errorf("stdout %q, want %q", out, want)
	}
}

func TestMqttSampleJSONLinesAndSummary(t *testing.T) {
	e := mqttSampleCLIEnv(t, func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, msgEvent("/devices/d1/state/a", "hello"))
		_, _ = io.WriteString(w, mcpSSEEvent("message", map[string]any{"topic": "/devices/d1/state/b", "payloadBase64": "AAEC", "ts": "t"}))
	})
	out, errs, err := runCLI(t, e, "mqtt", "sample", "/devices/+/state/#", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if errs != "" {
		t.Errorf("stderr %q, want nothing: the summary is a JSON line with --json", errs)
	}
	s, lines := mqttSampleSummaryOf(t, out)
	if s.Messages != 2 || s.Bytes != 8 || s.StoppedBy != "end" {
		t.Errorf("summary %+v, want 2 messages, 8 bytes, stopped by end", s)
	}
	var first, second sampleMsg
	got := strings.Split(lines, "\n")
	if err := json.Unmarshal([]byte(got[0]), &first); err != nil {
		t.Fatalf("%q: %v", got[0], err)
	}
	if err := json.Unmarshal([]byte(got[1]), &second); err != nil {
		t.Fatalf("%q: %v", got[1], err)
	}
	if first.I != 0 || first.Encoding != "utf8" || first.Payload != "hello" || first.Bytes != 5 {
		t.Errorf("first %+v", first)
	}
	if second.I != 1 || second.Encoding != "base64" || second.Payload != "AAEC" || second.Bytes != 3 {
		t.Errorf("second %+v", second)
	}
}

func TestMqttSampleOutFile(t *testing.T) {
	e := mqttSampleCLIEnv(t, func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, msgEvent("/devices/d1/state/a", "hello"))
	})
	path := filepath.Join(t.TempDir(), "cam.jsonl")
	out, _, err := runCLI(t, e, "mqtt", "sample", "/devices/+/state/#", "--out", path, "--json")
	if err != nil {
		t.Fatal(err)
	}
	// With --out, stdout carries only the summary.
	s, rest := mqttSampleSummaryOf(t, out)
	if rest != "" {
		t.Errorf("stdout has more than the summary: %q", out)
	}
	if s.Messages != 1 {
		t.Errorf("summary %+v", s)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d lines in the file: %q", len(lines), b)
	}
	var m sampleMsg
	if err := json.Unmarshal([]byte(lines[0]), &m); err != nil || m.Payload != "hello" {
		t.Errorf("first line %q (%v)", lines[0], err)
	}
	var sum struct {
		Summary mqttSampleSummary `json:"summary"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &sum); err != nil || sum.Summary.Messages != 1 {
		t.Errorf("the file's summary %q (%v)", lines[1], err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode %o, want 0600", perm)
	}

	// A file that already holds something is refused, and its content is left alone.
	if err := os.WriteFile(path, []byte("keep me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = runCLI(t, e, "mqtt", "sample", "/devices/+/state/#", "--out", path)
	if err == nil || !strings.Contains(err.Error(), "exists and is not empty") {
		t.Fatalf("err %v, want a refusal", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "keep me\n" {
		t.Errorf("the refused file changed: %q", b)
	}
}

func TestMqttSampleMaxBytesParsing(t *testing.T) {
	for _, c := range []struct {
		in      string
		want    int
		wantErr string
	}{
		{in: "16777216", want: 16777216},
		{in: "16MiB", want: 16 << 20},
		{in: "16M", want: 16 << 20},
		{in: "16MB", want: 16 << 20},
		{in: "4MiB", want: 4 << 20},
		{in: "1024", want: 1024},
		{in: "1KiB", want: 1024},
		{in: "1K", want: 1024},
		{in: "1KB", want: 1024},
		{in: "", wantErr: "is not a byte count"},
		{in: "lots", wantErr: "is not a byte count"},
		{in: "4GiB", wantErr: "is not a byte count"},
		{in: "4mi", wantErr: "is not a byte count"},
		{in: "512", wantErr: "must be from 1KiB to 16MiB"},
		{in: "17MiB", wantErr: "must be from 1KiB to 16MiB"},
		{in: "1000000000000", wantErr: "must be from 1KiB to 16MiB"},
	} {
		got, err := parseByteSize(c.in)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%q: %v %v, want an error with %q", c.in, got, err, c.wantErr)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%q: %d, %v; want %d", c.in, got, err, c.want)
		}
	}

	// The command refuses an out-of-range value before it sends anything.
	e := mqttSampleCLIEnv(t, func(w http.ResponseWriter) {})
	_, _, err := runCLI(t, e, "mqtt", "sample", "/devices/+/state/#", "--max-bytes", "512")
	if err == nil || exitCodeOf(err) != codeUsage || !strings.Contains(err.Error(), "1KiB to 16MiB") {
		t.Errorf("%v (exit %d)", err, exitCodeOf(err))
	}
}

func TestMqttSampleCountsDroppedByReason(t *testing.T) {
	e := mqttSampleCLIEnv(t, func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, mcpSSEEvent("dropped", map[string]any{"count": 2, "reason": "size"}))
		_, _ = io.WriteString(w, mcpSSEEvent("dropped", map[string]any{"count": 1, "reason": "rate"}))
		_, _ = io.WriteString(w, mcpSSEEvent("dropped", map[string]any{"count": 4, "reason": "why-not"}))
		_, _ = io.WriteString(w, mcpSSEEvent("dropped", map[string]any{"count": 5}))
	})
	_, errs, err := runCLI(t, e, "mqtt", "sample", "/devices/+/state/#")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"the platform dropped 12 (size 2, rate 1, other 9)",
		"messages over 256 KiB are dropped by the platform",
	} {
		if !strings.Contains(errs, want) {
			t.Errorf("stderr %q lacks %q", errs, want)
		}
	}

	out, _, err := runCLI(t, e, "mqtt", "sample", "/devices/+/state/#", "--json")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := mqttSampleSummaryOf(t, out)
	if s.ServerDropped != (streamDropped{Size: 2, Rate: 1, Other: 9}) {
		t.Errorf("serverDropped %+v", s.ServerDropped)
	}
}

func TestMqttSampleOpenBound(t *testing.T) {
	old := mcpSampleOpenMax
	mcpSampleOpenMax = 50 * time.Millisecond
	t.Cleanup(func() { mcpSampleOpenMax = old })
	noBotMode(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // never answers: the platform cannot reach its broker
	}))
	t.Cleanup(srv.Close)
	e, _ := testEnv(t, newFakePlatform(t), nil)
	e.fleet = func() (*api.Fleet, string, string, error) {
		return &api.Fleet{Base: srv.URL + "/fleet/teknoir-demo", Token: func(context.Context) (string, error) { return "tok", nil }},
			"teknoir.cloud", "teknoir-demo", nil
	}
	_, _, err := runCLI(t, e, "mqtt", "sample", "/devices/+/state/#")
	if err == nil || !strings.Contains(err.Error(), "did not open the MQTT stream within") {
		t.Fatalf("%v, want the open bound's error", err)
	}
	if exitCodeOf(err) != codeTimeout {
		t.Errorf("exit %d, want %d", exitCodeOf(err), codeTimeout)
	}
}

func TestMqttSampleErrorEventEndsTheCommand(t *testing.T) {
	e := mqttSampleCLIEnv(t, func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, mcpSSEEvent("error", map[string]any{"code": "access_lost", "message": "Your access to this teamspace ended."}))
	})
	_, _, err := runCLI(t, e, "mqtt", "sample", "/devices/+/state/#")
	if err == nil || !strings.Contains(err.Error(), "Your access to this teamspace ended.") || exitCodeOf(err) == 0 {
		t.Fatalf("%v (exit %d)", err, exitCodeOf(err))
	}
}

func TestMqttSampleRefusesBadTopics(t *testing.T) {
	e := mqttSampleCLIEnv(t, func(w http.ResponseWriter) {})
	for _, args := range [][]string{
		{"mqtt", "sample"},
		{"mqtt", "sample", "/devices/d1/config"},
		{"mqtt", "sample", "/devices/+/state/#", "--count", "0"},
		{"mqtt", "sample", "/devices/+/state/#", "--count", "1001"},
		{"mqtt", "sample", "/devices/+/state/#", "--seconds", "0"},
		{"mqtt", "sample", "/devices/+/state/#", "--seconds", "121"},
	} {
		if _, _, err := runCLI(t, e, args...); err == nil || exitCodeOf(err) != codeUsage {
			t.Errorf("%v: %v (exit %d), want a usage error", args, err, exitCodeOf(err))
		}
	}
}

// The token is read inside the open bound, by the stream itself: a token answer that blocks must not
// hold the command past the bound, or Ctrl-C cannot reach the interrupted path.
func TestMqttSampleTokenIsInsideTheOpenBound(t *testing.T) {
	old := mcpSampleOpenMax
	mcpSampleOpenMax = 100 * time.Millisecond
	t.Cleanup(func() { mcpSampleOpenMax = old })
	noBotMode(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	e, _ := testEnv(t, newFakePlatform(t), nil)
	e.fleet = func() (*api.Fleet, string, string, error) {
		return &api.Fleet{Base: srv.URL + "/fleet/teknoir-demo", Token: func(ctx context.Context) (string, error) {
			// The platform's key file read takes its time: longer than the bound.
			select {
			case <-time.After(2 * time.Second):
				return "tok", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}}, "teknoir.cloud", "teknoir-demo", nil
	}
	start := time.Now()
	_, _, err := runCLI(t, e, "mqtt", "sample", "/devices/+/state/#")
	d := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "did not open the MQTT stream within") {
		t.Fatalf("%v, want the open bound's error", err)
	}
	if d > 1500*time.Millisecond {
		t.Errorf("the command took %s: the token read is outside the bound", d)
	}
}

// --out opens its file without truncating it and without following a link: an existing empty file
// becomes 0600, and a link to a file that holds something is refused with the target unchanged.
func TestMqttSampleOutOpensSafely(t *testing.T) {
	e := mqttSampleCLIEnv(t, func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, msgEvent("/devices/d1/state/a", "hello"))
	})
	dir := t.TempDir()
	// An existing empty file of mode 0644 is used, and ends 0600.
	path := filepath.Join(dir, "cam.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, e, "mqtt", "sample", "/devices/+/state/#", "--out", path); err != nil {
		t.Fatalf("%v", err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("mode %o (%v), want 0600", st.Mode().Perm(), err)
	}

	// A symlink to a file that holds something is refused, and its target is left alone.
	target := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(target, []byte("keep me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, _, err := runCLI(t, e, "mqtt", "sample", "/devices/+/state/#", "--out", link)
	if err == nil || (!strings.Contains(err.Error(), "exists and is not empty") && !strings.Contains(err.Error(), "symbolic link")) {
		t.Fatalf("%v, want a refusal of the link", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "keep me\n" {
		t.Errorf("the refused link's target changed: %q", b)
	}
}
