package proof

import (
	"strings"
	"testing"
)

func TestParseFoldsWrappedFieldContinuationLines(t *testing.T) {
	proof := "## Validation: wrapped\n- Status: pass\n- Test location: test/\n- Test name: npm test\n- Flow/scenario: Boot engine, exercise fixed-step\n  physics, coyote jump, mushroom growth (live,\n  queued Super i-frames), and audio fallbacks.\n- What it verifies: acceptance checks\n- Proof it passed: $ npm test -> pass 104 / fail 0\n- Manual run instructions: npm install, then npm test.\n"
	parsed, err := Parse([]byte(proof))
	if err != nil {
		t.Fatalf("wrapped field rejected: %v", err)
	}
	got := parsed.Validations[0].FlowScenario
	if !strings.Contains(got, "physics, coyote jump") || !strings.Contains(got, "audio fallbacks") {
		t.Fatalf("continuation not folded: %q", got)
	}
}
