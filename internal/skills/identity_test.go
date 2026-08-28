package skills

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var (
	skillIdentityPattern     = regexp.MustCompile(`^gg-[a-z0-9]+(?:-[a-z0-9]+)*$`)
	prefixedReferencePattern = regexp.MustCompile(`\b(gg-[a-z0-9]+(?:-[a-z0-9]+)*)\b`)
	unprefixedDollarPattern  = regexp.MustCompile(`\$([a-z][a-z0-9]*(?:-[a-z0-9]+)*)([^a-z0-9-]|$)`)
	unprefixedSlashPattern   = regexp.MustCompile(`/([a-z][a-z0-9]*(?:-[a-z0-9]+)*)([^a-z0-9.-]|$)`)
	unprefixedSlashSentence  = regexp.MustCompile(`/([a-z][a-z0-9]*(?:-[a-z0-9]+)*)\.(?:\s|$)`)
	codeSpanPattern          = regexp.MustCompile("`([^`\\n]+)`")
)

type skillDocument struct {
	path     string
	identity string
	kind     string
}

func TestSkillSourceIdentity(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate the test source")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..", "skills")
	if err := validateSkillTree(root); err != nil {
		t.Fatal(err)
	}
}

func TestValidateSkillTreeFixtures(t *testing.T) {
	cases := []struct {
		name        string
		mutate      func(t *testing.T, root string)
		errorDetail string
	}{
		{name: "valid layouts", mutate: func(*testing.T, string) {}},
		{
			name:        "unprefixed path",
			errorDetail: `identity "alpha"`,
			mutate: func(t *testing.T, root string) {
				rename(t, filepath.Join(root, "canonical", "gg-alpha"), filepath.Join(root, "canonical", "alpha"))
			},
		},
		{
			name:        "duplicate prefix",
			errorDetail: `identity "gg-gg-alpha"`,
			mutate: func(t *testing.T, root string) {
				rename(t, filepath.Join(root, "canonical", "gg-alpha"), filepath.Join(root, "canonical", "gg-gg-alpha"))
				rename(t, filepath.Join(root, "canonical", "gg-gg-alpha", "gg-alpha.md"), filepath.Join(root, "canonical", "gg-gg-alpha", "gg-gg-alpha.md"))
			},
		},
		{
			name:        "canonical basename",
			errorDetail: "canonical source must contain exactly gg-alpha.md",
			mutate: func(t *testing.T, root string) {
				rename(t, filepath.Join(root, "canonical", "gg-alpha", "gg-alpha.md"), filepath.Join(root, "canonical", "gg-alpha", "README.md"))
			},
		},
		{
			name:        "Claude basename",
			errorDetail: "Claude skill identity must be a Markdown file",
			mutate: func(t *testing.T, root string) {
				rename(t, filepath.Join(root, "claude", "commands", "gg-alpha.md"), filepath.Join(root, "claude", "commands", "gg-alpha"))
			},
		},
		{
			name:        "Codex basename",
			errorDetail: "Codex source must contain exactly SKILL.md",
			mutate: func(t *testing.T, root string) {
				rename(t, filepath.Join(root, "codex", "skills", "gg-alpha", "SKILL.md"), filepath.Join(root, "codex", "skills", "gg-alpha", "README.md"))
			},
		},
		{
			name:        "core basename",
			errorDetail: "core source must be gg-coding-patterns.md",
			mutate: func(t *testing.T, root string) {
				rename(t, filepath.Join(root, "core", "gg-coding-patterns.md"), filepath.Join(root, "core", "coding-patterns.md"))
			},
		},
		{
			name:        "missing frontmatter",
			errorDetail: "missing YAML frontmatter",
			mutate: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "canonical", "gg-alpha", "gg-alpha.md"), "# Alpha\n")
			},
		},
		{
			name:        "invalid YAML frontmatter",
			errorDetail: "invalid YAML frontmatter",
			mutate: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "canonical", "gg-alpha", "gg-alpha.md"), "---\nname: [gg-alpha\n---\n# Alpha\n")
			},
		},
		{
			name:        "path and name mismatch",
			errorDetail: `frontmatter name "gg-beta"`,
			mutate: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "canonical", "gg-alpha", "gg-alpha.md"), "---\nname: gg-beta\n---\n# Alpha\n")
			},
		},
		{
			name:        "unprefixed local reference in code span",
			errorDetail: `local skill reference "alpha"`,
			mutate: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "canonical", "gg-alpha", "gg-alpha.md"), "---\nname: gg-alpha\n---\nUse `alpha`.\n")
			},
		},
		{
			name:        "unprefixed local dollar marker",
			errorDetail: `local skill reference "alpha"`,
			mutate: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "canonical", "gg-alpha", "gg-alpha.md"), "---\nname: gg-alpha\n---\nUse $alpha.\n")
			},
		},
		{
			name:        "unprefixed local slash marker",
			errorDetail: `local skill reference "alpha"`,
			mutate: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "canonical", "gg-alpha", "gg-alpha.md"), "---\nname: gg-alpha\n---\nUse /alpha.\n")
			},
		},
		{
			name:        "dangling plain prefixed reference",
			errorDetail: `reference "gg-missing"`,
			mutate: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "canonical", "gg-alpha", "gg-alpha.md"), "---\nname: gg-alpha\n---\nUse gg-missing.\n")
			},
		},
		{
			name:        "dangling dollar prefixed reference",
			errorDetail: `reference "gg-missing"`,
			mutate: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "canonical", "gg-alpha", "gg-alpha.md"), "---\nname: gg-alpha\n---\nUse $gg-missing.\n")
			},
		},
		{
			name:        "dangling slash prefixed reference",
			errorDetail: `reference "gg-missing"`,
			mutate: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "canonical", "gg-alpha", "gg-alpha.md"), "---\nname: gg-alpha\n---\nUse /gg-missing.\n")
			},
		},
		{
			name:        "missing adapted source",
			errorDetail: `adapted skill "gg-beta" has no canonical source`,
			mutate: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "claude", "commands", "gg-beta.md"), "---\nname: gg-beta\n---\n# Beta\n")
			},
		},
		{
			name:        "unexpected canonical child",
			errorDetail: "canonical source must contain exactly gg-alpha.md",
			mutate: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "canonical", "gg-alpha", "notes.md"), "# Notes\n")
			},
		},
		{
			name:        "missing core source",
			errorDetail: "core source must contain exactly gg-coding-patterns.md",
			mutate: func(t *testing.T, root string) {
				if err := os.Remove(filepath.Join(root, "core", "gg-coding-patterns.md")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:        "duplicate canonical and core identity",
			errorDetail: `duplicate local skill identity "gg-coding-patterns"`,
			mutate: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "canonical", "gg-coding-patterns", "gg-coding-patterns.md"), "---\nname: gg-coding-patterns\n---\n# Duplicate\n")
			},
		},
		{
			name:        "unprefixed local reference later in code span",
			errorDetail: `local skill reference "alpha"`,
			mutate: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "canonical", "gg-alpha", "gg-alpha.md"), "---\nname: gg-alpha\n---\nUse the `alpha` skill.\n")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := makeValidSkillTree(t)
			tc.mutate(t, root)
			err := validateSkillTree(root)
			if tc.name == "valid layouts" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("validateSkillTree accepted an invalid fixture")
			}
			if !strings.Contains(err.Error(), tc.errorDetail) {
				t.Fatalf("validateSkillTree error %q does not contain %q", err, tc.errorDetail)
			}
		})
	}
}

