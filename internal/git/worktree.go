package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var (
	ErrWorktreeNotFound    = errors.New("git worktree not found")
	ErrWorktreePathInUse   = errors.New("git worktree path is already in use")
	ErrWorktreeBranchInUse = errors.New("git worktree branch is already in use")
	ErrWorktreeMismatch    = errors.New("git worktree does not match requested ownership")
	ErrUnsafeWorktree      = errors.New("refusing unsafe worktree removal")
)

// Worktree is one entry from git worktree list --porcelain.
type Worktree struct {
	Path, Head, Branch string
	Detached, Bare     bool
}

// WorktreeRequest identifies an owned worktree and its creation base.
type WorktreeRequest struct{ Path, Branch, BaseRef string }

// ListWorktrees returns all worktrees known to the repository.
func (c *Client) ListWorktrees(ctx context.Context) ([]Worktree, error) {
	if c == nil {
		return nil, errors.New("git client is nil")
	}
	if err := c.ValidateRepository(ctx); err != nil {
		return nil, err
	}
	command := Command{Dir: c.root, Name: "git", Args: []string{"worktree", "list", "--porcelain"}}
	if c.dryRun {
		c.commands = append(c.commands, command)
		return nil, nil
	}
	output, err := c.execute(ctx, command)
	if err != nil {
		return nil, fmt.Errorf("list git worktrees: %w", err)
	}
	worktrees, err := parseWorktreePorcelain(output)
	if err != nil {
		return nil, fmt.Errorf("parse git worktrees: %w", err)
	}
	return worktrees, nil
}

// LookupWorktree finds an exact path or branch. Supplying both requires both fields to match.
func (c *Client) LookupWorktree(ctx context.Context, path, branch string) (Worktree, error) {
	path = cleanWorktreePath(path)
	branch = strings.TrimSpace(branch)
	if path == "" && branch == "" {
		return Worktree{}, errors.New("worktree path or branch is required")
	}
	worktrees, err := c.ListWorktrees(ctx)
	if err != nil {
		return Worktree{}, err
	}
	for _, worktree := range worktrees {
		pathMatch := path != "" && worktreePathsEqual(worktree.Path, path)
		branchMatch := branch != "" && worktree.Branch == branch
		if pathMatch || branchMatch {
			if path != "" && branch != "" && (!pathMatch || !branchMatch) {
				return Worktree{}, fmt.Errorf("%w: requested path %q and branch %q, found path %q and branch %q", ErrWorktreeMismatch, path, branch, worktree.Path, worktree.Branch)
			}
			if pathMatch {
				// Preserve the caller's spelling when Git reports the same path
				// through a platform-specific symlink such as macOS /var.
				worktree.Path = path
			}
			return worktree, nil
		}
	}
	return Worktree{}, fmt.Errorf("%w: path %q branch %q", ErrWorktreeNotFound, path, branch)
}

