package state

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestFileStoreSaveLoadListAcrossInstances(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	first := validProjectState()
	first.Slug = "zeta-project"
	first.PullRequestURL = "https://github.com/example/repo/pull/7"
	second := validProjectState()
	second.Slug = "alpha-project"
	if err := store.Save(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".gg", "projects", first.Slug, "state.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state path missing: %v", err)
	}

	reopened, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Load(context.Background(), first.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, first) {
		t.Fatalf("Load() = %#v, want %#v", got, first)
	}
	list, err := reopened.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Slug != second.Slug || list[1].Slug != first.Slug {
		t.Fatalf("List() = %#v, want sorted alpha/zeta", list)
	}
}

func TestFileStoreDeleteIsIdempotentAndPersistsAcrossRestart(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	project := validProjectState()
	project.Slug = "delete-me"
	if err := store.Save(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(context.Background(), project.Slug); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(context.Background(), project.Slug); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Load(context.Background(), project.Slug); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load() error=%v, want missing state", err)
	}
}

func TestFileStoreRejectsPathTraversalAndUnsafeSlugs(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"../escape", "a/b", "-bad", "bad-", "bad--slug", "UPPER"} {
		if _, err := store.Load(context.Background(), slug); err == nil {
			t.Errorf("Load(%q) unexpectedly succeeded", slug)
		}
	}
	state := validProjectState()
	state.Slug = "../escape"
	if err := store.Save(context.Background(), state); err == nil {
		t.Fatal("Save() unexpectedly accepted path traversal")
	}
}

func TestFileStoreVersionHandlingAndMigrationHook(t *testing.T) {
	root := t.TempDir()
	state := validProjectState()
	state.Slug = "migrated"
	legacy := state
	legacy.SchemaVersion = 0
	legacyBytes, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".gg", "projects", state.Slug, "state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, legacyBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	withoutMigration, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := withoutMigration.Load(context.Background(), state.Slug); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("Load() error = %v, want ErrUnsupportedVersion", err)
	}
	store, err := NewFileStoreWithMigrations(root, map[int]StateMigration{
		0: func(ctx context.Context, document json.RawMessage) (json.RawMessage, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			var upgraded map[string]any
			if err := json.Unmarshal(document, &upgraded); err != nil {
				return nil, err
			}
			upgraded["schemaVersion"] = CurrentSchemaVersion
			return json.Marshal(upgraded)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background(), state.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, state) {
		t.Fatalf("migrated state = %#v, want %#v", got, state)
	}
}

func TestFileStoreLoadsSchemaV1LegacyDocumentWithoutReservationToken(t *testing.T) {
	root := t.TempDir()
	state := validProjectState()
	state.Slug = "legacy-running"
	state.Status = StatusRunning
	document, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var fixture map[string]any
	if err := json.Unmarshal(document, &fixture); err != nil {
		t.Fatal(err)
	}
	delete(fixture, "runReservationToken")
	document, err = json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".gg", "projects", state.Slug, "state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, document, 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background(), state.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != 1 || got.Status != StatusRunning || got.RunReservationToken != "" {
		t.Fatalf("legacy schema-v1 state = %#v", got)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("legacy schema-v1 state failed current validation: %v", err)
	}
}

func TestFileStoreRecoveryPolicyForCorruptAndPartialState(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".gg", "projects", "broken", "state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"schemaVersion":1,"name":"partial"`)
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background(), "broken"); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("Load() error = %v, want ErrCorruptState", err)
	}
	unchanged, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(unchanged) != string(original) {
		t.Fatal("corrupt state was modified; recovery policy must leave it untouched")
	}
	if _, err := store.List(context.Background()); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("List() error = %v, want ErrCorruptState", err)
	}
}

func TestFileStoreHonorsCanceledContext(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.List(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("List() error = %v, want context.Canceled", err)
	}
	if _, err := store.Load(ctx, "valid-project"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Load() error = %v, want context.Canceled", err)
	}
	if err := store.Save(ctx, validProjectState()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Save() error = %v, want context.Canceled", err)
	}
}

