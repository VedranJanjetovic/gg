package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/VedranJanjetovic/gg/internal/state"
)

type listOptions struct{ includeFinished bool }

func parseListOptions(args []string) (listOptions, error) {
	var options listOptions
	for _, arg := range args {
		switch arg {
		case "-a", "--all":
			if options.includeFinished {
				return listOptions{}, errors.New("list: -a may be specified only once")
			}
			options.includeFinished = true
		default:
			return listOptions{}, fmt.Errorf("list does not accept argument %q", arg)
		}
	}
	return options, nil
}

func (a *App) listCommand(ctx context.Context, stdout io.Writer, args []string) error {
	options, err := parseListOptions(args)
	if err != nil {
		return err
	}
	if err := a.requireConfiguredProject(); err != nil {
		return err
	}
	projects, err := a.Projects(ctx)
	if err != nil {
		return fmt.Errorf("list projects: %w", err)
	}
	projects = filterProjects(projects, options.includeFinished)
	if len(projects) == 0 {
		_, err := fmt.Fprintln(stdout, "No gg projects found. (No gg agents found.)")
		return err
	}
	return writeProjectList(stdout, projects)
}

func filterProjects(projects []state.ProjectState, includeFinished bool) []state.ProjectState {
	filtered := make([]state.ProjectState, 0, len(projects))
	for _, project := range projects {
		if !includeFinished && project.Status.IsTerminal() {
			continue
		}
		filtered = append(filtered, project)
	}
	return filtered
}

func writeProjectList(output io.Writer, projects []state.ProjectState) error {
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "NAME\tSTATUS\tCURRENT PHASE"); err != nil {
		return err
	}
	for _, project := range projects {
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\n", project.Name, project.Status, displayValue(project.CurrentPhase)); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func displayValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}
