package proof

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverAndCopyTable(t *testing.T) {
	const proofText = "# PROOF\n\n## Validation: flow\n- Status: pass\n- Test location: api_test.go\n- Test name: TestRealRequest\n- Flow/scenario: send a real local request\n- What it verifies: response and persistence\n- Proof it passed: $ go test ./... exited 0\n- Manual run instructions: start the service and curl the endpoint.\n"
	tests := []struct {
		name    string
		data    []byte
		wantErr bool
	}{
		{"copy preserves bytes", []byte(proofText), false},
		{"invalid proof rejected", []byte("## Validation: missing\n- Status: maybe\n"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, worktree := t.TempDir(), t.TempDir()
			if err := os.WriteFile(proofPath(t, worktree), tt.data, 0600); err != nil {
				t.Fatal(err)
			}
			artifact, err := NewArtifactService(root, alwaysUncommittedChecker{}).DiscoverAndCopy(context.Background(), worktree, "demo", ArtifactBaseline{}, "", false)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected validation error")
				}
				if _, statErr := os.Stat(filepath.Join(root, ".gg", "projects", "demo", "artifacts", ArtifactName)); !os.IsNotExist(statErr) {
					t.Fatalf("invalid proof was copied: %v", statErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			copied, err := os.ReadFile(artifact.Path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(copied, tt.data) {
				t.Fatal("copy changed proof bytes")
			}
			if artifact.Classification != ClassificationPass {
				t.Fatalf("classification=%q", artifact.Classification)
			}
		})
	}
}

func TestDiscoverAndCopyRejectsUnsafeProjectSlug(t *testing.T) {
	root, worktree := t.TempDir(), t.TempDir()
	if err := os.WriteFile(proofPath(t, worktree), []byte(proofTextForArtifactTest), 0600); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"../escape", "Demo", "-bad", "bad--slug"} {
		t.Run(slug, func(t *testing.T) {
			if _, err := NewArtifactService(root, alwaysUncommittedChecker{}).DiscoverAndCopy(context.Background(), worktree, slug, ArtifactBaseline{}, "", false); err == nil {
				t.Fatalf("unsafe slug %q was accepted", slug)
			}
		})
	}
}

const proofTextForArtifactTest = "# PROOF\n\n## Validation: flow\n- Status: pass\n- Test location: api_test.go\n- Test name: TestRealRequest\n- Flow/scenario: send a real local request\n- What it verifies: response and persistence\n- Proof it passed: `go test ./...` exited 0\n- Manual run instructions: start the service and run the curl request.\n"

type alwaysUncommittedChecker struct{}

func (alwaysUncommittedChecker) IsUncommittedNewFile(context.Context, string, string) (bool, error) {
	return true, nil
}

func TestDiscoverAndCopyRejectsPreExistingUntrackedProofButAcceptsChangedProof(t *testing.T) {
	root, worktree := t.TempDir(), t.TempDir()
	path := proofPath(t, worktree)
	baselineData := []byte("---\ngg_run_id: \"run-1\"\n---\n\n" + proofTextForArtifactTest)
	if err := os.WriteFile(path, baselineData, 0600); err != nil {
		t.Fatal(err)
	}
	service := NewArtifactService(root, alwaysUncommittedChecker{})
	baseline, err := service.Capture(context.Background(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	if !baseline.Exists {
		t.Fatal("Capture did not record the existing proof baseline")
	}

	// The file is deliberately reported as untracked. Freshness, not Git status,
	// must reject a valid proof that existed before this QA attempt.
	if _, err := service.DiscoverAndCopy(context.Background(), worktree, "demo", baseline, "run-1", false); err == nil || !strings.Contains(err.Error(), "not created or changed") {
		t.Fatalf("pre-existing untracked proof error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".gg", "projects", "demo", "artifacts", ArtifactName)); !os.IsNotExist(err) {
		t.Fatalf("pre-existing proof was copied: %v", err)
	}

	changedData := []byte("---\ngg_run_id: \"run-2\"\n---\n\n" + proofTextForArtifactTest + "\n## Feedback\nThe changed proof is from the current QA attempt.\n")
	if err := os.WriteFile(path, changedData, 0600); err != nil {
		t.Fatal(err)
	}
	artifact, err := service.DiscoverAndCopy(context.Background(), worktree, "demo", baseline, "run-2", false)
	if err != nil {
		t.Fatalf("changed proof was rejected: %v", err)
	}
	copied, err := os.ReadFile(artifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(copied, changedData) {
		t.Fatal("changed proof copy did not preserve bytes")
	}
}

type neverUncommittedChecker struct{}

func (neverUncommittedChecker) IsUncommittedNewFile(context.Context, string, string) (bool, error) {
	return false, nil
}

func TestDiscoverAndCopyRejectsUnchangedAndWrongRunProof(t *testing.T) {
	root, worktree := t.TempDir(), t.TempDir()
	path := proofPath(t, worktree)
	data := []byte("---\ngg_run_id: \"run-1\"\n---\n\n" + proofTextForArtifactTest)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	service := NewArtifactService(root, alwaysUncommittedChecker{})
	baseline, err := service.Capture(context.Background(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.DiscoverAndCopy(context.Background(), worktree, "demo", baseline, "run-1", false); err == nil || !strings.Contains(err.Error(), "not created or changed") {
		t.Fatalf("unchanged proof error = %v", err)
	}
	if err := os.WriteFile(path, bytes.Replace(data, []byte("run-1"), []byte("run-old"), 1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.DiscoverAndCopy(context.Background(), worktree, "demo", baseline, "run-2", false); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("wrong run proof error = %v", err)
	}
}

type trackedChecker struct{}

func (trackedChecker) IsUncommittedNewFile(context.Context, string, string) (bool, error) {
	return false, nil
}

func TestDiscoverAndCopyRejectsChangedTrackedProof(t *testing.T) {
	root, worktree := t.TempDir(), t.TempDir()
	path := proofPath(t, worktree)
	baselineData := []byte(proofTextForArtifactTest)
	if err := os.WriteFile(path, baselineData, 0600); err != nil {
		t.Fatal(err)
	}
	service := NewArtifactService(root, trackedChecker{})
	baseline, err := service.Capture(context.Background(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	changedData := []byte("---\ngg_run_id: \"run-2\"\n---\n\n" + string(baselineData))
	if err := os.WriteFile(path, changedData, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.DiscoverAndCopy(context.Background(), worktree, "demo", baseline, "run-2", false); err == nil || !strings.Contains(err.Error(), "newly produced uncommitted") {
		t.Fatalf("changed tracked proof error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".gg", "projects", "demo", "artifacts", ArtifactName)); !os.IsNotExist(err) {
		t.Fatalf("tracked proof was copied: %v", err)
	}
}

func TestDiscoverAndCopyAcceptsNewlyCreatedProof(t *testing.T) {
	root, worktree := t.TempDir(), t.TempDir()
	service := NewArtifactService(root, alwaysUncommittedChecker{})
	baseline, err := service.Capture(context.Background(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	if baseline.Exists {
		t.Fatal("Capture reported a proof before it was created")
	}
	data := []byte("---\ngg_run_id: \"run-new\"\n---\n\n" + proofTextForArtifactTest)
	if err := os.WriteFile(proofPath(t, worktree), data, 0600); err != nil {
		t.Fatal(err)
	}
	artifact, err := service.DiscoverAndCopy(context.Background(), worktree, "demo", baseline, "run-new", false)
	if err != nil {
		t.Fatalf("new proof was rejected: %v", err)
	}
	if artifact.Classification != ClassificationPass {
		t.Fatalf("new proof classification = %q, want %q", artifact.Classification, ClassificationPass)
	}
}

func TestDiscoverAndCopyRejectsEmptyAndWrongRunProof(t *testing.T) {
	for _, test := range []struct {
		name, data, runID, wantError string
	}{
		{name: "empty", data: "", runID: "run-empty", wantError: "proof must contain at least one validation"},
		{name: "wrong run", data: "---\ngg_run_id: \"run-old\"\n---\n\n" + proofTextForArtifactTest, runID: "run-current", wantError: "does not match"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, worktree := t.TempDir(), t.TempDir()
			if err := os.WriteFile(proofPath(t, worktree), []byte(test.data), 0600); err != nil {
				t.Fatal(err)
			}
			service := NewArtifactService(root, alwaysUncommittedChecker{})
			artifact, err := service.DiscoverAndCopy(context.Background(), worktree, "demo", ArtifactBaseline{}, test.runID, false)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("proof result = %#v, error = %v, want error containing %q", artifact, err, test.wantError)
			}
			if _, err := os.Stat(filepath.Join(root, ".gg", "projects", "demo", "artifacts", ArtifactName)); !os.IsNotExist(err) {
				t.Fatalf("invalid proof was copied: %v", err)
			}
		})
	}
}

// proofPath returns the worktree-relative proof location inside the ignored
// artifact directory, creating the directory.
func proofPath(t *testing.T, worktree string) string {
	t.Helper()
	dir := filepath.Join(worktree, ".gg")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "PROOF.md")
}

type failingGitChecker struct{}

func (failingGitChecker) IsUncommittedNewFile(context.Context, string, string) (bool, error) {
	return false, errors.New("fatal: not a git repository")
}

func TestDiscoverAndCopySkipsGitChecksForGitDisabledProjects(t *testing.T) {
	// A project running in a plain (non-git) folder has no commits to be
	// "uncommitted" relative to: the git check is skipped entirely, while
	// freshness and run-ID validation still apply.
	root := t.TempDir()
	worktree := t.TempDir()
	if err := os.WriteFile(proofPath(t, worktree), []byte("---\ngg_run_id: run-1\n---\n\n"+proofTextForArtifactTest), 0600); err != nil {
		t.Fatal(err)
	}
	service := NewArtifactService(root, failingGitChecker{})
	if _, err := service.DiscoverAndCopy(context.Background(), worktree, "demo", ArtifactBaseline{}, "run-1", true); err != nil {
		t.Fatalf("git-disabled proof discovery failed: %v", err)
	}
	if _, err := service.DiscoverAndCopy(context.Background(), worktree, "demo", ArtifactBaseline{}, "run-1", false); err == nil {
		t.Fatal("git-enabled project must still enforce the uncommitted check")
	}
}
