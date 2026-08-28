package update

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// InstallerSourceEnv overrides the base URL the platform installer is
	// fetched from. The version tag path is always appended, so an override
	// still installs the installer that shipped with the requested release.
	// A fork points this at its own raw content host; a test points it at a
	// local fixture server.
	InstallerSourceEnv  = "GG_INSTALLER_SOURCE"
	installerTimeout    = 5 * time.Minute
	installerFetchLimit = 1 << 20
)

// ScriptInstaller fetches the platform installer pinned to the release tag
// being installed and runs it with explicit version and destination arguments.
//
// The installer is fetched rather than read from a persisted copy on purpose.
// A copy saved at first-install time can never be improved: on update it is
// both the running script and the persistence source, so it rewrites itself
// unchanged and every later update repeats the same stale logic. Pinning the
// fetch to the release tag replaces that with a stronger property — the
// installer and the binary it installs always come from the same commit, so
// they cannot skew.
//
// The fetched script is written to a temporary file and passed to the
// interpreter as an argv element. It is never piped to the interpreter's stdin
// and never composed into a shell command string.
type ScriptInstaller struct {
	platform   string
	env        func(string) string
	executable func() (string, error)
	fetch      func(context.Context, string) ([]byte, error)
	run        func(context.Context, string, []string, io.Writer) error
	tempDir    string
}

// NewScriptInstaller constructs the production installer runner.
func NewScriptInstaller() *ScriptInstaller {
	return &ScriptInstaller{
		platform:   runtime.GOOS,
		env:        os.Getenv,
		executable: os.Executable,
		fetch:      fetchInstallerScript,
		run:        runInstallerProcess,
	}
}

// installerScript names the platform installer and the staging pattern used to
// write it. The pattern's extension is load-bearing on Windows: PowerShell
// -File rejects a script whose name does not end in .ps1.
type installerScript struct {
	name    string
	pattern string
}

func scriptForPlatform(platform string) (installerScript, error) {
	switch platform {
	case "linux", "darwin":
		return installerScript{name: "install.sh", pattern: "gg-installer-*.sh"}, nil
	case "windows":
		return installerScript{name: "install.ps1", pattern: "gg-installer-*.ps1"}, nil
	default:
		return installerScript{}, fmt.Errorf("unsupported installer platform %q", platform)
	}
}

// Install fetches the installer for normalizedVersion and runs it once against
// the directory holding the running executable.
func (i *ScriptInstaller) Install(ctx context.Context, normalizedVersion string) error {
	if i == nil {
		return errors.New("installer is not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if normalizedVersion == "" {
		return errors.New("installer received an empty release version")
	}
	script, err := scriptForPlatform(i.platform)
	if err != nil {
		return err
	}
	prefix, err := i.resolveDestinationPrefix()
	if err != nil {
		return err
	}
	url, err := i.installerURL(script.name, normalizedVersion)
	if err != nil {
		return err
	}
	body, err := i.fetchScript(ctx, url)
	if err != nil {
		return err
	}
	path, remove, err := i.stageScript(script.pattern, body)
	if err != nil {
		return err
	}
	defer remove()
	return i.runScript(ctx, path, normalizedVersion, prefix)
}

// resolveDestinationPrefix returns the directory holding the running
// executable, which is where the update must land. Deriving it from the
// running binary rather than from the environment is what keeps an update in
// the place the user actually installed gg: the installer's own default prefix
// comes from XDG_BIN_HOME or ~/.local/bin, so a gg installed with an explicit
// --prefix would otherwise be updated somewhere else while the gg on PATH
// stayed stale.
//
// Every failure here is reported rather than worked around. A silent fallback
// to a guessed directory is the exact defect this function exists to prevent.
func (i *ScriptInstaller) resolveDestinationPrefix() (string, error) {
	executable, err := i.executable()
	if err != nil {
		return "", fmt.Errorf("locate the running gg executable: %w", err)
	}
	// os.Executable resolves symlinks on Linux, where it reads /proc/self/exe,
	// but not on darwin, where it comes from _NSGetExecutablePath. Resolving
	// explicitly makes both platforms target the same file, and it also
	// guarantees the returned directory has no symlinked components, which the
	// installer itself refuses.
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		// A replaced or deleted image makes /proc/self/exe read
		// "<path> (deleted)", a suffix Go does not strip; resolution fails on
		// that path rather than producing a plausible-looking directory.
		return "", fmt.Errorf("resolve the running gg executable %q: %w", executable, err)
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect the running gg executable %q: %w", resolved, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("running gg executable %q is not a regular file; reinstall gg with the installer before updating", resolved)
	}
	prefix := filepath.Dir(resolved)
	if err := assertWritableDirectory(prefix); err != nil {
		return "", err
	}
	return prefix, nil
}

