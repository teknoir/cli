package cmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// council_roles without a bot reads the teamspace's catalog, byte for byte.
func TestMCPCouncilRolesCatalog(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	answer := catalogJSON(
		[]string{roleViewJSON("triage", "Triage", "teamspace", "member", "ok", []string{"fleet-read"}, nil)},
		[]string{roleViewJSON("fleet", "Fleet", "template", "member", "ok", []string{"fleet-read"}, nil)})
	p.on("GET", "/council/catalog", 200, answer)
	cs := councilHarness(t, p)

	res := mcpCall(t, cs, "council_roles", nil)
	if got, want := mcpText(t, res), mcpIndent(json.RawMessage(answer)); got != want {
		t.Errorf("result:\n%s\nwant:\n%s", got, want)
	}
	wantReqs(t, p, "GET /council/catalog")
}

// council_roles with a bot reads the bot's council once and answers available and catalog.
func TestMCPCouncilRolesForABot(t *testing.T) {
	fleet := roleViewJSON("fleet", "Fleet", "template", "member", "ok", []string{"fleet-read"}, nil)
	before := roleViewJSON("triage", "Triage", "teamspace", "member", "ok", []string{"fleet-read"}, nil)
	triage := strings.Replace(before, `"description":"Does triage."`, `"description":"Does <b> & </b>."`, 1)
	if triage == before {
		t.Fatal("the triage role's description did not change")
	}
	const olds = `"available":[],"catalog":null`
	available := `"available":[` + fleet + `,` + triage + `],"catalog":{"groups":[{"name":"fleet-read"}],"skills":[],"limits":{}}`
	view := func() string {
		v := councilViewJSON(canEditOK)
		if n := strings.Count(v, olds); n != 1 {
			t.Fatalf("councilViewJSON changed: %q occurs %d times", olds, n)
		}
		return strings.Replace(v, olds, available, 1)
	}()

	t.Run("available and catalog", func(t *testing.T) {
		councilFast(t)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/council", 200, view)
		cs := councilHarness(t, p)

		res := mcpCall(t, cs, "council_roles", map[string]any{"bot": "scout"})
		got := mcpText(t, res)
		// The hub sends the premade fleet first; the result puts the teamspace's triage first.
		want := mcpIndent(json.RawMessage(`{"available":[` + triage + `,` + fleet + `],"catalog":{"groups":[{"name":"fleet-read"}],"skills":[],"limits":{}}}`))
		if got != want {
			t.Errorf("result:\n%s\nwant:\n%s", got, want)
		}
		if !strings.Contains(got, "Does <b> & </b>.") {
			t.Errorf("the description was escaped: %s", got)
		}
		if strings.Contains(got, "\\u003c") {
			t.Errorf("the result carries the HTML escape of <: %s", got)
		}
		wantReqs(t, p, "GET /bots/scout/council")
	})

	t.Run("no catalog key", func(t *testing.T) {
		councilFast(t)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/council", 200, `{"council":{"proto":2,"available":[]}}`)
		cs := councilHarness(t, p)

		got := mcpText(t, mcpCall(t, cs, "council_roles", map[string]any{"bot": "scout"}))
		if want := mcpIndent(json.RawMessage(`{"available":[],"catalog":null}`)); got != want {
			t.Errorf("result:\n%s\nwant:\n%s", got, want)
		}
	})
}

func TestMCPCouncilRolesErrors(t *testing.T) {
	for _, c := range []struct {
		name   string
		path   string
		status int
		body   string
		args   map[string]any
		want   string
	}{
		{"no such bot", "/bots/nobot/council", 404,
			councilErrBody("not_found", "No bot named nobot in teamspace teknoir-demo.", ""),
			map[string]any{"bot": "nobot"},
			"No bot named nobot in teamspace teknoir-demo."},
		{"the bot's chat is off", "/bots/scout/council", 404,
			`{"code":"council_off","message":"The council is turned off on this platform."}`,
			map[string]any{"bot": "scout"},
			"the TeknoirBot chat is turned off on this platform"},
		{"the catalog's chat is off", "/council/catalog", 404,
			`{"code":"council_off","message":"The council is turned off on this platform."}`,
			nil,
			"the TeknoirBot chat is turned off on this platform"},
		{"no access to the catalog", "/council/catalog", 403,
			councilErrBody("chat_not_allowed", "You do not have access to devices in teamspace teknoir-demo. Ask a teamspace admin for read access.", ""),
			nil,
			"You do not have access to devices in teamspace teknoir-demo. Ask a teamspace admin for read access."},
	} {
		t.Run(c.name, func(t *testing.T) {
			councilFast(t)
			p := newChatAPI(t)
			p.on("GET", c.path, c.status, c.body)
			cs := councilHarness(t, p)

			if got := mcpErr(t, mcpCall(t, cs, "council_roles", c.args)); got != c.want {
				t.Errorf("%q, want %q", got, c.want)
			}
		})
	}
}

