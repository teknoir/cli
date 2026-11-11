package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/teknoir/cli/pkg/api"
)

// botAPI is a fake bot API for tests: the routes live below the same prefix the platform uses,
// /api/device/bot/v1, and every request's bearer token is recorded.
type botAPI struct {
	t  *testing.T
	mu sync.Mutex
	// routes are matched on "METHOD <path after /api/device/bot/v1>".
	routes map[string]func(r rec) (int, string)
	reqs   []rec
	auths  []string
	srv    *httptest.Server
	// keyFile is the file the bot's key is read from; the tests rewrite it.
	keyFile string
}

// botKey1 and botKey2 are the keys the fake platform accepts, in order.
const (
	botKey1 = "bot-key-one"
	botKey2 = "bot-key-two"
)

func newBotAPI(t *testing.T) *botAPI {
	t.Helper()
	p := &botAPI{t: t, routes: map[string]func(rec) (int, string){}}
	dir := t.TempDir()
	p.keyFile = filepath.Join(dir, "bot-key")
	if err := os.WriteFile(p.keyFile, []byte(botKey1+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		path := strings.TrimPrefix(r.URL.EscapedPath(), "/api/device/bot/v1")
		rc := rec{Method: r.Method, Path: path, Query: r.URL.RawQuery, Body: string(b), Full: r.URL.EscapedPath(), UA: r.Header.Get("User-Agent")}
		auth := r.Header.Get("Authorization")
		p.mu.Lock()
		p.reqs = append(p.reqs, rc)
		p.auths = append(p.auths, auth)
		h := p.routes[r.Method+" "+path]
		p.mu.Unlock()
		if auth != "Bearer "+botKey1 && auth != "Bearer "+botKey2 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"unauthorized","message":"The bot's key was refused."}`))
			return
		}
		if h == nil {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"not_found","message":"no route ` + r.Method + " " + path + `"}`))
			return
		}
		status, body := h(rc)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *botAPI) on(method, path string, status int, body string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.routes[method+" "+path] = func(rec) (int, string) { return status, body }
}

func (p *botAPI) onFunc(method, path string, f func(rec) (int, string)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.routes[method+" "+path] = f
}

// seen returns the requests with this method and path.
func (p *botAPI) seen(method, path string) []rec {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []rec
	for _, r := range p.reqs {
		if r.Method == method && r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

// count is how many requests the fake platform saw.
func (p *botAPI) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.reqs)
}

