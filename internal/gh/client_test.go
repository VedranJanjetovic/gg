package gh_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/gh"
)

type fakeExecutor struct {
	command gh.Command
	output  string
	err     error
}

func (f *fakeExecutor) Execute(_ context.Context, command gh.Command) (string, error) {
	f.command = gh.Command{Dir: command.Dir, Name: command.Name, Args: append([]string(nil), command.Args...)}
	return f.output, f.err
}

type sequenceExecutor struct {
	commands []gh.Command
	outputs  []string
	errs     []error
}

func (f *sequenceExecutor) Execute(_ context.Context, command gh.Command) (string, error) {
	f.commands = append(f.commands, gh.Command{Dir: command.Dir, Name: command.Name, Args: append([]string(nil), command.Args...)})
	i := len(f.commands) - 1
	output, err := "", error(nil)
	if i < len(f.outputs) {
		output = f.outputs[i]
	}
	if i < len(f.errs) {
		err = f.errs[i]
	}
	return output, err
}

func TestCreatePullRequestUsesSafeExactArgvAndParsesURL(t *testing.T) {
	// `gh pr create` has no --json flag; it prints the created PR's URL as
	// the final stdout line.
	executor := &fakeExecutor{output: "Creating pull request for branch/name into main\nhttps://github.com/o/r/pull/2\n"}
	client := gh.NewClient(executor)
	got, err := client.CreatePullRequest(context.Background(), "/repo", "feat: title; $(unsafe)", "# Why\ntext", "main", "branch/name")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://github.com/o/r/pull/2" {
		t.Fatalf("url = %q", got)
	}
	want := []string{"pr", "create", "--title", "feat: title; $(unsafe)", "--body", "# Why\ntext", "--base", "main", "--head", "branch/name"}
	if executor.command.Dir != "/repo" || executor.command.Name != "gh" || strings.Join(executor.command.Args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("command = %#v, want args %#v", executor.command, want)
	}
}

func TestCreatePullRequestRejectsOutputWithoutURL(t *testing.T) {
	for _, output := range []string{"", "no url here", "{}"} {
		t.Run(output, func(t *testing.T) {
			_, err := gh.NewClient(&fakeExecutor{output: output}).CreatePullRequest(context.Background(), "/repo", "feat: title", "body", "main", "branch")
			if err == nil || !strings.Contains(err.Error(), "missing url") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestCreatePullRequestReusesExistingPullRequest(t *testing.T) {
	// Re-running the PR phase must be idempotent: when gh reports the branch
	// already has a PR, its URL is looked up and returned instead of failing.
	executor := &sequenceExecutor{
		outputs: []string{"a pull request for branch \"branch\" into branch \"main\" already exists:\nhttps://github.com/o/r/pull/7", `{"url":"https://github.com/o/r/pull/7"}`},
		errs:    []error{errors.New("exit status 1"), nil},
	}
	got, err := gh.NewClient(executor).CreatePullRequest(context.Background(), "/repo", "feat: title", "body", "main", "branch")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://github.com/o/r/pull/7" {
		t.Fatalf("url = %q", got)
	}
	if len(executor.commands) != 2 || strings.Join(executor.commands[1].Args, " ") != "pr view branch --json url" {
		t.Fatalf("commands = %#v, want create then view", executor.commands)
	}
}

func TestCreatePullRequestSurfacesCreateFailure(t *testing.T) {
	executor := &fakeExecutor{err: errors.New("execute gh command: exit status 1: unknown flag")}
	_, err := gh.NewClient(executor).CreatePullRequest(context.Background(), "/repo", "feat: title", "body", "main", "branch")
	if err == nil || !strings.Contains(err.Error(), "create pull request") {
		t.Fatalf("error = %v", err)
	}
}

func TestTokenFromRemoteURLExtractsEmbeddedCredentialOnly(t *testing.T) {
	cases := []struct {
		name, url, want string
	}{
		{"token as username", "https://github_pat_abc123@github.com/o/r.git", "github_pat_abc123"},
		{"user and token", "https://x-access-token:ghp_secret@github.com/o/r.git", "ghp_secret"},
		{"escaped token", "https://user:p%40ss@github.com/o/r.git", "p@ss"},
		{"no credential", "https://github.com/o/r.git", ""},
		{"ssh remote", "git@github.com:o/r.git", ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := gh.TokenFromRemoteURL(tc.url); got != tc.want {
				t.Fatalf("token = %q, want %q", got, tc.want)
			}
		})
	}
}
