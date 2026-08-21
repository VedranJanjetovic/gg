package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/VedranJanjetovic/gg/internal/state"
	"github.com/VedranJanjetovic/gg/internal/tui"
)

func TestParseQuestionsToleratesProseAndKeepsEveryQuestion(t *testing.T) {
	output := "Here you go:\n```json\n{\"questions\": [\"q1\", \" \", \"q2\", \"q3\", \"q4\", \"q5\", \"q6\"]}\n```"
	got := parseQuestions(output)
	want := []string{"q1", "q2", "q3", "q4", "q5", "q6"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseQuestions = %#v, want %#v", got, want)
	}
	if len(parseQuestions("no json here")) != 0 || len(parseQuestions("{\"questions\": []}")) != 0 {
		t.Fatal("empty and invalid outputs must yield no questions")
	}
}

// fakeInterviewService satisfies lifecycleService through an embedded nil
// interface (only SetInterview is exercised by the interview loop) and
// records every persisted step.
type fakeInterviewService struct {
	lifecycleService
	project  state.ProjectState
	saved    []state.InterviewState
	criteria [][]string
}

func (s *fakeInterviewService) SetInterview(_ context.Context, slug string, interview state.InterviewState, appendCriteria []string) (state.ProjectState, error) {
	if slug != s.project.Slug {
		return state.ProjectState{}, os.ErrNotExist
	}
	copied := interview
	s.saved = append(s.saved, copied)
	s.criteria = append(s.criteria, append([]string(nil), appendCriteria...))
	s.project.Interview = &copied
	s.project.AcceptanceCriteria = append(s.project.AcceptanceCriteria, appendCriteria...)
	return s.project, nil
}

func interviewProject() state.ProjectState {
	return state.ProjectState{
		Slug: "demo", Name: "Demo", OriginalGoal: "supermario",
		AcceptanceCriteria: []string{"supermario"},
		Status:             state.StatusPending,
		WorktreePath:       "/worktrees/demo",
		Interview:          &state.InterviewState{},
	}
}

func interviewApp(t *testing.T, generator QuestionGenerator, asker QuestionAsker) *App {
	t.Helper()
	return New(
		WithInput(os.Stdin),
		WithWorkingDirectory(func() (string, error) { return "/repo", nil }),
		WithConfigStore(configuredMemoryStore()),
		WithQuestionGenerator(generator),
		WithQuestionAsker(asker),
	)
}

func TestGroomingInterviewLoopsAndPersistsEveryStep(t *testing.T) {
	var prompts []string
	generator := func(_ context.Context, settings config.AgentSettings, dir, prompt string) (string, error) {
		if settings.Agent != config.AgentClaude || dir != "/worktrees/demo" {
			t.Fatalf("generator settings/dir = %#v %q", settings, dir)
		}
		prompts = append(prompts, prompt)
		switch len(prompts) {
		case 1:
			return `{"questions": ["Which framework?"]}`, nil
		case 2:
			return `{"questions": ["Need persistence?"]}`, nil
		default:
			return `{"questions": []}`, nil
		}
	}
	var asked [][]string
	asker := func(_ context.Context, questions []string, _ io.Reader, _ io.Writer) ([]string, int, error) {
		asked = append(asked, questions)
		if len(asked) == 1 {
			return []string{"Plain canvas"}, len(questions), nil
		}
		return []string{"Yes, localStorage"}, len(questions), nil
	}
	app := interviewApp(t, generator, asker)
	service := &fakeInterviewService{project: interviewProject()}
	project := service.project

	proceed, notice := app.runGroomingInterview(context.Background(), service, &project)
	if !proceed {
		t.Fatal("completed interview must allow the pipeline to start")
	}
	if !strings.Contains(notice, "No open questions") {
		t.Fatalf("notice = %q, want the all-clear message for the attach screen", notice)
	}
	if len(prompts) != 3 || len(asked) != 2 {
		t.Fatalf("generator rounds = %d, interview rounds = %d; want 3 and 2", len(prompts), len(asked))
	}
	if !strings.Contains(prompts[1], "Plain canvas") || !strings.Contains(prompts[2], "localStorage") {
		t.Fatalf("re-check prompts do not carry earlier answers: %q", prompts[1:])
	}
	want := []string{
		"supermario",
		"Clarification — Q: Which framework? A: Plain canvas",
		"Clarification — Q: Need persistence? A: Yes, localStorage",
	}
	if !reflect.DeepEqual(project.AcceptanceCriteria, want) {
		t.Fatalf("criteria = %#v, want %#v", project.AcceptanceCriteria, want)
	}
	final := service.saved[len(service.saved)-1]
	if !final.Done || len(final.Pending) != 0 || len(final.Clarifications) != 2 {
		t.Fatalf("final persisted interview = %#v, want done with both clarifications", final)
	}
	// Pending questions are persisted before the user is asked, so an exit at
	// any point can re-enter the interview.
	if first := service.saved[0]; first.Done || !reflect.DeepEqual(first.Pending, []string{"Which framework?"}) {
		t.Fatalf("first persisted step = %#v, want pending questions saved before asking", first)
	}
}

