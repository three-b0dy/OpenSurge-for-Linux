package process

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
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
	cmd := exec.Command("sh", "-c", "sleep 0.05")
	pid, err := startDetached(cmd)
	if err != nil {
		t.Fatalf("startDetached() error = %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	lastState := ""
	for time.Now().Before(deadline) {
		state, stateErr := childState(pid)
		if errors.Is(stateErr, os.ErrNotExist) {
			return
		}
		if stateErr != nil {
			t.Fatalf("childState() error = %v", stateErr)
		}
		lastState = strings.TrimSpace(state)
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child %d was not reaped before timeout (state %q)", pid, lastState)
}

func childState(pid int) (string, error) {
	output, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return "", os.ErrNotExist
		}
		return "", err
	}
	return string(output), nil
}
