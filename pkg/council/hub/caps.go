package hub

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The capability headers, on every K1 (proto 2). The daemon announces what its computer can do, so
// the platform can check a turn's council against it before it queues the turn.
const (
	// HeaderProto is the protocol of the K1 handshake: 2 when the bot announces its capabilities.
	HeaderProto = "X-Teknoir-Council-Proto"
	// HeaderKit is the version of the agent kit installed on the bot's computer.
	HeaderKit = "X-Teknoir-Council-Kit"
	// HeaderTools is the comma-separated, sorted names of the MCP tools the computer offers.
	HeaderTools = "X-Teknoir-Council-Tools"
	// HeaderSkills is the comma-separated names of the guides the computer has, kit order first. An
	// empty value means none.
	HeaderSkills = "X-Teknoir-Council-Skills"
)

// The limits of the capability headers.
const (
	// proto2 is the only protocol a bot with capabilities announces.
	proto2 = 2
	// maxToolNames and maxSkillNames are how many names a header may carry.
	maxToolNames  = 128
	maxSkillNames = 64
	// maxToolsHeader and maxSkillsHeader are the most bytes each joined header may take.
	maxToolsHeader  = 4096
	maxSkillsHeader = 2048
)

var (
	kitName   = regexp.MustCompile(`^[0-9A-Za-z.+-]{1,40}$`)
	toolName  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	skillName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
)

// Caps are what a bot's computer can do: the protocol, the kit it installed, the tools its MCP
// server offers and the guides it carries.
type Caps struct {
	// Proto is the handshake protocol: 2.
	Proto int
	// Kit is the agent kit's version, as agentkit.Version.
	Kit string
	// Tools are the names of the MCP tools the computer offers. They are sent sorted.
	Tools []string
	// Skills are the guides the computer has, in the order the kit lists them. They are sent as
	// given, and an empty list sends an empty header.
	Skills []string
}

// SetCaps validates caps and stores them for every later Next. It returns an error, and stores
// nothing, when a value breaks one of the protocol's rules; the daemon then runs without caps, as
// proto 1.
func (c *Client) SetCaps(caps Caps) error {
	hdr, err := headersOf(caps)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.caps = &capsSet{hdr: hdr}
	c.mu.Unlock()
	return nil
}

// headersOf validates caps and returns the headers a K1 sends for them.
func headersOf(caps Caps) (http.Header, error) {
	if caps.Proto != proto2 {
		return nil, fmt.Errorf("proto is %d, and the only protocol with capabilities is %d", caps.Proto, proto2)
	}
	if !kitName.MatchString(caps.Kit) {
		return nil, fmt.Errorf("the kit %q is not a version (letters, digits, '.', '+' and '-' only, 1 to 40 of them)", cutCapsText(caps.Kit))
	}
	tools, err := toolNames(caps.Tools)
	if err != nil {
		return nil, err
	}
	skills, err := skillNames(caps.Skills)
	if err != nil {
		return nil, err
	}
	h := http.Header{}
	h.Set(HeaderProto, strconv.Itoa(caps.Proto))
	h.Set(HeaderKit, caps.Kit)
	h.Set(HeaderTools, strings.Join(tools, ","))
	h.Set(HeaderSkills, strings.Join(skills, ","))
	return h, nil
}

// toolNames validates the tool names and returns them sorted.
func toolNames(names []string) ([]string, error) {
	if len(names) < 1 || len(names) > maxToolNames {
		return nil, fmt.Errorf("%d tool names, and the protocol takes 1 to %d", len(names), maxToolNames)
	}
	out := make([]string, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if !toolName.MatchString(name) {
			return nil, fmt.Errorf("the tool %q is not a tool name (a lower-case letter, then lower-case letters, digits and '_')", cutCapsText(name))
		}
		if seen[name] {
			return nil, fmt.Errorf("the tool %q is named twice", cutCapsText(name))
		}
		seen[name] = true
		out = append(out, name)
	}
	slices.Sort(out)
	if n := len(strings.Join(out, ",")); n > maxToolsHeader {
		return nil, fmt.Errorf("the tools make a %d-byte header, and the protocol takes at most %d bytes", n, maxToolsHeader)
	}
	return out, nil
}

// skillNames validates the guide names and returns them as given.
func skillNames(names []string) ([]string, error) {
	if len(names) > maxSkillNames {
		return nil, fmt.Errorf("%d skill names, and the protocol takes at most %d", len(names), maxSkillNames)
	}
	out := make([]string, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if !skillName.MatchString(name) {
			return nil, fmt.Errorf("the skill %q is not a skill name (a lower-case letter, then lower-case letters, digits and '-')", cutCapsText(name))
		}
		if seen[name] {
			return nil, fmt.Errorf("the skill %q is named twice", cutCapsText(name))
		}
		seen[name] = true
		out = append(out, name)
	}
	if n := len(strings.Join(out, ",")); n > maxSkillsHeader {
		return nil, fmt.Errorf("the skills make a %d-byte header, and the protocol takes at most %d bytes", n, maxSkillsHeader)
	}
	return out, nil
}

// cutCapsText is s cut for an error message: at most 64 bytes, at a rune start.
func cutCapsText(s string) string {
	if len(s) <= 64 {
		return s
	}
	n := 64
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n] + "…"
}

// capsSet is the headers of one SetCaps, prepared once and never changed.
type capsSet struct {
	hdr http.Header
}

// capsHeader is the headers a K1 sends, or nil when no caps were set. The caller copies them into
// its request, so the returned header is only read here.
func (c *Client) capsHeader() http.Header {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.caps == nil {
		return nil
	}
	return c.caps.hdr
}
