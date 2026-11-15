package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/teknoir/cli/pkg/api"
)

// ---------- fixtures ----------

// RosterSummary objects as P1 lists them and P4 carries them.
const rosterStandardJSON = `{"id":"tpl:standard","name":"standard","displayName":"Standard","scope":"template","version":1,"lead":"captain","members":["fleet","coder"],"reviewer":"critic","isDefault":true,"legacyCompatible":true,"status":"ok","reasons":[]}`
const rosterOpsJSON = `{"id":"tpl:ops","name":"ops","displayName":"Ops","scope":"template","version":1,"lead":"captain","members":["fleet","triage-sre","stream-analyst"],"reviewer":"critic","isDefault":false,"legacyCompatible":false,"status":"ok","reasons":[]}`
const rosterNightJSON = `{"id":"ros_1","name":"night","displayName":"Night shift","scope":"bot","version":3,"lead":"captain","members":["triage","rol_AbCdEfGhIjKlMnOpQrStUv"],"reviewer":null,"isDefault":false,"legacyCompatible":false,"status":"invalid","reasons":["rol_AbCdEfGhIjKlMnOpQrStUv: this member was archived or deleted; replace it in this council."]}`

// chatInfoV2 is P1 from a hub with councils: the default council standard, its four members, and the
// councils standard, ops and night.
const chatInfoV2 = `{"bot":{"namespace":"teknoir-demo","name":"scout","uid":"u1","access":"creator","phase":"Running","online":true,"lastSeen":1,"council":"tnctl/v0.3.0"},` +
	`"canChat":true,"isCreator":true,` +
	`"members":[{"id":"captain","name":"Captain","model":"claude-sonnet-5-5","effort":"high","job":"leads"},` +
	`{"id":"fleet","name":"Fleet","model":"claude-haiku-5-5","effort":"medium","job":"facts"},` +
	`{"id":"coder","name":"Coder","model":"claude-sonnet-5-5","effort":"high","job":"code","runtime":"OpenCode"},` +
	`{"id":"critic","name":"Critic","model":"claude-sonnet-5-5","effort":"medium","job":"checks"}],` +
	`"budget":{"tokensToday":123456,"tokensPerDay":5000000},` +
	`"council":{"proto":2,"tnctl":"v0.3.0","kit":null,"capsError":null,"defaultRoster":"standard","canEdit":{"ok":true},` +
	`"roster":{"id":"tpl:standard","name":"standard","displayName":"Standard","scope":"template","version":1,"lead":"captain","members":["fleet","coder"],"reviewer":"critic","isDefault":true,"legacyCompatible":true,"status":"ok","reasons":[],"description":null,"budget":null,` +
	`"roles":[{"id":"tpl:captain","slug":"captain","name":"Captain","kind":"lead"},{"id":"tpl:fleet","slug":"fleet","name":"Fleet","kind":"member"},{"id":"tpl:coder","slug":"coder","name":"Coder","kind":"member"},{"id":"tpl:critic","slug":"critic","name":"Critic","kind":"reviewer"}]},` +
	`"rosters":[` + rosterStandardJSON + `,` + rosterOpsJSON + `,` + rosterNightJSON + `]}}`

// chatDetailOn is P4's answer for a council chat: council is a RosterSummary object, or "null" for a council
// the hub cannot find.
func chatDetailOn(thread, council string, lastSeq int64, turns ...string) string {
	block := "null"
	if council != "null" {
		block = `{"roster":` + council + `}`
	}
	return `{"thread":{"id":` + q(thread) + `,"title":"t","origin":"cli","member":null,"author":{"ref":"r"},"createdAt":1,"updatedAt":1,"turnCount":1,"lastTurn":null},` +
		`"council":` + block + `,"turns":[` + strings.Join(turns, ",") + `],"lastSeq":` + strconv.FormatInt(lastSeq, 10) + `}`
}

// chatMemberThreadRunning is chatMemberThreadFleet with a turn still running, trn_9.
var chatMemberThreadRunning = strings.Replace(chatMemberThreadFleet, `"turns":[]`, `"turns":[`+chatTurnAt("trn_9", "thr_old", 1, "running", "")+`]`, 1)

// chatStreamTrn9 replaces thr_old's stream with the end of turn trn_9.
func chatStreamTrn9(p *chatAPI) {
	p.onStream("/bots/scout/chat/threads/thr_old/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, []string{
			frameOf("trn_9", 1, "final", `"text":"the turn finished"`),
			frameOf("trn_9", 2, "turn_end", `"state":"done"`),
		})
	})
}

// chatNewThread registers thr_new, on council standard: P3 makes it, its read, turns and stream answer.
func chatNewThread(p *chatAPI) {
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
	p.on("GET", "/bots/scout/chat/threads/thr_new", 200, chatDetailOn("thr_new", rosterStandardJSON, 2))
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
	p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, quickFrames(quickFinal))
	})
}

// chatREPLRun runs the conversation "chat scout <args>" with this stdin.
func chatREPLRun(t *testing.T, p *chatAPI, stdin string, args ...string) (string, string, error) {
	t.Helper()
	old := cliStdin
	cliStdin = strings.NewReader(stdin)
	t.Cleanup(func() { cliStdin = old })
	return runCLI(t, p.env(t), append([]string{"chat", "scout"}, args...)...)
}

// chatTurnMaps is the bodies of the turns posted on one thread, in order.
func chatTurnMaps(t *testing.T, p *chatAPI, thread string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range p.seen("POST", "/bots/scout/chat/threads/"+thread+"/turns") {
		out = append(out, chatBodyMap(t, r.Body))
	}
	return out
}

// ---------- the lines of the chat ----------

func TestChatMemberLine(t *testing.T) {
	noBotMode(t)
	cases := []struct {
		line       string
		slugs      []string
		wantMember string
		wantPrompt string
	}{
		{"@fleet look", []string{"fleet", "coder"}, "fleet", "look"},
		{"@fleet\tlook", []string{"fleet", "coder"}, "fleet", "look"},
		{"@fleet    look", []string{"fleet"}, "fleet", "look"},
		{"@fleet", []string{"fleet"}, "fleet", ""},
		{"@fleetx look", []string{"fleet"}, "", "@fleetx look"},
		{"@triage-sre why", []string{"fleet", "triage-sre"}, "triage-sre", "why"},
		{"@captain hi", []string{"fleet", "coder"}, "", "@captain hi"},
		{"@coder fix", nil, "", "@coder fix"},
		{"hello @fleet", []string{"fleet"}, "", "hello @fleet"},
		{"@", []string{"fleet"}, "", "@"},
		{"@ fleet x", []string{"fleet"}, "", "@ fleet x"},
		{"@Fleet x", []string{"fleet"}, "", "@Fleet x"},
	}
	for _, c := range cases {
		t.Run(c.line, func(t *testing.T) {
			member, prompt := chatMemberLine(c.line, c.slugs)
			if member != c.wantMember || prompt != c.wantPrompt {
				t.Errorf("chatMemberLine(%q, %q) = (%q, %q), want (%q, %q)", c.line, c.slugs, member, prompt, c.wantMember, c.wantPrompt)
			}
		})
	}
}

func TestChatSlugLists(t *testing.T) {
	noBotMode(t)
	decodeInfo := func(body string) api.ChatInfo {
		t.Helper()
		var info api.ChatInfo
		if err := json.Unmarshal([]byte(body), &info); err != nil {
			t.Fatalf("chat info %q: %v", body, err)
		}
		return info
	}
	decodeThread := func(body string) *api.ChatThreadDetail {
		t.Helper()
		var d api.ChatThreadDetail
		if err := json.Unmarshal([]byte(body), &d); err != nil {
			t.Fatalf("thread %q: %v", body, err)
		}
		return &d
	}
	memberThread := decodeThread(chatMemberThreadFleet)
	noon := decodeThread(chatDetailOn("thr_old", "null", 42))

	newCases := []struct {
		name string
		info api.ChatInfo
		f    chatFlags
		want []string
	}{
		{"the default council", decodeInfo(chatInfoV2), chatFlags{}, []string{"fleet", "coder"}},
		{"ops", decodeInfo(chatInfoV2), chatFlags{roster: "ops"}, []string{"fleet", "triage-sre", "stream-analyst"}},
		{"night, without the deleted member", decodeInfo(chatInfoV2), chatFlags{roster: "night"}, []string{"triage"}},
		{"a council the info does not list", decodeInfo(chatInfoV2), chatFlags{roster: "nope"}, nil},
		{"one member", decodeInfo(chatInfoV2), chatFlags{memberSlug: "fleet"}, nil},
		{"one member, an older hub", decodeInfo(chatInfoBody), chatFlags{memberSlug: "triage"}, nil},
		{"an older hub", decodeInfo(chatInfoBody), chatFlags{}, []string{"fleet", "coder"}},
		{"an older hub, a council", decodeInfo(chatInfoBody), chatFlags{roster: "ops"}, []string{"fleet", "coder"}},
		{"a council block without a default roster", decodeInfo(chatInfoCouncils), chatFlags{}, nil},
	}
	for _, c := range newCases {
		t.Run("new: "+c.name, func(t *testing.T) {
			if got := chatSlugsForNew(&c.info, c.f); !slices.Equal(got, c.want) {
				t.Errorf("slugs %q, want %q", got, c.want)
			}
		})
	}

	threadCases := []struct {
		name string
		info api.ChatInfo
		d    *api.ChatThreadDetail
		want []string
	}{
		{"the thread's council", decodeInfo(chatInfoV2), decodeThread(chatDetailOn("thr_old", rosterOpsJSON, 42)), []string{"fleet", "triage-sre", "stream-analyst"}},
		{"night, without the deleted member", decodeInfo(chatInfoV2), decodeThread(chatDetailOn("thr_old", rosterNightJSON, 42)), []string{"triage"}},
		{"one member", decodeInfo(chatInfoV2), memberThread, nil},
		{"one member, an older hub", decodeInfo(chatInfoBody), memberThread, nil},
		{"a council the hub cannot find", decodeInfo(chatInfoV2), noon, nil},
		{"an older hub keeps fleet and coder", decodeInfo(chatInfoBody), decodeThread(chatDetailOn("thr_old", rosterOpsJSON, 42)), []string{"fleet", "coder"}},
		{"each slug once, and entries that are not slugs left out", decodeInfo(chatInfoV2),
			decodeThread(chatDetailOn("thr_old", strings.Replace(rosterOpsJSON, `["fleet","triage-sre","stream-analyst"]`, `["fleet","fleet","Coder","coder"]`, 1), 42)),
			[]string{"fleet", "coder"}},
	}
	for _, c := range threadCases {
		t.Run("thread: "+c.name, func(t *testing.T) {
			if got := chatSlugsForThread(&c.info, c.d); !slices.Equal(got, c.want) {
				t.Errorf("slugs %q, want %q", got, c.want)
			}
		})
	}

	rosterCases := []struct {
		name   string
		info   api.ChatInfo
		roster string
		want   []string
	}{
		{"ops", decodeInfo(chatInfoV2), "ops", []string{"fleet", "triage-sre", "stream-analyst"}},
		{"standard", decodeInfo(chatInfoV2), "standard", []string{"fleet", "coder"}},
		{"a council the info does not list", decodeInfo(chatInfoV2), "nope", nil},
		{"an older hub", decodeInfo(chatInfoBody), "ops", []string{"fleet", "coder"}},
	}
	for _, c := range rosterCases {
		t.Run("roster: "+c.name, func(t *testing.T) {
			if got := chatSlugsForRoster(&c.info, c.roster); !slices.Equal(got, c.want) {
				t.Errorf("slugs %q, want %q", got, c.want)
			}
		})
	}

	if got, want := chatHelpLine(nil), "commands: /new /cancel /quit"; got != want {
		t.Errorf("help line %q, want %q", got, want)
	}
	if got, want := chatHelpLine([]string{"fleet", "triage-sre"}), "commands: /new /cancel /quit, @fleet, @triage-sre"; got != want {
		t.Errorf("help line %q, want %q", got, want)
	}

	info := decodeInfo(chatInfoV2)
	old := decodeInfo(chatInfoBody)
	if err := chatRosterMissing("scout", &info, chatFlags{roster: "ops"}); err != nil {
		t.Errorf("a listed council was refused: %v", err)
	}
	if err := chatRosterMissing("scout", &info, chatFlags{}); err != nil {
		t.Errorf("no --roster was refused: %v", err)
	}
	if err := chatRosterMissing("scout", &old, chatFlags{roster: "nope"}); err != nil {
		t.Errorf("an older hub refused a council it cannot know: %v", err)
	}
	for _, block := range []string{`,"council":{"defaultRoster":"standard","rosters":[]}}`, `,"council":{"defaultRoster":"standard"}}`} {
		none := decodeInfo(strings.TrimSuffix(chatInfoBody, "}") + block)
		if none.Council == nil {
			t.Fatalf("%s: no council block", block)
		}
		if err := chatRosterMissing("scout", &none, chatFlags{roster: "review"}); err != nil {
			t.Errorf("%s: a P1 that lists no council refused one: %v", block, err)
		}
	}
	err := chatRosterMissing("scout", &info, chatFlags{roster: "nope"})
	if err == nil || exitCodeOf(err) != codeUsage {
		t.Fatalf("exit %d: %v", exitCodeOf(err), err)
	}
	if want := "There is no council 'nope' for scout.\ncouncils: standard, ops, night"; err.Error() != want {
		t.Errorf("error %q, want %q", err, want)
	}
}

