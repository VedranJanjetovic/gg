package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/update"
	"github.com/VedranJanjetovic/gg/internal/version"
)

type fakeUpdateService struct {
	result update.Result
	err    error
	called bool
}

func (f *fakeUpdateService) Update(context.Context) (update.Result, error) {
	f.called = true
	return f.result, f.err
}

func TestUpdateCommandUsesInjectedServiceAndDeterministicOutput(t *testing.T) {
	service := &fakeUpdateService{result: update.Result{Current: "v1.0.0", Latest: "v1.1.0", Action: "manual"}}
	app := New(WithVersion(version.Metadata{Version: "v1.0.0"}), WithUpdateService(service))
	var stdout, stderr bytes.Buffer
	if code := app.Run(context.Background(), []string{"update"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !service.called || !strings.Contains(stdout.String(), "Download the gg-v1.1.0 release") {
		t.Fatalf("called=%v stdout=%q", service.called, stdout.String())
	}
}

func TestUpdateCommandReturnsServiceErrors(t *testing.T) {
	service := &fakeUpdateService{err: errors.New("release source unavailable")}
	app := New(WithUpdateService(service))
	var stdout, stderr bytes.Buffer
	if code := app.Run(context.Background(), []string{"update"}, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "release source unavailable") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}
