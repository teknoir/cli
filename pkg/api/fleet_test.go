package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type seen struct {
	method, path, query, body string
}

// fleetServer answers every request with reply and records what it saw.
func fleetServer(t *testing.T, status int, reply string) (*Fleet, *seen) {
	t.Helper()
	s := &seen{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.method, s.path, s.query, s.body = r.Method, r.URL.EscapedPath(), r.URL.RawQuery, string(b)
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(ts.Close)
	f := &Fleet{
		Base:  ts.URL + "/api/device/fleet/teknoir-demo",
		Token: func(context.Context) (string, error) { return "tok", nil },
	}
	return f, s
}

func TestFleetRoutes(t *testing.T) {
	ctx := context.Background()
	f, s := fleetServer(t, 200, `{}`)

	f.Robots(ctx)
	if s.method != "GET" || s.path != "/api/device/fleet/teknoir-demo/robots" {
		t.Errorf("robots: %+v", s)
	}
	f.Robot(ctx, "vision-station-01")
	if s.path != "/api/device/fleet/teknoir-demo/robots/vision-station-01" {
		t.Errorf("robot: %+v", s)
	}
	f.ConfigTarget(ctx, "vision-station-01")
	if s.method != "GET" || s.path != "/api/device/fleet/teknoir-demo/config/vision-station-01" {
		t.Errorf("target: %+v", s)
	}
	f.ConfigResource(ctx, "vision-station-01", "HelmChart/default/notebook", true)
	if s.path != "/api/device/fleet/teknoir-demo/config/vision-station-01/resource" || s.query != "defaults=1&id=HelmChart%2Fdefault%2Fnotebook" {
		t.Errorf("resource: %+v", s)
	}
	f.PatchResourceOverride(ctx, "vs", "HelmChart/default/notebook", OverrideResourcePatch{Ops: []ValueOp{{Op: "unset", Path: "a"}}}, true)
	if s.method != "PATCH" || !strings.HasSuffix(s.path, "/config/vs/overrides/resource") || s.query != "dryRun=1&id=HelmChart%2Fdefault%2Fnotebook" {
		t.Errorf("patch: %+v", s)
	}
	f.PutResourceOverride(ctx, "vs", "HelmChart/default/notebook", ResourceOverride{ValuesContent: "a: 1\n"}, false)
	if s.method != "PUT" || s.query != "id=HelmChart%2Fdefault%2Fnotebook" || s.body != `{"valuesContent":"a: 1\n"}` {
		t.Errorf("put: %+v", s)
	}
	f.DeleteResourceOverride(ctx, "vs", "Deployment/default/whoami")
	if s.method != "DELETE" || !strings.HasSuffix(s.path, "/overrides/resource") || s.query != "id=Deployment%2Fdefault%2Fwhoami" {
		t.Errorf("delete: %+v", s)
	}
	f.PutApplicationOverride(ctx, "vs", ApplicationOverride{RepoURL: "https://github.com/teknoir/teknoir-demo-gitops", Path: "apps/mock-vision", Revision: "feature-x"}, false)
	if s.method != "PUT" || !strings.HasSuffix(s.path, "/overrides/application") || s.body != `{"repo_url":"https://github.com/teknoir/teknoir-demo-gitops","path":"apps/mock-vision","revision":"feature-x"}` {
		t.Errorf("put app: %+v", s)
	}
	f.DeleteApplicationOverride(ctx, "vs", "https://github.com/teknoir/teknoir-demo-gitops", "apps/mock-vision")
	if s.method != "DELETE" || s.query != "path=apps%2Fmock-vision&repo_url=https%3A%2F%2Fgithub.com%2Fteknoir%2Fteknoir-demo-gitops" {
		t.Errorf("delete app: %+v", s)
	}
	f.DeleteAllOverrides(ctx, "vs")
	if s.method != "DELETE" || !strings.HasSuffix(s.path, "/config/vs/overrides") || s.query != "" {
		t.Errorf("delete all: %+v", s)
	}
	f.Promote(ctx, "vs", "HelmChart/default/notebook", "fleet:test")
	if !strings.HasSuffix(s.path, "/config/vs/promote") || s.query != "id=HelmChart%2Fdefault%2Fnotebook&to=fleet%3Atest" {
		t.Errorf("promote: %+v", s)
	}
}

func TestFleetRemoveLocalItem(t *testing.T) {
	ctx := context.Background()
	f, s := fleetServer(t, 200, `{"id":"HelmChart/default/local-llm","dryRun":true,"owner":"devstudio","hadOverride":true,"removed":{"kind":"HelmChart"},"warnings":["w"]}`)
	r, raw, err := f.RemoveLocalItem(ctx, "vs", "HelmChart/default/local-llm", true)
	if err != nil || s.method != "DELETE" || !strings.HasSuffix(s.path, "/config/vs/local") || s.query != "dryRun=1&id=HelmChart%2Fdefault%2Flocal-llm" || s.body != "" {
		t.Errorf("%v %+v", err, s)
	}
	if !r.DryRun || r.Owner != "devstudio" || !r.HadOverride || r.Removed["kind"] != "HelmChart" || len(r.Warnings) != 1 || len(raw) == 0 {
		t.Errorf("%+v", r)
	}
	_, _, _ = f.RemoveLocalItem(ctx, "vs", "HelmChart/default/local-llm", false)
	if s.query != "id=HelmChart%2Fdefault%2Flocal-llm" {
		t.Errorf("query %q", s.query)
	}
}

func TestFleetTargetNameIsEscaped(t *testing.T) {
	f, s := fleetServer(t, 200, `{}`)
	f.ConfigTarget(context.Background(), "a/b c")
	if !strings.HasSuffix(s.path, "/config/a%2Fb%20c") {
		t.Errorf("path %s", s.path)
	}
}

func TestFleetErrors(t *testing.T) {
	f, _ := fleetServer(t, 404, `{"error":{"name":"NotFoundError","message":"No robot or device called \"x\"."}}`)
	_, _, err := f.ConfigTarget(context.Background(), "x")
	ae, ok := err.(*Error)
	if !ok || ae.Status != 404 || !strings.Contains(ae.Message, "No robot or device") {
		t.Fatalf("got %#v", err)
	}
}

func TestFleetDecodesView(t *testing.T) {
	f, _ := fleetServer(t, 200, `{
	  "target":{"kind":"robot","name":"vision-station-01","device":"vision-station-01","robot":"vision-station-01"},
	  "configuration":{"resources":[],"effective":[{"identity":"HelmChart-notebook-default","kind":"HelmChart","name":"notebook","namespace":"default","layer":"fleet","source":"test","status":"winner",
	     "chart":{"chart":"notebook","version":"0.0.5"},"values":{"merged":{"image":{"tag":"1"}},"provenance":[{"path":"image.tag","value":"1","layer":"fleet","source":"test"}]}}],
	     "deviations":[],"warnings":[]},
	  "overrides":{"revision":"r1","resources":{"HelmChart/default/notebook":{"valuesContent":"image:\n  tag: pr-123\n","setBy":"a@b"}}},
	  "overrideStatus":[{"target":"HelmChart/default/notebook","state":"active"}],
	  "overridesRevision":"r1","overrideCount":1}`)
	v, raw, err := f.ConfigTarget(context.Background(), "vision-station-01")
	if err != nil {
		t.Fatal(err)
	}
	if v.OverridesRevision != "r1" || v.Overrides.Resources["HelmChart/default/notebook"].SetBy != "a@b" || v.OverrideStatus[0].State != "active" {
		t.Errorf("view: %+v", v)
	}
	if got := v.Configuration.Effective[0].ID(); got != "HelmChart/default/notebook" {
		t.Errorf("ID = %s", got)
	}
	if !json.Valid(raw) {
		t.Error("raw must stay valid JSON")
	}
}

func TestOverridePatchJSON(t *testing.T) {
	cases := []struct {
		name string
		p    OverrideResourcePatch
		want string
	}{
		{"set keeps false, 0, empty and null", OverrideResourcePatch{Ops: []ValueOp{
			{Op: "set", Path: "a", Value: false}, {Op: "set", Path: "b", Value: int64(0)},
			{Op: "set", Path: "c", Value: ""}, {Op: "set", Path: "d", Value: nil},
			{Op: "delete", Path: "e"}, {Op: "unset", Path: "f.g"}}},
			`{"ops":[{"op":"set","path":"a","value":false},{"op":"set","path":"b","value":0},{"op":"set","path":"c","value":""},{"op":"set","path":"d","value":null},{"op":"delete","path":"e"},{"op":"unset","path":"f.g"}]}`},
		{"version", OverrideResourcePatch{Version: "0.0.6"}, `{"version":"0.0.6"}`},
		{"clear version", OverrideResourcePatch{ClearVersion: true}, `{"version":null}`},
		{"images", OverrideResourcePatch{Images: map[string]*string{"whoami": strp("traefik/whoami:v1.11.0"), "sidecar": nil}}, `{"images":{"sidecar":null,"whoami":"traefik/whoami:v1.11.0"}}`},
		{"image", OverrideResourcePatch{Image: &ImageChange{Value: ":pr-123"}}, `{"image":{"value":":pr-123"}}`},
		{"image path", OverrideResourcePatch{Image: &ImageChange{Path: "defaults.image", Value: "ghcr.io/x/y:2"}}, `{"image":{"path":"defaults.image","value":"ghcr.io/x/y:2"}}`},
		{"note", OverrideResourcePatch{Note: "try pr-123"}, `{"note":"try pr-123"}`},
		{"clear note", OverrideResourcePatch{ClearNote: true}, `{"note":null}`},
	}
	for _, c := range cases {
		b, err := json.Marshal(c.p)
		if err != nil || string(b) != c.want {
			t.Errorf("%s:\n got %s (%v)\nwant %s", c.name, b, err, c.want)
		}
	}
	if !(OverrideResourcePatch{}).Empty() || (OverrideResourcePatch{ClearNote: true}).Empty() {
		t.Error("Empty()")
	}
}

func strp(s string) *string { return &s }

func TestOffRoutes(t *testing.T) {
	ctx := context.Background()
	f, s := fleetServer(t, 200, `{}`)

	f.SetResourceOff(ctx, "vs", "HelmChart/default/notebook", true, "why", true, "")
	if s.method != "PATCH" || !strings.HasSuffix(s.path, "/config/vs/overrides/resource") ||
		s.query != "dryRun=1&id=HelmChart%2Fdefault%2Fnotebook" || s.body != `{"note":"why","off":true}` {
		t.Errorf("off dry run: %+v", s)
	}
	f.SetResourceOff(ctx, "vs", "HelmChart/default/notebook", false, "", false, "delete it")
	if s.query != "confirm=delete+it&id=HelmChart%2Fdefault%2Fnotebook" || s.body != `{"off":false}` {
		t.Errorf("on with confirm: %+v", s)
	}
	f.SetApplicationOff(ctx, "vs", "https://github.com/teknoir/teknoir-demo-gitops", "apps/mock-vision", true, "", false, "yes")
	if s.method != "PATCH" || !strings.HasSuffix(s.path, "/config/vs/overrides/application/off") ||
		s.query != "confirm=yes&path=apps%2Fmock-vision&repo_url=https%3A%2F%2Fgithub.com%2Fteknoir%2Fteknoir-demo-gitops" || s.body != `{"off":true}` {
		t.Errorf("application off: %+v", s)
	}
	f.SetApplicationOff(ctx, "vs", "https://github.com/teknoir/teknoir-demo-gitops", "apps/mock-vision", false, "back", true, "")
	if s.query != "dryRun=1&path=apps%2Fmock-vision&repo_url=https%3A%2F%2Fgithub.com%2Fteknoir%2Fteknoir-demo-gitops" || s.body != `{"off":false,"note":"back"}` {
		t.Errorf("application on dry run: %+v", s)
	}
}

func TestOffJSON(t *testing.T) {
	// an old-style PUT of a whole override never sends off (a stored off stays on the platform)
	if b, err := json.Marshal(ResourceOverride{ValuesContent: "a: 1\n"}); err != nil || strings.Contains(string(b), "off") {
		t.Errorf("ResourceOverride: %s (%v)", b, err)
	}
	if b, _ := json.Marshal(ResourceOverride{Off: true}); !strings.Contains(string(b), `"off":true`) {
		t.Errorf("ResourceOverride with off: %s", b)
	}
	// a patch: on is off:false, and a patch that does not touch off does not send it
	no := false
	if b, _ := json.Marshal(OverrideResourcePatch{Off: &no}); string(b) != `{"off":false}` {
		t.Errorf("patch off:false: %s", b)
	}
	if b, _ := json.Marshal(OverrideResourcePatch{Note: "x"}); strings.Contains(string(b), "off") {
		t.Errorf("patch without off: %s", b)
	}
	// the view carries the state and the rows their status
	var v ConfigTargetView
	if err := json.Unmarshal([]byte(`{"configuration":{"effective":[{"kind":"HelmChart","name":"n","namespace":"default","status":"off"}]},
	  "overrides":{"resources":{"HelmChart/default/n":{"off":true}}},
	  "overrideStatus":[{"target":"HelmChart/default/n","state":"off"}]}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.OverrideStatus[0].State != StateOff || v.Configuration.Effective[0].Status != StatusOff || !v.Overrides.Resources["HelmChart/default/n"].Off {
		t.Errorf("view: %+v", v)
	}
}

func TestApplicationOffResultDecodes(t *testing.T) {
	f, _ := fleetServer(t, 200, `{"dryRun":false,"ids":["HelmChart-a-default"],"skipped":[{"identity":"Deployment-x-default","reason":"not yours"}],
	  "impacts":[{"identity":"HelmChart-a-default","kind":"HelmChart","deletesData":"yes","confirm":"c"}],
	  "confirm":"c","previousByKey":{"HelmChart/default/a":{"valuesContent":"a: 1\n","off":true}}}`)
	r, _, err := f.SetApplicationOff(context.Background(), "vs", "https://x/y", "apps/a", true, "", false, "c")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.IDs) != 1 || r.Skipped[0].Identity != "Deployment-x-default" || len(r.Impacts) != 1 ||
		r.Confirm != "c" || !r.PreviousByKey["HelmChart/default/a"].Off {
		t.Errorf("result: %+v", r)
	}
}
