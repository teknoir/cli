package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/teknoir/cli/pkg/api"
)

// q is a JSON string.
func q(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// strs is a JSON list of strings; nil is the empty list the hub sends.
func strs(v []string) string {
	if v == nil {
		return "[]"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// councilErrBody is a council route's refusal: the hub's code and message, plus extra raw JSON
// members such as "document":1.
func councilErrBody(code, msg, extra string) string {
	m := q(msg)
	more := ""
	if extra != "" {
		more = "," + extra
	}
	return `{"code":` + q(code) + `,"message":` + m + more + `,"error":{"name":"CouncilError","message":` + m + `}}`
}

// roleViewJSON is one member as the hub's views carry it.
func roleViewJSON(slug, name, scope, kind, status string, groups, reasons []string) string {
	id := "rol_" + slug
	if scope == "template" {
		id = "tpl:" + slug
	}
	return `{"id":` + q(id) + `,"slug":` + q(slug) + `,"name":` + q(name) +
		`,"kind":` + q(kind) + `,"runtime":"inproc","model":"sonnet","modelId":"claude-sonnet-5-5",` +
		`"effort":"medium","description":` + q("Does "+slug+".") + `,"groups":` + strs(groups) +
		`,"effectiveTools":null,"level":"read","ceiling":"read","skills":[],"scope":` + q(scope) +
		`,"version":1,"category":"operations","templateRef":null,"status":` + q(status) +
		`,"reasons":` + strs(reasons) + `,"notes":[]}`
}

// roleAnswerJSON is an answer that carries one member.
func roleAnswerJSON(role string) string { return `{"role":` + role + `}` }

// catalogJSON is the teamspace's catalog: its shared members and the premade ones.
func catalogJSON(roles, templates []string) string {
	return `{"groups":{},"skills":[],"limits":{},"templates":{"roles":[` + strings.Join(templates, ",") +
		`],"rosters":[]},"roles":[` + strings.Join(roles, ",") + `]}`
}

// noTrailingSpaces fails when a line of out ends with a space.
func noTrailingSpaces(t *testing.T, out string) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if strings.HasSuffix(line, " ") {
			t.Errorf("a line ends with a space: %q", line)
		}
	}
}

// compactEq fails unless got and want are the same JSON after json.Compact.
func compactEq(t *testing.T, what, got, want string) {
	t.Helper()
	var g, w bytes.Buffer
	if err := json.Compact(&g, []byte(got)); err != nil {
		t.Fatalf("%s: %q is not JSON: %v", what, got, err)
	}
	if err := json.Compact(&w, []byte(want)); err != nil {
		t.Fatal(err)
	}
	if g.String() != w.String() {
		t.Errorf("%s:\n%s\nwant:\n%s", what, g.String(), w.String())
	}
}

// wantReqs fails unless the requests sent were exactly want, in order, each "METHOD path" or
// "METHOD path?query".
func wantReqs(t *testing.T, p *chatAPI, want ...string) {
	t.Helper()
	p.mu.Lock()
	reqs := append([]rec(nil), p.reqs...)
	p.mu.Unlock()
	var got []string
	for _, r := range reqs {
		s := r.Method + " " + r.Path
		if r.Query != "" {
			s += "?" + r.Query
		}
		got = append(got, s)
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestCouncilRoleListBot lists a bot's available members: the teamspace's and the bot's own, and with
// --templates the premade ones first.
func TestCouncilRoleListBot(t *testing.T) {
	noBotMode(t)
	available := []string{
		roleViewJSON("captain", "Captain", "template", "lead", "ok", []string{"core_read"}, nil),
		roleViewJSON("triage", "Triage", "template", "member", "ok", []string{"metrics", "inspect"}, nil),
		roleViewJSON("audit", "Auditor", "teamspace", "member", "invalid", nil, []string{"its model is gone", "its tools are gone"}),
		roleViewJSON("scribe", "Scribe", "bot", "member", "unchecked", nil, nil),
	}
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/council", 200, `{"council":{"available":[`+strings.Join(available, ",")+`]}}`)

	out, errs, err := councilArgs(t, p.env(t), "role", "list", "--bot", "scout")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	want := "SLUG    NAME     SCOPE      PART    MODEL   EFFORT  TOOLS  STATUS\n" +
		"audit   Auditor  teamspace  member  sonnet  medium  -      unavailable\n" +
		"scribe  Scribe   bot        member  sonnet  medium  -      not checked\n"
	if out != want {
		t.Errorf("stdout:\n%q\nwant:\n%q", out, want)
	}
	noTrailingSpaces(t, out)
	if wantErr := "· audit: its model is gone; its tools are gone\n"; errs != wantErr {
		t.Errorf("stderr %q, want %q", errs, wantErr)
	}
	got := p.seen("GET", "/bots/scout/council")
	if len(got) != 1 || got[0].Query != "" {
		t.Fatalf("requests: %+v", p.reqs)
	}

	out, errs, err = councilArgs(t, p.env(t), "role", "list", "--bot", "scout", "--templates")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	want = "SLUG     NAME     SCOPE      PART    MODEL   EFFORT  TOOLS            STATUS\n" +
		"captain  Captain  template   lead    sonnet  medium  core_read        ok\n" +
		"triage   Triage   template   member  sonnet  medium  metrics,inspect  ok\n" +
		"audit    Auditor  teamspace  member  sonnet  medium  -                unavailable\n" +
		"scribe   Scribe   bot        member  sonnet  medium  -                not checked\n"
	if out != want {
		t.Errorf("--templates stdout:\n%q\nwant:\n%q", out, want)
	}
	noTrailingSpaces(t, out)
	if p.countAll() != 2 {
		t.Errorf("%d requests, want 2", p.countAll())
	}
	for _, r := range p.seen("GET", "/bots/scout/council") {
		if r.Query != "" {
			t.Errorf("query %q", r.Query)
		}
	}
}

// TestCouncilRoleListTeamspace lists the teamspace's shared members, and with --templates the premade
// ones first.
func TestCouncilRoleListTeamspace(t *testing.T) {
	noBotMode(t)
	shared := []string{
		roleViewJSON("audit", "Auditor", "teamspace", "member", "ok", []string{"metrics"}, nil),
		roleViewJSON("ledger", "Ledger", "teamspace", "reviewer", "ok", nil, nil),
	}
	premade := []string{roleViewJSON("captain", "Captain", "template", "lead", "ok", []string{"core_read"}, nil)}
	p := newChatAPI(t)
	p.on("GET", "/council/catalog", 200, catalogJSON(shared, premade))

	out, errs, err := councilArgs(t, p.env(t), "role", "list")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("stdout %q: %d lines, want a header and two members", out, len(lines))
	}
	if !strings.HasPrefix(lines[1], "audit") || !strings.HasPrefix(lines[2], "ledger") {
		t.Errorf("stdout %q", out)
	}
	noTrailingSpaces(t, out)

	out, errs, err = councilArgs(t, p.env(t), "role", "list", "--templates")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	lines = strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("--templates stdout %q: %d lines, want a header and three members", out, len(lines))
	}
	if !strings.HasPrefix(lines[1], "captain") || !strings.HasPrefix(lines[2], "audit") || !strings.HasPrefix(lines[3], "ledger") {
		t.Errorf("--templates stdout %q", out)
	}
	noTrailingSpaces(t, out)
	got := p.seen("GET", "/council/catalog")
	if len(got) != 2 {
		t.Fatalf("requests: %+v", p.reqs)
	}
	for _, r := range got {
		if r.Query != "" {
			t.Errorf("query %q", r.Query)
		}
	}
}

// TestCouncilRoleListJSON prints the entries the hub sent, unchanged: a field tnctl does not know
// survives, and <, > and & are not escaped.
func TestCouncilRoleListJSON(t *testing.T) {
	noBotMode(t)
	teamspace := roleViewJSON("audit", "Auditor", "teamspace", "member", "ok", []string{"metrics"}, nil)
	teamspace = strings.Replace(teamspace, `"description":"Does audit."`, `"description":"Reads <pods> & logs."`, 1)
	bot := `{"future":1,` + strings.TrimPrefix(roleViewJSON("scribe", "Scribe", "bot", "member", "ok", nil, nil), "{")
	premade := roleViewJSON("captain", "Captain", "template", "lead", "ok", nil, nil)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/council", 200, `{"council":{"available":[`+
		strings.Join([]string{premade, teamspace, bot}, ",")+`]}}`)

	out, errs, err := councilArgs(t, p.env(t), "role", "list", "--bot", "scout", "--json")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	var got []json.RawMessage
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout %q is not a JSON array: %v", out, err)
	}
	if len(got) != 2 {
		t.Fatalf("%d entries, want the two non-template ones: %s", len(got), out)
	}
	for i, served := range []string{teamspace, bot} {
		var want bytes.Buffer
		if err := json.Compact(&want, []byte(served)); err != nil {
			t.Fatal(err)
		}
		var have bytes.Buffer
		if err := json.Compact(&have, got[i]); err != nil {
			t.Fatal(err)
		}
		if have.String() != want.String() {
			t.Errorf("entry %d:\n%s\nwant:\n%s", i, have.String(), want.String())
		}
	}
	if strings.Contains(out, `\u003c`) || !strings.Contains(out, "Reads <pods> & logs.") {
		t.Errorf("stdout %q", out)
	}
	if errs != "" {
		t.Errorf("stderr %q", errs)
	}
	wantReqs(t, p, "GET /bots/scout/council")
}

