package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/VedranJanjetovic/gg/testdata/fakeagent"
)

func main() {
	spec, installed, err := fakeagent.Installed()
	if err != nil {
		fatal(err)
	}
	if installed {
		code, err := spec.Run(os.Args[1:])
		if err != nil {
			fatal(err)
		}
		os.Exit(code)
	}
	prompt := lastArgument(os.Args[1:])
	if agentName() == "gh" {
		if err := runGH(os.Args[1:]); err != nil {
			fatal(err)
		}
		return
	}
	switch {
	case os.Getenv("GG_PRODUCTION_FAKE") == "1":
		if err := runProduction(prompt); err != nil {
			fatal(err)
		}
	case os.Getenv("GG_PHASE3_FAKE") == "1":
		if err := runPhase3(prompt); err != nil {
			fatal(err)
		}
	default:
		if err := runBasic(prompt); err != nil {
			fatal(err)
		}
	}
}

func runGH(args []string) error {
	if len(args) >= 2 && args[0] == "pr" && args[1] == "create" {
		fmt.Println("https://github.com/gg/test/pull/1")
		return nil
	}
	if len(args) >= 2 && args[0] == "pr" && args[1] == "checks" {
		fmt.Println(`[{"name":"fake-ci","state":"SUCCESS","bucket":"pass","link":"https://github.com/gg/test/actions/runs/1"}]`)
		return nil
	}
	if len(args) >= 2 && args[0] == "pr" && args[1] == "view" {
		fmt.Println(`{"url":"https://github.com/gg/test/pull/1","state":"MERGED","mergeable":"MERGEABLE","updatedAt":"2026-01-01T00:00:00Z"}`)
		return nil
	}
	return fmt.Errorf("unsupported fake gh command %q", args)
}

func runBasic(_ string) error {
	agent := agentName()
	if logPath := os.Getenv("GG_FAKE_AGENT_LOG"); logPath != "" {
		log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		defer log.Close()
		if _, err := fmt.Fprintf(log, "agent=%s\n", agent); err != nil {
			return err
		}
		for _, argument := range os.Args[1:] {
			if _, err := fmt.Fprintf(log, "arg=%s\n", argument); err != nil {
				return err
			}
		}
	}
	fmt.Printf("fake-%s: deterministic response\n", agent)
	return nil
}

func runPhase3(prompt string) error {
	phase, subphase := phaseAndSubphase(prompt)
	runID := runID(prompt)
	if logPath := os.Getenv("GG_FAKE_AGENT_LOG"); logPath != "" {
		log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		defer log.Close()
		if _, err := fmt.Fprintf(log, "agent=%s\nphase=%s\nsubphase=%s\nrun_id=%s\n", agentName(), phase, subphase, runID); err != nil {
			return err
		}
		for _, argument := range os.Args[1:] {
			if _, err := fmt.Fprintf(log, "arg=%s\n", argument); err != nil {
				return err
			}
		}
	}
	if err := waitForBlockFile(); err != nil {
		return err
	}

	artifact := map[string]string{
		"acceptance_criteria": "acceptance-criteria.md",
		"grooming":            "grooming.md",
		"planning":            "plan.md",
		"development":         "development.md",
		"qa":                  "qa-report.md",
		"rebase":              "rebase-report.md",
		"test_document":       "test-document.md",
		"build_checker":       "build-checker.md",
	}[phase]
	if artifact == "" {
		return fmt.Errorf("unknown phase %q", phase)
	}
	disposition := "passed"
	if phase == "qa" {
		if os.Getenv("GG_FAKE_QA_ALWAYS_FAIL") == "1" || (os.Getenv("GG_FAKE_QA_FAIL_ONCE") == "1" && !fileExists(os.Getenv("GG_FAKE_QA_MARKER"))) {
			disposition = "feedback"
			if marker := os.Getenv("GG_FAKE_QA_MARKER"); marker != "" {
				if err := os.WriteFile(marker, []byte("failed\n"), 0o600); err != nil {
					return err
				}
			}
		}
	}
	if err := writeArtifact(filepath.Join(".gg", artifact), runID, disposition, fmt.Sprintf("Deterministic fake %s result for %s %s.\n", agentName(), phase, subphase)); err != nil {
		return err
	}
	if phase == "development" {
		if err := appendFile("development-progress.txt", subphase+"\n"); err != nil {
			return err
		}
		if err := gitCommit(filepath.Join(".gg", "development.md"), "development-progress.txt", "fake: "+subphase+" development"); err != nil {
			return err
		}
	}
	if phase == "qa" {
		if err := writePhase3Proof(runID, disposition); err != nil {
			return err
		}
	}
	fmt.Printf("fake-%s: deterministic response\n", agentName())
	return nil
}