func TestGroomingInterviewEarlyExitKeepsPendingQuestions(t *testing.T) {
	generator := func(context.Context, config.AgentSettings, string, string) (string, error) {
		return `{"questions": ["Anything?"]}`, nil
	}
	asker := func(context.Context, []string, io.Reader, io.Writer) ([]string, int, error) {
		return nil, 0, tui.ErrPickerCancelled
	}
	app := interviewApp(t, generator, asker)
	service := &fakeInterviewService{project: interviewProject()}
	project := service.project

	proceed, _ := app.runGroomingInterview(context.Background(), service, &project)
	if proceed {
		t.Fatal("cancelled interview must not start the pipeline")
	}
	saved := service.saved[len(service.saved)-1]
	if saved.Done || !reflect.DeepEqual(saved.Pending, []string{"Anything?"}) {
		t.Fatalf("persisted interview = %#v, want pending question kept for re-entry", saved)
	}
}

func TestGroomingInterviewReentersWithPersistedPendingQuestions(t *testing.T) {
	generator := func(context.Context, config.AgentSettings, string, string) (string, error) {
		return `{"questions": []}`, nil
	}
	var asked [][]string
	asker := func(_ context.Context, questions []string, _ io.Reader, _ io.Writer) ([]string, int, error) {
		asked = append(asked, questions)
		return []string{"Answered later"}, len(questions), nil
	}
	app := interviewApp(t, generator, asker)
	service := &fakeInterviewService{project: interviewProject()}
	service.project.Interview = &state.InterviewState{Pending: []string{"Left over?"}, Rounds: 1}
	project := service.project

	proceed, _ := app.runGroomingInterview(context.Background(), service, &project)
	if !proceed {
		t.Fatal("re-entered interview must finish and allow the pipeline")
	}
	if len(asked) != 1 || !reflect.DeepEqual(asked[0], []string{"Left over?"}) {
		t.Fatalf("asked = %#v, want the persisted pending question first", asked)
	}
	if got := project.AcceptanceCriteria; len(got) != 2 || !strings.Contains(got[1], "Answered later") {
		t.Fatalf("criteria = %#v, want the late answer folded in", got)
	}
}

func TestGroomingInterviewAllSkippedEndsInterview(t *testing.T) {
	generator := func(context.Context, config.AgentSettings, string, string) (string, error) {
		return `{"questions": ["Anything?"]}`, nil
	}
	rounds := 0
	asker := func(context.Context, []string, io.Reader, io.Writer) ([]string, int, error) {
		rounds++
		return []string{""}, 1, nil // deliberately skipped
	}
	app := interviewApp(t, generator, asker)
	service := &fakeInterviewService{project: interviewProject()}
	project := service.project

	proceed, _ := app.runGroomingInterview(context.Background(), service, &project)
	if !proceed || rounds != 1 {
		t.Fatalf("proceed = %v rounds = %d; skipping every question must end the interview", proceed, rounds)
	}
	saved := service.saved[len(service.saved)-1]
	if !saved.Done || len(project.AcceptanceCriteria) != 1 {
		t.Fatalf("interview = %#v criteria = %#v", saved, project.AcceptanceCriteria)
	}
}

func TestGroomingInterviewGeneratorFailureParksInterview(t *testing.T) {
	// A broken question generator (bad model, dead agent CLI) must not start
	// a pipeline the same agent will fail: the interview stays pending and
	// the reason is surfaced for a retry via g.
	app := interviewApp(t,
		func(context.Context, config.AgentSettings, string, string) (string, error) {
			return "", context.DeadlineExceeded
		},
		func(context.Context, []string, io.Reader, io.Writer) ([]string, int, error) {
			t.Fatal("asker called after generator failure")
			return nil, 0, nil
		},
	)
	service := &fakeInterviewService{project: interviewProject()}
	project := service.project

	proceed, notice := app.runGroomingInterview(context.Background(), service, &project)
	if proceed || !strings.Contains(notice, "Grooming check failed") {
		t.Fatalf("proceed = %v notice = %q", proceed, notice)
	}
	if project.Interview == nil || project.Interview.Done {
		t.Fatalf("interview must stay pending, got %#v", project.Interview)
	}
}

