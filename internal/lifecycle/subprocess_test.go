package lifecycle

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const helperModeEnv = "CO_SIGNER_LIFECYCLE_LOCK_HELPER"

func TestLifetimeLockHelperProcess(t *testing.T) {
	mode := os.Getenv(helperModeEnv)
	if mode == "" {
		return
	}
	path := os.Getenv("CO_SIGNER_LIFECYCLE_LOCK_PATH")
	switch mode {
	case "owner":
		lock, err := AcquireLifetimeLock(path)
		if err != nil {
			fmt.Printf("ERROR %v\n", err)
			return
		}
		defer lock.Close()
		fmt.Println("READY")
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			switch scanner.Text() {
			case "drain":
				fmt.Println("DRAINING")
			case "exec-child":
				child := helperCommand("exec-child", "")
				child.Stdout = os.Stdout
				child.Stderr = os.Stderr
				if err := child.Start(); err != nil {
					fmt.Printf("ERROR %v\n", err)
					return
				}
				fmt.Printf("CHILD %d\n", child.Process.Pid)
			case "release":
				fmt.Println("RELEASING")
				return
			}
		}
	case "contender":
		lock, err := AcquireLifetimeLock(path)
		if errors.Is(err, ErrLockHeld) {
			fmt.Println("LOCKED")
			return
		}
		if err != nil {
			fmt.Printf("ERROR %v\n", err)
			return
		}
		defer lock.Close()
		if marker := os.Getenv("CO_SIGNER_LIFECYCLE_BACKEND_MARKER"); marker != "" {
			_ = os.WriteFile(marker, []byte("backend accessed"), 0o600)
		}
		fmt.Println("ACQUIRED")
	case "exec-child":
		ctx, stop := context.WithCancel(context.Background())
		defer stop()
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
		defer signal.Stop(signals)
		fmt.Println("EXEC_READY")
		select {
		case <-ctx.Done():
		case <-signals:
		}
	default:
		fmt.Printf("ERROR unknown helper mode %q\n", mode)
	}
}

func TestLifetimeLockRealProcessContentionPreventsLoserBackendAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "co-signer.lock")
	owner := startLockOwner(t, path)
	defer owner.stop(t)

	marker := filepath.Join(t.TempDir(), "backend-accessed")
	contender := helperCommand("contender", path)
	contender.Env = append(contender.Env, "CO_SIGNER_LIFECYCLE_BACKEND_MARKER="+marker)
	output, err := contender.CombinedOutput()
	if err != nil {
		t.Fatalf("contender error = %v, output = %s", err, output)
	}
	if !strings.HasPrefix(string(output), "LOCKED\n") {
		t.Fatalf("contender output = %q, want LOCKED", output)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("loser reached backend marker: %v", err)
	}
}

func TestLifetimeLockRealProcessGracefulAndSIGKILLRelease(t *testing.T) {
	tests := []struct {
		name string
		stop func(*testing.T, *lockOwner)
	}{
		{name: "graceful", stop: func(t *testing.T, owner *lockOwner) { owner.release(t) }},
		{name: "sigkill", stop: func(t *testing.T, owner *lockOwner) { owner.kill(t) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "co-signer.lock")
			owner := startLockOwner(t, path)
			tt.stop(t, owner)

			replacement, err := AcquireLifetimeLock(path)
			if err != nil {
				t.Fatalf("AcquireLifetimeLock(after process death) error = %v", err)
			}
			if err := replacement.Close(); err != nil {
				t.Fatalf("replacement.Close() error = %v", err)
			}
		})
	}
}

func TestLifetimeLockRealProcessRetainsOwnershipDuringControlledDrain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "co-signer.lock")
	owner := startLockOwner(t, path)
	defer owner.stop(t)
	owner.send(t, "drain")
	owner.expect(t, "DRAINING")

	contender := helperCommand("contender", path)
	output, err := contender.CombinedOutput()
	if err != nil {
		t.Fatalf("contender error = %v, output = %s", err, output)
	}
	if !strings.HasPrefix(string(output), "LOCKED\n") {
		t.Fatalf("contender output = %q, want LOCKED during drain", output)
	}
	owner.release(t)
}

