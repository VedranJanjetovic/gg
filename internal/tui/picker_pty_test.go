//go:build linux || darwin

package tui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VedranJanjetovic/gg/internal/config"
	"github.com/creack/pty"
	"golang.org/x/term"
)

const pickerPTYTimeout = 3 * time.Second

func ptyPickerCatalog() config.AgentCatalog {
	return config.NewAgentCatalog(
		config.AgentCatalogEntry{Agent: config.AgentClaude, Models: []string{"sonnet"}, ModelListStatus: config.ModelListAvailable},
		config.AgentCatalogEntry{Agent: config.AgentCodex, Models: []string{"gpt-5", "gpt-4", "gpt-3"}, ModelListStatus: config.ModelListAvailable},
	)
}

func TestRunAgentModelPickerPTYNavigatesWithoutCookedEcho(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	master, slave, before := openPickerPTY(t)
	defer master.Close()
	defer slave.Close()

	resultCh := make(chan pickerRunResult, 1)
	go func() {
		result, err := RunConfigureWizard(context.Background(), ptyPickerCatalog(), WizardDefaults{}, slave, slave)
		resultCh <- pickerRunResult{result: result, err: err}
	}()

	capture := newPTYCapture(master)
	output := capture.wait(t, "Choose an agent to configure")
	writePTY(t, master, []byte("\x1b[B"))
	output += capture.wait(t, "▸ codex")
	writePTY(t, master, []byte("\r"))
	output += capture.wait(t, "Select a model for")
	writePTY(t, master, []byte("j\r"))
	output += capture.wait(t, "Select default effort")
	writePTY(t, master, []byte("\r"))

	result := waitPickerResult(t, resultCh)
	if result.err != nil {
		t.Fatalf("picker error = %v; output = %q", result.err, output)
	}
	if !reflect.DeepEqual(result.result, PickerResult{Agent: config.AgentCodex, Model: "gpt-4", Effort: config.EffortMedium}) {
		t.Fatalf("picker result = %#v; want codex/gpt-4/medium after j; output = %q", result.result, output)
	}
	assertPickerPTYRestored(t, slave, before)
	slave.Close()
	capture.finish(t)
	output = capture.output()
	if strings.Contains(output, "^[[") {
		t.Fatalf("PTY output contains cooked escape echo: %q", output)
	}
	if !strings.Contains(output, "\x1b[?1049l") {
		t.Fatalf("PTY output did not leave alternate screen: %q", output)
	}
}

func TestRunAgentModelPickerPTYKSelectsPreviousModel(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	master, slave, before := openPickerPTY(t)
	defer master.Close()
	defer slave.Close()

	resultCh := make(chan pickerRunResult, 1)
	go func() {
		result, err := RunConfigureWizard(context.Background(), ptyPickerCatalog(), WizardDefaults{}, slave, slave)
		resultCh <- pickerRunResult{result: result, err: err}
	}()
	capture := newPTYCapture(master)
	capture.wait(t, "Choose an agent to configure")
	writePTY(t, master, []byte("\x1b[B"))
	capture.wait(t, "▸ codex")
	writePTY(t, master, []byte("\r"))
	capture.wait(t, "Select a model for")
	writePTY(t, master, []byte("j"))
	capture.wait(t, "▸ gpt-4")
	writePTY(t, master, []byte("k\r"))
	capture.wait(t, "Select default effort")
	writePTY(t, master, []byte("\r"))
	result := waitPickerResult(t, resultCh)
	if result.err != nil {
		t.Fatalf("picker error = %v; output = %q", result.err, capture.output())
	}
	if !reflect.DeepEqual(result.result, PickerResult{Agent: config.AgentCodex, Model: "gpt-5", Effort: config.EffortMedium}) {
		t.Fatalf("picker result = %#v; want codex/gpt-5/medium after j then k; output = %q", result.result, capture.output())
	}
	assertPickerPTYRestored(t, slave, before)
	slave.Close()
	capture.finish(t)
}

func TestRunAgentModelPickerPTYManualModelEntry(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	master, slave, before := openPickerPTY(t)
	defer master.Close()
	defer slave.Close()

	resultCh := make(chan pickerRunResult, 1)
	go func() {
		result, err := RunConfigureWizard(context.Background(), ptyPickerCatalog(), WizardDefaults{}, slave, slave)
		resultCh <- pickerRunResult{result: result, err: err}
	}()
	capture := newPTYCapture(master)
	capture.wait(t, "Choose an agent to configure")
	writePTY(t, master, []byte("\r"))
	capture.wait(t, "Enter model name manually")
	writePTY(t, master, []byte("k\r")) // wrap up to the manual row and select it
	capture.wait(t, "Enter a model name for")
	writePTY(t, master, []byte("claude-custom-model\r"))
	capture.wait(t, "Select default effort")
	writePTY(t, master, []byte("\r"))
	result := waitPickerResult(t, resultCh)
	if result.err != nil {
		t.Fatalf("picker error = %v; output = %q", result.err, capture.output())
	}
	if !reflect.DeepEqual(result.result, PickerResult{Agent: config.AgentClaude, Model: "claude-custom-model", Manual: true, Effort: config.EffortMedium}) {
		t.Fatalf("picker result = %#v; want manual claude-custom-model; output = %q", result.result, capture.output())
	}
	assertPickerPTYRestored(t, slave, before)
	slave.Close()
	capture.finish(t)
}

