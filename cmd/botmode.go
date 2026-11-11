package cmd

import (
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/teknoir/cli/pkg/api"
)

// Environment variables of bot mode. Bot mode is on when both of the first two are set: the computer
// runs a TeknoirBot, whose AI agent uses tnctl with the bot's own key instead of a user's login.
const (
	envBotKeyFile   = "TNCTL_BOT_KEY_FILE"
	envBotAPI       = "TEKNOIRBOT_API"
	envBotNamespace = "TEKNOIRBOT_NAMESPACE"
	envBotDomain    = "TEKNOIR_DOMAIN"
)

// botEnabled reports whether this tnctl runs in bot mode, and its settings: the bot API's base, the
// bot's teamspace and the file the bot's key is read from. Bot mode is on when both the bot API and
// the key file are configured; the teamspace is then required (botMode checks it).
func botEnabled() (on bool, apiBase, ns, keyFile string) {
	apiBase = strings.TrimRight(strings.TrimSpace(os.Getenv(envBotAPI)), "/")
	keyFile = strings.TrimSpace(os.Getenv(envBotKeyFile))
	ns = strings.TrimSpace(os.Getenv(envBotNamespace))
	return apiBase != "" && keyFile != "", apiBase, ns, keyFile
}

// botMode is botEnabled with the teamspace required: a bot works only in its own, and a command that
// needs the fleet cannot work without it.
func botMode() (on bool, apiBase, ns, keyFile string, err error) {
	on, apiBase, ns, keyFile = botEnabled()
	if !on {
		return false, "", "", "", nil
	}
	if ns == "" {
		return false, "", "", "", usageErr("%s is not set: a TeknoirBot works only in its own teamspace", envBotNamespace)
	}
	return true, apiBase, ns, keyFile, nil
}

// botFleet is the fleet client of a bot: the bot API's fleet routes for the bot's own teamspace,
// authenticated with the bot's key. It reads no login and no config file.
func botFleet(apiBase, ns, keyFile string) *api.Fleet {
	return api.NewBotFleet(apiBase, ns, keyFile)
}

// botDomain is the domain bot mode reports: informational ($TEKNOIR_DOMAIN), the bot API's host when
// that is not set.
func botDomain() string {
	if d := strings.TrimSpace(os.Getenv(envBotDomain)); d != "" {
		return d
	}
	apiBase := strings.TrimRight(strings.TrimSpace(os.Getenv(envBotAPI)), "/")
	if i := strings.Index(apiBase, "://"); i >= 0 {
		host, _, _ := strings.Cut(apiBase[i+3:], "/")
		if h, _, ok := strings.Cut(host, ":"); ok {
			return h
		}
		return host
	}
	return ""
}

// checkBotNamespace refuses a -n/--namespace (or TNCTL_NAMESPACE) that names another teamspace than
// the bot's: a bot works only in its own.
func checkBotNamespace(want string) error {
	got := namespace
	if got == "" {
		got = os.Getenv("TNCTL_NAMESPACE")
	}
	if got != "" && got != want {
		return usageErr("a TeknoirBot works only in its own teamspace (%s)", want)
	}
	return nil
}

// botNoDeviceShell is the refusal of a device shell in bot mode: --wait=applied and --wait=ready need
// one, --wait=published does not. flag is the flag the level came from (--wait, or --for of config
// wait) and is named in the message; level is the wait mode the caller knows, "" when it has none.
func botNoDeviceShell(flag string, level waitLevel) error {
	if level == "" {
		return newErr(codeAuth, "a TeknoirBot cannot run commands on devices: %s=applied and %s=ready need a device shell; use %s=published", flag, flag, flag)
	}
	return newErr(codeAuth, "a TeknoirBot cannot run commands on devices: %s=%s needs a device shell; use %s=published", flag, level, flag)
}

// botRefused are the commands a TeknoirBot may not run, matched on the command path ("wg status") and
// every path below it. They open tunnels, hold device logins or change what this computer is.
var botRefused = map[string]bool{
	"login":             true,
	"ssh":               true,
	"exec":              true,
	"logs":              true,
	"port-forward":      true,
	"socks-proxy":       true,
	"wg":                true,
	"wg-proxy":          true,
	"ansible":           true,
	"ansible-playbook":  true,
	"ansible-inventory": true,
	"bootstrap":         true,
	"add":               true,
	"domain":            true,
	"chat":              true,
}

// botRefusedText is the refusal's own text, for the commands whose reason is not a tunnel or a device
// login. Every other refused command gets botRefusedError's.
var botRefusedText = map[string]string{
	"chat": `"tnctl chat" is not available to a TeknoirBot: a bot does not use the person chat.`,
}

// botRefusedError is the refusal of such a command: exit code 6, the same as "not allowed". The
// text is looked up by the command's first word, so "chat threads" gets chat's.
func botRefusedError(path string) error {
	first, _, _ := strings.Cut(path, " ")
	if text, ok := botRefusedText[first]; ok {
		return newErr(codeAuth, "%s", text)
	}
	return newErr(codeAuth, "%q is not available to a TeknoirBot: it cannot open tunnels or hold device logins. For logs use \"tnctl inspect logs\".", "tnctl "+path)
}

// botPreRun refuses the commands of botRefused in bot mode. It runs as rootCmd's PersistentPreRunE,
// so it sees the command that was found before any of it runs.
func botPreRun(cmd *cobra.Command, args []string) error {
	// Without its teamspace a bot cannot use the fleet, and botMode says so when a command asks for
	// it; commands that need no fleet (version, help) still work.
	on, _, ns, _ := botEnabled()
	if !on {
		return nil
	}
	// --domain and --config point a person's tnctl at a platform; a bot has $TEKNOIRBOT_API and its key.
	if cmd.Flags().Changed("domain") || cmd.Flags().Changed("config") {
		return usageErr("bot mode uses $TEKNOIRBOT_API; --domain and --config are not used")
	}
	if ns != "" {
		if err := checkBotNamespace(ns); err != nil {
			return err
		}
	}
	path := commandPath(cmd)
	// cobra hands the subcommand's own arguments, or all of them when the command parses no flags.
	if len(args) == 0 {
		args = cmd.Flags().Args()
	}
	first, _, _ := strings.Cut(path, " ")
	if path != "" && (botRefused[first] || (first == "ns" && len(args) > 0)) {
		// The refusal is the whole answer: no usage text after it.
		cmd.SilenceUsage = true
		return botRefusedError(path)
	}
	return nil
}

// commandPath is the command names below the root ("wg status"), without arguments.
func commandPath(cmd *cobra.Command) string {
	var names []string
	for c := cmd; c != nil && c.Parent() != nil; c = c.Parent() {
		name, _, _ := strings.Cut(c.Name(), " ")
		names = append([]string{name}, names...)
	}
	return strings.Join(names, " ")
}
