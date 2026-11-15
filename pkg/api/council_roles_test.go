package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// roleDetailJSON is a member as the detail routes answer one: it carries "versions", so Detail is true.
const roleDetailJSON = `{"id":"r1","slug":"triage","name":"Triage","kind":"member","runtime":"inproc",
"model":"sonnet","modelId":"claude-sonnet-5-5","effort":"medium","description":"sorts the queue",
"groups":["core_read","results"],"effectiveTools":["read"],"level":"read","ceiling":"read","skills":["search"],
"scope":"teamspace","version":4,"category":"operations","templateRef":{"name":"triage","version":1},
"status":"ok","reasons":[],"notes":[],"instructions":"Triage the queue.","persona":null,
"budget":{"maxToolRounds":8,"maxTokens":40000},"versions":[{"version":4,"createdBy":"a@x","createdAt":1}],
"createdBy":"a@x","updatedBy":"a@x","updatedAt":2,"archivedAt":null,"archivedBy":null}`

// roleViewJSON is the same member as a view: no "versions" key, no instructions.
const roleViewJSON = `{"id":"r1","slug":"triage","name":"Triage","kind":"member","runtime":"inproc",
"model":"sonnet","modelId":"claude-sonnet-5-5","effort":"medium","description":"sorts the queue",
"groups":["core_read"],"effectiveTools":null,"level":"read","ceiling":"read","skills":[],
"scope":"teamspace","version":4,"category":"operations","templateRef":null,"status":"unchecked",
"reasons":null,"notes":null}`

// rolePremadeJSON is a premade member: it carries "versions", so it decodes as a detail too.
const rolePremadeJSON = `{"id":"r2","slug":"analyst","name":"Analyst","kind":"member","runtime":"inproc",
"model":"sonnet","modelId":"claude-sonnet-5-5","effort":"medium","description":"reads things",
"groups":["core_read"],"effectiveTools":["read"],"level":"read","ceiling":"read","skills":["search"],
"scope":"template","version":1,"category":"operations","templateRef":null,"status":"ok","reasons":[],"notes":[],
"instructions":"Read things.","persona":null,"budget":null,"versions":[],"createdBy":null,"updatedBy":null,
"updatedAt":null,"archivedAt":null,"archivedBy":null}`

// rosterSummaryJSON is a council summary; RosterView adds description, budget and roles.
const rosterSummaryJSON = `{"id":"ros_1","name":"standard","displayName":"Standard","scope":"bot",
"version":1,"lead":"captain","members":["triage"],"reviewer":null,"isDefault":true,
"legacyCompatible":true,"status":"ok","reasons":[]}`

func TestBotCouncilRoute(t *testing.T) {
	var calls []councilCall
	const body = `{"council":{"proto":1,"tnctl":"v0.4.0","kit":"0.2.0","capsError":null,
"defaultRoster":"standard","canEdit":{"ok":false,"reason":"Only the bot's creator or a teamspace admin may change its council."},
"roster":{"id":"ros_1","name":"standard","displayName":"Standard","scope":"bot","version":1,"lead":"triage",
"members":["triage"],"reviewer":null,"isDefault":true,"legacyCompatible":true,"status":"ok","reasons":[],
"description":null,"budget":null,"roles":[` + roleViewJSON + `]},
"rosters":[{"id":"ros_1","name":"standard","displayName":"Standard","scope":"bot","version":1,"lead":"captain",
"members":["fleet"],"reviewer":null,"isDefault":true,"legacyCompatible":true,"status":"ok","reasons":[],
"description":null,"budget":null,"roles":[]}],
"available":[` + roleViewJSON + `],"catalog":{"groups":[{"name":"core_read","label":"Platform read","description":"d","examples":["whoami"]}]},"caps":null}}`
	f := councilServer(t, 200, body, &calls)
	v, raw, err := f.BotCouncil(context.Background(), "scout")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Method != "GET" ||
		calls[0].Path != "/api/device/fleet/teknoir-demo/bots/scout/council" || calls[0].Query != "" {
		t.Fatalf("requests %+v", calls)
	}
	if v.DefaultRoster != "standard" || v.CanEdit.OK ||
		v.CanEdit.Reason != "Only the bot's creator or a teamspace admin may change its council." {
		t.Errorf("%+v", v)
	}
	if len(v.Roster.Roles) != 1 || v.Roster.Roles[0].Slug != "triage" || v.Roster.Lead != "triage" {
		t.Errorf("roster %+v", v.Roster)
	}
	if len(v.Rosters) != 1 || v.Rosters[0].Reviewer != nil || v.Rosters[0].Budget != nil {
		t.Errorf("rosters %+v", v.Rosters)
	}
	if len(v.Available) != 1 || v.Available[0].EffectiveTools != nil {
		t.Errorf("available %+v", v.Available)
	}
	if len(v.Catalog) == 0 || !strings.HasPrefix(strings.TrimSpace(string(v.Catalog)), "{") {
		t.Errorf("catalog %s", v.Catalog)
	}
	if v.Caps != nil {
		t.Errorf("caps %+v", v.Caps)
	}
	if !json.Valid(raw) {
		t.Errorf("raw %s", raw)
	}
}

func TestCouncilCatalogRoute(t *testing.T) {
	var calls []councilCall
	const body = `{"groups":[{"name":"core_read","label":"Platform read","description":"d","examples":["whoami"]}],"skills":["search"],"limits":{"maxTokens":100000},
"templates":{"roles":[` + rolePremadeJSON + `],
"rosters":[` + rosterSummaryJSON + `]},"roles":[` + roleViewJSON + `]}`
	f := councilServer(t, 200, body, &calls)
	c, _, err := f.CouncilCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Method != "GET" ||
		calls[0].Path != "/api/device/fleet/teknoir-demo/council/catalog" || calls[0].Query != "" {
		t.Fatalf("requests %+v", calls)
	}
	if len(c.Templates.Roles) != 1 {
		t.Fatalf("template roles %+v", c.Templates.Roles)
	}
	premade := c.Templates.Roles[0]
	if !premade.Detail || len(premade.Versions) != 0 || premade.CreatedBy != nil || premade.UpdatedBy != nil || premade.UpdatedAt != nil {
		t.Errorf("premade %+v", premade)
	}
	if len(c.Templates.Rosters) != 1 || c.Templates.Rosters[0].Name != "standard" {
		t.Errorf("rosters %+v", c.Templates.Rosters)
	}
	if len(c.Roles) != 1 || c.Roles[0].Slug != "triage" {
		t.Errorf("roles %+v", c.Roles)
	}
	if len(c.Skills) != 1 || c.Skills[0] != "search" {
		t.Errorf("skills %+v", c.Skills)
	}
}

