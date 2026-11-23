package hub

// The council snapshot: the council of one turn, as the platform built it at claim. The daemon
// reads it and checks it against its own catalog before it runs anything; unknown fields are
// ignored, and a semantic change of what a field means needs a proto bump.
//
// The JSON keys are the platform's, in its order (proto 2, council snapshot). The daemon echoes
// Snapshot.Hash and never recomputes it.

// Snapshot is the council of one turn.
type Snapshot struct {
	// Proto is the snapshot's protocol: 2.
	Proto int `json:"proto"`
	// Hash is "sha256:" and the hex digest of the snapshot without it.
	Hash string `json:"hash"`
	// Roster is the council's name and version; nil on a member turn.
	Roster *RosterRef `json:"roster"`
	// Level is the bot's effective access level at claim: none, read or creator.
	Level string `json:"level"`
	// Thorough is the turn's Thorough mode (options.mode council on the wire).
	Thorough bool `json:"thorough"`
	// Unattended is a routine's turn: always false in Stage 1.
	Unattended bool `json:"unattended"`
	// Budget lowers the turn's token cap; nil on a member turn.
	Budget *RosterBudget `json:"budget"`
	// Lead leads the turn; nil on a member turn.
	Lead *RoleSnapshot `json:"lead"`
	// Members are the turn's members, in roster order; a member turn has exactly one.
	Members []RoleSnapshot `json:"members"`
	// Reviewer checks the members' reports; nil on a member turn.
	Reviewer *RoleSnapshot `json:"reviewer"`
}

// RosterRef names the council a turn's snapshot came from.
type RosterRef struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Version     int    `json:"version"`
}

// RosterBudget is a roster's own token cap for the turn.
type RosterBudget struct {
	MaxTokens int `json:"maxTokens"`
}

// RoleBudget is what one member may spend.
type RoleBudget struct {
	MaxToolRounds int `json:"maxToolRounds"`
	MaxTokens     int `json:"maxTokens"`
}

// RoleSnapshot is one role of a turn's council, with everything the daemon needs to run it.
type RoleSnapshot struct {
	ID           string      `json:"id"`
	Version      int         `json:"version"`
	Slug         string      `json:"slug"`
	Name         string      `json:"name"`
	Persona      *string     `json:"persona"`
	Description  string      `json:"description"`
	Instructions string      `json:"instructions"`
	Kind         string      `json:"kind"`
	Runtime      string      `json:"runtime"`
	Model        string      `json:"model"`
	Effort       string      `json:"effort"`
	Level        string      `json:"level"`
	Groups       []string    `json:"groups"`
	Tools        []string    `json:"tools"`
	Results      bool        `json:"results"`
	Skills       []string    `json:"skills"`
	Budget       *RoleBudget `json:"budget"`
}
