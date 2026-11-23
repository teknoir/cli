package api

// Go mirror of a TeknoirBot's person chat: the routes below
// <Base>/bots/<bot>/chat. A bot itself never uses them; they are what a person's tnctl (and the web
// UI, and an MCP client) chats through.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ChatInfo is the answer of GET bots/<bot>/chat: who the bot is, whether this person may chat with it,
// the members of its council and the token budget.
type ChatInfo struct {
	Bot       ChatBot      `json:"bot"`
	CanChat   bool         `json:"canChat"`
	Reason    string       `json:"reason"`
	IsCreator bool         `json:"isCreator"`
	Members   []ChatMember `json:"members"`
	Budget    ChatBudget   `json:"budget"`

	// Council is the bot's council; nil from an older platform that does not send it.
	Council *ChatInfoCouncil `json:"council"`
}

// ChatInfoCouncil is the council as the chat's info carries it: the bot's default council, the
// councils it may use, and which members it could use.
type ChatInfoCouncil struct {
	// Proto is the council protocol the bot's computer last announced, 1 or 2 (1 also for an older
	// computer that announces none); nil when the platform has not seen it since it started.
	Proto *int `json:"proto"`
	// Tnctl is the tnctl version the bot's computer last reported, without the "tnctl/" prefix
	// (ChatBot.Council keeps the header's form "tnctl/<version>"); nil when the platform has not seen
	// it since it started or it never sent a well-formed version.
	Tnctl *string `json:"tnctl"`
	// Kit is the kit version the bot's computer last reported; nil when it sent no readable caps (an
	// older computer, or see CapsError) or was not seen.
	Kit *string `json:"kit"`
	// CapsError is why the bot's computer could not report its tools; nil when it did.
	CapsError *string `json:"capsError"`
	// DefaultRoster is the default council's name.
	DefaultRoster string          `json:"defaultRoster"`
	CanEdit       CouncilCanEdit  `json:"canEdit"`
	Roster        RosterView      `json:"roster"`  // the default council
	Rosters       []RosterSummary `json:"rosters"` // every council the bot can use
}

// CouncilCanEdit is whether this person may change the bot's council, and why not.
type CouncilCanEdit struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason"` // set when OK is false
}

// ChatBot is the bot a chat belongs to.
type ChatBot struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
	// Access is the bot's own council level: none, read or creator.
	Access string `json:"access"`
	// Phase is null while the bot has no computer yet.
	Phase *string `json:"phase"`
	// Online says whether the bot's computer is connected.
	Online bool `json:"online"`
	// LastSeen is when the bot was last seen, in milliseconds since the epoch; nil when never.
	LastSeen *int64 `json:"lastSeen"`
	// Council is the tnctl version the bot's computer last reported, in the header's form
	// "tnctl/<version>" (ChatInfoCouncil.Tnctl drops the prefix); nil when the platform has not seen
	// a well-formed one since it started, or does not report it.
	Council *string `json:"council"`
}

// ChatMember is one member of a council.
type ChatMember struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Model  string `json:"model"`
	Effort string `json:"effort"`
	// Job is what the member does in one sentence.
	Job string `json:"job"`
	// Runtime is the agent runtime the member runs on; empty when the platform does not say.
	Runtime string `json:"runtime"`
}

// ChatBudget is how many tokens the person may spend on the bot today.
type ChatBudget struct {
	TokensToday  int64 `json:"tokensToday"`
	TokensPerDay int64 `json:"tokensPerDay"`
}

// ChatAuthor is who wrote a thread or a turn.
type ChatAuthor struct {
	Ref   string `json:"ref"`
	Email string `json:"email"`
}