// assertWritableDirectory reports whether the update can write to dir by
// writing to it, because permission bits alone do not answer the question on
// Windows. This catches a root-owned or package-manager-owned destination
// before the download rather than after it.
func assertWritableDirectory(dir string) error {
	probe, err := os.CreateTemp(dir, ".gg-update-probe-*")
	if err != nil {
		return fmt.Errorf("cannot write to %q, so gg cannot update the binary installed there; re-run the installer with GG_INSTALL_PREFIX set to a writable directory, or with the privileges that directory requires: %w", dir, err)
	}
	name := probe.Name()
	if err := probe.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("close write probe in %q: %w", dir, err)
	}
	return os.Remove(name)
}

// installerURL composes the tag-pinned installer URL. The tag path element is
// appended even under an override, so an override cannot silently downgrade the
// fetch to a moving branch tip.
func (i *ScriptInstaller) installerURL(name, normalizedVersion string) (string, error) {
	override := strings.TrimRight(strings.TrimSpace(i.env(InstallerSourceEnv)), "/")
	base := override
	if base == "" {
		base = installerSourceBase
	}
	if override != "" && !strings.HasPrefix(override, "https://") && !strings.HasPrefix(override, "http://") {
		return "", fmt.Errorf("%s must be an http or https base URL: %q", InstallerSourceEnv, override)
	}
	return fmt.Sprintf("%s/gg-v%s/%s", base, normalizedVersion, name), nil
}

// fetchScript retrieves the installer and rejects a body that is not a script.
// The check that matters is the HTML one: a missing tag or a redirected host
// answers with a page, and handing that to bash is worse than failing.
func (i *ScriptInstaller) fetchScript(ctx context.Context, url string) ([]byte, error) {
	body, err := i.fetch(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("fetch installer %s: %w", url, err)
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("installer %s is empty", url)
	}
	if trimmed[0] == '<' {
		return nil, fmt.Errorf("installer %s returned a document rather than a script; refusing to run it", url)
	}
	return body, nil
}

// stageScript writes the installer to a private temporary file and returns a
// remove function that runs on every exit path.
func (i *ScriptInstaller) stageScript(pattern string, body []byte) (string, func(), error) {
	file, err := os.CreateTemp(i.tempDir, pattern)
	if err != nil {
		return "", nil, fmt.Errorf("stage the fetched installer: %w", err)
	}
	path := file.Name()
	remove := func() { _ = os.Remove(path) }
	if _, err := file.Write(body); err != nil {
		_ = file.Close()
		remove()
		return "", nil, fmt.Errorf("write the fetched installer to %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		remove()
		return "", nil, fmt.Errorf("close the staged installer %q: %w", path, err)
	}
	return path, remove, nil
}

func (i *ScriptInstaller) runScript(ctx context.Context, path, normalizedVersion, prefix string) error {
	name := "bash"
	args := []string{path, "--version", normalizedVersion, "--prefix", prefix}
	if i.platform == "windows" {
		name = "powershell.exe"
		args = []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", path, "-Version", normalizedVersion, "-Prefix", prefix}
	}
	installCtx, cancel := context.WithTimeout(ctx, installerTimeout)
	defer cancel()
	var output bytes.Buffer
	if err := i.run(installCtx, name, args, &output); err != nil {
		if errors.Is(installCtx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("run %s installer: timed out after %s", i.platform, installerTimeout)
		}
		if errors.Is(installCtx.Err(), context.Canceled) {
			return context.Canceled
		}
		message := strings.TrimSpace(output.String())
		if message != "" {
			return fmt.Errorf("run %s installer: %w: %s", i.platform, err, message)
		}
		return fmt.Errorf("run %s installer: %w", i.platform, err)
	}
	return nil
}

func fetchInstallerScript(ctx context.Context, url string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create installer request: %w", err)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
		return nil, fmt.Errorf("installer source returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, installerFetchLimit))
	if err != nil {
		return nil, fmt.Errorf("read installer body: %w", err)
	}
	return body, nil
}

func runInstallerProcess(ctx context.Context, name string, args []string, output io.Writer) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = output
	command.Stderr = output
	return command.Run()
}

var _ Installer = (*ScriptInstaller)(nil)
