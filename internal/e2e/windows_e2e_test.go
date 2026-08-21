//go:build windows

package e2e

import "testing"

func TestRealCLIE2ESuiteIsUnsupportedOnWindows(t *testing.T) {
	t.Skip("real-CLI E2E suite is Linux/macOS-only until native Windows fixtures exist")
}