// writeKey replaces the bot's key file.
func (p *botAPI) writeKey(t *testing.T, content string) {
	t.Helper()
	if err := os.WriteFile(p.keyFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// botEnv makes the environment of a test that of a TeknoirBot pointed at p, and returns the real
// env (so the commands take the real bot-mode path). A $HOME/.tnctl.yaml naming another domain and
// namespace is present: bot mode must ignore it.
func botEnv(t *testing.T, p *botAPI) *cliEnv {
	t.Helper()
	return botEnvWithConfig(t, p, "domain: other.example\nauths:\n  other_example:\n    access_token: not-this\n    namespace: someone-else\n")
}

// botEnvWithConfig is botEnv with that .tnctl.yaml in $HOME; cfg "" leaves $HOME empty of one.
func botEnvWithConfig(t *testing.T, p *botAPI, cfg string) *cliEnv {
	t.Helper()
	home := t.TempDir()
	if cfg != "" {
		if err := os.WriteFile(filepath.Join(home, ".tnctl.yaml"), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv(envBotAPI, p.srv.URL+"/api/device/bot/v1")
	t.Setenv(envBotKeyFile, p.keyFile)
	t.Setenv(envBotNamespace, "teknoir-demo")
	t.Setenv(envBotDomain, "teknoir.cloud")
	t.Setenv("TNCTL_NAMESPACE", "")
	return &cliEnv{
		fleet:        defaultEnv.fleet,
		domainFleet:  defaultEnv.domainFleet,
		now:          time.Now,
		sleep:        realSleep,
		pollInterval: time.Millisecond,
	}
}

// noBotMode clears the bot mode variables for a test.
func noBotMode(t *testing.T) {
	t.Helper()
	t.Setenv(envBotAPI, "")
	t.Setenv(envBotKeyFile, "")
	t.Setenv(envBotNamespace, "")
	t.Setenv(envBotDomain, "")
}

const botWhoamiBody = `{"mode":"bot","bot":{"namespace":"teknoir-demo","name":"scout","unit":"Karlstad","unitId":"se-karlstad",
"access":"read","configuredAccess":"read"},
"creator":{"email":"a@b.c"},"domain":"teknoir.cloud"}`

// ---------- bot mode plumbing ----------

func TestBotModeCallsTheBotAPIWithTheBotKey(t *testing.T) {
	p := newBotAPI(t)
	p.on("GET", "/fleet/teknoir-demo/robots", 200, `[{"name":"scout","namespace":"teknoir-demo","type":"vision","device":"scout","phase":"Online"}]`)
	e := botEnv(t, p)
	capturePterm(t)

	if _, _, err := runCLI(t, e, "robots"); err != nil {
		t.Fatal(err)
	}
	got := p.seen("GET", "/fleet/teknoir-demo/robots")
	if len(got) != 1 {
		t.Fatalf("requests: %+v", p.reqs)
	}
	if a := p.auths[0]; a != "Bearer "+botKey1 {
		t.Errorf("Authorization %q, want the key file's content trimmed", a)
	}
	if got[0].UA != api.UserAgent() {
		t.Errorf("User-Agent %q", got[0].UA)
	}

	// The platform replaces the key file: the next request must use the new key.
	p.writeKey(t, "  "+botKey2+"\n\n")
	if _, _, err := runCLI(t, e, "robots"); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	last := p.auths[len(p.auths)-1]
	p.mu.Unlock()
	if last != "Bearer "+botKey2 {
		t.Errorf("Authorization %q after the key file changed", last)
	}
}

func TestBotModeMissingOrEmptyKeySendsNothing(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, p *botAPI)
	}{
		{"missing", func(t *testing.T, p *botAPI) { _ = os.Remove(p.keyFile) }},
		{"empty", func(t *testing.T, p *botAPI) { p.writeKey(t, "  \n\t\n") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newBotAPI(t)
			p.on("GET", "/fleet/teknoir-demo/robots", 200, `[]`)
			e := botEnv(t, p)
			c.setup(t, p)
			capturePterm(t)
			_, _, err := runCLI(t, e, "robots")
			if err == nil {
				t.Fatal("want an error")
			}
			want := "the bot key is missing or empty (" + p.keyFile + "): this computer predates the agent; re-create the bot"
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q, want %q", err, want)
			}
			if n := p.count(); n != 0 {
				t.Errorf("%d requests were sent", n)
			}
			if strings.Contains(err.Error(), botKey1) {
				t.Error("the error names the key")
			}
		})
	}
}

func TestBotModeIgnoresTheUserConfig(t *testing.T) {
	p := newBotAPI(t)
	p.on("GET", "/fleet/teknoir-demo/robots", 200, `[]`)
	e := botEnv(t, p) // sets $HOME to a config naming other.example / someone-else
	capturePterm(t)
	// -n teknoir-demo (the bot's own) and no user login at all: it still works.
	if _, _, err := runCLI(t, e, "robots", "-n", "teknoir-demo"); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	for _, r := range p.reqs {
		if !strings.HasPrefix(r.Full, "/api/device/bot/v1/fleet/teknoir-demo/") {
			t.Errorf("request outside the bot's teamspace: %s %s", r.Method, r.Full)
		}
	}
	p.mu.Unlock()
}

func TestBotModeNamespaceMustBeTheBotsOwn(t *testing.T) {
	p := newBotAPI(t)
	p.on("GET", "/fleet/teknoir-demo/robots", 200, `[]`)
	e := botEnv(t, p)
	capturePterm(t)
	for _, env := range []string{"same", "different"} {
		args := []string{"robots"}
		if env == "different" {
			args = append(args, "-n", "someone-else")
		}
		_, _, err := runCLI(t, e, args...)
		if env == "same" {
			if err != nil {
				t.Fatalf("the bot's own teamspace: %v", err)
			}
			continue
		}
		if err == nil || exitCodeOf(err) != codeUsage || !strings.Contains(err.Error(), "a TeknoirBot works only in its own teamspace (teknoir-demo)") {
			t.Errorf("exit %d: %v", exitCodeOf(err), err)
		}
	}
	if n := p.count(); n != 1 {
		t.Errorf("%d requests, want only the one for the bot's own teamspace", n)
	}
	// TNCTL_NAMESPACE counts too.
	t.Setenv("TNCTL_NAMESPACE", "someone-else")
	if _, _, err := runCLI(t, e, "robots"); err == nil || exitCodeOf(err) != codeUsage {
		t.Errorf("TNCTL_NAMESPACE: exit %d: %v", exitCodeOf(err), err)
	}
}

// runRealRoot runs args through the real command tree (rootCmd), the ones bot mode refuses are
// package level commands that runCLIWith does not add.
func runRealRoot(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errb)
	rootCmd.SetIn(strings.NewReader(""))
	// The flags write package level variables: take them before the flags are parsed.
	oldNS, oldOut, oldDomain, oldCfg := namespace, outputFlag, domain, cfgFile
	rootCmd.SetArgs(args)
	c, err := rootCmd.ExecuteContextC(context.Background())
	rootCmd.SetArgs(nil)
	rootCmd.SetOut(nil)
	rootCmd.SetErr(nil)
	// A flag set by one test stays "changed" on the shared tree unless it is put back: a test that
	// sets --namespace must not change what the next one sees.
	if c == nil {
		c = rootCmd
	}
	resetFlagSet(t, c.Flags())
	resetFlagSet(t, rootCmd.PersistentFlags())
	namespace, outputFlag, domain, cfgFile = oldNS, oldOut, oldDomain, oldCfg
	return out.String(), errb.String(), err
}

