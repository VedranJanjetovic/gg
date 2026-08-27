package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/VedranJanjetovic/gg/internal/robustio"
)

var (
	// ErrCorruptState means a state file is missing, unreadable, or invalid.
	// Load and List never delete or overwrite such files; callers can repair the
	// file explicitly after inspecting the returned error.
	ErrCorruptState = errors.New("corrupt project state")
	// ErrUnsupportedVersion means the state is valid JSON but cannot be migrated
	// to CurrentSchemaVersion by the migrations registered with the store.
	ErrUnsupportedVersion = errors.New("unsupported project state schema version")
)

// StateMigration upgrades one JSON state document from fromVersion to the next
// schema version. Migrations must return a complete JSON document and must set
// its schemaVersion to fromVersion+1. They are applied in ascending order.
type StateMigration func(ctx context.Context, document json.RawMessage) (json.RawMessage, error)

// Store is the persistence boundary for project state. Implementations expose
// validated domain values while owning their persistence format and migrations.
type Store interface {
	Load(ctx context.Context, slug string) (ProjectState, error)
	Save(ctx context.Context, state ProjectState) error
	List(ctx context.Context) ([]ProjectState, error)
	Delete(ctx context.Context, slug string) error
}

// FileStore persists each project below <root>/.gg/projects/<slug>/state.json.
// Saves use per-project locks and crash-safe atomic replacement.
type FileStore struct {
	root       string
	migrations map[int]StateMigration
	locker     ProjectLocker
	replace    func(context.Context, string, []byte) error
}

// NewFileStore creates a store rooted at configuredRoot. The root itself is not
// created until Save is called, so constructing a store is side-effect free.
func NewFileStore(configuredRoot string) (*FileStore, error) {
	return NewFileStoreWithMigrations(configuredRoot, nil)
}

// NewFileStoreWithMigrations creates a store with optional one-version-at-a-time
// schema migrations. Unsupported versions and corrupt/partial files are
// deterministic errors: they are returned to the caller and left untouched.
func NewFileStoreWithMigrations(configuredRoot string, migrations map[int]StateMigration) (*FileStore, error) {
	if configuredRoot == "" {
		return nil, errors.New("configured root is required")
	}
	root, err := filepath.Abs(configuredRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve configured root: %w", err)
	}
	copied := make(map[int]StateMigration, len(migrations))
	for version, migration := range migrations {
		if version < 0 || migration == nil {
			return nil, fmt.Errorf("invalid migration from schema version %d", version)
		}
		copied[version] = migration
	}
	return &FileStore{root: root, migrations: copied, locker: newFileProjectLocker(root), replace: atomicReplace}, nil
}

// Root returns the resolved configured root used by the store.
func (s *FileStore) Root() string { return s.root }

// Locker returns the per-project locker used by lifecycle services.
func (s *FileStore) Locker() ProjectLocker { return s.locker }

// Load reads and validates one project state. A missing file is returned as an
// error, as are malformed, partial, and unsupported state files.
func (s *FileStore) Load(ctx context.Context, slug string) (ProjectState, error) {
	if err := checkContext(ctx); err != nil {
		return ProjectState{}, err
	}
	path, err := s.statePath(slug)
	if err != nil {
		return ProjectState{}, err
	}
	// Reads are lock-free: Save replaces the file atomically, so a reader
	// always sees one complete document. This keeps list/status/attach
	// responsive while another gg process works on the project.
	document, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ProjectState{}, fmt.Errorf("load project %q: %w", slug, err)
		}
		return ProjectState{}, corruptError(slug, err)
	}
	if err := checkContext(ctx); err != nil {
		return ProjectState{}, err
	}
	state, err := s.decode(ctx, slug, document)
	if err != nil {
		return ProjectState{}, err
	}
	return state, checkContext(ctx)
}

// Save validates and atomically replaces one project state while holding its
// per-project lock.
func (s *FileStore) Save(ctx context.Context, input ProjectState) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	state, err := NewProjectState(input)
	if err != nil {
		return fmt.Errorf("validate project state: %w", err)
	}
	path, err := s.statePath(state.Slug)
	if err != nil {
		return err
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create project state directory: %w", err)
	}
	document, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode project %q: %w", state.Slug, err)
	}
	document = append(document, '\n')
	if err := checkContext(ctx); err != nil {
		return err
	}
	var lock io.Closer
	if !projectLockHeld(ctx, state.Slug) {
		lock, err = s.locker.Lock(ctx, state.Slug)
		if err != nil {
			return err
		}
		defer lock.Close()
	}
	if err := s.replace(ctx, path, document); err != nil {
		return fmt.Errorf("save project %q: %w", state.Slug, err)
	}
	return checkContext(ctx)
}

