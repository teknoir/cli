package cmd

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/teknoir/cli/pkg/api"
	"github.com/teknoir/cli/pkg/templates"
)

// registrySettings is what `tnctl bootstrap` puts into TemplateData for the registry mirror.
type registrySettings struct {
	Mirror, Username, Password string
}

// registryFromFlags decides the registry mirror of the generated scripts (plans/harbor-proxy-staging.md):
//
//   - --registry-mirror none removes a registries.yaml that an earlier script wrote; no credential;
//   - --registry-mirror HOST uses HOST with the credential of --registry-credential-file
//     (one line username:password), or the platform's credential when the device's credentials
//     response names the same host;
//   - no --registry-mirror: the platform's registry of the device credentials response when it sends
//     one (not yet), otherwise nothing: registries.yaml is left alone.
//
// Errors never contain the credential.
func registryFromFlags(mirrorFlag, credFile string, platform *api.FleetRegistry) (registrySettings, error) {
	mirror := strings.TrimSpace(mirrorFlag)
	if err := templates.ValidateRegistryMirror(mirror); err != nil {
		return registrySettings{}, err
	}
	fromPlatform := false
	if mirror == "" && platform != nil && platform.Host != "" {
		mirror = platform.Host
		fromPlatform = true
		if err := templates.ValidateRegistryMirror(mirror); err != nil {
			return registrySettings{}, fmt.Errorf("the registry mirror that the platform sent is unusable: %w", err)
		}
	}
	switch mirror {
	case "":
		if credFile != "" {
			return registrySettings{}, fmt.Errorf("--registry-credential-file needs --registry-mirror HOST")
		}
		return registrySettings{}, nil
	case templates.RegistryMirrorNone:
		return registrySettings{Mirror: mirror}, nil
	}
	s := registrySettings{Mirror: mirror}
	switch {
	case credFile != "":
		user, pass, err := readRegistryCredentialFile(credFile)
		if err != nil {
			return registrySettings{}, err
		}
		s.Username, s.Password = user, pass
	case platform != nil && platform.Host == mirror && platform.Username != "" && platform.Password != "":
		s.Username, s.Password = platform.Username, platform.Password
	default:
		what := "--registry-mirror " + mirror
		if fromPlatform {
			what = "the registry mirror " + mirror + " that the platform sent"
		}
		return registrySettings{}, fmt.Errorf("%s needs a credential: pass --registry-credential-file FILE (one line username:password; the Harbor proxy projects are private)", what)
	}
	return s, nil
}

// checkRegistryFlags checks the two flags on their own, before the platform is asked for anything: the
// mirror syntax and, when given, the credential file. Whether a mirror and a credential are both there
// is decided by registryFromFlags once the platform's answer is known.
func checkRegistryFlags(mirrorFlag, credFile string) error {
	if err := templates.ValidateRegistryMirror(strings.TrimSpace(mirrorFlag)); err != nil {
		return err
	}
	if credFile != "" {
		if _, _, err := readRegistryCredentialFile(credFile); err != nil {
			return err
		}
	}
	return nil
}

// readRegistryCredentialFile reads `username:password` from the first line of path. Where file modes
// mean something, a file that group or others can read is refused: it holds a password. The password
// is never part of an error.
func readRegistryCredentialFile(path string) (user, pass string, err error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", "", fmt.Errorf("--registry-credential-file: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return "", "", fmt.Errorf("--registry-credential-file %s is not a regular file", path)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		return "", "", fmt.Errorf("--registry-credential-file %s has mode %04o: it holds a password, make it readable by you only (chmod 600)", path, fi.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("--registry-credential-file: %w", err)
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return "", "", fmt.Errorf("--registry-credential-file %s is empty", path)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\r\n"), "\n")
	if len(lines) != 1 {
		return "", "", fmt.Errorf("--registry-credential-file %s must hold exactly one line, username:password", path)
	}
	line := strings.TrimSuffix(lines[0], "\r")
	user, pass, ok := strings.Cut(line, ":")
	if !ok || user == "" || pass == "" {
		return "", "", fmt.Errorf("--registry-credential-file %s must hold one line username:password", path)
	}
	return user, pass, nil
}
