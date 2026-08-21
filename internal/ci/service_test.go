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
