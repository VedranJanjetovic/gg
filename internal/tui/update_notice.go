package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
)

// updateNotice is the footer shown once a newer gg release is known to exist.
const updateNotice = "Update is there! Run: gg update"

// UpdateChecker reports whether a newer gg release is available. It runs as a
// background Bubble Tea command, so a slow or unreachable release source never
// delays rendering.
type UpdateChecker func(context.Context) (bool, error)

type updateAvailableMsg struct {
	available bool
	err       error
}

func checkUpdateCmd(ctx context.Context, check UpdateChecker) tea.Cmd {
	return func() tea.Msg {
		available, err := check(ctx)
		return updateAvailableMsg{available: available, err: err}
	}
}
