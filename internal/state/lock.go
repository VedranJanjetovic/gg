package state

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// ProjectLocker serializes operations for one project across gg processes.
type ProjectLocker interface {
	Lock(ctx context.Context, slug string) (io.Closer, error)
}

// errLockHeld reports that another live process currently holds the lock.
var errLockHeld = errors.New("project lock is held by another process")

type fileProjectLocker struct {
	root       string
	maxWait    time.Duration
	initialGap time.Duration
}

func newFileProjectLocker(root string) *fileProjectLocker {
	return &fileProjectLocker{root: root, maxWait: 2 * time.Second, initialGap: 10 * time.Millisecond}
}

// Lock acquires an OS-level advisory lock (flock/LockFileEx) on the project's
// lock file. The kernel releases it automatically when the process exits for
// any reason — including a killed terminal — so locks can never go stale.
func (l *fileProjectLocker) Lock(ctx context.Context, slug string) (io.Closer, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if !validSlug(slug) {
		return nil, fmt.Errorf("invalid project slug %q", slug)
	}
	path := filepath.Join(l.root, ".gg", "locks", slug+".lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create project lock directory: %w", err)
	}
	deadline := time.NewTimer(l.maxWait)
	defer deadline.Stop()
	gap := l.initialGap
	for {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o644)
		if err != nil {
			return nil, fmt.Errorf("open project lock %q: %w", slug, err)
		}
		lockErr := tryLockFile(file)
		if lockErr == nil {
			return &fileLock{file: file}, nil
		}
		_ = file.Close()
		if !errors.Is(lockErr, errLockHeld) {
			return nil, fmt.Errorf("acquire project lock %q: %w", slug, lockErr)
		}
		timer := time.NewTimer(gap)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-deadline.C:
			timer.Stop()
			return nil, fmt.Errorf("acquire project lock %q: another gg process is using this project (timeout)", slug)
		case <-timer.C:
		}
		if gap < 100*time.Millisecond {
			gap *= 2
		}
	}
}

type fileLock struct {
	file *os.File
}

// Close releases the advisory lock. The lock file itself is intentionally
// never removed: deleting it would race a concurrent acquirer that already
// holds a handle to the old inode, and an unlocked leftover file is harmless.
func (l *fileLock) Close() error {
	unlockErr := unlockFile(l.file)
	closeErr := l.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

var _ ProjectLocker = (*fileProjectLocker)(nil)
