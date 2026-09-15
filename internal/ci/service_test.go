package ci

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeGH struct {
	outputs []string
	err     error
	calls   [][]string
	called  chan struct{}
}

func (f *fakeGH) Execute(_ context.Context, args []string) (string, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	if f.called != nil {
		select {
		case f.called <- struct{}{}:
		default:
		}
	}
	if f.err != nil {
		return "", f.err
	}
	i := len(f.calls) - 1
	if i >= len(f.outputs) {
		i = len(f.outputs) - 1
	}
	return f.outputs[i], nil
}
func cfg(dir string) Config {
	return Config{Enabled: true, Identity: "owner/repo#12", Worktree: dir, ArtifactRoot: dir, ProjectSlug: "demo", PollInterval: 0, MaxPolls: 2}
}
func TestMonitorSuccessExactArgvAndReport(t *testing.T) {
	f := &fakeGH{outputs: []string{`[{"name":"build","state":"SUCCESS","bucket":"pass","link":"https://example/build"}]`}}
	r, err := NewService(f).Monitor(context.Background(), cfg(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if r.Outcome != OutcomePassed || r.Polls != 1 {
		t.Fatalf("result=%+v", r)
	}
	want := []string{"pr", "checks", "owner/repo#12", "--json", "name,state,bucket,link"}
	if !reflect.DeepEqual(f.calls[0], want) {
		t.Fatalf("argv=%v want %v", f.calls[0], want)
	}
	data, _ := os.ReadFile(r.ReportPath)
	if !strings.Contains(string(data), "Disposition: **passed**") {
		t.Fatal("report missing passed disposition")
	}
}
func TestMonitorFailureWritesFeedback(t *testing.T) {
	d := t.TempDir()
	f := &fakeGH{outputs: []string{`[{"name":"unit","state":"FAILURE","bucket":"fail","link":"https://example/unit"}]`}}
	r, err := NewService(f).Monitor(context.Background(), cfg(d))
	if err != nil {
		t.Fatal(err)
	}
	if r.Outcome != OutcomeFailed || r.FeedbackPath == "" {
		t.Fatalf("result=%+v", r)
	}
	data, _ := os.ReadFile(r.FeedbackPath)
	if !strings.Contains(string(data), "unit") || !strings.Contains(string(data), "CI Feedback") {
		t.Fatalf("feedback=%s", data)
	}
}
func TestMonitorBlockedPollsUntilBounded(t *testing.T) {
	f := &fakeGH{outputs: []string{`[{"name":"build","bucket":"pending"}]`, `[{"name":"build","bucket":"pending"}]`}}
	c := cfg(t.TempDir())
	c.MaxPolls = 2
	r, err := NewService(f).Monitor(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if r.Outcome != OutcomeBlocked || r.Polls != 2 || len(f.calls) != 2 {
		t.Fatalf("result=%+v calls=%d", r, len(f.calls))
	}
}
func TestMonitorMalformedAndAuthAreBlocked(t *testing.T) {
	for name, f := range map[string]*fakeGH{"malformed": {outputs: []string{"not json"}}, "auth": {err: errors.New("authentication required")}} {
		t.Run(name, func(t *testing.T) {
			r, err := NewService(f).Monitor(context.Background(), cfg(t.TempDir()))
			if err != nil {
				t.Fatal(err)
			}
			if r.Outcome != OutcomeBlocked {
				t.Fatalf("result=%+v", r)
			}
			if r.ReportPath == "" {
				t.Fatal("missing report")
			}
		})
	}
}
func TestMonitorDisabledDoesNotInvokeGH(t *testing.T) {
	f := &fakeGH{}
	c := cfg(t.TempDir())
	c.Enabled = false
	r, err := NewService(f).Monitor(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if r.Outcome != OutcomeBlocked || len(f.calls) != 0 {
		t.Fatalf("result=%+v calls=%d", r, len(f.calls))
	}
}
func TestMonitorCancellationDuringPollWait(t *testing.T) {
	f := &fakeGH{outputs: []string{`[{"name":"build","bucket":"pending"}]`}, called: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	c := cfg(t.TempDir())
	c.PollInterval = time.Hour
	c.MaxPolls = 3
	done := make(chan error, 1)
	go func() { _, err := NewService(f).Monitor(ctx, c); done <- err }()
	select {
	case <-f.called:
	case <-time.After(time.Second):
		t.Fatal("fake gh was not called")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation not observed")
	}
}
func TestClassifyBuckets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		checks []Check
		want   verdict
	}{
		{"empty", nil, verdictNoChecks},
		{"all pass", []Check{{Name: "a", Bucket: "pass"}}, verdictPassed},
		// An aggregating check run such as CodeQL reports neutral while the
		// Analyze jobs beneath it pass; it must not hold the gate open.
		{"skipping is terminal", []Check{{Name: "a", Bucket: "pass"}, {Name: "codeql", Bucket: "skipping"}}, verdictPassed},
		{"only skipping", []Check{{Name: "codeql", Bucket: "skipping"}}, verdictPassed},
		{"pending", []Check{{Name: "a", Bucket: "pass"}, {Name: "b", Bucket: "pending"}}, verdictPending},
		{"fail", []Check{{Name: "a", Bucket: "fail"}}, verdictFailed},
		{"error", []Check{{Name: "a", Bucket: "error"}}, verdictFailed},
		{"fail outranks pending", []Check{{Name: "a", Bucket: "pending"}, {Name: "b", Bucket: "fail"}}, verdictFailed},
		{"cancel is infra", []Check{{Name: "a", Bucket: "pass"}, {Name: "b", Bucket: "cancel"}}, verdictInfra},
		{"pending outranks cancel", []Check{{Name: "a", Bucket: "cancel"}, {Name: "b", Bucket: "pending"}}, verdictPending},
		{"unknown bucket", []Check{{Name: "a", Bucket: "wat"}}, verdictUnknown},
		{"case and space insensitive", []Check{{Name: "a", Bucket: " PASS "}}, verdictPassed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(tc.checks); got != tc.want {
				t.Fatalf("classify=%v want %v", got, tc.want)
			}
		})
	}
}

// A skipped check is a terminal success, so it must not be listed as work to do.
func TestFeedbackOmitsSkippedChecks(t *testing.T) {
	checks := []Check{{Name: "codeql", Bucket: "skipping"}, {Name: "sonar", Bucket: "fail"}}
	got := feedbackFor(OutcomeFailed, verdictFailed, checks)
	if strings.Contains(got, "codeql") {
		t.Fatalf("skipped check listed as blocking:\n%s", got)
	}
	if !strings.Contains(got, "sonar") {
		t.Fatalf("failing check missing:\n%s", got)
	}
}

// All-pass-or-skipped is a pass: this is the whole-Monitor version of the
// classify case, proving the disposition reaches the report.
func TestMonitorPassesWithSkippedChecks(t *testing.T) {
	f := &fakeGH{outputs: []string{`[{"name":"build","bucket":"pass"},{"name":"CodeQL","state":"NEUTRAL","bucket":"skipping"}]`}}
	r, err := NewService(f).Monitor(context.Background(), cfg(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if r.Outcome != OutcomePassed || r.Polls != 1 || r.FeedbackPath != "" {
		t.Fatalf("result=%+v", r)
	}
}

// Zero checks means "not registered yet", which must be re-observed rather
// than failed. This is the race the CI phase hit when it polled 33ms after
// opening the pull request.
func TestMonitorRetriesWhileNoChecksReported(t *testing.T) {
	for name, outputs := range map[string][]string{
		"empty list": {`[]`, `[]`, `[{"name":"build","bucket":"pass"}]`},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeGH{outputs: outputs}
			c := cfg(t.TempDir())
			c.MaxPolls, c.MaxRegistrationPolls = 5, 4
			r, err := NewService(f).Monitor(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			if r.Outcome != OutcomePassed || r.Polls != 3 {
				t.Fatalf("result=%+v calls=%d", r, len(f.calls))
			}
		})
	}
}

// gh exits non-zero with this message instead of returning an empty list, so
// the message must be normalized to "no checks yet" rather than "unreadable".
func TestMonitorTreatsNoChecksReportedErrorAsPending(t *testing.T) {
	f := &fakeGH{err: errors.New("gh pr checks x: exit status 1: no checks reported on the 'topic' branch")}
	c := cfg(t.TempDir())
	c.MaxPolls, c.MaxRegistrationPolls = 3, 3
	r, err := NewService(f).Monitor(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	// Window exhausted with nothing ever reported: this change runs no CI.
	if r.Outcome != OutcomePassed || len(f.calls) != 3 {
		t.Fatalf("result=%+v calls=%d", r, len(f.calls))
	}
}

// The registration window is shorter than MaxPolls so a project with no CI is
// decided quickly instead of waiting out the whole completion timeout.
func TestMonitorRegistrationWindowIsShorterThanPollBudget(t *testing.T) {
	f := &fakeGH{outputs: []string{`[]`}}
	c := cfg(t.TempDir())
	c.MaxPolls, c.MaxRegistrationPolls = 50, 2
	r, err := NewService(f).Monitor(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if r.Outcome != OutcomePassed || len(f.calls) != 2 {
		t.Fatalf("result=%+v calls=%d", r, len(f.calls))
	}
}

// A cancelled check ended without judging the change, so it is blocked
// evidence to escalate — not feedback to loop a Development agent over.
func TestMonitorCancelledChecksAreBlockedNotFailed(t *testing.T) {
	f := &fakeGH{outputs: []string{`[{"name":"build","bucket":"cancel"}]`}}
	r, err := NewService(f).Monitor(context.Background(), cfg(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if r.Outcome != OutcomeBlocked {
		t.Fatalf("result=%+v", r)
	}
	data, _ := os.ReadFile(r.FeedbackPath)
	if !strings.Contains(string(data), "infrastructure") {
		t.Fatalf("feedback does not name infrastructure:\n%s", data)
	}
}

// A real gh failure must stay blocked, never be mistaken for a quiet pipeline.
func TestMonitorRealGHFailureStaysBlocked(t *testing.T) {
	f := &fakeGH{err: errors.New("gh pr checks x: exit status 1: could not resolve to a PullRequest")}
	c := cfg(t.TempDir())
	c.MaxPolls, c.MaxRegistrationPolls = 3, 3
	r, err := NewService(f).Monitor(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if r.Outcome != OutcomeBlocked || len(f.calls) != 1 {
		t.Fatalf("result=%+v calls=%d", r, len(f.calls))
	}
}

func TestNormalizeIdentityRejectsUnsafeValues(t *testing.T) {
	for _, v := range []string{"", "-1", "https://example.invalid/pull/1", "x;y"} {
		if _, err := normalizeIdentity(v); err == nil {
			t.Errorf("accepted %q", v)
		}
	}
	if _, err := normalizeIdentity("https://github.com/a/b/pull/1"); err != nil {
		t.Fatal(err)
	}
}
func TestArtifactsAreDurableUnderProject(t *testing.T) {
	d := t.TempDir()
	f := &fakeGH{outputs: []string{`[{"name":"x","bucket":"fail"}]`}}
	c := cfg(d)
	c.ProjectSlug = "safe-project"
	r, err := NewService(f).Monitor(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(d, ".gg", "projects", "safe-project", "artifacts", FeedbackName)
	if r.FeedbackPath != want {
		t.Fatalf("path=%s want %s", r.FeedbackPath, want)
	}
}
