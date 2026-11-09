package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// apiMethods are the methods `tnctl api` sends.
var apiMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true,
}

// errAPICredentials is the refusal of a device-login route.
func errAPICredentials() error {
	return usageErr("tnctl api does not call a device's login route: a device login is a secret tnctl never prints. Read a robot or device with tnctl robot, tnctl device or tnctl config")
}

// errAPIPath is the refusal of a path that is not one route below the fleet root.
func errAPIPath() error {
	return usageErr("the path must be relative to the fleet API, like teknoir-demo/robots")
}

// cleanAPIPath checks a path below the fleet root and returns it ready for the request: every
// segment re-escaped, followed by the query as given. The dots, the slashes and the escapes a shell
// or a caller can smuggle in are refused, not sent: they would reach another route than the one
// named (../../etc, a//b, credentials%2f., a segment with a stray ;).
func cleanAPIPath(p string) (string, error) {
	raw, query := p, ""
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		raw = p[:i]
		if p[i] == '?' {
			query = p[i:]
			if j := strings.IndexByte(query, '#'); j >= 0 {
				query = query[:j] // the fragment is not sent
			}
		}
	}
	path, err := url.PathUnescape(raw)
	if err != nil {
		return "", usageErr("the path is not a valid URL path (%v); it must be relative to the fleet API, like teknoir-demo/robots", err)
	}
	path = strings.TrimSuffix(path, "/")
	if strings.ContainsAny(path, "\\") || strings.HasPrefix(path, "/") || strings.Contains(path, "://") {
		return "", errAPIPath()
	}
	segs := strings.Split(path, "/")
	for _, seg := range segs {
		if seg == "" || seg == "." || seg == ".." || strings.Contains(seg, ";") {
			return "", errAPIPath()
		}
		if strings.EqualFold(seg, "credentials") {
			return "", errAPICredentials()
		}
	}
	escaped := make([]string, len(segs))
	for i, seg := range segs {
		escaped[i] = url.PathEscape(seg)
	}
	return strings.Join(escaped, "/") + query, nil
}

// looksLikeADataArg reports whether a --domain value is a request body, the mistake of writing -d for
// --data: -d is the root's --domain shorthand.
func looksLikeADataArg(s string) bool {
	return s == "-" || strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") || strings.HasPrefix(s, "@")
}

// readAPIData reads the request body: JSON text, @file, or - for stdin.
func readAPIData(data string, stdin io.Reader) (json.RawMessage, error) {
	if strings.HasPrefix(data, "@") {
		file := data[1:]
		if file == "" {
			return nil, usageErr("--data @ needs a file name")
		}
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, usageErr("cannot read --data file %s: %v", file, err)
		}
		data = string(b)
	} else if data == "-" {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("cannot read the request body from stdin: %w", err)
		}
		data = string(b)
	}
	if !json.Valid([]byte(data)) {
		return nil, usageErr("--data must be JSON, for example --data '{\"name\":\"x\"}', --data @file.json or --data - (stdin)")
	}
	return json.RawMessage(data), nil
}

func newAPICmd(e *cliEnv) *cobra.Command {
	var data string
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "api <METHOD> <path> [--data JSON|@file|-]",
		Short:        "Call the fleet API directly: METHOD and a path below the fleet root",
		Long: `Call the fleet API directly and print the JSON it answers, indented.

<path> is relative to the fleet root and names the teamspace, for example
"teknoir-demo/robots" or "teknoir-demo/robots/vision-station-01". It works as a person and
inside a TeknoirBot (bot mode), where the fleet root is the bot API's.

<METHOD> is GET, POST, PUT, PATCH or DELETE. --data sends a JSON body: JSON text, @file or - for
stdin; -d is the global --domain, not --data. A path below the fleet root has no ".", "..", empty
or "credentials" segment: a device's login is a secret tnctl never prints. The query is sent as
written.

This is a low-level tool for routes no command covers yet; the commands are safer because they
check their arguments.`,
		Example: `  tnctl api GET teknoir-demo/robots
  tnctl api GET teknoir-demo/robots/vision-station-01
  tnctl api POST teknoir-demo/robots --data '{"name":"amr-02","type":"amr"}'
  tnctl api PATCH teknoir-demo/config/vs/overrides/resource?id=HelmChart%2Fdefault%2Fnotebook --data @patch.json`,
		Args: argsRange(2, 2, "api needs a method and a path, for example: tnctl api GET teknoir-demo/robots"),
		RunE: func(cmd *cobra.Command, args []string) error {
			method := strings.ToUpper(args[0])
			if !apiMethods[method] {
				return usageErr("api sends GET, POST, PUT, PATCH or DELETE, not %q", args[0])
			}
			// -d is the root's --domain shorthand: a body written with it reaches nothing.
			if looksLikeADataArg(domain) {
				return usageErr("-d is --domain; send a body with --data")
			}
			path, err := cleanAPIPath(args[1])
			if err != nil {
				return err
			}
			var body any
			if data != "" {
				raw, err := readAPIData(data, cmd.InOrStdin())
				if err != nil {
					return err
				}
				body = raw
			}
			fl, _, _, err := e.targetClient()
			if err != nil {
				return err
			}
			raw, err := fl.DoRoot(cmd.Context(), method, path, body)
			if err != nil {
				return classify(err)
			}
			if len(strings.TrimSpace(string(raw))) == 0 {
				return nil
			}
			return writeStructured(cmd.OutOrStdout(), "json", raw)
		},
	}
	c.Flags().StringVar(&data, "data", "", "JSON body: JSON text, @file, or - for stdin (-d is --domain, not --data)")
	return c
}
