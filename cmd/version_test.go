package cmd

import (
	"bytes"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	old := Version
	Version = "1.2.3-test"
	defer func() { Version = old }()
	var b bytes.Buffer
	versionCmd.SetOut(&b)
	versionCmd.Run(versionCmd, nil)
	if b.String() != "tnctl 1.2.3-test\n" {
		t.Errorf("got %q", b.String())
	}
}