func TestRunConfigureWizardPTYPhaseCheckboxToggle(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	master, slave, before := openPickerPTY(t)
	defer master.Close()
	defer slave.Close()

	defaults := WizardDefaults{Phases: []PhaseState{
		{Phase: config.PhasePlanning, Enabled: true, Description: "plan"},
		{Phase: config.PhaseQA, Enabled: true, Description: "qa"},
	}}
	resultCh := make(chan pickerRunResult, 1)
	go func() {
		result, err := RunConfigureWizard(context.Background(), ptyPickerCatalog(), defaults, slave, slave)
		resultCh <- pickerRunResult{result: result, err: err}
	}()
	capture := newPTYCapture(master)
	capture.wait(t, "Choose an agent to configure")
	writePTY(t, master, []byte("\r")) // claude
	capture.wait(t, "Select a model for")
	writePTY(t, master, []byte("\r")) // sonnet
	capture.wait(t, "Select default effort")
	writePTY(t, master, []byte("\r")) // medium
	capture.wait(t, "[x] planning")
	writePTY(t, master, []byte("j ")) // move to qa, toggle off
	capture.wait(t, "[ ] qa")
	capture.wait(t, "Save configuration")
	writePTY(t, master, []byte("j\r")) // move to the save row, confirm
	result := waitPickerResult(t, resultCh)
	if result.err != nil {
		t.Fatalf("wizard error = %v; output = %q", result.err, capture.output())
	}
	want := PickerResult{Agent: config.AgentClaude, Model: "sonnet", Effort: config.EffortMedium, Phases: []PhaseState{
		{Phase: config.PhasePlanning, Enabled: true, Description: "plan"},
		{Phase: config.PhaseQA, Enabled: false, Description: "qa"},
	}}
	if !reflect.DeepEqual(result.result, want) {
		t.Fatalf("wizard result = %#v, want %#v; output = %q", result.result, want, capture.output())
	}
	assertPickerPTYRestored(t, slave, before)
	slave.Close()
	capture.finish(t)
}

func TestRunProjectPromptPTYEchoesAndConfirms(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	master, slave, before := openPickerPTY(t)
	defer master.Close()
	defer slave.Close()

	type promptRunResult struct {
		description string
		err         error
	}
	resultCh := make(chan promptRunResult, 1)
	go func() {
		description, err := RunProjectPrompt(context.Background(), func(string) string { return "supermario in browser" }, slave, slave)
		resultCh <- promptRunResult{description: description, err: err}
	}()
	capture := newPTYCapture(master)
	capture.wait(t, "Describe the project")
	writePTY(t, master, []byte("supermario in browser"))
	capture.wait(t, "supermario in browser") // typed text is echoed live
	writePTY(t, master, []byte("\r\r"))      // double Enter finishes
	capture.wait(t, "Create this project?")
	capture.wait(t, "Project name: supermario in browser")
	writePTY(t, master, []byte("\r"))
	var result promptRunResult
	select {
	case result = <-resultCh:
	case <-time.After(pickerPTYTimeout):
		t.Fatal("project prompt did not terminate within bounded PTY timeout")
	}
	if result.err != nil || result.description != "supermario in browser" {
		t.Fatalf("result = %q, err = %v; output = %q", result.description, result.err, capture.output())
	}
	assertPickerPTYRestored(t, slave, before)
	slave.Close()
	capture.finish(t)
}

func TestEOFNotifyingReaderForwardsDataAndEOF(t *testing.T) {
	notified := make(chan struct{})
	reader := &eofNotifyingReader{
		reader: eofDataReader{},
		notify: func() { close(notified) },
	}
	buf := make([]byte, 4)
	n, err := reader.Read(buf)
	if n != 3 || string(buf[:n]) != "eof" || !errors.Is(err, io.EOF) {
		t.Fatalf("Read() = (%d, %v), data %q; want (3, EOF), eof", n, err, buf[:n])
	}
	select {
	case <-notified:
	case <-time.After(time.Second):
		t.Fatal("EOF notification was not delivered")
	}
}

type eofDataReader struct{}

func (eofDataReader) Read(p []byte) (int, error) {
	copy(p, "eof")
	return 3, io.EOF
}

