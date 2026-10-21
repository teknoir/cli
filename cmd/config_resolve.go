package cmd

import (
	"context"
	"regexp"
	"sort"
	"strings"

	"github.com/teknoir/cli/pkg/api"
)

// k8sName guards what goes into ids and shell command lines on a device.
var k8sName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// splitID splits Kind/namespace/name.
func splitID(id string) (kind, ns, name string) {
	p := strings.SplitN(id, "/", 3)
	if len(p) != 3 {
		return "", "", ""
	}
	return p[0], p[1], p[2]
}

// knownIDs are the resource ids a target has: the effective resources and the ones that have
// an override (a stale override of a resource git no longer has can still be reset).
func knownIDs(v *api.ConfigTargetView) []string {
	seen := map[string]bool{}
	var ids []string
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, r := range v.Configuration.Effective {
		add(r.ID())
	}
	for id := range v.Overrides.Resources {
		add(id)
	}
	sort.Strings(ids)
	return ids
}

// resolveResourceArg turns what the user typed into a resource id: Kind/ns/name,
// Kind/name or a unique name. known are the ids of the target.
func resolveResourceArg(arg string, known []string) (string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", usageErr("the resource is empty: use Kind/namespace/name, Kind/name or a name, for example HelmChart/default/notebook")
	}
	parts := strings.Split(arg, "/")
	// The platform names a cluster-scoped item Namespace//name (no namespace); that is what it prints,
	// so accept it back. The platform ignores the namespace of such a kind.
	if len(parts) == 3 && parts[1] == "" && parts[2] != "" && strings.EqualFold(parts[0], "Namespace") {
		parts[1] = "default"
		arg = strings.Join(parts, "/")
	}
	for _, p := range parts {
		if p == "" {
			return "", usageErr("%q is not a resource: use Kind/namespace/name, Kind/name or a name, for example HelmChart/default/notebook", arg)
		}
	}
	switch len(parts) {
	case 3:
		for _, id := range known {
			k, ns, n := splitID(id)
			if strings.EqualFold(k, parts[0]) && ns == parts[1] && n == parts[2] {
				return id, nil
			}
		}
		// Not known: let the platform say whether it exists.
		return arg, nil
	case 2, 1:
		var matches []string
		for _, id := range known {
			k, _, n := splitID(id)
			if (len(parts) == 2 && strings.EqualFold(k, parts[0]) && n == parts[1]) || (len(parts) == 1 && n == parts[0]) {
				matches = append(matches, id)
			}
		}
		switch len(matches) {
		case 1:
			return matches[0], nil
		case 0:
			return "", newErr(codeNotFound, "no resource %q on this target%s", arg, didYouMean(arg, known))
		}
		return "", usageErr("%q is ambiguous, did you mean %s?", arg, orList(matches))
	}
	return "", usageErr("%q is not a resource: use Kind/namespace/name, Kind/name or a name", arg)
}

// didYouMean lists the ids that contain the typed name, or all ids when none does.
func didYouMean(arg string, known []string) string {
	if len(known) == 0 {
		return " (the target has no resources)"
	}
	needle := strings.ToLower(arg[strings.LastIndex(arg, "/")+1:])
	var close []string
	for _, id := range known {
		_, _, name := splitID(id)
		name = strings.ToLower(name)
		if strings.Contains(name, needle) || strings.Contains(needle, name) || editDistance(name, needle) <= 2 {
			close = append(close, id)
		}
	}
	if len(close) > 0 {
		return "; did you mean " + orList(close) + "?"
	}
	if len(known) > 8 {
		known = known[:8]
		return "; resources include " + strings.Join(known, ", ") + ", ..."
	}
	return "; resources are " + strings.Join(known, ", ")
}

func orList(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " or " + items[len(items)-1]
}

// resolveTarget resolves a resource argument against a target. A full Kind/ns/name id needs no
// lookup; anything shorter loads the target. view is nil when no lookup was needed.
func resolveTarget(ctx context.Context, fl *api.Fleet, target, arg string) (id string, view *api.ConfigTargetView, err error) {
	if strings.Count(arg, "/") == 2 {
		id, err = resolveResourceArg(arg, nil)
		return id, nil, err
	}
	view, _, err = fl.ConfigTarget(ctx, target)
	if err != nil {
		return "", nil, err
	}
	id, err = resolveResourceArg(arg, knownIDs(view))
	return id, view, err
}

// checkID makes sure an id is safe to use in shell command lines on the device.
func checkID(id string) (kind, ns, name string, err error) {
	kind, ns, name = splitID(id)
	if !k8sName.MatchString(kind) || !k8sName.MatchString(ns) || !k8sName.MatchString(name) {
		return "", "", "", usageErr("%q is not a valid resource id (Kind/namespace/name)", id)
	}
	return kind, ns, name, nil
}

// appKey is the status target of an application override: app:<repo_url>#<path>.
func appKey(repoURL, path string) string { return "app:" + repoURL + "#" + path }

func (e *cliEnv) targetClient() (*api.Fleet, string, string, error) {
	fl, domain, ns, err := e.fleet()
	if err != nil {
		return nil, "", "", classify(err)
	}
	return fl, domain, ns, nil
}

// editDistance is the Levenshtein distance of two strings.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