// TestCouncilRoleListEmpty says what there is to list and nothing on stdout.
func TestCouncilRoleListEmpty(t *testing.T) {
	noBotMode(t)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/council", 200, `{"council":{"available":[]}}`)

	out, errs, err := councilArgs(t, p.env(t), "role", "list", "--bot", "scout")
	if err != nil {
		t.Fatalf("%v", err)
	}
	if out != "" {
		t.Errorf("stdout %q", out)
	}
	if want := "· no members for scout yet (its own or the teamspace's); --templates lists the premade ones; the bot's creator or a teamspace admin makes one with tnctl council role new <name> --job \"…\" --bot scout\n"; errs != want {
		t.Errorf("stderr %q, want %q", errs, want)
	}

	out, errs, err = councilArgs(t, p.env(t), "role", "list", "--bot", "scout", "--templates")
	if err != nil {
		t.Fatalf("%v", err)
	}
	if out != "" {
		t.Errorf("--templates stdout %q", out)
	}
	if want := "· no members\n"; errs != want {
		t.Errorf("--templates stderr %q, want %q", errs, want)
	}

	out, errs, err = councilArgs(t, p.env(t), "role", "list", "--bot", "scout", "--json")
	if err != nil {
		t.Fatalf("%v", err)
	}
	if want := "[]\n"; out != want {
		t.Errorf("--json stdout %q, want %q", out, want)
	}
	if errs != "" {
		t.Errorf("--json stderr %q", errs)
	}
	wantReqs(t, p, "GET /bots/scout/council", "GET /bots/scout/council", "GET /bots/scout/council")
}

// TestCouncilRoleListNameCut keeps a table row one line: a long name is cut at 30 characters.
func TestCouncilRoleListNameCut(t *testing.T) {
	noBotMode(t)
	long := strings.Repeat("ä", 25) + "bcdefghijklmnop" // 40 runes
	exact := strings.Repeat("ö", 30)
	p := newChatAPI(t)
	p.on("GET", "/council/catalog", 200, catalogJSON([]string{
		roleViewJSON("audit", long, "teamspace", "member", "ok", nil, nil),
		roleViewJSON("ledger", exact, "teamspace", "member", "ok", nil, nil),
	}, nil))

	out, errs, err := councilArgs(t, p.env(t), "role", "list")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	runes := []rune(long)
	if want := string(runes[:29]) + "…"; !strings.Contains(out, want) {
		t.Errorf("stdout %q does not hold the cut name %q", out, want)
	}
	if strings.Contains(out, long) {
		t.Errorf("stdout %q holds the whole long name", out)
	}
	if !strings.Contains(out, exact) {
		t.Errorf("stdout %q does not hold the 30-rune name whole", out)
	}
	noTrailingSpaces(t, out)
	wantReqs(t, p, "GET /council/catalog")
}

// TestCouncilRoleGetRoutes sends each read to the route the routes table names.
func TestCouncilRoleGetRoutes(t *testing.T) {
	noBotMode(t)
	triage := roleViewJSON("triage", "Triage", "teamspace", "member", "ok", []string{"metrics"}, nil)
	captain := roleViewJSON("captain", "Captain", "template", "lead", "ok", []string{"core_read"}, nil)
	const yamlRole = "apiVersion: council.teknoir.org/v1alpha1\nkind: CouncilRole\n"
	cases := []struct {
		name  string
		args  []string
		path  string
		query string
		body  string
	}{
		{"bot table", []string{"get", "triage", "--bot", "scout"}, "/bots/scout/council/roles/triage", "", roleAnswerJSON(triage)},
		{"bot yaml", []string{"get", "triage", "--bot", "scout", "-o", "yaml"}, "/bots/scout/council/roles/triage/export", "", yamlRole},
		{"bot scope", []string{"get", "bot/triage", "--bot", "scout"}, "/bots/scout/council/roles/triage", "scope=bot", roleAnswerJSON(triage)},
		{"teamspace table", []string{"get", "triage"}, "/council/roles/triage", "", roleAnswerJSON(triage)},
		{"teamspace scope", []string{"get", "teamspace/triage"}, "/council/roles/triage", "", roleAnswerJSON(triage)},
		{"teamspace yaml", []string{"get", "triage", "-o", "yaml"}, "/council/roles/triage/export", "", yamlRole},
		{"version", []string{"export", "triage", "--version", "2"}, "/council/roles/triage/export", "version=2", yamlRole},
		{"template bare slug", []string{"get", "captain", "--template"}, "/council/catalog", "", catalogJSON(nil, []string{captain})},
		{"template scope", []string{"get", "template/captain"}, "/council/catalog", "", catalogJSON(nil, []string{captain})},
		{"template yaml", []string{"get", "captain", "--template", "-o", "yaml"}, "/council/roles/captain/export", "scope=template", yamlRole},
		{"bot export", []string{"export", "triage", "--bot", "scout"}, "/bots/scout/council/roles/triage/export", "", yamlRole},
		{"template export", []string{"export", "template/captain"}, "/council/roles/captain/export", "scope=template", yamlRole},
		{"bot export version", []string{"export", "triage", "--bot", "scout", "--version", "2"}, "/bots/scout/council/roles/triage/export", "version=2", yamlRole},
		{"bot export scope", []string{"export", "bot/triage", "--bot", "scout", "--version", "2"}, "/bots/scout/council/roles/triage/export", "scope=bot&version=2", yamlRole},
		{"template export version", []string{"export", "template/captain", "--version", "3"}, "/council/roles/captain/export", "scope=template&version=3", yamlRole},
		{"plain export", []string{"export", "triage"}, "/council/roles/triage/export", "", yamlRole},
		{"teamspace export", []string{"export", "teamspace/triage"}, "/council/roles/triage/export", "", yamlRole},
		{"bot json", []string{"get", "triage", "--bot", "scout", "-o", "json"}, "/bots/scout/council/roles/triage", "", roleAnswerJSON(triage)},
		{"bot scope json", []string{"get", "bot/triage", "--bot", "scout", "-o", "json"}, "/bots/scout/council/roles/triage", "scope=bot", roleAnswerJSON(triage)},
		{"teamspace json", []string{"get", "triage", "-o", "json"}, "/council/roles/triage", "", roleAnswerJSON(triage)},
		{"template json", []string{"get", "captain", "--template", "-o", "json"}, "/council/catalog", "", catalogJSON(nil, []string{captain})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newChatAPI(t)
			p.on("GET", c.path, 200, c.body)
			_, errs, err := councilArgs(t, p.env(t), append([]string{"role"}, c.args...)...)
			if err != nil {
				t.Fatalf("%v\n%s", err, errs)
			}
			if p.countAll() != 1 {
				t.Fatalf("requests: %+v", p.reqs)
			}
			r := p.reqs[0]
			if r.Method != "GET" || r.Path != c.path || r.Query != c.query {
				t.Errorf("request %s %s?%s, want GET %s?%s", r.Method, r.Path, r.Query, c.path, c.query)
			}
		})
	}
}

// TestCouncilRoleGetUsageErrors refuses a command line before anything is sent.
func TestCouncilRoleGetUsageErrors(t *testing.T) {
	noBotMode(t)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"get", "Bad"}, `"Bad" is not a member: use a slug such as triage, or <scope>/<slug> with scope bot, teamspace or template`},
		{[]string{"get", "x/y/z"}, `"x/y/z" is not a member: use a slug such as triage, or <scope>/<slug> with scope bot, teamspace or template`},
		{[]string{"get", "robot/triage"}, `"robot/triage" is not a member: use a slug such as triage, or <scope>/<slug> with scope bot, teamspace or template`},
		{[]string{"get", ".."}, `".." is not a member: use a slug such as triage, or <scope>/<slug> with scope bot, teamspace or template`},
		{[]string{"get", "bad\nref"}, `"bad\nref" is not a member: use a slug such as triage, or <scope>/<slug> with scope bot, teamspace or template`},
		{[]string{"get", "bot/triage", "--template"}, `--template and "bot/triage" name different scopes`},
		{[]string{"get", "bot/triage"}, "a bot's own member needs --bot <bot>"},
		{[]string{"get", "bot/triage", "--bot", ""}, `"" is not a bot name: use the bot's name, such as scout`},
		{[]string{"export", "bot/triage", "--bot", ""}, `"" is not a bot name: use the bot's name, such as scout`},
		{[]string{"export", "triage", "--version", "0"}, "--version must be 1 or more"},
		{[]string{"get", "triage", "--version", "2"}, "--version needs -o yaml (only an export can show an older version)"},
		{[]string{"get", "triage", "--bot", ".."}, `".." is not a bot name: use the bot's name, such as scout`},
		{[]string{"get", "triage", "--bot", "Scout"}, `"Scout" is not a bot name: use the bot's name, such as scout`},
		{[]string{"get", "triage", "--bot", ""}, `"" is not a bot name: use the bot's name, such as scout`},
		{[]string{"export", "triage", "--bot", ""}, `"" is not a bot name: use the bot's name, such as scout`},
		{[]string{"export", "triage", "-o", "json"}, "council role export prints YAML; use council role get -o json"},
		{[]string{"list", "--bot", ".."}, `".." is not a bot name: use the bot's name, such as scout`},
		{[]string{"list", "--bot", ""}, `"" is not a bot name: use the bot's name, such as scout`},
		{[]string{"list", "scout"}, "council role list takes no arguments; name a bot with --bot, for example: tnctl council role list --bot scout"},
		{[]string{"get"}, "council role get needs a member, for example: tnctl council role get triage --bot scout"},
		{[]string{"export"}, "council role export needs a member, for example: tnctl council role export triage --bot scout"},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			p := newChatAPI(t)
			_, _, err := councilArgs(t, p.env(t), append([]string{"role"}, c.args...)...)
			if err == nil {
				t.Fatalf("%v was accepted", c.args)
			}
			if code := exitCodeOf(err); code != codeUsage {
				t.Errorf("exit %d, want %d: %v", code, codeUsage, err)
			}
			// argsRange adds "\nusage: <use line>" to the two missing-argument errors.
			if got := strings.SplitN(err.Error(), "\nusage: ", 2)[0]; got != c.want {
				t.Errorf("error %q, want %q", err, c.want)
			}
			if n := p.countAll(); n != 0 {
				t.Errorf("%d requests were sent", n)
			}
		})
	}
}

