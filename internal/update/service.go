// Package update owns gg self-update behavior.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	// DefaultReleaseSource is the GitHub Releases API endpoint for gg's source
	// repository. The endpoint is a contract: its latest release JSON must have a
	// non-empty tag_name using the canonical gg-vX.Y.Z convention.
	DefaultReleaseSource = "https://api.github.com/repos/VedranJanjetovic/gg/releases/latest"
	DefaultReleasePage   = "https://github.com/VedranJanjetovic/gg/releases"
	// installerSourceBase is the raw content host the platform installer is
	// fetched from. The release tag is appended, so an update always runs the
	// installer from the same commit as the binary it installs.
	installerSourceBase = "https://raw.githubusercontent.com/VedranJanjetovic/gg"
)

type ReleaseLookup interface {
	LatestRelease(context.Context) (string, error)
}

// StatusRunning is the only project status that blocks installation.
const StatusRunning = "running"

// ProjectStatus is the minimal lifecycle data needed to gate installation.
// Status is compared exactly; in particular, only StatusRunning blocks updates.
type ProjectStatus struct {
	Status string
}

// ProjectStatusLister lists project lifecycle statuses for update gating.
type ProjectStatusLister interface {
	List(context.Context) ([]ProjectStatus, error)
}

// Installer installs an explicit normalized version. The service deliberately
// does not compose the platform-specific command, its argument vector, or the
// destination: only the installer knows where the running binary lives.
type Installer interface {
	Install(context.Context, string) error
}

type Result struct {
	Current string
	Latest  string
	Action  string
	Message string
}

type Service struct {
	lookup    ReleaseLookup
	current   func() string
	projects  ProjectStatusLister
	installer Installer
}

func NewService() *Service {
	return &Service{lookup: NewHTTPReleaseLookup(nil, DefaultReleaseSource), current: func() string { return strings.TrimSpace(os.Getenv("GG_VERSION")) }}
}

// ServiceOption configures optional update policy dependencies.
type ServiceOption func(*Service)

// WithProjectStatusLister enables the running-project update gate.
func WithProjectStatusLister(lister ProjectStatusLister) ServiceOption {
	return func(service *Service) { service.projects = lister }
}

// WithInstaller enables platform installation for newer releases.
func WithInstaller(installer Installer) ServiceOption {
	return func(service *Service) { service.installer = installer }
}

