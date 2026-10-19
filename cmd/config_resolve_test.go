package cmd

import (
	"strings"
	"testing"

	"github.com/teknoir/cli/pkg/api"
)

var testIDs = []string{
	"Deployment/default/notebook",
	"Deployment/default/whoami",
	"HelmChart/default/notebook",
	"HelmChart/kube-system/traefik",
	"HelmChart/other/whoami",
}

func TestResolveResourceArg(t *testing.T) {
	ok := []struct{ in, want string }{
		{"HelmChart/default/notebook", "HelmChart/default/notebook"},
		{"helmchart/default/notebook", "HelmChart/default/notebook"}, // kind is case-insensitive
		{"HelmChart/notebook", "HelmChart/default/notebook"},
		{"Deployment/notebook", "Deployment/default/notebook"},
		{"traefik", "HelmChart/kube-system/traefik"},
		{"HelmChart/traefik", "HelmChart/kube-system/traefik"},
		// A full id that is not known passes through: the platform decides.
		{"HelmChart/default/brand-new", "HelmChart/default/brand-new"},
	}
	for _, c := range ok {
		got, err := resolveResourceArg(c.in, testIDs)
		if err != nil || got != c.want {
			t.Errorf("%q = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

func TestResolveResourceArgAmbiguous(t *testing.T) {
	for _, in := range []string{"notebook", "whoami"} {
		_, err := resolveResourceArg(in, testIDs)
		if exitCodeOf(err) != codeUsage {
			t.Errorf("%q: exit code %d, want usage; %v", in, exitCodeOf(err), err)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, "ambiguous, did you mean") || !strings.Contains(msg, " or ") {
			t.Errorf("%q: %s", in, msg)
		}
	}
	_, err := resolveResourceArg("notebook", testIDs)
	for _, id := range []string{"Deployment/default/notebook", "HelmChart/default/notebook"} {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("candidates must be listed: %s", err)
		}
	}
}

func TestResolveResourceArgNotFound(t *testing.T) {
	_, err := resolveResourceArg("nope", testIDs)
	if exitCodeOf(err) != codeNotFound || !strings.Contains(err.Error(), `no resource "nope"`) {
		t.Errorf("got %d %v", exitCodeOf(err), err)
	}
	_, err = resolveResourceArg("HelmChart/notebok", testIDs)
	if exitCodeOf(err) != codeNotFound || !strings.Contains(err.Error(), "did you mean") {
		t.Errorf("a close name should be suggested: %v", err)
	}
	_, err = resolveResourceArg("nope", nil)
	if exitCodeOf(err) != codeNotFound || !strings.Contains(err.Error(), "no resources") {
		t.Errorf("got %v", err)
	}
}

func TestResolveResourceArgMalformed(t *testing.T) {
	for _, in := range []string{"", "  ", "a//b", "/x", "x/", "a/b/c/d"} {
		if _, err := resolveResourceArg(in, testIDs); exitCodeOf(err) != codeUsage {
			t.Errorf("%q should be a usage error, got %v", in, err)
		}
	}
}

func TestKnownIDsIncludesStaleOverrides(t *testing.T) {
	v := standardView()
	v.Overrides.Resources = map[string]api.ResourceOverride{"HelmChart/default/gone": {Version: "1"}}
	ids := knownIDs(v)
	want := []string{"Deployment/default/whoami", "HelmChart/default/gone", "HelmChart/default/notebook"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", ids, want)
	}
}

func TestCheckID(t *testing.T) {
	if k, ns, n, err := checkID("HelmChart/default/notebook"); err != nil || k != "HelmChart" || ns != "default" || n != "notebook" {
		t.Errorf("got %q %q %q %v", k, ns, n, err)
	}
	for _, bad := range []string{"a/b", "HelmChart/default/x;rm -rf", "HelmChart/def ault/x", "H/d/$(id)", "H/d/-x", "H/d/"} {
		if _, _, _, err := checkID(bad); exitCodeOf(err) != codeUsage {
			t.Errorf("%q should be refused: %v", bad, err)
		}
	}
}

func TestAppHelpers(t *testing.T) {
	if normRepo("HTTPS://GitHub.com/Teknoir/Demo.git/") != "https://github.com/teknoir/demo" {
		t.Error("normRepo")
	}
	for in, want := range map[string]string{"apps/mock/": "apps/mock", "/apps/mock": "apps/mock", ".": "", "": "", " a ": "a"} {
		if got := normAppPath(in); got != want {
			t.Errorf("normAppPath(%q) = %q, want %q", in, got, want)
		}
	}
	v := standardView()
	repo, err := resolveAppRepo(v, "", "apps/mock-vision/")
	if err != nil || repo != "https://github.com/teknoir/teknoir-demo-gitops" {
		t.Errorf("got %q %v", repo, err)
	}
	if _, err := resolveAppRepo(v, "", "apps/none"); exitCodeOf(err) != codeNotFound || !strings.Contains(err.Error(), "apps/mock-vision") {
		t.Errorf("unknown path: %v", err)
	}
	if _, err := resolveAppRepo(v, "", "."); exitCodeOf(err) != codeUsage {
		t.Errorf("whole repo needs --repo: %v", err)
	}
	if repo, _ := resolveAppRepo(v, "https://example.com/x", "."); repo != "https://example.com/x" {
		t.Errorf("--repo wins: %q", repo)
	}
	// the same path in two repositories is ambiguous
	a := v.Configuration.Resources[0]
	other := *a.Application
	other.RepoURL = "https://github.com/acme/other"
	a.Application = &other
	v.Configuration.Resources = append(v.Configuration.Resources, a)
	if _, err := resolveAppRepo(v, "", "apps/mock-vision"); exitCodeOf(err) != codeUsage || !strings.Contains(err.Error(), "--repo") {
		t.Errorf("ambiguous path: %v", err)
	}
	ids := resourcesOfApp(standardView(), "https://github.com/teknoir/teknoir-demo-gitops.git", "apps/mock-vision")
	if len(ids) != 1 || ids[0] != "HelmChart/default/notebook" {
		t.Errorf("resourcesOfApp = %v", ids)
	}
	if ids := resourcesOfApp(standardView(), "https://github.com/teknoir/teknoir-demo-gitops", ""); len(ids) != 1 {
		t.Errorf("whole repo = %v", ids)
	}
}
