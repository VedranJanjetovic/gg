package orchestrator

import (
	"errors"
	"strings"
	"unicode"
)

// ProjectInput is the user-provided input required to create a project.
type ProjectInput struct {
	Goal               string
	AcceptanceCriteria []string
}

// ValidateProjectInput enforces the required fields for project creation.
func ValidateProjectInput(input ProjectInput) error {
	if strings.TrimSpace(input.Goal) == "" {
		return errors.New("project goal is required")
	}
	for _, criterion := range input.AcceptanceCriteria {
		if strings.TrimSpace(criterion) != "" {
			return nil
		}
	}
	return errors.New("at least one acceptance criterion is required")
}

// InferProjectName derives a concise display name from project input. The first
// meaningful goal line is preferred; a criterion is a fallback for callers that
// collect the fields independently. It is deliberately pure so naming remains
// deterministic and testable without filesystem or Git state.
func InferProjectName(input ProjectInput) (string, error) {
	if name := shortProjectName(firstSentence(input.Goal)); name != "" {
		return name, nil
	}
	for _, criterion := range input.AcceptanceCriteria {
		if name := shortProjectName(firstSentence(criterion)); name != "" {
			return name, nil
		}
	}
	return "", errors.New("project goal or acceptance criteria is required to infer a name")
}

// projectNameStopwords are connective and generic action words dropped when
// compressing a goal into a short generated project name; they are kept only
// when nothing else remains.
var projectNameStopwords = map[string]bool{
	"a": true, "an": true, "the": true, "in": true, "on": true, "of": true,
	"for": true, "to": true, "and": true, "or": true, "with": true,
	"using": true, "use": true, "it": true, "its": true, "should": true,
	"be": true, "is": true, "are": true, "that": true, "which": true,
	"into": true, "by": true, "as": true, "at": true, "from": true,
	"this": true, "please": true, "i": true, "we": true, "want": true,
	"build": true, "create": true, "make": true, "implement": true,
	"write": true, "add": true, "develop": true, "new": true,
}

// maxProjectNameWords bounds every project name, however it was produced.
const maxProjectNameWords = 5

// NormalizeProjectName coerces an externally proposed name (for example one an
// agent generated) into the canonical form: at most five lowercase alphanumeric
// words joined by underscores. Unlike the heuristic fallback it keeps stopwords,
// because a proposed name has already been compressed deliberately and dropping
// its words would corrupt the meaning. It returns "" when nothing usable
// remains, which callers treat as a signal to fall back.
func NormalizeProjectName(proposed string) string {
	fields := strings.FieldsFunc(strings.ToLower(proposed), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(fields) > maxProjectNameWords {
		fields = fields[:maxProjectNameWords]
	}
	return strings.Join(fields, "_")
}

// shortProjectName compresses a sentence into the canonical generated
// project name: at most five lowercase words joined by underscores.
func shortProjectName(sentence string) string {
	fields := strings.FieldsFunc(strings.ToLower(sentence), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	const maxWords = maxProjectNameWords
	words := make([]string, 0, maxWords)
	for _, word := range fields {
		if projectNameStopwords[word] {
			continue
		}
		words = append(words, word)
		if len(words) == maxWords {
			break
		}
	}
	if len(words) == 0 {
		// The sentence is all connective words: keep the first few rather
		// than reject the project.
		for _, word := range fields {
			words = append(words, word)
			if len(words) == maxWords {
				break
			}
		}
	}
	return strings.Join(words, "_")
}

func firstSentence(value string) string {
	value = strings.TrimSpace(strings.SplitN(strings.ReplaceAll(value, "\r\n", "\n"), "\n", 2)[0])
	value = strings.TrimSpace(strings.TrimLeft(value, "-*_# "))
	if index := strings.IndexAny(value, ".!?"); index >= 0 {
		return strings.TrimSpace(value[:index])
	}
	return value
}
