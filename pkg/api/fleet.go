package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/teknoir/cli/pkg/auth"
)

// Fleet is the client of the Backstage fleet API of one teamspace:
// https://<domain>/api/device/fleet/<namespace>/...
type Fleet struct {
	// Base is https://<domain>/api/device/fleet/<namespace>.
	Base string
	// Root is https://<domain>/api/device/fleet, the base of the routes that name no teamspace
	// (the teamspace list). Empty: Base without its last path segment.
	Root string
	// Token returns a valid bearer token for each request.
	Token func(ctx context.Context) (string, error)
	// bot is set by NewBotFleet: this client talks to the bot API as a TeknoirBot, which may not
	// read device logins.
	bot bool
}

// NewFleet returns the fleet client of a teamspace on a domain, authenticated with the
// token that `tnctl login` stored.
func NewFleet(domain, namespace string) *Fleet {
	root := fmt.Sprintf("https://%s/api/device/fleet", domain)
	return &Fleet{
		Root: root,
		Base: root + "/" + url.PathEscape(namespace),
		Token: func(ctx context.Context) (string, error) {
			return auth.AccessToken(ctx, domain)
		},
	}
}

// ErrBotKey is what the bot's fleet returns when its key file is missing or empty (exit code 6: this
// computer predates the agent, and only a person can re-create the bot, whose new computer brings a new
// key). The error message adds the file's path; its content is never shown.
var ErrBotKey = errors.New("the bot key is missing or empty")

// ErrBotAPIInsecure is what NewBotFleet refuses a $TEKNOIRBOT_API with: the bot's key must not travel
// in clear text. Only a loopback address may be plain http.
var ErrBotAPIInsecure = errors.New("TEKNOIRBOT_API must be https")

// NewBotFleet returns the fleet client of a TeknoirBot: the bot API's fleet routes for the bot's own
// teamspace, authenticated with the bot's key, which is read from keyFile on every request. A bot gets
// its key when its computer is made: re-creating the bot gives it a new computer and a new key. A bot
// API that is not https (and not on a loopback address) answers ErrBotAPIInsecure before any request
// leaves.
func NewBotFleet(apiBase, namespace, keyFile string) *Fleet {
	root := strings.TrimSuffix(apiBase, "/") + "/fleet"
	f := &Fleet{
		Root: root,
		Base: root + "/" + url.PathEscape(namespace),
		Token: func(context.Context) (string, error) {
			return botKey(keyFile)
		},
		bot: true,
	}
	if !botAPIAllowed(apiBase) {
		f.Token = func(context.Context) (string, error) { return "", ErrBotAPIInsecure }
	}
	return f
}

// botAPIAllowed reports whether apiBase may carry the bot's key: https anywhere, http only on a
// loopback address.
func botAPIAllowed(apiBase string) bool {
	u, err := url.Parse(apiBase)
	if err != nil || u.Host == "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		host := u.Hostname()
		if ip := net.ParseIP(host); ip != nil {
			return ip.IsLoopback()
		}
		return strings.EqualFold(host, "localhost")
	}
	return false
}

// botKey is the content of the bot's key file, trimmed. The file is read on every request so a
// replaced key is picked up without a restart; its path may be shown, its content never.
func botKey(keyFile string) (string, error) {
	b, err := os.ReadFile(keyFile)
	if err != nil || strings.TrimSpace(string(b)) == "" {
		return "", fmt.Errorf("%w (%s): this computer predates the agent; re-create the bot", ErrBotKey, keyFile)
	}
	return strings.TrimSpace(string(b)), nil
}

// ErrBotNoDeviceLogin is what the device-login routes answer in bot mode. The platform refuses these
// routes to bots as well; tnctl does not even ask.
var ErrBotNoDeviceLogin = errors.New("a TeknoirBot cannot read device logins: it cannot open tunnels or hold device logins")

// Do calls path (relative to Base, no leading slash) and returns the raw JSON of a 2xx.
func (f *Fleet) Do(ctx context.Context, method, path string, query url.Values, body any) (json.RawMessage, error) {
	token, err := f.Token(ctx)
	if err != nil {
		return nil, err
	}
	u := f.Base + "/" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	return doRaw(ctx, method, u, token, body)
}

