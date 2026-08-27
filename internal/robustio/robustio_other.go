//go:build !windows

package robustio

// retry runs the operation once: only Windows reports transient sharing
// failures for otherwise valid file operations.
func retry(operation func() error) error { return operation() }