// ChatThread is one chat thread.
type ChatThread struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Origin string `json:"origin"`
	// Member is the member a member thread is for; nil when the whole council answers.
	Member    *string    `json:"member"`
	Author    ChatAuthor `json:"author"`
	CreatedAt int64      `json:"createdAt"`
	UpdatedAt int64      `json:"updatedAt"`
	TurnCount int        `json:"turnCount"`
	// LastTurn is the newest turn; nil for a thread with none.
	LastTurn *ChatTurnRef `json:"lastTurn"`

	// Roster is the council the thread is set to; nil when the thread follows the bot's default
	// council (made without a roster, or patched back with a null roster), for a member thread, and
	// when the council it named is gone. The council a thread asks is ChatThreadDetail.Council.
	Roster *ChatThreadRoster `json:"roster"`
	// MembersPin is the members the thread pins; null decodes to nil.
	MembersPin []string `json:"membersPin"`
	// MemberRole is the member a member thread is for; nil for a council thread.
	MemberRole *ChatMemberRole `json:"memberRole"`
}

// ChatThreadRoster names the council a thread is set to.
type ChatThreadRoster struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
}

// ChatMemberRole names the member a member thread is for, and its scope.
type ChatMemberRole struct {
	ID    string `json:"id"`
	Scope string `json:"scope"`
	Name  string `json:"name"`
}

// ChatTurnRef names a turn and its state, as a thread carries it.
type ChatTurnRef struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// ChatTurnOptions are the options a turn was asked with.
type ChatTurnOptions struct {
	Mode     string `json:"mode"`
	Effort   string `json:"effort"`
	Thorough bool   `json:"thorough"`
}

// ChatError is a turn's failure.
type ChatError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ChatUsage is what one turn cost.
type ChatUsage struct {
	Calls      int   `json:"calls"`
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
	MS         int64 `json:"ms"`
}

// ChatTurn is one question to a council and its answer.
type ChatTurn struct {
	ID       string `json:"id"`
	ThreadID string `json:"threadId"`
	Index    int    `json:"index"`
	// Kind is ask (a person asks the council) or member (one member answers: every turn of a thread
	// made with a member, from `tnctl chat --member` or council_ask, and an @slug turn in a council
	// thread).
	Kind    string          `json:"kind"`
	Member  *string         `json:"member"`
	Prompt  string          `json:"prompt"`
	Options ChatTurnOptions `json:"options"`
	// State is queued, running, done, failed, cancelled or expired.
	State string `json:"state"`
	// Final is the answer as markdown; nil until the turn ends.
	Final *string `json:"final"`
	// Error is why the turn failed; nil unless it did.
	Error      *ChatError `json:"error"`
	Usage      *ChatUsage `json:"usage"`
	Author     ChatAuthor `json:"author"`
	CreatedAt  int64      `json:"createdAt"`
	StartedAt  *int64     `json:"startedAt"`
	FinishedAt *int64     `json:"finishedAt"`

	// Roster is the council the turn ran on (an @slug turn of a council thread names it too); nil for
	// the turns of a thread made with a member, and when the council was deleted.
	Roster *ChatTurnRoster `json:"roster"`
	// RosterHash is the hash of the council the claim recorded ("sha256:<64 hex>" or "legacy"); nil
	// until the turn is claimed, and on turns from before Stage 1.
	RosterHash *string `json:"rosterHash"`
}

// ChatTurnRoster names the council a turn asked.
type ChatTurnRoster struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ChatThreadDetail is the answer of GET bots/<bot>/chat/threads/<thread>.
type ChatThreadDetail struct {
	Thread  ChatThread `json:"thread"`
	Turns   []ChatTurn `json:"turns"`
	LastSeq int64      `json:"lastSeq"`

	// Council is the council the thread asks; nil for a member thread and when the thread's council
	// cannot be resolved.
	Council *ChatThreadCouncil `json:"council"`
}

// ChatThreadCouncil is the council a thread asks, as the thread's detail carries it.
type ChatThreadCouncil struct {
	Roster RosterSummary `json:"roster"`
}

// chatPath is the path of the chat routes below Base.
func chatPath(bot string, rest ...string) string {
	return strings.Join(append([]string{"bots", url.PathEscape(bot), "chat"}, rest...), "/")
}

// chatThreadPath is the path of one thread's routes.
func chatThreadPath(bot, thread string, rest ...string) string {
	return chatPath(bot, append([]string{"threads", url.PathEscape(thread)}, rest...)...)
}