// TestCouncilRoleGetFallsBackToTemplate reads an unqualified slug as the teamspace's member, and when
// there is none as the premade one.
func TestCouncilRoleGetFallsBackToTemplate(t *testing.T) {
	noBotMode(t)
	captain := roleViewJSON("captain", "Captain", "template", "lead", "ok", []string{"core_read"}, nil)
	const noShared = "There is no shared member 'captain' in this teamspace."
	const yamlRole = "apiVersion: council.teknoir.org/v1alpha1\nkind: CouncilRole\n"

	t.Run("table", func(t *testing.T) {
		captainDetail := captain[:len(captain)-1] + `,"instructions":"Lead the turn.","persona":null,"budget":null,"versions":[],"createdBy":null,"updatedBy":null,"updatedAt":null,"archivedAt":null,"archivedBy":null}`
		p := newChatAPI(t)
		p.on("GET", "/council/roles/captain", 404, councilErrBody("not_found", noShared, ""))
		p.on("GET", "/council/catalog", 200, catalogJSON(nil, []string{captainDetail}))
		out, errs, err := councilArgs(t, p.env(t), "role", "get", "captain")
		if err != nil {
			t.Fatalf("%v\n%s", err, errs)
		}
		if !strings.Contains(out, "scope:       template") {
			t.Errorf("stdout %q does not show the template scope", out)
		}
		if !strings.Contains(out, "effective:   not known without a bot\n") {
			t.Errorf("stdout %q does not say the effective tools are unknown", out)
		}
		if !strings.Contains(out, "access:      at most read (the bot's access decides)\n") {
			t.Errorf("stdout %q does not give the ceiling as the most access", out)
		}
		if !strings.Contains(out, "instructions:\n  Lead the turn.\n") {
			t.Errorf("stdout %q does not show the instructions", out)
		}
		for _, key := range []string{"updated:", "archived:", "used by:"} {
			if strings.Contains(out, key) {
				t.Errorf("stdout shows %q: %q", key, out)
			}
		}
		wantReqs(t, p, "GET /council/roles/captain", "GET /council/catalog")
	})

	t.Run("yaml", func(t *testing.T) {
		p := newChatAPI(t)
		p.onFunc("GET", "/council/roles/captain/export", func(r rec) (int, string) {
			if r.Query == "scope=template" {
				return 200, yamlRole
			}
			return 404, councilErrBody("not_found", noShared, "")
		})
		out, errs, err := councilArgs(t, p.env(t), "role", "get", "captain", "-o", "yaml")
		if err != nil {
			t.Fatalf("%v\n%s", err, errs)
		}
		if out != yamlRole {
			t.Errorf("stdout %q, want %q", out, yamlRole)
		}
		wantReqs(t, p, "GET /council/roles/captain/export", "GET /council/roles/captain/export?scope=template")
	})

	const noMember = `there is no shared or premade member "captain" in this teamspace; a bot's own member needs --bot <bot>`
	t.Run("both missing, table", func(t *testing.T) {
		p := newChatAPI(t)
		p.on("GET", "/council/roles/captain", 404, councilErrBody("not_found", noShared, ""))
		p.on("GET", "/council/catalog", 200, catalogJSON(nil, nil))
		_, _, err := councilArgs(t, p.env(t), "role", "get", "captain")
		if err == nil || exitCodeOf(err) != codeNotFound || err.Error() != noMember {
			t.Errorf("exit %d: %v", exitCodeOf(err), err)
		}
		wantReqs(t, p, "GET /council/roles/captain", "GET /council/catalog")
	})
	t.Run("both missing, yaml", func(t *testing.T) {
		p := newChatAPI(t)
		p.on("GET", "/council/roles/captain/export", 404, councilErrBody("not_found", noShared, ""))
		_, _, err := councilArgs(t, p.env(t), "role", "get", "captain", "-o", "yaml")
		if err == nil || exitCodeOf(err) != codeNotFound || err.Error() != noMember {
			t.Errorf("exit %d: %v", exitCodeOf(err), err)
		}
		wantReqs(t, p, "GET /council/roles/captain/export", "GET /council/roles/captain/export?scope=template")
	})

	t.Run("version", func(t *testing.T) {
		p := newChatAPI(t)
		p.onFunc("GET", "/council/roles/triage/export", func(r rec) (int, string) {
			if r.Query == "version=2" {
				return 404, councilErrBody("not_found", "'triage' has no version 2.", "")
			}
			return 404, councilErrBody("not_found", "There is no premade member 'triage'.", "")
		})
		_, _, err := councilArgs(t, p.env(t), "role", "export", "triage", "--version", "2")
		if want := "'triage' has no version 2.\nThere is no premade member 'triage'."; err == nil || err.Error() != want {
			t.Fatalf("%v, want %q", err, want)
		}
		if exitCodeOf(err) != codeNotFound {
			t.Errorf("exit %d, want %d", exitCodeOf(err), codeNotFound)
		}
		wantReqs(t, p, "GET /council/roles/triage/export?version=2", "GET /council/roles/triage/export?scope=template&version=2")
	})

	// Two not_found answers without words still say something, and an empty one adds no empty line.
	t.Run("version, no words", func(t *testing.T) {
		for _, c := range []struct {
			name, templateBody, want string
		}{
			{"none", `{"code":"not_found"}`, "the platform refused the request (not_found)"},
			{"one", councilErrBody("not_found", "There is no premade member 'triage'.", ""), "There is no premade member 'triage'."},
		} {
			p := newChatAPI(t)
			p.onFunc("GET", "/council/roles/triage/export", func(r rec) (int, string) {
				if r.Query == "version=2" {
					return 404, `{"code":"not_found"}`
				}
				return 404, c.templateBody
			})
			_, _, err := councilArgs(t, p.env(t), "role", "export", "triage", "--version", "2")
			if err == nil || exitCodeOf(err) != codeNotFound || err.Error() != c.want {
				t.Errorf("%s: exit %d %v, want exit 3 %q", c.name, exitCodeOf(err), err, c.want)
			}
			wantReqs(t, p, "GET /council/roles/triage/export?version=2", "GET /council/roles/triage/export?scope=template&version=2")
		}
	})

	t.Run("no fallback for teamspace", func(t *testing.T) {
		p := newChatAPI(t)
		p.on("GET", "/council/roles/captain", 404, councilErrBody("not_found", noShared, ""))
		_, _, err := councilArgs(t, p.env(t), "role", "get", "teamspace/captain")
		if err == nil || exitCodeOf(err) != codeNotFound || err.Error() != noShared {
			t.Errorf("exit %d: %v", exitCodeOf(err), err)
		}
		wantReqs(t, p, "GET /council/roles/captain")
	})
	t.Run("no fallback for another code", func(t *testing.T) {
		p := newChatAPI(t)
		const off = "The council is turned off on this platform."
		p.on("GET", "/council/roles/captain", 404, councilErrBody("council_off", off, ""))
		_, _, err := councilArgs(t, p.env(t), "role", "get", "captain")
		if err == nil || exitCodeOf(err) != codeError || err.Error() != off {
			t.Errorf("exit %d: %v", exitCodeOf(err), err)
		}
		wantReqs(t, p, "GET /council/roles/captain")
	})
	t.Run("no fallback with a bot", func(t *testing.T) {
		p := newChatAPI(t)
		const noRole = "There is no member 'captain' for scout."
		p.on("GET", "/bots/scout/council/roles/captain", 404, councilErrBody("not_found", noRole, ""))
		_, _, err := councilArgs(t, p.env(t), "role", "get", "captain", "--bot", "scout")
		if err == nil || exitCodeOf(err) != codeNotFound || err.Error() != noRole {
			t.Errorf("exit %d: %v", exitCodeOf(err), err)
		}
		wantReqs(t, p, "GET /bots/scout/council/roles/captain")
	})

	t.Run("premade missing", func(t *testing.T) {
		triage := roleViewJSON("triage", "Triage", "teamspace", "member", "ok", nil, nil)
		for _, c := range []struct {
			args []string
			want string
		}{
			{[]string{"get", "template/nosuch"}, "There is no premade member 'nosuch'."},
			{[]string{"get", "nosuch", "--template"}, "There is no premade member 'nosuch'."},
			{[]string{"get", "template/nosuch", "-o", "json"}, "There is no premade member 'nosuch'."},
			// triage is a shared member, not a premade one: the shared list is not read for template/.
			{[]string{"get", "template/triage"}, "There is no premade member 'triage'."},
		} {
			p := newChatAPI(t)
			p.on("GET", "/council/catalog", 200, catalogJSON([]string{triage}, nil))
			_, _, err := councilArgs(t, p.env(t), append([]string{"role"}, c.args...)...)
			if err == nil || exitCodeOf(err) != codeNotFound || err.Error() != c.want {
				t.Errorf("%v: exit %d %v, want exit 3 %q", c.args, exitCodeOf(err), err, c.want)
			}
			wantReqs(t, p, "GET /council/catalog")
		}
	})
}