func TestRunAgentModelPickerPTYEscRestoresTerminal(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	master, slave, before := openPickerPTY(t)
	defer master.Close()
	defer slave.Close()

	resultCh := make(chan pickerRunResult, 1)
	go func() {
		result, err := RunConfigureWizard(context.Background(), pickerCatalog(), WizardDefaults{}, slave, slave)
		resultCh <- pickerRunResult{result: result, err: err}
	}()
	capture := newPTYCapture(master)
	output := capture.wait(t, "Choose an agent to configure")
	writePTY(t, master, []byte("\x1b"))
	result := waitPickerResult(t, resultCh)
	if !errors.Is(result.err, ErrPickerCancelled) {
		t.Fatalf("Esc error = %v, want %v", result.err, ErrPickerCancelled)
	}
	assertPickerPTYRestored(t, slave, before)
	slave.Close()
	capture.finish(t)
	output = capture.output()
	if strings.Contains(output, "^[[") || !strings.Contains(output, "\x1b[?1049l") {
		t.Fatalf("Esc cleanup output = %q; want no cooked echo and alternate-screen exit", output)
	}
}

func TestRunAgentModelPickerPTYContextCancellationRestoresTerminal(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	master, slave, before := openPickerPTY(t)
	defer master.Close()
	defer slave.Close()

	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan pickerRunResult, 1)
	go func() {
		result, err := RunConfigureWizard(ctx, pickerCatalog(), WizardDefaults{}, slave, slave)
		resultCh <- pickerRunResult{result: result, err: err}
	}()
	capture := newPTYCapture(master)
	output := capture.wait(t, "Choose an agent to configure")
	cancel()
	result := waitPickerResult(t, resultCh)
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("context error = %v, want %v", result.err, context.Canceled)
	}
	assertPickerPTYRestored(t, slave, before)
	slave.Close()
	capture.finish(t)
	output = capture.output()
	if strings.Contains(output, "^[[") || !strings.Contains(output, "\x1b[?1049l") {
		t.Fatalf("context cleanup output = %q; want no cooked echo and alternate-screen exit", output)
	}
}

func openPickerPTY(t *testing.T) (master, slave *os.File, before *term.State) {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Skipf("PTY support unavailable: %v", err)
	}
	if err := pty.Setsize(slave, &pty.Winsize{Cols: 100, Rows: 30}); err != nil {
		master.Close()
		slave.Close()
		t.Skipf("PTY window sizing unavailable: %v", err)
	}
	before, err = term.GetState(int(slave.Fd()))
	if err != nil {
		master.Close()
		slave.Close()
		t.Skipf("PTY terminal state unavailable: %v", err)
	}
	return master, slave, before
}

type pickerRunResult struct {
	result PickerResult
	err    error
}

func writePTY(t *testing.T, master *os.File, input []byte) {
	t.Helper()
	if _, err := master.Write(input); err != nil {
		t.Fatalf("write PTY input %q: %v", input, err)
	}
}

type ptyCapture struct {
	master *os.File
	events chan ptyCaptureEvent
	stop   chan struct{}
	done   chan struct{}
	close  sync.Once
	buf    bytes.Buffer
}

type ptyCaptureEvent struct {
	data []byte
	err  error
}

func newPTYCapture(master *os.File) *ptyCapture {
	capture := &ptyCapture{master: master, events: make(chan ptyCaptureEvent, 8), stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(capture.done)
		for {
			chunk := make([]byte, 4096)
			n, err := master.Read(chunk)
			event := ptyCaptureEvent{err: err}
			if n > 0 {
				event.data = append([]byte(nil), chunk[:n]...)
			}
			select {
			case capture.events <- event:
			case <-capture.stop:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return capture
}

func (capture *ptyCapture) wait(t *testing.T, marker string) string {
	t.Helper()
	deadline := time.NewTimer(pickerPTYTimeout)
	defer deadline.Stop()
	for !strings.Contains(capture.buf.String(), marker) {
		select {
		case event := <-capture.events:
			capture.buf.Write(event.data)
			if event.err != nil && !strings.Contains(capture.buf.String(), marker) {
				t.Fatalf("read PTY output while waiting for %q: %v; output = %q", marker, event.err, capture.buf.String())
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for PTY marker %q; output = %q", marker, capture.buf.String())
		}
	}
	return capture.buf.String()
}

func (capture *ptyCapture) finish(t *testing.T) {
	t.Helper()
	capture.close.Do(func() {
		if err := capture.master.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Errorf("close PTY master: %v", err)
		}
		close(capture.stop)
	})
	select {
	case <-capture.done:
	case <-time.After(pickerPTYTimeout):
		t.Fatal("PTY capture reader did not terminate")
	}
	for {
		select {
		case event := <-capture.events:
			capture.buf.Write(event.data)
		default:
			return
		}
	}
}

func (capture *ptyCapture) output() string { return capture.buf.String() }

func waitPickerResult(t *testing.T, resultCh <-chan pickerRunResult) pickerRunResult {
	t.Helper()
	select {
	case result := <-resultCh:
		return result
	case <-time.After(pickerPTYTimeout):
		t.Fatal("picker did not terminate within bounded PTY timeout")
		return pickerRunResult{}
	}
}

func assertPickerPTYRestored(t *testing.T, slave *os.File, before *term.State) {
	t.Helper()
	after, err := term.GetState(int(slave.Fd()))
	if err != nil {
		t.Fatalf("read terminal state after picker: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("terminal state was not restored: before=%#v after=%#v", before, after)
	}
}
