package git

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

var ErrRebaseConflict = errors.New("git rebase has unresolved conflicts")

type RebaseRequest struct{ WorktreePath, Branch, ParentBranch, BaseRef string }
type FetchResult struct{ ParentBranch, Output string }
type RebaseResult struct {
	Branch, BaseRef, Output string
	Conflict                *ConflictEvidence
}
type PushResult struct{ Branch, Output string }
type BranchInspection struct{ Branch, BaseRef, BaseHead string }
type ConflictEvidence struct {
	Paths  []string
	Output string
}

func (c *Client) FetchParent(ctx context.Context, parentBranch string) (FetchResult, error) {
	parentBranch = strings.TrimSpace(parentBranch)
	if err := validateRef(parentBranch, "parent branch"); err != nil {
		return FetchResult{}, err
	}
	output, err := c.run(ctx, Command{Dir: c.root, Name: "git", Args: []string{"fetch", "origin", parentBranch}})
	result := FetchResult{ParentBranch: parentBranch, Output: output}
	if err != nil {
		// Surface git's own message ("couldn't find remote ref …") instead
		// of a bare exit status.
		if detail := strings.TrimSpace(output); detail != "" {
			return result, fmt.Errorf("fetch git parent %q: %w: %s", parentBranch, err, detail)
		}
		return result, fmt.Errorf("fetch git parent %q: %w", parentBranch, err)
	}
	return result, nil
}

// RemoteURL returns the configured URL of the named remote at the repository
// root, exactly as stored in git config (it may embed a credential; callers
// must never log it).
func (c *Client) RemoteURL(ctx context.Context, remote string) (string, error) {
	remote = strings.TrimSpace(remote)
	if err := validateRef(remote, "remote"); err != nil {
		return "", err
	}
	output, err := c.run(ctx, Command{Dir: c.root, Name: "git", Args: []string{"config", "--get", "remote." + remote + ".url"}})
	if err != nil {
		return "", fmt.Errorf("read git remote %q url: %w", remote, err)
	}
	return strings.TrimSpace(output), nil
}

// DefaultBranch detects the repository's default branch: the local
// origin/HEAD ref when present, otherwise the remote's advertised HEAD,
// otherwise the branch checked out at the repository root. It returns ""
// when nothing can be detected so callers keep their configured fallback.
func (c *Client) DefaultBranch(ctx context.Context) string {
	if output, err := c.run(ctx, Command{Dir: c.root, Name: "git", Args: []string{"symbolic-ref", "--short", "refs/remotes/origin/HEAD"}}); err == nil {
		if branch := strings.TrimSpace(output); branch != "" {
			return strings.TrimPrefix(branch, "origin/")
		}
	}
	if output, err := c.run(ctx, Command{Dir: c.root, Name: "git", Args: []string{"ls-remote", "--symref", "origin", "HEAD"}}); err == nil {
		for _, line := range strings.Split(output, "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 3 && fields[0] == "ref:" && fields[2] == "HEAD" {
				if branch := strings.TrimPrefix(fields[1], "refs/heads/"); branch != fields[1] {
					return branch
				}
			}
		}
	}
	if output, err := c.run(ctx, Command{Dir: c.root, Name: "git", Args: []string{"branch", "--show-current"}}); err == nil {
		return strings.TrimSpace(output)
	}
	return ""
}

func (c *Client) RebaseProject(ctx context.Context, request RebaseRequest) (RebaseResult, error) {
	request.WorktreePath = cleanWorktreePath(request.WorktreePath)
	request.Branch, request.BaseRef = strings.TrimSpace(request.Branch), strings.TrimSpace(request.BaseRef)
	if request.WorktreePath == "" || !filepath.IsAbs(request.WorktreePath) {
		return RebaseResult{}, errors.New("git rebase worktree path must be absolute")
	}
	if request.Branch == "" {
		return RebaseResult{}, errors.New("git rebase branch is required")
	}
	if err := validateRef(request.BaseRef, "base ref"); err != nil {
		return RebaseResult{}, err
	}
	output, err := c.run(ctx, Command{Dir: request.WorktreePath, Name: "git", Args: []string{"rebase", request.BaseRef}})
	result := RebaseResult{Branch: request.Branch, BaseRef: request.BaseRef, Output: output}
	if err == nil {
		return result, nil
	}
	evidence, evidenceErr := c.ConflictEvidence(ctx, request.WorktreePath, output)
	if evidenceErr != nil {
		return result, fmt.Errorf("rebase git branch %q onto %q: %w; inspect conflicts: %v", request.Branch, request.BaseRef, err, evidenceErr)
	}
	result.Conflict = &evidence
	if len(evidence.Paths) > 0 {
		return result, fmt.Errorf("%w: branch %q onto %q: %v", ErrRebaseConflict, request.Branch, request.BaseRef, err)
	}
	return result, fmt.Errorf("rebase git branch %q onto %q: %w", request.Branch, request.BaseRef, err)
}

