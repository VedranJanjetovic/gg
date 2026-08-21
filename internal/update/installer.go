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
	// InstallerPathEnv names the explicit path to the platform installer script.
	// The installer is intentionally not discovered from the current directory.
	InstallerPathEnv = "GG_INSTALLER_PATH"
	installerTimeout = 5 * time.Minute
)

// PlatformInstaller runs the existing binary-only installer using explicit
// executable and argument vectors. It never invokes a shell command string.
type PlatformInstaller struct {
	platform string
	env      func(string) string
	run      func(context.Context, string, []string, io.Writer) error
}

// NewPlatformInstaller constructs the production installer runner.
func NewPlatformInstaller() *PlatformInstaller {
	return &PlatformInstaller{platform: runtime.GOOS, env: os.Getenv, run: runInstallerProcess}
}

// Install runs the configured platform installer exactly once. path must be
// supplied via GG_INSTALLER_PATH; this avoids guessing a source checkout or
// silently executing downloaded shell text.
func (i *PlatformInstaller) Install(ctx context.Context, normalizedVersion string, args []string) error {
	if i == nil {
		return errors.New("installer is not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	path := strings.TrimSpace(i.env(InstallerPathEnv))
	if path == "" {
		return fmt.Errorf("%s is not set; set it to the trusted gg binary installer path", InstallerPathEnv)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%s must be an absolute path: %q", InstallerPathEnv, path)
	}
	if normalizedVersion == "" || len(args) != 2 || args[0] != "--version" || args[1] != normalizedVersion {
		return errors.New("installer received invalid explicit version arguments")
	}

	commandName := "bash"
	commandArgs := []string{path, "--version", normalizedVersion}
	switch i.platform {
	case "windows":
		commandName = "powershell.exe"
		commandArgs = []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", path, "-Version", normalizedVersion}
	case "linux", "darwin":
	default:
		return fmt.Errorf("unsupported installer platform %q", i.platform)
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

func runInstallerProcess(ctx context.Context, name string, args []string, output io.Writer) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = output
	command.Stderr = output
	return command.Run()
}

var _ Installer = (*PlatformInstaller)(nil)