// A cut result names the command that lists the rest, with the teamspace and the bot it read.
func TestMCPCouncilRolesCutNamesTheCommand(t *testing.T) {
	roles := func() []string {
		var out []string
		for i := 0; i < 60; i++ {
			slug := fmt.Sprintf("member-%02d", i)
			r := roleViewJSON(slug, "Member", "teamspace", "member", "ok", nil, nil)
			r = strings.Replace(r, `"description":"Does `+slug+`."`, `"description":`+q(strings.Repeat("d", 1000)), 1)
			out = append(out, r)
		}
		return out
	}()
	view := func() string {
		v := councilViewJSON(canEditOK)
		const olds = `"available":[]`
		if n := strings.Count(v, olds); n != 1 {
			t.Fatalf("councilViewJSON changed: %q occurs %d times", olds, n)
		}
		return strings.Replace(v, olds, `"available":[`+strings.Join(roles, ",")+`]`, 1)
	}()

	t.Run("with a bot", func(t *testing.T) {
		councilFast(t)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/council", 200, view)
		cs := councilHarness(t, p)

		res := mcpCall(t, cs, "council_roles", map[string]any{"bot": "scout"})
		if n := len(mcpText(t, res)); n > mcpMaxResult {
			t.Errorf("%d bytes, want at most %d", n, mcpMaxResult)
		}
		got := mcpMap(t, res)
		if got["truncated"] != true || got["field"] != "available" || got["totalItems"] != float64(60) {
			t.Errorf("cut %v", got)
		}
		if shown, ok := got["shownItems"].(float64); !ok || shown < 1 || shown >= 60 {
			t.Errorf("shownItems %v, want a number from 1 to 59", got["shownItems"])
		}
		want := "The result was cut at whole items to fit 48 KiB: tnctl council role list -n teknoir-demo --bot scout --templates lists every member."
		if got["hint"] != want {
			t.Errorf("hint %v, want %q", got["hint"], want)
		}
	})

	t.Run("without a bot", func(t *testing.T) {
		councilFast(t)
		p := newChatAPI(t)
		p.on("GET", "/council/catalog", 200, catalogJSON(roles, nil))
		cs := councilHarness(t, p)

		res := mcpCall(t, cs, "council_roles", nil)
		if n := len(mcpText(t, res)); n > mcpMaxResult {
			t.Errorf("%d bytes, want at most %d", n, mcpMaxResult)
		}
		got := mcpMap(t, res)
		if got["truncated"] != true || got["field"] != "roles" {
			t.Errorf("cut %v", got)
		}
		want := "The result was cut at whole items to fit 48 KiB: tnctl council role list -n teknoir-demo --templates lists every member."
		if got["hint"] != want {
			t.Errorf("hint %v, want %q", got["hint"], want)
		}
	})

	t.Run("another teamspace", func(t *testing.T) {
		councilFast(t)
		p := newChatAPI(t)
		// The fake strips only teknoir-demo's prefix: another teamspace's routes are full paths.
		p.on("GET", "/api/device/fleet/other/council/catalog", 200, catalogJSON(roles, nil))
		p.on("GET", "/api/device/fleet/other/bots/scout/council", 200, view)
		cs := councilHarness(t, p)

		got := mcpMap(t, mcpCall(t, cs, "council_roles", map[string]any{"namespace": "other"}))
		want := "The result was cut at whole items to fit 48 KiB: tnctl council role list -n other --templates lists every member."
		if got["hint"] != want {
			t.Errorf("hint %v, want %q", got["hint"], want)
		}
		got = mcpMap(t, mcpCall(t, cs, "council_roles", map[string]any{"namespace": "other", "bot": "scout"}))
		want = "The result was cut at whole items to fit 48 KiB: tnctl council role list -n other --bot scout --templates lists every member."
		if got["hint"] != want {
			t.Errorf("hint %v, want %q", got["hint"], want)
		}
	})
}