// resetFlagSet puts every flag of fs back to its default and marks it unchanged. A slice or array
// flag's default is "[]", which Set would append to the value, so those are replaced; robot add's
// --label (key=value) refuses its own empty default, so it is cleared. Any other flag that cannot
// be reset fails the test: its value would leak into the next run.
func resetFlagSet(t *testing.T, fs *pflag.FlagSet) {
	t.Helper()
	fs.VisitAll(func(f *pflag.Flag) {
		if lf, ok := f.Value.(*labelFlags); ok {
			lf.labels = nil
		} else if strings.HasSuffix(f.Value.Type(), "Slice") || strings.HasSuffix(f.Value.Type(), "Array") {
			sv, ok := f.Value.(pflag.SliceValue)
			if !ok {
				t.Errorf("runRealRoot cannot reset --%s (%s): not a pflag.SliceValue", f.Name, f.Value.Type())
			} else if err := sv.Replace(nil); err != nil {
				t.Errorf("runRealRoot cannot reset --%s (%s): %v", f.Name, f.Value.Type(), err)
			}
		} else if err := f.Value.Set(f.DefValue); err != nil {
			t.Errorf("runRealRoot cannot reset --%s (%s): %v", f.Name, f.Value.Type(), err)
		}
		f.Changed = false
	})
}

func TestBotModeRefusesTheCommandsItMayNotRun(t *testing.T) {
	cases := [][]string{
		{"login"},
		{"ssh", "scout"},
		{"exec", "scout", "--", "true"},
		{"logs", "scout", "HelmChart/default/notebook"},
		{"port-forward", "scout"},
		{"socks-proxy", "scout", "1080"},
		{"wg", "status", "scout"},
		{"wg"},
		{"wg-proxy"},
		{"ansible", "all", "-m", "ping"},
		{"ansible-playbook", "site.yml"},
		{"ansible-inventory", "--list"},
		{"bootstrap", "scout"},
		{"add", "domain", "x.example"},
		{"ns", "someone-else"},
		{"domain"},
		{"chat", "scout", "hello"},
		{"chat", "threads", "scout"},
	}
	// chat's refusal names its own reason: a bot does not chat with itself.
	ownText := map[string]string{
		"chat": `"tnctl chat" is not available to a TeknoirBot: a bot does not use the person chat.`,
	}
	// The commands do more than tnctl when cobra hands them the arguments: none of them may run.
	for _, args := range cases {
		t.Run(args[0]+strings.Join(args[1:], " "), func(t *testing.T) {
			p := newBotAPI(t)
			botEnv(t, p)
			// If a command got past the refusal it would ask for the fleet: answer with an error,
			// so the test fails on the exit code instead of on a nil pointer.
			useDefaultEnv(t, &cliEnv{
				fleet: func() (*api.Fleet, string, string, error) {
					return nil, "", "", errors.New("the command ran")
				},
				domainFleet: func() (*api.Fleet, string, error) { return nil, "", errors.New("the command ran") },
			})
			_, errs, err := runRealRoot(t, args...)
			if err == nil {
				t.Fatalf("%v was not refused", args)
			}
			if exitCodeOf(err) != codeAuth {
				t.Errorf("exit %d, want %d", exitCodeOf(err), codeAuth)
			}
			// The refusal is the whole answer: no usage text after it.
			if strings.Contains(errs, "Usage:") {
				t.Errorf("stderr carries the usage: %q", errs)
			}
			path := args[0]
			if len(args) > 1 && args[0] == "wg" && strings.HasPrefix(args[1], "status") {
				path = "wg status"
			}
			if len(args) > 1 && args[0] == "add" {
				path = "add domain"
			}
			want := `"tnctl ` + path + `" is not available to a TeknoirBot: it cannot open tunnels or hold device logins. For logs use "tnctl inspect logs".`
			if own, ok := ownText[path]; ok {
				want = own
			}
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q, want %q", err, want)
			}
			if n := p.count(); n != 0 {
				t.Errorf("%d requests were sent", n)
			}
		})
	}
}

