package api

import (
	"context"
	"strings"
	"testing"
)

func TestFleetPutLocalItem(t *testing.T) {
	ctx := context.Background()
	f, s := fleetServer(t, 200, `{"id":"Deployment/default/whoami","dryRun":true,"action":"create","validation":{"verdict":"valid","issues":[]},"item":{"kind":"Deployment"},"replaces":{"layer":"fleet","source":"gpu"},"warnings":["w"]}`)
	r, raw, err := f.PutLocalItem(ctx, "vs", LocalWriteRequest{Manifest: "kind: Deployment\n", Replace: true}, true)
	if err != nil || s.method != "PUT" || !strings.HasSuffix(s.path, "/config/vs/local") || s.query != "dryRun=1" || s.body != `{"manifest":"kind: Deployment\n","replace":true}` {
		t.Fatalf("%v %+v", err, s)
	}
	if r.Action != LocalActionCreate || r.Invalid() || r.Replaces == nil || r.Replaces.Source != "gpu" || r.Item["kind"] != "Deployment" || len(raw) == 0 {
		t.Errorf("%+v", r)
	}
	_, _, _ = f.PutLocalItem(ctx, "vs", LocalWriteRequest{Manifest: "x"}, false)
	if s.query != "" || s.body != `{"manifest":"x"}` {
		t.Errorf("%+v", s)
	}
}

func TestFleetPutLocalItem422IsAResult(t *testing.T) {
	body := `{"id":"Deployment/default/whoami","dryRun":false,"action":"create","validation":{"verdict":"invalid","issues":[{"severity":"error","message":"bad","path":"spec","line":3,"column":5}]},"warnings":[]}`
	f, _ := fleetServer(t, 422, body)
	r, raw, err := f.PutLocalItem(context.Background(), "vs", LocalWriteRequest{Manifest: "x"}, false)
	if err != nil || !r.Invalid() || len(r.Validation.Issues) != 1 || r.Validation.Issues[0].Line != 3 || r.Validation.Issues[0].Column != 5 || string(raw) != body {
		t.Fatalf("%v %+v", err, r)
	}
	// a 422 that is not a validation result stays an error
	f, _ = fleetServer(t, 422, `{"error":{"message":"manifest is required"}}`)
	if _, _, err := f.PutLocalItem(context.Background(), "vs", LocalWriteRequest{}, false); err == nil || !strings.Contains(err.Error(), "manifest is required") {
		t.Errorf("%v", err)
	}
}

func TestFleetPutLocalItem409IsAnError(t *testing.T) {
	f, _ := fleetServer(t, 409, `{"error":{"name":"ConflictError","message":"already exists"}}`)
	_, _, err := f.PutLocalItem(context.Background(), "vs", LocalWriteRequest{Manifest: "x"}, false)
	ae, ok := err.(*Error)
	if !ok || ae.Status != 409 || ae.Message != "already exists" {
		t.Fatalf("%#v", err)
	}
}

func TestFleetLocalItemAndKinds(t *testing.T) {
	ctx := context.Background()
	f, s := fleetServer(t, 200, `{"id":"ConfigMap/default/a","manifest":"kind: ConfigMap\n","owner":"devstudio","hasOverride":true}`)
	it, _, err := f.LocalItem(ctx, "vs", "ConfigMap/default/a")
	if err != nil || s.method != "GET" || !strings.HasSuffix(s.path, "/config/vs/local") || s.query != "id=ConfigMap%2Fdefault%2Fa" {
		t.Fatalf("%v %+v", err, s)
	}
	if it.Manifest != "kind: ConfigMap\n" || it.Owner != "devstudio" || !it.HasOverride {
		t.Errorf("%+v", it)
	}
	f, s = fleetServer(t, 200, `{"kinds":[{"apiVersion":"v1","kind":"Namespace","namespaced":false,"note":"n","template":"t"}],"refused":[{"apiVersion":"*","kind":"Node","reason":"r"}],"schemaSource":"src"}`)
	c, _, err := f.LocalKinds(ctx)
	if err != nil || s.method != "GET" || !strings.HasSuffix(s.path, "/fleet/teknoir-demo/local-kinds") {
		t.Fatalf("%v %+v", err, s)
	}
	if len(c.Kinds) != 1 || c.Kinds[0].Namespaced || c.Kinds[0].Template != "t" || c.Refused[0].Reason != "r" || c.SchemaSource != "src" {
		t.Errorf("%+v", c)
	}
}