// TestCouncilRoleExportVerbatim writes the hub's bytes as they came: no decoding, no added newline.
func TestCouncilRoleExportVerbatim(t *testing.T) {
	noBotMode(t)
	body := "# a comment\r\nkind: CouncilRole\r\nspec:\r\n  name: triage \r\nlast: no newline"
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/council/roles/triage/export", 200, body)
	for _, args := range [][]string{
		{"export", "triage", "--bot", "scout"},
		{"get", "triage", "--bot", "scout", "-o", "yaml"},
	} {
		out, errs, err := councilArgs(t, p.env(t), append([]string{"role"}, args...)...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, errs)
		}
		if out != body {
			t.Errorf("%v: stdout %q, want %q", args, out, body)
		}
	}
	wantReqs(t, p, "GET /bots/scout/council/roles/triage/export", "GET /bots/scout/council/roles/triage/export")
}

// TestCouncilRoleGetTable prints a member's detail, and only the detail's own lines.
func TestCouncilRoleGetTable(t *testing.T) {
	noBotMode(t)
	view := roleViewJSON("triage", "Triage", "teamspace", "member", "ok", []string{"metrics", "inspect"}, nil)
	view = strings.Replace(view, `"version":1`, `"version":2`, 1)
	view = strings.Replace(view, `"skills":[]`, `"skills":["deploy"]`, 1)
	view = strings.Replace(view, `"notes":[]`, `"notes":["Effort lowered to low by the platform","Pods and logs need Act as me"]`, 1)
	detail := view[:len(view)-1] +
		`,"instructions":"first line\n\nsecond \u001b[31mline\u001b[0m here",` +
		`"persona":null,"budget":{"maxToolRounds":4,"maxTokens":1000},` +
		`"versions":[{"version":2,"createdBy":"ada","createdAt":1763213400000}],` +
		`"createdBy":"ada","updatedBy":"ada","updatedAt":1763213400000,` +
		`"archivedAt":1763213400000,"archivedBy":"ada",` +
		`"usedBy":[{"bot":"scout","roster":"ops","displayName":"Ops"},` +
		`{"bot":"arm-2","roster":"review","displayName":"Review"}],"hiddenCount":1}`

	p := newChatAPI(t)
	p.on("GET", "/council/roles/triage", 200, roleAnswerJSON(detail))
	out, errs, err := councilArgs(t, p.env(t), "role", "get", "triage")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	want := "slug:        triage\n" +
		"name:        Triage\n" +
		"scope:       teamspace\n" +
		"part:        member\n" +
		"model:       sonnet (claude-sonnet-5-5), effort medium\n" +
		"tools:       metrics, inspect\n" +
		"effective:   not known without a bot\n" +
		"access:      at most read (the bot's access decides)\n" +
		"skills:      deploy\n" +
		"version:     2\n" +
		"status:      ok\n" +
		"notes:       Effort lowered to low by the platform; Pods and logs need Act as me\n" +
		"description: Does triage.\n" +
		"updated:     2025-11-15 13:30 UTC by ada\n" +
		"archived:    2025-11-15 13:30 UTC by ada\n" +
		"used by:     scout (Ops), arm-2 (Review) and 1 more you cannot see\n" +
		"instructions:\n" +
		"  first line\n" +
		"\n" +
		"  second line here\n"
	if out != want {
		t.Errorf("stdout:\n%q\nwant:\n%q", out, want)
	}
	wantReqs(t, p, "GET /council/roles/triage")

	// A view, not a detail: the hub sent no versions, so the person may read none of those lines.
	p = newChatAPI(t)
	view = roleViewJSON("audit", "Auditor", "bot", "member", "ok", nil, nil)
	view = view[:len(view)-1] +
		`,"instructions":"do the audit","updatedAt":1763213400000,"archivedAt":1763213400000,` +
		`"usedBy":[{"bot":"scout","roster":"ops","displayName":"Ops"}],"hiddenCount":1}`
	p.on("GET", "/bots/scout/council/roles/audit", 200, roleAnswerJSON(view))
	out, errs, err = councilArgs(t, p.env(t), "role", "get", "audit", "--bot", "scout")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	for _, key := range []string{"updated:", "archived:", "used by:", "instructions:"} {
		if strings.Contains(out, key) {
			t.Errorf("a view shows %q: %q", key, out)
		}
	}
	if !strings.Contains(out, "effective:   not checked yet\n") {
		t.Errorf("a bot's member without caps: %q", out)
	}
	if !strings.Contains(out, "access:      read (ceiling read)\n") {
		t.Errorf("a bot's member: %q", out)
	}
	if strings.Contains(out, "notes:") {
		t.Errorf("empty notes print a line: %q", out)
	}
	wantReqs(t, p, "GET /bots/scout/council/roles/audit")
}

// TestCouncilRoleGetJSON prints the role the hub sent, not the answer that carries it.
func TestCouncilRoleGetJSON(t *testing.T) {
	noBotMode(t)
	view := roleViewJSON("triage", "Triage", "teamspace", "member", "ok", []string{"metrics"}, nil)
	view = `{"future":1,` + strings.TrimPrefix(view, "{")
	p := newChatAPI(t)
	p.on("GET", "/council/roles/triage", 200, roleAnswerJSON(view))
	out, errs, err := councilArgs(t, p.env(t), "role", "get", "triage", "-o", "json")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	var got, want bytes.Buffer
	if err := json.Compact(&got, []byte(out)); err != nil {
		t.Fatalf("stdout %q is not JSON: %v", out, err)
	}
	if err := json.Compact(&want, []byte(view)); err != nil {
		t.Fatal(err)
	}
	if got.String() != want.String() {
		t.Errorf("stdout:\n%s\nwant:\n%s", got.String(), want.String())
	}
	if strings.Contains(out, `"role"`) {
		t.Errorf("stdout holds the answer's wrapper: %q", out)
	}
	wantReqs(t, p, "GET /council/roles/triage")
}

