// Package eventlog persists agent and orchestrator events in one per-project
// JSONL journal. Project state remains the authoritative lifecycle model.
package eventlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/VedranJanjetovic/gg/internal/agent"
	"github.com/VedranJanjetovic/gg/internal/orchestrator"
)

// Store appends complete JSON objects to per-project event journals.
type Store struct {
	root string
	mu   sync.Mutex
}

func New(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("event log root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve event log root: %w", err)
	}
	absolute = filepath.Clean(absolute)
	if resolved, resolveErr := filepath.EvalSymlinks(absolute); resolveErr == nil {
		absolute = resolved
	}
	return &Store{root: absolute}, nil
}

func (s *Store) AgentSink() agent.EventSink {
	return agentSink{store: s}
}

func (s *Store) OrchestratorSink() orchestrator.EventSink {
	return orchestratorSink{store: s}
}

type journalRecord struct {
	Type        string                     `json:"type"`
	ProjectSlug string                     `json:"projectSlug"`
	Phase       string                     `json:"phase,omitempty"`
	Subphase    string                     `json:"subphase,omitempty"`
	Stream      string                     `json:"stream,omitempty"`
	Payload     string                     `json:"payload,omitempty"`
	At          time.Time                  `json:"at"`
	Error       string                     `json:"error,omitempty"`
	Result      *agent.RunResult           `json:"result,omitempty"`
	Outcome     *orchestrator.PhaseOutcome `json:"outcome,omitempty"`
}

func (s *Store) append(record journalRecord) error {
	if s == nil {
		return errors.New("event log store is nil")
	}
	if !validSlug(record.ProjectSlug) {
		return fmt.Errorf("invalid event project slug %q", record.ProjectSlug)
	}
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode event %q: %w", record.Type, err)
	}
	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	projectsRoot := filepath.Join(s.root, ".gg", "projects")
	dir := filepath.Join(projectsRoot, record.ProjectSlug)
	if !pathWithin(projectsRoot, dir) {
		return errors.New("event project path escapes state root")
	}
	if err := mkdirAllWithoutSymlinks(s.root, dir); err != nil {
		return fmt.Errorf("create event journal directory: %w", err)
	}
	path := filepath.Join(dir, "events.jsonl")
	if err := rejectSymlink(path); err != nil {
		return fmt.Errorf("validate event journal: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open event journal: %w", err)
	}
	if err := validateOpenedJournal(s.root, dir, path, file); err != nil {
		_ = file.Close()
		return fmt.Errorf("validate opened event journal: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("append event journal: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync event journal: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close event journal: %w", err)
	}
	return nil
}

type agentSink struct{ store *Store }

func (sink agentSink) Publish(_ context.Context, event agent.Event) error {
	return sink.store.append(journalRecord{
		Type:        string(event.Type),
		ProjectSlug: event.ProjectSlug,
		Phase:       string(event.Phase),
		Subphase:    event.Subphase,
		Stream:      event.Stream,
		Payload:     string(event.Payload),
		At:          event.At,
		Result:      event.Result,
	})
}

type orchestratorSink struct{ store *Store }

func (sink orchestratorSink) Publish(_ context.Context, event orchestrator.Event) error {
	record := journalRecord{
		Type:        string(event.Type),
		ProjectSlug: event.ProjectSlug,
		Phase:       string(event.Phase),
		Subphase:    event.Subphase,
		At:          event.At,
		Outcome:     event.Outcome,
	}
	if event.Error != nil {
		record.Error = event.Error.Error()
	}
	return sink.store.append(record)
}

func validSlug(slug string) bool {
	if slug == "" || strings.HasPrefix(slug, "-") || strings.HasSuffix(slug, "-") || strings.Contains(slug, "--") {
		return false
	}
	for _, character := range slug {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func mkdirAllWithoutSymlinks(root, target string) error {
	if !pathWithin(root, target) {
		return errors.New("directory escapes event log root")
	}
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return err
	}
	current := filepath.Clean(root)
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		switch {
		case statErr == nil:
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("%q is a symlink", current)
			}
			if !info.IsDir() {
				return fmt.Errorf("%q is not a directory", current)
			}
		case errors.Is(statErr, os.ErrNotExist):
			if err := os.Mkdir(current, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err = os.Lstat(current)
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("%q is not a real directory", current)
			}
		default:
			return statErr
		}
	}
	return nil
}

func rejectSymlink(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%q is a symlink", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%q is not a regular file", path)
	}
	return nil
}

func validateOpenedJournal(root, dir, path string, file *os.File) error {
	if err := mkdirAllWithoutSymlinks(root, dir); err != nil {
		return err
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() {
		return fmt.Errorf("%q is not a regular non-symlink file", path)
	}
	fileInfo, err := file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(pathInfo, fileInfo) {
		return fmt.Errorf("%q changed while opening", path)
	}
	return nil
}
