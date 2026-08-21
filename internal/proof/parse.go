package proof

import (
	"fmt"
	"strings"
)

// Parse parses the small Markdown contract used by PROOF.md.
func Parse(data []byte) (Proof, error) {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	var result Proof
	var current *Validation
	seenFields := make(map[string]bool)
	section := ""
	var feedback []string
	lastKey := ""
	frontMatter := false
	flush := func() {
		if current != nil {
			result.Validations = append(result.Validations, *current)
			current = nil
		}
		lastKey = ""
	}
	for lineNumber, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "---" && !frontMatter && current == nil && len(result.Validations) == 0 && section == "" {
			frontMatter = true
			continue
		}
		if frontMatter {
			if line == "---" {
				frontMatter = false
				continue
			}
			if key, value, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(key) == "gg_run_id" {
				result.RunID = strings.Trim(strings.TrimSpace(value), "\"")
			}
			continue
		}
		if strings.HasPrefix(line, "## Validation") {
			seenFields = make(map[string]bool)
			label := strings.TrimSpace(strings.TrimPrefix(line, "## Validation:"))
			if !strings.HasPrefix(line, "## Validation:") || label == "" {
				return Proof{}, fmt.Errorf("line %d: validation heading must be ## Validation: <label>", lineNumber+1)
			}
			flush()
			current = &Validation{}
			section = "validation"
			continue
		}
		if strings.EqualFold(line, "## Feedback") {
			flush()
			section = "feedback"
			continue
		}
		if strings.HasPrefix(line, "## ") {
			flush()
			section = ""
			continue
		}
		if section == "feedback" {
			if line != "" {
				feedback = append(feedback, line)
			}
			continue
		}
		if current == nil || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, "-") {
			// A long field value wrapped across lines continues the previous
			// free-text field; only a section with no field yet is malformed.
			if appendFieldContinuation(current, lastKey, line) {
				continue
			}
			return Proof{}, fmt.Errorf("line %d: validation fields must use a dash field", lineNumber+1)
		}
		key, value, ok := strings.Cut(strings.TrimSpace(strings.TrimPrefix(line, "-")), ":")
		if !ok {
			return Proof{}, fmt.Errorf("line %d: malformed validation field", lineNumber+1)
		}
		value = strings.TrimSpace(value)
		normalizedKey := normalizeKey(key)
		lastKey = normalizedKey
		if seenFields[normalizedKey] {
			return Proof{}, fmt.Errorf("line %d: duplicate validation field %q", lineNumber+1, strings.TrimSpace(key))
		}
		seenFields[normalizedKey] = true
		switch normalizedKey {
		case "status":
			current.Status = Status(strings.ToLower(value))
		case "testlocation":
			current.TestLocation = value
		case "testname":
			current.TestName = value
		case "flowscenario":
			current.FlowScenario = value
		case "whatitverifies":
			current.WhatItVerifies = value
		case "proofitpassed":
			current.ProofItPassed = value
		case "manualruninstructions":
			current.ManualRunInstructions = value
		default:
			return Proof{}, fmt.Errorf("line %d: unknown validation field %q", lineNumber+1, strings.TrimSpace(key))
		}
	}
	flush()
	result.Feedback = strings.TrimSpace(strings.Join(feedback, "\n"))
	if err := result.Validate(); err != nil {
		return Proof{}, err
	}
	return result, nil
}

// appendFieldContinuation folds a wrapped line into the previous free-text
// field's value. Status is a controlled vocabulary and never wraps.
func appendFieldContinuation(current *Validation, lastKey, text string) bool {
	if current == nil || text == "" {
		return false
	}
	var target *string
	switch lastKey {
	case "testlocation":
		target = &current.TestLocation
	case "testname":
		target = &current.TestName
	case "flowscenario":
		target = &current.FlowScenario
	case "whatitverifies":
		target = &current.WhatItVerifies
	case "proofitpassed":
		target = &current.ProofItPassed
	case "manualruninstructions":
		target = &current.ManualRunInstructions
	default:
		return false
	}
	if *target == "" {
		*target = text
	} else {
		*target += " " + text
	}
	return true
}

func normalizeKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.ReplaceAll(key, "/", "")
	key = strings.ReplaceAll(key, " ", "")
	return key
}

// ClassifyMarkdown parses and classifies a PROOF.md payload.
func ClassifyMarkdown(data []byte) (Classification, error) {
	parsed, err := Parse(data)
	if err != nil {
		return ClassificationFail, err
	}
	return parsed.Classify(), nil
}