// ---------- the banner ----------

// chatInfoRows are the banner's member rows: the lines between the first line and the tokens line, leaving
// the council line out, each split into its fields.
func chatInfoRows(t *testing.T, out string) [][]string {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	var rows [][]string
	for i, l := range lines {
		if i == 0 || strings.HasPrefix(l, "council: ") {
			continue
		}
		if strings.HasPrefix(l, "tokens today: ") {
			break
		}
		rows = append(rows, strings.Fields(l))
	}
	return rows
}

// chatInfoLine is the banner's line at index i (0 is the bot's line).
func chatInfoLine(t *testing.T, out string, i int) string {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if i >= len(lines) {
		t.Fatalf("line %d of %q", i, out)
	}
	return lines[i]
}

func TestPrintChatInfoCouncil(t *testing.T) {
	noBotMode(t)
	v2Lines := []string{
		"scout (creator, online) — computer: tnctl/v0.3.0",
		"council: Standard (standard)",
		"Captain  leads    claude-sonnet-5-5  high",
		"Fleet    @fleet   claude-haiku-5-5   medium",
		"Coder    @coder   claude-sonnet-5-5  high",
		"Critic   reviews  claude-sonnet-5-5  medium",
		"tokens today: 123456 of 5000000",
	}
	oldLines := []string{
		"scout (creator, online) — computer: tnctl/v0.3.0",
		"Captain  claude-sonnet-5-5  high",
		"Fleet    claude-sonnet-5-5  medium",
		"Coder    claude-sonnet-5-5  medium",
		"tokens today: 123456 of 5000000",
	}
	// The four rows of the default council, as its own list makes them.
	standardRows := [][]string{
		{"Captain", "leads", "claude-sonnet-5-5", "high"},
		{"Fleet", "@fleet", "claude-haiku-5-5", "medium"},
		{"Coder", "@coder", "claude-sonnet-5-5", "high"},
		{"Critic", "reviews", "claude-sonnet-5-5", "medium"},
	}
	defaultGone := `{"bot":{"namespace":"teknoir-demo","name":"scout","uid":"u1","access":"creator","phase":"Running","online":true,"lastSeen":1,"council":"tnctl/v0.3.0"},` +
		`"canChat":true,"isCreator":true,"members":[],"budget":{"tokensToday":123456,"tokensPerDay":5000000},` +
		`"council":{"proto":2,"tnctl":"v0.3.0","kit":null,"capsError":null,"defaultRoster":"ros_gone","canEdit":{"ok":true},` +
		`"roster":{"id":"ros_gone","name":"ros_gone","displayName":"ros_gone","scope":"bot","version":0,"lead":"","members":[],"reviewer":null,"isDefault":true,"legacyCompatible":false,"status":"invalid","reasons":["This bot's default council is gone: make another council the default."],"description":null,"budget":null,"roles":[]},` +
		`"rosters":[` + rosterStandardJSON + `,` + rosterOpsJSON + `,` + rosterNightJSON + `]}}`
	goneLead := `{"bot":{"namespace":"teknoir-demo","name":"scout","uid":"u1","access":"creator","phase":"Running","online":true,"lastSeen":1,"council":"tnctl/v0.3.0"},` +
		`"canChat":true,"isCreator":true,` +
		`"members":[{"id":"fleet","name":"Fleet","model":"claude-haiku-5-5","effort":"medium","job":"facts"}],` +
		`"budget":{"tokensToday":123456,"tokensPerDay":5000000},` +
		`"council":{"proto":2,"tnctl":"v0.3.0","kit":null,"capsError":null,"defaultRoster":"standard","canEdit":{"ok":true},` +
		`"roster":{"id":"tpl:standard","name":"standard","displayName":"Standard","scope":"template","version":1,"lead":"gone-lead","members":["fleet"],"reviewer":"gone-critic","isDefault":true,"legacyCompatible":false,"status":"invalid","reasons":["gone-lead: this member was archived or deleted; replace it in this council."],"description":null,"budget":null,` +
		`"roles":[{"id":"tpl:fleet","slug":"fleet","name":"Fleet","kind":"member"}]},` +
		`"rosters":[` + rosterStandardJSON + `,` + rosterOpsJSON + `,` + rosterNightJSON + `]}}`
	shareSlug := `{"bot":{"namespace":"teknoir-demo","name":"scout","uid":"u1","access":"creator","phase":"Running","online":true,"lastSeen":1,"council":"tnctl/v0.3.0"},` +
		`"canChat":true,"isCreator":true,` +
		`"members":[{"id":"chair","name":"Chair","model":"claude-sonnet-5-5","effort":"high","job":"j"},` +
		`{"id":"chair","name":"Deputy","model":"claude-sonnet-5-5","effort":"high","job":"j"}],` +
		`"budget":{"tokensToday":123456,"tokensPerDay":5000000},` +
		`"council":{"proto":2,"tnctl":"v0.3.0","kit":null,"capsError":null,"defaultRoster":"standard","canEdit":{"ok":true},` +
		`"roster":{"id":"tpl:standard","name":"standard","displayName":"Standard","scope":"template","version":1,"lead":"chair","members":["chair"],"reviewer":null,"isDefault":true,"legacyCompatible":true,"status":"ok","reasons":[],"description":null,"budget":null,` +
		`"roles":[{"id":"tpl:chair","slug":"chair","name":"Chair","kind":"lead"},{"id":"tpl:chair2","slug":"chair","name":"Deputy","kind":"member"}]},` +
		`"rosters":[` + rosterStandardJSON + `,` + rosterOpsJSON + `,` + rosterNightJSON + `]}}`

	cases := []struct {
		name  string
		info  string
		slugs []string
		// useSlugs says the row gives the list itself (nil is a row of its own).
		useSlugs bool
		check    func(t *testing.T, out string)
	}{
		{"the default council", chatInfoV2, nil, false, func(t *testing.T, out string) {
			if want := strings.Join(v2Lines, "\n") + "\n"; out != want {
				t.Errorf("banner %q, want %q", out, want)
			}
		}},
		{"an older hub", chatInfoBody, nil, false, func(t *testing.T, out string) {
			if want := strings.Join(oldLines, "\n") + "\n"; out != want {
				t.Errorf("banner %q, want %q", out, want)
			}
		}},
		{"a bot that reported no computer", strings.Replace(chatInfoV2, `"council":"tnctl/v0.3.0"`, `"council":null`, 1), nil, false,
			func(t *testing.T, out string) {
				if got, want := chatInfoLine(t, out, 0), "scout (creator, online) — computer: unknown"; got != want {
					t.Errorf("line %q, want %q", got, want)
				}
			}},
		{"a bot whose default council is gone", defaultGone, nil, false, func(t *testing.T, out string) {
			want := "scout (creator, online) — computer: tnctl/v0.3.0\n" +
				"council: ros_gone — unavailable: This bot's default council is gone: make another council the default.\n" +
				"tokens today: 123456 of 5000000\n"
			if out != want {
				t.Errorf("banner %q, want %q", out, want)
			}
		}},
		{"a default council that cannot answer", strings.Replace(chatInfoV2,
			`"status":"ok","reasons":[],"description"`,
			`"status":"invalid","reasons":["fleet: the platform no longer offers Haiku.","coder: this bot's computer has no guide x (tnctl 0.3.0)."],"description"`, 1), nil, false,
			func(t *testing.T, out string) {
				want := "council: Standard (standard) — unavailable: fleet: the platform no longer offers Haiku.; coder: this bot's computer has no guide x (tnctl 0.3.0)."
				if got := chatInfoLine(t, out, 1); got != want {
					t.Errorf("line %q, want %q", got, want)
				}
			}},
		{"a default council that cannot answer, with no reasons", strings.Replace(chatInfoV2,
			`"status":"ok","reasons":[],"description"`, `"status":"invalid","reasons":[],"description"`, 1), nil, false,
			func(t *testing.T, out string) {
				if got, want := chatInfoLine(t, out, 1), "council: Standard (standard) — unavailable"; got != want {
					t.Errorf("line %q, want %q", got, want)
				}
			}},
		{"a default council not checked yet", strings.Replace(chatInfoV2,
			`"status":"ok","reasons":[],"description"`, `"status":"unchecked","reasons":[],"description"`, 1), nil, false,
			func(t *testing.T, out string) {
				if got, want := chatInfoLine(t, out, 1), "council: Standard (standard)"; got != want {
					t.Errorf("line %q, want %q", got, want)
				}
			}},
		{"a gone lead and reviewer", goneLead, nil, false, func(t *testing.T, out string) {
			want := [][]string{{"Fleet", "@fleet", "claude-haiku-5-5", "medium"}}
			if got := chatInfoRows(t, out); !reflect.DeepEqual(got, want) {
				t.Errorf("rows %q, want %q", got, want)
			}
		}},
		{"a lead and a member that share a slug", shareSlug, nil, false, func(t *testing.T, out string) {
			want := [][]string{
				{"Chair", "leads", "claude-sonnet-5-5", "high"},
				{"Deputy", "@chair", "claude-sonnet-5-5", "high"},
			}
			if got := chatInfoRows(t, out); !reflect.DeepEqual(got, want) {
				t.Errorf("rows %q, want %q", got, want)
			}
		}},
		{"no roles", strings.Replace(chatInfoV2,
			`"roles":[{"id":"tpl:captain","slug":"captain","name":"Captain","kind":"lead"},{"id":"tpl:fleet","slug":"fleet","name":"Fleet","kind":"member"},{"id":"tpl:coder","slug":"coder","name":"Coder","kind":"member"},{"id":"tpl:critic","slug":"critic","name":"Critic","kind":"reviewer"}]`,
			`"roles":[]`, 1), nil, false, func(t *testing.T, out string) {
			if got := chatInfoRows(t, out); !reflect.DeepEqual(got, standardRows) {
				t.Errorf("rows %q, want %q", got, standardRows)
			}
		}},
		{"roles out of line", strings.Replace(chatInfoV2,
			`{"id":"tpl:captain","slug":"captain","name":"Captain","kind":"lead"},{"id":"tpl:fleet","slug":"fleet","name":"Fleet","kind":"member"}`,
			`{"id":"tpl:fleet","slug":"fleet","name":"Fleet","kind":"member"},{"id":"tpl:captain","slug":"captain","name":"Captain","kind":"lead"}`, 1), nil, false,
			func(t *testing.T, out string) {
				if got := chatInfoRows(t, out); !reflect.DeepEqual(got, standardRows) {
					t.Errorf("rows %q, want %q", got, standardRows)
				}
			}},
		{"another council's list", chatInfoV2, []string{"fleet", "triage-sre", "stream-analyst"}, true, func(t *testing.T, out string) {
			want := [][]string{
				{"Captain", "leads", "claude-sonnet-5-5", "high"},
				{"Fleet", "@fleet", "claude-haiku-5-5", "medium"},
				{"Coder", "member", "claude-sonnet-5-5", "high"},
				{"Critic", "reviews", "claude-sonnet-5-5", "medium"},
			}
			if got := chatInfoRows(t, out); !reflect.DeepEqual(got, want) {
				t.Errorf("rows %q, want %q", got, want)
			}
		}},
		{"no list", chatInfoV2, nil, true, func(t *testing.T, out string) {
			rows := chatInfoRows(t, out)
			if len(rows) != 4 {
				t.Fatalf("rows %q", rows)
			}
			if rows[0][1] != "leads" || rows[1][1] != "member" || rows[2][1] != "member" || rows[3][1] != "reviews" {
				t.Errorf("parts %q", rows)
			}
		}},
		{"dirty text", strings.Replace(strings.Replace(chatInfoV2,
			`"displayName":"Standard","scope":"template","version":1,"lead"`,
			`"displayName":"Sta\u001b[31mndard","scope":"template","version":1,"lead"`, 1),
			`"id":"fleet","name":"Fleet"`, `"id":"fl\tx","name":"Fl\u001b[2Jeet"`, 1),
			[]string{"fleet", "coder", "fl\tx"}, true, func(t *testing.T, out string) {
				if got, want := chatInfoLine(t, out, 1), "council: Standard (standard)"; got != want {
					t.Errorf("line %q, want %q", got, want)
				}
				if strings.ContainsAny(out, "\x1b") {
					t.Errorf("an escape reached stdout: %q", out)
				}
				want := []string{"Fleet", `@fl\tx`, "claude-haiku-5-5", "medium"}
				for _, row := range chatInfoRows(t, out) {
					if len(row) > 0 && row[0] == "Fleet" {
						if !slices.Equal(row, want) {
							t.Errorf("row %q, want %q", row, want)
						}
						return
					}
				}
				t.Errorf("no Fleet row in %q", out)
			}},
		{"a council block without a default roster", chatInfoCouncils, nil, false, func(t *testing.T, out string) {
			if got, want := chatInfoLine(t, out, 1), "council: standard"; got != want {
				t.Errorf("line %q, want %q", got, want)
			}
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var info api.ChatInfo
			if err := json.Unmarshal([]byte(c.info), &info); err != nil {
				t.Fatalf("chat info: %v", err)
			}
			slugs := c.slugs
			if !c.useSlugs {
				slugs = chatSlugsForNew(&info, chatFlags{})
			}
			var buf bytes.Buffer
			printChatInfo(&buf, "scout", &info, slugs)
			c.check(t, buf.String())
		})
	}
}