// List loads every project with a state.json file. Results are sorted by slug.
// Any corrupt, partial, unsupported, or unreadable state aborts the operation;
// this prevents an apparently complete list from silently omitting a project.
func (s *FileStore) List(ctx context.Context) ([]ProjectState, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(s.root, ".gg", "projects"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []ProjectState{}, nil
		}
		return nil, fmt.Errorf("list project state directory: %w", err)
	}
	states := make([]ProjectState, 0, len(entries))
	for _, entry := range entries {
		if err := checkContext(ctx); err != nil {
			return nil, err
		}
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(s.root, ".gg", "projects", entry.Name(), "state.json")
		if _, err := os.Stat(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, corruptError(entry.Name(), err)
		}
		state, err := s.Load(ctx, entry.Name())
		if err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].Slug < states[j].Slug })
	return states, checkContext(ctx)
}

// Delete removes one project state after its caller has cleaned up any external
// resources. Missing state is treated as success so a completed prune is
// idempotent. The state file is never removed before the caller's cleanup.
func (s *FileStore) Delete(ctx context.Context, slug string) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	path, err := s.statePath(slug)
	if err != nil {
		return err
	}
	var lock io.Closer
	if !projectLockHeld(ctx, slug) {
		lock, err = s.locker.Lock(ctx, slug)
		if err != nil {
			return err
		}
		defer lock.Close()
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete project %q: %w", slug, err)
	}
	return checkContext(ctx)
}

func (s *FileStore) statePath(slug string) (string, error) {
	if !validSlug(slug) {
		return "", fmt.Errorf("invalid project slug %q", slug)
	}
	return filepath.Join(s.root, ".gg", "projects", slug, "state.json"), nil
}

func (s *FileStore) decode(ctx context.Context, slug string, document []byte) (ProjectState, error) {
	if len(document) == 0 {
		return ProjectState{}, corruptError(slug, errors.New("empty document"))
	}
	var envelope struct {
		SchemaVersion *int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(document, &envelope); err != nil || envelope.SchemaVersion == nil {
		if err == nil {
			err = errors.New("schemaVersion is required")
		}
		return ProjectState{}, corruptError(slug, err)
	}
	version := *envelope.SchemaVersion
	for version < CurrentSchemaVersion {
		migration, ok := s.migrations[version]
		if !ok {
			return ProjectState{}, fmt.Errorf("project %q: %w: %d", slug, ErrUnsupportedVersion, version)
		}
		if err := checkContext(ctx); err != nil {
			return ProjectState{}, err
		}
		upgraded, err := migration(ctx, json.RawMessage(document))
		if err != nil {
			return ProjectState{}, corruptError(slug, fmt.Errorf("migration from version %d: %w", version, err))
		}
		var next struct {
			SchemaVersion *int `json:"schemaVersion"`
		}
		if err := json.Unmarshal(upgraded, &next); err != nil || next.SchemaVersion == nil || *next.SchemaVersion != version+1 {
			return ProjectState{}, corruptError(slug, fmt.Errorf("migration from version %d returned invalid next version", version))
		}
		document = upgraded
		version = *next.SchemaVersion
	}
	if version != CurrentSchemaVersion {
		return ProjectState{}, fmt.Errorf("project %q: %w: %d", slug, ErrUnsupportedVersion, version)
	}
	var state ProjectState
	if err := json.Unmarshal(document, &state); err != nil {
		return ProjectState{}, corruptError(slug, err)
	}
	if err := state.Validate(); err != nil {
		return ProjectState{}, corruptError(slug, err)
	}
	if state.Slug != slug {
		return ProjectState{}, corruptError(slug, fmt.Errorf("state slug is %q", state.Slug))
	}
	return state, nil
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func corruptError(slug string, err error) error {
	return fmt.Errorf("project %q: %w: %v", slug, ErrCorruptState, err)
}

func atomicReplace(ctx context.Context, path string, document []byte) (err error) {
	if err = checkContext(ctx); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".state.json-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary state file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err = temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set temporary state file permissions: %w", err)
	}
	if _, err = temporary.Write(document); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary state file: %w", err)
	}
	if err = checkContext(ctx); err != nil {
		_ = temporary.Close()
		return err
	}
	if err = temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary state file: %w", err)
	}
	if err = temporary.Close(); err != nil {
		return fmt.Errorf("close temporary state file: %w", err)
	}
	if err = robustio.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace state file: %w", err)
	}
	if directory, syncErr := os.Open(filepath.Dir(path)); syncErr == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

var _ Store = (*FileStore)(nil)

// ProjectService is the application boundary for lifecycle operations that use
// a Store. Its implementation is intentionally deferred to a later phase.
type ProjectService interface {
	Create(ctx context.Context, state ProjectState) error
	Load(ctx context.Context, slug string) (ProjectState, error)
	Save(ctx context.Context, state ProjectState) error
	List(ctx context.Context) ([]ProjectState, error)
	Delete(ctx context.Context, slug string) error
}