// The read tools keep their own hint; mcpCutMaxHint takes the one its caller gives.
func TestMCPCutHintOfTheReadTools(t *testing.T) {
	if got, want := mcpCutHint(49152), "The result was cut at whole items to fit 48 KiB: narrow the query (labels, robot, device, tail, since) for the rest."; got != want {
		t.Errorf("%q, want %q", got, want)
	}
	var items []string
	for i := 0; i < 200; i++ {
		items = append(items, `{"name":`+q(strings.Repeat("x", 100))+`}`)
	}
	text := "[" + strings.Join(items, ",") + "]"

	var got map[string]any
	cut := mcpCutMax(text, 4096)
	if err := json.Unmarshal([]byte(cut), &got); err != nil {
		t.Fatalf("the cut is not JSON: %v", err)
	}
	want := "The result was cut at whole items to fit 4 KiB: narrow the query (labels, robot, device, tail, since) for the rest."
	if got["hint"] != want {
		t.Errorf("hint %v, want %q", got["hint"], want)
	}

	got = map[string]any{}
	cut = mcpCutMaxHint(text, 4096, "say this")
	if err := json.Unmarshal([]byte(cut), &got); err != nil {
		t.Fatalf("the cut is not JSON: %v", err)
	}
	if got["hint"] != "say this" {
		t.Errorf("hint %v, want the caller's", got["hint"])
	}
}

// A cut at whole items keeps a prefix of available: the bot's own members come first, so a cut drops
// premade members before them.
func TestMCPCouncilRolesCutKeepsTheBotsOwn(t *testing.T) {
	var roles []string
	for i := 0; i < 13; i++ {
		roles = append(roles, roleViewJSON(fmt.Sprintf("tpl-%02d", i), "T", "template", "member", "ok", nil, nil))
	}
	for i := 0; i < 60; i++ {
		slug := fmt.Sprintf("member-%02d", i)
		roles = append(roles, strings.Replace(roleViewJSON(slug, "Member", "teamspace", "member", "ok", nil, nil),
			`"description":"Does `+slug+`."`, `"description":`+q(strings.Repeat("d", 1000)), 1))
	}
	roles = append(roles, roleViewJSON("night-watch", "Night watch", "bot", "member", "ok", nil, nil))
	v := councilViewJSON(canEditOK)
	const olds = `"available":[]`
	if n := strings.Count(v, olds); n != 1 {
		t.Fatalf("councilViewJSON changed: %q occurs %d times", olds, n)
	}
	councilFast(t)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/council", 200, strings.Replace(v, olds, `"available":[`+strings.Join(roles, ",")+`]`, 1))
	cs := councilHarness(t, p)

	res := mcpCall(t, cs, "council_roles", map[string]any{"bot": "scout"})
	text := mcpText(t, res)
	got := mcpMap(t, res)
	if got["truncated"] != true || got["field"] != "available" || got["totalItems"] != float64(74) {
		t.Fatalf("cut %v, want a cut of available's 74 items", got)
	}
	if !strings.Contains(text, `"slug": "night-watch"`) {
		t.Errorf("the cut dropped the bot's own member night-watch")
	}
	if strings.Contains(text, `"slug": "tpl-12"`) {
		t.Errorf("the cut kept the last premade member before the teamspace's")
	}
}

