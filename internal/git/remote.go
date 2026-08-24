package git

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var ErrRebaseConflict = errors.New("git rebase has unresolved conflicts")

var objectIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{4,64}$`)

// RebaseCheckpoint identifies the clean branch state captured immediately
// before a Rebase execution. Rebase is only safe to retry from a clean
// checkpoint: accepted changes are already commits and the failed operation
// must not discard unrelated working-tree edits.
type RebaseCheckpoint struct {
	WorktreePath string
	Branch       string
	Head         string
}

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

// CaptureRebaseCheckpoint records the attached branch and HEAD and requires a
// clean index/worktree. Development commits are therefore preserved by a
// reset-based restore, while uncommitted caller work is never silently lost.
func (c *Client) CaptureRebaseCheckpoint(ctx context.Context, worktreePath string) (RebaseCheckpoint, error) {
	worktreePath = cleanWorktreePath(worktreePath)
	if worktreePath == "" || !filepath.IsAbs(worktreePath) {
		return RebaseCheckpoint{}, errors.New("git rebase checkpoint path must be absolute")
	}
	branchOutput, err := c.run(ctx, Command{Dir: worktreePath, Name: "git", Args: []string{"branch", "--show-current"}})
	if err != nil {
		return RebaseCheckpoint{}, fmt.Errorf("capture Rebase branch: %w", err)
	}
	branch := strings.TrimSpace(branchOutput)
	if err := validateRef(branch, "Rebase branch"); err != nil {
		return RebaseCheckpoint{}, err
	}
	headOutput, err := c.run(ctx, Command{Dir: worktreePath, Name: "git", Args: []string{"rev-parse", "HEAD"}})
	if err != nil {
		return RebaseCheckpoint{}, fmt.Errorf("capture Rebase HEAD: %w", err)
	}
	head := strings.TrimSpace(headOutput)
	if !objectIDPattern.MatchString(head) {
		return RebaseCheckpoint{}, errors.New("capture Rebase HEAD: git returned an invalid commit")
	}
	status, err := c.run(ctx, Command{Dir: worktreePath, Name: "git", Args: []string{"status", "--porcelain=v1", "--untracked-files=all", "--"}})
	if err != nil {
		return RebaseCheckpoint{}, fmt.Errorf("capture Rebase worktree state: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		return RebaseCheckpoint{}, errors.New("capture Rebase checkpoint: worktree has uncommitted changes")
	}
	return RebaseCheckpoint{WorktreePath: worktreePath, Branch: branch, Head: head}, nil
}

// AbortRebaseIfActive leaves a worktree out of an in-progress rebase. Git's
// verification ref makes the operation idempotent instead of treating the
// normal "no rebase in progress" case as a failure.
func (c *Client) AbortRebaseIfActive(ctx context.Context, worktreePath string) error {
	worktreePath = cleanWorktreePath(worktreePath)
	if worktreePath == "" || !filepath.IsAbs(worktreePath) {
		return errors.New("git rebase abort path must be absolute")
	}
	if _, err := c.run(ctx, Command{Dir: worktreePath, Name: "git", Args: []string{"rev-parse", "--verify", "REBASE_HEAD"}}); err != nil {
		return nil
	}
	if output, err := c.run(ctx, Command{Dir: worktreePath, Name: "git", Args: []string{"rebase", "--abort"}}); err != nil {
		return fmt.Errorf("abort active git rebase: %w: %s", err, strings.TrimSpace(output))
	}
	return nil
}

// RestoreRebaseCheckpoint aborts any partial rebase and restores the exact
// captured branch commit, index, and clean worktree. The final verification
// makes retries fail closed if Git leaves unexpected state behind.
func (c *Client) RestoreRebaseCheckpoint(ctx context.Context, checkpoint RebaseCheckpoint) error {
	if err := validateCheckpoint(checkpoint); err != nil {
		return err
	}
	if err := c.AbortRebaseIfActive(ctx, checkpoint.WorktreePath); err != nil {
		return err
	}
	if _, err := c.run(ctx, Command{Dir: checkpoint.WorktreePath, Name: "git", Args: []string{"checkout", "--force", checkpoint.Branch}}); err != nil {
		return fmt.Errorf("restore Rebase branch %q: %w", checkpoint.Branch, err)
	}
	if _, err := c.run(ctx, Command{Dir: checkpoint.WorktreePath, Name: "git", Args: []string{"reset", "--hard", checkpoint.Head}}); err != nil {
		return fmt.Errorf("restore Rebase HEAD %s: %w", checkpoint.Head, err)
	}
	if _, err := c.run(ctx, Command{Dir: checkpoint.WorktreePath, Name: "git", Args: []string{"clean", "-fd", "--"}}); err != nil {
		return fmt.Errorf("restore Rebase untracked worktree state: %w", err)
	}
	return c.VerifyRebaseCheckpoint(ctx, checkpoint)
}

// VerifyRebaseCheckpoint confirms that Git has no active rebase, unresolved
// index entries, or worktree changes after cleanup.
func (c *Client) VerifyRebaseCheckpoint(ctx context.Context, checkpoint RebaseCheckpoint) error {
	if err := validateCheckpoint(checkpoint); err != nil {
		return err
	}
	if _, err := c.run(ctx, Command{Dir: checkpoint.WorktreePath, Name: "git", Args: []string{"rev-parse", "--verify", "REBASE_HEAD"}}); err == nil {
		return errors.New("verify Rebase checkpoint: rebase is still active")
	}
	branchOutput, err := c.run(ctx, Command{Dir: checkpoint.WorktreePath, Name: "git", Args: []string{"branch", "--show-current"}})
	if err != nil || strings.TrimSpace(branchOutput) != checkpoint.Branch {
		if err != nil {
			return fmt.Errorf("verify Rebase branch: %w", err)
		}
		return fmt.Errorf("verify Rebase branch: got %q, want %q", strings.TrimSpace(branchOutput), checkpoint.Branch)
	}
	headOutput, err := c.run(ctx, Command{Dir: checkpoint.WorktreePath, Name: "git", Args: []string{"rev-parse", "HEAD"}})
	if err != nil || strings.TrimSpace(headOutput) != checkpoint.Head {
		if err != nil {
			return fmt.Errorf("verify Rebase HEAD: %w", err)
		}
		return fmt.Errorf("verify Rebase HEAD: got %q, want %q", strings.TrimSpace(headOutput), checkpoint.Head)
	}
	status, err := c.run(ctx, Command{Dir: checkpoint.WorktreePath, Name: "git", Args: []string{"status", "--porcelain=v1", "--untracked-files=all", "--"}})
	if err != nil {
		return fmt.Errorf("verify Rebase worktree state: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		return fmt.Errorf("verify Rebase worktree state: unexpected changes %q", strings.TrimSpace(status))
	}
	return nil
}

// VerifyRebaseWorktree checks the post-operation index without requiring the
// worktree to be clean; ignored reports and focused test output may be created
// after a successful rebase.
func (c *Client) VerifyRebaseWorktree(ctx context.Context, worktreePath string) error {
	worktreePath = cleanWorktreePath(worktreePath)
	if worktreePath == "" || !filepath.IsAbs(worktreePath) {
		return errors.New("git Rebase verification path must be absolute")
	}
	if _, err := c.run(ctx, Command{Dir: worktreePath, Name: "git", Args: []string{"rev-parse", "--verify", "REBASE_HEAD"}}); err == nil {
		return errors.New("verify Rebase worktree: rebase is still active")
	}
	paths, err := c.run(ctx, Command{Dir: worktreePath, Name: "git", Args: []string{"diff", "--name-only", "--diff-filter=U", "--"}})
	if err != nil {
		return fmt.Errorf("verify Rebase unresolved paths: %w", err)
	}
	if strings.TrimSpace(paths) != "" {
		return fmt.Errorf("verify Rebase unresolved paths: %s", strings.TrimSpace(paths))
	}
	return nil
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
	request.Branch, request.ParentBranch, request.BaseRef = strings.TrimSpace(request.Branch), strings.TrimSpace(request.ParentBranch), strings.TrimSpace(request.BaseRef)
	if request.WorktreePath == "" || !filepath.IsAbs(request.WorktreePath) {
		return RebaseResult{}, errors.New("git rebase worktree path must be absolute")
	}
	if request.Branch == "" {
		return RebaseResult{}, errors.New("git rebase branch is required")
	}
	parentBranch := request.ParentBranch
	if parentBranch == "" {
		// Keep direct adapter callers compatible while the orchestrator always
		// supplies the configured parent explicitly.
		parentBranch = strings.TrimPrefix(request.BaseRef, "origin/")
	}
	if err := validateRef(parentBranch, "parent branch"); err != nil {
		return RebaseResult{}, err
	}
	target := "origin/" + parentBranch
	output, err := c.run(ctx, Command{Dir: request.WorktreePath, Name: "git", Args: []string{"rebase", target}})
	result := RebaseResult{Branch: request.Branch, BaseRef: target, Output: output}
	if err == nil {
		return result, nil
	}
	evidence, evidenceErr := c.ConflictEvidence(ctx, request.WorktreePath, output)
	if evidenceErr != nil {
		return result, fmt.Errorf("rebase git branch %q onto %q: %w; inspect conflicts: %v", request.Branch, target, err, evidenceErr)
	}
	result.Conflict = &evidence
	if len(evidence.Paths) > 0 {
		return result, fmt.Errorf("%w: branch %q onto %q: %v", ErrRebaseConflict, request.Branch, target, err)
	}
	return result, fmt.Errorf("rebase git branch %q onto %q: %w", request.Branch, target, err)
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
	if strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, "\r\n\x00\t ") || strings.Contains(ref, "..") || strings.Contains(ref, "@{") || strings.ContainsAny(ref, "~^:?*[\\") || strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".") || strings.Contains(ref, "//") {
		return fmt.Errorf("git %s is invalid", label)
	}
	return nil
}

func validateCheckpoint(checkpoint RebaseCheckpoint) error {
	if checkpoint.WorktreePath == "" || !filepath.IsAbs(checkpoint.WorktreePath) {
		return errors.New("git Rebase checkpoint path must be absolute")
	}
	if err := validateRef(checkpoint.Branch, "Rebase checkpoint branch"); err != nil {
		return err
	}
	if !objectIDPattern.MatchString(checkpoint.Head) {
		return errors.New("git Rebase checkpoint HEAD is invalid")
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
