// Package gh owns the GitHub CLI command adapter used by gg.
package gh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
)

type Command struct {
	Dir  string
	Name string
	Args []string
}
type CommandExecutor interface {
	Execute(context.Context, Command) (string, error)
}
type ExecCommandExecutor struct {
	// Env entries are appended to the parent environment — for example
	// GH_TOKEN so gh authenticates as the repository's configured credential
	// instead of its interactively logged-in account.
	Env []string
}

func (e ExecCommandExecutor) Execute(ctx context.Context, command Command) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, command.Name, command.Args...)
	cmd.Dir = command.Dir
	if len(e.Env) > 0 {
		cmd.Env = append(os.Environ(), e.Env...)
	}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		// Surface gh's own message ("unknown flag", auth errors) instead of
		// a bare exit status.
		if detail := strings.TrimSpace(output.String()); detail != "" {
			return output.String(), fmt.Errorf("execute gh command: %w: %s", err, detail)
		}
		return "", fmt.Errorf("execute gh command: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return output.String(), nil
}

// TokenFromRemoteURL extracts a credential embedded in an http(s) git remote
// URL (https://TOKEN@host/… or https://user:TOKEN@host/…). gg passes it to gh
// as GH_TOKEN so GitHub operations act as the same identity git uses to push
// to that remote. It returns "" when the URL carries no credential; the
// caller must never log the result.
func TokenFromRemoteURL(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.User == nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return ""
	}
	if password, ok := parsed.User.Password(); ok {
		return password
	}
	return parsed.User.Username()
}

type Client struct{ executor CommandExecutor }

func NewClient(executor CommandExecutor) *Client {
	if executor == nil {
		executor = ExecCommandExecutor{}
	}
	return &Client{executor: executor}
}

// CreatePullRequest passes every value as an individual argv value; no shell
// is involved. `gh pr create` prints the new PR's URL on stdout (it has no
// --json flag); when a PR for the branch already exists the existing URL is
// looked up instead, so re-running the PR phase is idempotent.
func (c *Client) CreatePullRequest(ctx context.Context, worktree, title, body, base, head string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if c == nil || c.executor == nil {
		return "", errors.New("gh client is nil")
	}
	if strings.TrimSpace(worktree) == "" || strings.TrimSpace(title) == "" || strings.TrimSpace(body) == "" || strings.TrimSpace(base) == "" || strings.TrimSpace(head) == "" {
		return "", errors.New("gh pull request requires worktree, title, body, base, and head")
	}
	command := Command{Dir: worktree, Name: "gh", Args: []string{"pr", "create", "--title", title, "--body", body, "--base", base, "--head", head}}
	output, err := c.executor.Execute(ctx, command)
	if err != nil {
		if strings.Contains(output, "already exists") {
			if url, viewErr := c.pullRequestURL(ctx, worktree, head); viewErr == nil {
				return url, nil
			}
		}
		return "", fmt.Errorf("create pull request: %w", err)
	}
	if url := lastURLLine(output); url != "" {
		return url, nil
	}
	return "", errors.New("parse gh pull request output: missing url")
}

// pullRequestURL resolves the URL of the branch's existing pull request.
func (c *Client) pullRequestURL(ctx context.Context, worktree, head string) (string, error) {
	output, err := c.executor.Execute(ctx, Command{Dir: worktree, Name: "gh", Args: []string{"pr", "view", head, "--json", "url"}})
	if err != nil {
		return "", fmt.Errorf("view pull request: %w", err)
	}
	var response struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &response); err != nil {
		return "", fmt.Errorf("parse gh pull request view output: %w", err)
	}
	if strings.TrimSpace(response.URL) == "" {
		return "", errors.New("parse gh pull request view output: missing url")
	}
	return strings.TrimSpace(response.URL), nil
}

// lastURLLine returns the last line of output that looks like an https URL —
// `gh pr create` prints the created PR's URL as its final stdout line.
func lastURLLine(output string) string {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "https://") {
			return line
		}
	}
	return ""
}
