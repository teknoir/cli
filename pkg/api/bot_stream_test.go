package api

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

// ---------- bot mode ----------

func TestNewBotFleetReadsTheKeyFileOnEveryRequest(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key")
	if err := os.WriteFile(keyFile, []byte("  first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var got []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Authorization"))
		if r.URL.Path != "/api/device/bot/v1/fleet/teknoir-demo/robots" {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer ts.Close()
	f := NewBotFleet(ts.URL+"/api/device/bot/v1/", "teknoir-demo", keyFile)
	if f.Root != ts.URL+"/api/device/bot/v1/fleet" || f.Base != f.Root+"/teknoir-demo" {
		t.Errorf("Root %q Base %q", f.Root, f.Base)
	}
	if _, _, err := f.Robots(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Robots(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "Bearer first" || got[1] != "Bearer second" {
		t.Errorf("Authorization headers %q", got)
	}
}

func TestBotKeyMissingOrEmpty(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope")
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte(" \n\t"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{missing, empty} {
		f := NewBotFleet("https://x/api/device/bot/v1", "ns", path)
		_, err := f.Token(context.Background())
		want := "the bot key is missing or empty (" + path + "): this computer predates the agent; re-create the bot"
		if err == nil || err.Error() != want {
			t.Errorf("err %v, want %q", err, want)
		}
	}
}

// The bot's key must not travel in clear text: only a loopback address may be plain http.
func TestNewBotFleetRefusesPlainHTTPOffLoopback(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "k")
	if err := os.WriteFile(key, []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{
		"http://example.com/api/device/bot/v1",
		"http://192.168.2.181:8085/api/device/bot/v1",
		"ftp://example.com/api",
		"not a url",
	} {
		if _, err := NewBotFleet(base, "ns", key).Token(context.Background()); !errors.Is(err, ErrBotAPIInsecure) {
			t.Errorf("%q: %v, want %v", base, err, ErrBotAPIInsecure)
		}
	}
	if _, err := NewBotFleet("https://tb-api.teknoir.cloud:8085/api/device/bot/v1", "ns", key).Token(context.Background()); err != nil {
		t.Errorf("https: %v", err)
	}
	// a loopback test server is fine
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("Authorization %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer ts.Close()
	if _, _, err := NewBotFleet(ts.URL+"/api/device/bot/v1", "ns", key).Robots(context.Background()); err != nil {
		t.Errorf("loopback: %v", err)
	}
	localhost := strings.Replace(ts.URL, "127.0.0.1", "localhost", 1)
	if _, _, err := NewBotFleet(localhost+"/api/device/bot/v1", "ns", key).Robots(context.Background()); err != nil {
		t.Errorf("localhost: %v", err)
	}
}

// A missing key is ErrBotKey, so the command exits 6: the bot must be re-created.
func TestBotKeyMissingIsErrBotKey(t *testing.T) {
	f := NewBotFleet("https://x/api/device/bot/v1", "ns", filepath.Join(t.TempDir(), "nope"))
	if _, err := f.Token(context.Background()); !errors.Is(err, ErrBotKey) {
		t.Errorf("%v, want ErrBotKey", err)
	}
}

func TestBotFleetRefusesTheDeviceLoginRoutes(t *testing.T) {
	var hits int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer ts.Close()
	dir := t.TempDir()
	key := filepath.Join(dir, "k")
	if err := os.WriteFile(key, []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := NewBotFleet(ts.URL, "ns", key)
	if _, err := f.Credentials(context.Background(), "d"); err != ErrBotNoDeviceLogin {
		t.Errorf("Credentials: %v", err)
	}
	if _, err := f.CredentialsBulk(context.Background(), []string{"d"}); err != ErrBotNoDeviceLogin {
		t.Errorf("CredentialsBulk: %v", err)
	}
	if hits != 0 {
		t.Errorf("%d requests were sent", hits)
	}
	// A user's fleet still asks the platform.
	if _, err := (&Fleet{Base: ts.URL, Token: func(context.Context) (string, error) { return "t", nil }}).Credentials(context.Background(), "d"); err != nil {
		t.Errorf("user fleet: %v", err)
	}
	if hits != 1 {
		t.Errorf("%d requests", hits)
	}
}

// ---------- the stream's topic rule ----------

func TestValidateStreamTopic(t *testing.T) {
	valid := []string{
		"/devices/+/state/#",
		"/devices/+/events/#",
		"/devices/d1/state/#",
		"/devices/d1/events/x/y",
		"/devices/d1/state/telemetry",
		"/devices/vision-station-01/events/app/log",
		// a concrete topic below a + device level, and one with several segments
		"/devices/+/state/temp",
		"/devices/d1/state/a/b",
	}
	for _, topic := range valid {
		if err := ValidateStreamTopic(topic); err != nil {
			t.Errorf("%q must be valid: %v", topic, err)
		}
	}
	invalid := []string{
		"#",
		"/devices/#",
		"$share/a//devices/+/state/#",
		"/devices/d1/config",
		"/devices/+/commands/#",
		"/devices/d1/state",
		"/devices/d1/state/",
		"/devices/d1/state/+/x",
		"/devices/+/state/#/x",
		"devices/d1/state/#",
		"/devices//state/#",
		// the server's rule for <d> and for a concrete segment
		"/devices/D1/state/#",
		"/devices/d1/state/a b",
		"/devices/d1/state/+\tx",
		// 257 characters: one over the limit
		"/devices/d1/state/" + strings.Repeat("a", 257-len("/devices/d1/state/")),
	}
	for _, topic := range invalid {
		if err := ValidateStreamTopic(topic); err == nil {
			t.Errorf("%q must be refused", topic)
		}
	}
	if err := ValidateStreamTopics(make([]string, 9)); err == nil {
		t.Error("9 topics must be refused")
	}
	if err := ValidateStreamTopics(nil); err == nil {
		t.Error("no topic must be refused")
	}
}

// ---------- the stream's events ----------

// sseServer answers the stream route with what write writes, in the order it writes it.
func sseServer(t *testing.T, write func(w http.ResponseWriter, r *http.Request)) (*Fleet, *int) {
	t.Helper()
	hits := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/api/device/fleet/teknoir-demo/mqtt/stream" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("Accept %q", r.Header.Get("Accept"))
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("Authorization %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		write(w, r)
	}))
	t.Cleanup(ts.Close)
	return &Fleet{
		Base:  ts.URL + "/api/device/fleet/teknoir-demo",
		Token: func(context.Context) (string, error) { return "tok", nil },
	}, &hits
}

func TestMqttStreamParsesTheSSEEvents(t *testing.T) {
	f, _ := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		fl := w.(http.Flusher)
		// A keepalive, a text message, a base64 one, dropped, a split event, an error.
		io.WriteString(w, ": keepalive\n\n")
		io.WriteString(w, "event: message\ndata: {\"topic\":\"/devices/d1/state/x\",\"payload\":\"hello\",\"retain\":false,\"ts\":\"2026-11-08T10:00:00Z\"}\n\n")
		fl.Flush()
		io.WriteString(w, "event: message\r\ndata: {\"topic\":\"/devices/d1/state/y\",\"payloadBase64\":\"AAEC\",\"ts\":\"2026-11-08T10:00:01Z\"}\r\n\r\n")
		io.WriteString(w, "event: dropped\ndata: {\"count\":3,\"reason\":\"rate\"}\n\n")
		fl.Flush()
		// an event split across two writes
		io.WriteString(w, "event: message\ndata: {\"topic\":\"/devices/d1/state/z\",")
		fl.Flush()
		io.WriteString(w, "\"payload\":\"split\",\"ts\":\"2026-11-08T10:00:02Z\"}\n\n")
		fl.Flush()
		io.WriteString(w, "event: error\ndata: {\"code\":\"too_many\",\"message\":\"Too many streams for one bot.\"}\n\n")
	})
	sub, err := f.MqttStream(context.Background(), []string{"/devices/+/state/#"})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	var events []StreamEvent
	for {
		ev, err := sub.Next()
		if err != nil {
			break
		}
		events = append(events, ev)
	}
	if len(events) != 5 {
		t.Fatalf("events: %+v", events)
	}
	m, err := ParseStreamMessage(events[0].Data)
	if err != nil || m.Topic != "/devices/d1/state/x" || m.Payload != "hello" || m.Retain || m.TS != "2026-11-08T10:00:00Z" {
		t.Errorf("message: %+v %v", m, err)
	}
	m, err = ParseStreamMessage(events[1].Data)
	if err != nil || m.PayloadBase64 != "AAEC" {
		t.Errorf("base64 message: %+v %v", m, err)
	}
	d, err := ParseStreamDropped(events[2].Data)
	if err != nil || d.Count != 3 || d.Reason != "rate" {
		t.Errorf("dropped: %+v %v", d, err)
	}
	m, err = ParseStreamMessage(events[3].Data)
	if err != nil || m.Payload != "split" {
		t.Errorf("split event: %+v %v", m, err)
	}
	e, err := ParseStreamError(events[4].Data)
	if err != nil || e.Code != "too_many" {
		t.Errorf("error: %+v %v", e, err)
	}
}

// A line ends at \r\n, \n or a bare \r, and a \r at the end of a read is not a line end yet: it may
// be the first half of a \r\n. OneByteReader puts every byte in a read of its own.
func TestSplitSSEHandlesEveryLineEnding(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"a\nb\n", []string{"a", "b"}},
		{"a\r\nb\r\n", []string{"a", "b"}},
		{"a\rb\r", []string{"a", "b"}},
		{"a\r\nb\rc\n", []string{"a", "b", "c"}},
		{"event: message\ndata: {}\n\n", []string{"event: message", "data: {}", ""}},
	}
	for _, c := range cases {
		// Once read straight through, once a byte at a time: a \r and the \n after it then arrive in
		// two different reads, which must still be one line ending.
		for _, reader := range []io.Reader{strings.NewReader(c.in), iotest.OneByteReader(strings.NewReader(c.in))} {
			sc := bufio.NewScanner(reader)
			sc.Split(splitSSE)
			var lines []string
			for sc.Scan() {
				lines = append(lines, sc.Text())
			}
			if err := sc.Err(); err != nil {
				t.Errorf("%q: %v", c.in, err)
			}
			if strings.Join(lines, "\x00") != strings.Join(c.want, "\x00") {
				t.Errorf("%q: lines %q, want %q", c.in, lines, c.want)
			}
		}
	}
}