// ---------- @<slug> in the conversation ----------

// chatBannerRow is the banner's row whose first field is name, or nil.
func chatBannerRow(t *testing.T, out, name string) []string {
	t.Helper()
	for _, row := range chatInfoRows(t, out) {
		if len(row) > 0 && row[0] == name {
			return row
		}
	}
	return nil
}

// chatREPLExit fails unless the conversation exited 0.
func chatREPLExit(t *testing.T, err error, errs string) {
	t.Helper()
	if err != nil {
		t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
	}
}

// TestChatREPLAtSlugNewThread: the line's @<slug> asks a member of the council the next thread will have.
func TestChatREPLAtSlugNewThread(t *testing.T) {
	cases := []struct {
		name    string
		info    string
		args    []string
		line    string
		created map[string]any
		turn    map[string]any
	}{
		{"a member of the default council", chatInfoV2, nil, "@coder fix it",
			map[string]any{"title": "fix it", "origin": "cli"},
			map[string]any{"prompt": "fix it", "member": "coder"}},
		{"a slug that is not a member of that council", chatInfoV2, nil, "@triage-sre why",
			map[string]any{"title": "@triage-sre why", "origin": "cli"},
			map[string]any{"prompt": "@triage-sre why"}},
		{"the lead is not a member `@` asks", chatInfoV2, nil, "@captain hi",
			map[string]any{"title": "@captain hi", "origin": "cli"},
			map[string]any{"prompt": "@captain hi"}},
		{"a member of --roster", chatInfoV2, []string{"--roster", "ops"}, "@triage-sre why",
			map[string]any{"title": "why", "origin": "cli", "roster": "ops"},
			map[string]any{"prompt": "why", "member": "triage-sre"}},
		{"a slug --roster does not have", chatInfoV2, []string{"--roster", "ops"}, "@coder fix",
			map[string]any{"title": "@coder fix", "origin": "cli", "roster": "ops"},
			map[string]any{"prompt": "@coder fix"}},
		{"a council's deleted member is no slug", chatInfoV2, []string{"--roster", "night"}, "@rol_AbCdEfGhIjKlMnOpQrStUv x",
			map[string]any{"title": "@rol_AbCdEfGhIjKlMnOpQrStUv x", "origin": "cli", "roster": "night"},
			map[string]any{"prompt": "@rol_AbCdEfGhIjKlMnOpQrStUv x"}},
		{"a chat with one member asks no @", chatInfoV2, []string{"--member", "fleet"}, "@coder x",
			map[string]any{"title": "@coder x", "origin": "cli", "member": "fleet"},
			map[string]any{"prompt": "@coder x", "member": "fleet"}},
		{"thorough, then one member", chatInfoV2, []string{"--thorough"}, "@fleet look",
			map[string]any{"title": "look", "origin": "cli"},
			map[string]any{"prompt": "look", "member": "fleet"}},
		{"an older hub asks no @ on a chat with one member", chatInfoBody, []string{"--member", "fleet"}, "@coder x",
			map[string]any{"title": "@coder x", "origin": "cli", "member": "fleet"},
			map[string]any{"prompt": "@coder x", "member": "fleet"}},
		{"a council block without the members", chatInfoCouncils, nil, "@fleet x",
			map[string]any{"title": "@fleet x", "origin": "cli"},
			map[string]any{"prompt": "@fleet x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			noBotMode(t)
			setTTY(t, true, true)
			p := newChatAPI(t)
			p.on("GET", "/bots/scout/chat", 200, c.info)
			chatNewThread(p)
			_, errs, err := chatREPLRun(t, p, c.line+"\n/quit\n", c.args...)
			chatREPLExit(t, err, errs)
			created := p.seen("POST", "/bots/scout/chat/threads")
			if len(created) != 1 {
				t.Fatalf("%d threads were created, want 1: %+v", len(created), p.reqs)
			}
			if got := chatBodyMap(t, created[0].Body); !reflect.DeepEqual(got, c.created) {
				t.Errorf("thread body %v, want %v", got, c.created)
			}
			turns := p.seen("POST", "/bots/scout/chat/threads/thr_new/turns")
			if len(turns) != 1 {
				t.Fatalf("%d turns, want 1", len(turns))
			}
			if got := chatBodyMap(t, turns[0].Body); !reflect.DeepEqual(got, c.turn) {
				t.Errorf("turn body %v, want %v", got, c.turn)
			}
		})
	}
}

