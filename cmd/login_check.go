package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
	tnauth "github.com/teknoir/cli/pkg/auth"
	"github.com/teknoir/cli/pkg/config"
	"golang.org/x/oauth2"
)

// runLoginCheck is `tnctl login --check`: it verifies the stored login without a browser. Exit 0
// when the access token is valid or a refresh works; the session-expired error (exit 6) when the
// login is dead. It prints the user and the expiry times, never a token.
func runLoginCheck(cmd *cobra.Command) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	st, err := tnauth.Check(cmd.Context(), cfg.Domain)
	if err != nil {
		return err
	}
	return writeLoginCheck(cmd.OutOrStdout(), st, outputFlag)
}

func writeLoginCheck(w io.Writer, st *tnauth.Status, format string) error {
	if format == "json" {
		b, err := json.Marshal(st)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(b))
		return err
	}
	who := st.User
	if who == "" {
		who = "an unknown user"
	}
	how := "access token valid"
	if st.Refreshed {
		how = "renewed just now, access token valid"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Logged in to %s as %s (%s until %s).\n", st.Domain, who, how, st.AccessExpires.Local().Format(time.RFC3339))
	switch {
	case st.RefreshExpires != nil:
		fmt.Fprintf(&sb, "The login can be renewed until %s.\n", st.RefreshExpires.Local().Format(time.RFC3339))
	case st.Offline:
		sb.WriteString("The login is an offline session: it has no fixed expiry and ends when it is revoked or has been idle for the realm's offline idle timeout (30 days by default).\n")
	default:
		sb.WriteString("The expiry of the login is not known.\n")
	}
	_, err := io.WriteString(w, sb.String())
	return err
}

// missingRefreshTokenWarning is the text to show when the token response has no refresh token
// (the offline_access scope was not granted), or "" when it has one.
func missingRefreshTokenWarning(token *oauth2.Token, clientID string) string {
	if token.RefreshToken != "" {
		return ""
	}
	return fmt.Sprintf("Warning: the login response has no refresh token (the offline_access scope was not granted), so this login cannot be renewed and ends when the access token expires (%s). Ask a platform admin to allow offline_access for the %s client.",
		time.Until(token.Expiry).Round(time.Second), clientID)
}