func TestInterviewPendingRequiresPendingStatusAndUnfinishedInterview(t *testing.T) {
	project := interviewProject()
	if !interviewPending(project) {
		t.Fatal("fresh project with an interview must be pending")
	}
	project.Status = state.StatusRunning
	if interviewPending(project) {
		t.Fatal("running project must not re-enter the interview")
	}
	project.Status = state.StatusFailed
	if !interviewPending(project) {
		t.Fatal("failed project with an unfinished interview must offer it")
	}
	project.Status = state.StatusFinished
	if interviewPending(project) {
		t.Fatal("finished project must not re-enter the interview")
	}
	project.Status = state.StatusPending
	project.Interview.Done = true
	if interviewPending(project) {
		t.Fatal("finished interview must not re-run")
	}
	project.Interview = nil
	if interviewPending(project) {
		t.Fatal("legacy project without interview must not owe one")
	}
}

func TestGroomingInterviewPauseKeepsUnseenQuestionsPending(t *testing.T) {
	generator := func(context.Context, config.AgentSettings, string, string) (string, error) {
		return `{"questions": ["First?", "Second?", "Third?"]}`, nil
	}
	// The user answers the first question, then pauses with Esc: progressed
	// reports 1, and the remaining questions must stay pending — this must
	// NOT count as "skipped everything" (which would end the interview and
	// let the pipeline start past the questions).
	asker := func(_ context.Context, questions []string, _ io.Reader, _ io.Writer) ([]string, int, error) {
		return []string{"Answer one", "", ""}, 1, nil
	}
	app := interviewApp(t, generator, asker)
	service := &fakeInterviewService{project: interviewProject()}
	project := service.project

	proceed, _ := app.runGroomingInterview(context.Background(), service, &project)
	if proceed {
		t.Fatal("paused interview must not start the pipeline")
	}
	saved := service.saved[len(service.saved)-1]
	if saved.Done {
		t.Fatalf("paused interview marked done: %#v", saved)
	}
	if want := []string{"Second?", "Third?"}; !reflect.DeepEqual(saved.Pending, want) {
		t.Fatalf("pending = %#v, want the unseen questions %#v", saved.Pending, want)
	}
	if len(saved.Clarifications) != 1 || saved.Clarifications[0].Answer != "Answer one" {
		t.Fatalf("clarifications = %#v, want the answered question folded in", saved.Clarifications)
	}
	if got := project.AcceptanceCriteria; len(got) != 2 || !strings.Contains(got[1], "Answer one") {
		t.Fatalf("criteria = %#v", got)
	}
}

func TestParseQuestionsUnwrapsClaudeJSONEnvelope(t *testing.T) {
	output := `{"type":"result","result":"{\"questions\": [\"Real question?\"]}","usage":{"output_tokens":5}}`
	if got := parseQuestions(output); len(got) != 1 || got[0] != "Real question?" {
		t.Fatalf("parseQuestions = %#v, want the unwrapped question", got)
	}
}

