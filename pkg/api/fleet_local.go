package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
)

// Adding and editing local items: the types mirror the platform's contract (packages/common
// models/fleet.ts, "adding and editing local items").

// LocalKindInfo is one kind the platform accepts as a local item.
type LocalKindInfo struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	// Namespaced is false for cluster-scoped kinds (Namespace).
	Namespaced bool   `json:"namespaced"`
	Note       string `json:"note,omitempty"`
	// Template is a starter manifest (YAML).
	Template string `json:"template,omitempty"`
}

// LocalKindRefusal is a kind the platform refuses, with the reason. APIVersion may be a pattern; * is any.
type LocalKindRefusal struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Reason     string `json:"reason"`
}

// LocalKindCatalog is GET local-kinds.
type LocalKindCatalog struct {
	Kinds        []LocalKindInfo    `json:"kinds"`
	Refused      []LocalKindRefusal `json:"refused"`
	SchemaSource string             `json:"schemaSource,omitempty"`
}

// LocalValidationIssue is one finding about a manifest, positioned in the text that was sent
// (1-based line and column, relative to the manifest, not to a file it came from).
type LocalValidationIssue struct {
	Severity  string `json:"severity"` // error or warning
	Message   string `json:"message"`
	Path      string `json:"path"`
	Line      int    `json:"line,omitempty"`
	Column    int    `json:"column,omitempty"`
	EndLine   int    `json:"endLine,omitempty"`
	EndColumn int    `json:"endColumn,omitempty"`
}

// Verdicts of a LocalValidation.
const (
	VerdictValid   = "valid"
	VerdictInvalid = "invalid"
)

// LocalValidation is the verdict on a manifest.
type LocalValidation struct {
	Verdict string                 `json:"verdict"`
	Issues  []LocalValidationIssue `json:"issues"`
}

// LocalWriteRequest is the body of PUT config/:target/local.
type LocalWriteRequest struct {
	// Manifest is one YAML (or JSON) document.
	Manifest string `json:"manifest"`
	// Replace overwrites an existing local item with the same identity; without it the platform answers 409.
	Replace bool `json:"replace,omitempty"`
}

// Actions of a LocalWriteResult.
const (
	LocalActionCreate    = "create"
	LocalActionReplace   = "replace"
	LocalActionUnchanged = "unchanged"
)

// LocalReplaces names the git copy that a local item hides on the robot.
type LocalReplaces struct {
	Layer  string `json:"layer"`
	Source string `json:"source"`
}

// LocalWriteResult is the answer of PUT config/:target/local. On a real write whose validation
// is invalid the platform answers 422 with the same body.
type LocalWriteResult struct {
	Target        ConfigTargetRef `json:"target"`
	ID            string          `json:"id"`
	DryRun        bool            `json:"dryRun"`
	Action        string          `json:"action"`
	Validation    LocalValidation `json:"validation"`
	Item          map[string]any  `json:"item,omitempty"`
	Replaces      *LocalReplaces  `json:"replaces,omitempty"`
	ReplacesOwner string          `json:"replacesOwner,omitempty"`
	Warnings      []string        `json:"warnings"`
}

// Invalid reports whether the platform refused the manifest.
func (r *LocalWriteResult) Invalid() bool { return r.Validation.Verdict == VerdictInvalid }

// LocalItemSource is GET config/:target/local?id=: a local item as YAML for an editor. Secret
// values are the placeholder <hidden>; writing it back keeps the stored value.
type LocalItemSource struct {
	Target      ConfigTargetRef `json:"target"`
	ID          string          `json:"id"`
	Manifest    string          `json:"manifest"`
	Owner       string          `json:"owner,omitempty"`
	HasOverride bool            `json:"hasOverride"`
}

// msgNoLocalAPI explains a platform whose device backend predates local resources.
const msgNoLocalAPI = "this platform cannot add or edit local resources yet: its device backend is older than this tnctl"

// noLocalAPI gives the bare 404 of a missing route a message. A 404 that carries the platform's own
// message (an unknown robot, device or item) is kept.
func noLocalAPI(err error) error {
	var ae *Error
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound && ae.Message == "" {
		ae.Message = msgNoLocalAPI
	}
	return err
}

// LocalKinds returns the kinds the platform accepts as local items, and the refused ones.
func (f *Fleet) LocalKinds(ctx context.Context) (*LocalKindCatalog, json.RawMessage, error) {
	c, raw, err := decode[LocalKindCatalog](f.Do(ctx, "GET", "local-kinds", nil, nil))
	return &c, raw, noLocalAPI(err)
}

// LocalItem returns a local item of a robot or device as YAML. id is Kind/namespace/name. An
// item that is not local (it comes from git or is managed by the platform) is a 409 with the
// platform's explanation.
func (f *Fleet) LocalItem(ctx context.Context, target, id string) (*LocalItemSource, json.RawMessage, error) {
	s, raw, err := decode[LocalItemSource](f.Do(ctx, "GET", configPath(target, "local"), url.Values{"id": {id}}, nil))
	return &s, raw, noLocalAPI(err)
}

// PutLocalItem adds or replaces a local item. With dryRun nothing is written. An answer of 422
// (the manifest is invalid) is a result, not an error: check LocalWriteResult.Invalid. Every
// other failure, 409 included, is an *Error with the platform's message.
func (f *Fleet) PutLocalItem(ctx context.Context, target string, req LocalWriteRequest, dryRun bool) (*LocalWriteResult, json.RawMessage, error) {
	r, raw, err := decode[LocalWriteResult](f.Do(ctx, "PUT", configPath(target, "local"), writeQuery("", dryRun), req))
	if err != nil {
		var ae *Error
		if errors.As(err, &ae) && ae.Status == http.StatusUnprocessableEntity && len(ae.Body) > 0 {
			var res LocalWriteResult
			if json.Unmarshal(ae.Body, &res) == nil && res.Validation.Verdict != "" {
				return &res, json.RawMessage(ae.Body), nil
			}
		}
		return nil, nil, noLocalAPI(err)
	}
	return &r, raw, nil
}
