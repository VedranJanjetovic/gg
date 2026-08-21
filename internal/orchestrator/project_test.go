package orchestrator

import "testing"

func TestInferProjectName(t *testing.T) {
	tests := []struct {
		name, goal string
		criteria   []string
		want       string
		wantErr    bool
	}{
		{name: "goal sentence", goal: "Build a release dashboard. Track deployment health.", want: "release_dashboard"},
		{name: "markdown goal", goal: "# Improve onboarding\nDetails follow", want: "improve_onboarding"},
		{name: "criterion fallback", criteria: []string{"- Persist project input"}, want: "persist_project_input"},
		{name: "caps at five words", goal: "Super mario game in browser with keyboard controls and pause", want: "super_mario_game_browser_keyboard"},
		{name: "stopwords only falls back", goal: "Make it for the new", want: "make_it_for_the_new"},
		{name: "missing input", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := InferProjectName(ProjectInput{Goal: test.goal, AcceptanceCriteria: test.criteria})
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("name = %q, want %q", got, test.want)
			}
		})
	}
}

func TestValidateProjectInput(t *testing.T) {
	tests := []struct {
		name  string
		input ProjectInput
		want  string
	}{
		{name: "valid", input: ProjectInput{Goal: "Ship it", AcceptanceCriteria: []string{"It works"}}},
		{name: "missing goal", input: ProjectInput{AcceptanceCriteria: []string{"It works"}}, want: "project goal is required"},
		{name: "missing criteria", input: ProjectInput{Goal: "Ship it"}, want: "at least one acceptance criterion is required"},
		{name: "blank criteria", input: ProjectInput{Goal: "Ship it", AcceptanceCriteria: []string{"  ", "\t"}}, want: "at least one acceptance criterion is required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateProjectInput(test.input)
			if test.want == "" {
				if err != nil {
					t.Fatalf("error = %v, want nil", err)
				}
				return
			}
			if err == nil || err.Error() != test.want {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}
