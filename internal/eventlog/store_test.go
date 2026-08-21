package eventlog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/VedranJanjetovic/gg/internal/agent"
	"github.com/VedranJanjetovic/gg/internal/orchestrator"
	"github.com/VedranJanjetovic/gg/internal/pipeline"
)

func TestStorePersistsAgentAndOrchestratorEventsAsJSONL(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.AgentSink().Publish(context.Background(), agent.Event{
		ProjectSlug: "event-project", Phase: pipeline.PhaseQA, Type: agent.EventOutput,
		Payload: []byte("evidence\n"), At: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.OrchestratorSink().Publish(context.Background(), orchestrator.Event{
		ProjectSlug: "event-project", Phase: pipeline.PhaseQA,
		Type: orchestrator.EventPhaseFailed, Error: errors.New("semantic failure"), At: now,
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".gg", "projects", "event-project", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	if len(lines) != 2 {
		t.Fatalf("event lines = %d, want 2", len(lines))
	}
	for _, line := range lines {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("invalid JSONL line %q: %v", line, err)
		}
	}
	if !bytes.Contains(lines[1], []byte(`"error":"semantic failure"`)) {
		t.Fatalf("orchestrator error was not serialized as a string: %s", lines[1])
	}
}

func TestStoreRejectsUnsafeProjectSlug(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	err = store.AgentSink().Publish(context.Background(), agent.Event{ProjectSlug: "../escape", Type: agent.EventStarted, At: time.Now().UTC()})
	if err == nil {
		t.Fatal("unsafe project slug was accepted")
	}
}

func TestStoreRejectsSymlinkedProjectJournalDirectory(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, ".gg", "projects")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(projects, "event-project")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	err = store.AgentSink().Publish(context.Background(), agent.Event{
		ProjectSlug: "event-project",
		Type:        agent.EventStarted,
		At:          time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("symlinked project event directory was accepted")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "events.jsonl")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("event journal escaped state root: %v", statErr)
	}
}

func TestStoreRejectsSymlinkedJournalFile(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, ".gg", "projects", "event-project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.WriteFile(outside, []byte("sentinel\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(projectDir, "events.jsonl")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	err = store.OrchestratorSink().Publish(context.Background(), orchestrator.Event{
		ProjectSlug: "event-project",
		Type:        orchestrator.EventProjectFinished,
		At:          time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("symlinked event journal file was accepted")
	}
	data, readErr := os.ReadFile(outside)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "sentinel\n" {
		t.Fatalf("symlink target was modified: %q", data)
	}
}
