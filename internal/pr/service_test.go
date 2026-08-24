package pr_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/pr"
	"github.com/VedranJanjetovic/gg/internal/proof"
)

type fakeGit struct {
	pushes      []string
	uncommitted bool
}

func (f *fakeGit) PushBranchToRemote(_ context.Context, worktree, remote, branch string) error {
	f.pushes = append(f.pushes, strings.Join([]string{worktree, remote, branch}, "|"))
	return nil
}
func (f *fakeGit) IsUncommittedNewFile(context.Context, string, string) (bool, error) {
	return f.uncommitted, nil
}

type fakeGH struct {
	request []string
	url     string
	err     error
}

func (f *fakeGH) CreatePullRequest(_ context.Context, worktree, title, body, base, head string) (string, error) {
	f.request = []string{worktree, title, body, base, head}
	return f.url, f.err
}

const proofText = "# PROOF\n\n## Validation: unit\n- Status: pass\n- Test location: service_test.go\n- Test name: TestCreate\n- Flow/scenario: create a pull request\n- What it verifies: adapter behavior\n- Proof it passed: `go test ./internal/pr` exited 0\n- Manual run instructions: run the focused test.\n"

func request(worktree string) pr.Request {
	return pr.Request{GitOps: config.GitOpsConfig{ParentBranch: "main", EnablePR: true}, Worktree: worktree, Remote: "origin", Branch: "feature/x", Title: "feat: add PR adapter", Why: "review the change", What: "push and create a pull request", Push: true, ProofRequired: true}
}
func proofWorktree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".gg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gg", "PROOF.md"), []byte(proofText), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCreatePushesAndUsesExactBodyHeadings(t *testing.T) {
	dir := proofWorktree(t)
	git := &fakeGit{uncommitted: true}
	gh := &fakeGH{url: "https://github.com/o/r/pull/1"}
	result, err := pr.NewService(git, gh).Create(context.Background(), request(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || result.URL != gh.url {
		t.Fatalf("result = %#v", result)
	}
	if !reflect.DeepEqual(git.pushes, []string{dir + "|origin|feature/x"}) {
		t.Fatalf("pushes = %#v", git.pushes)
	}
	want := "# Why\nreview the change\n\n# What\npush and create a pull request\n\n# Validation\n- PROOF.md: passed\n- Validations: 1\n- TestCreate: pass\n"
	if result.Body != want || gh.request[2] != want {
		t.Fatalf("body = %q, want %q", result.Body, want)
	}
}
func TestDisabledPRDoesNothing(t *testing.T) {
	git := &fakeGit{}
	gh := &fakeGH{}
	req := request(t.TempDir())
	req.GitOps.EnablePR = false
	got, err := pr.NewService(git, gh).Create(context.Background(), req)
	if err != nil || !got.Skipped || len(git.pushes) != 0 || gh.request != nil {
		t.Fatalf("got %#v, error %v", got, err)
	}
}
func TestRejectsInvalidTitleAndProof(t *testing.T) {
	for _, title := range []string{"Add PR", "unknown: change", "feat:"} {
		t.Run(title, func(t *testing.T) {
			req := request(proofWorktree(t))
			req.Title = title
			_, err := pr.NewService(&fakeGit{uncommitted: true}, &fakeGH{}).Create(context.Background(), req)
			if err == nil || !strings.Contains(err.Error(), "conventional") {
				t.Fatalf("error = %v", err)
			}
		})
	}
	dir := t.TempDir()
	_, err := pr.NewService(&fakeGit{uncommitted: true}, &fakeGH{}).Create(context.Background(), request(dir))
	if err == nil || !strings.Contains(err.Error(), "proof") {
		t.Fatalf("missing proof error = %v", err)
	}
	dir = proofWorktree(t)
	_, err = pr.NewService(&fakeGit{}, &fakeGH{}).Create(context.Background(), request(dir))
	if err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("tracked proof error = %v", err)
	}
}
func TestPropagatesGitHubError(t *testing.T) {
	dir := proofWorktree(t)
	gh := &fakeGH{err: errors.New("bad gh output")}
	_, err := pr.NewService(&fakeGit{uncommitted: true}, gh).Create(context.Background(), request(dir))
	if err == nil || !strings.Contains(err.Error(), "bad gh output") {
		t.Fatalf("error = %v", err)
	}
}

func TestCreateWithoutQAPhaseSkipsProofRequirement(t *testing.T) {
	// With the QA phase disabled no PROOF.md ever exists: the PR is created
	// anyway and its body states why the validation summary is absent.
	dir := t.TempDir()
	gh := &fakeGH{url: "https://github.com/o/r/pull/9"}
	req := request(dir)
	req.ProofRequired = false
	result, err := pr.NewService(&fakeGit{uncommitted: true}, gh).Create(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || result.URL != gh.url {
		t.Fatalf("result = %#v", result)
	}
	if !strings.Contains(result.Body, "PROOF.md: not produced (QA phase disabled)") {
		t.Fatalf("body = %q, want QA-disabled note", result.Body)
	}

	// A proof that DOES exist is still validated and summarized even when
	// not required (for example a rerun after QA was turned off).
	req = request(proofWorktree(t))
	req.ProofRequired = false
	result, err = pr.NewService(&fakeGit{uncommitted: true}, gh).Create(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Body, "- TestCreate: pass") {
		t.Fatalf("body = %q, want existing proof summarized", result.Body)
	}
}

func TestCreateDisclosesSkippedExecutionsDeferredChecksAndQASkip(t *testing.T) {
	dir := t.TempDir()
	gh := &fakeGH{url: "https://github.com/o/r/pull/10"}
	req := request(dir)
	req.ProofRequired = false
	req.ProofWaived = true
	req.SkippedExecutions = []pr.SkippedExecution{
		{Phase: "development", Subphase: "testing", OccurrenceID: "testing-1", Failure: "focused test failed", ExternalIdentity: "ci-run-1"},
		{Phase: "qa", OccurrenceID: "qa-1", Failure: "AWS endpoint unavailable"},
	}
	req.DeferredChecks = []proof.DeferredCheck{{
		TestLocation: "internal/aws_test.go", CheckName: "TestRemoteFlow",
		RemoteOnlyReason: "requires AWS credentials", RepositoryEvidence: "config/aws.go uses AWS_ENDPOINT",
		RunInstructions: "run in CI with AWS secrets", FlowScenario: "call deployed API", ExpectedBehavior: "request persists",
	}}
	result, err := pr.NewService(&fakeGit{uncommitted: true}, gh).Create(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"PROOF.md: waived for the exact confirmed QA skip",
		"# Accepted skipped executions",
		"development/testing (occurrence testing-1): focused test failed",
		"external identity retained: ci-run-1",
		"qa (occurrence qa-1): AWS endpoint unavailable",
		"# Deferred remote validations",
		"TestRemoteFlow (internal/aws_test.go): deferred because requires AWS credentials",
		"config/aws.go uses AWS_ENDPOINT",
	} {
		if !strings.Contains(result.Body, want) || !strings.Contains(gh.request[2], want) {
			t.Errorf("PR body missing %q:\n%s", want, result.Body)
		}
	}
}