// An event without an "event:" line is a message; an event without data is not an event at all.
func TestMqttStreamEventNameDefaultsAndDataIsRequired(t *testing.T) {
	f, _ := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		fl := w.(http.Flusher)
		// CR-only line endings, no event: line
		io.WriteString(w, "data: {\"topic\":\"/devices/d1/state/x\",\"payload\":\"cr\"}\r\r")
		fl.Flush()
		io.WriteString(w, "event: dropped\n\n") // no data: dropped
		io.WriteString(w, "data: {\"topic\":\"/devices/d1/state/y\",\"payload\":\"two\"}\n\n")
	})
	sub, err := f.MqttStream(context.Background(), []string{"/devices/+/state/#"})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	var events []StreamEvent
	for {
		ev, err := sub.Next()
		if err != nil {
			break
		}
		events = append(events, ev)
	}
	if len(events) != 2 {
		t.Fatalf("events %+v", events)
	}
	if events[0].Name != StreamEventMessage {
		t.Errorf("first event name %q, want %q", events[0].Name, StreamEventMessage)
	}
	if m, err := ParseStreamMessage(events[0].Data); err != nil || m.Payload != "cr" {
		t.Errorf("CR-only event: %+v %v", m, err)
	}
	if m, err := ParseStreamMessage(events[1].Data); err != nil || m.Payload != "two" {
		t.Errorf("second event: %+v %v", m, err)
	}
}

