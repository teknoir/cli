package main

import "github.com/teknoir/cli/cmd"

// version is set by goreleaser (-X main.version=...).
var version = "dev"

func main() {
	cmd.Version = version
	cmd.Execute()
}
