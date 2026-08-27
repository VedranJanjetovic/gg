//go:build !unix && !windows

package tui

import "io"

// prepareRawMode is intentionally a no-op on the targets golang.org/x/term
// does not implement (js, plan9, wasip1). Those platforms have no console mode
// to take ownership of; full-screen screens are unsupported there anyway.
func prepareRawMode(io.Reader) (func() error, error) {
	return func() error { return nil }, nil
}
