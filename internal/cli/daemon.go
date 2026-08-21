package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// RunSpawner launches `gg <args>` as a detached background process rooted at
// the configured folder, with its output appended to logPath. The spawned
// process must survive the calling gg process exiting.
type RunSpawner func(ctx context.Context, root string, args []string, logPath string) error

// WithRunSpawner enables detached pipeline execution: instead of running the
// pipeline inside the attached gg process (where it dies with the process),
// start and resume actions spawn a background gg that owns the run.
func WithRunSpawner(spawner RunSpawner) Option {
	return func(app *App) { app.runSpawner = spawner }
}

const (
	defaultDetachedStartTimeout = 15 * time.Second
	defaultDetachedPollInterval = 150 * time.Millisecond
)

// startDetached spawns `gg <args>` in the background and waits until the
// project's persisted state shows the spawned process took ownership (any
// status change away from the observed starting status), so a daemon that
// dies immediately surfaces an error with the log path instead of silently
// leaving the project parked.
func (a *App) startDetached(ctx context.Context, selector string, args []string) error {
	root, err := a.root.ConfiguredRoot(ctx)
	if err != nil {
		return fmt.Errorf("resolve configured root: %w", err)
	}
	service, err := a.projectService(ctx)
	if err != nil {
		return fmt.Errorf("load project service: %w", err)
	}
	before, err := service.Load(ctx, selector)
	if err != nil {
		return fmt.Errorf("load project %q: %w", selector, err)
	}
	logPath := filepath.Join(root, ".gg", "projects", selector, "logs", "daemon.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return fmt.Errorf("prepare daemon log directory: %w", err)
	}
	if err := a.runSpawner(ctx, root, args, logPath); err != nil {
		return fmt.Errorf("spawn detached run: %w", err)
	}
	timeout := a.detachedStartTimeout
	if timeout <= 0 {
		timeout = defaultDetachedStartTimeout
	}
	interval := a.detachedPollInterval
	if interval <= 0 {
		interval = defaultDetachedPollInterval
	}
	deadline := time.Now().Add(timeout)
	for {
		project, loadErr := service.Load(ctx, selector)
		if loadErr == nil && (project.Status != before.Status || !project.UpdatedAt.Equal(before.UpdatedAt)) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("detached run did not take over project %q within %s; see %s", selector, timeout, logPath)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// NewDetachedRunSpawner returns the production RunSpawner: it re-executes the
// current gg binary in its own session with stdin disconnected and output
// appended to the log file, so the run survives the parent gg (and its
// terminal) exiting.
func NewDetachedRunSpawner() RunSpawner {
	return func(ctx context.Context, root string, args []string, logPath string) error {
		executable, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate gg executable: %w", err)
		}
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("open daemon log %s: %w", logPath, err)
		}
		defer logFile.Close()
		fmt.Fprintf(logFile, "--- gg detached run %s: gg %v\n", time.Now().UTC().Format(time.RFC3339), args)
		// The detached context is deliberate: the child must not inherit the
		// parent's cancellation, or quitting the parent would kill the run.
		cmd := exec.Command(executable, args...)
		cmd.Dir = root
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		cmd.SysProcAttr = detachedSysProcAttr()
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start detached gg: %w", err)
		}
		// Reap the child if it exits while this process is still alive; when
		// this process exits first, the child re-parents to init.
		go func() { _ = cmd.Wait() }()
		return nil
	}
}
