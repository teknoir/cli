package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/teknoir/cli/pkg/api"
	"github.com/teknoir/cli/pkg/auth"
)

func TestExitCodeOf(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"plain", errors.New("boom"), 1},
		{"usage", usageErr("bad"), 2},
		{"exitError passes through", &exitError{code: 42}, 42},
		{"not logged in", auth.ErrNotLoggedIn, 6},
		{"wrapped not logged in", fmt.Errorf("x: %w", auth.ErrNotLoggedIn), 6},
		{"redirect to login", &api.Error{Status: 302, Kind: api.KindAuth, Message: "not logged in"}, 6},
		{"401", &api.Error{Status: 401}, 6},
		{"403", &api.Error{Status: 403, Message: "no"}, 6},
		{"404", &api.Error{Status: 404}, 3},
		{"409", &api.Error{Status: 409}, 8},
		{"400", &api.Error{Status: 400}, 5},
		{"422", &api.Error{Status: 422}, 5},
		{"408", &api.Error{Status: 408}, 4},
		{"504", &api.Error{Status: 504}, 4},
		{"500", &api.Error{Status: 500}, 1},
		{"deadline", context.DeadlineExceeded, 4},
		{"wrapped deadline", fmt.Errorf("get: %w", context.DeadlineExceeded), 4},
		{"explicit code wins", newErr(codeUnhealthy, "job failed"), 7},
		{"wrapped api error", fmt.Errorf("x: %w", &api.Error{Status: 404}), 3},
	}
	for _, c := range cases {
		if got := exitCodeOf(c.err); got != c.want {
			t.Errorf("%s: exit code %d, want %d", c.name, got, c.want)
		}
	}
}

func TestWriteErrorText(t *testing.T) {
	var b bytes.Buffer
	writeError(&b, usageErr("bad flag"), "table")
	if b.String() != "Error: bad flag\n" {
		t.Errorf("got %q", b.String())
	}
}

func TestWriteErrorJSON(t *testing.T) {
	cases := map[int]string{1: "error", 2: "usage", 3: "not_found", 4: "timeout", 5: "rejected", 6: "auth", 7: "unhealthy", 8: "conflict"}
	for code, name := range cases {
		var b bytes.Buffer
		writeError(&b, &cliError{code: code, err: errors.New("it \"broke\"")}, "json")
		var v struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		if err := json.Unmarshal(b.Bytes(), &v); err != nil {
			t.Fatalf("code %d: %v: %s", code, err, b.String())
		}
		if v.Error.Code != name || v.Error.Message != `it "broke"` {
			t.Errorf("code %d: got %+v", code, v)
		}
		if strings.Count(b.String(), "\n") != 1 {
			t.Errorf("one line expected: %q", b.String())
		}
	}
}

func TestParseWaitLevel(t *testing.T) {
	for in, want := range map[string]waitLevel{"": waitNone, "published": waitPublished, "applied": waitApplied, "ready": waitReady, "READY": waitReady} {
		got, err := parseWaitLevel(in)
		if err != nil || got != want {
			t.Errorf("%q = %q, %v", in, got, err)
		}
	}
	if _, err := parseWaitLevel("done"); exitCodeOf(err) != codeUsage {
		t.Errorf("unknown level must be a usage error: %v", err)
	}
}

func TestOutputFormat(t *testing.T) {
	defer func() { outputFlag = "" }()
	for in, want := range map[string]string{"": "table", "table": "table", "json": "json", "JSON": "json", "yaml": "yaml"} {
		outputFlag = in
		if got, err := outputFormat(); err != nil || got != want {
			t.Errorf("%q = %q, %v", in, got, err)
		}
	}
	outputFlag = "xml"
	if _, err := outputFormat(); exitCodeOf(err) != codeUsage {
		t.Errorf("xml must be a usage error: %v", err)
	}
}
