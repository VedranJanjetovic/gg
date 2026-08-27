//go:build !unix && !windows

package agent

import (
	"os/exec"
)

// processGroup is the stub used on platforms with no process-group primitive we
// support. Processes start normally but cannot be terminated as a tree.
type processGroup struct{}

func newProcessGroup() *processGroup { return &processGroup{} }

func (g *processGroup) configure(cmd *exec.Cmd) {}

func (g *processGroup) attach(cmd *exec.Cmd) error { return nil }

func (g *processGroup) terminate() error { return exec.ErrNotFound }

func (g *processGroup) release() {}