// DoRoot calls path relative to Root, the routes whose path names the teamspace (for example
// "teknoir-demo/robots"). It is what `tnctl api` uses.
func (f *Fleet) DoRoot(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	token, err := f.Token(ctx)
	if err != nil {
		return nil, err
	}
	return doRaw(ctx, method, f.rootURL()+"/"+strings.TrimPrefix(path, "/"), token, body)
}

func decode[T any](raw json.RawMessage, err error) (T, json.RawMessage, error) {
	var out T
	if err != nil {
		return out, nil, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return out, raw, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, raw, fmt.Errorf("unexpected answer from the fleet API: %w", err)
	}
	return out, raw, nil
}

// Robots lists the robots of the teamspace.
func (f *Fleet) Robots(ctx context.Context) ([]RobotSummary, json.RawMessage, error) {
	return decode[[]RobotSummary](f.Do(ctx, "GET", "robots", nil, nil))
}

// Robot returns one robot.
func (f *Fleet) Robot(ctx context.Context, name string) (*RobotDetail, json.RawMessage, error) {
	d, raw, err := decode[RobotDetail](f.Do(ctx, "GET", "robots/"+url.PathEscape(name), nil, nil))
	return &d, raw, err
}

// CreateRobot creates a robot (and, unless req.Device adopts an existing one, its device) and returns
// it as GET robots/<name> would.
func (f *Fleet) CreateRobot(ctx context.Context, req CreateRobotRequest) (*RobotDetail, json.RawMessage, error) {
	d, raw, err := decode[RobotDetail](f.Do(ctx, "POST", "robots", nil, req))
	return &d, raw, err
}

func configPath(target string, rest ...string) string {
	return strings.Join(append([]string{"config", url.PathEscape(target)}, rest...), "/")
}

// ConfigTarget returns the layered configuration, overrides and states of a robot or device.
func (f *Fleet) ConfigTarget(ctx context.Context, target string) (*ConfigTargetView, json.RawMessage, error) {
	v, raw, err := decode[ConfigTargetView](f.Do(ctx, "GET", configPath(target), nil, nil))
	return &v, raw, err
}

// ConfigResource returns one resource with provenance, the rendered item and the override.
func (f *Fleet) ConfigResource(ctx context.Context, target, id string, defaults bool) (*ConfigResourceDetail, json.RawMessage, error) {
	q := url.Values{"id": {id}}
	if defaults {
		q.Set("defaults", "1")
	}
	d, raw, err := decode[ConfigResourceDetail](f.Do(ctx, "GET", configPath(target, "resource"), q, nil))
	return &d, raw, err
}

func writeQuery(id string, dryRun bool) url.Values {
	q := url.Values{}
	if id != "" {
		q.Set("id", id)
	}
	if dryRun {
		q.Set("dryRun", "1")
	}
	return q
}

// PatchResourceOverride applies a patch to the override of one resource.
func (f *Fleet) PatchResourceOverride(ctx context.Context, target, id string, patch OverrideResourcePatch, dryRun bool) (*OverrideWriteResult, json.RawMessage, error) {
	r, raw, err := decode[OverrideWriteResult](f.Do(ctx, "PATCH", configPath(target, "overrides", "resource"), writeQuery(id, dryRun), patch))
	return &r, raw, err
}

// SetResourceOff turns one resource off (or on) on a robot. Off is a resource that the robot type,
// a fleet or the robot's own apps put there: it is left out of the robot's configuration and deleted
// on the robot. confirm is the text an earlier dry run asked for; "" sends no confirm.
func (f *Fleet) SetResourceOff(ctx context.Context, target, id string, off bool, note string, dryRun bool, confirm string) (*OverrideWriteResult, json.RawMessage, error) {
	q := writeQuery(id, dryRun)
	if confirm != "" {
		q.Set("confirm", confirm)
	}
	r, raw, err := decode[OverrideWriteResult](f.Do(ctx, "PATCH", configPath(target, "overrides", "resource"), q, OverrideResourcePatch{Off: &off, Note: note}))
	return &r, raw, err
}

