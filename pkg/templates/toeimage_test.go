package templates

import (
	"regexp"
	"strings"
	"testing"
	"text/template"
)

// toeDigestRE is the pinned form of the TOE image: a tag for humans, then the digest that really
// decides what runs.
var toeDigestRE = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)

// toeDeployment returns the rendered Deployment named toe of a script, or "" when the script has
// none (the agent script only joins the cluster).
func toeDeployment(out string) string {
	const marker = "\n  name: toe\n"
	i := strings.Index(out, marker)
	if i < 0 {
		return ""
	}
	rest := out[i+len(marker):]
	if j := strings.Index(rest, "\n---"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// toeContainer returns the image and imagePullPolicy of the container that runs /manager in the
// toe Deployment of a rendered script.
func toeContainer(t *testing.T, out string) (image, policy string) {
	t.Helper()
	block := toeDeployment(out)
	if block == "" {
		t.Fatal("no Deployment named toe in the rendered script")
	}
	lines := strings.Split(block, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != "- /manager" {
			continue
		}
		for _, c := range lines[i:] {
			s := strings.TrimSpace(c)
			if image == "" && strings.HasPrefix(s, "image:") {
				image = strings.TrimSpace(strings.TrimPrefix(s, "image:"))
			}
			if policy == "" && strings.HasPrefix(s, "imagePullPolicy:") {
				policy = strings.TrimSpace(strings.TrimPrefix(s, "imagePullPolicy:"))
			}
		}
		return image, policy
	}
	t.Fatal("no command line - /manager in the toe Deployment")
	return "", ""
}

// Every variant that renders the TOE Deployment pins the image by digest: a moving tag with
// imagePullPolicy: Always can turn a changed or unpullable image into a watchdog reboot loop.
// The digest is the one ansible playbooks/toe-image.yml writes on the robots; bump both together.
func TestTOEImagePinnedByDigest(t *testing.T) {
	variants := map[string]*template.Template{
		"server":        ServerTemplate,
		"docker server": DockerServerTemplate,
	}
	for name, tpl := range variants {
		t.Run(name, func(t *testing.T) {
			image, policy := toeContainer(t, renderK3S(t, tpl, ""))
			if !toeDigestRE.MatchString(image) {
				t.Errorf("toe image %q is not pinned by @sha256: and a 64-character digest", image)
			}
			if !strings.Contains(image, "tekn0ir/toe:") {
				t.Errorf("toe image %q is not the TOE image", image)
			}
			if policy != "IfNotPresent" {
				t.Errorf("toe imagePullPolicy is %q, want IfNotPresent", policy)
			}
		})
	}
	t.Run("server roles", func(t *testing.T) {
		var b strings.Builder
		if err := ServerTemplate.Execute(&b, serverRolesData()); err != nil {
			t.Fatal(err)
		}
		out := b.String()
		image, policy := toeContainer(t, out)
		if !toeDigestRE.MatchString(image) {
			t.Errorf("toe image %q is not pinned by digest", image)
		}
		if policy != "IfNotPresent" {
			t.Errorf("toe imagePullPolicy is %q, want IfNotPresent", policy)
		}
		if strings.Contains(toeDeployment(out), "toe:toe_v4\n") {
			t.Error("the toe Deployment still uses the moving tag toe_v4")
		}
	})
}

// toeAnsibleRE is toe_re of the ansible repo (playbooks/vars/toe-manifest.yml): toe-image.yml and the
// rebootstrap TOE keep find the toe container by exactly this sequence and refuse a manifest where it
// is not found once. A comment or another field between the lines breaks every later TOE rollout.
var toeAnsibleRE = regexp.MustCompile(`(?m)([ ]+command:\n[ ]+- /manager\n[ ]+image: )(\S+)(\n[ ]+imagePullPolicy: )(\S+)`)

func TestTOEContainerMatchesAnsiblePlaybook(t *testing.T) {
	var roles strings.Builder
	if err := ServerTemplate.Execute(&roles, serverRolesData()); err != nil {
		t.Fatal(err)
	}
	outputs := map[string]string{
		"server":        renderK3S(t, ServerTemplate, ""),
		"docker server": renderK3S(t, DockerServerTemplate, ""),
		"server roles":  roles.String(),
	}
	for name, out := range outputs {
		t.Run(name, func(t *testing.T) {
			m := toeAnsibleRE.FindAllStringSubmatch(out, -1)
			if len(m) != 1 {
				t.Fatalf("the playbook's toe_re matches %d times, want exactly 1", len(m))
			}
			if !toeDigestRE.MatchString(m[0][2]) || m[0][4] != "IfNotPresent" {
				t.Errorf("toe_re found image %q policy %q", m[0][2], m[0][4])
			}
		})
	}
}