// TestChatREPLAtSlugFromP4: on an existing thread, @<slug> asks a member of the council that thread reads as.
func TestChatREPLAtSlugFromP4(t *testing.T) {
	t.Run("the thread's council", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, true, true)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoV2)
		chatOldThread(p, chatDetailOn("thr_old", rosterOpsJSON, 42))
		_, errs, err := chatREPLRun(t, p, "@triage-sre why\n@captain hi\n@coder fix\n/quit\n", "--thread", "thr_old")
		chatREPLExit(t, err, errs)
		want := []map[string]any{
			{"prompt": "why", "member": "triage-sre"},
			{"prompt": "@captain hi"},
			{"prompt": "@coder fix"},
		}
		if got := chatTurnMaps(t, p, "thr_old"); !reflect.DeepEqual(got, want) {
			t.Errorf("turns %v, want %v", got, want)
		}
		order := []string{"GET /bots/scout/chat/threads/thr_old"}
		for range want {
			order = append(order, "GET /bots/scout/chat/threads/thr_old", "POST /bots/scout/chat/threads/thr_old/turns")
		}
		if got := chatRequestOrder(p); !slices.Equal(got, order) {
			t.Errorf("order %q, want %q", got, order)
		}
		if n := chatCountMethod(p, http.MethodPatch); n != 0 {
			t.Errorf("%d patches were sent", n)
		}
	})

	t.Run("each line reads the council again", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, true, true)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoV2)
		chatOldThread(p, chatDetailOn("thr_old", rosterOpsJSON, 42))
		p.onSeq("GET", "/bots/scout/chat/threads/thr_old",
			chatReply{200, chatDetailOn("thr_old", rosterOpsJSON, 42)},
			chatReply{200, chatDetailOn("thr_old", rosterOpsJSON, 42)},
			chatReply{200, chatDetailOn("thr_old", rosterStandardJSON, 43)})
		_, errs, err := chatREPLRun(t, p, "@triage-sre a\n@triage-sre b\n@coder c\n/quit\n", "--thread", "thr_old")
		chatREPLExit(t, err, errs)
		want := []map[string]any{
			{"prompt": "a", "member": "triage-sre"},
			{"prompt": "@triage-sre b"},
			{"prompt": "c", "member": "coder"},
		}
		if got := chatTurnMaps(t, p, "thr_old"); !reflect.DeepEqual(got, want) {
			t.Errorf("turns %v, want %v", got, want)
		}
	})

	t.Run("a thread made in this conversation", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, true, true)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoV2)
		chatNewThread(p)
		p.on("GET", "/bots/scout/chat/threads/thr_new", 200, chatDetailOn("thr_new", rosterOpsJSON, 7))
		_, errs, err := chatREPLRun(t, p, "hello\n@triage-sre why\n/quit\n")
		chatREPLExit(t, err, errs)
		wantOrder := []string{
			"POST /bots/scout/chat/threads",
			"POST /bots/scout/chat/threads/thr_new/turns",
			"GET /bots/scout/chat/threads/thr_new",
			"POST /bots/scout/chat/threads/thr_new/turns",
		}
		if got := chatRequestOrder(p); !slices.Equal(got, wantOrder) {
			t.Errorf("order %q, want %q", got, wantOrder)
		}
		want := []map[string]any{
			{"prompt": "hello"},
			{"prompt": "why", "member": "triage-sre"},
		}
		if got := chatTurnMaps(t, p, "thr_new"); !reflect.DeepEqual(got, want) {
			t.Errorf("turns %v, want %v", got, want)
		}
	})
}

// TestChatREPLAtSlugWithoutACouncil: a chat with one member, and a council the hub cannot find, ask no @.
func TestChatREPLAtSlugWithoutACouncil(t *testing.T) {
	cases := []struct {
		name   string
		detail string
	}{
		{"a chat with one member", chatMemberThreadFleet},
		{"a council the hub cannot find", chatDetailOn("thr_old", "null", 42)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			noBotMode(t)
			setTTY(t, true, true)
			p := newChatAPI(t)
			p.on("GET", "/bots/scout/chat", 200, chatInfoV2)
			chatOldThread(p, c.detail)
			_, errs, err := chatREPLRun(t, p, "@fleet look\n/quit\n", "--thread", "thr_old")
			chatREPLExit(t, err, errs)
			want := []map[string]any{{"prompt": "@fleet look"}}
			if got := chatTurnMaps(t, p, "thr_old"); !reflect.DeepEqual(got, want) {
				t.Errorf("turns %v, want %v", got, want)
			}
		})
	}
}

// An older hub keeps fleet and coder, even when the thread's read carries a council.
func TestChatREPLOldHubKeepsFleetAndCoder(t *testing.T) {
	noBotMode(t)
	setTTY(t, true, true)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	chatOldThread(p, chatDetailOn("thr_old", rosterOpsJSON, 42))
	out, errs, err := chatREPLRun(t, p, "@coder fix\n@triage-sre why\n/\n/quit\n", "--thread", "thr_old")
	chatREPLExit(t, err, errs)
	want := []map[string]any{
		{"prompt": "fix", "member": "coder"},
		{"prompt": "@triage-sre why"},
	}
	if got := chatTurnMaps(t, p, "thr_old"); !reflect.DeepEqual(got, want) {
		t.Errorf("turns %v, want %v", got, want)
	}
	if want := "> commands: /new /cancel /quit, @fleet, @coder\n"; !strings.Contains(out, want) {
		t.Errorf("stdout %q lacks %q", out, want)
	}
	if want := "scout (creator, online) — computer: tnctl/v0.3.0\n"; !strings.Contains(out, want) {
		t.Errorf("stdout %q lacks %q", out, want)
	}
	if strings.Contains(out, "council:") {
		t.Errorf("stdout %q names a council the older hub has not", out)
	}
	if strings.Contains(errs, "this conversation") {
		t.Errorf("stderr %q: an older hub has no session line", errs)
	}
}

// TestChatREPLEmptyAtSlug: "@<slug>" with no text asks nothing.
func TestChatREPLEmptyAtSlug(t *testing.T) {
	t.Run("a new thread", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, true, true)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoV2)
		chatNewThread(p)
		_, errs, err := chatREPLRun(t, p, "@fleet\n@fleet   \nhello\n/quit\n")
		chatREPLExit(t, err, errs)
		if n := strings.Count(errs, "· type the task for @fleet\n"); n != 2 {
			t.Errorf("%d hints, want 2 (stderr %q)", n, errs)
		}
		created := p.seen("POST", "/bots/scout/chat/threads")
		if len(created) != 1 {
			t.Fatalf("%d threads were created, want 1", len(created))
		}
		if got, want := chatBodyMap(t, created[0].Body), map[string]any{"title": "hello", "origin": "cli"}; !reflect.DeepEqual(got, want) {
			t.Errorf("thread body %v, want %v", got, want)
		}
		want := []map[string]any{{"prompt": "hello"}}
		if got := chatTurnMaps(t, p, "thr_new"); !reflect.DeepEqual(got, want) {
			t.Errorf("turns %v, want %v", got, want)
		}
	})

	t.Run("a thread", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, true, true)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoV2)
		chatOldThread(p, chatDetailOn("thr_old", rosterOpsJSON, 42))
		_, errs, err := chatREPLRun(t, p, "@fleet\n/quit\n", "--thread", "thr_old")
		chatREPLExit(t, err, errs)
		if !strings.Contains(errs, "· type the task for @fleet\n") {
			t.Errorf("stderr %q lacks the hint", errs)
		}
		want := []string{"GET /bots/scout/chat/threads/thr_old", "GET /bots/scout/chat/threads/thr_old"}
		if got := chatRequestOrder(p); !slices.Equal(got, want) {
			t.Errorf("order %q, want %q (the read at the start, then the line's)", got, want)
		}
	})

	t.Run("not a member", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, true, true)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoV2)
		chatNewThread(p)
		_, errs, err := chatREPLRun(t, p, "@triage-sre\n/quit\n")
		chatREPLExit(t, err, errs)
		if strings.Contains(errs, "type the task") {
			t.Errorf("stderr %q: the line was read as a member's", errs)
		}
		created := p.seen("POST", "/bots/scout/chat/threads")
		if len(created) != 1 {
			t.Fatalf("%d threads were created, want 1", len(created))
		}
		if got, want := chatBodyMap(t, created[0].Body), map[string]any{"title": "@triage-sre", "origin": "cli"}; !reflect.DeepEqual(got, want) {
			t.Errorf("thread body %v, want %v", got, want)
		}
		want := []map[string]any{{"prompt": "@triage-sre"}}
		if got := chatTurnMaps(t, p, "thr_new"); !reflect.DeepEqual(got, want) {
			t.Errorf("turns %v, want %v", got, want)
		}
	})
}

// TestChatREPLHelpLine: the help line names the members @ asks in the current chat, and sends nothing.
func TestChatREPLHelpLine(t *testing.T) {
	type row struct {
		name  string
		info  string
		args  []string
		stdin string
		line  string
		check func(t *testing.T, out string, p *chatAPI)
	}
	cases := []row{
		{"the default council", chatInfoV2, nil, "/\n/quit\n", "commands: /new /cancel /quit, @fleet, @coder",
			func(t *testing.T, out string, p *chatAPI) {
				if got, want := chatBannerRow(t, out, "Coder"), []string{"Coder", "@coder", "claude-sonnet-5-5", "high"}; !slices.Equal(got, want) {
					t.Errorf("Coder row %q, want %q", got, want)
				}
			}},
		{"--roster ops", chatInfoV2, []string{"--roster", "ops"}, "/help\n/quit\n", "commands: /new /cancel /quit, @fleet, @triage-sre, @stream-analyst",
			func(t *testing.T, out string, p *chatAPI) {
				if got, want := chatBannerRow(t, out, "Coder"), []string{"Coder", "member", "claude-sonnet-5-5", "high"}; !slices.Equal(got, want) {
					t.Errorf("Coder row %q, want %q", got, want)
				}
				fleet := chatBannerRow(t, out, "Fleet")
				if len(fleet) < 2 || fleet[1] != "@fleet" {
					t.Errorf("Fleet row %q, want @fleet", fleet)
				}
			}},
		{"--roster night", chatInfoV2, []string{"--roster", "night"}, "/\n/quit\n", "commands: /new /cancel /quit, @triage", nil},
		{"--member", chatInfoV2, []string{"--member", "fleet"}, "/\n/quit\n", "commands: /new /cancel /quit",
			func(t *testing.T, out string, p *chatAPI) {
				for _, name := range []string{"Fleet", "Coder"} {
					if row := chatBannerRow(t, out, name); len(row) < 2 || row[1] != "member" {
						t.Errorf("%s row %q, want member", name, row)
					}
				}
			}},
		{"an older hub, one member", chatInfoBody, []string{"--member", "fleet"}, "/\n/quit\n", "commands: /new /cancel /quit", nil},
		{"an older hub", chatInfoBody, nil, "/\n/quit\n", "commands: /new /cancel /quit, @fleet, @coder", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			noBotMode(t)
			setTTY(t, true, true)
			p := newChatAPI(t)
			p.on("GET", "/bots/scout/chat", 200, c.info)
			out, errs, err := chatREPLRun(t, p, c.stdin, c.args...)
			chatREPLExit(t, err, errs)
			if want := "> " + c.line + "\n"; !strings.Contains(out, want) {
				t.Errorf("stdout %q lacks %q", out, want)
			}
			if got := chatRequestOrder(p); len(got) != 0 {
				t.Errorf("requests %q, want none after the info", got)
			}
			if c.check != nil {
				c.check(t, out, p)
			}
		})
	}

	t.Run("/new changes the list back", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, true, true)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoV2)
		chatOldThread(p, chatDetailOn("thr_old", rosterOpsJSON, 42))
		out, errs, err := chatREPLRun(t, p, "/\n/new\n/\n/quit\n", "--thread", "thr_old")
		chatREPLExit(t, err, errs)
		first := strings.Index(out, "> commands: /new /cancel /quit, @fleet, @triage-sre, @stream-analyst\n")
		second := strings.Index(out, "> commands: /new /cancel /quit, @fleet, @coder\n")
		if first < 0 || second < 0 || second < first {
			t.Errorf("stdout %q: want the thread's list, then the new thread's", out)
		}
		want := []string{"GET /bots/scout/chat/threads/thr_old"}
		if got := chatRequestOrder(p); !slices.Equal(got, want) {
			t.Errorf("order %q, want %q", got, want)
		}
	})
}