func TestBotModeAllowsListingTeamspaces(t *testing.T) {
	p := newBotAPI(t)
	p.on("GET", "/fleet/teamspaces", 200, `[{"name":"teknoir-demo","access":"read"}]`)
	e := botEnv(t, p)
	capturePterm(t)
	if _, _, err := runCLI(t, e, "ns", "--list"); err != nil {
		t.Fatal(err)
	}
	// Without a name a bot lists too: it has one teamspace and never writes the user's config.
	if _, _, err := runCLI(t, e, "ns"); err != nil {
		t.Fatal(err)
	}
	if n := p.count(); n != 2 {
		t.Errorf("%d requests", n)
	}
}

// A bot has $TEKNOIRBOT_API: the flags that point a person's tnctl at a platform are refused.
func TestBotModeRefusesDomainAndConfigFlags(t *testing.T) {
	p := newBotAPI(t)
	botEnv(t, p)
	otherConfig := filepath.Join(t.TempDir(), "tnctl.yaml")
	for _, args := range [][]string{
		{"robots", "-d", "teknoir.cloud"},
		{"robots", "--domain", "teknoir.cloud"},
		{"robots", "--config", otherConfig},
	} {
		_, _, err := runRealRoot(t, args...)
		if err == nil || exitCodeOf(err) != codeUsage || !strings.Contains(err.Error(), "bot mode uses $TEKNOIRBOT_API; --domain and --config are not used") {
			t.Errorf("%v: exit %d, %v", args, exitCodeOf(err), err)
		}
	}
	if n := p.count(); n != 0 {
		t.Errorf("%d requests were sent", n)
	}
}

// The bot's key never travels in clear text: a $TEKNOIRBOT_API off a loopback address must be https.
func TestBotModeRefusesAPlainHTTPBotAPI(t *testing.T) {
	p := newBotAPI(t)
	e := botEnv(t, p)
	t.Setenv(envBotAPI, "http://example.com/api/device/bot/v1")
	capturePterm(t)
	_, _, err := runCLI(t, e, "robots")
	if err == nil || exitCodeOf(err) != codeAuth || !strings.Contains(err.Error(), "TEKNOIRBOT_API must be https") {
		t.Errorf("exit %d, %v", exitCodeOf(err), err)
	}
	if n := p.count(); n != 0 {
		t.Errorf("%d requests were sent", n)
	}
}

