package cli_test

import (
	"context"
	"github.com/VedranJanjetovic/gg/internal/cli"
	"github.com/VedranJanjetovic/gg/internal/orchestrator"
	"github.com/VedranJanjetovic/gg/internal/state"
	"strings"
	"testing"
)

type allResumeFake struct{}

func (allResumeFake) ResumeAll(context.Context, orchestrator.ResumeAllRequest) ([]orchestrator.ResumeResult, error) {
	return []orchestrator.ResumeResult{{ProjectSlug: "one", Kind: state.RerunResume}}, nil
}

func TestResumeWithoutSelectorUsesAllProjectCoordinator(t *testing.T) {
	var stdout, stderr strings.Builder
	app := cli.New(cli.WithResumeCoordinator(allResumeFake{}))
	if code := app.Run(context.Background(), []string{"resume"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if got := stdout.String(); got != "Resumed 1 project(s).\n" {
		t.Fatalf("stdout=%q", got)
	}
}