// TestChatREPLThreadMemberChatRefusals: a chat with one member refuses the flags that cannot work before
// anything is printed or sent.
func TestChatREPLThreadMemberChatRefusals(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"another member", []string{"--thread", "thr_old", "--member", "coder"},
			"thread thr_old is a chat with fleet alone: leave out --member, or start a new chat without --thread"},
		{"thorough", []string{"--thread", "thr_old", "--thorough"},
			"thread thr_old is a chat with fleet alone: leave out --thorough, or start a new chat without --thread"},
		{"a council", []string{"--thread", "thr_old", "--roster", "ops"},
			"thread thr_old is a chat with fleet alone: leave out --roster, or start a new chat without --thread"},
		{"thorough before the council", []string{"--thread", "thr_old", "--thorough", "--roster", "ops"},
			"thread thr_old is a chat with fleet alone: leave out --thorough, or start a new chat without --thread"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			noBotMode(t)
			setTTY(t, true, true)
			p := newChatAPI(t)
			p.on("GET", "/bots/scout/chat", 200, chatInfoV2)
			chatOldThread(p, chatMemberThreadRunning)
			chatStreamTrn9(p)
			p.on("PATCH", "/bots/scout/chat/threads/thr_old", 200, chatThreadOnRoster("thr_old", "ops", "Ops"))
			out, errs, err := chatREPLRun(t, p, "/quit\n", c.args...)
			if err == nil || exitCodeOf(err) != codeUsage {
				t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
			}
			if err.Error() != c.want {
				t.Errorf("error %q, want %q", err, c.want)
			}
			if out != "" {
				t.Errorf("stdout %q, want nothing", out)
			}
			if errs != "" {
				t.Errorf("stderr %q, want nothing", errs)
			}
			if got, want := chatRequestOrder(p), []string{"GET /bots/scout/chat/threads/thr_old"}; !slices.Equal(got, want) {
				t.Errorf("order %q, want %q", got, want)
			}
			if n := len(p.seen("GET", "/bots/scout/chat/threads/thr_old/events")); n != 0 {
				t.Errorf("%d connections to the events stream, want none", n)
			}
			if n := chatCountMethod(p, http.MethodPatch); n != 0 {
				t.Errorf("%d patches, want none", n)
			}
		})
	}

	t.Run("the same member is not refused", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, true, true)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoV2)
		chatOldThread(p, chatMemberThreadFleet)
		_, errs, err := chatREPLRun(t, p, "look\n/quit\n", "--thread", "thr_old", "--member", "fleet")
		chatREPLExit(t, err, errs)
		want := []map[string]any{{"prompt": "look", "member": "fleet"}}
		if got := chatTurnMaps(t, p, "thr_old"); !reflect.DeepEqual(got, want) {
			t.Errorf("turns %v, want %v", got, want)
		}
	})
}

// TestChatREPLThreadRosterPatch: --thread --roster changes the thread's council once, before the prompts.
func TestChatREPLThreadRosterPatch(t *testing.T) {
	noBotMode(t)
	setTTY(t, true, true)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoV2)
	chatOldThread(p, chatDetailOn("thr_old", rosterOpsJSON, 42))
	p.onSeq("GET", "/bots/scout/chat/threads/thr_old",
		chatReply{200, chatDetailOn("thr_old", rosterOpsJSON, 42)},
		chatReply{200, chatDetailOn("thr_old", rosterNightJSON, 43)})
	p.on("PATCH", "/bots/scout/chat/threads/thr_old", 200, chatThreadOnRoster("thr_old", "night", "Night shift"))
	out, errs, err := chatREPLRun(t, p, "/\n@triage why\nagain\n/quit\n", "--thread", "thr_old", "--roster", "night")
	chatREPLExit(t, err, errs)

	patches := p.seen("PATCH", "/bots/scout/chat/threads/thr_old")
	if len(patches) != 1 {
		t.Fatalf("%d patches, want 1", len(patches))
	}
	if got, want := patches[0].Body, `{"roster":"night"}`; got != want {
		t.Errorf("patch body %q, want %q", got, want)
	}
	wantOrder := []string{
		"GET /bots/scout/chat/threads/thr_old",
		"PATCH /bots/scout/chat/threads/thr_old",
		"GET /bots/scout/chat/threads/thr_old",
		"POST /bots/scout/chat/threads/thr_old/turns",
		"GET /bots/scout/chat/threads/thr_old",
		"POST /bots/scout/chat/threads/thr_old/turns",
	}
	if got := chatRequestOrder(p); !slices.Equal(got, wantOrder) {
		t.Errorf("order %q, want %q", got, wantOrder)
	}
	threadAt := strings.Index(errs, "thread thr_old\n")
	if threadAt < 0 {
		t.Fatalf("stderr %q lacks the thread", errs)
	}
	nowAt := strings.Index(errs, "· this chat's council is now Night shift (night)\n")
	if nowAt < threadAt {
		t.Errorf("stderr %q: the change's line does not follow the thread's", errs)
	}
	if strings.Contains(errs, "this conversation") {
		t.Errorf("stderr %q: the change's own line names the council", errs)
	}
	if want := "> commands: /new /cancel /quit, @triage\n"; !strings.Contains(out, want) {
		t.Errorf("stdout %q lacks %q", out, want)
	}
	if got, want := chatBannerRow(t, out, "Fleet"), []string{"Fleet", "member", "claude-haiku-5-5", "medium"}; !slices.Equal(got, want) {
		t.Errorf("Fleet row %q, want %q", got, want)
	}
	unavailable := "· council Night shift (night) — unavailable: rol_AbCdEfGhIjKlMnOpQrStUv: this member was archived or deleted; replace it in this council.\n"
	unAt := strings.Index(errs, unavailable)
	if unAt < 0 || unAt < nowAt {
		t.Errorf("stderr %q: want %q after the change's line", errs, unavailable)
	}
	want := []map[string]any{
		{"prompt": "why", "member": "triage"},
		{"prompt": "again"},
	}
	if got := chatTurnMaps(t, p, "thr_old"); !reflect.DeepEqual(got, want) {
		t.Errorf("turns %v, want %v", got, want)
	}
}

// TestChatREPLPatchAfterTheOpenTurn: the thread's open turn is followed before its council changes.
func TestChatREPLPatchAfterTheOpenTurn(t *testing.T) {
	noBotMode(t)
	setTTY(t, true, true)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoV2)
	chatOldThread(p, chatDetailOn("thr_old", rosterOpsJSON, 5, chatTurnAt("trn_9", "thr_old", 1, "running", "")))
	chatStreamTrn9(p)
	p.on("PATCH", "/bots/scout/chat/threads/thr_old", 200, chatThreadOnRoster("thr_old", "night", "Night shift"))
	out, errs, err := chatREPLRun(t, p, "/quit\n", "--thread", "thr_old", "--roster", "night")
	chatREPLExit(t, err, errs)
	if !strings.Contains(out, "the turn finished") {
		t.Errorf("stdout %q lacks the followed turn's answer", out)
	}
	eventsAt, patchAt := -1, -1
	for i, r := range chatReqs(p) {
		switch {
		case r.Method == http.MethodGet && r.Path == "/bots/scout/chat/threads/thr_old/events" && eventsAt < 0:
			eventsAt = i
		case r.Method == http.MethodPatch && r.Path == "/bots/scout/chat/threads/thr_old" && patchAt < 0:
			patchAt = i
		}
	}
	if eventsAt < 0 || patchAt < 0 || eventsAt > patchAt {
		t.Errorf("requests %+v: want the stream at %d before the patch at %d", chatReqs(p), eventsAt, patchAt)
	}
	if n := len(p.seen("GET", "/bots/scout/chat/threads/thr_old")); n != 1 {
		t.Errorf("%d reads of the thread, want 1 (the follow uses the read at the start)", n)
	}
	if n := chatCountMethod(p, http.MethodPatch); n != 1 {
		t.Errorf("%d patches, want 1", n)
	}
	for _, r := range chatReqs(p) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/turns") {
			t.Errorf("a turn was posted: %s", r.Path)
		}
	}
}

// TestChatREPLPatchErrorEnds: a refused council change ends the conversation with its own words.
func TestChatREPLPatchErrorEnds(t *testing.T) {
	cases := []struct {
		name   string
		info   string
		status int
		body   string
		code   int
		want   string
	}{
		{"a turn still open", chatInfoV2, 409, councilErrBody("turn_open", "This chat has a turn in progress. Wait for it, or cancel it.", ""),
			codeConflict, "this chat already has a turn in progress (tnctl chat scout --thread thr_old --follow, then Ctrl-C cancels it)"},
		{"a council deleted after the info was read", chatInfoV2, 404, councilErrBody("unknown_roster", "There is no council 'night' for scout.", ""),
			codeUsage, "There is no council 'night' for scout.\ncouncils: standard, ops"},
		{"not the author", chatInfoV2, 403, councilErrBody("not_author", "Only the author of this chat or the bot's creator can change its council.", ""),
			codeAuth, "Only the author of this chat or the bot's creator can change its council."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			noBotMode(t)
			setTTY(t, true, true)
			p := newChatAPI(t)
			p.on("GET", "/bots/scout/chat", 200, c.info)
			chatOldThread(p, chatDetailOn("thr_old", rosterOpsJSON, 42))
			p.on("PATCH", "/bots/scout/chat/threads/thr_old", c.status, c.body)
			_, errs, err := chatREPLRun(t, p, "hello\n/quit\n", "--thread", "thr_old", "--roster", "night")
			if err == nil || exitCodeOf(err) != c.code {
				t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
			}
			if err.Error() != c.want {
				t.Errorf("error %q, want %q", err, c.want)
			}
			if n := chatCountMethod(p, http.MethodPatch); n != 1 {
				t.Errorf("%d patches, want 1", n)
			}
			want := []string{"GET /bots/scout/chat/threads/thr_old", "PATCH /bots/scout/chat/threads/thr_old"}
			if got := chatRequestOrder(p); !slices.Equal(got, want) {
				t.Errorf("order %q, want %q (no read and no turn after the refusal)", got, want)
			}
			if !strings.Contains(errs, "thread thr_old\n") {
				t.Errorf("stderr %q lacks the thread", errs)
			}
			if strings.Contains(errs, "council is now") || strings.Contains(errs, "Error:") {
				t.Errorf("stderr %q: the refusal is not the REPL's to print", errs)
			}
		})
	}

	t.Run("an older hub", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, true, true)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
		chatOldThread(p, chatDetailOn("thr_old", rosterOpsJSON, 42))
		p.on("PATCH", "/bots/scout/chat/threads/thr_old", 404, "")
		_, errs, err := chatREPLRun(t, p, "hello\n/quit\n", "--thread", "thr_old", "--roster", "night")
		if err == nil || exitCodeOf(err) != codeNotFound {
			t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
		}
		if !strings.Contains(err.Error(), "HTTP 404") {
			t.Errorf("error %q, want the platform's 404", err)
		}
		if n := chatCountMethod(p, http.MethodPatch); n != 1 {
			t.Errorf("%d patches, want 1", n)
		}
		want := []string{"GET /bots/scout/chat/threads/thr_old", "PATCH /bots/scout/chat/threads/thr_old"}
		if got := chatRequestOrder(p); !slices.Equal(got, want) {
			t.Errorf("order %q, want %q (no read and no turn after the refusal)", got, want)
		}
		if !strings.Contains(errs, "thread thr_old\n") {
			t.Errorf("stderr %q lacks the thread", errs)
		}
		if strings.Contains(errs, "council is now") || strings.Contains(errs, "Error:") {
			t.Errorf("stderr %q: the refusal is not the REPL's to print", errs)
		}
	})
}