// A bot may not open a device shell, so --wait and --wait=applied are refused before it reads or
// writes anything; --wait=published needs no shell and works.
func TestBotConfigWaitOnlyPublished(t *testing.T) {
	p := newBotAPI(t)
	e := botEnv(t, p)
	capturePterm(t)
	for _, wait := range []string{"--wait", "--wait=applied", "--wait=ready"} {
		_, _, err := runCLI(t, e, "config", "set", "scout", nbID, "replicas=2", wait)
		if err == nil || exitCodeOf(err) != codeAuth ||
			!strings.Contains(err.Error(), "a TeknoirBot cannot run commands on devices") ||
			!strings.Contains(err.Error(), "use --wait=published") {
			t.Errorf("%s: exit %d, %v", wait, exitCodeOf(err), err)
		}
	}
	if n := p.count(); n != 0 {
		t.Errorf("%d requests were sent", n)
	}

	// --wait=published goes through the bot API.
	p.on("GET", "/fleet/teknoir-demo/config/scout/resource", 200, jsonOf(t, notebookDetail(nil)))
	p.on("PATCH", "/fleet/teknoir-demo/config/scout/overrides/resource", 200, writeOK(nbID, "r2"))
	p.on("GET", "/fleet/teknoir-demo/config/scout", 200, jsonOf(t, viewAt("r2", "active", "")))
	out, errs, err := runCLI(t, e, "config", "set", "scout", nbID, "replicas=2", "--wait=published")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if !strings.Contains(errs, "published after") || !strings.Contains(out, "notebook") {
		t.Errorf("stdout %q, stderr %q", out, errs)
	}
	if n := len(p.seen("PATCH", "/fleet/teknoir-demo/config/scout/overrides/resource")); n != 1 {
		t.Errorf("%d writes", n)
	}
}

func TestBotFleetNeverAsksForDeviceLogins(t *testing.T) {
	// pkg/api's guard, through the real bot fleet: no request leaves.
	p := newBotAPI(t)
	fl := api.NewBotFleet(p.srv.URL+"/api/device/bot/v1", "teknoir-demo", p.keyFile)
	if _, err := fl.Credentials(nil, "scout"); err == nil {
		t.Error("Credentials answered")
	}
	if _, err := fl.CredentialsBulk(nil, []string{"scout"}); err == nil {
		t.Error("CredentialsBulk answered")
	}
	if n := p.count(); n != 0 {
		t.Errorf("%d requests were sent", n)
	}
}

// the bot's answer says nothing about the user's login: bot mode never reads it.
func TestBotModeNeverLoadsTheConfig(t *testing.T) {
	p := newBotAPI(t)
	e := botEnv(t, p)
	// no config file at all in $HOME
	t.Setenv("HOME", t.TempDir())
	if on, apiBase, ns, keyFile, err := botMode(); err != nil || !on {
		t.Fatalf("botMode: %v %v", on, err)
	} else if apiBase != p.srv.URL+"/api/device/bot/v1" || ns != "teknoir-demo" || keyFile != p.keyFile {
		t.Errorf("%q %q %q", apiBase, ns, keyFile)
	}
	fl, domain, ns, err := e.fleet()
	if err != nil || fl == nil || domain != "teknoir.cloud" || ns != "teknoir-demo" {
		t.Fatalf("%v %q %q", err, domain, ns)
	}
	if fl.Base != p.srv.URL+"/api/device/bot/v1/fleet/teknoir-demo" ||
		fl.Root != p.srv.URL+"/api/device/bot/v1/fleet" {
		t.Errorf("Base %q Root %q", fl.Base, fl.Root)
	}
	fl2, domain2, err := e.domainFleet()
	if err != nil || fl2 == nil || domain2 != "teknoir.cloud" {
		t.Fatalf("%v %q", err, domain2)
	}
}

// A bot without its teamspace cannot use the fleet, but version still works.
func TestBotModeWithoutNamespaceFailsFleetUseButNotVersion(t *testing.T) {
	p := newBotAPI(t)
	p.on("GET", "/fleet/teknoir-demo/robots", 200, `[]`)
	e := botEnv(t, p)
	t.Setenv(envBotNamespace, "")
	capturePterm(t)
	_, _, err := runCLI(t, e, "robots")
	if err == nil || exitCodeOf(err) != codeUsage || !strings.Contains(err.Error(), "TEKNOIRBOT_NAMESPACE is not set") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
	if n := p.count(); n != 0 {
		t.Errorf("%d requests were sent", n)
	}
}

// both variables are needed: one alone is not bot mode.
func TestBotModeNeedsBothVariables(t *testing.T) {
	cases := []struct{ api, key string }{{"", "k"}, {"https://x/api", ""}}
	for _, c := range cases {
		t.Setenv(envBotAPI, c.api)
		t.Setenv(envBotKeyFile, c.key)
		if on, _, _, _, err := botMode(); err != nil || on {
			t.Errorf("api=%q key=%q: on %v err %v", c.api, c.key, on, err)
		}
	}
}

// ---------- the config file is not bot mode's ----------