// council_roles' cap is mcpMaxResult, tested at its edge.
func TestMCPCouncilRolesCutAtTheBoundary(t *testing.T) {
	fleet := roleViewJSON("fleet", "Fleet", "template", "member", "ok", []string{"fleet-read"}, nil)
	const desc = `"description":"Does fleet."`
	if strings.Count(fleet, desc) != 1 {
		t.Fatalf("roleViewJSON changed: %s", fleet)
	}
	roles := func(pad string) string {
		var out []string
		for i := 0; i < 20; i++ {
			out = append(out, fleet)
		}
		out = append(out, strings.Replace(fleet, desc, `"description":`+q(pad), 1))
		return "[" + strings.Join(out, ",") + "]"
	}
	view := func(pad string) string {
		v := councilViewJSON(canEditOK)
		const olds = `"available":[]`
		if strings.Count(v, olds) != 1 {
			t.Fatalf("councilViewJSON changed")
		}
		return strings.Replace(v, olds, `"available":`+roles(pad), 1)
	}
	result := func(pad string) string {
		return mcpIndent(json.RawMessage(`{"available":` + roles(pad) + `,"catalog":null}`))
	}
	n := mcpMaxResult - len(result(""))
	councilFast(t)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/council", 200, view(strings.Repeat("d", n)))
	cs := councilHarness(t, p)
	got := mcpText(t, mcpCall(t, cs, "council_roles", map[string]any{"bot": "scout"}))
	if want := result(strings.Repeat("d", n)); got != want || len(got) != mcpMaxResult {
		t.Errorf("the answer at the cap: %d bytes, want it whole at %d", len(got), len(want))
	}
	p2 := newChatAPI(t)
	p2.on("GET", "/bots/scout/council", 200, view(strings.Repeat("d", n+1)))
	cs2 := councilHarness(t, p2)
	m := mcpMap(t, mcpCall(t, cs2, "council_roles", map[string]any{"bot": "scout"}))
	if m["truncated"] != true || m["field"] != "available" || m["totalItems"] != float64(21) {
		t.Errorf("one byte more: %v, want a cut of available", m)
	}
}

// councilBotsOwnFirst keeps the hub's order within each scope (32 items, past the insertion sort that
// would hide an unstable sort), puts a missing or unknown scope with the premade ones, and passes
// anything that is not an array through as it is.
func TestCouncilBotsOwnFirstKeepsTheHubsOrder(t *testing.T) {
	var tpl, ts, bot string
	for i := 0; i < 10; i++ {
		tpl += fmt.Sprintf(`,{"slug":"t%d","scope":"template","d":"<&>"}`, i)
		ts += fmt.Sprintf(`,{"slug":"s%d","scope":"teamspace"}`, i)
		bot += fmt.Sprintf(`,{"slug":"b%d","scope":"bot"}`, i)
	}
	in := "[" + tpl[1:] + `,{"slug":"x"}` + ts + bot + `,{"slug":"y","scope":"org"}]`
	want := "[" + bot[1:] + ts + tpl + `,{"slug":"x"},{"slug":"y","scope":"org"}]`
	if got, err := councilBotsOwnFirst(json.RawMessage(in)); err != nil || string(got) != want {
		t.Errorf("got %s, %v\nwant %s", string(got), err, want)
	}
	for i, raw := range []json.RawMessage{nil, json.RawMessage("null"), json.RawMessage("[]"), json.RawMessage("{}"), json.RawMessage(`"x"`)} {
		if got, err := councilBotsOwnFirst(raw); err != nil || string(got) != string(raw) {
			t.Errorf("input %d %q: got %q, %v, want it as it is", i, string(raw), string(got), err)
		}
	}
}

// An answer whose available is missing or null passes it through as null: only an array is reordered.
func TestMCPCouncilRolesAvailableNotAnArray(t *testing.T) {
	for name, view := range map[string]string{
		"missing": `{"council":{"proto":2,"catalog":null}}`,
		"null":    `{"council":{"proto":2,"available":null,"catalog":null}}`,
	} {
		t.Run(name, func(t *testing.T) {
			councilFast(t)
			p := newChatAPI(t)
			p.on("GET", "/bots/scout/council", 200, view)
			cs := councilHarness(t, p)

			got := mcpText(t, mcpCall(t, cs, "council_roles", map[string]any{"bot": "scout"}))
			if want := mcpIndent(json.RawMessage(`{"available":null,"catalog":null}`)); got != want {
				t.Errorf("result:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}