func validateSkillTree(root string) error {
	canonicalRoot := filepath.Join(root, "canonical")
	claudeRoot := filepath.Join(root, "claude", "commands")
	codexRoot := filepath.Join(root, "codex", "skills")
	coreRoot := filepath.Join(root, "core")

	documents := make([]skillDocument, 0)
	inventory := make(map[string]struct{})

	canonicalDocuments, err := collectCanonicalDocuments(canonicalRoot)
	if err != nil {
		return err
	}
	for _, document := range canonicalDocuments {
		if err := addInventoryIdentity(inventory, document.identity, document.path); err != nil {
			return err
		}
	}
	documents = append(documents, canonicalDocuments...)

	coreDocuments, err := collectCoreDocuments(coreRoot)
	if err != nil {
		return err
	}
	for _, document := range coreDocuments {
		if err := addInventoryIdentity(inventory, document.identity, document.path); err != nil {
			return err
		}
	}
	documents = append(documents, coreDocuments...)

	adaptedDocuments, err := collectAdaptedDocuments(claudeRoot, "claude")
	if err != nil {
		return err
	}
	documents = append(documents, adaptedDocuments...)
	codexDocuments, err := collectAdaptedDocuments(codexRoot, "codex")
	if err != nil {
		return err
	}
	documents = append(documents, codexDocuments...)

	if len(inventory) == 0 {
		return fmt.Errorf("%s: no canonical or core skill identities found", root)
	}
	for _, document := range documents {
		if document.kind != "canonical" && document.kind != "core" {
			if _, ok := inventory[document.identity]; !ok {
				return fmt.Errorf("%s: adapted skill %q has no canonical source", document.path, document.identity)
			}
		}
		if err := validateDocument(document, inventory); err != nil {
			return err
		}
	}
	return nil
}

