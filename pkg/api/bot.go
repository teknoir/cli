package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// Go mirror of the bot API (GET whoami). A TeknoirBot is an edge computer that runs an AI agent;
// the agent reaches the platform with the bot's own key instead of a user's login.

// The bot's access levels. none: the bot may do nothing in the teamspace yet; read: it may view;
// creator: it acts with its creator's permissions.
const (
	BotAccessNone    = "none"
	BotAccessRead    = "read"
	BotAccessCreator = "creator"
)

// BotWhoami is the answer of GET <bot API>/whoami.
type BotWhoami struct {
	// Mode is "bot".
	Mode string `json:"mode"`
	Bot  struct {
		// Namespace is the bot's teamspace; Name the bot's name.
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
		// Unit is the name of the organisation unit the bot is placed in; nil when it is not placed.
		Unit *string `json:"unit"`
		// UnitID is that unit's id, the one `tnctl robot add --unit` takes; nil when the bot is not
		// placed (and from a platform that predates it).
		UnitID *string `json:"unitId"`
		// Access is what the bot may do here: none, read or creator. It is read when the bot is set to
		// creator but its creator may only view.
		Access string `json:"access"`
		// ConfiguredAccess is the level its creator set; empty from a platform that predates it.
		ConfiguredAccess string `json:"configuredAccess"`
	} `json:"bot"`
	Creator struct {
		// Email is the person the bot works for.
		Email string `json:"email"`
	} `json:"creator"`
	// Domain is informational.
	Domain string `json:"domain"`
}

// ErrNotBotFleet is returned by BotWhoami for a fleet client that is not a bot's.
var ErrNotBotFleet = errors.New("the whoami route of the bot API is available in bot mode only")

// BotWhoami asks the bot API who this tnctl is: which bot it runs as, for which person and with what
// access. It answers ErrNotBotFleet outside bot mode.
func (f *Fleet) BotWhoami(ctx context.Context) (*BotWhoami, json.RawMessage, error) {
	if !f.bot {
		return nil, nil, ErrNotBotFleet
	}
	token, err := f.Token(ctx)
	if err != nil {
		return nil, nil, err
	}
	u := strings.TrimSuffix(f.rootURL(), "/fleet") + "/whoami"
	raw, err := doRaw(ctx, "GET", u, token, nil)
	out, raw, err := decode[BotWhoami](raw, err)
	return &out, raw, err
}
