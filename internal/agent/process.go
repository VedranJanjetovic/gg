package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// RawLogWriter receives each raw stdout/stderr chunk. Implementations must not
// retain payload after Write returns unless they copy it.
type RawLogWriter interface {
	Write(context.Context, string, []byte) error
}

// ExecProcessFactory starts local child processes. Output is always drained
// concurrently; optional sinks receive the same chunks as they are read.
type ExecProcessFactory struct {
	events EventSink
	logs   RawLogWriter
	now    func() time.Time
}

var _ ProcessFactory = (*ExecProcessFactory)(nil)

// NewExecProcessFactory returns a process factory. A nil sink or writer is
// allowed. Env on ProcessSpec is an explicit set of KEY=VALUE overrides over
// the inherited environment, not a replacement environment.
func NewExecProcessFactory(events EventSink, logs RawLogWriter) *ExecProcessFactory {
	return &ExecProcessFactory{events: events, logs: logs, now: time.Now}
}

// Start validates the worktree and starts the requested executable directly,
// without shell interpolation. The returned Process owns both output-draining
// goroutines and must be waited on even after Cancel.
func (f *ExecProcessFactory) Start(ctx context.Context, spec ProcessSpec) (Process, error) {
	if ctx == nil {
		return nil, errors.New("process context is nil")
	}
	if strings.TrimSpace(spec.Command) == "" {
		return nil, errors.New("process command is required")
	}
	worktree, err := cleanExistingDirectory(spec.WorkingDirectory)
	if err != nil {
		return nil, fmt.Errorf("validate process worktree: %w", err)
	}
	env, err := mergedEnvironment(spec.Env)
	if err != nil {
		return nil, fmt.Errorf("build process environment: %w", err)
	}

	cmd := exec.Command(spec.Command, spec.Args...)
	cmd.Dir = worktree
	cmd.Env = env
	group := newProcessGroup()
	group.configure(cmd)
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create stdout pipe: %w", err)
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		return nil, fmt.Errorf("create stderr pipe: %w", err)
	}
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter
	// Check after all validation and setup, immediately before the irreversible
	// spawn. Callers must not start a process after cancellation was observed.
	if err := ctx.Err(); err != nil {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		_ = stderrReader.Close()
		_ = stderrWriter.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		_ = stderrReader.Close()
		_ = stderrWriter.Close()
		return nil, fmt.Errorf("start process: %w", err)
	}
	// A child that cannot be put in its process group is unstoppable, which is
	// worse than a failed start: kill it and report the failure instead.
	if err := group.attach(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		_ = stderrReader.Close()
		_ = stderrWriter.Close()
		_ = cmd.Wait()
		return nil, fmt.Errorf("attach process group: %w", err)
	}
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()

	events, logs := f.events, f.logs
	if spec.Events != nil {
		events = spec.Events
	}
	if spec.Logs != nil {
		logs = spec.Logs
	}
	p := &execProcess{cmd: cmd, ctx: ctx, events: events, logs: logs, now: f.now, start: f.now(), group: group, done: make(chan struct{}), waitFinished: make(chan struct{}), cancelDone: make(chan struct{})}
	p.outputWG.Add(2)
	go p.drain("stdout", stdoutReader)
	go p.drain("stderr", stderrReader)
	go func() {
		select {
		case <-ctx.Done():
			_ = p.Cancel()
		case <-p.done:
		}
	}()
	return p, nil
}

type execProcess struct {
	cmd          *exec.Cmd
	ctx          context.Context
	events       EventSink
	logs         RawLogWriter
	now          func() time.Time
	start        time.Time
	group        *processGroup
	done         chan struct{}
	waitFinished chan struct{}

	outputWG   sync.WaitGroup
	errMu      sync.Mutex
	outputErr  error
	cancelMu   sync.Mutex
	cancelOnce sync.Once
	cancelDone chan struct{}
	cancelErr  error
	waitOnce   sync.Once
	result     ProcessResult
	waitErr    error
}

func (p *execProcess) drain(stream string, reader io.ReadCloser) {
	defer p.outputWG.Done()
	defer reader.Close()
	buf := make([]byte, 32*1024)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			payload := append([]byte(nil), buf[:n]...)
			if p.events != nil {
				event := Event{Type: EventOutput, Stream: stream, Payload: payload, At: p.now()}
				if publishErr := p.events.Publish(p.ctx, event); publishErr != nil {
					p.recordOutputError(fmt.Errorf("publish %s output: %w", stream, publishErr))
				}
			}
			if p.logs != nil {
				if writeErr := p.logs.Write(p.ctx, stream, payload); writeErr != nil {
					p.recordOutputError(fmt.Errorf("write %s log: %w", stream, writeErr))
				}
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				p.recordOutputError(fmt.Errorf("read %s output: %w", stream, err))
			}
			return
		}
	}
}

func (p *execProcess) recordOutputError(err error) {
	p.errMu.Lock()
	defer p.errMu.Unlock()
	if p.outputErr == nil {
		p.outputErr = err
	}
}

func (p *execProcess) Wait() (ProcessResult, error) {
	p.waitOnce.Do(func() {
		defer close(p.waitFinished)
		err := p.cmd.Wait()
		p.outputWG.Wait()
		finished := p.now()
		p.result = ProcessResult{ExitCode: p.cmd.ProcessState.ExitCode(), StartedAt: p.start, FinishedAt: finished, Duration: finished.Sub(p.start)}
		p.errMu.Lock()
		outputErr := p.outputErr
		p.errMu.Unlock()
		p.waitErr = errors.Join(err, outputErr)
		if p.ctx != nil && p.ctx.Err() != nil {
			// The cancellation watcher runs independently of Wait. Ensure the
			// bounded TERM -> KILL sequence has completed before Wait returns;
			// reaping the leader alone does not prove descendants are gone.
			_ = p.Cancel()
			p.waitErr = errors.Join(p.waitErr, p.ctx.Err())
		}
		// Safe only here: the tree is gone, so releasing platform resources
		// cannot take a live descendant down with it.
		p.group.release()
		close(p.done)
	})
	<-p.waitFinished
	return p.result, p.waitErr
}

func (p *execProcess) Cancel() error {
	p.cancelOnce.Do(func() {
		err := p.group.terminate()
		if err != nil {
			err = fmt.Errorf("cancel process group: %w", err)
		}
		p.cancelMu.Lock()
		p.cancelErr = err
		p.cancelMu.Unlock()
		close(p.cancelDone)
	})
	<-p.cancelDone
	p.cancelMu.Lock()
	defer p.cancelMu.Unlock()
	return p.cancelErr
}

func cleanExistingDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("working directory is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	info, err := os.Stat(absolute)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", absolute)
	}
	return absolute, nil
}

func mergedEnvironment(overrides []string) ([]string, error) {
	values := make(map[string]string)
	order := make([]string, 0)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		values[key] = value
		order = append(order, key)
	}
	for _, entry := range overrides {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("environment override %q is not KEY=VALUE", entry)
		}
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = value
	}
	result := make([]string, 0, len(order))
	for _, key := range order {
		result = append(result, key+"="+values[key])
	}
	return result, nil
}