// TestCouncilErrorMapping turns each hub council code into tnctl's own exit code and the hub's words.
func TestCouncilErrorMapping(t *testing.T) {
	noBotMode(t)
	p := newChatAPI(t)
	e := p.env(t)
	fl, _, _, err := e.fleet()
	if err != nil {
		t.Fatal(err)
	}
	// read makes a real request, so CouncilErrorOf sees a real *api.Error.
	read := func(t *testing.T, status int, body string) error {
		t.Helper()
		p.on("GET", "/council/catalog", status, body)
		_, _, err := fl.CouncilCatalog(context.Background())
		if err == nil {
			t.Fatal("the platform answered success")
		}
		return councilError(err)
	}

	t.Run("exit codes", func(t *testing.T) {
		for code, want := range map[string]int{
			"unknown_member": codeUsage, "unknown_roster": codeUsage, "ambiguous": codeUsage,
			"council_outdated": codeError, "role_invalid": codeError, "roster_gone": codeError,
			"role_gone": codeError, "pin_empty": codeError, "creator_only": codeError, "council_off": codeError,
			"conflict": codeConflict, "exists": codeConflict, "turn_open": codeConflict,
			"invalid": codeRejected, "bad_request": codeRejected, "template_read_only": codeRejected,
			"limit": codeRejected, "role_in_use": codeRejected, "roster_default": codeRejected,
			"too_large": codeRejected,
			"not_admin": codeAuth, "council_edit_not_allowed": codeAuth, "chat_not_allowed": codeAuth,
			"not_author": codeAuth, "bot_caller": codeAuth,
			"not_found": codeNotFound,
		} {
			got := read(t, 400, councilErrBody(code, "the hub's own words", ""))
			if exitCodeOf(got) != want {
				t.Errorf("%s: exit %d, want %d", code, exitCodeOf(got), want)
			}
			if got.Error() != "the hub's own words" {
				t.Errorf("%s: text %q", code, got.Error())
			}
		}
	})

	t.Run("403 without the client's own words", func(t *testing.T) {
		got := read(t, 403, councilErrBody("council_edit_not_allowed", "Only a teamspace admin may edit this member.", ""))
		text := got.Error()
		if text != "Only a teamspace admin may edit this member." {
			t.Errorf("text %q", text)
		}
		if strings.Contains(text, "not allowed for this account or teamspace") || strings.Contains(text, "(HTTP") {
			t.Errorf("text %q carries the client's own words", text)
		}
	})

	t.Run("fields and a document", func(t *testing.T) {
		got := read(t, 400, councilErrBody("invalid", "the top message is the fields'",
			`"document":1,"fields":[{"path":"spec.lead","message":"m1"},{"path":"","message":"m2"}]`))
		if want := "document 2: spec.lead: m1\nm2"; got.Error() != want {
			t.Errorf("text %q, want %q", got.Error(), want)
		}
		got = read(t, 400, councilErrBody("invalid", "the top message is the field's",
			`"fields":[{"path":"yaml","message":"it is a list, not an object"}]`))
		if want := "it is a list, not an object"; got.Error() != want {
			t.Errorf("text %q, want %q", got.Error(), want)
		}
	})

	t.Run("role in use", func(t *testing.T) {
		got := read(t, 400, councilErrBody("role_in_use", "The member is in use.",
			`"usedBy":[{"bot":"scout","roster":"ops","displayName":"Ops"},{"bot":"arm-2","roster":"review","displayName":"Review"}],"hiddenCount":2`))
		want := "The member is in use.\nused by scout (Ops), arm-2 (Review) and 2 more you cannot see"
		if got.Error() != want {
			t.Errorf("text %q, want %q", got.Error(), want)
		}
	})

	t.Run("escapes", func(t *testing.T) {
		// The hub's message carries ESC before [31m and before [0m: the six-character JSON escape
		// writes it, so the served text is what a bot's answer would carry.
		got := read(t, 400, councilErrBody("bad_request", "bad \u001b[31mred\u001b[0m thing", ""))
		text := got.Error()
		if strings.ContainsRune(text, 0x1b) {
			t.Errorf("text %q holds an ESC byte", text)
		}
		if !strings.Contains(text, "red") {
			t.Errorf("text %q lost the word", text)
		}
	})

	t.Run("codes the client does not know", func(t *testing.T) {
		for _, c := range []struct {
			status int
			body   string
		}{
			{409, councilErrBody("bot_not_ready", "The bot is not ready.", "")},
			{404, `{"message":"gone","error":{"name":"NotFoundError","message":"gone"}}`},
		} {
			p.on("GET", "/council/catalog", c.status, c.body)
			_, _, reqErr := fl.CouncilCatalog(context.Background())
			if reqErr == nil {
				t.Fatal("the platform answered success")
			}
			got := councilError(reqErr)
			want := classify(reqErr)
			if exitCodeOf(got) != exitCodeOf(want) || got.Error() != want.Error() {
				t.Errorf("%d %s: exit %d %q, want exit %d %q", c.status, c.body,
					exitCodeOf(got), got.Error(), exitCodeOf(want), want.Error())
			}
		}
	})

	t.Run("no words", func(t *testing.T) {
		for body, want := range map[string]string{
			`{"code":"invalid"}`:              "the platform refused the request (invalid)",
			`{"code":"invalid","document":0}`: "document 1: the platform refused the request (invalid)",
			`{"code":"invalid","message":"","fields":[{"path":"spec.lead","message":""}]}`:            "the platform refused the request (invalid)",
			`{"code":"invalid","message":"Top words.","fields":[{"path":"spec.lead","message":" "}]}`: "Top words.",
		} {
			got := read(t, 400, body)
			if got.Error() != want {
				t.Errorf("%s: text %q, want %q", body, got.Error(), want)
			}
			if exitCodeOf(got) != codeRejected {
				t.Errorf("%s: exit %d, want %d", body, exitCodeOf(got), codeRejected)
			}
		}
	})
}

// TestCouncilRoleGetFallbackEdges covers the yaml twins of the no-fallback cases, the second call's own
// error, a missing premade member, and the JSON of a premade member.
func TestCouncilRoleGetFallbackEdges(t *testing.T) {
	noBotMode(t)
	const noShared = "There is no shared member 'captain' in this teamspace."
	const off = "The council is turned off on this platform."
	const noBotRole = "There is no member 'captain' for scout."
	const yamlRole = "apiVersion: council.teknoir.org/v1alpha1\nkind: CouncilRole\n"
	captain := `{"future":2,` + strings.TrimPrefix(roleViewJSON("captain", "Captain", "template", "lead", "ok", []string{"core_read"}, nil), "{")
	triage := roleViewJSON("triage", "Triage", "template", "member", "ok", []string{"metrics"}, nil)

	type step struct {
		name   string
		setup  func(p *chatAPI)
		args   []string
		exit   int
		text   string
		reqs   []string
		stdout string
	}
	for _, c := range []step{
		{"teamspace scope yaml: no fallback", func(p *chatAPI) {
			p.onFunc("GET", "/council/roles/captain/export", func(r rec) (int, string) {
				if r.Query == "scope=template" {
					return 200, yamlRole
				}
				return 404, councilErrBody("not_found", noShared, "")
			})
		}, []string{"get", "teamspace/captain", "-o", "yaml"}, codeNotFound, noShared,
			[]string{"GET /council/roles/captain/export"}, ""},
		{"another code yaml: no fallback", func(p *chatAPI) {
			p.onFunc("GET", "/council/roles/captain/export", func(r rec) (int, string) {
				if r.Query == "scope=template" {
					return 200, yamlRole
				}
				return 404, councilErrBody("council_off", off, "")
			})
		}, []string{"get", "captain", "-o", "yaml"}, codeError, off,
			[]string{"GET /council/roles/captain/export"}, ""},
		{"bot yaml: no fallback", func(p *chatAPI) {
			p.on("GET", "/bots/scout/council/roles/captain/export", 404, councilErrBody("not_found", noBotRole, ""))
			p.on("GET", "/council/roles/captain/export", 200, yamlRole)
		}, []string{"get", "captain", "--bot", "scout", "-o", "yaml"}, codeNotFound, noBotRole,
			[]string{"GET /bots/scout/council/roles/captain/export"}, ""},
		{"second export fails with another code", func(p *chatAPI) {
			p.onFunc("GET", "/council/roles/captain/export", func(r rec) (int, string) {
				if r.Query == "scope=template" {
					return 400, councilErrBody("bad_request", "Bad scope.", "")
				}
				return 404, councilErrBody("not_found", noShared, "")
			})
		}, []string{"get", "captain", "-o", "yaml"}, codeRejected, "Bad scope.",
			[]string{"GET /council/roles/captain/export", "GET /council/roles/captain/export?scope=template"}, ""},
		{"catalog fails after the fallback", func(p *chatAPI) {
			p.on("GET", "/council/roles/captain", 404, councilErrBody("not_found", noShared, ""))
			p.on("GET", "/council/catalog", 404, councilErrBody("council_off", off, ""))
		}, []string{"get", "captain"}, codeError, off,
			[]string{"GET /council/roles/captain", "GET /council/catalog"}, ""},
		{"premade member missing", func(p *chatAPI) {
			p.on("GET", "/council/catalog", 200, catalogJSON(nil, []string{triage}))
		}, []string{"get", "template/captain"}, codeNotFound, "There is no premade member 'captain'.",
			[]string{"GET /council/catalog"}, ""},
		{"fallback json", func(p *chatAPI) {
			p.on("GET", "/council/roles/captain", 404, councilErrBody("not_found", noShared, ""))
			p.on("GET", "/council/catalog", 200, catalogJSON(nil, []string{triage, captain}))
		}, []string{"get", "captain", "-o", "json"}, 0, "",
			[]string{"GET /council/roles/captain", "GET /council/catalog"}, captain},
		{"premade json at its index", func(p *chatAPI) {
			p.on("GET", "/council/catalog", 200, catalogJSON(nil, []string{triage, captain}))
		}, []string{"get", "captain", "--template", "-o", "json"}, 0, "",
			[]string{"GET /council/catalog"}, captain},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newChatAPI(t)
			c.setup(p)
			out, _, err := councilArgs(t, p.env(t), append([]string{"role"}, c.args...)...)
			if c.exit == 0 {
				if err != nil {
					t.Fatal(err)
				}
				compactEq(t, "stdout", out, c.stdout)
			} else if err == nil || exitCodeOf(err) != c.exit || err.Error() != c.text {
				t.Errorf("exit %d %v, want exit %d %q", exitCodeOf(err), err, c.exit, c.text)
			}
			wantReqs(t, p, c.reqs...)
		})
	}
}

// TestCouncilRoleListCatalogJSON: without --bot, --json prints the catalog's entries exactly as served
// (with --templates the premade ones first), and nothing on stderr, not even for an unavailable member.
func TestCouncilRoleListCatalogJSON(t *testing.T) {
	noBotMode(t)
	premade := `{"future":"t",` + strings.TrimPrefix(roleViewJSON("captain", "Captain", "template", "lead", "ok", nil, nil), "{")
	audit := roleViewJSON("audit", "Auditor", "teamspace", "member", "invalid", []string{"metrics"}, []string{"its model is gone"})
	ledger := `{"future":"l",` + strings.TrimPrefix(roleViewJSON("ledger", "Ledger", "teamspace", "reviewer", "ok", nil, nil), "{")
	p := newChatAPI(t)
	p.on("GET", "/council/catalog", 200, catalogJSON([]string{audit, ledger}, []string{premade}))
	for _, c := range []struct {
		args []string
		want []string
	}{
		{[]string{"role", "list", "--json"}, []string{audit, ledger}},
		{[]string{"role", "list", "--templates", "--json"}, []string{premade, audit, ledger}},
	} {
		out, errs, err := councilArgs(t, p.env(t), c.args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", c.args, err, errs)
		}
		compactEq(t, strings.Join(c.args, " "), out, "["+strings.Join(c.want, ",")+"]")
		if errs != "" {
			t.Errorf("%v: stderr %q", c.args, errs)
		}
	}
	wantReqs(t, p, "GET /council/catalog", "GET /council/catalog")
}

