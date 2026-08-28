package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// skillsSnapshotLocator is the expression install.sh uses to find the skill
// assets inside an extracted GitHub source archive. The archive nests the whole
// tree under a single gg-<ref>/ directory, so `skills` sits at depth 2 from the
// extraction root only while it is a direct child of the repository root.
const skillsSnapshotLocator = `-mindepth 2 -maxdepth 2 -type d -name skills`

// The skills tree must stay a direct child of the repository root, holding these
// directories. install.sh locates it in a fetched source snapshot with a fixed
// depth expression, and install.sh:213 swallows the failure — so moving skills/
// deeper would freeze agent skills silently for every user who updates, with no
// error anywhere. Both installers fetch the snapshot the same way, so this test
// covers install.ps1's Install-Skills too.
func TestSkillAssetsStayADirectChildOfTheRepositoryRoot(t *testing.T) {
	root := moduleRoot(t)
	skills := filepath.Join(root, "skills")
	info, err := os.Stat(skills)
	if err != nil || !info.IsDir() {
		t.Fatalf("skills must be a directory at the repository root %q: err=%v", root, err)
	}
	for _, name := range []string{"canonical", "claude", "codex", "core"} {
		child := filepath.Join(skills, name)
		if childInfo, statErr := os.Stat(child); statErr != nil || !childInfo.IsDir() {
			t.Fatalf("skills/%s must be a directory: err=%v", name, statErr)
		}
	}
}

// The guard above is only meaningful while install.sh still locates the skill
// assets by depth. If that expression changes, the layout constraint changes
// with it and both sides must be reconsidered together.
func TestInstallShStillLocatesSkillAssetsByDepth(t *testing.T) {
	script, err := os.ReadFile(filepath.Join(moduleRoot(t), "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), skillsSnapshotLocator) {
		t.Fatalf("install.sh no longer contains %q; revisit the skills layout guard", skillsSnapshotLocator)
	}
}
