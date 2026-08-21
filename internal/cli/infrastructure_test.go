package cli

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/VedranJanjetovic/gg/internal/config"
)

func TestParseProjectSelector(t *testing.T) {
	tests := []struct {
		name, input, want string
		wantErr           string
	}{
		{"display name", " Demo Project ", "demo-project", ""},
		{"canonical slug", "demo-project", "demo-project", ""},
		{"punctuation", "Release/v2!", "release-v2", ""},
		{"blank", "   ", "", "project selector is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseProjectSelector(tt.input)
			if (err != nil) != (tt.wantErr != "") || (err != nil && !strings.Contains(err.Error(), tt.wantErr)) || got != tt.want {
				t.Fatalf("got (%q, %v), want (%q, %q)", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestFirstProjectSelectorPreservesNoArgumentBehavior(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"none", nil, ""}, {"flags only", []string{"--yes", "--"}, ""},
		{"first positional", []string{"Demo Project", "--force"}, "demo-project"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FirstProjectSelector(tt.args)
			if err != nil || got != tt.want {
				t.Fatalf("got (%q, %v), want (%q, nil)", got, err, tt.want)
			}
		})
	}
}

func TestResolveConfiguredFolder(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name              string
		store             ConfigureStore
		wantRoot, wantErr string
	}{
		{"configured", &memoryConfigureStore{}, root, ""},
		{"unconfigured", &memoryConfigureStore{projectErr: config.ErrProjectNotConfigured}, "", "run \"gg configure\""},
		{"store error", &memoryConfigureStore{projectErr: errors.New("broken")}, "", "check project configuration"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveConfiguredFolder(context.Background(), func() (string, error) { return root, nil }, tt.store)
			if tt.wantErr == "" {
				if err != nil || got != root {
					t.Fatalf("got (%q, %v), want %q", got, err, root)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseConfirmation(t *testing.T) {
	tests := []struct {
		name       string
		args, want []string
		yes        bool
		wantErr    string
	}{
		{"absent", []string{"project"}, []string{"project"}, false, ""},
		{"anywhere", []string{"project", "--yes", "extra"}, []string{"project", "extra"}, true, ""},
		{"delimiter", []string{"--", "--yes"}, []string{"--", "--yes"}, false, ""},
		{"duplicate", []string{"--yes", "--yes"}, nil, false, "only once"},
		{"assignment", []string{"--yes=true"}, nil, false, "use --yes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, yes, err := ParseConfirmation(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || yes != tt.yes || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got (%#v, %v, %v), want (%#v, %v, nil)", got, yes, err, tt.want, tt.yes)
			}
		})
	}
}

func TestCommandHelpMentionsConfigurationRecovery(t *testing.T) {
	var output strings.Builder
	writeTopLevelHelp(&output)
	if !strings.Contains(output.String(), "gg configure") {
		t.Fatal("help lacks configuration recovery command")
	}
}
