// Package git owns repository integration for gg workflows.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Command describes one git command invocation.
type Command struct {
	Dir  string
	Name string
	Args []string
}

// CommandExecutor runs a command. Implementations may record commands instead
// of executing them, which keeps callers testable and supports dry-run flows.
type CommandExecutor interface {
	Execute(context.Context, Command) (string, error)
}

// ExecCommandExecutor executes commands with the standard library.
type ExecCommandExecutor struct{}

// Execute runs the command in its configured directory and returns its output.
func (ExecCommandExecutor) Execute(ctx context.Context, command Command) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	cmd := exec.CommandContext(ctx, command.Name, command.Args...)
	cmd.Dir = command.Dir
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return output.String(), fmt.Errorf("execute %s: %w", formatCommand(command), err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return output.String(), nil
}

// Client wraps git operations needed by gg.
type Client struct {
	root     string
	executor CommandExecutor
	dryRun   bool
	commands []Command
}

// ClientOption configures a Client.
type ClientOption func(*Client)

// WithDryRun records commands without executing them.
func WithDryRun() ClientOption {
	return func(client *Client) { client.dryRun = true }
}

// NewClient constructs a git client rooted at root. A nil executor uses the
// standard-library implementation.
func NewClient(root string, executor CommandExecutor, options ...ClientOption) *Client {
	if executor == nil {
		executor = ExecCommandExecutor{}
	}
	client := &Client{root: root, executor: executor}
	for _, option := range options {
		option(client)
	}
	return client
}

// Commands returns a copy of commands constructed by this client. It is useful
// for dry-run callers that need to inspect the planned operations.
func (c *Client) Commands() []Command {
	commands := make([]Command, len(c.commands))
	copy(commands, c.commands)
	for i := range commands {
		commands[i].Args = append([]string(nil), commands[i].Args...)
	}
	return commands
}

// RepositoryRoot validates that the configured directory is inside a git
// repository and returns git's repository root.
func (c *Client) RepositoryRoot(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if c == nil {
		return "", errors.New("git client is nil")
	}
	if strings.TrimSpace(c.root) == "" {
		return "", errors.New("git repository path is empty")
	}

	command := Command{Dir: c.root, Name: "git", Args: []string{"rev-parse", "--show-toplevel"}}
	if c.dryRun {
		c.commands = append(c.commands, command)
		return filepath.Abs(c.root)
	}
	output, err := c.execute(ctx, command)
	if err != nil {
		return "", fmt.Errorf("validate git repository %q: %w", c.root, err)
	}
	root := strings.TrimSpace(output)
	if root == "" {
		return "", fmt.Errorf("validate git repository %q: git returned an empty root", c.root)
	}
	return filepath.Clean(root), nil
}

// ValidateRepository checks that the configured directory is inside a git
// repository.
func (c *Client) ValidateRepository(ctx context.Context) error {
	_, err := c.RepositoryRoot(ctx)
	return err
}

// Status returns a repository status summary.
func (c *Client) Status(ctx context.Context) (string, error) {
	if c == nil || c.executor == nil {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return "unknown", nil
	}
	if err := c.ValidateRepository(ctx); err != nil {
		return "", err
	}
	command := Command{Dir: c.root, Name: "git", Args: []string{"status", "--short", "--branch"}}
	if c.dryRun {
		c.commands = append(c.commands, command)
		return "", nil
	}
	output, err := c.execute(ctx, command)
	if err != nil {
		return "", fmt.Errorf("read git status: %w", err)
	}
	return strings.TrimSpace(output), nil
}