// TestCouncilRoleListEmptyTeamspace: without --bot, an empty catalog says where members come from.
func TestCouncilRoleListEmptyTeamspace(t *testing.T) {
	noBotMode(t)
	p := newChatAPI(t)
	p.on("GET", "/council/catalog", 200, catalogJSON(nil, nil))
	out, errs, err := councilArgs(t, p.env(t), "role", "list")
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Errorf("stdout %q", out)
	}
	if want := "· no shared members in this teamspace yet; --templates lists the premade ones, --bot <bot> a bot's own; a teamspace admin makes one with tnctl council role new <name> --job \"…\" --teamspace\n"; errs != want {
		t.Errorf("stderr %q, want %q", errs, want)
	}
	out, errs, err = councilArgs(t, p.env(t), "role", "list", "--templates")
	if err != nil {
		t.Fatal(err)
	}
	if out != "" || errs != "· no members\n" {
		t.Errorf("--templates: stdout %q stderr %q", out, errs)
	}
	wantReqs(t, p, "GET /council/catalog", "GET /council/catalog")
}

// TestCouncilErrorMappingText keeps hub text on one line (the top message, a field's message with and
// without a path), escapes a field path, adds used by only for role_in_use, and prefixes a document
// without fields.
func TestCouncilErrorMappingText(t *testing.T) {
	noBotMode(t)
	p := newChatAPI(t)
	fl, _, _, err := p.env(t).fleet()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, body, want string
		exit             int
	}{
		{"message on one line", councilErrBody("bad_request", "two\nlines\there", ""), "two lines here", codeRejected},
		{"field path escaped, message on one line", councilErrBody("invalid", "x",
			`"fields":[{"path":"spec.lead\nx","message":"m1\nmore"}]`), `spec.lead\nx: m1 more`, codeRejected},
		{"pathless field on one line", councilErrBody("invalid", "x",
			`"fields":[{"path":"yaml","message":"a list,\nnot an object"}]`), "a list, not an object", codeRejected},
		{"used by only for role_in_use", councilErrBody("conflict", "Someone changed it.",
			`"usedBy":[{"bot":"scout","roster":"ops","displayName":"Ops"}],"hiddenCount":1`), "Someone changed it.", codeConflict},
		{"document without fields", councilErrBody("not_admin", "Only an admin may change it.", `"document":0`),
			"document 1: Only an admin may change it.", codeAuth},
	} {
		t.Run(c.name, func(t *testing.T) {
			p.on("GET", "/council/catalog", 400, c.body)
			_, _, reqErr := fl.CouncilCatalog(context.Background())
			if reqErr == nil {
				t.Fatal("the platform answered success")
			}
			got := councilError(reqErr)
			if exitCodeOf(got) != c.exit || got.Error() != c.want {
				t.Errorf("exit %d %q, want exit %d %q", exitCodeOf(got), got.Error(), c.exit, c.want)
			}
		})
	}
}

// TestCouncilRoleListHubText keeps a slug, a name, a scope and a reason from the hub on their line.
func TestCouncilRoleListHubText(t *testing.T) {
	noBotMode(t)
	esc := string(rune(27))
	row := roleViewJSON("au\ndit", "Audit\n"+esc+"[1mlog", "team\nspace", "member", "invalid", nil,
		[]string{"its model\nis gone", esc + "[31mred" + esc + "[0m here"})
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/council", 200, `{"council":{"available":[`+row+`]}}`)
	out, errs, err := councilArgs(t, p.env(t), "role", "list", "--bot", "scout")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	want := "SLUG     NAME       SCOPE        PART    MODEL   EFFORT  TOOLS  STATUS\n" +
		`au\ndit  Audit log  team\nspace  member  sonnet  medium  -      unavailable` + "\n"
	if out != want {
		t.Errorf("stdout:\n%q\nwant:\n%q", out, want)
	}
	if wantErr := `· au\ndit: its model is gone; red here` + "\n"; errs != wantErr {
		t.Errorf("stderr %q, want %q", errs, wantErr)
	}
	wantReqs(t, p, "GET /bots/scout/council")
}

// TestCouncilUsedByOneLine keeps a council's bot name and display name on their line.
func TestCouncilUsedByOneLine(t *testing.T) {
	esc := string(rune(27))
	got := councilUsedBy([]api.CouncilUse{{Bot: "a\nb", Roster: "ops", DisplayName: "Ops\n" + esc + "[31mteam"}}, 0)
	if want := `a\nb (Ops team)`; got != want {
		t.Errorf("councilUsedBy = %q, want %q", got, want)
	}
}

// TestCouncilRoleGetTableEdges prints the effective tools, the reasons of an unavailable member, the dash
// of an empty description, and hub text on one line (name, description, reasons, updated and archived by).
func TestCouncilRoleGetTableEdges(t *testing.T) {
	noBotMode(t)
	view := roleViewJSON("triage", "Tri\nage", "teamspace", "member", "invalid", []string{"core_read"},
		[]string{"its model\nis gone", "no tools"})
	view = strings.Replace(view, `"effectiveTools":null`, `"effectiveTools":["get_device","list_devices"]`, 1)
	view = strings.Replace(view, `"description":"Does triage."`, `"description":""`, 1)
	detail := view[:len(view)-1] + `,"instructions":"one\r\ntwo","versions":[{"version":1}],` +
		`"updatedAt":1763213400000,"updatedBy":"ada\nlovelace","archivedAt":1763213400000,"archivedBy":"bob\nsmith"}`
	p := newChatAPI(t)
	p.on("GET", "/council/roles/triage", 200, roleAnswerJSON(detail))
	out, errs, err := councilArgs(t, p.env(t), "role", "get", "triage")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	want := "slug:        triage\n" +
		"name:        Tri age\n" +
		"scope:       teamspace\n" +
		"part:        member\n" +
		"model:       sonnet (claude-sonnet-5-5), effort medium\n" +
		"tools:       core_read\n" +
		"effective:   get_device, list_devices\n" +
		"access:      at most read (the bot's access decides)\n" +
		"skills:      -\n" +
		"version:     1\n" +
		"status:      unavailable: its model is gone; no tools\n" +
		"description: -\n" +
		"updated:     2025-11-15 13:30 UTC by ada lovelace\n" +
		"archived:    2025-11-15 13:30 UTC by bob smith\n" +
		"instructions:\n" +
		"  one\n" +
		"  two\n"
	if out != want {
		t.Errorf("stdout:\n%q\nwant:\n%q", out, want)
	}

	none := strings.Replace(roleViewJSON("audit", "Auditor", "teamspace", "member", "ok", nil, nil),
		`"effectiveTools":null`, `"effectiveTools":[]`, 1)
	none = strings.Replace(none, `"description":"Does audit."`, `"description":"Reads\npods."`, 1)
	p.on("GET", "/council/roles/audit", 200, roleAnswerJSON(none))
	out, errs, err = councilArgs(t, p.env(t), "role", "get", "audit")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if !strings.Contains(out, "effective:   none\n") || !strings.Contains(out, "description: Reads pods.\n") {
		t.Errorf("stdout %q, want effective: none and description: Reads pods.", out)
	}
	wantReqs(t, p, "GET /council/roles/triage", "GET /council/roles/audit")
}

