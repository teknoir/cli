package api

// Go mirror of a TeknoirBot's council: the routes below <Base>/bots/<bot>/council and <Base>/council.
// A council is the members a bot asks, each of them a role (the hub calls it a member) with its own
// model, effort, tools and instructions. Field order follows the hub's key order; a value the hub may
// send as null is a pointer, and a list it may send as null is a *[]string.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// RoleTemplateRef names the premade member a bot's or a teamspace's member was made from.
type RoleTemplateRef struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
}

// RoleView is one member as a view shows it, never its instructions.
type RoleView struct {
	ID          string   `json:"id"`
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`    // lead, member or reviewer
	Runtime     string   `json:"runtime"` // inproc or coder
	Model       string   `json:"model"`   // sonnet or haiku
	ModelID     string   `json:"modelId"`
	Effort      string   `json:"effort"`
	Description string   `json:"description"`
	Groups      []string `json:"groups"`
	// EffectiveTools is nil until the bot's computer reported its tools.
	EffectiveTools *[]string        `json:"effectiveTools"`
	Level          string           `json:"level"`
	Ceiling        string           `json:"ceiling"`
	Skills         []string         `json:"skills"`
	Scope          string           `json:"scope"` // template, teamspace or bot
	Version        int              `json:"version"`
	Category       string           `json:"category"`
	TemplateRef    *RoleTemplateRef `json:"templateRef"`
	Status         string           `json:"status"` // ok, invalid or unchecked
	Reasons        []string         `json:"reasons"`
	Notes          []string         `json:"notes"`
}

// RoleVersion is one version of a member.
type RoleVersion struct {
	Version   int    `json:"version"`
	CreatedBy string `json:"createdBy"`
	CreatedAt int64  `json:"createdAt"`
}

// RoleBudget caps what one turn on the member may spend.
type RoleBudget struct {
	MaxToolRounds int `json:"maxToolRounds"`
	MaxTokens     int `json:"maxTokens"`
}

// CouncilUse is a council that uses a member.
type CouncilUse struct {
	Bot         string `json:"bot"`
	Roster      string `json:"roster"`
	DisplayName string `json:"displayName"`
}

// RoleDetail is a member with all a person may read of it. GET bots/<bot>/council/roles/<slug> answers a
// RoleView instead for a bot-scope member when the person may not drive the bot; Detail tells them apart.
type RoleDetail struct {
	RoleView
	Instructions *string       `json:"instructions"`
	Persona      *string       `json:"persona"`
	Budget       *RoleBudget   `json:"budget"`
	Versions     []RoleVersion `json:"versions"`
	CreatedBy    *string       `json:"createdBy"`
	UpdatedBy    *string       `json:"updatedBy"`
	UpdatedAt    *int64        `json:"updatedAt"`
	ArchivedAt   *int64        `json:"archivedAt"`
	ArchivedBy   *string       `json:"archivedBy"`
	UsedBy       []CouncilUse  `json:"usedBy,omitempty"`      // teamspace members only
	HiddenCount  *int          `json:"hiddenCount,omitempty"` // teamspace members only
	Detail       bool          `json:"-"`                     // true when the answer carried the "versions" key
}

// UnmarshalJSON decodes the member and notes whether the answer was the detail (it carries "versions")
// or only the view.
func (d *RoleDetail) UnmarshalJSON(b []byte) error {
	// A JSON null leaves the member as it is, as encoding/json does for any struct.
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return nil
	}
	type roleDetail RoleDetail
	var a roleDetail
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*d = RoleDetail(a)
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(b, &keys); err == nil {
		_, d.Detail = keys["versions"]
	}
	return nil
}

// RosterSummary is a council without its members' views.
type RosterSummary struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	DisplayName string   `json:"displayName"`
	Scope       string   `json:"scope"` // template or bot
	Version     int      `json:"version"`
	Lead        string   `json:"lead"`    // slug
	Members     []string `json:"members"` // slugs; may name a member that is gone
	// Reviewer is a member's slug; nil when the council has none.
	Reviewer         *string  `json:"reviewer"`
	IsDefault        bool     `json:"isDefault"`
	LegacyCompatible bool     `json:"legacyCompatible"`
	Status           string   `json:"status"`
	Reasons          []string `json:"reasons"`
}

// RosterBudget caps what one turn on the council may spend.
type RosterBudget struct {
	MaxTokens int `json:"maxTokens"`
}

// RosterRefView is one stored reference of a council, gone ones included.
type RosterRefView struct {
	RoleID string `json:"roleId"` // tpl:<slug> for a premade member, else the role's id
	Role   string `json:"role"`   // the slug, as Lead, Members and Reviewer show it
	// Scope is template, teamspace or bot; nil only for a gone reference whose role cannot be read.
	Scope *string `json:"scope"`
	// Version is the pin; nil when the reference follows the member's latest version.
	Version *int `json:"version"`
	Gone    bool `json:"gone"` // the member was archived or deleted
}

// RosterRefs are a council's stored references: what a PUT of the council would name.
type RosterRefs struct {
	Lead     *RosterRefView  `json:"lead"` // nil only in the stub of a missing default council
	Members  []RosterRefView `json:"members"`
	Reviewer *RosterRefView  `json:"reviewer"`
}

// RosterView is a council with its members' views. Roles omits members that are gone, so never line
// Members[i] up with Roles[i]: match by slug and kind. Refs (when sent) holds every stored reference,
// gone ones included, in the same order as Lead, Members and Reviewer: Refs.Members[i].Role == Members[i].
type RosterView struct {
	RosterSummary
	Description *string       `json:"description"`
	Budget      *RosterBudget `json:"budget"`
	Roles       []RoleView    `json:"roles"`
	// Refs are the council's stored references; nil from a platform that does not send them.
	Refs *RosterRefs `json:"refs"`
}

// CouncilCaps is what the bot's computer reported of its tools and skills.
type CouncilCaps struct {
	Proto     int       `json:"proto"`
	Tnctl     *string   `json:"tnctl"`
	Kit       *string   `json:"kit"`
	Tools     *[]string `json:"tools"`
	Skills    *[]string `json:"skills"`
	CapsError *string   `json:"capsError"`
}

// CouncilView is a bot's council: GET bots/<bot>/council answers {"council": CouncilView}. Proto, Tnctl, Kit
// and CapsError are as in ChatInfoCouncil.
type CouncilView struct {
	Proto         *int            `json:"proto"`
	Tnctl         *string         `json:"tnctl"`
	Kit           *string         `json:"kit"`
	CapsError     *string         `json:"capsError"`
	DefaultRoster string          `json:"defaultRoster"`
	CanEdit       CouncilCanEdit  `json:"canEdit"`
	Roster        RosterView      `json:"roster"`
	Rosters       []RosterView    `json:"rosters"`
	Available     []RoleView      `json:"available"`
	Catalog       json.RawMessage `json:"catalog"`
	Caps          *CouncilCaps    `json:"caps"`
}

// CatalogTemplates are the premade members and councils the teamspace may copy.
type CatalogTemplates struct {
	Roles   []RoleDetail    `json:"roles"`
	Rosters []RosterSummary `json:"rosters"`
}

// Catalog is GET council/catalog: the tool groups, skills and limits, the premade members and councils,
// and the teamspace's shared members.
type Catalog struct {
	Groups    json.RawMessage  `json:"groups"`
	Skills    []string         `json:"skills"`
	Limits    json.RawMessage  `json:"limits"`
	Templates CatalogTemplates `json:"templates"`
	Roles     []RoleView       `json:"roles"`
}

// RoleDraftRequest is the body of POST council/roles/draft: a job for a new member, in words.
type RoleDraftRequest struct {
	Name  string `json:"name"`
	Job   string `json:"job"`
	How   string `json:"how,omitempty"`
	From  string `json:"from,omitempty"`
	Bot   string `json:"bot,omitempty"`
	Scope string `json:"scope,omitempty"` // bot or teamspace
}

// CouncilField is one field of a refused council document, and why.
type CouncilField struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// RoleDraftStart is the premade member a draft was made from.
type RoleDraftStart struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// RoleDraft is the answer of POST council/roles/draft: a member spec, for review before its PUT.
type RoleDraft struct {
	Slug string `json:"slug"`
	// Spec is a whole member spec; send it back unchanged in RolePut.
	Spec           json.RawMessage `json:"spec"`
	StartsFrom     RoleDraftStart  `json:"startsFrom"`
	EffectiveTools *[]string       `json:"effectiveTools"`
	Warnings       []CouncilField  `json:"warnings"`
}

// RolePut is the body of a member's PUT. ExpectVersion nil leaves the key out; a pointer to 0 sends 0
// (create only).
type RolePut struct {
	Spec          json.RawMessage `json:"spec"`
	ExpectVersion *int            `json:"expectVersion,omitempty"`
	Restore       bool            `json:"restore,omitempty"`
	Confirm       []string        `json:"confirm,omitempty"`
}

// RoleWrite is the answer of a member's PUT: the member as it now is, and what the write did.
type RoleWrite struct {
	Role   RoleDetail `json:"role"`
	Action string     `json:"action"` // created, updated or unchanged
}

// RosterRef names a member of a council: its slug, its scope when known, and a version pin.
type RosterRef struct {
	Role    string `json:"role" yaml:"role"`
	Scope   string `json:"scope,omitempty" yaml:"scope"`
	Version *int   `json:"version,omitempty" yaml:"version"`
}

// UnmarshalYAML reads a member reference as a role slug alone, or as an object.
func (r *RosterRef) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		var slug string
		if err := value.Decode(&slug); err != nil {
			return err
		}
		*r = RosterRef{Role: slug}
		return nil
	case yaml.MappingNode:
		type alias RosterRef
		var a alias
		if err := value.Decode(&a); err != nil {
			return err
		}
		*r = RosterRef(a)
		return nil
	}
	return fmt.Errorf("a council member is a slug or an object, not a %s", yamlKind(value))
}

// RosterSpec is a council's whole spec: what a PUT of a council sends.
type RosterSpec struct {
	DisplayName string      `json:"displayName"`
	Description *string     `json:"description,omitempty"`
	Lead        RosterRef   `json:"lead"`
	Members     []RosterRef `json:"members"`
	// Reviewer has no omitempty: nil is sent as null (no reviewer).
	Reviewer *RosterRef    `json:"reviewer"`
	Budget   *RosterBudget `json:"budget,omitempty"`
}

// RosterPut is the body of a council's PUT.
type RosterPut struct {
	Spec          RosterSpec `json:"spec"`
	ExpectVersion *int       `json:"expectVersion,omitempty"`
	ExpectID      string     `json:"expectId,omitempty"`
}

// RosterWrite is the answer of a council's PUT: the council as it now is, and what the write did.
type RosterWrite struct {
	Roster RosterView `json:"roster"`
	Action string     `json:"action"`
}

// ApplyRequest is the body of POST council/apply: a YAML file of members and councils.
type ApplyRequest struct {
	YAML    string   `json:"yaml"`
	DryRun  bool     `json:"dryRun,omitempty"`
	Confirm []string `json:"confirm,omitempty"`
}

// ApplyItem is what applying one document of the file did.
type ApplyItem struct {
	Index   int    `json:"index"` // 0-based position in the file, empty documents counted
	Kind    string `json:"kind"`  // CouncilRole or CouncilRoster
	Name    string `json:"name"`
	Scope   string `json:"scope"`
	Bot     string `json:"bot,omitempty"`
	Action  string `json:"action"`
	Version int    `json:"version"`
}

// ApplyResult is the answer of POST council/apply.
type ApplyResult struct {
	Results []ApplyItem `json:"results"`
}

// CouncilError is a council route's refusal, read from the hub's body: {code, message, ...extra,
// error:{name, message}}.
type CouncilError struct {
	Status  int
	Code    string
	Message string // the hub's own message, through cleanMessage: never the 403 rewrite
	// Document is the 0-based index of the document refused, on apply.
	Document *int
	Fields   []CouncilField // messages through cleanMessage
	Needs    []string       // confirm_needed: inspect, creator
	// Documents are the documents that ask, on a confirm_needed of apply.
	Documents []int
	// Current is a conflict's current value: nil when absent, the bytes null, or an object.
	Current     json.RawMessage
	Archived    bool         // conflict on an archived member
	UsedBy      []CouncilUse // role_in_use
	HiddenCount int          // role_in_use: councils the person cannot see
}

// CouncilErrorOf returns the council refusal inside err: false when err holds no *Error, its body is not a JSON
// object, or the body has no non-empty string "code" (keys match exactly, never case-insensitively).
func CouncilErrorOf(err error) (*CouncilError, bool) {
	var e *Error
	if !errors.As(err, &e) || e == nil {
		return nil, false
	}
	var keys map[string]json.RawMessage
	if json.Unmarshal(e.Body, &keys) != nil || keys == nil {
		return nil, false
	}
	var code string
	if json.Unmarshal(keys["code"], &code) != nil || code == "" {
		return nil, false
	}
	// Every other key is read on its own: one of an unexpected type stays at its zero value and never
	// hides the code and the message. Current keeps the raw value: nil when absent, `null` when null.
	ce := &CouncilError{Status: e.Status, Code: code, Current: keys["current"]}
	var message string
	if json.Unmarshal(keys["message"], &message) == nil {
		ce.Message = cleanMessage(message)
	}
	var document *int
	if json.Unmarshal(keys["document"], &document) == nil {
		ce.Document = document
	}
	var fields []CouncilField
	if json.Unmarshal(keys["fields"], &fields) == nil {
		for _, f := range fields {
			ce.Fields = append(ce.Fields, CouncilField{Path: cleanMessage(f.Path), Message: cleanMessage(f.Message)})
		}
	}
	var needs []string
	if json.Unmarshal(keys["needs"], &needs) == nil {
		ce.Needs = needs
	}
	var documents []int
	if json.Unmarshal(keys["documents"], &documents) == nil {
		ce.Documents = documents
	}
	var archived bool
	if json.Unmarshal(keys["archived"], &archived) == nil {
		ce.Archived = archived
	}
	var usedBy []CouncilUse
	if json.Unmarshal(keys["usedBy"], &usedBy) == nil {
		ce.UsedBy = usedBy
	}
	var hidden int
	if json.Unmarshal(keys["hiddenCount"], &hidden) == nil {
		ce.HiddenCount = hidden
	}
	return ce, true
}

// rosterExportHead is what an export document says it is.
type rosterExportHead struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
}

// rosterExportDoc is the part of a council's export that DecodeRosterExport reads.
type rosterExportDoc struct {
	Spec rosterExportSpec `yaml:"spec"`
}

type rosterExportSpec struct {
	DisplayName string              `yaml:"displayName"`
	Description *string             `yaml:"description"`
	Lead        RosterRef           `yaml:"lead"`
	Members     []RosterRef         `yaml:"members"`
	Reviewer    *RosterRef          `yaml:"reviewer"`
	Budget      *rosterExportBudget `yaml:"budget"`
}

type rosterExportBudget struct {
	MaxTokens *int `yaml:"maxTokens"`
}

// DecodeRosterExport reads the hub's export of one council (GET …/rosters/<name>/export) back into the
// spec a PUT takes. It is only for the hub's own export, to rebuild a PUT body: the scope, the bot,
// a template's version, metadata and status are ignored.
func DecodeRosterExport(b []byte) (*RosterSpec, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	var docs []*yaml.Node
	for {
		var n yaml.Node
		err := dec.Decode(&n)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, exportError("%w", err)
		}
		if n.Kind == 0 {
			continue
		}
		if n.Kind == yaml.DocumentNode && len(n.Content) == 1 &&
			n.Content[0].Kind == yaml.ScalarNode && n.Content[0].Tag == "!!null" {
			continue
		}
		docs = append(docs, &n)
	}
	switch {
	case len(docs) == 0:
		return nil, exportError("it holds no document")
	case len(docs) > 1:
		return nil, exportError("it holds more than one document")
	}
	// Each remaining document is a DocumentNode with one child. A list or a scalar there (a proxy's text
	// page) is named by its kind, never by a decode error that prints Go types.
	root := docs[0].Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, exportError("it is a %s, not an object", yamlKind(root))
	}
	if spec := mapValue(root, "spec"); spec != nil && spec.Kind != yaml.MappingNode && spec.Tag != "!!null" {
		return nil, exportError("its spec is a %s, not an object", yamlKind(spec))
	}
	var head rosterExportHead
	if err := docs[0].Decode(&head); err != nil {
		return nil, exportError("%w", err)
	}
	if head.APIVersion != "council.teknoir.org/v1alpha1" {
		return nil, exportError("apiVersion %q is not council.teknoir.org/v1alpha1", head.APIVersion)
	}
	if head.Kind != "CouncilRoster" {
		return nil, exportError("kind %q is not CouncilRoster", head.Kind)
	}
	var doc rosterExportDoc
	if err := docs[0].Decode(&doc); err != nil {
		return nil, exportError("%w", err)
	}
	if len(doc.Spec.Members) == 0 {
		return nil, exportError("it names no member")
	}
	if doc.Spec.Lead.Role == "" {
		return nil, exportError("the lead names no role")
	}
	for i, m := range doc.Spec.Members {
		if m.Role == "" {
			return nil, exportError("member %d names no role", i+1)
		}
	}
	if doc.Spec.Reviewer != nil && doc.Spec.Reviewer.Role == "" {
		return nil, exportError("the reviewer names no role")
	}
	spec := &RosterSpec{
		DisplayName: doc.Spec.DisplayName,
		Description: doc.Spec.Description,
		Lead:        doc.Spec.Lead,
		Members:     doc.Spec.Members,
		Reviewer:    doc.Spec.Reviewer,
	}
	// An absent or null maxTokens gives no budget: the hub refuses a budget of 0.
	if doc.Spec.Budget != nil && doc.Spec.Budget.MaxTokens != nil {
		spec.Budget = &RosterBudget{MaxTokens: *doc.Spec.Budget.MaxTokens}
	}
	return spec, nil
}

// mapValue returns the value of key in the mapping node m, or nil.
func mapValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// exportError is a DecodeRosterExport failure, in one sentence.
func exportError(format string, args ...any) error {
	return fmt.Errorf("the platform's council export could not be read: "+format, args...)
}

// yamlKind names a YAML node's kind for an error message.
func yamlKind(n *yaml.Node) string {
	switch n.Kind {
	case yaml.DocumentNode:
		return "document"
	case yaml.SequenceNode:
		return "list"
	case yaml.MappingNode:
		return "object"
	case yaml.ScalarNode:
		return "scalar"
	case yaml.AliasNode:
		return "alias"
	}
	return "node"
}

// botCouncilPath is the path of a bot's council routes below Base.
func botCouncilPath(bot string, rest ...string) string {
	return strings.Join(append([]string{"bots", url.PathEscape(bot), "council"}, rest...), "/")
}

// councilPath is the path of the teamspace's council routes below Base.
func councilPath(rest ...string) string {
	return strings.Join(append([]string{"council"}, rest...), "/")
}

// councilQuery is the query of a council route that names a scope and a version: each key only when
// it is set.
func councilQuery(scope string, version int) url.Values {
	q := url.Values{}
	if scope != "" {
		q.Set("scope", scope)
	}
	if version > 0 {
		q.Set("version", strconv.Itoa(version))
	}
	return q
}

// councilAnswer is an answer that carries a bot's council under "council".
type councilAnswer struct {
	Council CouncilView `json:"council"`
}

// roleAnswer is an answer that carries a member under "role".
type roleAnswer struct {
	Role RoleDetail `json:"role"`
}

// BotCouncil returns the bot's council, the councils it may use and the members it could use.
func (f *Fleet) BotCouncil(ctx context.Context, bot string) (*CouncilView, json.RawMessage, error) {
	a, raw, err := decode[councilAnswer](f.Do(ctx, "GET", botCouncilPath(bot), nil, nil))
	return &a.Council, raw, err
}

// SetDefaultRoster makes a council the bot's default and returns the bot's council.
func (f *Fleet) SetDefaultRoster(ctx context.Context, bot, roster string) (*CouncilView, json.RawMessage, error) {
	body := struct {
		Roster string `json:"roster"`
	}{Roster: roster}
	a, raw, err := decode[councilAnswer](f.Do(ctx, "PUT", botCouncilPath(bot, "default"), nil, body))
	return &a.Council, raw, err
}

// PutRoster writes a council of the bot's: create it, or replace it with ExpectVersion or ExpectID.
func (f *Fleet) PutRoster(ctx context.Context, bot, name string, body RosterPut) (*RosterWrite, json.RawMessage, error) {
	w, raw, err := decode[RosterWrite](f.Do(ctx, "PUT", botCouncilPath(bot, "rosters", url.PathEscape(name)), nil, body))
	return &w, raw, err
}

// DeleteRoster deletes a council of the bot's.
func (f *Fleet) DeleteRoster(ctx context.Context, bot, name string) error {
	_, err := f.Do(ctx, "DELETE", botCouncilPath(bot, "rosters", url.PathEscape(name)), nil, nil)
	return err
}

// RosterExport returns the hub's export of one of the bot's councils as YAML.
func (f *Fleet) RosterExport(ctx context.Context, bot, name string) ([]byte, error) {
	raw, err := f.Do(ctx, "GET", botCouncilPath(bot, "rosters", url.PathEscape(name), "export"), nil, nil)
	if err != nil {
		return nil, err
	}
	return []byte(raw), nil
}

// BotRole returns one of the bot's members. Scope narrows it to a scope when set.
func (f *Fleet) BotRole(ctx context.Context, bot, slug, scope string) (*RoleDetail, json.RawMessage, error) {
	a, raw, err := decode[roleAnswer](f.Do(ctx, "GET", botCouncilPath(bot, "roles", url.PathEscape(slug)), councilQuery(scope, 0), nil))
	return &a.Role, raw, err
}

// BotRoleExport returns the hub's export of one of the bot's members as YAML.
func (f *Fleet) BotRoleExport(ctx context.Context, bot, slug, scope string, version int) ([]byte, error) {
	raw, err := f.Do(ctx, "GET", botCouncilPath(bot, "roles", url.PathEscape(slug), "export"), councilQuery(scope, version), nil)
	if err != nil {
		return nil, err
	}
	return []byte(raw), nil
}

// PutBotRole writes one of the bot's members.
func (f *Fleet) PutBotRole(ctx context.Context, bot, slug string, body RolePut) (*RoleWrite, json.RawMessage, error) {
	w, raw, err := decode[RoleWrite](f.Do(ctx, "PUT", botCouncilPath(bot, "roles", url.PathEscape(slug)), nil, body))
	return &w, raw, err
}

// ArchiveBotRole archives one of the bot's members.
func (f *Fleet) ArchiveBotRole(ctx context.Context, bot, slug string) (*RoleDetail, json.RawMessage, error) {
	a, raw, err := decode[roleAnswer](f.Do(ctx, "POST", botCouncilPath(bot, "roles", url.PathEscape(slug), "archive"), nil, struct{}{}))
	return &a.Role, raw, err
}

// CouncilCatalog returns the teamspace's council catalog.
func (f *Fleet) CouncilCatalog(ctx context.Context) (*Catalog, json.RawMessage, error) {
	c, raw, err := decode[Catalog](f.Do(ctx, "GET", councilPath("catalog"), nil, nil))
	return &c, raw, err
}

// TeamspaceRole returns one of the teamspace's shared members.
func (f *Fleet) TeamspaceRole(ctx context.Context, slug string) (*RoleDetail, json.RawMessage, error) {
	a, raw, err := decode[roleAnswer](f.Do(ctx, "GET", councilPath("roles", url.PathEscape(slug)), nil, nil))
	return &a.Role, raw, err
}

// TeamspaceRoleExport returns the hub's export of one of the teamspace's shared members as YAML. Scope
// is teamspace or template.
func (f *Fleet) TeamspaceRoleExport(ctx context.Context, slug, scope string, version int) ([]byte, error) {
	raw, err := f.Do(ctx, "GET", councilPath("roles", url.PathEscape(slug), "export"), councilQuery(scope, version), nil)
	if err != nil {
		return nil, err
	}
	return []byte(raw), nil
}

// PutTeamspaceRole writes one of the teamspace's shared members.
func (f *Fleet) PutTeamspaceRole(ctx context.Context, slug string, body RolePut) (*RoleWrite, json.RawMessage, error) {
	w, raw, err := decode[RoleWrite](f.Do(ctx, "PUT", councilPath("roles", url.PathEscape(slug)), nil, body))
	return &w, raw, err
}

// ArchiveTeamspaceRole archives one of the teamspace's shared members.
func (f *Fleet) ArchiveTeamspaceRole(ctx context.Context, slug string) (*RoleDetail, json.RawMessage, error) {
	a, raw, err := decode[roleAnswer](f.Do(ctx, "POST", councilPath("roles", url.PathEscape(slug), "archive"), nil, struct{}{}))
	return &a.Role, raw, err
}

// DraftRole asks the platform to draft a member from a job in words: the answer holds a spec, for
// review before its PUT.
func (f *Fleet) DraftRole(ctx context.Context, body RoleDraftRequest) (*RoleDraft, json.RawMessage, error) {
	d, raw, err := decode[RoleDraft](f.Do(ctx, "POST", councilPath("roles", "draft"), nil, body))
	return &d, raw, err
}

// ApplyCouncil applies a YAML file of members and councils.
func (f *Fleet) ApplyCouncil(ctx context.Context, body ApplyRequest) (*ApplyResult, json.RawMessage, error) {
	r, raw, err := decode[ApplyResult](f.Do(ctx, "POST", councilPath("apply"), nil, body))
	return &r, raw, err
}