func runProduction(prompt string) error {
	phase, subphase := phaseAndSubphase(prompt)
	runID := runID(prompt)
	artifact := map[string]string{
		"acceptance_criteria": "acceptance-criteria.md",
		"grooming":            "grooming.md",
		"planning":            "plan.md",
		"development":         "development.md",
		"qa":                  "PROOF.md",
		"rebase":              "rebase-report.md",
		"test_document":       "test-document.md",
		"build_checker":       "build-checker.md",
		"pr":                  "pr.md",
		"ci":                  "ci-report.md",
	}[phase]
	if artifact == "" {
		return fmt.Errorf("unknown phase %q", phase)
	}
	disposition := "passed"
	if phase == "qa" && !fileExists(filepath.Join(".gg", "qa-report.md")) {
		disposition = "failed"
	}
	if err := writeArtifact(filepath.Join(".gg", artifact), runID, disposition, fmt.Sprintf("phase=%s subphase=%s disposition=%s\n", phase, subphase, disposition)); err != nil {
		return err
	}
	if phase == "qa" {
		if err := writeProductionProof(runID, disposition); err != nil {
			return err
		}
		if err := writeArtifact(filepath.Join(".gg", "qa-report.md"), runID, "passed", "legacy qa report\n"); err != nil {
			return err
		}
	}
	if phase == "development" {
		if err := appendFile("development-progress.txt", subphase+"\n"); err != nil {
			return err
		}
		if err := gitCommit(filepath.Join(".gg", "development.md"), "development-progress.txt", "development-"+subphase); err != nil {
			return err
		}
	}
	fmt.Printf("fake-agent phase=%s subphase=%s\n", phase, subphase)
	return nil
}

func phaseAndSubphase(prompt string) (string, string) {
	phase, subphase := "", ""
	lines := strings.Split(prompt, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != "## Phase" || i+1 >= len(lines) {
			continue
		}
		line = strings.TrimSpace(lines[i+1])
		parts := strings.SplitN(line, " / ", 2)
		phase = strings.Trim(strings.TrimSpace(parts[0]), "\"")
		if len(parts) == 2 {
			subphase = strings.Trim(strings.TrimSpace(parts[1]), "\"")
		}
	}
	if phase == "" {
		for _, candidate := range []string{"acceptance_criteria", "grooming", "planning", "development", "qa", "rebase", "test_document", "build_checker", "pr", "ci"} {
			if strings.Contains(prompt, "\""+candidate+"\"") {
				phase = candidate
				break
			}
		}
	}
	if subphase == "" && phase == "development" {
		for _, candidate := range []string{"implementation", "testing", "review"} {
			if strings.Contains(prompt, "\"development\" / \""+candidate+"\"") {
				subphase = candidate
				break
			}
		}
	}
	return phase, subphase
}

func runID(prompt string) string {
	for _, line := range strings.Split(prompt, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "gg_run_id:") {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "gg_run_id:")), "\"")
		}
	}
	return "unknown-run"
}