// TestChatREPLNewKeepsRoster: /new makes the next thread on the same council.
func TestChatREPLNewKeepsRoster(t *testing.T) {
	noBotMode(t)
	setTTY(t, true, true)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoV2)
	chatOldThread(p, chatDetailOn("thr_old", rosterOpsJSON, 42))
	p.on("PATCH", "/bots/scout/chat/threads/thr_old", 200, chatThreadOnRoster("thr_old", "night", "Night shift"))
	chatNewThread(p)
	_, errs, err := chatREPLRun(t, p, "/new\n@triage hello\n/quit\n", "--thread", "thr_old", "--roster", "night")
	chatREPLExit(t, err, errs)
	if n := chatCountMethod(p, http.MethodPatch); n != 1 {
		t.Errorf("%d patches, want 1 (on thr_old)", n)
	}
	if n := len(p.seen("PATCH", "/bots/scout/chat/threads/thr_old")); n != 1 {
		t.Errorf("%d patches on thr_old, want 1", n)
	}
	created := p.seen("POST", "/bots/scout/chat/threads")
	if len(created) != 1 {
		t.Fatalf("%d threads were created, want 1", len(created))
	}
	if got, want := chatBodyMap(t, created[0].Body), map[string]any{"title": "hello", "origin": "cli", "roster": "night"}; !reflect.DeepEqual(got, want) {
		t.Errorf("thread body %v, want %v", got, want)
	}
	want := []map[string]any{{"prompt": "hello", "member": "triage"}}
	if got := chatTurnMaps(t, p, "thr_new"); !reflect.DeepEqual(got, want) {
		t.Errorf("turns on thr_new %v, want %v", got, want)
	}
	if n := len(p.seen("POST", "/bots/scout/chat/threads/thr_old/turns")); n != 0 {
		t.Errorf("%d turns on thr_old, want none", n)
	}
	if want := "· a new thread starts at your next message, with council Night shift (night)\n"; !strings.Contains(errs, want) {
		t.Errorf("stderr %q lacks %q", errs, want)
	}
}

// TestChatREPLThreadRefusalsEnd: a refusal of a new thread that comes from the session's flags ends the
// conversation; any other is printed and the conversation goes on.
func TestChatREPLThreadRefusalsEnd(t *testing.T) {
	ended := []struct {
		name   string
		args   []string
		status int
		body   string
		code   int
		want   string
	}{
		{"an ambiguous member", []string{"--member", "triage"}, 400,
			councilErrBody("ambiguous", "More than one member is called 'triage' (teamspace, bot): name the scope.", ""), codeUsage,
			"More than one member is called 'triage' (teamspace, bot): name the scope."},
		{"an unknown member", []string{"--member", "triage"}, 400,
			councilErrBody("unknown_member", "'triage' is not a member of this council. Members: fleet, coder.", ""), codeUsage,
			"'triage' is not a member of this council. Members: fleet, coder."},
		{"an unknown member, answered 409", []string{"--member", "triage"}, 409,
			councilErrBody("unknown_member", "'triage' is not a member of this council. Members: fleet, coder.", ""), codeUsage,
			"'triage' is not a member of this council. Members: fleet, coder."},
		{"a member only the creator may use", []string{"--member", "triage"}, 409,
			councilErrBody("creator_only", "scout acts as its creator, and Triage was last changed by bob@example.com: at Act as me it runs only members and councils its creator wrote. Clone it to this bot, or ask its creator.", ""), codeError,
			"scout acts as its creator, and Triage was last changed by bob@example.com: at Act as me it runs only members and councils its creator wrote. Clone it to this bot, or ask its creator."},
		{"a council deleted after the info was read", []string{"--roster", "night"}, 404,
			councilErrBody("unknown_roster", "There is no council 'night' for scout.", ""), codeUsage,
			"There is no council 'night' for scout.\ncouncils: standard, ops"},
	}
	for _, c := range ended {
		t.Run(c.name, func(t *testing.T) {
			noBotMode(t)
			setTTY(t, true, true)
			p := newChatAPI(t)
			p.on("GET", "/bots/scout/chat", 200, chatInfoV2)
			p.on("POST", "/bots/scout/chat/threads", c.status, c.body)
			_, errs, err := chatREPLRun(t, p, "hello\nagain\n/quit\n", c.args...)
			if err == nil || exitCodeOf(err) != c.code {
				t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
			}
			if err.Error() != c.want {
				t.Errorf("error %q, want %q", err, c.want)
			}
			if strings.Contains(errs, "Error:") {
				t.Errorf("stderr %q: the REPL printed the refusal", errs)
			}
			if n := len(p.seen("POST", "/bots/scout/chat/threads")); n != 1 {
				t.Errorf("%d threads were created, want 1", n)
			}
			for _, r := range chatReqs(p) {
				if r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/turns") {
					t.Errorf("a turn was posted: %s", r.Path)
				}
			}
		})
	}

	t.Run("a member that went meanwhile", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, true, true)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoV2)
		p.on("POST", "/bots/scout/chat/threads", 409,
			councilErrBody("role_gone", "The member 'triage' was archived or deleted; start a new chat with another member.", ""))
		_, errs, err := chatREPLRun(t, p, "hello\nagain\n/quit\n", "--member", "triage")
		chatREPLExit(t, err, errs)
		if n := len(p.seen("POST", "/bots/scout/chat/threads")); n != 2 {
			t.Errorf("%d threads were created, want 2 (the prompt is tried again)", n)
		}
		if n := strings.Count(errs, "Error: The member 'triage' was archived or deleted; start a new chat with another member.\n"); n != 2 {
			t.Errorf("%d lines of the refusal, want 2 (stderr %q)", n, errs)
		}
		for _, r := range chatReqs(p) {
			if r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/turns") {
				t.Errorf("a turn was posted: %s", r.Path)
			}
		}
	})

	t.Run("a failure of the platform", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, true, true)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoV2)
		p.on("POST", "/bots/scout/chat/threads", 500, `{"message":"boom"}`)
		_, errs, err := chatREPLRun(t, p, "hello\nagain\n/quit\n")
		chatREPLExit(t, err, errs)
		if n := len(p.seen("POST", "/bots/scout/chat/threads")); n != 2 {
			t.Errorf("%d threads were created, want 2", n)
		}
		if n := strings.Count(errs, "Error: "); n != 2 {
			t.Errorf("%d error lines, want 2 (stderr %q)", n, errs)
		}
		if n := strings.Count(errs, "boom"); n != 2 {
			t.Errorf("%d lines with the platform's words, want 2 (stderr %q)", n, errs)
		}
		for _, r := range chatReqs(p) {
			if r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/turns") {
				t.Errorf("a turn was posted: %s", r.Path)
			}
		}
	})
}

// TestChatREPLRosterNotListed: a --roster the bot's councils do not list is refused before anything.
func TestChatREPLRosterNotListed(t *testing.T) {
	const want = "There is no council 'nope' for scout.\ncouncils: standard, ops, night"
	t.Run("a new chat", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, true, true)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoV2)
		chatNewThread(p)
		out, errs, err := chatREPLRun(t, p, "hello\n/quit\n", "--roster", "nope")
		if err == nil || exitCodeOf(err) != codeUsage {
			t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
		}
		if err.Error() != want {
			t.Errorf("error %q, want %q", err, want)
		}
		if out != "" || errs != "" {
			t.Errorf("stdout %q, stderr %q, want nothing", out, errs)
		}
		if got := chatRequestOrder(p); len(got) != 0 {
			t.Errorf("requests %q, want none after the info", got)
		}
	})

	t.Run("a thread", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, true, true)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoV2)
		chatOldThread(p, chatDetailOn("thr_old", rosterOpsJSON, 42))
		p.on("PATCH", "/bots/scout/chat/threads/thr_old", 200, chatThreadOnRoster("thr_old", "nope", "Nope"))
		out, errs, err := chatREPLRun(t, p, "hello\n/quit\n", "--thread", "thr_old", "--roster", "nope")
		if err == nil || exitCodeOf(err) != codeUsage {
			t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
		}
		if err.Error() != want {
			t.Errorf("error %q, want %q", err, want)
		}
		if out != "" || errs != "" {
			t.Errorf("stdout %q, stderr %q, want nothing", out, errs)
		}
		if got := chatRequestOrder(p); len(got) != 0 {
			t.Errorf("requests %q, want none (no read of the thread, no patch)", got)
		}
	})

	t.Run("an older hub decides at P3", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, true, true)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
		chatNewThread(p)
		_, errs, err := chatREPLRun(t, p, "/quit\n", "--roster", "nope")
		chatREPLExit(t, err, errs)
		if n := len(p.seen("POST", "/bots/scout/chat/threads")); n != 0 {
			t.Errorf("%d threads were created, want none", n)
		}
	})
}

