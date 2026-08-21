//go:build linux

package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	seccompSetModeFilter = 1
	seccompRetErrno      = 0x00050000
	seccompRetAllow      = 0x7fff0000

	networkDeniedHelperEnv  = "GG_E2E_NETWORK_DENIED_HELPER"
	networkDeniedModeEnv    = "GG_E2E_NETWORK_DENIED_MODE"
	networkDeniedTargetEnv  = "GG_E2E_NETWORK_DENIED_TARGET"
	networkDeniedArgsEnv    = "GG_E2E_NETWORK_DENIED_ARGS"
	networkDeniedAddressEnv = "GG_E2E_NETWORK_DENIED_ADDRESS"
)

func networkDeniedSupported() bool { return runtimeLinuxSeccompSupported() }

// TestNetworkDeniedHelper is a test-only child entrypoint. It installs a
// process-local seccomp filter before either making a direct dial or execing
// the real CLI. No production binary or global network state is changed.
func TestNetworkDeniedHelper(t *testing.T) {
	if os.Getenv(networkDeniedHelperEnv) != "1" {
		return
	}
	if err := installNetworkDeny(); err != nil {
		t.Fatalf("install network deny: %v", err)
	}
	switch os.Getenv(networkDeniedModeEnv) {
	case "dial":
		conn, err := net.DialTimeout("tcp", os.Getenv(networkDeniedAddressEnv), 2*time.Second)
		if err == nil {
			_ = conn.Close()
			t.Fatalf("direct dial unexpectedly succeeded")
		}
		fmt.Printf("network denied: %v\n", err)
	case "exec":
		var args []string
		encoded := os.Getenv(networkDeniedArgsEnv)
		data, err := base64.RawStdEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatalf("decode exec args: %v", err)
		}
		if err := json.Unmarshal(data, &args); err != nil || len(args) == 0 {
			t.Fatalf("decode exec args: %v", err)
		}
		if err := syscall.Exec(os.Getenv(networkDeniedTargetEnv), args, os.Environ()); err != nil {
			t.Fatalf("exec denied command: %v", err)
		}
	default:
		t.Fatalf("unknown network-denied helper mode %q", os.Getenv(networkDeniedModeEnv))
	}
}

func networkDeniedCommand(dir string, env []string, name string, args ...string) (*exec.Cmd, error) {
	if !networkDeniedSupported() {
		return nil, errors.New("Linux seccomp network denial is unavailable on this architecture")
	}
	merged := mergeEnv(os.Environ(), env)
	target := resolveCommand(name, merged)
	encodedArgs, err := json.Marshal(append([]string{target}, args...))
	if err != nil {
		return nil, fmt.Errorf("encode network-denied command: %w", err)
	}
	helper, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate network-denied helper: %w", err)
	}
	cmd := exec.Command(helper, "-test.run=^TestNetworkDeniedHelper$", "-test.v")
	cmd.Dir = dir
	cmd.Env = mergeEnv(merged, []string{
		networkDeniedHelperEnv + "=1",
		networkDeniedModeEnv + "=exec",
		networkDeniedTargetEnv + "=" + target,
		networkDeniedArgsEnv + "=" + base64.RawStdEncoding.EncodeToString(encodedArgs),
	})
	configureCommand(cmd)
	return cmd, nil
}

func RunWithInputNetworkDeniedTimeout(t *testing.T, dir string, env []string, input io.Reader, name string, args ...string) CommandResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), defaultCommandTimeout)
	defer cancel()
	cmd, err := networkDeniedCommand(dir, env, name, args...)
	if err != nil {
		t.Skipf("network-denied E2E unsupported: %v", err)
	}
	cmd.Stdin = input
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		return CommandResult{Err: err, ExitCode: -1}
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-waitCh:
	case <-ctx.Done():
		_ = terminateCommand(cmd)
		waitErr = errors.Join(<-waitCh, ctx.Err())
	}
	result := CommandResult{Stdout: stdout.String(), Stderr: stderr.String(), Err: waitErr}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	} else {
		result.ExitCode = -1
	}
	return result
}

func TestNetworkDeniedDirectConnectionHasZeroListenerAccepts(t *testing.T) {
	if !networkDeniedSupported() {
		t.Skip("Linux seccomp network denial is unavailable on this architecture")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestNetworkDeniedHelper$", "-test.v")
	cmd.Env = mergeEnv(os.Environ(), []string{
		networkDeniedHelperEnv + "=1", networkDeniedModeEnv + "=dial",
		networkDeniedAddressEnv + "=" + listener.Addr().String(),
	})
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("denied dial helper failed: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "network denied:") {
		t.Fatalf("helper did not prove direct dial denial: %q", stdout.String())
	}
	_ = listener.(*net.TCPListener).SetDeadline(time.Now().Add(100 * time.Millisecond))
	conn, err := listener.Accept()
	if err == nil {
		conn.Close()
		t.Fatal("listener accepted a connection despite socket denial")
	}
	if !errors.Is(err, os.ErrDeadlineExceeded) && !errors.Is(err, syscall.EAGAIN) {
		t.Fatalf("listener error = %v, want deadline with zero accepts", err)
	}
}

func runtimeLinuxSeccompSupported() bool {
	return runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64"
}

func installNetworkDeny() error {
	if !runtimeLinuxSeccompSupported() {
		return errors.New("unsupported Linux architecture")
	}
	if _, _, errno := unix.Syscall6(unix.SYS_PRCTL, unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0, 0); errno != 0 {
		return errno
	}
	filter := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: uint32(unix.SYS_SOCKET), Jt: 0, Jf: 1},
		{Code: unix.BPF_RET | unix.BPF_K, K: seccompRetErrno | uint32(unix.EPERM)},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: uint32(unix.SYS_SOCKETPAIR), Jt: 0, Jf: 1},
		{Code: unix.BPF_RET | unix.BPF_K, K: seccompRetErrno | uint32(unix.EPERM)},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: uint32(unix.SYS_CONNECT), Jt: 0, Jf: 1},
		{Code: unix.BPF_RET | unix.BPF_K, K: seccompRetErrno | uint32(unix.EPERM)},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: uint32(unix.SYS_SENDTO), Jt: 0, Jf: 1},
		{Code: unix.BPF_RET | unix.BPF_K, K: seccompRetErrno | uint32(unix.EPERM)},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: uint32(unix.SYS_SENDMSG), Jt: 0, Jf: 1},
		{Code: unix.BPF_RET | unix.BPF_K, K: seccompRetErrno | uint32(unix.EPERM)},
		{Code: unix.BPF_RET | unix.BPF_K, K: seccompRetAllow},
	}
	prog := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	if _, _, errno := unix.Syscall6(unix.SYS_SECCOMP, seccompSetModeFilter, 0, uintptr(unsafe.Pointer(&prog)), 0, 0, 0); errno != 0 {
		return errno
	}
	return nil
}
