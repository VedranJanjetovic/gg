package version

import "testing"

func TestCurrentUsesDevelopmentFallbacks(t *testing.T) {
	if got, want := (Metadata{Version: Version, Commit: Commit, Date: Date}), (Metadata{Version: "dev", Commit: "unknown", Date: "unknown"}); got != want {
		t.Fatalf("Current() = %#v, want %#v", got, want)
	}
}

func TestMetadataStringIsDeterministic(t *testing.T) {
	got := (Metadata{Version: "v1.2.3", Commit: "abc123", Date: "2026-08-04T12:00:00Z"}).String()
	want := "gg version v1.2.3 (commit abc123, build date 2026-08-04T12:00:00Z)"
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestCurrentReadsLinkerStyleValues(t *testing.T) {
	oldVersion, oldCommit, oldDate := Version, Commit, Date
	t.Cleanup(func() { Version, Commit, Date = oldVersion, oldCommit, oldDate })
	Version, Commit, Date = "v9.8.7", "deadbeef", "2026-08-04T00:00:00Z"

	if got, want := Current(), (Metadata{Version: Version, Commit: Commit, Date: Date}); got != want {
		t.Fatalf("Current() = %#v, want %#v", got, want)
	}
}
