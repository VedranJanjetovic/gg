package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/VedranJanjetovic/gg/internal/state"
	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"
)

// ErrGroomingRequested reports that the user pressed g to re-enter the
// grooming interview; the caller re-runs the interview and re-attaches.
var ErrGroomingRequested = errors.New("grooming interview requested")

// ErrInteractiveRequested reports that the user pressed i to open the
// interactive session picker (QA chat or feedback loop)
// chat; the caller runs it and re-attaches.
var ErrInteractiveRequested = errors.New("project interactive session requested")

// ErrConfigureRequested reports that the user pressed e to edit a parked
// project's execution tuples. The caller must release the progress terminal
// before opening the configuration picker.
var ErrConfigureRequested = errors.New("project configuration requested")

// Run attaches to a project. When either stream is not a terminal it performs
// a deterministic one-shot render instead of initializing Bubble Tea.
func Run(ctx context.Context, project state.ProjectState, loader Loader, actions Actions, input io.Reader, output io.Writer, options ...Option) error {
	if input == nil {
		input = os.Stdin
	}
	if output == nil {
		output = os.Stdout
	}
	if !interactiveTerminal(input, output) {
		switch project.Status {
		case state.StatusPending:
			if actions.Start != nil {
				if err := actions.Start(ctx); err != nil {
					return fmt.Errorf("start project: %w", err)
				}
			}
		case state.StatusStopped:
			if actions.Resume != nil {
				if err := actions.Resume(ctx); err != nil {
					return fmt.Errorf("resume project: %w", err)
				}
			}
		}
		return WriteStatus(ctx, output, project, loader, options...)
	}

	model, err := NewModel(ctx, project, loader, actions, options...)
	if err != nil {
		return err
	}
	final, err := tea.NewProgram(model, tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output)).Run()
	if err != nil {
		if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("run project TUI: %w", err)
	}
	if finalModel, ok := final.(Model); ok {
		if finalModel.groomingRequested {
			return ErrGroomingRequested
		}
		if finalModel.interactiveRequested {
			return ErrInteractiveRequested
		}
		if finalModel.configureRequested {
			return ErrConfigureRequested
		}
		if finalModel.LastError() != nil {
			return finalModel.LastError()
		}
	}
	return nil
}

// WriteStatus loads state once and writes stable, uncolored status output.
func WriteStatus(ctx context.Context, output io.Writer, project state.ProjectState, loader Loader, options ...Option) error {
	if output == nil {
		return errors.New("status output is required")
	}
	if loader != nil {
		latest, err := loader(ctx)
		if err != nil {
			return fmt.Errorf("load project status: %w", err)
		}
		project = latest
	}
	plainOptions := append(append([]Option(nil), options...), WithColor(false))
	model, err := NewModel(ctx, project, nil, Actions{}, plainOptions...)
	if err != nil {
		return err
	}
	_, err = io.WriteString(output, model.statusView())
	return err
}

func interactiveTerminal(input io.Reader, output io.Writer) bool {
	in, inputIsFile := input.(*os.File)
	out, outputIsFile := output.(*os.File)
	return inputIsFile && outputIsFile && term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(out.Fd()))
}

// InteractiveTerminal reports whether the input/output pair can host a
// full-screen session; non-TTY callers should use line-oriented flows.
func InteractiveTerminal(input io.Reader, output io.Writer) bool {
	return interactiveTerminal(input, output)
}
