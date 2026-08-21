package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/VedranJanjetovic/gg/internal/state"
)

const DefaultGlobalRefreshInterval = time.Second

type FolderLister func(context.Context) ([]string, error)
type ProjectLister func(context.Context, string) ([]state.ProjectState, error)

type GlobalController struct {
	folders        FolderLister
	projects       ProjectLister
	refreshTimeout time.Duration
}
type GlobalControllerOption func(*GlobalController)

func WithRefreshTimeout(timeout time.Duration) GlobalControllerOption {
	return func(c *GlobalController) { c.refreshTimeout = timeout }
}

func NewGlobalController(folders FolderLister, projects ProjectLister, options ...GlobalControllerOption) (*GlobalController, error) {
	if folders == nil || projects == nil {
		return nil, errors.New("global controller requires folder and project listers")
	}
	c := &GlobalController{folders: folders, projects: projects, refreshTimeout: 2 * DefaultGlobalRefreshInterval}
	for _, option := range options {
		option(c)
	}
	if c.refreshTimeout < 0 {
		return nil, errors.New("global refresh timeout cannot be negative")
	}
	return c, nil
}

func (c *GlobalController) Refresh(ctx context.Context) (GlobalSnapshot, error) {
	if c == nil || c.folders == nil || c.projects == nil {
		return GlobalSnapshot{}, errors.New("global controller is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return GlobalSnapshot{}, err
	}
	folders, err := c.folders(ctx)
	if err != nil {
		return GlobalSnapshot{}, fmt.Errorf("list configured folders: %w", err)
	}
	canonical := uniqueFolders(folders)
	groups := make([]FolderObservation, 0, len(canonical))
	for _, folder := range canonical {
		if err := ctx.Err(); err != nil {
			return GlobalSnapshot{}, err
		}
		listCtx := ctx
		cancel := func() {}
		if c.refreshTimeout > 0 {
			listCtx, cancel = context.WithTimeout(ctx, c.refreshTimeout)
		}
		projects, listErr := c.projects(listCtx, folder)
		cancel()
		if listErr != nil {
			return GlobalSnapshot{}, fmt.Errorf("list projects in %s: %w", folder, listErr)
		}
		ordered := append([]state.ProjectState(nil), projects...)
		sort.SliceStable(ordered, func(i, j int) bool {
			if ordered[i].Slug == ordered[j].Slug {
				return ordered[i].Name < ordered[j].Name
			}
			return ordered[i].Slug < ordered[j].Slug
		})
		groups = append(groups, FolderObservation{Folder: folder, Projects: state.ObserveAll(ordered)})
	}
	return GlobalSnapshot{Folders: groups}, nil
}

func uniqueFolders(folders []string) []string {
	seen := make(map[string]struct{}, len(folders))
	result := make([]string, 0, len(folders))
	for _, folder := range folders {
		folder = strings.TrimSpace(folder)
		if folder == "" {
			continue
		}
		if absolute, err := filepath.Abs(folder); err == nil {
			folder = filepath.Clean(absolute)
		}
		if _, ok := seen[folder]; ok {
			continue
		}
		seen[folder] = struct{}{}
		result = append(result, folder)
	}
	sort.Strings(result)
	return result
}

type FolderObservation struct {
	Folder   string
	Projects []state.ProjectObservation
}
type GlobalSnapshot struct{ Folders []FolderObservation }

func (s GlobalSnapshot) ProjectCount() int {
	count := 0
	for _, folder := range s.Folders {
		count += len(folder.Projects)
	}
	return count
}

// ProjectAt returns the deterministic flattened project row for a zero-based
// index. Folder and project order are established by Refresh.
func (s GlobalSnapshot) ProjectAt(index int) (state.ProjectState, bool) {
	if index < 0 {
		return state.ProjectState{}, false
	}
	for _, folder := range s.Folders {
		if index < len(folder.Projects) {
			return folder.Projects[index].Project, true
		}
		index -= len(folder.Projects)
	}
	return state.ProjectState{}, false
}

func (s GlobalSnapshot) projectIndex(slug string) int {
	index := 1
	for _, folder := range s.Folders {
		for _, observation := range folder.Projects {
			if observation.Project.Slug == slug {
				return index
			}
			index++
		}
	}
	return index
}
