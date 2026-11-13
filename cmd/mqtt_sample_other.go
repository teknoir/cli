//go:build !unix

package cmd

import "os"

// mqttNoFollow is 0 where there is no O_NOFOLLOW.
const mqttNoFollow = 0

// mqttSingleLink is a no-op where a link count is not available.
func mqttSingleLink(os.FileInfo, string) error { return nil }
