package update

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// InstallerPathEnv names an explicit override for the platform installer
	// script. The installer is intentionally not discovered from the current
	// directory; when the override is unset the trusted copy that the platform
	// installer persisted at install time is used instead.
	InstallerPathEnv = "GG_INSTALLER_PATH"
	// installerDirName is the gg-owned directory the platform installers copy
	// themselves into, so an update runs a script the user already executed
	// deliberately rather than freshly downloaded shell text.
	installerDirName = ".gg"
	installerTimeout = 5 * time.Minute
)

// PlatformInstaller runs the existing binary-only installer using explicit
// executable and argument vectors. It never invokes a shell command string.
type PlatformInstaller struct {
	platform string
	env      func(string) string
	home     func() (string, error)
	run      func(context.Context, string, []string, io.Writer) error
}

// NewPlatformInstaller constructs the production installer runner.
func NewPlatformInstaller() *PlatformInstaller {
	return &PlatformInstaller{platform: runtime.GOOS, env: os.Getenv, home: os.UserHomeDir, run: runInstallerProcess}
}

// Install runs the trusted platform installer exactly once. The installer is
// the copy persisted under ~/.gg at install time, or the explicit
// GG_INSTALLER_PATH override; neither guesses a source checkout nor silently
// executes downloaded shell text.
func (i *PlatformInstaller) Install(ctx context.Context, normalizedVersion string, args []string) error {
	if i == nil {
		return errors.New("installer is not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if normalizedVersion == "" || len(args) != 2 || args[0] != "--version" || args[1] != normalizedVersion {
		return errors.New("installer received invalid explicit version arguments")
	}

	var installerName, commandName string
	switch i.platform {
	case "linux", "darwin":
		installerName, commandName = "install.sh", "bash"
	case "windows":
		installerName, commandName = "install.ps1", "powershell.exe"
	default:
		return fmt.Errorf("unsupported installer platform %q", i.platform)
	}
	path, err := i.resolveInstallerPath(installerName)
	if err != nil {
		return err
	}
	commandArgs := []string{path, "--version", normalizedVersion}
	if i.platform == "windows" {
		commandArgs = []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", path, "-Version", normalizedVersion}
	}

	installCtx, cancel := context.WithTimeout(ctx, installerTimeout)
	defer cancel()
	var output bytes.Buffer
	if err := i.run(installCtx, commandName, commandArgs, &output); err != nil {
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

// resolveInstallerPath returns the trusted installer path: the explicit
// GG_INSTALLER_PATH override when set, otherwise the copy the platform
// installer persisted under ~/.gg. A missing copy is reported with the action
// that restores it, because the update cannot proceed without one.
func (i *PlatformInstaller) resolveInstallerPath(installerName string) (string, error) {
	if override := strings.TrimSpace(i.env(InstallerPathEnv)); override != "" {
		if !filepath.IsAbs(override) {
			return "", fmt.Errorf("%s must be an absolute path: %q", InstallerPathEnv, override)
		}
		return override, nil
	}
	home, err := i.home()
	if err != nil {
		return "", fmt.Errorf("locate home directory for the trusted installer: %w", err)
	}
	path := filepath.Join(home, installerDirName, installerName)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("no trusted installer at %s; re-run the gg installer to restore it, or set %s to an inspected %s", path, InstallerPathEnv, installerName)
	}
	return path, nil
}

func runInstallerProcess(ctx context.Context, name string, args []string, output io.Writer) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = output
	command.Stderr = output
	return command.Run()
}

var _ Installer = (*PlatformInstaller)(nil)
