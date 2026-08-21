//go:build windows

package cli

import "syscall"

const (
	createNewProcessGroup = 0x00000200
	detachedProcess       = 0x00000008
)

// detachedSysProcAttr starts the child detached from the parent's console so
// it survives the parent gg exiting and its terminal closing.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | detachedProcess}
}
