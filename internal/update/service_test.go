package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeLookup struct {
	latest string
	err    error
	called bool
}

func (f *fakeLookup) LatestRelease(context.Context) (string, error) {
	f.called = true
	return f.latest, f.err
}

func TestUpdateCurrentReleaseDoesNotExecute(t *testing.T) {
	lookup := &fakeLookup{latest: "v1.2.3"}
	s := NewServiceWithDependencies(func() string { return "v1.2.3" }, lookup)
	got, err := s.Update(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != "current" {
		t.Fatalf("result=%#v", got)
	}
}

func TestUpdateNewReleasePrintsManualAction(t *testing.T) {
	s := NewServiceWithDependencies(func() string { return "v1.2.3" }, &fakeLookup{latest: "v1.3.0"})
	got, err := s.Update(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != "manual" || !strings.Contains(ManualInstructions(got.Latest), "Download the gg-v1.3.0 release") {
		t.Fatalf("result=%#v", got)
	}
}

func TestUpdateAcceptsCanonicalReleaseTagThroughHTTPPath(t *testing.T) {
	var called bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodGet {
			t.Errorf("method=%s, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"gg-v1.2.3"}`))
	}))
	defer ts.Close()

	s := NewServiceWithDependencies(
		func() string { return "1.2.2" },
		NewHTTPReleaseLookup(ts.Client(), ts.URL),
	)
	got, err := s.Update(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("release lookup did not reach HTTP fixture")
	}
	if got.Action != "manual" || got.Latest != "gg-v1.2.3" {
		t.Fatalf("result=%#v", got)
	}
	if instructions := ManualInstructions(got.Latest); !strings.Contains(instructions, "releases/tag/gg-v1.2.3") {
		t.Fatalf("instructions=%q", instructions)
	}
}

func TestUpdateDoesNotLookupDevelopmentBuild(t *testing.T) {
	lookup := &fakeLookup{latest: "v9.9.9"}
	s := NewServiceWithDependencies(func() string { return "dev" }, lookup)
	got, err := s.Update(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != "unrecognized" || lookup.called {
		t.Fatalf("result=%#v lookup=%v", got, lookup.called)
	}
}

func TestUpdateCancellationAfterLookup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	lookup := lookupFunc(func(context.Context) (string, error) { cancel(); return "v1.3.0", nil })
	s := NewServiceWithDependencies(func() string { return "v1.2.3" }, lookup)
	_, err := s.Update(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

type lookupFunc func(context.Context) (string, error)

func (f lookupFunc) LatestRelease(ctx context.Context) (string, error) { return f(ctx) }

func TestUpdateCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := NewServiceWithDependencies(func() string { return "v1.2.3" }, &fakeLookup{latest: "v1.3.0"})
	_, err := s.Update(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestHTTPReleaseLookup(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		want    string
		wantErr string
	}{
		{name: "tag", status: http.StatusOK, body: `{"tag_name":"v2.0.0"}`, want: "v2.0.0"},
		{name: "malformed", status: http.StatusOK, body: `{`, wantErr: "decode release response"},
		{name: "not found", status: http.StatusNotFound, body: `no`, wantErr: "HTTP 404"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer ts.Close()
			got, err := NewHTTPReleaseLookup(ts.Client(), ts.URL).LatestRelease(context.Background())
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err=%v", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got=%q err=%v", got, err)
			}
		})
	}
}

func TestHTTPReleaseLookupNetworkFailure(t *testing.T) {
	lookup := NewHTTPReleaseLookup(nil, "http://127.0.0.1:1/releases/latest")
	if _, err := lookup.LatestRelease(context.Background()); err == nil {
		t.Fatal("expected network error")
	}
}

func TestHTTPReleaseLookupCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-release }))
	defer ts.Close()
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := NewHTTPReleaseLookup(ts.Client(), ts.URL).LatestRelease(ctx); result <- err }()
	<-started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

type fakeProjectStatuses struct {
	projects []ProjectStatus
	err      error
	calls    int
}

func (f *fakeProjectStatuses) List(context.Context) ([]ProjectStatus, error) {
	f.calls++
	return f.projects, f.err
}

type fakeInstaller struct {
	version string
	err     error
	calls   int
}

func (f *fakeInstaller) Install(_ context.Context, version string) error {
	f.calls++
	f.version = version
	return f.err
}

func TestUpdateGatesNewReleaseAndInvokesInstallerOnceWhenNoProjectIsRunning(t *testing.T) {
	projects := &fakeProjectStatuses{projects: []ProjectStatus{{Status: "stopped"}, {Status: "pending"}}}
	installer := &fakeInstaller{}
	service := NewServiceWithDependencies(
		func() string { return "gg-v1.2.3" },
		&fakeLookup{latest: "gg-v1.3.0"},
		WithProjectStatusLister(projects), WithInstaller(installer),
	)

	got, err := service.Update(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != "installed" || projects.calls != 1 || installer.calls != 1 {
		t.Fatalf("result=%#v projectCalls=%d installerCalls=%d", got, projects.calls, installer.calls)
	}
	if installer.version != "1.3.0" {
		t.Fatalf("installer version=%q", installer.version)
	}
}

func TestUpdateBlocksBeforeInstallerWhenProjectExactlyRunning(t *testing.T) {
	projects := &fakeProjectStatuses{projects: []ProjectStatus{{Status: "stopped"}, {Status: "running"}, {Status: "unknown"}}}
	installer := &fakeInstaller{}
	service := NewServiceWithDependencies(
		func() string { return "1.2.3" }, &fakeLookup{latest: "v1.3.0"},
		WithProjectStatusLister(projects), WithInstaller(installer),
	)

	got, err := service.Update(context.Background())
	if err != nil || got.Action != "blocked" || !strings.Contains(got.Message, "gg stop-all") {
		t.Fatalf("result=%#v err=%v", got, err)
	}
	if installer.calls != 0 {
		t.Fatalf("installer calls=%d, want 0", installer.calls)
	}
}

func TestUpdateInstallerErrorIsReturned(t *testing.T) {
	installErr := errors.New("installer failed")
	installer := &fakeInstaller{err: installErr}
	service := NewServiceWithDependencies(
		func() string { return "1.2.3" }, &fakeLookup{latest: "v1.3.0"},
		WithProjectStatusLister(&fakeProjectStatuses{}), WithInstaller(installer),
	)

	got, err := service.Update(context.Background())
	if got.Action != "" || !errors.Is(err, installErr) || installer.calls != 1 {
		t.Fatalf("result=%#v err=%v calls=%d", got, err, installer.calls)
	}
}

func TestUpdateCancellationBeforeInstaller(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	installer := &fakeInstaller{}
	service := NewServiceWithDependencies(
		func() string { return "1.2.3" }, &fakeLookup{latest: "v1.3.0"},
		WithProjectStatusLister(&fakeProjectStatuses{}), WithInstaller(installer),
	)

	_, err := service.Update(ctx)
	if !errors.Is(err, context.Canceled) || installer.calls != 0 {
		t.Fatalf("err=%v installer calls=%d", err, installer.calls)
	}
}
