//go:build !unix && !windows

package agent

import "testing"

func runPlatformFakeAgent(t *testing.T, _ string) {
	t.Helper()
}
