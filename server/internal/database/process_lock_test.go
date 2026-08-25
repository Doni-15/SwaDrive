package database

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProcessLockIsExclusiveAndReusableAfterRelease(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "state.db")

	first, err := AcquireProcessLock(databasePath)
	if err != nil {
		t.Fatalf("first AcquireProcessLock() error = %v", err)
	}

	if _, err := AcquireProcessLock(databasePath); !errors.Is(err, ErrProcessLockBusy) {
		_ = first.Close()
		t.Fatalf("second AcquireProcessLock() error = %v; want ErrProcessLockBusy", err)
	}

	if err := first.Close(); err != nil {
		t.Fatalf("first ProcessLock.Close() error = %v", err)
	}

	second, err := AcquireProcessLock(databasePath)
	if err != nil {
		t.Fatalf("AcquireProcessLock() after release error = %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("second ProcessLock.Close() error = %v", err)
	}
}

func TestProcessLockCanonicalizesSymlinkedParent(t *testing.T) {
	realDirectory := t.TempDir()
	linkContainer := t.TempDir()
	linkDirectory := filepath.Join(linkContainer, "state-link")

	if err := os.Symlink(realDirectory, linkDirectory); err != nil {
		t.Fatalf("create directory symbolic link: %v", err)
	}

	realPath := filepath.Join(realDirectory, "state.db")
	aliasPath := filepath.Join(linkDirectory, "state.db")

	first, err := AcquireProcessLock(realPath)
	if err != nil {
		t.Fatalf("AcquireProcessLock(real path) error = %v", err)
	}
	defer first.Close()

	if _, err := AcquireProcessLock(aliasPath); !errors.Is(err, ErrProcessLockBusy) {
		t.Fatalf("AcquireProcessLock(symlink alias) error = %v; want ErrProcessLockBusy", err)
	}
}

func TestProcessLockCanonicalizesDatabaseFileSymlink(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "state.db")
	aliasPath := filepath.Join(directory, "alias.db")

	if err := os.WriteFile(databasePath, nil, 0o600); err != nil {
		t.Fatalf("create database target: %v", err)
	}
	if err := os.Symlink("state.db", aliasPath); err != nil {
		t.Fatalf("create database symbolic link: %v", err)
	}

	first, err := AcquireProcessLock(databasePath)
	if err != nil {
		t.Fatalf("AcquireProcessLock(database target) error = %v", err)
	}
	defer first.Close()

	if _, err := AcquireProcessLock(aliasPath); !errors.Is(err, ErrProcessLockBusy) {
		t.Fatalf("AcquireProcessLock(database symlink) error = %v; want ErrProcessLockBusy", err)
	}
}

func TestProcessLockRejectsSymlinkLockFile(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "state.db")
	lockTarget := filepath.Join(directory, "unexpected-target")

	if err := os.WriteFile(lockTarget, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(lockTarget, databasePath+".lock"); err != nil {
		t.Fatal(err)
	}

	if lock, err := AcquireProcessLock(databasePath); err == nil {
		_ = lock.Close()
		t.Fatal("AcquireProcessLock() followed symbolic lock file")
	}
}

func TestProcessLockIsExclusiveAcrossIndependentProcesses(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "state.db")
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	command := exec.Command(executable, "-test.run=^TestProcessLockHelperProcess$")
	command.Env = append(os.Environ(),
		"SWADRIVE_PROCESS_LOCK_HELPER=1",
		"SWADRIVE_PROCESS_LOCK_HELPER_DB="+databasePath,
	)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start lock helper: %v", err)
	}
	reader := bufio.NewReader(stdout)
	ready, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(ready) != "LOCKED" {
		_ = stdin.Close()
		_ = command.Wait()
		t.Fatalf("lock helper readiness = %q, %v; stderr=%q", ready, err, stderr.String())
	}

	if lock, err := AcquireProcessLock(databasePath); !errors.Is(err, ErrProcessLockBusy) {
		if lock != nil {
			_ = lock.Close()
		}
		_ = stdin.Close()
		_, _ = io.Copy(io.Discard, reader)
		_ = command.Wait()
		t.Fatalf("AcquireProcessLock(while child owns lock) error = %v; want ErrProcessLockBusy", err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatalf("release helper stdin: %v", err)
	}
	_, _ = io.Copy(io.Discard, reader)
	if err := command.Wait(); err != nil {
		t.Fatalf("lock helper exit: %v; stderr=%q", err, stderr.String())
	}

	lock, err := AcquireProcessLock(databasePath)
	if err != nil {
		t.Fatalf("AcquireProcessLock(after child exit) error = %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("ProcessLock.Close() error = %v", err)
	}
}

func TestProcessLockHelperProcess(t *testing.T) {
	if os.Getenv("SWADRIVE_PROCESS_LOCK_HELPER") != "1" {
		return
	}
	lock, err := AcquireProcessLock(os.Getenv("SWADRIVE_PROCESS_LOCK_HELPER_DB"))
	if err != nil {
		t.Fatalf("helper AcquireProcessLock() error = %v", err)
	}
	defer lock.Close()
	if _, err := fmt.Fprintln(os.Stdout, "LOCKED"); err != nil {
		t.Fatalf("helper readiness write: %v", err)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}
