package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/git"
	"github.com/VedranJanjetovic/gg/internal/orchestrator"
	"github.com/VedranJanjetovic/gg/internal/state"
)

// Output is the application-owned output boundary.
type Output struct {
	Stdout io.Writer
	Stderr io.Writer
}

// StateReader is the read-only state seam needed by list and status.
type StateReader interface {
	List(context.Context) ([]state.AgentStatus, error)
	Status(context.Context) (state.Status, error)
}

// Controller aliases the canonical orchestrator contract.
type Controller = orchestrator.Controller

// TUIRunner is the terminal attachment seam used by frontends and tests.
type TUIRunner func(context.Context, ProjectAttachment, io.Reader, io.Writer) error

// ParseProjectSelector returns the canonical selector used by state, git, and paths.
func ParseProjectSelector(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("project selector is required")
	}
	slug, err := git.ProjectSlug(raw)
	if err != nil {
		return "", fmt.Errorf("normalize project selector: %w", err)
	}
	return slug, nil
}

// FirstProjectSelector normalizes the first positional selector, or returns empty
// for no selector so legacy no-argument behavior remains available.
func FirstProjectSelector(args []string) (string, error) {
	for _, arg := range args {
		if arg == "" || strings.HasPrefix(arg, "-") {
			continue
		}
		return ParseProjectSelector(arg)
	}
	return "", nil
}

// ResolveConfiguredFolder validates the current folder and provides actionable guidance.
func ResolveConfiguredFolder(ctx context.Context, cwd func() (string, error), store ConfigureStore) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if cwd == nil {
		return "", errors.New("determine current folder: working-directory resolver is not configured")
	}
	root, err := cwd()
	if err != nil {
		return "", fmt.Errorf("determine current folder: %w", err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve current folder %q: %w", root, err)
	}
	if store == nil {
		return "", errors.New("check project configuration: configuration store is not configured")
	}
	if _, err := store.LoadProject(root); err != nil {
		if errors.Is(err, config.ErrProjectNotConfigured) {
			return "", fmt.Errorf("current folder is not configured; run \"gg configure\" in %s", root)
		}
		return "", fmt.Errorf("check project configuration in %s: %w", root, err)
	}
	return root, nil
}

// ParseConfirmation extracts --yes before -- and preserves all other arguments.
func ParseConfirmation(args []string) (remaining []string, yes bool, err error) {
	remaining = make([]string, 0, len(args))
	afterDelimiter := false
	for _, arg := range args {
		if !afterDelimiter && arg == "--" {
			afterDelimiter = true
			remaining = append(remaining, arg)
			continue
		}
		if !afterDelimiter && arg == "--yes" {
			if yes {
				return nil, false, errors.New("--yes may be specified only once")
			}
			yes = true
			continue
		}
		if !afterDelimiter && strings.HasPrefix(arg, "--yes=") {
			return nil, false, fmt.Errorf("invalid confirmation flag %q; use --yes", arg)
		}
		remaining = append(remaining, arg)
	}
	return remaining, yes, nil
}
