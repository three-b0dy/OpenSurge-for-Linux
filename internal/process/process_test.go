package process

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestSignalErrMeansAlive(t *testing.T) {
	for _, err := range []error{
		nil,
		syscall.EPERM,
		os.ErrPermission,
		&os.SyscallError{Syscall: "kill", Err: syscall.EPERM},
	} {
		if !signalErrMeansAlive(err) {
			t.Fatalf("signalErrMeansAlive(%v) = false", err)
		}
	}

	for _, err := range []error{
		os.ErrProcessDone,
		syscall.ESRCH,
		errors.New("missing"),
	} {
		if signalErrMeansAlive(err) {
			t.Fatalf("signalErrMeansAlive(%v) = true", err)
		}
	}
}

func TestStartDetachedReapsExitedChild(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 0")
	pid, err := startDetached(cmd)
	if err != nil {
		t.Fatalf("startDetached() error = %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var status syscall.WaitStatus
		waited, waitErr := syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
		if errors.Is(waitErr, syscall.ECHILD) {
			return
		}
		if waitErr != nil {
			t.Fatalf("Wait4() error = %v", waitErr)
		}
		if waited == pid {
			t.Fatalf("startDetached() left child %d unreaped", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child %d was not reaped before timeout", pid)
}