func waitForBlockFile() error {
	path := os.Getenv("GG_FAKE_BLOCK_FILE")
	if path == "" {
		return nil
	}
	for fileExists(path) {
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}

func writeArtifact(path, id, disposition, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	frontmatter := fmt.Sprintf("---\ngg_run_id: %q\ngg_disposition: %s\n", id, disposition)
	if filepath.Base(path) == "plan.md" {
		frontmatter += "gg_plan_complexity: \"Trivial\"\ngg_plan_complexity_evidence: [\"The fixture exercises one cohesive pipeline outcome.\"]\ngg_plan_phases: [\"Phase 1: production pipeline\"]\ngg_plan_phase_boundaries: [{\"phase\":\"Phase 1: production pipeline\",\"justification\":\"The fixture is one cohesive outcome with no dependency ordering.\"}]\n"
		verificationCommand := "go"
		verificationArgs := `["test","./..."]`
		verificationAdapter := "go-test"
		if os.Getenv("GG_PRODUCTION_FAKE") == "1" || os.Getenv("GG_PHASE3_FAKE") == "1" {
			// These composition fixtures are disposable Git repositories, not the
			// gg module, so their executable check must be repository-local.
			verificationCommand = "git"
			verificationArgs = `["diff","--check"]`
			verificationAdapter = "git-diff-check"
		}
		frontmatter += fmt.Sprintf("gg_verification_steps: [{\"name\":\"tests\",\"command\":\"%s\",\"args\":%s,\"adapter\":\"%s\"}]\ngg_repair_mode: false\n", verificationCommand, verificationArgs, verificationAdapter)
		body = "# Implementation Plan\n\n## Complexity assessment\n\n- Complexity category: **Trivial**\n- Selected phase count: **1**\n\nSupporting evidence:\n\n1. The fixture exercises one cohesive pipeline outcome.\n\n## Phase 1: production pipeline\n\nBoundary justification: The fixture is one cohesive outcome with no dependency ordering.\n"
	}
	frontmatter += fmt.Sprintf("---\n\n%s", body)
	return os.WriteFile(path, []byte(frontmatter), 0o644)
}

func writePhase3Proof(id, disposition string) error {
	proofDisposition := "pass"
	status := "pass"
	if disposition != "passed" {
		proofDisposition, status = "feedback", "feedback"
	}
	proof := fmt.Sprintf("# PROOF\n\n---\ngg_run_id: %q\ngg_disposition: %s\n---\n\n## Validation: fake QA\n- Status: %s\n- Test location: internal/e2e/phase3_cli_e2e_test.go\n- Test name: real fake pipeline\n- Flow/Scenario: deterministic CLI pipeline\n- What it verifies: the fake QA executable produced canonical evidence\n- Proof it passed: `go test ./...` returned result %s\n- Manual run instructions: run the focused E2E test\n", id, proofDisposition, status, status)
	proofPath := filepath.Join(".gg", "PROOF.md")
	if mode := os.Getenv("GG_FAKE_PROOF_MODE"); mode == "malformed" || mode == "tracked" {
		if err := os.WriteFile(proofPath, []byte("# malformed proof\n"), 0o644); err != nil {
			return err
		}
		if mode == "tracked" {
			return gitAdd(proofPath)
		}
		return nil
	}
	if os.Getenv("GG_FAKE_PROOF_MODE") == "missing" {
		return nil
	}
	if os.Getenv("GG_FAKE_PROOF_MODE") == "stale" {
		proof = strings.Replace(proof, id, "stale-run", 1)
	}
	if err := os.WriteFile(proofPath, []byte(proof), 0o644); err != nil {
		return err
	}
	return nil
}

func writeProductionProof(id, disposition string) error {
	proofStatus, proofDisposition := "pass", "pass"
	if disposition != "passed" {
		proofStatus, proofDisposition = "feedback", "feedback"
	}
	proof := fmt.Sprintf("# PROOF\n\n---\ngg_run_id: %q\ngg_disposition: %s\n---\n\n## Validation: production flow\n- Status: %s\n- Test location: production fixture\n- Test name: TestProductionCompositionRunsFakeAgentsGitStateAndPersistsAllEvents\n- Flow/scenario: execute the configured production pipeline through the CLI\n- What it verifies: each configured phase runs and persists artifacts\n- Proof it passed: `$ go test ./cmd/gg -run TestProductionCompositionRunsFakeAgentsGitStateAndPersistsAllEvents -count=1; result: exit code 0`\n- Manual run instructions: configure the repository and run gg run e2e-production.\n", id, proofDisposition, proofStatus)
	return os.WriteFile(filepath.Join(".gg", "PROOF.md"), []byte(proof), 0o644)
}

func appendFile(path, content string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteString(content)
	return err
}

func gitCommit(paths ...string) error {
	message := paths[len(paths)-1]
	paths = paths[:len(paths)-1]
	if err := gitAdd(paths...); err != nil {
		return err
	}
	return runCommand("git", "-c", "commit.gpgsign=false", "commit", "-m", message)
}

func gitAdd(paths ...string) error {
	args := append([]string{"add", "-f"}, paths...)
	return runCommand("git", args...)
}

func runCommand(name string, args ...string) error {
	command := exec.Command(name, args...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func agentName() string {
	name := filepath.Base(os.Args[0])
	return strings.TrimSuffix(name, filepath.Ext(name))
}

func lastArgument(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[len(args)-1]
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func fatal(err error) {
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(os.Stderr, err)
	} else {
		fmt.Fprintf(os.Stderr, "fake agent: %v\n", err)
	}
	os.Exit(1)
}