// TestCouncilRoleHubWordsAreSafe keeps every hub value of both tables on its line and drives nothing:
// no ESC reaches the terminal, and a tab cannot split a list cell.
func TestCouncilRoleHubWordsAreSafe(t *testing.T) {
	noBotMode(t)
	esc := string(rune(27))
	bad := q("x" + esc + "[2J\nY\tZ")
	view := roleViewJSON("triage", "Triage", "teamspace", "member", "ok", nil, nil)
	for _, r := range [][2]string{
		{`"kind":"member"`, `"kind":` + bad},
		{`"model":"sonnet"`, `"model":` + bad},
		{`"modelId":"claude-sonnet-5-5"`, `"modelId":` + bad},
		{`"effort":"medium"`, `"effort":` + bad},
		{`"level":"read"`, `"level":` + bad},
		{`"ceiling":"read"`, `"ceiling":` + bad},
		{`"groups":[]`, `"groups":[` + bad + `]`},
		{`"skills":[]`, `"skills":[` + bad + `]`},
		{`"effectiveTools":null`, `"effectiveTools":[` + bad + `]`},
		{`"scope":"teamspace"`, `"scope":` + q("team\tspace")},
	} {
		if !strings.Contains(view, r[0]) {
			t.Fatalf("the fixture lacks %s", r[0])
		}
		view = strings.Replace(view, r[0], r[1], 1)
	}
	p := newChatAPI(t)
	p.on("GET", "/council/roles/triage", 200, roleAnswerJSON(view))
	p.on("GET", "/council/catalog", 200, catalogJSON([]string{view}, nil))

	out, errs, err := councilArgs(t, p.env(t), "role", "get", "triage")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if strings.Contains(out, esc) {
		t.Errorf("get: ESC on stdout: %q", out)
	}
	if n := strings.Count(out, "\n"); n != 12 {
		t.Errorf("get: %d lines, want 12: %q", n, out)
	}
	for _, want := range []string{
		"part:        x Y Z\n",
		"model:       x Y Z (x Y Z), effort x Y Z\n",
		"tools:       x Y Z\n",
		"effective:   x Y Z\n",
		"access:      at most x Y Z (the bot's access decides)\n",
		"skills:      x Y Z\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("get: stdout %q lacks %q", out, want)
		}
	}

	out, errs, err = councilArgs(t, p.env(t), "role", "list")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if strings.Contains(out, esc) {
		t.Errorf("list: ESC on stdout: %q", out)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("list: %d lines, want 2: %q", len(lines), out)
	}
	if want := "triage  Triage  " + `team\tspace` + "  x Y Z  x Y Z  x Y Z   x Y Z  ok"; lines[1] != want {
		t.Errorf("list row %q, want %q", lines[1], want)
	}

	// With a bot the hub's level is real, so it is printed, safely too.
	p.on("GET", "/bots/scout/council/roles/triage", 200, roleAnswerJSON(view))
	out, errs, err = councilArgs(t, p.env(t), "role", "get", "triage", "--bot", "scout")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if strings.Contains(out, esc) || !strings.Contains(out, "access:      x Y Z (ceiling x Y Z)\n") {
		t.Errorf("get --bot: stdout %q, want access:      x Y Z (ceiling x Y Z) and no ESC", out)
	}
	wantReqs(t, p, "GET /council/roles/triage", "GET /council/catalog", "GET /bots/scout/council/roles/triage")
}

// TestCouncilRoleGetUsageOrder: when a command line breaks two rules, the first check's error is
// reported, in exact text, before any request.
func TestCouncilRoleGetUsageOrder(t *testing.T) {
	noBotMode(t)
	const notMember = " is not a member: use a slug such as triage, or <scope>/<slug> with scope bot, teamspace or template"
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"get", "robot/triage", "--template"}, `"robot/triage"` + notMember},
		{[]string{"get", "teamspace/triage", "--template", "--bot", ".."}, `--template and "teamspace/triage" name different scopes`},
		{[]string{"export", "bot/triage", "--version", "0"}, "a bot's own member needs --bot <bot>"},
		{[]string{"get", "bot/triage", "--version", "2"}, "a bot's own member needs --bot <bot>"},
		{[]string{"get", "triage", "--version", "0"}, "--version must be 1 or more"},
		{[]string{"export", "triage", "--version", "0", "--bot", ".."}, "--version must be 1 or more"},
		{[]string{"get", "triage", "--version", "2", "--bot", ".."}, "--version needs -o yaml (only an export can show an older version)"},
		{[]string{"get", "triage", "--bot", "Scout"}, `"Scout" is not a bot name: use the bot's name, such as scout`},
	} {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			p := newChatAPI(t)
			_, _, err := councilArgs(t, p.env(t), append([]string{"role"}, c.args...)...)
			if err == nil || exitCodeOf(err) != codeUsage || err.Error() != c.want {
				t.Errorf("exit %d %v, want exit 2 %q", exitCodeOf(err), err, c.want)
			}
			if n := p.countAll(); n != 0 {
				t.Errorf("%d requests were sent", n)
			}
		})
	}
}

// TestCouncilRoleExportVerbatimTeamspace writes the teamspace's and the premade member's exports as they
// came, too.
func TestCouncilRoleExportVerbatimTeamspace(t *testing.T) {
	noBotMode(t)
	body := "# a comment\r\nkind: CouncilRole\r\nspec:\r\n  name: triage \r\nlast: no newline"
	p := newChatAPI(t)
	p.on("GET", "/council/roles/triage/export", 200, body)
	p.on("GET", "/council/roles/captain/export", 200, body)
	for _, args := range [][]string{
		{"export", "triage"},
		{"get", "triage", "-o", "yaml"},
		{"export", "template/captain"},
		{"get", "captain", "--template", "-o", "yaml"},
	} {
		out, errs, err := councilArgs(t, p.env(t), append([]string{"role"}, args...)...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, errs)
		}
		if out != body {
			t.Errorf("%v: stdout %q, want %q", args, out, body)
		}
	}
	wantReqs(t, p, "GET /council/roles/triage/export", "GET /council/roles/triage/export",
		"GET /council/roles/captain/export?scope=template", "GET /council/roles/captain/export?scope=template")
}

// TestCouncilRoleListIgnoresOutputFlag: the global -o, whatever its value, does not change role list:
// the same table, the same stderr, no error. Only --json prints JSON.
func TestCouncilRoleListIgnoresOutputFlag(t *testing.T) {
	noBotMode(t)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/council", 200, `{"council":{"available":[`+
		roleViewJSON("audit", "Auditor", "teamspace", "member", "invalid", nil, []string{"its model is gone"})+`]}}`)
	want, wantErr, err := councilArgs(t, p.env(t), "role", "list", "--bot", "scout")
	if err != nil || !strings.HasPrefix(want, "SLUG") {
		t.Fatalf("plain run: %v\n%q\n%s", err, want, wantErr)
	}
	for _, o := range []string{"json", "yaml", "bogus"} {
		out, errs, err := councilArgs(t, p.env(t), "role", "list", "--bot", "scout", "-o", o)
		if err != nil {
			t.Errorf("-o %s: %v", o, err)
			continue
		}
		if out != want || errs != wantErr {
			t.Errorf("-o %s changed the output: stdout %q stderr %q, want %q %q", o, out, errs, want, wantErr)
		}
	}
	wantReqs(t, p, "GET /bots/scout/council", "GET /bots/scout/council", "GET /bots/scout/council", "GET /bots/scout/council")
}

// TestCouncilRoleShowHelp explains <member> in get's and export's help; export's --version needs no -o.
func TestCouncilRoleShowHelp(t *testing.T) {
	noBotMode(t)
	out, _, err := councilArgs(t, &cliEnv{}, "role", "get", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"tnctl council role get <member> [flags]",
		"<member> is a slug such as triage, or <scope>/<slug> (scope bot, teamspace or template) when\n",
		"a bot's own member needs --bot <bot>.\n",
		"  tnctl council role get bot/triage --bot scout\n",
		"print an older version (with -o yaml)",
		"Show one member a bot's councils can use: its part, model, tools, access, status and description,\n",
		"(for a shared member) the councils that use it.\n",
		"a bot's own member needs --bot <bot>.\n\n" +
			"-o json prints the member as the platform sent it. -o yaml prints it as YAML, byte for byte as\n" +
			"the platform exports it (as tnctl council role export does); --version N then prints an older version.\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("get --help lacks %q:\n%s", want, out)
		}
	}
	out, _, err = councilArgs(t, &cliEnv{}, "role", "export", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"tnctl council role export <member> [flags]",
		"Print one member as YAML, byte for byte as the platform exports it.",
		"<member> is a slug such as triage, or <scope>/<slug> (scope bot, teamspace or template), read\nas in tnctl council role get.\n",
		"print an older version\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("export --help lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "(with -o yaml)") {
		t.Errorf("export's --version help names -o yaml:\n%s", out)
	}
}

// TestCouncilUsedBy names the councils a member is in, and the ones the person cannot see.
func TestCouncilUsedBy(t *testing.T) {
	ops := []api.CouncilUse{{Bot: "scout", Roster: "ops", DisplayName: "Ops"}}
	both := []api.CouncilUse{ops[0], {Bot: "arm-2", Roster: "review", DisplayName: "Review"}}
	for _, c := range []struct {
		used   []api.CouncilUse
		hidden int
		want   string
	}{
		{ops, 0, "scout (Ops)"},
		{both, 0, "scout (Ops) and arm-2 (Review)"},
		{both, 2, "scout (Ops), arm-2 (Review) and 2 more you cannot see"},
		{nil, 1, "1 council you cannot see"},
		{nil, 0, ""},
		{nil, 2, "2 councils you cannot see"},
	} {
		if got := councilUsedBy(c.used, c.hidden); got != c.want {
			t.Errorf("councilUsedBy(%d, %d) = %q, want %q", len(c.used), c.hidden, got, c.want)
		}
	}
}

// TestParseMemberRef reads a member's reference: a slug, or a scope and a slug.
func TestParseMemberRef(t *testing.T) {
	ok := []struct{ ref, scope, slug string }{
		{"triage", "", "triage"},
		{"bot/triage", "bot", "triage"},
		{"teamspace/triage", "teamspace", "triage"},
		{"template/captain", "template", "captain"},
	}
	for _, c := range ok {
		scope, slug, got := parseMemberRef(c.ref)
		if !got || scope != c.scope || slug != c.slug {
			t.Errorf("parseMemberRef(%q) = %q, %q, %v; want %q, %q, true", c.ref, scope, slug, got, c.scope, c.slug)
		}
	}
	for _, ref := range []string{"Triage", "t", "a234567890123456789012345", "robot/triage", "bot/", "/triage", "bot/triage/x", ""} {
		if _, _, got := parseMemberRef(ref); got {
			t.Errorf("parseMemberRef(%q) is ok", ref)
		}
	}
}