// TestChatREPLSessionLine: before the first prompt the conversation says what it asks, when that is not the
// bot's default council.
func TestChatREPLSessionLine(t *testing.T) {
	cases := []struct {
		name  string
		setup func(p *chatAPI)
		args  []string
		line  string // "" says no session line
		order string // the stderr text the line must follow, "" for none
	}{
		{"the default council", nil, nil, "", ""},
		{"the default council, by name", nil, []string{"--roster", "standard"}, "", ""},
		{"another council", nil, []string{"--roster", "ops"},
			"· this conversation asks council Ops (ops)", ""},
		{"a council that cannot answer", nil, []string{"--roster", "night"},
			"· this conversation asks council Night shift (night) — unavailable: rol_AbCdEfGhIjKlMnOpQrStUv: this member was archived or deleted; replace it in this council.", ""},
		{"one member", nil, []string{"--member", "triage"},
			"· this conversation asks triage alone", ""},
		{"the thread's council", func(p *chatAPI) { chatOldThread(p, chatDetailOn("thr_old", rosterOpsJSON, 42)) },
			[]string{"--thread", "thr_old"}, "· this conversation asks council Ops (ops)", "thread thr_old\n"},
		{"the thread's default council", func(p *chatAPI) { chatOldThread(p, chatDetailOn("thr_old", rosterStandardJSON, 42)) },
			[]string{"--thread", "thr_old"}, "", ""},
		{"a thread with one member", func(p *chatAPI) { chatOldThread(p, chatMemberThreadFleet) },
			[]string{"--thread", "thr_old"}, "· this conversation asks fleet alone", ""},
		{"a thread whose council is gone", func(p *chatAPI) { chatOldThread(p, chatDetailOn("thr_old", "null", 42)) },
			[]string{"--thread", "thr_old"}, "· this conversation's council is gone: pick another with --roster", ""},
		{"a member of the thread", func(p *chatAPI) { chatOldThread(p, chatDetailOn("thr_old", rosterOpsJSON, 42)) },
			[]string{"--thread", "thr_old", "--member", "triage-sre"}, "· this conversation asks triage-sre alone", ""},
		{"a member of a thread whose council is gone", func(p *chatAPI) { chatOldThread(p, chatDetailOn("thr_old", "null", 42)) },
			[]string{"--thread", "thr_old", "--member", "fleet"}, "· this conversation's council is gone: leave out --member and pick another with --roster", ""},
		{"a member of a council that cannot answer", func(p *chatAPI) {
			chatOldThread(p, chatDetailOn("thr_old", strings.Replace(rosterNightJSON, `"members":["triage","rol_AbCdEfGhIjKlMnOpQrStUv"]`, `"members":["fleet","rol_AbCdEfGhIjKlMnOpQrStUv"]`, 1), 42))
		}, []string{"--thread", "thr_old", "--member", "fleet"},
			"· this conversation asks fleet alone, on council Night shift (night) — unavailable: rol_AbCdEfGhIjKlMnOpQrStUv: this member was archived or deleted; replace it in this council.", ""},
		{"the change's own line names the council", func(p *chatAPI) {
			chatOldThread(p, chatDetailOn("thr_old", rosterOpsJSON, 42))
			p.on("PATCH", "/bots/scout/chat/threads/thr_old", 200, chatThreadOnRoster("thr_old", "night", "Night shift"))
		}, []string{"--thread", "thr_old", "--roster", "night"}, "", ""},
		{"the change from a council that is gone", func(p *chatAPI) {
			chatOldThread(p, chatDetailOn("thr_old", "null", 42))
			p.on("PATCH", "/bots/scout/chat/threads/thr_old", 200, chatThreadOnRoster("thr_old", "ops", "Ops"))
		}, []string{"--thread", "thr_old", "--roster", "ops"}, "", ""},
		{"the change to a council that can answer", func(p *chatAPI) {
			chatOldThread(p, chatDetailOn("thr_old", rosterStandardJSON, 42))
			p.on("PATCH", "/bots/scout/chat/threads/thr_old", 200, chatThreadOnRoster("thr_old", "ops", "Ops"))
		}, []string{"--thread", "thr_old", "--roster", "ops"}, "", ""},
		{"an older hub", nil, []string{"--roster", "ops"}, "", ""},
		{"an older hub, a thread with one member", func(p *chatAPI) { chatOldThread(p, chatMemberThreadFleet) },
			[]string{"--thread", "thr_old"}, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			noBotMode(t)
			setTTY(t, true, true)
			p := newChatAPI(t)
			info := chatInfoV2
			if strings.HasPrefix(c.name, "an older hub") {
				info = chatInfoBody
			}
			p.on("GET", "/bots/scout/chat", 200, info)
			if c.setup != nil {
				c.setup(p)
			}
			_, errs, err := chatREPLRun(t, p, "/quit\n", c.args...)
			chatREPLExit(t, err, errs)
			if c.line == "" {
				if strings.Contains(errs, "· this conversation") {
					t.Errorf("stderr %q: no session line wanted", errs)
				}
			} else if n := strings.Count(errs, c.line+"\n"); n != 1 {
				t.Errorf("%d session lines, want 1 (stderr %q)", n, errs)
			}
			if c.order != "" && c.line != "" {
				at, before := strings.Index(errs, c.line+"\n"), strings.Index(errs, c.order)
				if before < 0 || at < before {
					t.Errorf("stderr %q: want the session line after %q", errs, c.order)
				}
			}
			if strings.Contains(errs, "· council ") && strings.HasSuffix(c.name, "can answer") {
				t.Errorf("stderr %q: a council that can answer needs no reason", errs)
			}
		})
	}

	// The /new line says what the next thread asks.
	newCases := []struct {
		name  string
		info  string
		setup func(p *chatAPI)
		args  []string
		want  string
		none  string
	}{
		{"the thread's council, then the default", chatInfoV2,
			func(p *chatAPI) { chatOldThread(p, chatDetailOn("thr_old", rosterOpsJSON, 42)) },
			[]string{"--thread", "thr_old"},
			"· a new thread starts at your next message, with council Standard (standard)\n", ""},
		{"a council", chatInfoV2, nil, []string{"--roster", "ops"},
			"· a new thread starts at your next message, with council Ops (ops)\n", ""},
		{"one member", chatInfoV2, nil, []string{"--member", "triage"},
			"· a new thread starts at your next message, with triage alone\n", ""},
		{"an older hub", chatInfoBody, nil, nil,
			"· a new thread starts at your next message\n", "with council"},
	}
	for _, c := range newCases {
		t.Run("/new: "+c.name, func(t *testing.T) {
			noBotMode(t)
			setTTY(t, true, true)
			p := newChatAPI(t)
			p.on("GET", "/bots/scout/chat", 200, c.info)
			if c.setup != nil {
				c.setup(p)
			}
			_, errs, err := chatREPLRun(t, p, "/new\n/quit\n", c.args...)
			chatREPLExit(t, err, errs)
			if !strings.Contains(errs, c.want) {
				t.Errorf("stderr %q lacks %q", errs, c.want)
			}
			if c.none != "" && strings.Contains(errs, c.none) {
				t.Errorf("stderr %q names a council the hub does not", errs)
			}
		})
	}
}

// TestChatREPLThreadMemberNotInCouncil: on a council chat, a --member its council does not have is refused
// before anything is printed or sent (every prompt would ask that member alone, and every one be refused).
func TestChatREPLThreadMemberNotInCouncil(t *testing.T) {
	t.Run("a member the council does not have", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, true, true)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoV2)
		chatOldThread(p, chatDetailOn("thr_old", rosterOpsJSON, 42))
		out, errs, err := chatREPLRun(t, p, "hello\n/quit\n", "--thread", "thr_old", "--member", "coder")
		if err == nil || exitCodeOf(err) != codeUsage {
			t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
		}
		want := "thread thr_old asks council Ops (ops), which has no member coder: leave out --member, or start a new chat without --thread\nmembers: fleet, triage-sre, stream-analyst"
		if err.Error() != want {
			t.Errorf("error %q, want %q", err, want)
		}
		if out != "" || errs != "" {
			t.Errorf("stdout %q, stderr %q, want nothing", out, errs)
		}
		if got, want := chatRequestOrder(p), []string{"GET /bots/scout/chat/threads/thr_old"}; !slices.Equal(got, want) {
			t.Errorf("order %q, want %q", got, want)
		}
	})
	t.Run("a council without members", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, true, true)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoV2)
		empty := strings.Replace(rosterOpsJSON, `"members":["fleet","triage-sre","stream-analyst"]`, `"members":["rol_AbCdEfGhIjKlMnOpQrStUv"]`, 1)
		chatOldThread(p, chatDetailOn("thr_old", empty, 42))
		_, errs, err := chatREPLRun(t, p, "/quit\n", "--thread", "thr_old", "--member", "coder")
		if err == nil || exitCodeOf(err) != codeUsage {
			t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
		}
		if !strings.HasSuffix(err.Error(), "\nmembers: none") {
			t.Errorf("error %q, want the members line to say none", err)
		}
	})
	// The lead and the reviewer are not ordinary members: the hub refuses --member for them at each turn.
	for _, slug := range []string{"captain", "critic"} {
		t.Run("the council's "+slug, func(t *testing.T) {
			noBotMode(t)
			setTTY(t, true, true)
			p := newChatAPI(t)
			p.on("GET", "/bots/scout/chat", 200, chatInfoV2)
			chatOldThread(p, chatDetailOn("thr_old", rosterOpsJSON, 42))
			out, errs, err := chatREPLRun(t, p, "/quit\n", "--thread", "thr_old", "--member", slug)
			if err == nil || exitCodeOf(err) != codeUsage {
				t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
			}
			want := "thread thr_old asks council Ops (ops), which has no member " + slug + ": leave out --member, or start a new chat without --thread\nmembers: fleet, triage-sre, stream-analyst"
			if err.Error() != want {
				t.Errorf("error %q, want %q", err, want)
			}
			if out != "" || errs != "" {
				t.Errorf("stdout %q, stderr %q, want nothing", out, errs)
			}
		})
	}
	// Not refused: a member the council has, a council the hub cannot find (the hub says why at the first
	// turn), and an older hub (its threads carry no council).
	for _, c := range []struct {
		name, info, detail, member string
	}{
		{"a member the council has", chatInfoV2, chatDetailOn("thr_old", rosterOpsJSON, 42), "triage-sre"},
		{"a council that is gone", chatInfoV2, chatDetailOn("thr_old", "null", 42), "coder"},
		{"an older hub", chatInfoBody, chatDetailOn("thr_old", rosterOpsJSON, 42), "coder"},
	} {
		t.Run(c.name, func(t *testing.T) {
			noBotMode(t)
			setTTY(t, true, true)
			p := newChatAPI(t)
			p.on("GET", "/bots/scout/chat", 200, c.info)
			chatOldThread(p, c.detail)
			_, errs, err := chatREPLRun(t, p, "look\n/quit\n", "--thread", "thr_old", "--member", c.member)
			chatREPLExit(t, err, errs)
			want := []map[string]any{{"prompt": "look", "member": c.member}}
			if got := chatTurnMaps(t, p, "thr_old"); !reflect.DeepEqual(got, want) {
				t.Errorf("turns %v, want %v", got, want)
			}
		})
	}
}