func TestAtomicReplaceFailureCleansTemporaryFileAndPreservesTarget(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "project")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, "state.json")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := atomicReplace(context.Background(), target, []byte("new state")); err == nil {
		t.Fatal("atomicReplace unexpectedly succeeded over a directory")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".tmp" {
			t.Fatalf("temporary file left behind: %s", entry.Name())
		}
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("failed replacement did not preserve target directory: %v", err)
	}
}

func TestAtomicReplaceRecoversAfterFailedReplacement(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "project")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, "state.json")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := atomicReplace(context.Background(), target, []byte("new state")); err == nil {
		t.Fatal("atomicReplace unexpectedly succeeded over a directory")
	}
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	if err := atomicReplace(context.Background(), target, []byte("new state")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new state" {
		t.Fatalf("recovered state = %q, want %q", got, "new state")
	}
}

func TestFileStoreConcurrentWritersSerializeAcrossInstances(t *testing.T) {
	root := t.TempDir()
	first, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	states := []ProjectState{validProjectState(), validProjectState()}
	states[0].Name = "first"
	states[1].Name = "second"
	var results = make(chan error, len(states))
	for index, store := range []*FileStore{first, second} {
		store, state := store, states[index]
		go func() { results <- store.Save(context.Background(), state) }()
	}
	for range states {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	got, err := first.Load(context.Background(), states[0].Slug)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "first" && got.Name != "second" {
		t.Fatalf("final state has unexpected writer: %#v", got)
	}
	// The advisory lock must be released after the writers finished: a fresh
	// locker acquires it immediately (the lock file itself may remain).
	lock, err := newFileProjectLocker(root).Lock(context.Background(), states[0].Slug)
	if err != nil {
		t.Fatalf("project lock remains held after writers finished: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFileProjectLockerRejectsUnsafeSlug(t *testing.T) {
	locker := newFileProjectLocker(t.TempDir())
	for _, slug := range []string{"../escape", "a/b", "-bad", "bad-"} {
		if _, err := locker.Lock(context.Background(), slug); err == nil {
			t.Errorf("Lock(%q) unexpectedly succeeded", slug)
		}
	}
}

func TestFileProjectLockerHonorsCancellationAndTimeout(t *testing.T) {
	root := t.TempDir()
	locker := newFileProjectLocker(root)
	locker.maxWait = 50 * time.Millisecond
	locker.initialGap = time.Millisecond
	held, err := locker.Lock(context.Background(), "held")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if _, err := locker.Lock(ctx, "held"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Lock() error = %v, want context deadline", err)
	}
	if _, err := locker.Lock(context.Background(), "held"); err == nil {
		t.Fatal("Lock() unexpectedly acquired held lock")
	}
}

func TestStaleLockFileFromDeadProcessDoesNotBlockAcquisition(t *testing.T) {
	root := t.TempDir()
	lockPath := filepath.Join(root, ".gg", "locks", "held.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// A leftover lock file (crashed process, killed terminal) holds no
	// advisory lock and must not block anyone.
	if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	lock, err := newFileProjectLocker(root).Lock(context.Background(), "held")
	if err != nil {
		t.Fatalf("stale lock file blocked acquisition: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFileStoreFailedReplacementPreservesLastValidState(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	original := validProjectState()
	if err := store.Save(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	store.replace = func(context.Context, string, []byte) error {
		return errors.New("simulated interrupted replacement")
	}
	updated := original
	updated.Name = "updated"
	if err := store.Save(context.Background(), updated); err == nil {
		t.Fatal("Save() unexpectedly succeeded")
	}
	got, err := store.Load(context.Background(), original.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != original.Name {
		t.Fatalf("state after failed replacement = %q, want %q", got.Name, original.Name)
	}
}
