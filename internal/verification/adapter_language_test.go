package verification

import "testing"

// The adapter set names output shapes rather than toolchains, so a project in
// any language can declare a complete verification contract.
func TestLanguageNeutralAdaptersParseNonGoToolOutput(t *testing.T) {
	tests := []struct {
		name         string
		adapter      Adapter
		stdout       string
		exitCode     int
		wantIdentity []string
		wantReason   string
	}{
		{
			name:         "file-list reads prettier output",
			adapter:      AdapterFileList,
			stdout:       "src/app.ts\nsrc/lib/util.ts\n",
			exitCode:     1,
			wantIdentity: []string{"format:src/app.ts", "format:src/lib/util.ts"},
			wantReason:   "file requires formatting",
		},
		{
			name:         "diagnostic reads tsc output",
			adapter:      AdapterDiagnostic,
			stdout:       "src/app.ts:12:5: error TS2322: Type 'string' is not assignable to type 'number'.",
			exitCode:     2,
			wantIdentity: []string{"src/app.ts:12:5"},
		},
		{
			name:         "diagnostic reads clippy output",
			adapter:      AdapterDiagnostic,
			stdout:       "src/main.rs:8:9: warning: unused variable: `x`",
			exitCode:     1,
			wantIdentity: []string{"src/main.rs:8:9"},
		},
		{
			name:         "command-exit collapses an unparseable failure to one identity",
			adapter:      AdapterCommandExit,
			stdout:       "[ERROR] Tests run: 4, Failures: 1\n[ERROR] BUILD FAILURE",
			exitCode:     1,
			wantIdentity: []string{"command"},
			wantReason:   "command reported failure",
		},
		{
			name:     "command-exit reports nothing when the command succeeds",
			adapter:  AdapterCommandExit,
			stdout:   "BUILD SUCCESS",
			exitCode: 0,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			failures, classifiable, err := ParseOutput(test.adapter, test.stdout, "", test.exitCode)
			if err != nil {
				t.Fatalf("ParseOutput returned error: %v", err)
			}
			if !classifiable {
				t.Fatalf("ParseOutput reported unclassifiable output for adapter %q", test.adapter)
			}
			if len(failures) != len(test.wantIdentity) {
				t.Fatalf("got %d failures, want %d: %+v", len(failures), len(test.wantIdentity), failures)
			}
			for i, want := range test.wantIdentity {
				if failures[i].Identity != want {
					t.Fatalf("failure %d identity = %q, want %q", i, failures[i].Identity, want)
				}
				if test.wantReason != "" && failures[i].Reason != test.wantReason {
					t.Fatalf("failure %d reason = %q, want %q", i, failures[i].Reason, test.wantReason)
				}
			}
		})
	}
}

// A snapshot written before the adapters were generalized must keep resuming,
// and its baseline reasons must stay byte-identical or every unchanged failure
// would be reclassified as changed_reason and block the boundary.
func TestLegacyGoAdapterAliasesStayValidAndPreserveBaselineReasons(t *testing.T) {
	aliases := map[Adapter]Adapter{
		AdapterGofmtEmpty:   AdapterFileList,
		AdapterGoDiagnostic: AdapterDiagnostic,
	}
	for alias, canonical := range aliases {
		if !alias.IsValid() {
			t.Fatalf("legacy adapter %q is no longer valid", alias)
		}
		if got := alias.Canonical(); got != canonical {
			t.Fatalf("%q.Canonical() = %q, want %q", alias, got, canonical)
		}
	}

	failures, _, err := ParseOutput(AdapterGofmtEmpty, "internal/app.go\n", "", 1)
	if err != nil {
		t.Fatalf("ParseOutput returned error: %v", err)
	}
	if len(failures) != 1 {
		t.Fatalf("got %d failures, want 1", len(failures))
	}
	if failures[0].Reason != "file requires gofmt" {
		t.Fatalf("legacy gofmt-empty reason = %q, want %q — a changed reason breaks baseline comparison on resume", failures[0].Reason, "file requires gofmt")
	}
}

func TestUnsupportedAdapterIsStillRejected(t *testing.T) {
	if Adapter("mvn-surefire").IsValid() {
		t.Fatal("an unknown adapter must not validate")
	}
	if _, _, err := ParseOutput(Adapter("mvn-surefire"), "", "", 1); err == nil {
		t.Fatal("ParseOutput must reject an unknown adapter")
	}
}
