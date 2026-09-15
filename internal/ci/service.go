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

// Production polling budgets. The composition root passes these; the service
// itself never invents a wait, so tests can drive it with a zero interval.
const (
	// DefaultPollInterval paces observation: responsive enough that a fast
	// pipeline is not left waiting, cheap enough that a long CI run costs on
	// the order of a hundred gh invocations.
	DefaultPollInterval = 20 * time.Second
	// DefaultMaxPolls bounds total observation at roughly thirty minutes.
	DefaultMaxPolls = 90
	// DefaultMaxRegistrationPolls bounds the zero-check window at roughly two
	// minutes. See Config.MaxRegistrationPolls for why it is separate.
	DefaultMaxRegistrationPolls = 6
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
	// MaxRegistrationPolls bounds how many consecutive polls may report zero
	// checks before CI is deemed absent for this change.
	//
	// "CI has not started yet" and "this repository runs no CI for this
	// change" are the same observation — an empty check set — so only elapsed
	// time separates them. Checks register within seconds of a push, which is
	// why this window is deliberately far shorter than MaxPolls: it decides
	// the absent case quickly instead of making a project with no CI wait out
	// the whole completion timeout. Defaults to MaxPolls when unset.
	MaxRegistrationPolls int
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
	if cfg.MaxRegistrationPolls <= 0 || cfg.MaxRegistrationPolls > cfg.MaxPolls {
		cfg.MaxRegistrationPolls = cfg.MaxPolls
	}
	if cfg.PollInterval < 0 {
		return Result{}, errors.New("ci poll interval cannot be negative")
	}
	emptyPolls := 0
	for poll := 1; poll <= cfg.MaxPolls; poll++ {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		checks, obsErr := s.observe(ctx, id)
		if obsErr != nil {
			return s.finish(ctx, cfg, Result{Outcome: OutcomeBlocked, Identity: id, Polls: poll}, obsErr.Error())
		}
		observed := classify(checks)
		if observed == verdictNoChecks {
			// Keep waiting for checks to register. Only an exhausted
			// registration window may conclude that no CI exists, because
			// until then this is indistinguishable from a poll that raced
			// ahead of GitHub registering the workflow runs.
			if emptyPolls++; emptyPolls >= cfg.MaxRegistrationPolls {
				return s.finish(ctx, cfg, Result{Outcome: OutcomePassed, Identity: id, Polls: poll}, "")
			}
			if err := wait(ctx, cfg.PollInterval); err != nil {
				return Result{}, err
			}
			continue
		}
		outcome, terminal := dispose(observed)
		if terminal || poll == cfg.MaxPolls {
			feedback := ""
			if outcome != OutcomePassed {
				feedback = feedbackFor(outcome, observed, checks)
			}
			return s.finish(ctx, cfg, Result{Outcome: outcome, Identity: id, Checks: checks, Polls: poll}, feedback)
		}
		if err := wait(ctx, cfg.PollInterval); err != nil {
			return Result{}, err
		}
	}
	return Result{}, errors.New("ci polling ended unexpectedly")
}

// observe reads the pull request's checks. GitHub reports "no checks reported"
// as a gh exit failure rather than as an empty list, so that one message is
// normalized to an empty check set: it means CI has not registered yet, not
// that the provider is unreachable. Every other gh failure stays an error so a
// bad token or a missing pull request is never mistaken for a quiet pipeline.
func (s *Service) observe(ctx context.Context, id string) ([]Check, error) {
	out, err := s.gh.Execute(ctx, []string{"pr", "checks", id, "--json", "name,state,bucket,link"})
	if err != nil {
		if isNoChecksReported(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("GitHub checks could not be read: %w", err)
	}
	checks, parseErr := parseChecks(out)
	if parseErr != nil {
		return nil, fmt.Errorf("GitHub returned malformed check data: %w", parseErr)
	}
	return checks, nil
}

func isNoChecksReported(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no checks reported")
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

// verdict is the provider-neutral reading of a check set. It carries no policy:
// how long an empty or pending set may be tolerated belongs to Monitor, which
// owns the polling budgets, so this stays a pure function over one observation.
type verdict int

const (
	verdictNoChecks verdict = iota
	verdictPending
	verdictPassed
	verdictFailed
	// verdictInfra covers checks that ended without judging the change —
	// cancelled runs and dead runners. No code edit can turn these green.
	verdictInfra
	verdictUnknown
)

func classify(checks []Check) verdict {
	if len(checks) == 0 {
		return verdictNoChecks
	}
	pending, infra := false, false
	for _, c := range checks {
		switch strings.ToLower(strings.TrimSpace(c.Bucket)) {
		case "fail", "error":
			return verdictFailed
		case "cancel":
			infra = true
		case "pending":
			pending = true
		// A skipped or neutral check is terminal. GitHub never promotes it to
		// "pass" — an aggregating check run such as CodeQL reports neutral
		// while the Analyze jobs beneath it succeed — so treating it as
		// pending waits forever for a transition that cannot happen.
		case "pass", "skipping":
		default:
			return verdictUnknown
		}
	}
	switch {
	case pending:
		return verdictPending
	case infra:
		return verdictInfra
	}
	return verdictPassed
}

// dispose maps an observation to a disposition and reports whether it is
// terminal. Only a pending set is worth re-observing.
func dispose(observed verdict) (Outcome, bool) {
	switch observed {
	case verdictPassed:
		return OutcomePassed, true
	case verdictFailed:
		return OutcomeFailed, true
	case verdictPending:
		return OutcomeBlocked, false
	default:
		return OutcomeBlocked, true
	}
}

// blocking reports whether a check belongs in feedback as something to resolve.
// Passed and skipped checks are both terminal successes.
func blocking(bucket string) bool {
	switch strings.ToLower(strings.TrimSpace(bucket)) {
	case "pass", "skipping":
		return false
	}
	return true
}

func feedbackFor(outcome Outcome, observed verdict, checks []Check) string {
	var b strings.Builder
	b.WriteString("# CI Feedback\n\nThe required pull-request checks did not pass. Resolve the checks below and retry CI.\n\n")
	for _, c := range checks {
		if !blocking(c.Bucket) {
			continue
		}
		fmt.Fprintf(&b, "- **%s**: %s", c.Name, c.Bucket)
		if c.Link != "" {
			fmt.Fprintf(&b, " ([details](%s))", c.Link)
		}
		b.WriteByte('\n')
	}
	switch observed {
	case verdictInfra:
		b.WriteString("\nThese checks were cancelled rather than failing on the change: this is CI infrastructure state, not a code defect. Re-run CI instead of editing code.\n")
	case verdictUnknown:
		b.WriteString("\nAt least one check reported an unrecognized state, so CI evidence is incomplete. Inspect the checks above before claiming readiness.\n")
	default:
		if outcome == OutcomeBlocked {
			b.WriteString("\nCI evidence is blocked or incomplete; do not claim readiness.\n")
		}
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