func addInventoryIdentity(inventory map[string]struct{}, identity, path string) error {
	if _, exists := inventory[identity]; exists {
		return fmt.Errorf("%s: duplicate local skill identity %q", path, identity)
	}
	inventory[identity] = struct{}{}
	return nil
}

func collectCanonicalDocuments(root string) ([]skillDocument, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read canonical skills %s: %w", root, err)
	}
	documents := make([]skillDocument, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			return nil, fmt.Errorf("%s: canonical identity must be a directory", filepath.Join(root, entry.Name()))
		}
		identity := entry.Name()
		if err := validateIdentity(identity, filepath.Join(root, identity)); err != nil {
			return nil, err
		}
		dir := filepath.Join(root, identity)
		children, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("read canonical skill %s: %w", dir, err)
		}
		if len(children) != 1 || children[0].IsDir() || children[0].Name() != identity+".md" {
			return nil, fmt.Errorf("%s: canonical source must contain exactly %s.md", dir, identity)
		}
		documents = append(documents, skillDocument{path: filepath.Join(dir, children[0].Name()), identity: identity, kind: "canonical"})
	}
	return documents, nil
}

func collectCoreDocuments(root string) ([]skillDocument, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read core skills %s: %w", root, err)
	}
	if len(entries) != 1 {
		return nil, fmt.Errorf("%s: core source must contain exactly gg-coding-patterns.md", root)
	}
	documents := make([]skillDocument, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			return nil, fmt.Errorf("%s: core identity must be a Markdown file", filepath.Join(root, entry.Name()))
		}
		identity := strings.TrimSuffix(entry.Name(), ".md")
		if identity != "gg-coding-patterns" {
			return nil, fmt.Errorf("%s: core source must be gg-coding-patterns.md", filepath.Join(root, entry.Name()))
		}
		if err := validateIdentity(identity, filepath.Join(root, entry.Name())); err != nil {
			return nil, err
		}
		documents = append(documents, skillDocument{path: filepath.Join(root, entry.Name()), identity: identity, kind: "core"})
	}
	return documents, nil
}

func collectAdaptedDocuments(root, kind string) ([]skillDocument, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read %s skills %s: %w", kind, root, err)
	}
	documents := make([]skillDocument, 0, len(entries))
	for _, entry := range entries {
		identity := entry.Name()
		if kind == "claude" {
			if entry.IsDir() || filepath.Ext(identity) != ".md" {
				return nil, fmt.Errorf("%s: Claude skill identity must be a Markdown file", filepath.Join(root, identity))
			}
			identity = strings.TrimSuffix(identity, ".md")
			if err := validateIdentity(identity, filepath.Join(root, entry.Name())); err != nil {
				return nil, err
			}
			documents = append(documents, skillDocument{path: filepath.Join(root, entry.Name()), identity: identity, kind: kind})
			continue
		}

		if !entry.IsDir() {
			return nil, fmt.Errorf("%s: Codex skill identity must be a directory", filepath.Join(root, identity))
		}
		if err := validateIdentity(identity, filepath.Join(root, identity)); err != nil {
			return nil, err
		}
		dir := filepath.Join(root, identity)
		children, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("read Codex skill %s: %w", dir, err)
		}
		if len(children) != 1 || children[0].IsDir() || children[0].Name() != "SKILL.md" {
			return nil, fmt.Errorf("%s: Codex source must contain exactly SKILL.md", dir)
		}
		documents = append(documents, skillDocument{path: filepath.Join(dir, "SKILL.md"), identity: identity, kind: kind})
	}
	return documents, nil
}

func validateIdentity(identity, path string) error {
	if !skillIdentityPattern.MatchString(identity) || strings.Count(identity, "gg-") != 1 {
		return fmt.Errorf("%s: identity %q must carry exactly one gg- prefix", path, identity)
	}
	return nil
}