// TestChatREPLAuthEnds: a refusal that says the person may no longer chat ends the conversation, wherever it
// comes from, and is returned, not printed (its caller prints it once).
func TestChatREPLAuthEnds(t *testing.T) {
	const notAllowed = `{"code":"chat_not_allowed","message":"You may no longer chat with scout."}`
	noTurns := func(t *testing.T, p *chatAPI) {
		t.Helper()
		for _, r := range chatReqs(p) {
			if r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/turns") {
				t.Errorf("a turn was posted: %s", r.Path)
			}
		}
	}
	ends := func(t *testing.T, err error, errs string) {
		t.Helper()
		if err == nil || exitCodeOf(err) != codeAuth {
			t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
		}
		if strings.Contains(errs, "Error:") {
			t.Errorf("stderr %q: the REPL printed the refusal it returns", errs)
		}
	}

	t.Run("the turn", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, true, true)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoV2)
		chatOldThread(p, chatDetailOn("thr_old", rosterStandardJSON, 42))
		p.on("POST", "/bots/scout/chat/threads/thr_old/turns", 403, notAllowed)
		_, errs, err := chatREPLRun(t, p, "hello\nagain\n/quit\n", "--thread", "thr_old")
		ends(t, err, errs)
		if want := "You may no longer chat with scout."; err.Error() != want {
			t.Errorf("error %q, want %q", err, want)
		}
		if n := len(p.seen("POST", "/bots/scout/chat/threads/thr_old/turns")); n != 1 {
			t.Errorf("%d turns posted, want 1 (the conversation ends)", n)
		}
	})
	t.Run("access lost during a turn", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, true, true)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
		p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
		p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
		// A conversation that went on would read the thread and post the next line.
		p.on("GET", "/bots/scout/chat/threads/thr_new", 200, chatThreadDetail("thr_new", 0))
		p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
			writeFrames(w, []string{frameOf("", 0, "error", `"code":"access_lost","message":"Your access to scout was removed."`)})
		})
		_, errs, err := chatREPLRun(t, p, "hello\nagain\n")
		ends(t, err, errs)
		if !strings.Contains(err.Error(), "Your access to scout was removed.") {
			t.Errorf("error %q lacks the platform's words", err)
		}
		if n := len(p.seen("POST", "/bots/scout/chat/threads/thr_new/turns")); n != 1 {
			t.Errorf("%d turns posted, want 1", n)
		}
		if n := len(p.seen("GET", "/bots/scout/chat/threads/thr_new")); n != 0 {
			t.Errorf("%d reads of the thread after access was lost, want none", n)
		}
	})
	t.Run("access lost while following the open turn", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, true, true)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
		p.on("GET", "/bots/scout/chat/threads/thr_a", 200, chatThreadDetail("thr_a", 5, chatTurnAt("trn_9", "thr_a", 1, "running", "")))
		p.on("POST", "/bots/scout/chat/threads/thr_a/turns", 202, chatTurnBody)
		// A stream-level error (no turn id): a frame with the turn's id is that turn's own error event.
		p.onStream("/bots/scout/chat/threads/thr_a/events", func(w http.ResponseWriter, r *http.Request, conn int) {
			writeFrames(w, []string{frameOf("", 0, "error", `"code":"access_lost","message":"gone"`)})
		})
		out, errs, err := chatREPLRun(t, p, "hello\n", "--thread", "thr_a")
		ends(t, err, errs)
		if err.Error() != "gone" {
			t.Errorf("error %q, want %q", err, "gone")
		}
		if strings.Contains(out, "> ") {
			t.Errorf("stdout %q: a prompt after access was lost", out)
		}
		noTurns(t, p)
	})
	t.Run("the new thread", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, true, true)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoV2)
		p.on("POST", "/bots/scout/chat/threads", 403, notAllowed)
		_, errs, err := chatREPLRun(t, p, "hello\nagain\n/quit\n")
		ends(t, err, errs)
		if n := len(p.seen("POST", "/bots/scout/chat/threads")); n != 1 {
			t.Errorf("%d threads asked for, want 1", n)
		}
		noTurns(t, p)
	})
	t.Run("a line's read of the thread", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, true, true)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoV2)
		chatOldThread(p, chatDetailOn("thr_old", rosterStandardJSON, 42))
		p.onSeq("GET", "/bots/scout/chat/threads/thr_old",
			chatReply{200, chatDetailOn("thr_old", rosterStandardJSON, 42)},
			chatReply{403, notAllowed})
		_, errs, err := chatREPLRun(t, p, "hello\nagain\n/quit\n", "--thread", "thr_old")
		ends(t, err, errs)
		if n := len(p.seen("GET", "/bots/scout/chat/threads/thr_old")); n != 2 {
			t.Errorf("%d reads of the thread, want 2 (the start, then the first line)", n)
		}
		noTurns(t, p)
	})
}

// TestChatREPLFollowSecondCtrlCLeaves: two Ctrl-Cs while the conversation follows the thread's open turn
// at the start leave it, as they do during a turn of the conversation.
func TestChatREPLFollowSecondCtrlCLeaves(t *testing.T) {
	noBotMode(t)
	setTTY(t, true, true)
	oldGrace := chatCancelGrace
	chatCancelGrace = time.Minute // the grace must not be what ends this
	t.Cleanup(func() { chatCancelGrace = oldGrace })
	sigs := make(chan os.Signal, 2)
	old := chatSignals
	chatSignals = func() (<-chan os.Signal, func()) { return sigs, func() {} }
	t.Cleanup(func() { chatSignals = old })

	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("GET", "/bots/scout/chat/threads/thr_a", 200, chatThreadDetail("thr_a", 5, chatTurnAt("trn_9", "thr_a", 1, "running", "")))
	p.on("POST", "/bots/scout/chat/threads/thr_a/turns/trn_9/cancel", 202, chatTurnBody)
	p.onStream("/bots/scout/chat/threads/thr_a/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, ": open\n\n")
		w.(http.Flusher).Flush()
		if conn == 1 {
			sigs <- os.Interrupt
			sigs <- os.Interrupt
		}
		<-r.Context().Done() // the turn never ends
	})
	out, errs, err := chatREPLRun(t, p, "/quit\n", "--thread", "thr_a")
	if !errors.Is(err, errChatInterrupted) {
		t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
	}
	if strings.Contains(out, "> ") {
		t.Errorf("stdout %q: a prompt after two Ctrl-Cs", out)
	}
	if strings.Contains(errs, "Error:") {
		t.Errorf("stderr %q: the REPL printed the error it returns", errs)
	}
}

// TestChatREPLLineReadFails: a line whose read of the thread fails is not sent; the error is said and the
// conversation goes on.
func TestChatREPLLineReadFails(t *testing.T) {
	noBotMode(t)
	setTTY(t, true, true)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoV2)
	chatNewThread(p)
	// The first line makes the thread without reading it: the first read is the second line's.
	p.onSeq("GET", "/bots/scout/chat/threads/thr_new",
		chatReply{500, `{"code":"internal","message":"the platform is briefly down"}`},
		chatReply{200, chatDetailOn("thr_new", rosterStandardJSON, 2)})
	_, errs, err := chatREPLRun(t, p, "hello\nsecond\nthird\n/quit\n")
	chatREPLExit(t, err, errs)
	if n := strings.Count(errs, "Error: "); n != 1 || !strings.Contains(errs, "the platform is briefly down") {
		t.Errorf("stderr %q: want one error line with the platform's words", errs)
	}
	want := []map[string]any{{"prompt": "hello"}, {"prompt": "third"}}
	if got := chatTurnMaps(t, p, "thr_new"); !reflect.DeepEqual(got, want) {
		t.Errorf("turns %v, want %v", got, want)
	}
}

// TestChatREPLHubTextIsCleaned: hub text in the lines the conversation says on stderr, and in its refusals,
// reaches the terminal without a control character.
func TestChatREPLHubTextIsCleaned(t *testing.T) {
	dirtyMember := strings.Replace(chatMemberThreadFleet, `"member":"fleet"`, `"member":"fl\neet\u001b[0m"`, 1)
	dirtyOps := strings.Replace(rosterOpsJSON, `"displayName":"Ops"`, `"displayName":"O\u001b[2Jps"`, 1)
	dirtyNight := strings.Replace(strings.Replace(rosterNightJSON, `"displayName":"Night shift"`, `"displayName":"Night\u001b[31m shift"`, 1),
		`"reasons":["rol_`, `"reasons":["\u001b[2Jrol_`, 1)
	infoDirtyNight := strings.Replace(chatInfoV2, rosterNightJSON, dirtyNight, 1)
	if dirtyMember == chatMemberThreadFleet || dirtyOps == rosterOpsJSON || dirtyNight == rosterNightJSON || infoDirtyNight == chatInfoV2 {
		t.Fatal("a fixture was not made dirty")
	}
	cases := []struct {
		name  string
		info  string
		setup func(p *chatAPI)
		stdin string
		args  []string
		err   string // the exact error, "" for exit 0
		line  string // a line stderr must hold
	}{
		{"a thread with one member", chatInfoV2, func(p *chatAPI) { chatOldThread(p, dirtyMember) }, "/quit\n",
			[]string{"--thread", "thr_old"}, "", "· this conversation asks fl\\neet alone\n"},
		{"the refusal of another member", chatInfoV2, func(p *chatAPI) { chatOldThread(p, dirtyMember) }, "/quit\n",
			[]string{"--thread", "thr_old", "--member", "coder"},
			`thread thr_old is a chat with fl\neet alone: leave out --member, or start a new chat without --thread`, ""},
		{"the thread's council", chatInfoV2, func(p *chatAPI) { chatOldThread(p, chatDetailOn("thr_old", dirtyOps, 42)) }, "/quit\n",
			[]string{"--thread", "thr_old"}, "", "· this conversation asks council Ops (ops)\n"},
		{"the refusal of a member the council does not have", chatInfoV2,
			func(p *chatAPI) { chatOldThread(p, chatDetailOn("thr_old", dirtyOps, 42)) }, "/quit\n",
			[]string{"--thread", "thr_old", "--member", "coder"},
			"thread thr_old asks council Ops (ops), which has no member coder: leave out --member, or start a new chat without --thread\nmembers: fleet, triage-sre, stream-analyst", ""},
		{"the council after the change", infoDirtyNight, func(p *chatAPI) {
			chatOldThread(p, chatDetailOn("thr_old", rosterOpsJSON, 42))
			p.on("PATCH", "/bots/scout/chat/threads/thr_old", 200, chatThreadOnRoster("thr_old", "night", "Night shift"))
		}, "/quit\n", []string{"--thread", "thr_old", "--roster", "night"}, "",
			"· council Night shift (night) — unavailable: rol_AbCdEfGhIjKlMnOpQrStUv: this member was archived or deleted; replace it in this council.\n"},
		{"the /new line", infoDirtyNight, nil, "/new\n/quit\n", []string{"--roster", "night"}, "",
			"· a new thread starts at your next message, with council Night shift (night)\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			noBotMode(t)
			setTTY(t, true, true)
			p := newChatAPI(t)
			p.on("GET", "/bots/scout/chat", 200, c.info)
			if c.setup != nil {
				c.setup(p)
			}
			out, errs, err := chatREPLRun(t, p, c.stdin, c.args...)
			text := out + errs
			if c.err == "" {
				chatREPLExit(t, err, errs)
			} else {
				if err == nil || err.Error() != c.err {
					t.Fatalf("error %v, want %q", err, c.err)
				}
				text += err.Error()
			}
			if c.line != "" && !strings.Contains(errs, c.line) {
				t.Errorf("stderr %q lacks %q", errs, c.line)
			}
			if strings.ContainsAny(text, "\x1b\r") {
				t.Errorf("a control character reached the terminal: %q", text)
			}
		})
	}
}
