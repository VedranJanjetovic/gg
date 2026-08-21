package tui

import "github.com/muesli/reflow/wordwrap"

// minWrapWidth keeps degenerate terminal sizes readable instead of producing
// one-character columns.
const minWrapWidth = 20

// wrapToWidth wraps text at word boundaries so full-screen views never break
// words at the terminal edge. Words longer than the width are left intact and
// wrap at the terminal as a last resort.
func wrapToWidth(text string, width int) string {
	if width < minWrapWidth {
		width = minWrapWidth
	}
	return wordwrap.String(text, width)
}
