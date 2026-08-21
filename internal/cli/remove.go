package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/VedranJanjetovic/gg/internal/git"
	"github.com/VedranJanjetovic/gg/internal/state"
)

// removeCommand removes one specific project regardless of its parked status
// (failed, stopped, pending, or done): state, history, worktree, and owned
// branch. Running projects are refused — stop them first. `--yes` skips the
// confirmation prompt.
func (a *App) removeCommand(ctx context.Context, stdout io.Writer, args []string) error {
	selectorArgs, yes, err := ParseConfirmation(args)
	if err != nil {
		return err
	}
	if len(selectorArgs) != 1 {
		return fmt.Errorf("remove requires exactly one project selector")
	}
	if err := a.requireConfiguredProject(); err != nil {
		return err
	}
	slug, err := git.ProjectSlug(selectorArgs[0])
	if err != nil {
		return fmt.Errorf("normalize project selector: %w", err)
	}
	service, err := a.projectService(ctx)
	if err != nil {
		return fmt.Errorf("load project service: %w", err)
	}
	project, err := service.Load(ctx, slug)
	if err != nil {
		return fmt.Errorf("load project %q: %w", slug, err)
	}
	if project.Status == state.StatusRunning {
		return fmt.Errorf("project %q is running — stop it first (gg stop %s)", slug, slug)
	}
	if !yes {
		confirmed, confirmErr := a.confirmRemove(ctx, stdout, project)
		if confirmErr != nil {
			return confirmErr
		}
		if !confirmed {
			_, err := fmt.Fprintln(stdout, "Remove cancelled.")
			return err
		}
	}
	if err := a.pruneProject(ctx, service, project); err != nil {
		return fmt.Errorf("remove project %q: %w", slug, err)
	}
	_, err = fmt.Fprintf(stdout, "Removed project %q (%s).\n", project.Name, project.Status)
	return err
}

func (a *App) confirmRemove(ctx context.Context, stdout io.Writer, project state.ProjectState) (bool, error) {
	input := a.input
	if input == nil {
		input = os.Stdin
	}
	if _, err := fmt.Fprintf(stdout, "Remove project %q (%s) including its worktree and branch? [y/N]: ", project.Name, project.Status); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	line, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && line == "" {
		return false, nil
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}