// ChatInfo asks whether this person may chat with the bot, who its council is and how much of the
// token budget is left.
func (f *Fleet) ChatInfo(ctx context.Context, bot string) (*ChatInfo, json.RawMessage, error) {
	v, raw, err := decode[ChatInfo](f.Do(ctx, "GET", chatPath(bot), nil, nil))
	return &v, raw, err
}

// chatThreadList is the answer of GET bots/<bot>/chat/threads.
type chatThreadList struct {
	Items []ChatThread `json:"items"`
}

// ChatThreads lists the person's threads with the bot, newest first.
func (f *Fleet) ChatThreads(ctx context.Context, bot string) ([]ChatThread, json.RawMessage, error) {
	l, raw, err := decode[chatThreadList](f.Do(ctx, "GET", chatPath(bot, "threads"), nil, nil))
	return l.Items, raw, err
}

// NewChatThread is the body of POST bots/<bot>/chat/threads. Empty fields are left out.
type NewChatThread struct {
	Title  string `json:"title,omitempty"`
	Origin string `json:"origin,omitempty"`
	Member string `json:"member,omitempty"`
	// MemberScope is the scope of Member: bot, teamspace or template.
	MemberScope string `json:"memberScope,omitempty"`
	// Roster is the council the thread asks, by name.
	Roster string `json:"roster,omitempty"`
}

// CreateChatThread starts a thread. Origin is where it was started: web, cli or mcp.
func (f *Fleet) CreateChatThread(ctx context.Context, bot string, req NewChatThread) (*ChatThread, json.RawMessage, error) {
	t, raw, err := decode[ChatThread](f.Do(ctx, "POST", chatPath(bot, "threads"), nil, req))
	return &t, raw, err
}

// ChatThread returns one thread with its turns and the sequence number of the newest event.
func (f *Fleet) ChatThread(ctx context.Context, bot, thread string) (*ChatThreadDetail, json.RawMessage, error) {
	d, raw, err := decode[ChatThreadDetail](f.Do(ctx, "GET", chatThreadPath(bot, thread), nil, nil))
	return &d, raw, err
}

// DeleteChatThread deletes a thread and its turns.
func (f *Fleet) DeleteChatThread(ctx context.Context, bot, thread string) error {
	_, err := f.Do(ctx, "DELETE", chatThreadPath(bot, thread), nil, nil)
	return err
}

// ChatThreadCouncilPatch is the body of PATCH bots/<bot>/chat/threads/<thread>: it changes the thread's
// council. A nil Roster is sent as null, which puts the thread back on the bot's default council.
type ChatThreadCouncilPatch struct {
	SetRoster bool
	Roster    *string
}

// MarshalJSON writes the patch as {"roster":"<name>"}, or {"roster":null} when Roster is nil.
func (p ChatThreadCouncilPatch) MarshalJSON() ([]byte, error) {
	if p.Roster == nil {
		return []byte(`{"roster":null}`), nil
	}
	return json.Marshal(struct {
		Roster string `json:"roster"`
	}{Roster: *p.Roster})
}

// chatThreadAnswer is the answer of PATCH bots/<bot>/chat/threads/<thread>: the thread under "thread".
type chatThreadAnswer struct {
	Thread ChatThread `json:"thread"`
}

// PatchChatThread changes the thread's council and returns the thread.
func (f *Fleet) PatchChatThread(ctx context.Context, bot, thread string, p ChatThreadCouncilPatch) (*ChatThread, json.RawMessage, error) {
	if !p.SetRoster {
		return nil, nil, errors.New("a thread patch names a roster")
	}
	a, raw, err := decode[chatThreadAnswer](f.Do(ctx, "PATCH", chatThreadPath(bot, thread), nil, p))
	return &a.Thread, raw, err
}