func (c *Client) PushBranch(ctx context.Context, worktreePath, branch string) (PushResult, error) {
	worktreePath, branch = cleanWorktreePath(worktreePath), strings.TrimSpace(branch)
	if worktreePath == "" || !filepath.IsAbs(worktreePath) {
		return PushResult{}, errors.New("git push worktree path must be absolute")
	}
	if err := validateRef(branch, "branch"); err != nil {
		return PushResult{}, err
	}
	output, err := c.run(ctx, Command{Dir: worktreePath, Name: "git", Args: []string{"push", "origin", branch}})
	result := PushResult{Branch: branch, Output: output}
	if err != nil {
		return result, fmt.Errorf("push git branch %q: %w", branch, err)
	}
	return result, nil
}

func (c *Client) InspectBranch(ctx context.Context, worktreePath, baseRef string) (BranchInspection, error) {
	worktreePath, baseRef = cleanWorktreePath(worktreePath), strings.TrimSpace(baseRef)
	if worktreePath == "" || !filepath.IsAbs(worktreePath) {
		return BranchInspection{}, errors.New("git branch inspection path must be absolute")
	}
	if err := validateRef(baseRef, "base ref"); err != nil {
		return BranchInspection{}, err
	}
	branchOutput, err := c.run(ctx, Command{Dir: worktreePath, Name: "git", Args: []string{"branch", "--show-current"}})
	if err != nil {
		return BranchInspection{}, fmt.Errorf("inspect current git branch: %w", err)
	}
	branch := strings.TrimSpace(branchOutput)
	if branch == "" {
		return BranchInspection{}, errors.New("inspect current git branch: git returned an empty branch")
	}
	baseOutput, err := c.run(ctx, Command{Dir: worktreePath, Name: "git", Args: []string{"rev-parse", "--verify", baseRef}})
	if err != nil {
		return BranchInspection{Branch: branch, BaseRef: baseRef}, fmt.Errorf("resolve git base ref %q: %w", baseRef, err)
	}
	baseHead := strings.TrimSpace(baseOutput)
	if baseHead == "" {
		return BranchInspection{Branch: branch, BaseRef: baseRef}, fmt.Errorf("resolve git base ref %q: git returned an empty commit", baseRef)
	}
	return BranchInspection{Branch: branch, BaseRef: baseRef, BaseHead: baseHead}, nil
}

func (c *Client) ConflictEvidence(ctx context.Context, worktreePath string, priorOutput ...string) (ConflictEvidence, error) {
	worktreePath = cleanWorktreePath(worktreePath)
	if worktreePath == "" || !filepath.IsAbs(worktreePath) {
		return ConflictEvidence{}, errors.New("git conflict inspection path must be absolute")
	}
	output, err := c.run(ctx, Command{Dir: worktreePath, Name: "git", Args: []string{"diff", "--name-only", "--diff-filter=U", "--"}})
	if err != nil {
		return ConflictEvidence{}, fmt.Errorf("read unresolved git paths: %w", err)
	}
	combined := output
	if len(priorOutput) > 0 {
		combined = priorOutput[0] + output
	}
	return ConflictEvidence{Paths: parseConflictPaths(output), Output: combined}, nil
}

func (c *Client) run(ctx context.Context, command Command) (string, error) {
	if c == nil || c.executor == nil {
		return "", errors.New("git client is nil")
	}
	if c.dryRun {
		c.commands = append(c.commands, command)
		return "", nil
	}
	return c.execute(ctx, command)
}

func validateRef(ref, label string) error {
	if ref == "" {
		return fmt.Errorf("git %s is required", label)
	}
	if strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, "\r\n\x00") {
		return fmt.Errorf("git %s is invalid", label)
	}
	return nil
}

func parseConflictPaths(output string) []string {
	var paths []string
	seen := make(map[string]struct{})
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		path := strings.TrimSpace(line)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	return paths
}
