package templates

// goldenData is the fixed placeholder input for the golden files. The values
// are non-PEM, non-secret placeholders.
func goldenData() TemplateData {
	return TemplateData{
		Domain:       "example.test",
		Namespace:    "golden-ns",
		DeviceID:     "golden-device",
		RSAPrivate:   "TEST-ONLY-rsa-private\nTEST-ONLY-rsa-private-line2",
		RSAPublic:    "TEST-ONLY-rsa-public\nTEST-ONLY-rsa-public-line2",
		Username:     "golden-user",
		Password:     "TEST-ONLY-password",
		PublicSSHKey: "ssh-ed25519 TEST-ONLY-ssh-public golden@example.test",
		K3SToken:     "TEST-ONLY-k3s-token",
		DockerSecret: "TEST-ONLY-docker-secret",
	}
}