// NewChatTurn is the body of POST bots/<bot>/chat/threads/<thread>/turns. Empty fields are left out.
type NewChatTurn struct {
	Prompt string `json:"prompt,omitempty"`
	// Mode is auto, quick or council; Member asks one member (a member's slug) instead; Effort is
	// low, medium or high; Thorough asks the council for a longer, more careful answer.
	Mode     string `json:"mode,omitempty"`
	Member   string `json:"member,omitempty"`
	Effort   string `json:"effort,omitempty"`
	Thorough bool   `json:"thorough,omitempty"`
}

// chatTurnAnswer is the answer of a turn's routes.
type chatTurnAnswer struct {
	Turn ChatTurn `json:"turn"`
}

// PostChatTurn asks the council (or one member) and returns the queued turn. Its events come from
// ChatEvents.
func (f *Fleet) PostChatTurn(ctx context.Context, bot, thread string, req NewChatTurn) (*ChatTurn, json.RawMessage, error) {
	a, raw, err := decode[chatTurnAnswer](f.Do(ctx, "POST", chatThreadPath(bot, thread, "turns"), nil, req))
	return &a.Turn, raw, err
}

// CancelChatTurn asks the platform to cancel a queued or running turn.
func (f *Fleet) CancelChatTurn(ctx context.Context, bot, thread, turn string) (*ChatTurn, json.RawMessage, error) {
	a, raw, err := decode[chatTurnAnswer](f.Do(ctx, "POST", chatThreadPath(bot, thread, "turns", url.PathEscape(turn), "cancel"), nil, nil))
	return &a.Turn, raw, err
}

// ChatEvent is one event of a turn's stream. Data is the whole data object as the server sent it.
type ChatEvent struct {
	// Seq is the frame's id, the sequence number to resume from; 0 for an event without one (the
	// stream's own errors).
	Seq int64
	// Type is the frame's event name.
	Type string
	// TurnID is data.turnId; empty for the stream's own errors.
	TurnID string
	Data   json.RawMessage
}

// ChatStream is an open stream of a turn's events. Close it when done.
type ChatStream struct {
	r *sseReader
}

// ChatEvents opens the event stream of a thread after the sequence number after (0: from the
// beginning). The stream's own errors (access_lost, stream_expired, error) are events with no turn.
func (f *Fleet) ChatEvents(ctx context.Context, bot, thread string, after int64) (*ChatStream, error) {
	header := http.Header{}
	if after > 0 {
		header.Set("Last-Event-ID", strconv.FormatInt(after, 10))
	}
	resp, err := f.openSSE(ctx, f.Base+"/"+chatThreadPath(bot, thread, "events"), header)
	if err != nil {
		return nil, err
	}
	return &ChatStream{r: newSSEReader(resp.Body, streamIdleTimeout)}, nil
}

// ErrMalformedChatEvent is the error of a frame whose data is not a JSON object. Next returns it
// with the event's sequence number and type, so the reader can move past the frame.
var ErrMalformedChatEvent = errors.New("malformed chat event")

// Next returns the next event of the stream. io.EOF means the server ended the stream; a data line
// that is not a JSON object is an error, and the event it returned carries the frame's sequence
// number and type so the stream can be resumed after it.
func (s *ChatStream) Next() (ChatEvent, error) {
	ev, err := s.r.Next()
	if err != nil {
		return ChatEvent{}, err
	}
	out := ChatEvent{Type: ev.Name, Data: json.RawMessage(ev.Data)}
	if ev.ID != "" {
		if n, perr := strconv.ParseInt(ev.ID, 10, 64); perr == nil {
			out.Seq = n
		}
	}
	data := strings.TrimSpace(ev.Data)
	if !strings.HasPrefix(data, "{") || !strings.HasSuffix(data, "}") {
		return out, ErrMalformedChatEvent
	}
	var head struct {
		TurnID string `json:"turnId"`
	}
	if err := json.Unmarshal([]byte(data), &head); err != nil {
		// The message reads "malformed chat event: …" and errors.Is finds the sentinel.
		return out, fmt.Errorf("%w: %w", ErrMalformedChatEvent, err)
	}
	out.TurnID = head.TurnID
	return out, nil
}

// Close ends the stream.
func (s *ChatStream) Close() error {
	return s.r.Close()
}
