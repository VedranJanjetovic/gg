package git

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestProjectSlug(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "normal name", input: "My Demo Project 2", want: "my-demo-project-2"},
		{name: "trims and collapses separators", input: "  release___candidate--one  ", want: "release-candidate-one"},
		{name: "traversal is data not a path", input: "../unsafe/project", want: "unsafe-project"},
		{name: "only unsafe characters", input: "../", wantErr: true},
		{name: "unicode has no unsafe branch characters", input: "Café", want: "caf"},
		{name: "unicode case folding stays non-ASCII", input: "Kelvin İstanbul", want: "elvin-stanbul"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ProjectSlug(test.input)
			if (err != nil) != test.wantErr {
				t.Fatalf("ProjectSlug(%q) error = %v, wantErr %v", test.input, err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("ProjectSlug(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestValidateProjectSlug(t *testing.T) {
	tests := []struct {
		slug    string
		wantErr bool
	}{
		{slug: "project-1"},
		{slug: "../escape", wantErr: true},
		{slug: "/absolute", wantErr: true},
		{slug: "project/name", wantErr: true},
		{slug: "Project", wantErr: true},
		{slug: "project--name", wantErr: true},
		{slug: ".", wantErr: true},
		{slug: "..", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.slug, func(t *testing.T) {
			err := ValidateProjectSlug(test.slug)
			if (err != nil) != test.wantErr {
				t.Fatalf("ValidateProjectSlug(%q) error = %v, wantErr %v", test.slug, err, test.wantErr)
			}
		})
	}
}

func TestProjectNamingFor(t *testing.T) {
	naming, err := ProjectNamingFor(NativeAbs(t, "projects", "developer_ai"), "My Project")
	if err != nil {
		t.Fatalf("ProjectNamingFor returned error: %v", err)
	}

	want := ProjectNaming{
		Slug:         "my-project",
		BranchName:   "gg/my-project",
		WorktreePath: NativeAbs(t, "projects", ".gg-worktrees", "my-project"),
	}
	if naming != want {
		t.Fatalf("ProjectNamingFor returned %#v, want %#v", naming, want)
	}
}

func TestProjectNamingForCollidingNamesIsDeterministic(t *testing.T) {
	first, err := ProjectNamingFor(NativeAbs(t, "projects", "developer_ai"), "Project A")
	if err != nil {
		t.Fatal(err)
	}
	second, err := ProjectNamingFor(NativeAbs(t, "projects", "developer_ai"), "project-a")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("colliding names produced different identities: %#v != %#v", first, second)
	}
}

func TestProjectNamingForRejectsUnsafeAndNestedPaths(t *testing.T) {
	tests := []struct {
		name       string
		root       string
		slug       string
		wantErr    error
		wantWithin bool
	}{
		{name: "unsafe slug", root: NativeAbs(t, "projects", "developer_ai"), slug: "../escape", wantErr: ErrInvalidProjectSlug},
		{name: "main checkout is not parent", root: NativeAbs(t, "projects", "developer_ai"), slug: "safe", wantWithin: false},
		{name: "generated path cannot nest", root: NativeAbs(t, "projects", ".gg-worktrees"), slug: "safe", wantErr: ErrNestedWorktreeRoot},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ProjectNamingForSlug(test.root, test.slug)
			if test.wantErr != nil {
				if err == nil || !errors.Is(err, test.wantErr) {
					t.Fatalf("error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			root, absErr := filepath.Abs(test.root)
			if absErr != nil {
				t.Fatal(absErr)
			}
			if pathWithin(root, got.WorktreePath) != test.wantWithin {
				t.Fatalf("pathWithin(%q, %q) = %v, want %v", root, got.WorktreePath, pathWithin(root, got.WorktreePath), test.wantWithin)
			}
		})
	}
}