func validateDocument(document skillDocument, inventory map[string]struct{}) error {
	data, err := os.ReadFile(document.path)
	if err != nil {
		return fmt.Errorf("read %s: %w", document.path, err)
	}
	body, name, err := splitFrontmatter(data)
	if err != nil {
		return fmt.Errorf("%s: %w", document.path, err)
	}
	if name != document.identity {
		return fmt.Errorf("%s: frontmatter name %q does not match path identity %q", document.path, name, document.identity)
	}

	for _, match := range prefixedReferencePattern.FindAllSubmatch(body, -1) {
		reference := string(match[1])
		if _, ok := inventory[reference]; !ok {
			return fmt.Errorf("%s: reference %q does not resolve to a local skill", document.path, reference)
		}
	}
	for _, pattern := range []*regexp.Regexp{unprefixedDollarPattern, unprefixedSlashPattern, unprefixedSlashSentence} {
		for _, match := range pattern.FindAllStringSubmatch(string(body), -1) {
			candidate := match[1]
			if strings.HasPrefix(candidate, "gg-") {
				continue
			}
			if _, ok := inventory["gg-"+candidate]; ok {
				return fmt.Errorf("%s: local skill reference %q is missing the gg- prefix", document.path, candidate)
			}
		}
	}
	bodyText := string(body)
	for _, match := range codeSpanPattern.FindAllStringSubmatchIndex(bodyText, -1) {
		if inFencedCode(bodyText, match[0]) {
			continue
		}
		code := bodyText[match[2]:match[3]]
		lineStart := strings.LastIndex(bodyText[:match[0]], "\n") + 1
		line := bodyText[lineStart:]
		if lineEnd := strings.IndexByte(line, '\n'); lineEnd >= 0 {
			line = line[:lineEnd]
		}
		lowerLine := strings.ToLower(line)
		if strings.Contains(line, "Stable phase ID") || strings.Contains(line, "phase_id:") || strings.Contains(lowerLine, "phase") || strings.Contains(line, "Default:") {
			continue
		}
		for identity := range inventory {
			unprefixed := strings.TrimPrefix(identity, "gg-")
			if containsCodeToken(code, unprefixed) {
				return fmt.Errorf("%s: local skill reference %q in code span is missing the gg- prefix", document.path, unprefixed)
			}
		}
	}
	return nil
}

func splitFrontmatter(data []byte) ([]byte, string, error) {
	if !bytes.HasPrefix(data, []byte("---\n")) {
		return nil, "", fmt.Errorf("missing YAML frontmatter")
	}
	rest := data[len("---\n"):]
	end := bytes.Index(rest, []byte("\n---\n"))
	if end < 0 {
		return nil, "", fmt.Errorf("unterminated YAML frontmatter")
	}
	var metadata struct {
		Name *string `yaml:"name"`
	}
	if err := yaml.Unmarshal(rest[:end], &metadata); err != nil {
		return nil, "", fmt.Errorf("invalid YAML frontmatter: %w", err)
	}
	if metadata.Name == nil || strings.TrimSpace(*metadata.Name) == "" {
		return nil, "", fmt.Errorf("frontmatter is missing name")
	}
	body := rest[end+len("\n---\n"):]
	return body, *metadata.Name, nil
}

func containsCodeToken(code, token string) bool {
	trimmed := strings.Trim(strings.TrimSpace(code), "`.,;:()[]{}<>")
	if trimmed == token {
		return true
	}
	parts := strings.Fields(trimmed)
	return len(parts) > 0 && strings.Trim(parts[0], "`.,;:()[]{}<>") == token
}

func inFencedCode(text string, position int) bool {
	open := false
	for _, line := range strings.Split(text[:position], "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			open = !open
		}
	}
	return open
}

func makeValidSkillTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	canonical := "---\nname: gg-alpha\n---\n# Alpha\n\nValid references: gg-alpha, $gg-alpha, and /gg-alpha.\nPlain prose may mention alpha, test, review, or plan.md.\nStable phase ID: `test`\nExternal examples may use $python and /python.\n"
	core := "---\nname: gg-coding-patterns\n---\n# Patterns\n"
	writeFile(t, filepath.Join(root, "canonical", "gg-alpha", "gg-alpha.md"), canonical)
	writeFile(t, filepath.Join(root, "claude", "commands", "gg-alpha.md"), canonical)
	writeFile(t, filepath.Join(root, "codex", "skills", "gg-alpha", "SKILL.md"), canonical)
	writeFile(t, filepath.Join(root, "core", "gg-coding-patterns.md"), core)
	return root
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func rename(t *testing.T, oldPath, newPath string) {
	t.Helper()
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatal(err)
	}
}