func TestLifetimeLockRealProcessDoesNotSurviveExec(t *testing.T) {
	path := filepath.Join(t.TempDir(), "co-signer.lock")
	owner := startLockOwner(t, path)
	owner.send(t, "exec-child")
	line := owner.read(t)
	if !strings.HasPrefix(line, "CHILD ") {
		owner.stop(t)
		t.Fatalf("helper output = %q, want CHILD pid", line)
	}
	pid, err := strconv.Atoi(strings.TrimPrefix(line, "CHILD "))
	if err != nil {
		owner.stop(t)
		t.Fatalf("parse child pid: %v", err)
	}
	child, err := os.FindProcess(pid)
	if err != nil {
		owner.stop(t)
		t.Fatalf("FindProcess(%d): %v", pid, err)
	}
	defer child.Signal(syscall.SIGKILL)

	if line = owner.read(t); line != "EXEC_READY" {
		owner.stop(t)
		t.Fatalf("exec child output = %q, want EXEC_READY", line)
	}
	owner.release(t)

	replacement, err := AcquireLifetimeLock(path)
	if err != nil {
		t.Fatalf("exec child inherited lifetime lock: %v", err)
	}
	if err := replacement.Close(); err != nil {
		t.Fatalf("replacement.Close() error = %v", err)
	}
}

type lockOwner struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lines  chan string
	waited bool
}

func startLockOwner(t *testing.T, path string) *lockOwner {
	t.Helper()
	cmd := helperCommand("owner", path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe() error = %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe() error = %v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("helper Start() error = %v", err)
	}
	owner := &lockOwner{cmd: cmd, stdin: stdin, lines: make(chan string, 8)}
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			owner.lines <- scanner.Text()
		}
		close(owner.lines)
	}()
	owner.expect(t, "READY")
	return owner
}

func helperCommand(mode, path string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestLifetimeLockHelperProcess$")
	cmd.Env = append(os.Environ(), helperModeEnv+"="+mode)
	if path != "" {
		cmd.Env = append(cmd.Env, "CO_SIGNER_LIFECYCLE_LOCK_PATH="+path)
	}
	return cmd
}

func (o *lockOwner) send(t *testing.T, command string) {
	t.Helper()
	if _, err := io.WriteString(o.stdin, command+"\n"); err != nil {
		t.Fatalf("send helper command: %v", err)
	}
}

func (o *lockOwner) expect(t *testing.T, want string) {
	t.Helper()
	if got := o.read(t); got != want {
		t.Fatalf("helper output = %q, want %q", got, want)
	}
}

func (o *lockOwner) read(t *testing.T) string {
	t.Helper()
	select {
	case line, ok := <-o.lines:
		if !ok {
			t.Fatal("helper output closed before synchronization boundary")
		}
		return line
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for helper synchronization boundary")
		return ""
	}
}

func (o *lockOwner) release(t *testing.T) {
	t.Helper()
	if o.waited {
		return
	}
	o.send(t, "release")
	o.expect(t, "RELEASING")
	if err := o.cmd.Wait(); err != nil {
		t.Fatalf("graceful helper Wait() error = %v", err)
	}
	o.waited = true
}

func (o *lockOwner) kill(t *testing.T) {
	t.Helper()
	if o.waited {
		return
	}
	if err := o.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("helper SIGKILL error = %v", err)
	}
	err := o.cmd.Wait()
	if err == nil {
		t.Fatal("SIGKILL helper exited successfully")
	}
	o.waited = true
}

func (o *lockOwner) stop(t *testing.T) {
	t.Helper()
	if !o.waited {
		o.release(t)
	}
}
