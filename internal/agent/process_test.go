package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type capturedEvents struct {
	mu     sync.Mutex
	events []Event
}

func (s *capturedEvents) Publish(_ context.Context, event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	event.Payload = append([]byte(nil), event.Payload...)
	s.events = append(s.events, event)
	return nil
}

type capturedLogs struct {
	mu     sync.Mutex
	chunks map[string][]byte
}

func (s *capturedLogs) Write(_ context.Context, stream string, payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.chunks == nil {
		s.chunks = make(map[string][]byte)
	}
	s.chunks[stream] = append(s.chunks[stream], payload...)
	return nil
}

func (s *capturedLogs) contains(stream, want string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Contains(string(s.chunks[stream]), want)
}

func (s *capturedLogs) value(stream string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.chunks[stream])
}

func TestExecProcessFactoryCapturesWorktreeEnvironmentAndBothStreams(t *testing.T) {
	worktree := t.TempDir()
	events := &capturedEvents{}
	logs := &capturedLogs{}
	factory := NewExecProcessFactory(events, logs)
	process, err := factory.Start(context.Background(), ProcessSpec{
		Command:          os.Args[0],
		Args:             []string{"-test.run=TestFakeAgentProcess", "--", "streams"},
		WorkingDirectory: worktree,
		Env:              []string{"GO_WANT_FAKE_AGENT_PROCESS=1", "FAKE_AGENT_VALUE=override"},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	result, err := process.Wait()
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if result.ExitCode != 0 || result.Duration < 0 || result.FinishedAt.Before(result.StartedAt) {
		t.Fatalf("unexpected process result: %+v", result)
	}
	stdout := string(logs.chunks["stdout"])
	cwdAndEnvironment, found := strings.CutPrefix(strings.TrimSpace(stdout), "cwd=")
	if !found {
		t.Fatalf("stdout did not contain cwd: %q", stdout)
	}
	processCWD, _, found := strings.Cut(cwdAndEnvironment, " env=")
	if !found {
		t.Fatalf("stdout did not separate cwd and environment: %q", stdout)
	}
	gotCWD := canonicalTestPath(t, processCWD)
	wantCWD := canonicalTestPath(t, worktree)
	gotInfo, gotErr := os.Stat(gotCWD)
	wantInfo, wantErr := os.Stat(wantCWD)
	if gotErr != nil || wantErr != nil || !os.SameFile(gotInfo, wantInfo) {
		t.Fatalf("process cwd = %q, want %q (stat errors: %v, %v)", gotCWD, wantCWD, gotErr, wantErr)
	}
	if got := logs.chunks["stdout"]; !strings.Contains(string(got), "env=override") || !strings.Contains(string(got), "inherited=") {
		t.Fatalf("stdout did not contain merged environment: %q", got)
	}
	if got := string(logs.chunks["stderr"]); got != "stderr-line\n" {
		t.Fatalf("stderr log = %q", got)
	}
	events.mu.Lock()
	defer events.mu.Unlock()
	var streams []string
	for _, event := range events.events {
		if event.Type != EventOutput || len(event.Payload) == 0 {
			t.Fatalf("unexpected event: %+v", event)
		}
		streams = append(streams, event.Stream)
	}
	if !contains(streams, "stdout") || !contains(streams, "stderr") {
		t.Fatalf("captured event streams = %v", streams)
	}
}

func canonicalTestPath(t *testing.T, path string) string {
	t.Helper()
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("make path %q absolute: %v", path, err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		t.Fatalf("canonicalize path %q: %v", absolute, err)
	}
	return filepath.Clean(resolved)
}

func TestExecProcessFactoryDrainsLargeConcurrentOutput(t *testing.T) {
	logs := &capturedLogs{}
	process, err := NewExecProcessFactory(nil, logs).Start(context.Background(), ProcessSpec{
		Command:          os.Args[0],
		Args:             []string{"-test.run=TestFakeAgentProcess", "--", "large"},
		WorkingDirectory: t.TempDir(),
		Env:              []string{"GO_WANT_FAKE_AGENT_PROCESS=1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := process.Wait()
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("Wait() = %+v, %v", result, err)
	}
	if len(logs.chunks["stdout"]) < 128*1024 || len(logs.chunks["stderr"]) < 128*1024 {
		t.Fatalf("output lengths = %d/%d", len(logs.chunks["stdout"]), len(logs.chunks["stderr"]))
	}
}

func TestExecProcessFactoryCancellationTerminatesProcessGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	process, err := NewExecProcessFactory(nil, nil).Start(ctx, ProcessSpec{
		Command:          os.Args[0],
		Args:             []string{"-test.run=TestFakeAgentProcess", "--", "block"},
		WorkingDirectory: t.TempDir(),
		Env:              []string{"GO_WANT_FAKE_AGENT_PROCESS=1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitDone := make(chan error, 1)
	go func() {
		_, waitErr := process.Wait()
		waitDone <- waitErr
	}()
	cancel()
	select {
	case <-waitDone:
	case <-time.After(3 * time.Second):
		t.Fatal("canceled process did not exit")
	}
}

func TestExecProcessFactoryRejectsCancellationBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := NewExecProcessFactory(nil, nil).Start(ctx, ProcessSpec{
		Command:          os.Args[0],
		Args:             []string{"-test.run=TestFakeAgentProcess", "--", "block"},
		WorkingDirectory: t.TempDir(),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() error = %v, want context.Canceled", err)
	}
}

func TestExecProcessFactoryReportsStartAndExitFailures(t *testing.T) {
	factory := NewExecProcessFactory(nil, nil)
	if _, err := factory.Start(context.Background(), ProcessSpec{Command: os.Args[0], WorkingDirectory: ""}); err == nil {
		t.Fatal("empty worktree unexpectedly accepted")
	}
	if _, err := factory.Start(context.Background(), ProcessSpec{Command: os.Args[0], WorkingDirectory: t.TempDir(), Env: []string{"BROKEN_OVERRIDE"}}); err == nil {
		t.Fatal("malformed environment override unexpectedly accepted")
	}
	process, err := factory.Start(context.Background(), ProcessSpec{
		Command: os.Args[0], Args: []string{"-test.run=TestFakeAgentProcess", "--", "failure"},
		WorkingDirectory: t.TempDir(), Env: []string{"GO_WANT_FAKE_AGENT_PROCESS=1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, waitErr := process.Wait()
	if waitErr == nil || result.ExitCode != 7 {
		t.Fatalf("failure result = %+v, error = %v", result, waitErr)
	}
}

func TestFakeAgentProcess(t *testing.T) {
	if os.Getenv("GO_WANT_FAKE_AGENT_PROCESS") != "1" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	switch mode {
	case "streams":
		cwd, _ := os.Getwd()
		_, _ = os.Stdout.WriteString("cwd=" + cwd + " env=" + os.Getenv("FAKE_AGENT_VALUE") + " inherited=" + os.Getenv("PATH") + "\n")
		_, _ = os.Stderr.WriteString("stderr-line\n")
	case "large":
		_, _ = os.Stdout.Write(make([]byte, 128*1024))
		_, _ = os.Stderr.Write(make([]byte, 128*1024))
	case "block":
		for {
			time.Sleep(time.Second)
		}
	case "failure":
		os.Exit(7)
	default:
		runPlatformFakeAgent(t, mode)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
