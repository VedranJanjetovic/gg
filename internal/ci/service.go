// Package ci monitors GitHub pull-request checks and records durable evidence.
package ci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/VedranJanjetovic/gg/internal/robustio"
)

const (
	ReportName   = "ci-report.md"
	FeedbackName = "ci-feedback.md"
)

type Outcome string

const (
	OutcomePassed  Outcome = "passed"
	OutcomeFailed  Outcome = "failed"
	OutcomeBlocked Outcome = "blocked"
)

type Executor interface {
	Execute(context.Context, []string) (string, error)
}
type ExecExecutor struct {
	// Env entries are appended to the parent environment — for example
	// GH_TOKEN so gh authenticates as the repository's configured credential.
	Env []string
}

func (e ExecExecutor) Execute(ctx context.Context, args []string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(args) == 0 {
		return "", errors.New("gh command is empty")
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	if len(e.Env) > 0 {
		cmd.Env = append(os.Environ(), e.Env...)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

type Config struct {
	Enabled      bool
	Identity     string
	Worktree     string
	ArtifactRoot string
	ProjectSlug  string
	RunID        string
	PollInterval time.Duration
	MaxPolls     int
}
type Service struct{ gh Executor }

// Monitor is the consumer-owned boundary for bounded CI observation. The
// concrete Service writes evidence; callers need only the monitoring contract.
type Monitor interface {
	Monitor(context.Context, Config) (Result, error)
}

func NewService(gh Executor) *Service {
	if gh == nil {
		gh = ExecExecutor{}
	}
	return &Service{gh: gh}
}

type Check struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Bucket string `json:"bucket"`
	Link   string `json:"link"`
}

// CheckObservation is the provider-neutral check result consumed by the PR lifecycle monitor.
type CheckObservation struct {
	Cursor  string
	Failed  []string
	Pending bool
}

// CheckProvider exposes only check state; review comments are deliberately out of scope.
type CheckProvider interface {
	Observe(context.Context, string, string) (CheckObservation, error)
}

type Result struct {
	Outcome      Outcome
	Identity     string
	Checks       []Check
	Polls        int
	ReportPath   string
	FeedbackPath string
}

func (s *Service) Monitor(ctx context.Context, cfg Config) (Result, error) {
	if s == nil || s.gh == nil {
		return Result{}, errors.New("ci service requires a gh executor")
	}
	if !cfg.Enabled {
		return Result{Outcome: OutcomeBlocked, Identity: strings.TrimSpace(cfg.Identity)}, nil
	}
	id, err := normalizeIdentity(cfg.Identity)
	if err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(cfg.Worktree) == "" {
		return Result{}, errors.New("ci worktree is required")
	}
	if cfg.MaxPolls <= 0 {
		cfg.MaxPolls = 1
	}
	if cfg.PollInterval < 0 {
		return Result{}, errors.New("ci poll interval cannot be negative")
	}
	for poll := 1; poll <= cfg.MaxPolls; poll++ {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		out, runErr := s.gh.Execute(ctx, []string{"pr", "checks", id, "--json", "name,state,bucket,link"})
		if runErr != nil {
			return s.finish(ctx, cfg, Result{Outcome: OutcomeBlocked, Identity: id, Polls: poll}, fmt.Sprintf("GitHub checks could not be read: %v", runErr))
		}
		checks, parseErr := parseChecks(out)
		if parseErr != nil {
			return s.finish(ctx, cfg, Result{Outcome: OutcomeBlocked, Identity: id, Polls: poll}, "GitHub returned malformed check data: "+parseErr.Error())
		}
		outcome, terminal := classify(checks)
		if terminal || poll == cfg.MaxPolls {
			feedback := ""
			if outcome != OutcomePassed {
				feedback = feedbackFor(outcome, checks)
			}
			return s.finish(ctx, cfg, Result{Outcome: outcome, Identity: id, Checks: checks, Polls: poll}, feedback)
		}
		if err := wait(ctx, cfg.PollInterval); err != nil {
			return Result{}, err
		}
	}
	return Result{}, errors.New("ci polling ended unexpectedly")
}
func normalizeIdentity(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", errors.New("pull request identity is required")
	}
	if strings.ContainsAny(v, "\r\n\t") || strings.HasPrefix(v, "-") {
		return "", errors.New("invalid pull request identity")
	}
	if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
		if !strings.Contains(v, "github.com/") || !strings.Contains(v, "/pull/") {
			return "", errors.New("pull request URL must be a GitHub pull request URL")
		}
		return v, nil
	}
	if strings.ContainsAny(v, " ;|&$`\\") {
		return "", errors.New("invalid pull request identity")
	}
	return v, nil
}
func parseChecks(data string) ([]Check, error) {
	var checks []Check
	if err := json.Unmarshal([]byte(data), &checks); err != nil {
		return nil, err
	}
	for i := range checks {
		checks[i].Name = strings.TrimSpace(checks[i].Name)
		if checks[i].Name == "" {
			return nil, errors.New("check name is empty")
		}
	}
	sort.Slice(checks, func(i, j int) bool { return checks[i].Name < checks[j].Name })
	return checks, nil
}
func classify(checks []Check) (Outcome, bool) {
	if len(checks) == 0 {
		return OutcomeBlocked, true
	}
	pending := false
	for _, c := range checks {
		switch strings.ToLower(strings.TrimSpace(c.Bucket)) {
		case "fail", "error", "cancel":
			return OutcomeFailed, true
		case "pending", "skipping":
			pending = true
		case "pass":
		default:
			return OutcomeBlocked, true
		}
	}
	if pending {
		return OutcomeBlocked, false
	}
	return OutcomePassed, true
}
func feedbackFor(outcome Outcome, checks []Check) string {
	var b strings.Builder
	b.WriteString("# CI Feedback\n\nThe required pull-request checks did not pass. Resolve the checks below and retry CI.\n\n")
	for _, c := range checks {
		if strings.ToLower(c.Bucket) != "pass" {
			fmt.Fprintf(&b, "- **%s**: %s", c.Name, c.Bucket)
			if c.Link != "" {
				fmt.Fprintf(&b, " ([details](%s))", c.Link)
			}
			b.WriteByte('\n')
		}
	}
	if outcome == OutcomeBlocked {
		b.WriteString("\nCI evidence is blocked or incomplete; do not claim readiness.\n")
	}
	return b.String()
}
func (s *Service) finish(ctx context.Context, cfg Config, r Result, feedback string) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(cfg.Worktree, 0755); err != nil {
		return Result{}, fmt.Errorf("create CI worktree: %w", err)
	}
	if err := atomicWrite(filepath.Join(cfg.Worktree, ReportName), []byte(renderReport(r, feedback))); err != nil {
		return Result{}, fmt.Errorf("write CI report: %w", err)
	}
	r.ReportPath = filepath.Join(cfg.Worktree, ReportName)
	if feedback != "" {
		root := cfg.ArtifactRoot
		if strings.TrimSpace(root) == "" {
			root = cfg.Worktree
		}
		dir := filepath.Join(root, ".gg", "projects", cfg.ProjectSlug, "artifacts")
		if strings.TrimSpace(cfg.ProjectSlug) == "" {
			dir = filepath.Join(root, ".gg", "artifacts")
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			return Result{}, fmt.Errorf("create CI feedback directory: %w", err)
		}
		r.FeedbackPath = filepath.Join(dir, FeedbackName)
		if err := atomicWrite(r.FeedbackPath, []byte(feedback)); err != nil {
			return Result{}, fmt.Errorf("write CI feedback: %w", err)
		}
	}
	return r, nil
}
func renderReport(r Result, feedback string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# CI Report\n\n- Pull request: `%s`\n- Disposition: **%s**\n- Polls: %d\n\n## Checks\n\n", r.Identity, r.Outcome, r.Polls)
	if len(r.Checks) == 0 {
		b.WriteString("- No check evidence returned.\n")
	} else {
		for _, c := range r.Checks {
			fmt.Fprintf(&b, "- `%s`: **%s**", c.Name, c.Bucket)
			if c.Link != "" {
				fmt.Fprintf(&b, " ([details](%s))", c.Link)
			}
			b.WriteByte('\n')
		}
	}
	if feedback != "" {
		b.WriteString("\n## Feedback\n\n")
		b.WriteString(strings.TrimSpace(feedback))
		b.WriteByte('\n')
	}
	return b.String()
}
func atomicWrite(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ci-artifact-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return robustio.Rename(name, path)
}
func wait(ctx context.Context, d time.Duration) error {
	if d == 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
