//go:build !unix

package agentkit

// dirWritable cannot be checked here without writing: the real write then fails as it did before. A dry
// run writes nothing anyway.
func dirWritable(string) bool { return true }
