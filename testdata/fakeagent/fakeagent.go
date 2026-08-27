// Package fakeagent installs deterministic fake agent executables for tests.
//
// Tests describe the observable behavior they need with a Spec; the compiled
// fake at testdata/fake-agent performs it. The fake is a real executable rather
// than a "#!/bin/sh" fixture because Windows can neither run a shebang script
// nor exec a binary without an ".exe" suffix.
package fakeagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/VedranJanjetovic/gg/internal/pipeline"
)

// specFileName is the fixed name of the behavior file installed next to a fake
// agent executable. The spec travels with the executable instead of through the
// environment so that independently installed fakes never interfere.
const specFileName = "fake-agent-spec.json"

// Spec is the observable behavior of one fake agent invocation. Stdout, Stderr
// and every Files key and value expand these placeholders:
//
//	${PROMPT}          the last command-line argument
//	${ARGn}            the n-th argument, 1-based, like a shell "$n"
//	${ENV:NAME}        the value of environment variable NAME
//	${RUN_ID}          the gg_run_id the standalone prompt carries
//	${PHASE_ARTIFACT}  the canonical artifact path of the prompt's phase
type Spec struct {
	Stdout   string            `json:"stdout,omitempty"`
	Stderr   string            `json:"stderr,omitempty"`
	Files    map[string]string `json:"files,omitempty"`
	ExitCode int               `json:"exitCode,omitempty"`
	Block    bool              `json:"block,omitempty"`
}

// Install writes a copy of the compiled fake agent named name into dir together
// with the spec that drives it, and returns the executable path.
func Install(dir, name string, spec Spec) (string, error) {
	binary, err := Binary()
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, specFileName), encoded, 0o644); err != nil {
		return "", err
	}
	path := filepath.Join(dir, executableName(name))
	if err := os.WriteFile(path, binary, 0o755); err != nil {
		return "", err
	}
	return path, nil
}

// Installed loads the spec installed next to the running executable. It reports
// false when the executable was installed without one.
func Installed() (Spec, bool, error) {
	executable, err := os.Executable()
	if err != nil {
		executable = os.Args[0]
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(executable), specFileName))
	if errors.Is(err, os.ErrNotExist) {
		return Spec{}, false, nil
	}
	if err != nil {
		return Spec{}, false, err
	}
	var spec Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		return Spec{}, false, fmt.Errorf("decode fake agent spec: %w", err)
	}
	return spec, true, nil
}

// Run performs the spec against the given arguments and returns the exit code
// the fake agent must report.
func (s Spec) Run(args []string) (int, error) {
	for path, content := range s.Files {
		target := filepath.FromSlash(expand(path, args))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return 1, err
		}
		if err := os.WriteFile(target, []byte(expand(content, args)), 0o644); err != nil {
			return 1, err
		}
	}
	if _, err := os.Stdout.WriteString(expand(s.Stdout, args)); err != nil {
		return 1, err
	}
	if _, err := os.Stderr.WriteString(expand(s.Stderr, args)); err != nil {
		return 1, err
	}
	for s.Block {
		time.Sleep(time.Second)
	}
	return s.ExitCode, nil
}

var (
	buildOnce sync.Once
	binary    []byte
	buildErr  error
)

// Binary returns the compiled fake agent executable. It is built once per
// process because building it is far more expensive than copying it.
func Binary() ([]byte, error) {
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "gg-fake-agent")
		if err != nil {
			buildErr = err
			return
		}
		defer func() { _ = os.RemoveAll(dir) }()
		output := filepath.Join(dir, executableName("fake-agent"))
		build := exec.Command("go", "build", "-o", output, "./testdata/fake-agent")
		build.Dir = moduleRoot()
		if combined, err := build.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("build fake agent: %w\n%s", err, combined)
			return
		}
		binary, buildErr = os.ReadFile(output)
	})
	return binary, buildErr
}

func moduleRoot() string {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
}

func executableName(name string) string {
	if runtime.GOOS == "windows" && filepath.Ext(name) == "" {
		return name + ".exe"
	}
	return name
}

func expand(value string, args []string) string {
	var expanded strings.Builder
	for {
		start := strings.Index(value, "${")
		if start < 0 {
			break
		}
		end := strings.Index(value[start:], "}")
		if end < 0 {
			break
		}
		expanded.WriteString(value[:start])
		expanded.WriteString(placeholder(value[start+2:start+end], args))
		value = value[start+end+1:]
	}
	expanded.WriteString(value)
	return expanded.String()
}

func placeholder(name string, args []string) string {
	switch {
	case name == "PROMPT":
		return prompt(args)
	case name == "RUN_ID":
		return runID(prompt(args))
	case name == "PHASE_ARTIFACT":
		artifact, _ := pipeline.CanonicalArtifactName(pipeline.PhaseID(phase(prompt(args))))
		return artifact
	case strings.HasPrefix(name, "ENV:"):
		return os.Getenv(strings.TrimPrefix(name, "ENV:"))
	case strings.HasPrefix(name, "ARG"):
		index, err := strconv.Atoi(strings.TrimPrefix(name, "ARG"))
		if err != nil || index < 1 || index > len(args) {
			return ""
		}
		return args[index-1]
	}
	return "${" + name + "}"
}

func prompt(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[len(args)-1]
}

// phase returns the phase identifier declared by the standalone prompt's
// "## Phase" section, for example `"development" / "implementation"`.
func phase(prompt string) string {
	lines := strings.Split(prompt, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != "## Phase" || i+1 >= len(lines) {
			continue
		}
		name, _, _ := strings.Cut(strings.TrimSpace(lines[i+1]), " / ")
		return strings.Trim(name, `"`)
	}
	return ""
}

// runID returns the run ID the standalone prompt's result protocol declares.
func runID(prompt string) string {
	for _, line := range strings.Split(prompt, "\n") {
		value, ok := strings.CutPrefix(strings.TrimSpace(line), "gg_run_id:")
		if ok {
			return strings.Trim(strings.TrimSpace(value), `"`)
		}
	}
	return ""
}