// IsUncommittedNewFile reports whether relativePath carries uncommitted work
// in the supplied worktree: either an untracked file or a tracked file with
// uncommitted modifications. Accepting the tracked-but-modified form keeps a
// file usable even after an earlier phase accidentally committed it — the
// caller's freshness checks still guarantee the content is from the current
// attempt. The path is passed as a pathspec after -- and is restricted to a
// safe relative path.
func (c *Client) IsUncommittedNewFile(ctx context.Context, worktreePath, relativePath string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if c == nil || c.executor == nil {
		return false, errors.New("git client is nil")
	}
	worktreePath = filepath.Clean(strings.TrimSpace(worktreePath))
	if worktreePath == "." || !filepath.IsAbs(worktreePath) {
		return false, errors.New("git worktree path must be absolute")
	}
	relativePath = strings.TrimSpace(relativePath)
	if relativePath == "" || relativePath == "." || filepath.IsAbs(relativePath) || filepath.Clean(relativePath) != relativePath || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
		return false, errors.New("git file path must be a safe relative path")
	}
	command := Command{Dir: worktreePath, Name: "git", Args: []string{"status", "--porcelain=v1", "--untracked-files=all", "--ignored=matching", "-z", "--", relativePath}}
	output, err := c.execute(ctx, command)
	if err != nil {
		return false, fmt.Errorf("inspect uncommitted file %q: %w", relativePath, err)
	}
	for _, record := range strings.Split(output, "\x00") {
		if len(record) > 3 && record[3:] == relativePath {
			return true, nil
		}
	}
	return false, nil
}

// PushBranchToRemote pushes branch to the requested remote using separate argv values.
// It is the PR service narrow push operation; PushBranch is the richer remote
// adapter that returns command output for rebase/remote workflows.
func (c *Client) PushBranchToRemote(ctx context.Context, worktree, remote, branch string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil || c.executor == nil {
		return errors.New("git client is nil")
	}
	if strings.TrimSpace(worktree) == "" || strings.TrimSpace(remote) == "" || strings.TrimSpace(branch) == "" {
		return errors.New("git push requires worktree, remote, and branch")
	}
	command := Command{Dir: filepath.Clean(worktree), Name: "git", Args: []string{"push", remote, branch}}
	if _, err := c.execute(ctx, command); err != nil {
		return fmt.Errorf("push branch %q to remote %q: %w", branch, remote, err)
	}
	return nil
}

// HasUnresolvedConflicts reports whether the worktree contains unmerged paths.
func (c *Client) HasUnresolvedConflicts(ctx context.Context, worktreePath string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if c == nil || c.executor == nil {
		return false, errors.New("git client is nil")
	}
	if strings.TrimSpace(worktreePath) == "" {
		return false, errors.New("git worktree path is empty")
	}
	command := Command{Dir: filepath.Clean(worktreePath), Name: "git", Args: []string{"diff", "--name-only", "--diff-filter=U", "--"}}
	output, err := c.execute(ctx, command)
	if err != nil {
		return false, fmt.Errorf("read unresolved git paths: %w", err)
	}
	return strings.TrimSpace(output) != "", nil
}

// HeadCommit returns the commit currently checked out in worktreePath.
func (c *Client) HeadCommit(ctx context.Context, worktreePath string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if c == nil || c.executor == nil {
		return "", errors.New("git client is nil")
	}
	if strings.TrimSpace(worktreePath) == "" {
		return "", errors.New("git worktree path is empty")
	}
	command := Command{Dir: filepath.Clean(worktreePath), Name: "git", Args: []string{"rev-parse", "HEAD"}}
	output, err := c.execute(ctx, command)
	if err != nil {
		return "", fmt.Errorf("read git HEAD: %w", err)
	}
	head := strings.TrimSpace(output)
	if head == "" {
		return "", errors.New("read git HEAD: git returned an empty commit")
	}
	return head, nil
}

