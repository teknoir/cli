package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/teknoir/cli/pkg/api"
)

func TestSplitCommand(t *testing.T) {
	for in, want := range map[string][]string{
		"vi":                               {"vi"},
		"code --wait":                      {"code", "--wait"},
		`  emacsclient   -t -a ""  `:       {"emacsclient", "-t", "-a", ""},
		`'/opt/my editor/bin/ed' --wait`:   {"/opt/my editor/bin/ed", "--wait"},
		`"/opt/my editor/ed" --flag="a b"`: {"/opt/my editor/ed", "--flag=a b"},
		`/opt/my\ editor/ed`:               {"/opt/my editor/ed"},
		`nano +5 -m; rm -rf /`:             {"nano", "+5", "-m;", "rm", "-rf", "/"},
		`ed $(touch /x)`:                   {"ed", "$(touch", "/x)"},
		`sh -c 'echo "a  b"'`:              {"sh", "-c", `echo "a  b"`},
		`"a\"b" 'c\d'`:                     {`a"b`, `c\d`},
	} {
		got, err := splitCommand(in, true)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %q %v, want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{`ed 'x`, `ed "x`, `ed x\`} {
		if _, err := splitCommand(bad, true); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
	if got, _ := splitCommand(`C:\Program\ Files\code.exe --wait`, false); !reflect.DeepEqual(got, []string{`C:\Program\`, `Files\code.exe`, "--wait"}) {
		t.Errorf("windows paths keep their backslashes: %q", got)
	}
}

func TestEditHeaderRoundTrip(t *testing.T) {
	body := "kind: ConfigMap\ndata:\n  a: b\n"
	h := withEditHeader(body, []api.LocalValidationIssue{
		{Severity: "error", Message: "bad value", Path: "data.a", Line: 3, Column: 6},
		{Severity: "error", Message: "no position"},
	}, []string{"general\nproblem"})
	lines := strings.Split(h, "\n")
	// first + 1 general + 2 findings + last = 5 header lines, so line 3 of the manifest is line 8 in the editor
	if !strings.HasPrefix(lines[0], editHeaderStart) || lines[4] != editHeaderEnd || lines[5] != "kind: ConfigMap" {
		t.Fatalf("header:\n%s", h)
	}
	if lines[1] != "# general problem" || lines[2] != "# line 8:6: error: bad value (data.a)" || lines[3] != "# error: no position" {
		t.Errorf("header:\n%s", h)
	}
	if got := stripEditHeader(h); got != body {
		t.Errorf("strip: %q", got)
	}
	// the user's own comment lines at the top stay
	if got := stripEditHeader("# mine\n" + body); got != "# mine\n"+body {
		t.Errorf("%q", got)
	}
}

// The edit tests have a terminal on stdin unless they say otherwise.
func init() { stdinIsTerminal = func() bool { return true } }

// editorSetup writes a fake editor: a shell script that records what it was given and applies
// sed expression number N (file sedN in the state directory) to the file. The directory has a space
// in its name and the editor is given with quotes and an extra flag, the way 'code --wait' is.
type editorSetup struct {
	dir string
}

func newEditor(t *testing.T) *editorSetup {
	t.Helper()
	base := filepath.Join(t.TempDir(), "my editor")
	if err := os.MkdirAll(filepath.Join(base, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
dir=$1; shift
for last; do :; done
file=$last
n=$(cat "$dir/n" 2>/dev/null || echo 0); n=$((n+1)); echo $n > "$dir/n"
echo "$*" > "$dir/args$n"
cp "$file" "$dir/seen$n"
stat -c %a "$file" > "$dir/fileperm"
stat -c %a "$(dirname "$file")" > "$dir/dirperm"
dirname "$file" > "$dir/tmpdir"
if [ -f "$dir/term$n" ]; then kill -TERM "$PPID"; exec sleep 30; fi
if [ -f "$dir/exit$n" ]; then exit "$(cat "$dir/exit$n")"; fi
if [ -f "$dir/empty$n" ]; then : > "$file"; fi
if [ -f "$dir/sed$n" ]; then sed -i -f "$dir/sed$n" "$file"; fi
`
	if err := os.WriteFile(filepath.Join(base, "editor.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", "'"+filepath.Join(base, "editor.sh")+"' '"+filepath.Join(base, "state")+"' --wait")
	t.Setenv("EDITOR", "")
	return &editorSetup{dir: filepath.Join(base, "state")}
}

func (s *editorSetup) step(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (s *editorSetup) read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(s.dir, name))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return string(b)
}

func (s *editorSetup) runs() int {
	b, _ := os.ReadFile(filepath.Join(s.dir, "n"))
	n := 0
	for _, c := range strings.TrimSpace(string(b)) {
		n = n*10 + int(c-'0')
	}
	return n
}

const storedYAML = "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: whoami\n  namespace: default\nspec:\n  replicas: 1\n"

func manifestBody(id, manifest string) string {
	return mustJSON(api.LocalItemSource{
		Target: api.ConfigTargetRef{Kind: "robot", Name: "vision-station-01", Device: "vision-station-01"},
		ID:     id, Manifest: manifest,
	})
}

// editPlatform serves the item and checks manifests: one that sets replicas to "x" is invalid.
func editPlatform(t *testing.T) (*fakePlatform, *[]api.LocalWriteRequest) {
	p := newFakePlatform(t)
	p.on("GET", addPath, 200, manifestBody(whoamiID, storedYAML))
	var mu sync.Mutex
	var puts []api.LocalWriteRequest
	p.onFunc("PUT", addPath, func(r rec) (int, string) {
		var req api.LocalWriteRequest
		_ = json.Unmarshal([]byte(r.Body), &req)
		dry := strings.Contains(r.Query, "dryRun=1")
		if !dry {
			mu.Lock()
			puts = append(puts, req)
			mu.Unlock()
		}
		_, _, name := docIdentity(req.Manifest)
		id := "Deployment/default/" + name
		if strings.Contains(req.Manifest, `replicas: "x"`) {
			status := 200
			if !dry {
				status = 422
			}
			return status, writeResult(id, "replace", dry, nil, []issue{{Severity: "error", Message: "Invalid type. Expected: integer, given: string", Path: "spec.replicas", Line: 7, Column: 13}})
		}
		action := "replace"
		if strings.TrimSpace(req.Manifest) == strings.TrimSpace(storedYAML) {
			action = "unchanged"
		}
		return 200, writeResult(id, action, dry, nil, nil)
	})
	return p, &puts
}

func TestConfigEditLoopReopensOnErrors(t *testing.T) {
	p, puts := editPlatform(t)
	ed := newEditor(t)
	ed.step(t, "sed1", `s/replicas: 1/replicas: "x"/`)
	ed.step(t, "sed2", `s/replicas: "x"/replicas: 3/`)
	e, _ := testEnv(t, p, nil)
	out, errs, err := runCLI(t, e, "config", "edit", "vision-station-01", whoamiID)
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if out != "replaced Deployment/default/whoami on vision-station-01\n" || ed.runs() != 2 {
		t.Errorf("stdout %q, %d editor runs", out, ed.runs())
	}
	if got := ed.read(t, "seen1"); got != storedYAML {
		t.Errorf("the editor starts with the stored manifest:\n%s", got)
	}
	// round 2 has the errors on top, with the editor's line numbers (3 header lines + line 7)
	seen2 := ed.read(t, "seen2")
	if !strings.HasPrefix(seen2, editHeaderStart) || !strings.Contains(seen2, "# line 10:13: error: Invalid type. Expected: integer, given: string (spec.replicas)\n") || !strings.Contains(seen2, editHeaderEnd+"\napiVersion: apps/v1\n") {
		t.Errorf("round 2:\n%s", seen2)
	}
	if strings.Split(seen2, "\n")[9] != `  replicas: "x"` {
		t.Errorf("line 10 of the file in the editor is not the one the error names:\n%s", seen2)
	}
	// the header never reaches the platform, in the checks or in the write
	if len(*puts) != 1 || (*puts)[0].Manifest != strings.Replace(storedYAML, "replicas: 1", "replicas: 3", 1) || !(*puts)[0].Replace {
		t.Errorf("writes: %+v", *puts)
	}
	for _, r := range p.seen("PUT", addPath) {
		if strings.Contains(r.Body, "tnctl") {
			t.Errorf("header sent to the platform: %s", r.Body)
		}
	}
	// editors given with quotes and flags work; the file is the last argument
	if got := strings.TrimSpace(ed.read(t, "args1")); !strings.HasPrefix(got, "--wait ") || !strings.HasSuffix(got, "Deployment-whoami.yaml") {
		t.Errorf("editor args %q", got)
	}
	// the manifest is read with the id the user typed, the check is a dry run and the write is not
	var dry, real int
	for _, r := range p.seen("PUT", addPath) {
		if strings.Contains(r.Query, "dryRun=1") {
			dry++
		} else {
			real++
		}
		if !strings.Contains(r.Body, `"replace":true`) {
			t.Errorf("edit must send replace: %s", r.Body)
		}
	}
	if dry != 2 || real != 1 {
		t.Errorf("dry runs %d, writes %d", dry, real)
	}
	// the item is read when the edit starts and once more just before the write
	if g := p.seen("GET", addPath); len(g) != 2 || g[0].Query != "id=Deployment%2Fdefault%2Fwhoami" {
		t.Errorf("%+v", g)
	}
}

func TestConfigEditTemporaryFileIsPrivateAndRemoved(t *testing.T) {
	p, _ := editPlatform(t)
	ed := newEditor(t)
	e, _ := testEnv(t, p, nil)
	if _, errs, err := runCLI(t, e, "config", "edit", "vision-station-01", whoamiID); err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if got := strings.TrimSpace(ed.read(t, "fileperm")); got != "600" {
		t.Errorf("file mode %s", got)
	}
	if got := strings.TrimSpace(ed.read(t, "dirperm")); got != "700" {
		t.Errorf("directory mode %s", got)
	}
	if _, err := os.Stat(strings.TrimSpace(ed.read(t, "tmpdir"))); !os.IsNotExist(err) {
		t.Errorf("the temporary directory is still there: %v", err)
	}
}

func TestConfigEditNoChangesExitsZero(t *testing.T) {
	p, puts := editPlatform(t)
	ed := newEditor(t)
	e, _ := testEnv(t, p, nil)
	out, _, err := runCLI(t, e, "config", "edit", "vision-station-01", whoamiID)
	if err != nil || out != "No changes: Deployment/default/whoami on vision-station-01 is unchanged.\n" {
		t.Errorf("%v %q", err, out)
	}
	if ed.runs() != 1 || len(*puts) != 0 || len(p.seen("PUT", addPath)) != 0 {
		t.Errorf("runs %d, writes %d, requests %d", ed.runs(), len(*puts), len(p.seen("PUT", addPath)))
	}
	// a change that is only a comment is "unchanged" for the platform: no write, exit 0
	ed.step(t, "sed1", `1i # a comment`)
	out, _, err = runCLI(t, e, "config", "edit", "vision-station-01", whoamiID)
	if err != nil || !strings.HasPrefix(out, "No changes:") || len(*puts) != 0 {
		t.Errorf("%v %q writes %d", err, out, len(*puts))
	}
}

func TestConfigEditLeavingAnInvalidManifestAbandonsIt(t *testing.T) {
	p, puts := editPlatform(t)
	ed := newEditor(t)
	ed.step(t, "sed1", `s/replicas: 1/replicas: "x"/`) // round 2 changes nothing
	e, _ := testEnv(t, p, nil)
	_, errs, err := runCLI(t, e, "config", "edit", "vision-station-01", whoamiID)
	if exitCodeOf(err) != codeRejected || !strings.Contains(err.Error(), "still invalid") || len(*puts) != 0 || ed.runs() != 2 {
		t.Errorf("exit %d: %v, writes %d, runs %d", exitCodeOf(err), err, len(*puts), ed.runs())
	}
	if !strings.Contains(errs, "<edit>:7:13: error: Invalid type") {
		t.Errorf("stderr:\n%s", errs)
	}
}

func TestConfigEditEmptyFileCancels(t *testing.T) {
	p, puts := editPlatform(t)
	ed := newEditor(t)
	ed.step(t, "empty1", "")
	e, _ := testEnv(t, p, nil)
	out, _, err := runCLI(t, e, "config", "edit", "vision-station-01", whoamiID)
	if err != nil || !strings.HasPrefix(out, "Edit cancelled") || len(*puts) != 0 || len(p.seen("PUT", addPath)) != 0 {
		t.Errorf("%v %q", err, out)
	}
}

func TestConfigEditRefusesAnotherIdentity(t *testing.T) {
	p, puts := editPlatform(t)
	ed := newEditor(t)
	ed.step(t, "sed1", `s/name: whoami/name: other/`)
	ed.step(t, "sed2", `s/name: other/name: whoami/; s/replicas: 1/replicas: 2/`)
	e, _ := testEnv(t, p, nil)
	if _, errs, err := runCLI(t, e, "config", "edit", "vision-station-01", whoamiID); err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if !strings.Contains(ed.read(t, "seen2"), "# this is Deployment/default/other, not Deployment/default/whoami: kind, namespace and name cannot change here") || len(*puts) != 1 {
		t.Errorf("round 2:\n%s\nwrites %d", ed.read(t, "seen2"), len(*puts))
	}
}

func TestConfigEditSeveralDocumentsAreRefused(t *testing.T) {
	p, _ := editPlatform(t)
	ed := newEditor(t)
	ed.step(t, "sed1", `$a ---\nkind: ConfigMap`)
	ed.step(t, "sed2", `/^# tnctl/,/^# ---- end/d;/^---$/,$d`)
	e, _ := testEnv(t, p, nil)
	_, _, err := runCLI(t, e, "config", "edit", "vision-station-01", whoamiID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ed.read(t, "seen2"), "edit works on one item and the file has 2 documents") {
		t.Errorf("round 2:\n%s", ed.read(t, "seen2"))
	}
}

func TestConfigEditNotLocalIsExit8WithHint(t *testing.T) {
	p := newFakePlatform(t)
	msg := "whoami comes from the fleet gpu-nvidia (git) and cannot be edited as a local item"
	p.on("GET", addPath, 409, `{"error":{"name":"ConflictError","message":"`+msg+`"}}`)
	ed := newEditor(t)
	e, _ := testEnv(t, p, nil)
	_, _, err := runCLI(t, e, "config", "edit", "vision-station-01", whoamiID)
	if exitCodeOf(err) != codeConflict || !strings.Contains(err.Error(), msg) || !strings.Contains(err.Error(), "tnctl config get vision-station-01 Deployment/default/whoami --rendered > item.yaml") ||
		!strings.Contains(err.Error(), "tnctl config add vision-station-01 -f item.yaml") || ed.runs() != 0 {
		t.Errorf("exit %d: %v (editor runs %d)", exitCodeOf(err), err, ed.runs())
	}
}

func TestConfigEditEditorFailureWritesNothing(t *testing.T) {
	p, puts := editPlatform(t)
	ed := newEditor(t)
	ed.step(t, "exit1", "3")
	e, _ := testEnv(t, p, nil)
	_, _, err := runCLI(t, e, "config", "edit", "vision-station-01", whoamiID)
	if exitCodeOf(err) != codeError || !strings.Contains(err.Error(), "exited with status 3") || len(*puts) != 0 {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
	t.Setenv("VISUAL", "/nonexistent/editor")
	_, _, err = runCLI(t, e, "config", "edit", "vision-station-01", whoamiID)
	if exitCodeOf(err) != codeError || !strings.Contains(err.Error(), "cannot run the editor") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
	t.Setenv("VISUAL", "ed 'x")
	_, _, err = runCLI(t, e, "config", "edit", "vision-station-01", whoamiID)
	if exitCodeOf(err) != codeUsage {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
}

func TestConfigEditFallsBackToEditorThenVi(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "nano -w")
	if got, _ := editorCommand(); !reflect.DeepEqual(got, []string{"nano", "-w"}) {
		t.Errorf("%q", got)
	}
	t.Setenv("EDITOR", "")
	if got, _ := editorCommand(); !reflect.DeepEqual(got, []string{"vi"}) {
		t.Errorf("%q", got)
	}
}

func TestConfigEditWaitFlagsAreUsed(t *testing.T) {
	p, puts := editPlatform(t)
	item := storedDeployment("whoami", "traefik/whoami:v1.11.0", stampV2, 3)
	p.onFunc("PUT", addPath, func(r rec) (int, string) {
		return 200, writeResult(whoamiID, "replace", strings.Contains(r.Query, "dryRun"), item, nil)
	})
	sequence(t, p, addedView("h1", true), addedView("h2", true))
	p.on("GET", "/config/vision-station-01/resource", 200, renderedDetail(stampV2, 3))
	ed := newEditor(t)
	ed.step(t, "sed1", `s/replicas: 1/replicas: 3/`)
	e, _ := testEnv(t, p, nil)
	_, errs, err := runCLI(t, e, "config", "edit", "vision-station-01", whoamiID, "--wait=published")
	if err != nil || !strings.Contains(errs, "published after") {
		t.Fatalf("%v\n%s", err, errs)
	}
	_ = puts
	if _, _, err := runCLI(t, e, "config", "edit", "vision-station-01", whoamiID, "--wait=soon"); exitCodeOf(err) != codeUsage {
		t.Errorf("%v", err)
	}
}

// ---------- get --manifest ----------

func TestConfigGetManifest(t *testing.T) {
	p := newFakePlatform(t)
	p.on("GET", addPath, 200, manifestBody(whoamiID, storedYAML))
	p.on("GET", "/config/vision-station-01", 200, jsonOf(t, addedView("h1", true)))
	e, _ := testEnv(t, p, nil)
	out, _, err := runCLI(t, e, "config", "get", "vision-station-01", whoamiID, "--manifest")
	if err != nil || out != storedYAML {
		t.Errorf("%v\n%q", err, out)
	}
	// a short name is resolved first
	out, _, err = runCLI(t, e, "config", "get", "vision-station-01", "whoami", "--manifest")
	if err != nil || out != storedYAML {
		t.Errorf("%v\n%q", err, out)
	}
	if g := p.seen("GET", addPath); len(g) != 2 || g[1].Query != "id=Deployment%2Fdefault%2Fwhoami" {
		t.Errorf("%+v", g)
	}
	out, _, err = runCLI(t, e, "config", "get", "vision-station-01", whoamiID, "--manifest", "-o", "json")
	var v api.LocalItemSource
	if err != nil || json.Unmarshal([]byte(out), &v) != nil || v.Manifest != storedYAML {
		t.Errorf("%v\n%s", err, out)
	}
	for name, args := range map[string][]string{
		"no resource": {"config", "get", "vision-station-01", "--manifest"},
		"with layers": {"config", "get", "vision-station-01", whoamiID, "--manifest", "--layers"},
		"rendered":    {"config", "get", "vision-station-01", whoamiID, "--manifest", "--rendered"},
	} {
		if _, _, err := runCLI(t, e, args...); exitCodeOf(err) != codeUsage {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestConfigGetManifestOfANonLocalItemIsExit8(t *testing.T) {
	p := newFakePlatform(t)
	msg := "Deployment/default/whoami comes from the robot type (git)"
	p.on("GET", addPath, 409, `{"error":{"message":"`+msg+`"}}`)
	e, _ := testEnv(t, p, nil)
	_, _, err := runCLI(t, e, "config", "get", "vision-station-01", whoamiID, "--manifest")
	if exitCodeOf(err) != codeConflict || !strings.Contains(err.Error(), msg) || !strings.Contains(err.Error(), "--rendered > item.yaml") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
	p.on("GET", addPath, 404, `{"error":{"message":"no local item"}}`)
	if _, _, err := runCLI(t, e, "config", "get", "vision-station-01", whoamiID, "--manifest"); exitCodeOf(err) != codeNotFound {
		t.Errorf("%v", err)
	}
}

// ---------- kinds ----------

const kindsBody = `{"kinds":[
 {"apiVersion":"v1","kind":"ConfigMap","namespaced":true,"template":"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: my-config\ndata:\n  key: value\n"},
 {"apiVersion":"apps/v1","kind":"Deployment","namespaced":true,"template":"kind: Deployment\n"},
 {"apiVersion":"v1","kind":"Pod","namespaced":true,"note":"Prefer a Deployment","template":"kind: Pod\n"},
 {"apiVersion":"v1","kind":"Namespace","namespaced":false,"note":"Removing it deletes everything in it","template":"kind: Namespace\n"}],
 "refused":[{"apiVersion":"rbac.authorization.k8s.io/v1","kind":"ClusterRole","reason":"the robot's agent drops rules, and it runs as cluster-admin"},
  {"apiVersion":"*","kind":"CustomResourceDefinition","reason":"no schema to check it with"}],
 "schemaSource":"yannh/kubernetes-json-schema master-standalone-strict @93153a1"}`

func TestConfigKinds(t *testing.T) {
	p := newFakePlatform(t)
	p.on("GET", "/local-kinds", 200, kindsBody)
	e, _ := testEnv(t, p, nil)
	out, _, err := runCLI(t, e, "config", "kinds")
	if err != nil {
		t.Fatal(err)
	}
	for _, frag := range []string{
		"APIVERSION", "apps/v1", "Deployment", "namespaced", "Namespace", "cluster", "Prefer a Deployment", "Removing it deletes everything in it",
		"Refused:", "ClusterRole", "rbac.authorization.k8s.io/v1", "the robot's agent drops rules, and it runs as cluster-admin", "CustomResourceDefinition",
		"Checked against yannh/kubernetes-json-schema master-standalone-strict @93153a1.",
	} {
		if !strings.Contains(out, frag) {
			t.Errorf("table lacks %q:\n%s", frag, out)
		}
	}
	out, _, err = runCLI(t, e, "config", "kinds", "-o", "json")
	var cat api.LocalKindCatalog
	if err != nil || json.Unmarshal([]byte(out), &cat) != nil || len(cat.Kinds) != 4 || len(cat.Refused) != 2 {
		t.Errorf("%v\n%s", err, out)
	}
}

func TestConfigKindsTemplate(t *testing.T) {
	p := newFakePlatform(t)
	p.on("GET", "/local-kinds", 200, kindsBody)
	e, _ := testEnv(t, p, nil)
	out, _, err := runCLI(t, e, "config", "kinds", "--template", "configmap")
	if err != nil || out != "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: my-config\ndata:\n  key: value\n" {
		t.Errorf("%v\n%q", err, out)
	}
	_, _, err = runCLI(t, e, "config", "kinds", "--template", "ClusterRole")
	if exitCodeOf(err) != codeUsage || !strings.Contains(err.Error(), "ClusterRole cannot be added: the robot's agent drops rules") {
		t.Errorf("%v", err)
	}
	_, _, err = runCLI(t, e, "config", "kinds", "--template", "Widget")
	if exitCodeOf(err) != codeUsage || !strings.Contains(err.Error(), "ConfigMap, Deployment, Pod, Namespace") {
		t.Errorf("%v", err)
	}
	if _, _, err := runCLI(t, e, "config", "kinds", "extra"); exitCodeOf(err) != codeUsage {
		t.Errorf("%v", err)
	}
}

func TestConfigHelpMentionsTheLocalCommands(t *testing.T) {
	e, _ := testEnv(t, newFakePlatform(t), nil)
	out, _, err := runCLI(t, e, "config", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, frag := range []string{"add ", "edit ", "kinds "} {
		if !strings.Contains(out, frag) {
			t.Errorf("config --help lacks %q", frag)
		}
	}
	for _, c := range []string{"add", "edit", "kinds"} {
		h, _, err := runCLI(t, e, "config", c, "--help")
		if err != nil || !strings.Contains(h, "Example") && !strings.Contains(h, "Examples") {
			t.Errorf("%s help: %v", c, err)
		}
	}
}
