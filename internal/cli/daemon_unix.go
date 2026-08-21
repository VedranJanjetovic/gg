//go:build !windows

package cli

import "syscall"

// detachedSysProcAttr starts the child in its own session so it survives the
// parent gg exiting and its controlling terminal closing.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