func NewServiceWithDependencies(current func() string, lookup ReleaseLookup, options ...ServiceOption) *Service {
	service := NewService()
	if current != nil {
		service.current = current
	}
	if lookup != nil {
		service.lookup = lookup
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

// Available reports whether a released version newer than the running binary
// exists. It never installs anything, so read-only callers (the TUI footer)
// can advertise `gg update` without changing the binary under the user.
func (s *Service) Available(ctx context.Context) (bool, error) {
	_, _, newer, err := s.compare(ctx)
	return newer, err
}

// compare resolves the running version against the latest release. An
// unrecognized current version is not an error: it reports "no newer release"
// so callers never act on a version they cannot order.
func (s *Service) compare(ctx context.Context) (Result, semanticVersion, bool, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, semanticVersion{}, false, err
	}
	if s == nil || s.lookup == nil || s.current == nil {
		return Result{}, semanticVersion{}, false, errors.New("update service is not configured")
	}
	current := strings.TrimSpace(s.current())
	currentVersion, ok := parseVersion(current)
	if !ok {
		return Result{Current: current, Action: "unrecognized"}, semanticVersion{}, false, nil
	}
	latestText, err := s.lookup.LatestRelease(ctx)
	if err != nil {
		return Result{Current: current}, semanticVersion{}, false, fmt.Errorf("check latest release: %w", err)
	}
	latestVersion, ok := parseVersion(latestText)
	if !ok {
		return Result{Current: current, Latest: latestText}, semanticVersion{}, false, fmt.Errorf("latest release %q is not a semantic version", latestText)
	}
	result := Result{Current: current, Latest: latestText}
	if compareVersion(latestVersion, currentVersion) <= 0 {
		result.Action = "current"
		return result, latestVersion, false, nil
	}
	return result, latestVersion, true, nil
}

func (s *Service) Update(ctx context.Context) (Result, error) {
	result, latestVersion, newer, err := s.compare(ctx)
	if err != nil || !newer {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	// Without both injected seams, retain the existing manual-release behavior.
	if s.projects == nil || s.installer == nil {
		result.Action = "manual"
		return result, nil
	}
	projects, err := s.projects.List(ctx)
	if err != nil {
		return result, fmt.Errorf("list project statuses: %w", err)
	}
	for _, project := range projects {
		if project.Status == StatusRunning {
			result.Action = "blocked"
			result.Message = "update blocked: stop running projects first with gg stop-all, then retry gg update"
			return result, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	normalizedVersion := formatVersion(latestVersion)
	if err := s.installer.Install(ctx, normalizedVersion); err != nil {
		return result, fmt.Errorf("install release %s: %w", normalizedVersion, err)
	}
	result.Action = "installed"
	result.Message = fmt.Sprintf("installed gg %s", normalizedVersion)
	return result, nil
}

type HTTPReleaseLookup struct {
	client *http.Client
	source string
}

func NewHTTPReleaseLookup(client *http.Client, source string) *HTTPReleaseLookup {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	if source == "" {
		source = DefaultReleaseSource
	}
	return &HTTPReleaseLookup{client: client, source: source}
}
func (l *HTTPReleaseLookup) LatestRelease(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.source, nil)
	if err != nil {
		return "", fmt.Errorf("create release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	response, err := l.client.Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
		return "", fmt.Errorf("release source returned HTTP %d", response.StatusCode)
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&release); err != nil {
		return "", fmt.Errorf("decode release response: %w", err)
	}
	if strings.TrimSpace(release.TagName) == "" {
		return "", errors.New("release response has no tag_name")
	}
	return strings.TrimSpace(release.TagName), nil
}

func ManualInstructions(latest string) string {
	tag := canonicalReleaseTag(latest)
	version := strings.TrimPrefix(tag, "gg-v")
	return fmt.Sprintf("gg %s is available. Download the %s release from %s/tag/%s and replace your gg binary.", version, tag, DefaultReleasePage, tag)
}

func canonicalReleaseTag(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "gg-")
	value = strings.TrimPrefix(value, "v")
	return "gg-v" + value
}

type semanticVersion struct {
	major, minor, patch int
	prerelease          string
}

func parseVersion(value string) (semanticVersion, bool) {
	value = strings.TrimSpace(strings.TrimPrefix(value, "gg-v"))
	value = strings.TrimPrefix(value, "v")
	parts := strings.SplitN(value, "+", 2)
	value = parts[0]
	corePre := strings.SplitN(value, "-", 2)
	core := strings.Split(corePre[0], ".")
	if len(core) != 3 {
		return semanticVersion{}, false
	}
	var numbers [3]int
	for i, part := range core {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return semanticVersion{}, false
		}
		n := 0
		for _, r := range part {
			if r < '0' || r > '9' {
				return semanticVersion{}, false
			}
			n = n*10 + int(r-'0')
		}
		numbers[i] = n
	}
	pre := ""
	if len(corePre) == 2 {
		pre = corePre[1]
		if pre == "" {
			return semanticVersion{}, false
		}
	}
	return semanticVersion{numbers[0], numbers[1], numbers[2], pre}, true
}
func formatVersion(version semanticVersion) string {
	value := fmt.Sprintf("%d.%d.%d", version.major, version.minor, version.patch)
	if version.prerelease != "" {
		value += "-" + version.prerelease
	}
	return value
}

func compareVersion(a, b semanticVersion) int {
	for _, pair := range [][2]int{{a.major, b.major}, {a.minor, b.minor}, {a.patch, b.patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if a.prerelease == b.prerelease {
		return 0
	}
	if a.prerelease == "" {
		return 1
	}
	if b.prerelease == "" {
		return -1
	}
	return comparePrerelease(a.prerelease, b.prerelease)
}
func comparePrerelease(a, b string) int {
	aParts, bParts := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(aParts) && i < len(bParts); i++ {
		left, right := aParts[i], bParts[i]
		leftNum, leftOK := numericIdentifier(left)
		rightNum, rightOK := numericIdentifier(right)
		if leftOK && rightOK {
			if leftNum < rightNum {
				return -1
			}
			if leftNum > rightNum {
				return 1
			}
			continue
		}
		if leftOK != rightOK {
			if leftOK {
				return -1
			}
			return 1
		}
		if left < right {
			return -1
		}
		if left > right {
			return 1
		}
	}
	if len(aParts) < len(bParts) {
		return -1
	}
	return 1
}
func numericIdentifier(value string) (int, bool) {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return 0, false
	}
	n := 0
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}
