package tui

import "github.com/charmbracelet/lipgloss"

type styles struct {
	title     lipgloss.Style
	muted     lipgloss.Style
	success   lipgloss.Style
	running   lipgloss.Style
	failed    lipgloss.Style
	stopped   lipgloss.Style
	key       lipgloss.Style
	keyChip   lipgloss.Style
	errorText lipgloss.Style
	update    lipgloss.Style
}

func newStyles(color bool) styles {
	if !color {
		return styles{}
	}
	return styles{
		title:   lipgloss.NewStyle().Bold(true),
		muted:   lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		success: lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		running: lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
		failed:  lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
		stopped: lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		key:     lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
		// The chip carries its own padding so the uncoloured fallback renders a
		// bare letter instead of a stray double space.
		keyChip:   lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("6")).Bold(true).Padding(0, 1),
		errorText: lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
		update:    lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
	}
}