// TestCouncilTime formats a hub time in UTC, whatever the host's zone. TestMain runs every test in
// CET, so a time formatted in local time would read 01:00 and 14:30.
func TestCouncilTime(t *testing.T) {
	if _, off := time.Now().Zone(); off == 0 {
		t.Fatal("TestMain must set a time zone other than UTC")
	}
	for _, c := range []struct {
		ms   int64
		want string
	}{
		{0, "1970-01-01 00:00 UTC"},
		{1763213400000, "2025-11-15 13:30 UTC"},
	} {
		if got := councilTime(c.ms); got != c.want {
			t.Errorf("councilTime(%d) = %q, want %q", c.ms, got, c.want)
		}
	}
}

// TestCouncilStatus says a status in the web UI's words, and never lets one leave its line.
func TestCouncilStatus(t *testing.T) {
	for _, c := range []struct{ status, want string }{
		{"ok", "ok"},
		{"invalid", "unavailable"},
		{"unchecked", "not checked"},
		{"new\nthing", `new\nthing`},
	} {
		if got := councilStatus(c.status); got != c.want {
			t.Errorf("councilStatus(%q) = %q, want %q", c.status, got, c.want)
		}
	}
}

// TestCouncilBot checks a --bot value before any request.
func TestCouncilBot(t *testing.T) {
	for _, bot := range []string{"scout", "arm-2"} {
		if err := councilBot(bot); err != nil {
			t.Errorf("councilBot(%q): %v", bot, err)
		}
	}
	for _, bot := range []string{"..", "Scout", "-a", "a_b", strings.Repeat("a", 64)} {
		err := councilBot(bot)
		if err == nil || exitCodeOf(err) != codeUsage || !strings.Contains(err.Error(), "is not a bot name") {
			t.Errorf("councilBot(%q): %v", bot, err)
		}
	}
}

// TestCouncilRoleHubCellsAndNotes keeps a hub slug's and status's tab out of the list's columns, and
// a hub note on its line in the get table.
func TestCouncilRoleHubCellsAndNotes(t *testing.T) {
	noBotMode(t)
	esc := string(rune(27))
	bad := q("x" + esc + "[2J\nY\tZ")
	view := roleViewJSON("triage", "Triage", "teamspace", "member", "ok", nil, nil)
	for _, r := range [][2]string{
		{`"slug":"triage"`, `"slug":` + q("tri\tage")},
		{`"status":"ok"`, `"status":` + q("new\tstate")},
		{`"notes":[]`, `"notes":[` + bad + `]`},
	} {
		if !strings.Contains(view, r[0]) {
			t.Fatalf("the fixture lacks %s", r[0])
		}
		view = strings.Replace(view, r[0], r[1], 1)
	}
	p := newChatAPI(t)
	p.on("GET", "/council/roles/triage", 200, roleAnswerJSON(view))
	p.on("GET", "/council/catalog", 200, catalogJSON([]string{view}, nil))
	out, errs, err := councilArgs(t, p.env(t), "role", "get", "triage")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if strings.Contains(out, esc) || !strings.Contains(out, "notes:       x Y Z\n") {
		t.Errorf("get: the note is not on one line: %q", out)
	}
	out, errs, err = councilArgs(t, p.env(t), "role", "list")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	want := "SLUG      NAME    SCOPE      PART    MODEL   EFFORT  TOOLS  STATUS\n" +
		`tri\tage  Triage  teamspace  member  sonnet  medium  -      new\tstate` + "\n"
	if out != want {
		t.Errorf("list:\n%q\nwant:\n%q", out, want)
	}
	wantReqs(t, p, "GET /council/roles/triage", "GET /council/catalog")
}

// TestCouncilReasonsSkipsEmpty leaves out a reason or note with no words left: no empty notes line, no
// dangling colon after a status or a slug, no stray "; ".
func TestCouncilReasonsSkipsEmpty(t *testing.T) {
	noBotMode(t)
	esc := string(rune(27))
	for _, c := range []struct {
		in   []string
		want string
	}{
		{[]string{esc + "[2J", "a", " \t"}, "a"},
		{[]string{""}, ""},
		{nil, ""},
		{[]string{"a", "b\nc"}, "a; b c"},
	} {
		if got := councilReasons(c.in); got != c.want {
			t.Errorf("councilReasons(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	view := roleViewJSON("audit", "Auditor", "teamspace", "member", "invalid", nil, []string{""})
	view = strings.Replace(view, `"notes":[]`, `"notes":[`+q(string(rune(7)))+`]`, 1)
	p := newChatAPI(t)
	p.on("GET", "/council/roles/audit", 200, roleAnswerJSON(view))
	p.on("GET", "/council/catalog", 200, catalogJSON([]string{view}, nil))
	out, errs, err := councilArgs(t, p.env(t), "role", "get", "audit")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if !strings.Contains(out, "status:      unavailable\n") || strings.Contains(out, "notes:") {
		t.Errorf("get: stdout %q, want status:      unavailable and no notes line", out)
	}
	noTrailingSpaces(t, out)
	_, errs, err = councilArgs(t, p.env(t), "role", "list")
	if err != nil {
		t.Fatal(err)
	}
	if want := "· audit\n"; errs != want {
		t.Errorf("list: stderr %q, want %q", errs, want)
	}
	wantReqs(t, p, "GET /council/roles/audit", "GET /council/catalog")
}

// TestCouncilRoleGetAccessWithoutBot gives, without --bot, the ceiling as the most a member may use
// (the hub's level there is the ceiling), a ceiling of none as it is, and with --bot the real level.
func TestCouncilRoleGetAccessWithoutBot(t *testing.T) {
	noBotMode(t)
	shared := roleViewJSON("audit", "Auditor", "teamspace", "member", "ok", nil, nil)
	shared = strings.Replace(shared, `"level":"read","ceiling":"read"`, `"level":"creator","ceiling":"creator"`, 1)
	off := roleViewJSON("notes", "Notes", "teamspace", "member", "ok", nil, nil)
	off = strings.Replace(off, `"level":"read","ceiling":"read"`, `"level":"none","ceiling":"none"`, 1)
	own := roleViewJSON("audit", "Auditor", "teamspace", "member", "ok", nil, nil)
	own = strings.Replace(own, `"level":"read","ceiling":"read"`, `"level":"read","ceiling":"creator"`, 1)
	for _, f := range []string{shared, off, own} {
		if !strings.Contains(f, `"ceiling"`) || strings.Contains(f, `"level":"read","ceiling":"read"`) {
			t.Fatalf("a fixture was not changed: %s", f)
		}
	}
	p := newChatAPI(t)
	p.on("GET", "/council/roles/audit", 200, roleAnswerJSON(shared))
	p.on("GET", "/council/roles/notes", 200, roleAnswerJSON(off))
	p.on("GET", "/bots/scout/council/roles/audit", 200, roleAnswerJSON(own))
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"get", "audit"}, "access:      at most creator (the bot's access decides)\n"},
		{[]string{"get", "notes"}, "access:      none (ceiling none)\n"},
		{[]string{"get", "audit", "--bot", "scout"}, "access:      read (ceiling creator)\n"},
	} {
		out, errs, err := councilArgs(t, p.env(t), append([]string{"role"}, c.args...)...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", c.args, err, errs)
		}
		if !strings.Contains(out, c.want) {
			t.Errorf("%v: stdout %q lacks %q", c.args, out, c.want)
		}
	}
	wantReqs(t, p, "GET /council/roles/audit", "GET /council/roles/notes", "GET /bots/scout/council/roles/audit")
}

// TestCouncilRoleListHelp pins role list's help: what it lists with and without --bot, the premade
// rows, the SCOPE and STATUS words and the flag texts.
func TestCouncilRoleListHelp(t *testing.T) {
	noBotMode(t)
	list := councilSub(councilSub(newCouncilCmd(&cliEnv{}), "role"), "list")
	if list == nil {
		t.Fatal("council role has no list subcommand")
	}
	if list.Use != "list" || list.Short != "List the members a bot's councils can use" {
		t.Errorf("Use %q, Short %q", list.Use, list.Short)
	}
	wantLong := "List the members a bot's councils can use: with --bot, the bot's own members and the teamspace's\n" +
		"shared ones; without it, only the teamspace's shared members. --templates lists the premade\n" +
		"members too, first. SCOPE is bot, teamspace or template (a member's <scope>/<slug>). STATUS is ok,\n" +
		"unavailable (the reasons follow on stderr) or not checked. --json prints the members as the\n" +
		"platform sent them."
	if list.Long != wantLong {
		t.Errorf("list Long:\n%s", list.Long)
	}
	wantExample := "  tnctl council role list --bot scout\n" +
		"  tnctl council role list --bot scout --templates\n" +
		"  tnctl council role list --templates --json"
	if list.Example != wantExample {
		t.Errorf("list Example:\n%s", list.Example)
	}
	for name, want := range map[string]string{
		"bot":       "the TeknoirBot whose members to list",
		"templates": "list the premade members too",
		"json":      "print the members as the platform sent them, as JSON",
	} {
		f := list.Flags().Lookup(name)
		if f == nil {
			t.Errorf("list has no --%s", name)
			continue
		}
		if f.Usage != want {
			t.Errorf("--%s usage %q, want %q", name, f.Usage, want)
		}
	}
}