func TestBotRoleRoute(t *testing.T) {
	ctx := context.Background()
	var calls []councilCall
	f := councilServer(t, 200, `{"role":`+roleDetailJSON+`}`, &calls)
	d, _, err := f.BotRole(ctx, "scout", "triage", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Method != "GET" ||
		calls[0].Path != "/api/device/fleet/teknoir-demo/bots/scout/council/roles/triage" || calls[0].Query != "" {
		t.Fatalf("requests %+v", calls)
	}
	if !d.Detail || d.Instructions == nil || *d.Instructions != "Triage the queue." || d.Version != 4 {
		t.Errorf("%+v", d)
	}
	if d.Budget == nil || d.Budget.MaxTokens != 40000 || len(d.Versions) != 1 {
		t.Errorf("%+v", d)
	}

	calls = nil
	if _, _, err := f.BotRole(ctx, "scout", "triage", "template"); err != nil {
		t.Fatal(err)
	}
	if calls[0].Query != "scope=template" {
		t.Errorf("query %q", calls[0].Query)
	}

	// a bot-scope member the person may not drive comes back as a view
	calls = nil
	f2 := councilServer(t, 200, `{"role":`+roleViewJSON+`}`, &calls)
	v, _, err := f2.BotRole(ctx, "scout", "triage", "")
	if err != nil {
		t.Fatal(err)
	}
	if v.Detail || v.Instructions != nil {
		t.Errorf("a view must not be the detail: %+v", v)
	}
	if v.Slug != "triage" || v.EffectiveTools != nil {
		t.Errorf("%+v", v)
	}
}

func TestBotRoleExportRoute(t *testing.T) {
	const body = "apiVersion: council.teknoir.org/v1alpha1\nkind: CouncilRole\nmetadata:\n  name: triage\n" +
		"spec:\n  updatedAt: 2026-11-14T10:00:00Z\n"
	var got []councilCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, councilCall{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery})
		w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	f := &Fleet{Base: srv.URL + "/api/device/fleet/teknoir-demo", Token: func(context.Context) (string, error) { return "tok", nil }}
	ctx := context.Background()
	b, err := f.BotRoleExport(ctx, "scout", "triage", "bot", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Method != "GET" ||
		got[0].Path != "/api/device/fleet/teknoir-demo/bots/scout/council/roles/triage/export" ||
		got[0].Query != "scope=bot&version=3" {
		t.Fatalf("requests %+v", got)
	}
	if string(b) != body {
		t.Errorf("bytes %q", b)
	}
	if !strings.Contains(string(b), "updatedAt: 2026-11-14T10:00:00Z") || !strings.HasSuffix(string(b), "\n") {
		t.Errorf("bytes %q", b)
	}
	if _, err := f.BotRoleExport(ctx, "scout", "triage", "", 0); err != nil {
		t.Fatal(err)
	}
	if got[1].Query != "" {
		t.Errorf("query %q", got[1].Query)
	}
}

func TestTeamspaceRoleRoutes(t *testing.T) {
	ctx := context.Background()
	base := "/api/device/fleet/teknoir-demo/council/roles/triage"

	var calls []councilCall
	f := councilServer(t, 200, `{"role":`+roleDetailJSON+`}`, &calls)
	d, _, err := f.TeamspaceRole(ctx, "triage")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Method != "GET" || calls[0].Path != base || calls[0].Query != "" {
		t.Fatalf("requests %+v", calls)
	}
	if !d.Detail || d.Slug != "triage" {
		t.Errorf("%+v", d)
	}

	calls = nil
	if _, _, err := f.PutTeamspaceRole(ctx, "triage", RolePut{Spec: json.RawMessage(`{"name":"Triage"}`), ExpectVersion: intPtr(4)}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Method != "PUT" || calls[0].Path != base {
		t.Fatalf("requests %+v", calls)
	}
	if calls[0].Body != `{"spec":{"name":"Triage"},"expectVersion":4}` {
		t.Errorf("body %s", calls[0].Body)
	}

	calls = nil
	if _, _, err := f.ArchiveTeamspaceRole(ctx, "triage"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Method != "POST" || calls[0].Path != base+"/archive" {
		t.Fatalf("requests %+v", calls)
	}
	if calls[0].Body != `{}` {
		t.Errorf("body %q", calls[0].Body)
	}

	// the export is YAML text, whatever its content
	var exports []councilCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exports = append(exports, councilCall{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery})
		w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
		_, _ = io.WriteString(w, "apiVersion: x\n")
	}))
	defer srv.Close()
	ef := &Fleet{Base: srv.URL + "/api/device/fleet/teknoir-demo", Token: func(context.Context) (string, error) { return "tok", nil }}
	if _, err := ef.TeamspaceRoleExport(ctx, "fleet", "template", 2); err != nil {
		t.Fatal(err)
	}
	if len(exports) != 1 || exports[0].Method != "GET" ||
		exports[0].Path != "/api/device/fleet/teknoir-demo/council/roles/fleet/export" ||
		exports[0].Query != "scope=template&version=2" {
		t.Fatalf("requests %+v", exports)
	}
}

// intPtr is a version or a count for the bodies below.
func intPtr(n int) *int { return &n }