func TestClarificationPromptAsksForDecisionLevelArchitecture(t *testing.T) {
	prompt := clarificationPrompt("backend to process financial transactions", nil)
	for _, want := range []string{
		"key architectural decisions",
		"state machine",
		"decision level with 2-3 briefly named options",
		"never descend into implementation detail",
		"skip architecture questions entirely for projects too simple",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("clarification prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestInterviewSessionIngestsRecordedAnswersAndCompletesInterview(t *testing.T) {
	worktree := t.TempDir()
	var sessionPrompt string
	session := func(_ context.Context, settings config.AgentSettings, dir, prompt string) error {
		if dir != worktree {
			t.Fatalf("session dir = %q, want project folder", dir)
		}
		sessionPrompt = prompt
		if err := os.MkdirAll(filepath.Join(dir, ".gg"), 0o755); err != nil {
			t.Fatal(err)
		}
		answers := "# Interview answers\n\n## Q: State machine or CRUD?\nA: State machine — auditability wins.\nUse explicit transitions.\n\n## Q: Which database?\nA: PostgreSQL\n\n<!-- interview complete -->\n"
		return os.WriteFile(filepath.Join(dir, ".gg", "interview-answers.md"), []byte(answers), 0o644)
	}
	app := New(
		WithInput(os.Stdin),
		WithWorkingDirectory(func() (string, error) { return "/repo", nil }),
		WithConfigStore(configuredMemoryStore()),
		WithInterviewSession(session),
	)
	service := &fakeInterviewService{project: interviewProject()}
	service.project.WorktreePath = worktree
	service.project.Interview = &state.InterviewState{Pending: []string{"Left over?"}}
	project := service.project

	proceed, notice := app.runGroomingInterview(context.Background(), service, &project)
	if !proceed || !strings.Contains(notice, "2 clarification(s)") {
		t.Fatalf("proceed = %v notice = %q", proceed, notice)
	}
	for _, want := range []string{"supermario", "Left over?", "architectural choices", "interview-answers.md"} {
		if !strings.Contains(sessionPrompt, want) {
			t.Fatalf("session prompt missing %q:\n%s", want, sessionPrompt)
		}
	}
	saved := service.saved[len(service.saved)-1]
	if !saved.Done || len(saved.Clarifications) != 2 || len(saved.Pending) != 0 {
		t.Fatalf("persisted interview = %#v, want done with two clarifications", saved)
	}
	if got := saved.Clarifications[0].Answer; !strings.Contains(got, "State machine") || !strings.Contains(got, "explicit transitions") {
		t.Fatalf("multi-line answer not preserved: %q", got)
	}
	if !strings.Contains(project.AcceptanceCriteria[len(project.AcceptanceCriteria)-1], "PostgreSQL") {
		t.Fatalf("criteria missing session answers: %#v", project.AcceptanceCriteria)
	}
	if _, err := os.Stat(filepath.Join(worktree, ".gg", "interview-answers.md")); !os.IsNotExist(err) {
		t.Fatal("ingested answers file must be removed")
	}
}

func TestInterviewSessionWithoutAnswersKeepsInterviewPending(t *testing.T) {
	worktree := t.TempDir()
	session := func(context.Context, config.AgentSettings, string, string) error { return nil }
	app := New(
		WithInput(os.Stdin),
		WithWorkingDirectory(func() (string, error) { return "/repo", nil }),
		WithConfigStore(configuredMemoryStore()),
		WithInterviewSession(session),
	)
	service := &fakeInterviewService{project: interviewProject()}
	service.project.WorktreePath = worktree
	project := service.project

	proceed, notice := app.runGroomingInterview(context.Background(), service, &project)
	if proceed || !strings.Contains(notice, "without recorded answers") {
		t.Fatalf("proceed = %v notice = %q, want pending interview", proceed, notice)
	}
	if project.Interview == nil || project.Interview.Done {
		t.Fatal("interview must stay pending after an unfinished session")
	}
}

func TestInterviewSessionLaunchFailureFallsBackToQuestionList(t *testing.T) {
	session := func(context.Context, config.AgentSettings, string, string) error {
		return errors.New("claude CLI not found")
	}
	generator := func(context.Context, config.AgentSettings, string, string) (string, error) {
		return `{"questions": []}`, nil
	}
	asked := 0
	asker := func(_ context.Context, questions []string, _ io.Reader, _ io.Writer) ([]string, int, error) {
		asked++
		return []string{"fallback answer"}, len(questions), nil
	}
	app := interviewApp(t, generator, asker)
	WithInterviewSession(session)(app)
	service := &fakeInterviewService{project: interviewProject()}
	service.project.Interview = &state.InterviewState{Pending: []string{"Left over?"}}
	project := service.project

	proceed, _ := app.runGroomingInterview(context.Background(), service, &project)
	if !proceed || asked != 1 {
		t.Fatalf("proceed = %v asked = %d, want fallback question flow", proceed, asked)
	}
}

func TestInterviewSessionPartialProgressPersistsAndResumesWithoutReAsking(t *testing.T) {
	worktree := t.TempDir()
	sessions := 0
	var secondPrompt string
	session := func(_ context.Context, _ config.AgentSettings, dir, prompt string) error {
		sessions++
		if err := os.MkdirAll(filepath.Join(dir, ".gg"), 0o755); err != nil {
			t.Fatal(err)
		}
		if sessions == 1 {
			// Interrupted mid-conversation: one decision appended, no
			// completion marker.
			return os.WriteFile(filepath.Join(dir, ".gg", "interview-answers.md"), []byte("# Interview answers\n\n## Q: Which database?\nA: PostgreSQL\n"), 0o644)
		}
		secondPrompt = prompt
		// The re-entered session re-records an old decision plus a new one
		// and finishes.
		return os.WriteFile(filepath.Join(dir, ".gg", "interview-answers.md"), []byte("## Q: Which database?\nA: PostgreSQL\n\n## Q: Auth method?\nA: OAuth\n\n<!-- interview complete -->\n"), 0o644)
	}
	app := New(
		WithInput(os.Stdin),
		WithWorkingDirectory(func() (string, error) { return "/repo", nil }),
		WithConfigStore(configuredMemoryStore()),
		WithInterviewSession(session),
	)
	service := &fakeInterviewService{project: interviewProject()}
	service.project.WorktreePath = worktree
	project := service.project

	proceed, notice := app.runGroomingInterview(context.Background(), service, &project)
	if proceed || !strings.Contains(notice, "progress saved (1 answer(s))") {
		t.Fatalf("first session: proceed = %v notice = %q, want persisted partial progress", proceed, notice)
	}
	if project.Interview.Done || len(project.Interview.Clarifications) != 1 {
		t.Fatalf("interview after interruption = %#v, want one clarification and not done", project.Interview)
	}

	proceed, notice = app.runGroomingInterview(context.Background(), service, &project)
	if !proceed || !strings.Contains(notice, "1 clarification(s)") {
		t.Fatalf("second session: proceed = %v notice = %q, want completion with only the new answer counted", proceed, notice)
	}
	if !strings.Contains(secondPrompt, "Which database?") || !strings.Contains(secondPrompt, "do not re-ask") {
		t.Fatalf("re-entered session brief missing prior answers:\n%s", secondPrompt)
	}
	saved := service.saved[len(service.saved)-1]
	if !saved.Done || len(saved.Clarifications) != 2 {
		t.Fatalf("final interview = %#v, want done with exactly two deduplicated clarifications", saved)
	}
}

func TestInterviewSessionCommandPreauthorizesAnswerFileWrites(t *testing.T) {
	name, args := interviewSessionCommand(config.AgentSettings{Agent: config.AgentClaude, Model: "opus"}, "brief")
	if name != "claude" || !reflect.DeepEqual(args, []string{"--permission-mode", "acceptEdits", "--model", "opus", "brief"}) {
		t.Fatalf("claude invocation = %s %v", name, args)
	}
	name, args = interviewSessionCommand(config.AgentSettings{Agent: config.AgentCodex}, "brief")
	if name != "codex" || !reflect.DeepEqual(args, []string{"--sandbox", "workspace-write", "--ask-for-approval", "on-request", "brief"}) {
		t.Fatalf("codex invocation = %s %v", name, args)
	}
}

func TestCompletedInterviewOnStoppedProjectAutoResumesPipeline(t *testing.T) {
	// A feedback loop stops the project and re-opens the interview; once the
	// interview completes, the pipeline must resume without a manual r.
	root := t.TempDir()
	store, err := state.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service := state.NewLifecycleService(store, nil, store.Locker())
	project, err := state.NewProjectState(state.ProjectState{
		Name: "Demo", Slug: "demo", OriginalGoal: "goal",
		AcceptanceCriteria: []string{"criterion"},
		WorktreePath:       t.TempDir(), BranchName: "gg/demo",
		CurrentPhase: "pipeline", Status: state.StatusStopped,
		Interview:      &state.InterviewState{Done: true},
		PipelineConfig: state.PipelineConfigSnapshot{SchemaVersion: 1, Data: []byte(`{}`)},
		CreatedAt:      time.Now().UTC(), UpdatedAt: time.Now().UTC(), StatusChangedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	var spawned [][]string
	spawner := func(_ context.Context, _ string, args []string, _ string) error {
		spawned = append(spawned, args)
		if _, err := service.Transition(context.Background(), "demo", state.StatusRunning, "acceptance_criteria", "", nil); err != nil {
			t.Fatal(err)
		}
		return nil
	}
	app := New(WithLifecycleService(service), WithRootResolver(fixedRoot{root: root}), WithRunSpawner(spawner))
	app.detachedStartTimeout = time.Second
	app.detachedPollInterval = time.Millisecond

	loaded, err := service.Load(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	notice := app.autoResumeAfterInterview(context.Background(), service, "demo", "", &loaded)
	if len(spawned) != 1 || strings.Join(spawned[0], " ") != "resume demo" {
		t.Fatalf("spawned = %v, want a detached resume", spawned)
	}
	if !strings.Contains(notice, "pipeline resumed") || loaded.Status != state.StatusRunning {
		t.Fatalf("notice = %q status = %s", notice, loaded.Status)
	}

	// Running and pending projects are never auto-resumed.
	running := loaded
	if got := app.autoResumeAfterInterview(context.Background(), service, "demo", "kept", &running); got != "kept" || len(spawned) != 1 {
		t.Fatalf("running project must not auto-resume: %q %v", got, spawned)
	}
}
