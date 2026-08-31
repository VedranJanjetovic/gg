package update

import (
	"context"
	"testing"
)

func TestAvailableReportsNewerReleaseWithoutInstalling(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		{name: "newer release", current: "gg-v1.2.3", latest: "gg-v1.3.0", want: true},
		{name: "same release", current: "gg-v1.3.0", latest: "gg-v1.3.0", want: false},
		{name: "older release", current: "gg-v1.4.0", latest: "gg-v1.3.0", want: false},
		{name: "unrecognized current version", current: "dev", latest: "gg-v1.3.0", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projects := &fakeProjectStatuses{}
			installer := &fakeInstaller{}
			service := NewServiceWithDependencies(
				func() string { return test.current },
				&fakeLookup{latest: test.latest},
				WithProjectStatusLister(projects), WithInstaller(installer),
			)

			got, err := service.Available(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("available = %t, want %t", got, test.want)
			}
			if installer.calls != 0 || projects.calls != 0 {
				t.Fatalf("check installed or gated: installerCalls=%d projectCalls=%d", installer.calls, projects.calls)
			}
		})
	}
}

func TestAvailableDoesNotLookupDevelopmentBuild(t *testing.T) {
	lookup := &fakeLookup{latest: "gg-v1.3.0"}
	service := NewServiceWithDependencies(func() string { return "dev" }, lookup)

	if _, err := service.Available(context.Background()); err != nil {
		t.Fatal(err)
	}
	if lookup.called {
		t.Fatal("release lookup ran for an unrecognized current version")
	}
}