// A line or an event's data past 1 MiB ends the stream with an error.
func TestMqttStreamRefusesOversizedEvents(t *testing.T) {
	big := strings.Repeat("x", maxStreamEvent)
	// two data lines, each below the line cap, together past the event cap
	f, _ := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "data: "+strings.Repeat("y", maxStreamEvent/2)+"\n")
		io.WriteString(w, "data: "+strings.Repeat("y", maxStreamEvent/2)+"\n\n")
		io.WriteString(w, "data: {\"topic\":\"/devices/d1/state/x\",\"payload\":\"after\"}\n\n")
	})
	sub, err := f.MqttStream(context.Background(), []string{"/devices/+/state/#"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sub.Next(); !errors.Is(err, errStreamTooLarge) || !strings.Contains(err.Error(), "1 MiB") {
		t.Errorf("data past the cap: %v", err)
	}
	_ = sub.Close()

	// one line past the line cap
	f2, _ := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "data: "+big+"y\n\n")
	})
	sub2, err := f2.MqttStream(context.Background(), []string{"/devices/+/state/#"})
	if err != nil {
		t.Fatal(err)
	}
	defer sub2.Close()
	if _, err := sub2.Next(); !errors.Is(err, errStreamTooLarge) {
		t.Errorf("line past the cap: %v", err)
	}
}