// EnsureWorktree safely reuses an owned worktree or creates it from BaseRef.
// The created result is true only when this invocation successfully ran the add
// command and verified the resulting worktree. Callers must use it, rather than
// a separate preflight lookup, to decide whether they own cleanup.
func (c *Client) EnsureWorktree(ctx context.Context, request WorktreeRequest) (Worktree, bool, error) {
	request.Path = cleanWorktreePath(request.Path)
	request.Branch = strings.TrimSpace(request.Branch)
	request.BaseRef = strings.TrimSpace(request.BaseRef)
	if request.Path == "" || !filepath.IsAbs(request.Path) {
		return Worktree{}, false, errors.New("worktree path must be absolute")
	}
	if request.Branch == "" {
		return Worktree{}, false, errors.New("worktree branch is required")
	}
	if request.BaseRef == "" {
		return Worktree{}, false, errors.New("worktree base ref is required")
	}
	worktrees, err := c.ListWorktrees(ctx)
	if err != nil {
		return Worktree{}, false, err
	}
	for _, worktree := range worktrees {
		pathMatch := worktreePathsEqual(worktree.Path, request.Path)
		branchMatch := worktree.Branch == request.Branch
		switch {
		case pathMatch && branchMatch:
			worktree.Path = request.Path
			return worktree, false, nil
		case pathMatch:
			return Worktree{}, false, fmt.Errorf("%w: path %q belongs to branch %q, requested %q", ErrWorktreePathInUse, request.Path, worktree.Branch, request.Branch)
		case branchMatch:
			return Worktree{}, false, fmt.Errorf("%w: branch %q belongs to path %q, requested %q", ErrWorktreeBranchInUse, request.Branch, worktree.Path, request.Path)
		}
	}
	if !c.dryRun {
		if _, err := os.Lstat(request.Path); err == nil {
			return Worktree{}, false, fmt.Errorf("%w: %q exists but is not a matching git worktree", ErrWorktreePathInUse, request.Path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return Worktree{}, false, fmt.Errorf("inspect worktree path %q: %w", request.Path, err)
		}
	}
	command := Command{Dir: c.root, Name: "git", Args: []string{"worktree", "add", "-b", request.Branch, request.Path, request.BaseRef}}
	if c.dryRun {
		c.commands = append(c.commands, command)
		return Worktree{Path: request.Path, Branch: request.Branch}, false, nil
	}
	if output, err := c.execute(ctx, command); err != nil {
		detail := strings.TrimSpace(output)
		if strings.Contains(detail, "already exists") {
			return Worktree{}, false, fmt.Errorf("create git worktree %q: branch %q is left over from a previous project — delete it with `git branch -D %s` or use a different project name: %w", request.Path, request.Branch, request.Branch, err)
		}
		if detail != "" {
			return Worktree{}, false, fmt.Errorf("create git worktree %q: %w: %s", request.Path, err, detail)
		}
		return Worktree{}, false, fmt.Errorf("create git worktree %q: %w", request.Path, err)
	}
	worktree, err := c.LookupWorktree(ctx, request.Path, request.Branch)
	if err != nil {
		return Worktree{}, false, fmt.Errorf("verify created git worktree: %w", err)
	}
	return worktree, true, nil
}

// RemoveWorktree removes only a clean, exact owned worktree. Force removal is not exposed.
// RemoveWorktree refuses to delete a worktree with uncommitted changes.
func (c *Client) RemoveWorktree(ctx context.Context, path, branch string) error {
	return c.removeWorktree(ctx, path, branch, false)
}

// RemoveWorktreeForced deletes an owned worktree even when it carries
// uncommitted changes (pruning a terminal project discards leftovers such as
// ignored artifacts by design). Ownership and identity checks still apply.
func (c *Client) RemoveWorktreeForced(ctx context.Context, path, branch string) error {
	return c.removeWorktree(ctx, path, branch, true)
}

func (c *Client) removeWorktree(ctx context.Context, path, branch string, force bool) error {
	path = cleanWorktreePath(path)
	branch = strings.TrimSpace(branch)
	if path == "" || !filepath.IsAbs(path) || branch == "" {
		return fmt.Errorf("%w: absolute path and branch are required", ErrUnsafeWorktree)
	}
	worktree, err := c.LookupWorktree(ctx, path, branch)
	if err != nil {
		if c.dryRun && errors.Is(err, ErrWorktreeNotFound) {
			// Dry-run has no live Git listing by definition. The validated request
			// is the planned owned worktree; continue constructing the complete
			// safe removal command sequence instead of treating it as absent.
			worktree = Worktree{Path: path, Branch: branch}
		} else {
			if errors.Is(err, ErrWorktreeNotFound) {
				if _, statErr := os.Lstat(path); statErr == nil {
					return fmt.Errorf("%w: expected owned worktree path %q is occupied", ErrWorktreePathInUse, path)
				} else if errors.Is(statErr, os.ErrNotExist) {
					return nil
				} else {
					return fmt.Errorf("inspect expected worktree path %q: %w", path, statErr)
				}
			}
			if errors.Is(err, ErrWorktreeMismatch) {
				return fmt.Errorf("%w: %v", ErrUnsafeWorktree, err)
			}
			return err
		}
	}
	if !worktreePathsEqual(worktree.Path, path) || worktree.Branch != branch || worktree.Bare || worktree.Detached {
		return fmt.Errorf("%w: path %q branch %q is not an owned attached worktree", ErrUnsafeWorktree, path, branch)
	}
	statusCommand := Command{Dir: path, Name: "git", Args: []string{"status", "--porcelain", "--untracked-files=all"}}
	removeArgs := []string{"worktree", "remove"}
	if force {
		removeArgs = append(removeArgs, "--force")
	}
	removeArgs = append(removeArgs, "--", path)
	removeCommand := Command{Dir: c.root, Name: "git", Args: removeArgs}
	branchCommand := Command{Dir: c.root, Name: "git", Args: []string{"branch", "-D", "--", branch}}
	if c.dryRun {
		c.commands = append(c.commands, statusCommand, removeCommand, branchCommand)
		return nil
	}
	if !force {
		output, err := c.execute(ctx, statusCommand)
		if err != nil {
			return fmt.Errorf("inspect worktree %q before removal: %w", path, err)
		}
		if strings.TrimSpace(output) != "" {
			return fmt.Errorf("%w: worktree %q has uncommitted changes", ErrUnsafeWorktree, path)
		}
	}
	if _, err := c.execute(ctx, removeCommand); err != nil {
		return fmt.Errorf("remove git worktree %q: %w", path, err)
	}
	// Delete the owned branch too: leaving it behind makes recreating a
	// project with the same name fail on `worktree add -b`.
	if output, err := c.execute(ctx, Command{Dir: c.root, Name: "git", Args: []string{"branch", "-D", "--", branch}}); err != nil {
		return fmt.Errorf("delete owned branch %q: %w: %s", branch, err, strings.TrimSpace(output))
	}
	return nil
}

func cleanWorktreePath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	return filepath.Clean(absolute)
}