// SetApplicationOff turns a whole application off (or on) on a robot: every item it adds.
func (f *Fleet) SetApplicationOff(ctx context.Context, target, repoURL, path string, off bool, note string, dryRun bool, confirm string) (*ApplicationOffResult, json.RawMessage, error) {
	q := url.Values{"repo_url": {repoURL}}
	if path != "" {
		q.Set("path", path)
	}
	if dryRun {
		q.Set("dryRun", "1")
	}
	if confirm != "" {
		q.Set("confirm", confirm)
	}
	r, raw, err := decode[ApplicationOffResult](f.Do(ctx, "PATCH", configPath(target, "overrides", "application", "off"), q, OffRequest{Off: off, Note: note}))
	return &r, raw, err
}

// PutResourceOverride replaces the whole override of one resource.
func (f *Fleet) PutResourceOverride(ctx context.Context, target, id string, o ResourceOverride, dryRun bool) (*OverrideWriteResult, json.RawMessage, error) {
	r, raw, err := decode[OverrideWriteResult](f.Do(ctx, "PUT", configPath(target, "overrides", "resource"), writeQuery(id, dryRun), o))
	return &r, raw, err
}

// DeleteResourceOverride resets one resource to git.
func (f *Fleet) DeleteResourceOverride(ctx context.Context, target, id string) (*OverrideWriteResult, json.RawMessage, error) {
	r, raw, err := decode[OverrideWriteResult](f.Do(ctx, "DELETE", configPath(target, "overrides", "resource"), writeQuery(id, false), nil))
	return &r, raw, err
}

// PutApplicationOverride tries another revision of an application.
func (f *Fleet) PutApplicationOverride(ctx context.Context, target string, o ApplicationOverride, dryRun bool) (*OverrideWriteResult, json.RawMessage, error) {
	r, raw, err := decode[OverrideWriteResult](f.Do(ctx, "PUT", configPath(target, "overrides", "application"), writeQuery("", dryRun), o))
	return &r, raw, err
}

// DeleteApplicationOverride goes back to the application's own revision.
func (f *Fleet) DeleteApplicationOverride(ctx context.Context, target, repoURL, path string) (*OverrideWriteResult, json.RawMessage, error) {
	q := url.Values{"repo_url": {repoURL}, "path": {path}}
	r, raw, err := decode[OverrideWriteResult](f.Do(ctx, "DELETE", configPath(target, "overrides", "application"), q, nil))
	return &r, raw, err
}

// DeleteAllOverrides resets every override of the target.
func (f *Fleet) DeleteAllOverrides(ctx context.Context, target string) (*OverrideWriteResult, json.RawMessage, error) {
	r, raw, err := decode[OverrideWriteResult](f.Do(ctx, "DELETE", configPath(target, "overrides"), nil, nil))
	return &r, raw, err
}

// RemoveLocalItem deletes a local item (an entry a tool such as Devstudio wrote straight to the
// device's manifest) from a robot or device. id is Kind/namespace/name. With dryRun nothing is
// removed; the result says what would be.
func (f *Fleet) RemoveLocalItem(ctx context.Context, target, id string, dryRun bool) (*LocalRemoveResult, json.RawMessage, error) {
	r, raw, err := decode[LocalRemoveResult](f.Do(ctx, "DELETE", configPath(target, "local"), writeQuery(id, dryRun), nil))
	return &r, raw, noLocalAPI(err, featureLocalRemove)
}

// Promote returns the overlay YAML and GitHub links to commit a resource's override to git.
func (f *Fleet) Promote(ctx context.Context, target, id, to string) (*PromoteResult, json.RawMessage, error) {
	q := url.Values{"id": {id}}
	if to != "" {
		q.Set("to", to)
	}
	r, raw, err := decode[PromoteResult](f.Do(ctx, "GET", configPath(target, "promote"), q, nil))
	return &r, raw, err
}
