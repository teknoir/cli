package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// Go mirror of the bot API (GET whoami). A TeknoirBot is an edge computer that runs an AI agent;
// the agent reaches the platform with the bot's own key instead of a user's login.

// BotAccessNone is the bot's access when its creator may not do anything in the teamspace yet.
const BotAccessNone = "none"

// BotWhoami is the answer of GET <bot API>/whoami.
type BotWhoami struct {
	// Mode is "bot".
	Mode string `json:"mode"`
	Bot  struct {
		// Namespace is the bot's teamspace; Name the bot's name.
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
		// Unit is the organisation unit the bot is placed in; nil when it is not placed.
		Unit *string `json:"unit"`
		// Access is what the bot's creator may do here: none, read or creator.
		Access string `json:"access"`
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
