package git

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

const branchPrefix = "gg/"

var (
	ErrInvalidProjectName = errors.New("project name cannot produce a safe slug")
	ErrInvalidProjectSlug = errors.New("project slug must contain only lowercase letters, digits, and single hyphens")
	ErrNestedWorktreeRoot = errors.New("worktree root must not be inside the repository root")
)

// ProjectNaming contains the deterministic git and filesystem names for a project.
type ProjectNaming struct {
	Slug         string
	BranchName   string
	WorktreePath string
}

// ProjectNamingFor returns the canonical names for projectName.
//
// Worktrees are kept in a sibling directory named .gg-worktrees, rather than
// below the repository. This avoids making a worktree a child of the main
// checkout and makes the location independent of the caller's current
// directory. A generated worktree path inside the repository is rejected.
func ProjectNamingFor(repoRoot, projectName string) (ProjectNaming, error) {
	slug, err := ProjectSlug(projectName)
	if err != nil {
		return ProjectNaming{}, err
	}
	return ProjectNamingForSlug(repoRoot, slug)
}

// ProjectNamingForSlug returns deterministic names for an already-normalized slug.
func ProjectNamingForSlug(repoRoot, slug string) (ProjectNaming, error) {
	if err := ValidateProjectSlug(slug); err != nil {
		return ProjectNaming{}, err
	}

	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return ProjectNaming{}, fmt.Errorf("resolve repository root: %w", err)
	}
	root = filepath.Clean(root)
	worktreeRoot := filepath.Join(filepath.Dir(root), ".gg-worktrees")
	worktreePath := filepath.Join(worktreeRoot, slug)
	if pathWithin(root, worktreePath) {
		return ProjectNaming{}, fmt.Errorf("%w: %q is below %q", ErrNestedWorktreeRoot, worktreePath, root)
	}

	return ProjectNaming{
		Slug:         slug,
		BranchName:   branchPrefix + slug,
		WorktreePath: worktreePath,
	}, nil
}

// ProjectSlug converts a display name to the canonical safe project slug.
// ASCII letters and digits are retained; every other run becomes one hyphen.
func ProjectSlug(projectName string) (string, error) {
	var builder strings.Builder
	separator := false
	for _, r := range strings.TrimSpace(projectName) {
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		if r < unicode.MaxASCII && ((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			if separator && builder.Len() > 0 {
				builder.WriteByte('-')
			}
			builder.WriteRune(r)
			separator = false
			continue
		}
		separator = builder.Len() > 0
	}

	slug := builder.String()
	if err := ValidateProjectSlug(slug); err != nil {
		return "", fmt.Errorf("%w: %q", ErrInvalidProjectName, projectName)
	}
	return slug, nil
}

// ValidateProjectSlug verifies the slug contract used by branches and paths.
func ValidateProjectSlug(slug string) error {
	if slug == "" || slug == "." || slug == ".." || strings.HasPrefix(slug, "-") || strings.HasSuffix(slug, "-") || strings.Contains(slug, "--") {
		return ErrInvalidProjectSlug
	}
	for _, r := range slug {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return ErrInvalidProjectSlug
		}
	}
	return nil
}

func pathWithin(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}
