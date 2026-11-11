//go:build !unix

package coder

import "testing"

// processAlive is not checkable on this platform; the process tests are skipped there anyway.
func processAlive(*testing.T, string) bool { return false }