func TestPutBotRoleBody(t *testing.T) {
	var calls []councilCall
	f := councilServer(t, 200, `{"role":`+roleDetailJSON+`,"action":"created"}`, &calls)
	ctx := context.Background()
	w, _, err := f.PutBotRole(ctx, "scout", "triage", RolePut{Spec: json.RawMessage(`{"name":"x","displayName":"X","kind":"member"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Method != "PUT" ||
		calls[0].Path != "/api/device/fleet/teknoir-demo/bots/scout/council/roles/triage" {
		t.Fatalf("requests %+v", calls)
	}
	if !strings.Contains(calls[0].Body, `"spec":{"name":"x","displayName":"X","kind":"member"}`) {
		t.Errorf("body %s (the spec keeps its key order)", calls[0].Body)
	}
	if calls[0].Body != `{"spec":{"name":"x","displayName":"X","kind":"member"}}` {
		t.Errorf("body %s (nil expectVersion, false restore and nil confirm are left out)", calls[0].Body)
	}
	if w.Action != "created" || w.Role.Slug != "triage" {
		t.Errorf("%+v", w)
	}

	calls = nil
	if _, _, err := f.PutBotRole(ctx, "scout", "triage", RolePut{
		Spec:          json.RawMessage(`{"name":"x"}`),
		ExpectVersion: intPtr(0),
		Restore:       true,
		Confirm:       []string{"inspect"},
	}); err != nil {
		t.Fatal(err)
	}
	if calls[0].Body != `{"spec":{"name":"x"},"expectVersion":0,"restore":true,"confirm":["inspect"]}` {
		t.Errorf("body %s", calls[0].Body)
	}

	calls = nil
	d, _, err := f.ArchiveBotRole(ctx, "scout", "triage")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Method != "POST" ||
		calls[0].Path != "/api/device/fleet/teknoir-demo/bots/scout/council/roles/triage/archive" {
		t.Fatalf("requests %+v", calls)
	}
	if calls[0].Body != `{}` {
		t.Errorf("body %q", calls[0].Body)
	}
	if !d.Detail || len(d.Versions) != 1 {
		t.Errorf("%+v", d)
	}
}

func TestPutRosterBody(t *testing.T) {
	var calls []councilCall
	f := councilServer(t, 200, `{"roster":{"id":"ros_2","name":"ops-a","displayName":"Ops A","scope":"bot","version":4,
"lead":"captain","members":["triage"],"reviewer":null,"isDefault":false,"legacyCompatible":true,"status":"ok","reasons":[],
"description":null,"budget":null,"roles":[]},"action":"updated"}`, &calls)
	w, _, err := f.PutRoster(context.Background(), "scout", "ops-a", RosterPut{
		Spec: RosterSpec{
			DisplayName: "Ops A",
			Lead:        RosterRef{Role: "captain", Scope: "template"},
			Members: []RosterRef{
				{Role: "triage", Scope: "teamspace", Version: intPtr(4)},
				{Role: "x"},
			},
		},
		ExpectVersion: intPtr(3),
		ExpectID:      "ros_AAAAAAAAAAAAAAAAAAAAAA",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Method != "PUT" ||
		calls[0].Path != "/api/device/fleet/teknoir-demo/bots/scout/council/rosters/ops-a" {
		t.Fatalf("requests %+v", calls)
	}
	const want = `{"spec":{"displayName":"Ops A","lead":{"role":"captain","scope":"template"},` +
		`"members":[{"role":"triage","scope":"teamspace","version":4},{"role":"x"}],"reviewer":null},` +
		`"expectVersion":3,"expectId":"ros_AAAAAAAAAAAAAAAAAAAAAA"}`
	if calls[0].Body != want {
		t.Errorf("body\n%s\nwant\n%s", calls[0].Body, want)
	}
	if w.Action != "updated" || w.Roster.Version != 4 {
		t.Errorf("%+v", w)
	}
}

func TestSetDefaultRosterBody(t *testing.T) {
	var calls []councilCall
	f := councilServer(t, 200, `{"council":{"proto":1,"defaultRoster":"ops","canEdit":{"ok":true},
"roster":{"name":"ops","members":["fleet"],"reviewer":null,"roles":[]},"rosters":[],"available":[],
"catalog":{},"caps":null}}`, &calls)
	v, _, err := f.SetDefaultRoster(context.Background(), "scout", "ops")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Method != "PUT" ||
		calls[0].Path != "/api/device/fleet/teknoir-demo/bots/scout/council/default" {
		t.Fatalf("requests %+v", calls)
	}
	if calls[0].Body != `{"roster":"ops"}` {
		t.Errorf("body %s", calls[0].Body)
	}
	if v.DefaultRoster != "ops" || !v.CanEdit.OK {
		t.Errorf("%+v", v)
	}
}

func TestDeleteRosterRoute(t *testing.T) {
	var calls []councilCall
	f := councilServer(t, 204, "", &calls)
	if err := f.DeleteRoster(context.Background(), "scout", "ops-a"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Method != "DELETE" ||
		calls[0].Path != "/api/device/fleet/teknoir-demo/bots/scout/council/rosters/ops-a" {
		t.Fatalf("requests %+v", calls)
	}
}

func TestRosterExportRoute(t *testing.T) {
	const body = "apiVersion: council.teknoir.org/v1alpha1\nkind: CouncilRoster\nmetadata:\n  name: ops-a\n"
	var got []councilCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, councilCall{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery})
		w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	f := &Fleet{Base: srv.URL + "/api/device/fleet/teknoir-demo", Token: func(context.Context) (string, error) { return "tok", nil }}
	b, err := f.RosterExport(context.Background(), "scout", "ops-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Method != "GET" ||
		got[0].Path != "/api/device/fleet/teknoir-demo/bots/scout/council/rosters/ops-a/export" || got[0].Query != "" {
		t.Fatalf("requests %+v", got)
	}
	if string(b) != body {
		t.Errorf("bytes %q", b)
	}
}

func TestDraftRoleBody(t *testing.T) {
	var calls []councilCall
	const answer = `{"slug":"scout","spec":{"name":"Scout","job":"finds things","kind":"member"},
"startsFrom":{"slug":"analyst","name":"Analyst"},"effectiveTools":null,
"warnings":[{"path":"spec.effort","message":"Effort lowered to medium: the platform allows at most medium."}]}`
	f := councilServer(t, 200, answer, &calls)
	ctx := context.Background()
	d, _, err := f.DraftRole(ctx, RoleDraftRequest{Name: "Scout", Job: "finds things"})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Method != "POST" ||
		calls[0].Path != "/api/device/fleet/teknoir-demo/council/roles/draft" || calls[0].Query != "" {
		t.Fatalf("requests %+v", calls)
	}
	if calls[0].Body != `{"name":"Scout","job":"finds things"}` {
		t.Errorf("body %s", calls[0].Body)
	}
	if d.Slug != "scout" || string(d.Spec) != `{"name":"Scout","job":"finds things","kind":"member"}` {
		t.Errorf("%+v", d)
	}
	if d.StartsFrom.Slug != "analyst" || d.StartsFrom.Name != "Analyst" || d.EffectiveTools != nil {
		t.Errorf("%+v", d)
	}
	if len(d.Warnings) != 1 || d.Warnings[0].Path != "spec.effort" {
		t.Errorf("%+v", d.Warnings)
	}

	calls = nil
	if _, _, err := f.DraftRole(ctx, RoleDraftRequest{
		Name: "Scout", Job: "finds things", How: "slowly", From: "analyst", Bot: "scout", Scope: "bot",
	}); err != nil {
		t.Fatal(err)
	}
	if calls[0].Body != `{"name":"Scout","job":"finds things","how":"slowly","from":"analyst","bot":"scout","scope":"bot"}` {
		t.Errorf("body %s", calls[0].Body)
	}
}

func TestApplyCouncilBody(t *testing.T) {
	const yamlText = "# a comment <&>\r\n---\r\n---\napiVersion: council.teknoir.org/v1alpha1  \nkind: CouncilRole\nmetadata:\n  name: helper\nspec:\n  description: Fässer & co\n"
	var calls []councilCall
	f := councilServer(t, 0, `{"results":[{"index":1,"kind":"CouncilRole","name":"helper","scope":"bot","bot":"scout","action":"created","version":1}]}`, &calls)
	ctx := context.Background()
	body := func(i int) map[string]json.RawMessage {
		t.Helper()
		if len(calls) <= i {
			t.Fatalf("%d requests, want call %d", len(calls), i)
		}
		c := calls[i]
		if c.Method != "POST" || c.Path != "/api/device/fleet/teknoir-demo/council/apply" || c.Query != "" {
			t.Errorf("call %d: %s %s?%s", i, c.Method, c.Path, c.Query)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(c.Body), &m); err != nil {
			t.Fatalf("body %s: %v", c.Body, err)
		}
		return m
	}
	r, _, err := f.ApplyCouncil(ctx, ApplyRequest{YAML: yamlText})
	if err != nil {
		t.Fatal(err)
	}
	b0 := body(0)
	if len(b0) != 1 {
		t.Errorf("body keys %v (dryRun and confirm are left out)", b0)
	}
	var got string
	if err := json.Unmarshal(b0["yaml"], &got); err != nil || got != yamlText {
		t.Errorf("yaml %q: %v", got, err)
	}
	if len(r.Results) != 1 || r.Results[0] != (ApplyItem{Index: 1, Kind: "CouncilRole", Name: "helper", Scope: "bot", Bot: "scout", Action: "created", Version: 1}) {
		t.Errorf("%+v", r)
	}

	if _, _, err := f.ApplyCouncil(ctx, ApplyRequest{YAML: yamlText, DryRun: true, Confirm: []string{"inspect"}}); err != nil {
		t.Fatal(err)
	}
	b1 := body(1)
	if string(b1["dryRun"]) != "true" {
		t.Errorf("body keys %v", b1)
	}
	var confirm []string
	if err := json.Unmarshal(b1["confirm"], &confirm); err != nil || len(confirm) != 1 || confirm[0] != "inspect" {
		t.Errorf("confirm %v: %v", confirm, err)
	}
}

func TestCouncilErrorOf(t *testing.T) {
	ctx := context.Background()
	councilErr := func(status int, body string) (*CouncilError, bool) {
		t.Helper()
		var calls []councilCall
		f := councilServer(t, status, body, &calls)
		_, _, err := f.BotCouncil(ctx, "scout")
		if err == nil {
			t.Fatalf("%d: no error", status)
		}
		return CouncilErrorOf(err)
	}

	ce, ok := councilErr(409, `{"code":"confirm_needed","message":"This member can read pods and logs.","needs":["inspect"],"documents":[1,3],"error":{"name":"CouncilError","message":"This member can read pods and logs."}}`)
	if !ok || ce.Code != "confirm_needed" || ce.Status != 409 || ce.Message != "This member can read pods and logs." {
		t.Fatalf("%+v %v", ce, ok)
	}
	if len(ce.Needs) != 1 || ce.Needs[0] != "inspect" || len(ce.Documents) != 2 || ce.Documents[0] != 1 || ce.Documents[1] != 3 {
		t.Errorf("%+v", ce)
	}

	ce, ok = councilErr(400, `{"code":"invalid","message":"the member is invalid","document":2,"fields":[{"path":"spec.name","message":"is required"},{"path":"spec.effort","message":"is not low, medium or high"}],"error":{"name":"CouncilError","message":"the member is invalid"}}`)
	if !ok || ce.Code != "invalid" || ce.Document == nil || *ce.Document != 2 {
		t.Fatalf("%+v %v", ce, ok)
	}
	if len(ce.Fields) != 2 || ce.Fields[0].Path != "spec.name" || ce.Fields[1].Message != "is not low, medium or high" {
		t.Errorf("%+v", ce.Fields)
	}

	ce, ok = councilErr(409, `{"code":"role_in_use","message":"This member is in a council.","usedBy":[{"bot":"scout","roster":"standard","displayName":"Standard"}],"hiddenCount":2,"error":{"name":"CouncilError","message":"This member is in a council."}}`)
	if !ok || len(ce.UsedBy) != 1 || ce.UsedBy[0].Roster != "standard" || ce.UsedBy[0].DisplayName != "Standard" || ce.HiddenCount != 2 {
		t.Fatalf("%+v %v", ce, ok)
	}

	ce, ok = councilErr(409, `{"code":"conflict","message":"changed","current":null,"error":{"name":"CouncilError","message":"changed"}}`)
	if !ok || string(ce.Current) != "null" {
		t.Errorf("current %q %v", ce.Current, ok)
	}
	ce, ok = councilErr(409, `{"code":"conflict","message":"archived","archived":true,"current":{"version":4,"updatedBy":"a@x","updatedAt":1},"error":{"name":"CouncilError","message":"archived"}}`)
	if !ok || !ce.Archived {
		t.Fatalf("%+v %v", ce, ok)
	}
	var cur struct {
		Version   int    `json:"version"`
		UpdatedBy string `json:"updatedBy"`
		UpdatedAt int64  `json:"updatedAt"`
	}
	if err := json.Unmarshal(ce.Current, &cur); err != nil || cur.Version != 4 || cur.UpdatedBy != "a@x" || cur.UpdatedAt != 1 {
		t.Errorf("%+v: %v", cur, err)
	}
	ce, ok = councilErr(409, `{"code":"exists","message":"there already is one","error":{"name":"CouncilError","message":"there already is one"}}`)
	if !ok || ce.Current != nil {
		t.Errorf("current %q %v", ce.Current, ok)
	}

	ce, ok = councilErr(403, `{"code":"not_admin","message":"Only teamspace admins can change shared members.","error":{"name":"CouncilError","message":"Only teamspace admins can change shared members."}}`)
	if !ok || ce.Status != 403 || ce.Message != "Only teamspace admins can change shared members." {
		t.Errorf("%+v %v", ce, ok)
	}

	ce, ok = councilErr(400, `{"code":"invalid","message":"bad\u001b[31m name","fields":[{"path":"spec.x\u001b","message":"m\u001b"}],"error":{"name":"CouncilError","message":"bad"}}`)
	if !ok || ce.Message != "bad[31m name" {
		t.Fatalf("%q %v", ce.Message, ok)
	}
	if len(ce.Fields) != 1 || ce.Fields[0].Path != "spec.x" || ce.Fields[0].Message != "m" {
		t.Errorf("%+v", ce.Fields)
	}

	if _, ok := councilErr(500, `{"error":{"name":"NotFoundError","message":"TeknoirBots are not turned on on this platform."}}`); ok {
		t.Error("a Backstage error is not a council error")
	}
	if _, ok := councilErr(500, "not json at all"); ok {
		t.Error("a body that is not JSON is not a council error")
	}
	if _, ok := CouncilErrorOf(errors.New("x")); ok {
		t.Error("a plain error is not a council error")
	}

	var calls []councilCall
	f := councilServer(t, 409, `{"code":"stale","message":"the member moved on","error":{"name":"CouncilError","message":"x"}}`, &calls)
	_, _, err := f.BotCouncil(ctx, "scout")
	if err == nil {
		t.Fatal("no error")
	}
	ce, ok = CouncilErrorOf(fmt.Errorf("putting triage: %w", err))
	if !ok || ce.Code != "stale" {
		t.Errorf("%+v %v", ce, ok)
	}

	// A key of an unexpected type stays unset and never hides the code and the message.
	ce, ok = councilErr(400, `{"code":"invalid","message":"m","document":"2","fields":[{"path":"p","message":"x"}]}`)
	if !ok || ce.Code != "invalid" || ce.Message != "m" || ce.Document != nil ||
		len(ce.Fields) != 1 || ce.Fields[0] != (CouncilField{Path: "p", Message: "x"}) {
		t.Errorf("mistyped document: %+v %v", ce, ok)
	}
	ce, ok = councilErr(409, `{"code":"role_in_use","message":"m","hiddenCount":2.5}`)
	if !ok || ce.HiddenCount != 0 {
		t.Errorf("fractional hiddenCount: %+v %v", ce, ok)
	}
	ce, ok = councilErr(400, `{"code":"invalid","message":"m","document":1,"fields":[{"path":1,"message":"mm"}]}`)
	if !ok || len(ce.Fields) != 0 || ce.Document == nil || *ce.Document != 1 {
		t.Errorf("mistyped field: %+v %v", ce, ok)
	}
	// Keys match exactly: "Code" is not "code".
	ce, ok = councilErr(409, `{"code":"x","Code":"y","message":"m"}`)
	if !ok || ce.Code != "x" {
		t.Errorf("code and Code: %+v %v", ce, ok)
	}
	for _, body := range []string{`{"code":null,"message":"m"}`, `null`, `[]`, `[{"code":"x"}]`, `"text"`,
		`{"code":7,"message":"m"}`, `{"code":""}`, `{"CODE":"x","message":"m"}`} {
		if ce, ok := councilErr(400, body); ok {
			t.Errorf("%s is not a council error: %+v", body, ce)
		}
	}
	if _, ok := CouncilErrorOf((*Error)(nil)); ok {
		t.Error("a nil *Error is not a council error")
	}
}

const rosterExportFixture = `apiVersion: council.teknoir.org/v1alpha1
kind: CouncilRoster
metadata:
  name: ops-a
  namespace: teknoir-demo
spec:
  scope: bot
  bot: scout
  displayName: Ops A
  description: Day to day.
  lead:
    role: captain
    scope: template
  members:
    - role: fleet
      scope: template
    - role: triage
      scope: teamspace
      version: 4
    - role: helper
      scope: bot
  reviewer: null
  budget:
    maxTokens: 150000
`

func TestDecodeRosterExport(t *testing.T) {
	spec, err := DecodeRosterExport([]byte(rosterExportFixture))
	if err != nil {
		t.Fatal(err)
	}
	if spec.DisplayName != "Ops A" || spec.Description == nil || *spec.Description != "Day to day." {
		t.Errorf("%+v", spec)
	}
	if spec.Lead.Role != "captain" || spec.Lead.Scope != "template" || spec.Lead.Version != nil {
		t.Errorf("lead %+v", spec.Lead)
	}
	if len(spec.Members) != 3 || spec.Members[0].Role != "fleet" ||
		spec.Members[1].Role != "triage" || spec.Members[1].Version == nil || *spec.Members[1].Version != 4 ||
		spec.Members[2].Role != "helper" {
		t.Errorf("members %+v", spec.Members)
	}
	if spec.Reviewer != nil {
		t.Errorf("reviewer %+v", spec.Reviewer)
	}
	if spec.Budget == nil || spec.Budget.MaxTokens != 150000 {
		t.Errorf("budget %+v", spec.Budget)
	}

	// a member written as a plain string is the slug alone
	spec, err = DecodeRosterExport([]byte(`apiVersion: council.teknoir.org/v1alpha1
kind: CouncilRoster
metadata:
  name: ops-b
spec:
  displayName: Ops B
  lead: captain
  members:
    - fleet
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Members) != 1 || spec.Members[0] != (RosterRef{Role: "fleet"}) || spec.Lead != (RosterRef{Role: "captain"}) {
		t.Errorf("%+v", spec)
	}
	if spec.Description != nil || spec.Budget != nil {
		t.Errorf("%+v", spec)
	}

	// a reviewer object gives a reviewer
	spec, err = DecodeRosterExport([]byte(`apiVersion: council.teknoir.org/v1alpha1
kind: CouncilRoster
metadata:
  name: ops-c
spec:
  displayName: Ops C
  lead:
    role: captain
  members:
    - role: fleet
  reviewer:
    role: review
    scope: template
`))
	if err != nil {
		t.Fatal(err)
	}
	if spec.Reviewer == nil || spec.Reviewer.Role != "review" || spec.Reviewer.Scope != "template" {
		t.Errorf("reviewer %+v", spec.Reviewer)
	}

	// a premade council's export decodes the same way
	spec, err = DecodeRosterExport([]byte(`apiVersion: council.teknoir.org/v1alpha1
kind: CouncilRoster
metadata:
  name: standard
spec:
  scope: template
  version: 2
  displayName: Standard
  lead:
    role: captain
    scope: template
  members:
    - role: fleet
      scope: template
  reviewer: null
`))
	if err != nil {
		t.Fatal(err)
	}
	if spec.DisplayName != "Standard" || spec.Description != nil || spec.Budget != nil || len(spec.Members) != 1 {
		t.Errorf("%+v", spec)
	}

	const prefix = "the platform's council export could not be read: "
	for _, c := range []struct {
		name, text, want string
	}{
		{"a member with no role", "apiVersion: council.teknoir.org/v1alpha1\nkind: CouncilRoster\nspec:\n  lead:\n    role: captain\n  members:\n    - role: \"\"\n", "role"},
		{"two documents", rosterExportFixture + "---\n" + rosterExportFixture, "more than one document"},
		{"the wrong kind", "apiVersion: council.teknoir.org/v1alpha1\nkind: CouncilRole\n", "CouncilRole"},
		{"the wrong apiVersion", "apiVersion: council.teknoir.org/v1beta1\nkind: CouncilRoster\n", "council.teknoir.org/v1beta1"},
		{"invalid YAML", "a: b: c\n", ""},
		{"no members", "apiVersion: council.teknoir.org/v1alpha1\nkind: CouncilRoster\nspec:\n  lead:\n    role: captain\n", "member"},
		{"not an object", "hello\n", "it is a scalar, not an object"},
		{"a list", "- a\n- b\n", "it is a list, not an object"},
		{"a spec that is a list", "apiVersion: council.teknoir.org/v1alpha1\nkind: CouncilRoster\nspec: [a]\n", "its spec is a list, not an object"},
		{"the wrong kind with an odd spec", "apiVersion: council.teknoir.org/v1alpha1\nkind: CouncilRole\nspec: {members: 5}\n", `kind "CouncilRole" is not CouncilRoster`},
	} {
		_, err := DecodeRosterExport([]byte(c.text))
		if err == nil {
			t.Errorf("%s: no error", c.name)
			continue
		}
		if !strings.HasPrefix(err.Error(), prefix) {
			t.Errorf("%s: %v", c.name, err)
		}
		if c.want != "" && !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func TestPatchChatThreadBody(t *testing.T) {
	var calls []councilCall
	const answer = `{"thread":{"id":"thr_1","title":"t","origin":"cli","member":null,"createdAt":0,"updatedAt":1,"turnCount":0,"lastTurn":null,
"roster":{"id":"ros_2","name":"review","displayName":"Review"},"membersPin":null,"memberRole":null}}`
	f := councilServer(t, 200, answer, &calls)
	ctx := context.Background()
	review := "review"
	th, _, err := f.PatchChatThread(ctx, "scout", "thr_1", ChatThreadCouncilPatch{SetRoster: true, Roster: &review})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Method != "PATCH" ||
		calls[0].Path != "/api/device/fleet/teknoir-demo/bots/scout/chat/threads/thr_1" {
		t.Fatalf("requests %+v", calls)
	}
	if calls[0].Body != `{"roster":"review"}` {
		t.Errorf("body %s", calls[0].Body)
	}
	if th.Roster == nil || th.Roster.Name != "review" || th.MembersPin != nil || th.MemberRole != nil {
		t.Errorf("%+v", th)
	}

	calls = nil
	if _, _, err := f.PatchChatThread(ctx, "scout", "thr_1", ChatThreadCouncilPatch{SetRoster: true}); err != nil {
		t.Fatal(err)
	}
	if calls[0].Body != `{"roster":null}` {
		t.Errorf("body %s (a nil roster is sent as null)", calls[0].Body)
	}

	calls = nil
	if _, _, err := f.PatchChatThread(ctx, "scout", "thr_1", ChatThreadCouncilPatch{}); err == nil ||
		err.Error() != "a thread patch names a roster" {
		t.Fatalf("%v", err)
	}
	if len(calls) != 0 {
		t.Errorf("a patch with no roster must not be sent: %+v", calls)
	}
}

func TestCreateChatThreadCouncilFields(t *testing.T) {
	var calls []councilCall
	f := councilServer(t, 201, `{"id":"thr_2"}`, &calls)
	ctx := context.Background()
	if _, _, err := f.CreateChatThread(ctx, "scout", NewChatThread{Title: "t", Origin: "cli", Member: "triage", MemberScope: "teamspace"}); err != nil {
		t.Fatal(err)
	}
	if calls[0].Body != `{"title":"t","origin":"cli","member":"triage","memberScope":"teamspace"}` {
		t.Errorf("body %s", calls[0].Body)
	}
	if _, _, err := f.CreateChatThread(ctx, "scout", NewChatThread{Title: "t", Roster: "review"}); err != nil {
		t.Fatal(err)
	}
	if calls[1].Body != `{"title":"t","roster":"review"}` {
		t.Errorf("body %s", calls[1].Body)
	}
	if _, _, err := f.CreateChatThread(ctx, "scout", NewChatThread{Title: "t"}); err != nil {
		t.Fatal(err)
	}
	if calls[2].Body != `{"title":"t"}` {
		t.Errorf("body %s (empty memberScope and roster are left out)", calls[2].Body)
	}
}

func TestPostChatTurnThorough(t *testing.T) {
	var calls []councilCall
	f := councilServer(t, 202, `{"turn":{"id":"trn_1","state":"queued","options":{"mode":"council","thorough":true}}}`, &calls)
	ctx := context.Background()
	tu, _, err := f.PostChatTurn(ctx, "scout", "thr_1", NewChatTurn{Prompt: "p", Mode: "council", Thorough: true})
	if err != nil {
		t.Fatal(err)
	}
	if calls[0].Body != `{"prompt":"p","mode":"council","thorough":true}` {
		t.Errorf("body %s", calls[0].Body)
	}
	if !tu.Options.Thorough {
		t.Errorf("%+v", tu)
	}
	if _, _, err := f.PostChatTurn(ctx, "scout", "thr_1", NewChatTurn{Prompt: "p"}); err != nil {
		t.Fatal(err)
	}
	if calls[1].Body != `{"prompt":"p"}` {
		t.Errorf("body %s (thorough false is left out)", calls[1].Body)
	}
}

func TestChatCouncilShapes(t *testing.T) {
	ctx := context.Background()

	// P1 from an older platform: no council key at all
	var calls []councilCall
	f := councilServer(t, 200, `{"bot":{"name":"scout"},"canChat":true}`, &calls)
	info, _, err := f.ChatInfo(ctx, "scout")
	if err != nil {
		t.Fatal(err)
	}
	if info.Council != nil {
		t.Errorf("council %+v", info.Council)
	}

	calls = nil
	f = councilServer(t, 200, `{"bot":{"name":"scout"},"canChat":true,"council":{"proto":1,"tnctl":null,"kit":null,
"capsError":"the bot's computer has not reported its tools","defaultRoster":"standard",
"canEdit":{"ok":true},
"roster":{"id":"ros_1","name":"standard","displayName":"Standard","scope":"bot","version":1,"lead":"captain",
"members":["triage"],"reviewer":null,"isDefault":true,"legacyCompatible":true,"status":"ok","reasons":[],
"description":null,"budget":null,"roles":[]},
"rosters":[{"id":"ros_1","name":"standard","displayName":"Standard","scope":"bot","version":1,"lead":"captain",
"members":["triage"],"reviewer":null,"isDefault":true,"legacyCompatible":true,"status":"ok","reasons":[]}]}}`, &calls)
	info, _, err = f.ChatInfo(ctx, "scout")
	if err != nil {
		t.Fatal(err)
	}
	if info.Council == nil || info.Council.DefaultRoster != "standard" || !info.Council.CanEdit.OK ||
		len(info.Council.Roster.Members) != 1 || len(info.Council.Rosters) != 1 {
		t.Fatalf("%+v", info.Council)
	}
	if info.Council.CapsError == nil || info.Council.Tnctl != nil {
		t.Errorf("%+v", info.Council)
	}

	// P4 of a member thread: council is null
	calls = nil
	f = councilServer(t, 200, `{"thread":{},"turns":[],"lastSeq":0,"council":null}`, &calls)
	d, _, err := f.ChatThread(ctx, "scout", "thr_1")
	if err != nil {
		t.Fatal(err)
	}
	if d.Council != nil {
		t.Errorf("council %+v", d.Council)
	}

	// P4 of a council thread: the council's summary
	calls = nil
	f = councilServer(t, 200, `{"thread":{},"turns":[],"lastSeq":0,"council":{"roster":`+rosterSummaryJSON+`}}`, &calls)
	d, _, err = f.ChatThread(ctx, "scout", "thr_1")
	if err != nil {
		t.Fatal(err)
	}
	if d.Council == nil || d.Council.Roster.Name != "standard" || d.Council.Roster.Lead != "captain" {
		t.Errorf("%+v", d.Council)
	}

	// a thread with a roster, a pin and a member role, and a turn with a roster, a hash and thorough
	calls = nil
	f = councilServer(t, 200, `{"thread":{"id":"thr_1","roster":{"id":"ros_2","name":"review","displayName":"Review"},
"membersPin":["review"],"memberRole":{"id":"r1","scope":"teamspace","name":"Triage"}},
"turns":[{"id":"trn_1","options":{"mode":"council","effort":"high","thorough":true},"roster":{"id":"ros_2","name":"review"},"rosterHash":"abc"}],
"lastSeq":1,"council":null}`, &calls)
	d, _, err = f.ChatThread(ctx, "scout", "thr_1")
	if err != nil {
		t.Fatal(err)
	}
	th := d.Thread
	if th.Roster == nil || th.Roster.ID != "ros_2" || th.Roster.DisplayName != "Review" {
		t.Errorf("roster %+v", th.Roster)
	}
	if len(th.MembersPin) != 1 || th.MembersPin[0] != "review" {
		t.Errorf("membersPin %+v", th.MembersPin)
	}
	if th.MemberRole == nil || th.MemberRole.Scope != "teamspace" || th.MemberRole.Name != "Triage" {
		t.Errorf("memberRole %+v", th.MemberRole)
	}
	tu := d.Turns[0]
	if tu.Roster == nil || tu.Roster.Name != "review" || tu.RosterHash == nil || *tu.RosterHash != "abc" || !tu.Options.Thorough {
		t.Errorf("%+v", tu)
	}
}

// A RosterView carries its stored references when the platform sends them: pins and gone members included.
func TestRosterViewRefs(t *testing.T) {
	const summary = `"id":"ros_1","name":"ops","displayName":"Ops","scope":"bot","version":2,"lead":"captain",
"members":["triage","rol_BBBBBBBBBBBBBBBBBBBBBB"],"reviewer":null,"isDefault":true,"legacyCompatible":false,
"status":"invalid","reasons":["A member is gone."],"description":null,"budget":null,"roles":[]`
	var w RosterWrite
	if err := json.Unmarshal([]byte(`{"roster":{`+summary+`,"refs":{
"lead":{"roleId":"tpl:captain","role":"captain","scope":"template","version":null,"gone":false},
"members":[{"roleId":"rol_AAAAAAAAAAAAAAAAAAAAAA","role":"triage","scope":"teamspace","version":4,"gone":false},
{"roleId":"rol_BBBBBBBBBBBBBBBBBBBBBB","role":"rol_BBBBBBBBBBBBBBBBBBBBBB","scope":null,"version":null,"gone":true}],
"reviewer":null}},"action":"updated"}`), &w); err != nil {
		t.Fatal(err)
	}
	r := w.Roster.Refs
	if r == nil || r.Lead == nil || r.Lead.Role != "captain" || r.Lead.RoleID != "tpl:captain" || r.Lead.Version != nil ||
		r.Lead.Scope == nil || *r.Lead.Scope != "template" || r.Lead.Gone || r.Reviewer != nil {
		t.Fatalf("refs %+v", r)
	}
	if len(r.Members) != 2 || r.Members[0].Version == nil || *r.Members[0].Version != 4 ||
		r.Members[1].Scope != nil || !r.Members[1].Gone {
		t.Errorf("members %+v", r.Members)
	}
	for i, m := range r.Members {
		if m.Role != w.Roster.Members[i] {
			t.Errorf("refs.members[%d] is %q, members[%d] is %q", i, m.Role, i, w.Roster.Members[i])
		}
	}
	// The stub of a missing default council has empty refs.
	var stub RosterView
	if err := json.Unmarshal([]byte(`{`+summary+`,"refs":{"lead":null,"members":[],"reviewer":null}}`), &stub); err != nil {
		t.Fatal(err)
	}
	if stub.Refs == nil || stub.Refs.Lead != nil || stub.Refs.Reviewer != nil || stub.Refs.Members == nil || len(stub.Refs.Members) != 0 {
		t.Errorf("stub refs %+v", stub.Refs)
	}
	// An older platform sends no refs.
	var old RosterView
	if err := json.Unmarshal([]byte(`{`+summary+`}`), &old); err != nil || old.Refs != nil {
		t.Errorf("refs %+v: %v", old.Refs, err)
	}
}

// A JSON null leaves a member as it was, as encoding/json does for any struct.
func TestRoleDetailNullKeepsIt(t *testing.T) {
	d := RoleDetail{RoleView: RoleView{Slug: "keep"}, Detail: true}
	if err := json.Unmarshal([]byte("null"), &d); err != nil || d.Slug != "keep" || !d.Detail {
		t.Errorf("%+v %v", d, err)
	}
	var a roleAnswer
	if err := json.Unmarshal([]byte(`{"role":{"slug":"a","version":"x"}}`), &a); err == nil || strings.Contains(err.Error(), "alias") {
		t.Errorf("error %v names the internal type", err)
	}
}

// An export's budget is nil unless it names maxTokens; a type error inside a reference stays a *yaml.TypeError.
func TestDecodeRosterExportBudgetAndErrors(t *testing.T) {
	const head = "apiVersion: council.teknoir.org/v1alpha1\nkind: CouncilRoster\nspec:\n  displayName: x\n  lead: a\n  members:\n    - b\n"
	for _, b := range []string{"  budget: {}\n", "  budget: {maxTokens: null}\n", "  budget: null\n", ""} {
		s, err := DecodeRosterExport([]byte(head + b))
		if err != nil || s.Budget != nil {
			t.Errorf("%q: %+v %v", b, s, err)
		}
	}
	s, err := DecodeRosterExport([]byte(head + "  budget: {maxTokens: 150000}\n"))
	if err != nil || s.Budget == nil || s.Budget.MaxTokens != 150000 {
		t.Errorf("%+v %v", s, err)
	}
	_, err = DecodeRosterExport([]byte("apiVersion: council.teknoir.org/v1alpha1\nkind: CouncilRoster\nspec:\n  lead: {role: a, version: four}\n  members:\n    - b\n"))
	var te *yaml.TypeError
	if err == nil || !errors.As(err, &te) || !strings.HasPrefix(err.Error(), "the platform's council export could not be read: ") {
		t.Errorf("error %v", err)
	}
}

// Path segments are escaped: a bot, a council's name and a member's slug are each one segment. The names
// hold '/', which only url.PathEscape turns into %2F (Go's client re-escapes a space by itself).
func TestCouncilPathsAreEscaped(t *testing.T) {
	var calls []councilCall
	f := councilServer(t, 200, `{}`, &calls)
	ctx := context.Background()
	_, _, _ = f.BotCouncil(ctx, "a/b")
	_, _, _ = f.SetDefaultRoster(ctx, "a/b", "ops")
	_, _, _ = f.PutRoster(ctx, "scout", "ops/a", RosterPut{})
	_ = f.DeleteRoster(ctx, "scout", "ops/a")
	_, _ = f.RosterExport(ctx, "scout", "ops/a")
	_, _, _ = f.BotRole(ctx, "scout", "tri/age", "")
	_, _ = f.BotRoleExport(ctx, "scout", "tri/age", "", 0)
	_, _, _ = f.PutBotRole(ctx, "scout", "tri/age", RolePut{Spec: json.RawMessage(`{}`)})
	_, _, _ = f.ArchiveBotRole(ctx, "scout", "tri/age")
	_, _, _ = f.TeamspaceRole(ctx, "tri/age")
	_, _ = f.TeamspaceRoleExport(ctx, "tri/age", "", 0)
	_, _, _ = f.PutTeamspaceRole(ctx, "tri/age", RolePut{Spec: json.RawMessage(`{}`)})
	_, _, _ = f.ArchiveTeamspaceRole(ctx, "tri/age")
	_, _, _ = f.PatchChatThread(ctx, "a/b", "thr/1", ChatThreadCouncilPatch{SetRoster: true})
	var paths []string
	for _, c := range calls {
		paths = append(paths, c.Method+" "+c.Path)
	}
	const p = "/api/device/fleet/teknoir-demo/"
	want := []string{
		"GET " + p + "bots/a%2Fb/council",
		"PUT " + p + "bots/a%2Fb/council/default",
		"PUT " + p + "bots/scout/council/rosters/ops%2Fa",
		"DELETE " + p + "bots/scout/council/rosters/ops%2Fa",
		"GET " + p + "bots/scout/council/rosters/ops%2Fa/export",
		"GET " + p + "bots/scout/council/roles/tri%2Fage",
		"GET " + p + "bots/scout/council/roles/tri%2Fage/export",
		"PUT " + p + "bots/scout/council/roles/tri%2Fage",
		"POST " + p + "bots/scout/council/roles/tri%2Fage/archive",
		"GET " + p + "council/roles/tri%2Fage",
		"GET " + p + "council/roles/tri%2Fage/export",
		"PUT " + p + "council/roles/tri%2Fage",
		"POST " + p + "council/roles/tri%2Fage/archive",
		"PATCH " + p + "bots/a%2Fb/chat/threads/thr%2F1",
	}
	if strings.Join(paths, "\n") != strings.Join(want, "\n") {
		t.Errorf("paths\n%s\nwant\n%s", strings.Join(paths, "\n"), strings.Join(want, "\n"))
	}
}

// DecodeRosterExport skips empty documents, refuses a file with none, and checks every reference's role.
func TestDecodeRosterExportEdges(t *testing.T) {
	for _, text := range []string{
		"---\n" + rosterExportFixture,
		"---\n---\n" + rosterExportFixture,
		rosterExportFixture + "---\n",
	} {
		s, err := DecodeRosterExport([]byte(text))
		if err != nil || s.DisplayName != "Ops A" {
			t.Errorf("%q: %v", text, err)
		}
	}
	const head = "apiVersion: council.teknoir.org/v1alpha1\nkind: CouncilRoster\nspec:\n"
	for _, c := range []struct{ name, text, want string }{
		{"empty", "", "no document"},
		{"only a separator", "---\n", "no document"},
		{"a lead with no role", head + "  lead:\n    scope: template\n  members:\n    - fleet\n", "lead"},
		{"a reviewer with no role", head + "  lead: captain\n  members:\n    - fleet\n  reviewer:\n    scope: template\n", "reviewer"},
		{"members not a list", head + "  lead: captain\n  members: fleet\n", ""},
		{"a lead that is a list", head + "  lead: [captain]\n  members:\n    - fleet\n", "not a list"},
	} {
		_, err := DecodeRosterExport([]byte(c.text))
		if err == nil || !strings.HasPrefix(err.Error(), "the platform's council export could not be read: ") ||
			!strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	s, err := DecodeRosterExport([]byte(head + "  displayName: 2026-11-14\n  description: null\n  lead: captain\n  members:\n    - fleet\n  budget: null\n"))
	if err != nil || s.DisplayName != "2026-11-14" || s.Description != nil || s.Budget != nil {
		t.Errorf("%+v %v", s, err)
	}
	var r RosterRef
	if err := yaml.Unmarshal([]byte("[fleet]\n"), &r); err == nil {
		t.Errorf("a list is not a member reference: %+v", r)
	}
}

// Every field of a member, a council and the caps decodes from its hub key.
func TestCouncilShapesAllFields(t *testing.T) {
	const role = `{"id":"r1","slug":"triage","name":"Triage","kind":"member","runtime":"inproc","model":"sonnet",
"modelId":"claude-sonnet-5-5","effort":"medium","description":"sorts the queue","groups":["core_read"],"effectiveTools":["read"],
"level":"read","ceiling":"read","skills":["search"],"scope":"teamspace","version":4,"category":"operations",
"templateRef":{"name":"triage","version":1},"status":"invalid","reasons":["r"],"notes":["n"],"instructions":"Triage.",
"persona":"Calm.","budget":{"maxToolRounds":8,"maxTokens":40000},"versions":[{"version":4,"createdBy":"a@x","createdAt":1}],
"createdBy":"a@x","updatedBy":"b@x","updatedAt":2,"archivedAt":3,"archivedBy":"c@x",
"usedBy":[{"bot":"scout","roster":"ops","displayName":"Ops"}],"hiddenCount":1}`
	var d RoleDetail
	if err := json.Unmarshal([]byte(role), &d); err != nil {
		t.Fatal(err)
	}
	str := func(s string) *string { return &s }
	i64 := func(n int64) *int64 { return &n }
	tools := []string{"read"}
	want := RoleDetail{
		RoleView: RoleView{ID: "r1", Slug: "triage", Name: "Triage", Kind: "member", Runtime: "inproc", Model: "sonnet",
			ModelID: "claude-sonnet-5-5", Effort: "medium", Description: "sorts the queue", Groups: []string{"core_read"},
			EffectiveTools: &tools, Level: "read", Ceiling: "read", Skills: []string{"search"}, Scope: "teamspace",
			Version: 4, Category: "operations", TemplateRef: &RoleTemplateRef{Name: "triage", Version: 1}, Status: "invalid",
			Reasons: []string{"r"}, Notes: []string{"n"}},
		Instructions: str("Triage."), Persona: str("Calm."), Budget: &RoleBudget{MaxToolRounds: 8, MaxTokens: 40000},
		Versions:  []RoleVersion{{Version: 4, CreatedBy: "a@x", CreatedAt: 1}},
		CreatedBy: str("a@x"), UpdatedBy: str("b@x"), UpdatedAt: i64(2), ArchivedAt: i64(3), ArchivedBy: str("c@x"),
		UsedBy: []CouncilUse{{Bot: "scout", Roster: "ops", DisplayName: "Ops"}}, HiddenCount: intPtr(1), Detail: true,
	}
	if !reflect.DeepEqual(d, want) {
		t.Errorf("role\n%+v\nwant\n%+v", d, want)
	}

	const council = `{"proto":2,"tnctl":"0.4.0","kit":"0.2.0","capsError":"late","defaultRoster":"ops",
"canEdit":{"ok":true},
"roster":{"id":"ros_1","name":"ops","displayName":"Ops","scope":"bot","version":2,"lead":"captain","members":["triage"],
"reviewer":"skeptic","isDefault":true,"legacyCompatible":true,"status":"invalid","reasons":["gone"],
"description":"Day to day.","budget":{"maxTokens":150000},"roles":[]},
"rosters":[],"available":[],"catalog":{},
"caps":{"proto":2,"tnctl":"0.4.0","kit":"0.2.0","tools":["read"],"skills":["search"],"capsError":"late"}}`
	var v CouncilView
	if err := json.Unmarshal([]byte(council), &v); err != nil {
		t.Fatal(err)
	}
	wantRoster := RosterView{
		RosterSummary: RosterSummary{ID: "ros_1", Name: "ops", DisplayName: "Ops", Scope: "bot", Version: 2, Lead: "captain",
			Members: []string{"triage"}, Reviewer: str("skeptic"), IsDefault: true, LegacyCompatible: true, Status: "invalid",
			Reasons: []string{"gone"}},
		Description: str("Day to day."), Budget: &RosterBudget{MaxTokens: 150000}, Roles: []RoleView{},
	}
	if !reflect.DeepEqual(v.Roster, wantRoster) {
		t.Errorf("roster\n%+v\nwant\n%+v", v.Roster, wantRoster)
	}
	skills := []string{"search"}
	wantCaps := &CouncilCaps{Proto: 2, Tnctl: str("0.4.0"), Kit: str("0.2.0"), Tools: &tools, Skills: &skills, CapsError: str("late")}
	if !reflect.DeepEqual(v.Caps, wantCaps) {
		t.Errorf("caps\n%+v\nwant\n%+v", v.Caps, wantCaps)
	}
	if v.Proto == nil || *v.Proto != 2 || v.Tnctl == nil || *v.Tnctl != "0.4.0" || v.Kit == nil ||
		*v.Kit != "0.2.0" || v.CapsError == nil || *v.CapsError != "late" || !v.CanEdit.OK {
		t.Errorf("%+v", v)
	}
}

// The catalog's raw groups and limits, an apply result, and the chat's council names decode from their hub keys.
func TestCouncilSmallShapes(t *testing.T) {
	const groups = `[{"name":"core_read","label":"Platform read","description":"d","examples":["whoami"]}]`
	var c Catalog
	if err := json.Unmarshal([]byte(`{"groups":`+groups+`,"skills":[],"limits":{"maxTokens":100000}}`), &c); err != nil {
		t.Fatal(err)
	}
	if string(c.Groups) != groups || string(c.Limits) != `{"maxTokens":100000}` {
		t.Errorf("%s %s", c.Groups, c.Limits)
	}
	var r ApplyResult
	if err := json.Unmarshal([]byte(`{"results":[{"index":1,"kind":"CouncilRole","name":"helper","scope":"bot","bot":"scout","action":"created","version":1}]}`), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Results) != 1 || r.Results[0] != (ApplyItem{Index: 1, Kind: "CouncilRole", Name: "helper", Scope: "bot", Bot: "scout", Action: "created", Version: 1}) {
		t.Errorf("%+v", r)
	}
	var th ChatThread
	if err := json.Unmarshal([]byte(`{"memberRole":{"id":"r1","scope":"teamspace","name":"Triage"}}`), &th); err != nil {
		t.Fatal(err)
	}
	if th.MemberRole == nil || *th.MemberRole != (ChatMemberRole{ID: "r1", Scope: "teamspace", Name: "Triage"}) {
		t.Errorf("%+v", th.MemberRole)
	}
	var tu ChatTurn
	if err := json.Unmarshal([]byte(`{"roster":{"id":"ros_2","name":"review"}}`), &tu); err != nil {
		t.Fatal(err)
	}
	if tu.Roster == nil || tu.Roster.ID != "ros_2" || tu.Roster.Name != "review" {
		t.Errorf("%+v", tu.Roster)
	}
	var ic ChatInfoCouncil
	if err := json.Unmarshal([]byte(`{"proto":1,"tnctl":"0.4.0","kit":"0.2.0"}`), &ic); err != nil {
		t.Fatal(err)
	}
	if ic.Proto == nil || *ic.Proto != 1 || ic.Tnctl == nil || *ic.Tnctl != "0.4.0" || ic.Kit == nil || *ic.Kit != "0.2.0" {
		t.Errorf("%+v", ic)
	}
}

// A council's PUT leaves out an unset expectVersion and expectId, and sends description, reviewer and budget when set.
func TestPutRosterOptionalFields(t *testing.T) {
	var calls []councilCall
	f := councilServer(t, 200, `{"roster":{},"action":"created"}`, &calls)
	ctx := context.Background()
	if _, _, err := f.PutRoster(ctx, "scout", "ops-a", RosterPut{Spec: RosterSpec{
		DisplayName: "Ops A", Lead: RosterRef{Role: "captain"}, Members: []RosterRef{{Role: "x"}},
	}}); err != nil {
		t.Fatal(err)
	}
	desc := "Day to day."
	if _, _, err := f.PutRoster(ctx, "scout", "ops-a", RosterPut{Spec: RosterSpec{
		DisplayName: "Ops A", Description: &desc, Lead: RosterRef{Role: "captain"}, Members: []RosterRef{{Role: "x"}},
		Reviewer: &RosterRef{Role: "skeptic", Scope: "template"}, Budget: &RosterBudget{MaxTokens: 150000},
	}, ExpectVersion: intPtr(0)}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0].Method != "PUT" || calls[1].Method != "PUT" {
		t.Fatalf("requests %+v", calls)
	}
	if calls[0].Body != `{"spec":{"displayName":"Ops A","lead":{"role":"captain"},"members":[{"role":"x"}],"reviewer":null}}` {
		t.Errorf("body %s", calls[0].Body)
	}
	if calls[1].Body != `{"spec":{"displayName":"Ops A","description":"Day to day.","lead":{"role":"captain"},"members":[{"role":"x"}],"reviewer":{"role":"skeptic","scope":"template"},"budget":{"maxTokens":150000}},"expectVersion":0}` {
		t.Errorf("body %s", calls[1].Body)
	}
}

// Every JSON method returns the whole answer body as raw, its wrapper included.
func TestCouncilRawIsWholeAnswer(t *testing.T) {
	const answer = `{"council":{"defaultRoster":"ops"},"role":{"slug":"triage","versions":[]},"roster":{"name":"ops"},"action":"created","thread":{"id":"thr_1"},"results":[],"slug":"s"}`
	var calls []councilCall
	f := councilServer(t, 200, answer, &calls)
	ctx := context.Background()
	check := func(name string, raw json.RawMessage, err error) {
		t.Helper()
		if err != nil || string(raw) != answer {
			t.Errorf("%s: raw %s, err %v", name, raw, err)
		}
	}
	_, raw, err := f.BotCouncil(ctx, "scout")
	check("BotCouncil", raw, err)
	_, raw, err = f.SetDefaultRoster(ctx, "scout", "ops")
	check("SetDefaultRoster", raw, err)
	_, raw, err = f.PutRoster(ctx, "scout", "ops", RosterPut{})
	check("PutRoster", raw, err)
	_, raw, err = f.BotRole(ctx, "scout", "triage", "")
	check("BotRole", raw, err)
	_, raw, err = f.PutBotRole(ctx, "scout", "triage", RolePut{Spec: json.RawMessage(`{}`)})
	check("PutBotRole", raw, err)
	_, raw, err = f.ArchiveBotRole(ctx, "scout", "triage")
	check("ArchiveBotRole", raw, err)
	_, raw, err = f.CouncilCatalog(ctx)
	check("CouncilCatalog", raw, err)
	_, raw, err = f.TeamspaceRole(ctx, "triage")
	check("TeamspaceRole", raw, err)
	_, raw, err = f.PutTeamspaceRole(ctx, "triage", RolePut{Spec: json.RawMessage(`{}`)})
	check("PutTeamspaceRole", raw, err)
	_, raw, err = f.ArchiveTeamspaceRole(ctx, "triage")
	check("ArchiveTeamspaceRole", raw, err)
	_, raw, err = f.DraftRole(ctx, RoleDraftRequest{Name: "n", Job: "j"})
	check("DraftRole", raw, err)
	_, raw, err = f.ApplyCouncil(ctx, ApplyRequest{YAML: "x"})
	check("ApplyCouncil", raw, err)
	_, raw, err = f.PatchChatThread(ctx, "scout", "thr_1", ChatThreadCouncilPatch{SetRoster: true})
	check("PatchChatThread", raw, err)
}

// A shared member's PUT and archive answers decode, and its export comes back byte for byte.
func TestTeamspaceRoleAnswers(t *testing.T) {
	var calls []councilCall
	f := councilServer(t, 200, `{"role":`+roleDetailJSON+`,"action":"updated"}`, &calls)
	ctx := context.Background()
	w, _, err := f.PutTeamspaceRole(ctx, "triage", RolePut{Spec: json.RawMessage(`{}`)})
	if err != nil || w.Action != "updated" || w.Role.Slug != "triage" || !w.Role.Detail {
		t.Errorf("%+v %v", w, err)
	}
	a, _, err := f.ArchiveTeamspaceRole(ctx, "triage")
	if err != nil || a.Slug != "triage" || !a.Detail {
		t.Errorf("%+v %v", a, err)
	}
	ef := councilServer(t, 200, "apiVersion: x\n", &calls)
	b, err := ef.TeamspaceRoleExport(ctx, "fleet", "template", 2)
	if err != nil || string(b) != "apiVersion: x\n" {
		t.Errorf("%q %v", b, err)
	}
}

// Detail is the presence of the top-level "versions" key, whatever its value, and nothing else.
func TestRoleDetailIsVersionsKey(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		// The key decides, not its value.
		{`{"slug":"a","versions":null}`, true},
		// Instructions without versions is still a view.
		{`{"slug":"a","instructions":"x"}`, false},
		// Only a top-level key counts, not a value or a nested key.
		{`{"slug":"a","name":"versions","templateRef":{"name":"t","version":1,"versions":[]}}`, false},
	}
	for _, c := range cases {
		var d RoleDetail
		if err := json.Unmarshal([]byte(c.in), &d); err != nil || d.Detail != c.want {
			t.Errorf("%s: Detail=%v err=%v, want %v", c.in, d.Detail, err, c.want)
		}
	}
}

// The GETs and the DELETE send no body (Do with a nil body), so no Content-Type either.
func TestCouncilReadsSendNoBody(t *testing.T) {
	var calls []councilCall
	f := councilServer(t, 200, `{}`, &calls)
	ctx := context.Background()
	steps := []struct {
		name string
		call func() error
	}{
		{"BotCouncil", func() error { _, _, err := f.BotCouncil(ctx, "scout"); return err }},
		{"CouncilCatalog", func() error { _, _, err := f.CouncilCatalog(ctx); return err }},
		{"BotRole", func() error { _, _, err := f.BotRole(ctx, "scout", "triage", "template"); return err }},
		{"TeamspaceRole", func() error { _, _, err := f.TeamspaceRole(ctx, "triage"); return err }},
		{"DeleteRoster", func() error { return f.DeleteRoster(ctx, "scout", "ops-a") }},
		{"RosterExport", func() error { _, err := f.RosterExport(ctx, "scout", "ops-a"); return err }},
		{"BotRoleExport", func() error { _, err := f.BotRoleExport(ctx, "scout", "triage", "bot", 3); return err }},
		{"TeamspaceRoleExport", func() error { _, err := f.TeamspaceRoleExport(ctx, "triage", "template", 2); return err }},
	}
	for _, s := range steps {
		if err := s.call(); err != nil {
			t.Errorf("%s: %v", s.name, err)
		}
	}
	if len(calls) != len(steps) {
		t.Fatalf("%d requests for %d calls: %+v", len(calls), len(steps), calls)
	}
	for i, c := range calls {
		if c.Body != "" {
			t.Errorf("%s: %s %s sent body %q", steps[i].name, c.Method, c.Path, c.Body)
		}
	}
}

// An export's query names version without scope, and scope without version.
func TestBotRoleExportQueryKeysAlone(t *testing.T) {
	var calls []councilCall
	f := councilServer(t, 200, "a: b\n", &calls)
	ctx := context.Background()
	if _, err := f.BotRoleExport(ctx, "scout", "triage", "", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := f.BotRoleExport(ctx, "scout", "triage", "bot", 0); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0].Query != "version=2" || calls[1].Query != "scope=bot" {
		t.Errorf("requests %+v", calls)
	}
}