// On a fresh HOME a bot writes no config file and reads none: the answer on stdout is JSON only.
func TestBotModeFreshHomeWritesNoConfig(t *testing.T) {
	p := newBotAPI(t)
	p.on("GET", "/whoami", 200, botWhoamiBody)
	botEnvWithConfig(t, p, "") // $HOME with no .tnctl.yaml
	before := viper.ConfigFileUsed()

	out, _, err := runRealRoot(t, "whoami", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid([]byte(out)) {
		t.Errorf("stdout is not JSON on its own: %q", out)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".tnctl.yaml")); !os.IsNotExist(err) {
		t.Errorf("bot mode created %s", filepath.Join(home, ".tnctl.yaml"))
	}
	if got := viper.ConfigFileUsed(); got != before {
		t.Errorf("viper's config file changed from %q to %q", before, got)
	}
}

// A bot's $HOME may hold a config tnctl cannot read: bot mode never opens it.
func TestBotModeWithAnInvalidConfigStillAnswersJSON(t *testing.T) {
	p := newBotAPI(t)
	p.on("GET", "/whoami", 200, botWhoamiBody)
	botEnvWithConfig(t, p, ": : :\n")
	out, _, err := runRealRoot(t, "whoami", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid([]byte(out)) {
		t.Errorf("stdout is not JSON on its own: %q", out)
	}
}

// As a person on a fresh HOME, initConfig creates the default config file and says so: the notice
// goes to stderr, so stdout stays the command's data, also with -o json.
func TestWhoamiJSONOnAFreshHomeIsCleanStdout(t *testing.T) {
	noBotMode(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	// viper keeps the config and the overrides of earlier tests in memory: only a reset gives a
	// fresh HOME a fresh viper. With cfgFile empty, initConfig takes the default $HOME/.tnctl.yaml.
	oldCfg := cfgFile
	cfgFile = ""
	viper.Reset()
	bindViper()
	t.Cleanup(func() {
		cfgFile = oldCfg
		viper.Reset()
		bindViper()
	})

	oldErr := os.Stderr
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = pw
	out, _, runErr := runRealRoot(t, "whoami", "-o", "json")
	_ = pw.Close()
	os.Stderr = oldErr
	notice, _ := io.ReadAll(pr)

	if runErr == nil || exitCodeOf(runErr) != codeAuth {
		t.Fatalf("no login must exit %d: %v", codeAuth, runErr)
	}
	if out != "" && !json.Valid([]byte(out)) {
		t.Errorf("stdout is not JSON on its own: %q", out)
	}
	if !strings.Contains(string(notice), "Creating default configuration file at") {
		t.Errorf("the creation notice is not on stderr: %q", notice)
	}
	if _, err := os.Stat(filepath.Join(home, ".tnctl.yaml")); err != nil {
		t.Errorf("the default config was not created: %v", err)
	}
}

// config wait takes --for, and its refusal in bot mode says so.
func TestBotConfigWaitRefusalNamesItsOwnFlag(t *testing.T) {
	p := newBotAPI(t)
	e := botEnv(t, p)
	capturePterm(t)
	for _, args := range [][]string{
		{"config", "wait", "scout"},
		{"config", "wait", "scout", "--for", "applied"},
		{"config", "wait", "scout", "--for", "ready"},
	} {
		_, _, err := runCLI(t, e, args...)
		if err == nil || exitCodeOf(err) != codeAuth || !strings.Contains(err.Error(), "use --for=published") {
			t.Errorf("%v: exit %d, %v", args, exitCodeOf(err), err)
		}
	}
	if n := p.count(); n != 0 {
		t.Errorf("%d requests were sent", n)
	}

	// --for=published needs no device shell: it goes through the bot API.
	p.on("GET", "/fleet/teknoir-demo/config/scout", 200, jsonOf(t, standardView()))
	out, errs, err := runCLI(t, e, "config", "wait", "scout", "--for", "published")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if !strings.Contains(out, "published") {
		t.Errorf("stdout %q", out)
	}
	// The target is read before the wait and polled for the state: at least one request reached the
	// bot API, and none was refused.
	if n := len(p.seen("GET", "/fleet/teknoir-demo/config/scout")); n < 1 {
		t.Errorf("no request reached the bot API: %+v", p.reqs)
	}
}
