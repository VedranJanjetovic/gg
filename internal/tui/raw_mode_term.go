//go:build unix || windows

package tui

import (
	"io"

	"github.com/muesli/cancelreader"
	"golang.org/x/term"
)

// prepareRawMode owns raw-mode setup for the wrapped input. Bubble Tea 0.25
// only discovers a console when its input is a concrete *os.File; our EOF
// wrapper must remain in the input path so EOF notification and cancelreader
// support are preserved. Set and restore raw mode here instead.
//
// golang.org/x/term backs this on every unix target and on Windows, where
// MakeRaw/Restore are implemented with GetConsoleMode/SetConsoleMode, so the
// same code is correct on all of them.
func prepareRawMode(input io.Reader) (func() error, error) {
	file, ok := input.(cancelreader.File)
	if !ok {
		return func() error { return nil }, nil
	}
	fd := int(file.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	return func() error { return term.Restore(fd, state) }, nil
}
