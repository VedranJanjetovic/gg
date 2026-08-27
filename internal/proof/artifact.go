package proof

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/VedranJanjetovic/gg/internal/robustio"
)

// ArtifactName is the worktree-relative path of the QA proof. It lives in
// the ignored artifact directory so it can never be committed.
const ArtifactName = ".gg/PROOF.md"

type Artifact struct {
	Path           string
	Classification Classification
	Proof          Proof
	DeferredChecks []DeferredCheck
}

type WorktreeChecker interface {
	IsUncommittedNewFile(context.Context, string, string) (bool, error)
}

type ArtifactBaseline struct {
	Exists bool
	Digest [sha256.Size]byte
}

type ArtifactService struct {
	Root string
	Git  WorktreeChecker
}

func NewArtifactService(root string, git ...WorktreeChecker) *ArtifactService {
	service := &ArtifactService{Root: root}
	if len(git) > 0 {
		service.Git = git[0]
	}
	return service
}

// Capture records whether the proof existed before the QA process ran.
func (s *ArtifactService) Capture(ctx context.Context, worktree string) (ArtifactBaseline, error) {
	if err := ctx.Err(); err != nil {
		return ArtifactBaseline{}, err
	}
	worktree, err := existingDirectory(worktree)
	if err != nil {
		return ArtifactBaseline{}, fmt.Errorf("validate proof worktree: %w", err)
	}
	_, err = os.Lstat(filepath.Join(worktree, ArtifactName))
	if errors.Is(err, os.ErrNotExist) {
		return ArtifactBaseline{}, nil
	}
	if err != nil {
		return ArtifactBaseline{}, fmt.Errorf("inspect proof artifact: %w", err)
	}
	data, readErr := os.ReadFile(filepath.Join(worktree, ArtifactName))
	if readErr != nil {
		return ArtifactBaseline{}, fmt.Errorf("read proof baseline: %w", readErr)
	}
	return ArtifactBaseline{Exists: true, Digest: sha256.Sum256(data)}, nil
}

// DiscoverAndCopy validates the worktree PROOF.md and installs a byte-preserving
// copy in the durable project artifact directory. gitDisabled marks projects
// whose folder is not a git repository: the uncommitted-file requirement is
// skipped for them (there is no git to commit to); freshness and run-ID
// validation still apply.
func (s *ArtifactService) DiscoverAndCopy(ctx context.Context, worktree, slug string, baseline ArtifactBaseline, runID string, gitDisabled bool) (Artifact, error) {
	if err := ctx.Err(); err != nil {
		return Artifact{}, err
	}
	if s == nil || strings.TrimSpace(s.Root) == "" || strings.TrimSpace(worktree) == "" || strings.TrimSpace(slug) == "" {
		return Artifact{}, errors.New("proof artifact service requires root, worktree, and project slug")
	}
	root, err := existingDirectory(s.Root)
	if err != nil {
		return Artifact{}, fmt.Errorf("validate proof artifact root: %w", err)
	}
	worktree, err = existingDirectory(worktree)
	if err != nil {
		return Artifact{}, fmt.Errorf("validate proof worktree: %w", err)
	}
	if !validSlug(slug) {
		return Artifact{}, fmt.Errorf("invalid project slug %q", slug)
	}
	if s.Git == nil && !gitDisabled {
		return Artifact{}, errors.New("proof artifact service requires a Git worktree checker")
	}
	source := filepath.Join(worktree, ArtifactName)
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Artifact{}, fmt.Errorf("proof artifact %q is missing or not a regular file", source)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return Artifact{}, fmt.Errorf("read proof artifact: %w", err)
	}
	if baseline.Exists && sha256.Sum256(data) == baseline.Digest {
		return Artifact{}, errors.New("proof artifact was not created or changed by this QA attempt")
	}
	if !gitDisabled {
		uncommitted, uncommittedErr := s.Git.IsUncommittedNewFile(ctx, worktree, ArtifactName)
		if uncommittedErr != nil {
			return Artifact{}, fmt.Errorf("verify proof artifact is uncommitted: %w", uncommittedErr)
		}
		if !uncommitted {
			return Artifact{}, errors.New("proof artifact must be a newly produced uncommitted file")
		}
	}
	parsed, err := Parse(data)
	if err != nil {
		return Artifact{}, err
	}
	if strings.TrimSpace(runID) != "" && parsed.RunID != strings.TrimSpace(runID) {
		return Artifact{}, fmt.Errorf("proof artifact run ID %q does not match current attempt %q", parsed.RunID, runID)
	}
	if err := ctx.Err(); err != nil {
		return Artifact{}, err
	}
	dir := filepath.Join(root, ".gg", "projects", slug, "artifacts")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return Artifact{}, fmt.Errorf("create proof artifact directory: %w", err)
	}
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil || !pathWithin(root, resolvedDir) {
		return Artifact{}, errors.New("proof artifact directory escapes configured root")
	}
	dst := filepath.Join(dir, filepath.Base(ArtifactName))
	tmp, err := os.CreateTemp(dir, ".PROOF.md-*")
	if err != nil {
		return Artifact{}, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return Artifact{}, err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return Artifact{}, err
	}
	if err = tmp.Close(); err != nil {
		return Artifact{}, err
	}
	if err = robustio.Rename(tmpName, dst); err != nil {
		return Artifact{}, err
	}
	return Artifact{Path: dst, Classification: parsed.Classify(), Proof: parsed, DeferredChecks: parsed.DeferredChecks()}, nil
}

func existingDirectory(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("path is not a directory")
	}
	return filepath.Clean(resolved), nil
}

func validSlug(slug string) bool {
	if slug == "" || strings.HasPrefix(slug, "-") || strings.HasSuffix(slug, "-") || strings.Contains(slug, "--") {
		return false
	}
	for _, character := range slug {
		if (character < 97 || character > 122) && (character < 48 || character > 57) && character != 45 {
			return false
		}
	}
	return true
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