// A half-open connection would keep the stream forever: 90 s without a byte ends it.
func TestMqttStreamEndsAHalfOpenConnection(t *testing.T) {
	old := streamIdleTimeout
	streamIdleTimeout = 200 * time.Millisecond
	t.Cleanup(func() { streamIdleTimeout = old })
	f, _ := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush() // the headers, then nothing
		<-r.Context().Done()
	})
	sub, err := f.MqttStream(context.Background(), []string{"/devices/+/state/#"})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	_, err = sub.Next()
	if err == nil || !strings.Contains(err.Error(), "no data from the stream for") {
		t.Fatalf("err %v", err)
	}
	if errors.Is(err, io.EOF) {
		t.Error("a silent stream ended as a clean end of stream")
	}
}

func TestMqttStreamNon2xxIsAFleetError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":"forbidden","message":"Not allowed to follow MQTT here."}`))
	}))
	defer ts.Close()
	f := &Fleet{Base: ts.URL + "/api/device/fleet/teknoir-demo", Token: func(context.Context) (string, error) { return "tok", nil }}
	_, err := f.MqttStream(context.Background(), []string{"/devices/+/state/#"})
	ae, ok := err.(*Error)
	if !ok || ae.Status != 403 || ae.Code != "forbidden" || !strings.Contains(ae.Message, "Not allowed to follow MQTT here.") {
		t.Fatalf("%T %v", err, err)
	}
}

func TestInspectRoutesAskForTheShellToStayOff(t *testing.T) {
	var got []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.Path+"?"+r.URL.RawQuery)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer ts.Close()
	f := &Fleet{Base: ts.URL + "/api/device/fleet/teknoir-demo", Token: func(context.Context) (string, error) { return "tok", nil }}
	ctx := context.Background()
	if _, _, err := f.InspectPod(ctx, "vs", "default", "whoami-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.InspectLogs(ctx, "vs", "default", "whoami-1", InspectLogsOptions{Container: "app", Previous: true, Tail: 50, Since: "10m"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.InspectRelease(ctx, "vs", "default", "notebook"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/api/device/fleet/teknoir-demo/robots/vs/inspect/pods/default/whoami-1?enable=false",
		"/api/device/fleet/teknoir-demo/robots/vs/inspect/pods/default/whoami-1/logs?container=app&enable=false&previous=true&since=10m&tail=50",
		"/api/device/fleet/teknoir-demo/robots/vs/inspect/releases/default/notebook?enable=false",
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d:\n got %s\nwant %s", i, got[i], want[i])
		}
	}
}