// AutoCommitUncommittedChanges stages and commits any uncommitted work in the
// worktree with an unsigned commit. It is the safety net for development
// subphases whose agent finished its work without committing: the work is
// preserved instead of failing the phase. A clean worktree is a no-op.
func (c *Client) AutoCommitUncommittedChanges(ctx context.Context, worktreePath, message string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil || c.executor == nil {
		return errors.New("git client is nil")
	}
	if strings.TrimSpace(worktreePath) == "" {
		return errors.New("git worktree path is empty")
	}
	if strings.TrimSpace(message) == "" {
		return errors.New("auto-commit message is empty")
	}
	dir := filepath.Clean(worktreePath)
	status, err := c.execute(ctx, Command{Dir: dir, Name: "git", Args: []string{"status", "--porcelain"}})
	if err != nil {
		return fmt.Errorf("inspect worktree status: %w", err)
	}
	if strings.TrimSpace(status) == "" {
		return nil
	}
	// PROOF.md is contractually an uncommitted QA artifact: committing it
	// would make every later QA attempt fail its uncommitted-proof check.
	if _, err := c.execute(ctx, Command{Dir: dir, Name: "git", Args: []string{"add", "-A", "--", ".", ":(exclude)PROOF.md"}}); err != nil {
		return fmt.Errorf("stage uncommitted development changes: %w", err)
	}
	staged, err := c.execute(ctx, Command{Dir: dir, Name: "git", Args: []string{"diff", "--cached", "--name-only"}})
	if err != nil {
		return fmt.Errorf("inspect staged development changes: %w", err)
	}
	if strings.TrimSpace(staged) == "" {
		// Only excluded files were dirty; nothing to preserve.
		return nil
	}
	if _, err := c.execute(ctx, Command{Dir: dir, Name: "git", Args: []string{"-c", "commit.gpgsign=false", "commit", "-m", message}}); err != nil {
		return fmt.Errorf("auto-commit uncommitted development changes: %w", err)
	}
	return nil
}

// VerifyUnsignedDevelopmentCommit requires previousHead to be an ancestor of
// HEAD and every commit added since it to have Git signature status N.
func (c *Client) VerifyUnsignedDevelopmentCommit(ctx context.Context, worktreePath, previousHead string) error {
	return c.VerifyUnsignedDevelopmentCommits(ctx, worktreePath, previousHead, true)
}

// VerifyUnsignedDevelopmentCommits verifies every commit introduced after
// previousHead is unsigned. requireCommit additionally requires HEAD to have
// advanced, which is the success contract for a Development subphase.
func (c *Client) VerifyUnsignedDevelopmentCommits(ctx context.Context, worktreePath, previousHead string, requireCommit bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(previousHead) == "" {
		return errors.New("previous git HEAD is empty")
	}
	if c == nil || c.executor == nil {
		return errors.New("git client is nil")
	}
	if strings.TrimSpace(worktreePath) == "" {
		return errors.New("git worktree path is empty")
	}
	dir := filepath.Clean(worktreePath)
	ancestor := Command{Dir: dir, Name: "git", Args: []string{"merge-base", "--is-ancestor", strings.TrimSpace(previousHead), "HEAD"}}
	if _, err := c.execute(ctx, ancestor); err != nil {
		return fmt.Errorf("previous development HEAD %s is not an ancestor of HEAD: %w", strings.TrimSpace(previousHead), err)
	}
	command := Command{Dir: dir, Name: "git", Args: []string{"log", "--format=%H%x00%G?", strings.TrimSpace(previousHead) + "..HEAD"}}
	output, err := c.execute(ctx, command)
	if err != nil {
		return fmt.Errorf("inspect development commits: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 1 && strings.TrimSpace(lines[0]) == "" {
		if requireCommit {
			return fmt.Errorf("development subphase did not create a commit (HEAD remains %s)", strings.TrimSpace(previousHead))
		}
		return nil
	}
	for _, line := range lines {
		parts := strings.Split(line, "\x00")
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return fmt.Errorf("inspect development commits: invalid git metadata %q", strings.TrimSpace(line))
		}
		commit, signature := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if signature != "N" {
			return fmt.Errorf("development commit %s is signed (git signature status %q)", commit, signature)
		}
	}
	return nil
}

func (c *Client) execute(ctx context.Context, command Command) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	output, err := c.executor.Execute(ctx, command)
	if err != nil {
		return output, err
	}
	if err := ctx.Err(); err != nil {
		return output, err
	}
	return output, nil
}

func formatCommand(command Command) string {
	return strings.Join(append([]string{command.Name}, command.Args...), " ")
}