// PathsEqual reports whether two paths identify the same filesystem location.
// It resolves symlinks for existing paths and resolves the deepest existing
// parent for paths that have not been created yet. A resolution error fails
// closed, which keeps ownership checks from accepting an uncertain match.
func PathsEqual(left, right string) bool {
	return worktreePathsEqual(left, right)
}

func worktreePathsEqual(left, right string) bool {
	leftIdentity, err := filesystemPathIdentity(left)
	if err != nil {
		return false
	}
	rightIdentity, err := filesystemPathIdentity(right)
	if err != nil {
		return false
	}
	if filepath.IsAbs(leftIdentity) != filepath.IsAbs(rightIdentity) {
		return false
	}
	if leftIdentity == rightIdentity {
		return true
	}

	leftParent, leftMissing, err := existingPathParts(left)
	if err != nil {
		return false
	}
	rightParent, rightMissing, err := existingPathParts(right)
	if err != nil {
		return false
	}
	leftInfo, err := os.Stat(leftParent)
	if err != nil {
		return false
	}
	rightInfo, err := os.Stat(rightParent)
	if err != nil || !os.SameFile(leftInfo, rightInfo) || len(leftMissing) != len(rightMissing) {
		return false
	}
	if len(leftMissing) == 0 {
		return true
	}
	if !caseInsensitiveVolume(leftParent) {
		return false
	}
	for i := range leftMissing {
		if !strings.EqualFold(leftMissing[i], rightMissing[i]) {
			return false
		}
	}
	return true
}

func filesystemPathIdentity(path string) (string, error) {
	parent, missing, err := existingPathParts(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", err
	}
	resolved = filepath.Clean(resolved)
	for _, part := range missing {
		resolved = filepath.Join(resolved, part)
	}
	return filepath.Clean(resolved), nil
}

func existingPathParts(path string) (string, []string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil, errors.New("path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", nil, err
	}
	current := filepath.Clean(absolute)
	var missing []string
	for {
		if _, err := os.Stat(current); err == nil {
			for i, j := 0, len(missing)-1; i < j; i, j = i+1, j-1 {
				missing[i], missing[j] = missing[j], missing[i]
			}
			return current, missing, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", nil, err
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", nil, fmt.Errorf("no existing parent for path %q", path)
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func caseInsensitiveVolume(path string) bool {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		return false
	}
	current := filepath.Clean(path)
	for {
		info, err := os.Stat(current)
		if err == nil {
			name := filepath.Base(current)
			alternate := swapCase(name)
			if alternate != name {
				alternateInfo, alternateErr := os.Stat(filepath.Join(filepath.Dir(current), alternate))
				if alternateErr == nil && os.SameFile(info, alternateInfo) {
					return true
				}
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
		current = parent
	}
}

func swapCase(value string) string {
	var result strings.Builder
	result.Grow(len(value))
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			result.WriteRune(r - ('a' - 'A'))
		case r >= 'A' && r <= 'Z':
			result.WriteRune(r + ('a' - 'A'))
		default:
			result.WriteRune(r)
		}
	}
	return result.String()
}

func parseWorktreePorcelain(output string) ([]Worktree, error) {
	var result []Worktree
	var current *Worktree
	flush := func() {
		if current != nil && current.Path != "" {
			result = append(result, *current)
		}
		current = nil
	}
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			flush()
			current = &Worktree{Path: strings.TrimPrefix(line, "worktree ")}
		case current == nil:
			return nil, fmt.Errorf("metadata before worktree: %q", line)
		case strings.HasPrefix(line, "HEAD "):
			current.Head = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			current.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		case line == "detached":
			current.Detached = true
		case line == "bare":
			current.Bare = true
		case strings.HasPrefix(line, "prunable "), (line == "locked" || strings.HasPrefix(line, "locked ")), strings.HasPrefix(line, "reason "), line == "worktreeConfig":
			// Safe Git metadata variants do not affect ownership checks.
		default:
			return nil, fmt.Errorf("unknown metadata %q", line)
		}
	}
	flush()
	return result, nil
}
