package database

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

var ErrProcessLockBusy = errors.New("database is already owned by another SwaDrive process")

const maximumDatabaseSymlinkDepth = 32

// ProcessLock gives one SwaDrive process exclusive operational ownership of a
// database. SQLite's own locks still protect SQLite transactions; this lock
// prevents the server and administrator maintenance commands from operating on
// the same application state concurrently.
type ProcessLock struct {
	file     *os.File
	closeOne sync.Once
	closeErr error
}

// AcquireProcessLock acquires a non-blocking exclusive lock associated with
// databasePath. The caller must retain the returned lock for its entire
// operational lifetime and close it when finished.
//
// The lock is intentionally scoped to one canonical database path. Backend v1
// deployment must keep one database/root pair under administrator-owned parent
// directories; hard-link aliases and two databases sharing one storage root are
// unsupported configurations rather than identities this flock can discover.
func AcquireProcessLock(databasePath string) (*ProcessLock, error) {
	canonicalPath, err := canonicalDatabasePath(databasePath)
	if err != nil {
		return nil, err
	}

	lockPath := canonicalPath + ".lock"
	fd, created, err := openProcessLockFile(lockPath)
	if err != nil {
		return nil, err
	}

	closeFD := true
	defer func() {
		if closeFD {
			_ = unix.Close(fd)
		}
	}()

	if created {
		// O_CREAT is affected by umask. Normalize the harmless empty lock file
		// so a lock first created by root remains readable by the restricted
		// service account on the next startup.
		if err := unix.Fchmod(fd, 0o644); err != nil {
			return nil, fmt.Errorf("set database process lock permissions: %w", err)
		}
	}

	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, fmt.Errorf("inspect database process lock: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, errors.New("database process lock must be a regular file")
	}

	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrProcessLockBusy
		}
		return nil, fmt.Errorf("acquire database process lock: %w", err)
	}

	file := os.NewFile(uintptr(fd), lockPath)
	if file == nil {
		_ = unix.Flock(fd, unix.LOCK_UN)
		return nil, errors.New("create database process lock handle")
	}

	closeFD = false
	return &ProcessLock{file: file}, nil
}

func (lock *ProcessLock) Close() error {
	if lock == nil {
		return nil
	}

	lock.closeOne.Do(func() {
		if lock.file == nil {
			return
		}
		unlockErr := unix.Flock(int(lock.file.Fd()), unix.LOCK_UN)
		closeErr := lock.file.Close()
		lock.closeErr = errors.Join(unlockErr, closeErr)
	})
	return lock.closeErr
}

func openProcessLockFile(path string) (fd int, created bool, err error) {
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK

	fd, err = unix.Open(path, flags|unix.O_CREAT|unix.O_EXCL, 0o644)
	if err == nil {
		return fd, true, nil
	}
	if !errors.Is(err, unix.EEXIST) {
		return -1, false, fmt.Errorf("create database process lock: %w", err)
	}

	fd, err = unix.Open(path, flags, 0)
	if err != nil {
		return -1, false, fmt.Errorf("open database process lock: %w", err)
	}
	return fd, false, nil
}

func canonicalDatabasePath(databasePath string) (string, error) {
	if strings.TrimSpace(databasePath) == "" {
		return "", ErrDatabasePathRequired
	}
	if strings.ContainsRune(databasePath, '\x00') {
		return "", fmt.Errorf("%w: path contains a null byte", ErrDatabasePathRequired)
	}

	absolutePath, err := filepath.Abs(databasePath)
	if err != nil {
		return "", fmt.Errorf("resolve database path: %w", err)
	}

	return resolveDatabasePath(absolutePath, 0)
}

func resolveDatabasePath(databasePath string, depth int) (string, error) {
	if depth > maximumDatabaseSymlinkDepth {
		return "", errors.New("database path has too many symbolic links")
	}

	resolvedPath, err := filepath.EvalSymlinks(databasePath)
	if err == nil {
		return filepath.Clean(resolvedPath), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("resolve database path: %w", err)
	}

	info, lstatErr := os.Lstat(databasePath)
	switch {
	case lstatErr == nil && info.Mode()&os.ModeSymlink != 0:
		target, readErr := os.Readlink(databasePath)
		if readErr != nil {
			return "", fmt.Errorf("read database path symbolic link: %w", readErr)
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(databasePath), target)
		}
		return resolveDatabasePath(filepath.Clean(target), depth+1)

	case lstatErr != nil && !errors.Is(lstatErr, os.ErrNotExist):
		return "", fmt.Errorf("inspect database path: %w", lstatErr)
	}

	parent, err := filepath.EvalSymlinks(filepath.Dir(databasePath))
	if err != nil {
		return "", fmt.Errorf("resolve database parent directory: %w", err)
	}
	return filepath.Join(parent, filepath.Base(databasePath)), nil
}