func TestCreateRobotPostsTheRequest(t *testing.T) {
	var path, body string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		path, body = r.Method+" "+r.URL.Path, string(b)
		_, _ = w.Write([]byte(`{"name":"amr-02","device":"amr-02"}`))
	}))
	defer ts.Close()
	f := &Fleet{Base: ts.URL + "/api/device/fleet/teknoir-demo", Token: func(context.Context) (string, error) { return "tok", nil }}
	r, _, err := f.CreateRobot(context.Background(), CreateRobotRequest{Name: "amr-02", Type: "amr", Labels: map[string]string{"site": "karlstad"}, Device: "amr-01", OrgUnit: "karlstad"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Device != "amr-02" {
		t.Errorf("%+v", r)
	}
	if path != "POST /api/device/fleet/teknoir-demo/robots" {
		t.Errorf("path %s", path)
	}
	if !strings.Contains(body, `"name":"amr-02"`) || !strings.Contains(body, `"type":"amr"`) ||
		!strings.Contains(body, `"labels":{"site":"karlstad"}`) || !strings.Contains(body, `"device":"amr-01"`) ||
		!strings.Contains(body, `"orgUnit":"karlstad"`) {
		t.Errorf("body %s", body)
	}
}

func TestBotWhoamiNeedsTheBotAndReadsTheWhoamiRoute(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/device/bot/v1/whoami" {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"mode":"bot","bot":{"namespace":"t","name":"scout","unit":null,"access":"none"},"creator":{"email":"a@b.c"},"domain":"x"}`))
	}))
	defer ts.Close()
	dir := t.TempDir()
	key := filepath.Join(dir, "k")
	if err := os.WriteFile(key, []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	who, _, err := NewBotFleet(ts.URL+"/api/device/bot/v1", "t", key).BotWhoami(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if who.Bot.Name != "scout" || who.Bot.Unit != nil || who.Creator.Email != "a@b.c" {
		t.Errorf("%+v", who)
	}
	if _, _, err := (&Fleet{}).BotWhoami(context.Background()); err != ErrNotBotFleet {
		t.Errorf("a user fleet answered: %v", err)
	}
}

// An older platform has no stream route: Express answers a 404 with no or an HTML body, and a missing
// device inside the route answers a JSON error. Only the first says the platform is older.
func TestMqttStreamOnAnOlderPlatform(t *testing.T) {
	for _, c := range []struct{ name, body, want string }{
		{"no route", "", msgNoStreamOnOlderPlatform},
		{"an HTML page", "<!DOCTYPE html><html><body>Not Found</body></html>", msgNoStreamOnOlderPlatform},
		{"a missing device", `{"error":{"name":"NotFoundError","message":"device x not found"}}`, "device x not found"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, c.body)
			}))
			defer ts.Close()
			f := &Fleet{Base: ts.URL + "/api/device/fleet/teknoir-demo", Token: func(context.Context) (string, error) { return "tok", nil }}
			_, err := f.MqttStream(context.Background(), []string{"/devices/+/state/#"})
			ae, ok := err.(*Error)
			if !ok || ae.Status != http.StatusNotFound {
				t.Fatalf("%T %v", err, err)
			}
			if ae.Message != c.want {
				t.Errorf("message %q, want %q", ae.Message, c.want)
			}
		})
	}
}

// A platform whose inspect routes do not know enable=false is older than tnctl; it must be named as
// such rather than as a bad request, and tnctl must not ask again without enable=false.
func TestInspectOnAnOlderPlatform(t *testing.T) {
	ctx := context.Background()
	t.Run("the three inspect routes", func(t *testing.T) {
		requests := 0
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			// The older platform's own answers: the logs and release routes name what they allow.
			msg := "The query parameter enable is not known."
			switch {
			case strings.HasSuffix(r.URL.Path, "/logs"):
				msg = "The query parameter enable is not known. Use container, previous, tail, since."
			case strings.Contains(r.URL.Path, "/inspect/releases/"):
				msg = "The query parameter enable is not known. Use revision."
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"name":"InputError","message":"`+msg+`"}}`)
		}))
		defer ts.Close()
		f := &Fleet{Base: ts.URL + "/api/device/fleet/teknoir-demo", Token: func(context.Context) (string, error) { return "tok", nil }}
		for _, c := range []struct {
			name string
			call func() error
		}{
			{"pod", func() error { _, _, err := f.InspectPod(ctx, "vs", "default", "p"); return err }},
			{"logs", func() error { _, _, err := f.InspectLogs(ctx, "vs", "default", "p", InspectLogsOptions{}); return err }},
			{"release", func() error { _, _, err := f.InspectRelease(ctx, "vs", "default", "r"); return err }},
		} {
			before := requests
			err := c.call()
			ae, ok := err.(*Error)
			if !ok || ae.Status != http.StatusBadRequest || ae.Code != "InputError" {
				t.Errorf("%s: %T %v", c.name, err, err)
				continue
			}
			want := "this platform is older than this tnctl: it cannot read a robot without turning its Shell on, so tnctl does not ask it (ask for a platform update)"
			if ae.Message != want {
				t.Errorf("%s: message %q, want %q", c.name, ae.Message, want)
			}
			if n := requests - before; n != 1 {
				t.Errorf("%s sent %d request(s), want 1: tnctl must never retry without enable=false", c.name, n)
			}
		}
	})
	t.Run("another 400", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"name":"InputError","message":"tail must be at most 2000."}}`)
		}))
		defer ts.Close()
		f := &Fleet{Base: ts.URL + "/api/device/fleet/teknoir-demo", Token: func(context.Context) (string, error) { return "tok", nil }}
		_, _, err := f.InspectLogs(ctx, "vs", "default", "p", InspectLogsOptions{})
		ae, ok := err.(*Error)
		if !ok || ae.Status != http.StatusBadRequest || ae.Message != "tail must be at most 2000." {
			t.Errorf("%T %v", err, err)
		}
	})
}
