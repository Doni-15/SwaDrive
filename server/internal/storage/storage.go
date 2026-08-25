package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

var (
	ErrNotFound              = errors.New("storage item not found")
	ErrConflict              = errors.New("storage destination already exists")
	ErrNotDirectory          = errors.New("storage item is not a directory")
	ErrNotRegularFile        = errors.New("storage item is not a regular file")
	ErrSymlink               = errors.New("symbolic links are not accessible")
	ErrInsufficientSpace     = errors.New("insufficient free storage space")
	ErrDirectoryNotEmpty     = errors.New("directory is not empty")
	ErrDifferentFilesystem   = errors.New("files, uploads, and trash must share one filesystem")
	ErrStorageVolumeRequired = errors.New("storage volume ID is required")
	ErrStorageVolumeMismatch = errors.New("storage volume identity does not match")
)

const (
	storageVolumeMarkerName   = ".swadrive-volume"
	maximumStorageVolumeIDLen = 128
)

type Entry struct {
	Path       string
	Name       string
	IsDir      bool
	Size       int64
	ModifiedAt time.Time
}

type PublicationState struct {
	PartExists            bool
	DestinationExists     bool
	DestinationSize       int64
	DestinationModifiedAt time.Time
}

// ReindexEntry is emitted only by explicit administrative traversal. Normal
// metadata APIs must use SQLite and never call these disk-walking methods.
type ReindexEntry struct {
	RelativePath string
	IsDirectory  bool
	Size         int64
	ModifiedAt   time.Time
}

// UploadPartEntry is emitted only during explicit local-admin reconciliation.
// It contains an internal name and metadata, never user bytes or a host path.
type UploadPartEntry struct {
	Name       string
	Size       int64
	ModifiedAt time.Time
}

type Manager struct {
	root       *os.Root
	files      *os.Root
	uploads    *os.Root
	trash      *os.Root
	mutationMu sync.Mutex
}

func Open(rootPath string) (*Manager, error) {
	return open(rootPath, "", false)
}

// OpenVerified verifies the expected SwaDrive volume before creating or
// opening the content directories.
func OpenVerified(rootPath, expectedVolumeID string) (*Manager, error) {
	return open(rootPath, expectedVolumeID, true)
}

func open(rootPath, expectedVolumeID string, verifyVolume bool) (*Manager, error) {
	if strings.TrimSpace(rootPath) == "" {
		return nil, errors.New("storage root path is required")
	}

	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("open storage root: %w", err)
	}

	if verifyVolume {
		if err := verifyStorageVolume(root, expectedVolumeID); err != nil {
			_ = root.Close()
			return nil, err
		}
	}

	manager := &Manager{root: root}
	closeOnError := func(openErr error) (*Manager, error) {
		_ = manager.Close()
		return nil, openErr
	}

	for _, directory := range []string{"files", "uploads", "trash"} {
		if err := ensureDirectory(root, directory); err != nil {
			return closeOnError(err)
		}
	}

	if manager.files, err = root.OpenRoot("files"); err != nil {
		return closeOnError(fmt.Errorf("open files root: %w", err))
	}
	if manager.uploads, err = root.OpenRoot("uploads"); err != nil {
		return closeOnError(fmt.Errorf("open uploads root: %w", err))
	}
	if manager.trash, err = root.OpenRoot("trash"); err != nil {
		return closeOnError(fmt.Errorf("open trash root: %w", err))
	}

	if err := ensureSameFilesystem(manager.files, manager.uploads, manager.trash); err != nil {
		return closeOnError(err)
	}

	return manager, nil
}

func verifyStorageVolume(root *os.Root, expectedVolumeID string) error {
	expectedVolumeID = strings.TrimSpace(expectedVolumeID)
	if !validStorageVolumeID(expectedVolumeID) {
		return ErrStorageVolumeRequired
	}

	info, err := root.Lstat(storageVolumeMarkerName)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrStorageVolumeMismatch
		}
		return fmt.Errorf("inspect storage volume marker: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ErrStorageVolumeMismatch
	}

	file, err := root.Open(storageVolumeMarkerName)
	if err != nil {
		return fmt.Errorf("open storage volume marker: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maximumStorageVolumeIDLen+3))
	if err != nil {
		return fmt.Errorf("read storage volume marker: %w", err)
	}
	if len(data) > maximumStorageVolumeIDLen+2 {
		return ErrStorageVolumeMismatch
	}

	actualVolumeID := string(data)
	actualVolumeID = strings.TrimSuffix(actualVolumeID, "\n")
	actualVolumeID = strings.TrimSuffix(actualVolumeID, "\r")

	if !validStorageVolumeID(actualVolumeID) || actualVolumeID != expectedVolumeID {
		return ErrStorageVolumeMismatch
	}
	return nil
}

func validStorageVolumeID(value string) bool {
	if len(value) == 0 || len(value) > maximumStorageVolumeIDLen {
		return false
	}

	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z':
		case character >= 'A' && character <= 'Z':
		case character >= '0' && character <= '9':
		case character == '-', character == '_', character == '.':
		default:
			return false
		}
	}

	return true
}

func (manager *Manager) Close() error {
	var closeError error
	for _, root := range []*os.Root{manager.files, manager.uploads, manager.trash, manager.root} {
		if root != nil {
			closeError = errors.Join(closeError, root.Close())
		}
	}
	return closeError
}

func (manager *Manager) WalkFilesForReindex(ctx context.Context, visit func(ReindexEntry) error) error {
	return walkRootForReindex(ctx, manager.files, ".", func(name string) string {
		return strings.TrimPrefix(name, "./")
	}, visit)
}

func (manager *Manager) WalkTrashForReindex(ctx context.Context, trashName string, visit func(ReindexEntry) error) error {
	if !validInternalName(trashName) {
		return ErrInvalidPath
	}
	return walkRootForReindex(ctx, manager.trash, trashName, func(name string) string {
		if name == trashName {
			return ""
		}
		return strings.TrimPrefix(name, trashName+"/")
	}, visit)
}

// WalkUploadPartsForReconciliation scans only the internal uploads directory in
// bounded directory batches. Normal server startup and metadata APIs never call
// it; orphan cleanup is an explicit offline administrator operation.
func (manager *Manager) WalkUploadPartsForReconciliation(ctx context.Context, visit func(UploadPartEntry) error) error {
	if visit == nil {
		return errors.New("upload part visitor is required")
	}
	directory, err := manager.uploads.Open(".")
	if err != nil {
		return mapFilesystemError(err)
	}
	defer directory.Close()

	const directoryBatchSize = 100
	for {
		entries, readErr := directory.ReadDir(directoryBatchSize)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			name := entry.Name()
			if !validInternalName(name) || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			info, infoErr := manager.uploads.Lstat(name)
			if infoErr != nil {
				return mapFilesystemError(infoErr)
			}
			if !info.Mode().IsRegular() {
				continue
			}
			if err := visit(UploadPartEntry{Name: name, Size: info.Size(), ModifiedAt: info.ModTime().UTC()}); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return mapFilesystemError(readErr)
		}
	}
}

func walkRootForReindex(ctx context.Context, root *os.Root, start string, relative func(string) string, visit func(ReindexEntry) error) error {
	if visit == nil {
		return errors.New("reindex visitor is required")
	}
	err := fs.WalkDir(root.FS(), start, func(name string, directoryEntry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		if directoryEntry.Type()&os.ModeSymlink != 0 {
			if directoryEntry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, err := directoryEntry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return nil
		}
		return visit(ReindexEntry{
			RelativePath: relative(name),
			IsDirectory:  info.IsDir(),
			Size:         info.Size(),
			ModifiedAt:   info.ModTime().UTC(),
		})
	})
	return mapFilesystemError(err)
}

func (manager *Manager) CreateDirectory(logicalPath Path) error {
	if logicalPath.value == "" {
		return ErrConflict
	}
	manager.mutationMu.Lock()
	defer manager.mutationMu.Unlock()

	if err := manager.ensureDestinationAvailable(logicalPath); err != nil {
		return err
	}
	if err := manager.ensureFilesParent(logicalPath); err != nil {
		return err
	}
	if err := manager.files.Mkdir(logicalPath.rootName(), 0o750); err != nil {
		return mapFilesystemError(err)
	}
	return nil
}

func (manager *Manager) RemoveEmptyDirectory(logicalPath Path) error {
	if logicalPath.value == "" {
		return ErrInvalidPath
	}
	manager.mutationMu.Lock()
	defer manager.mutationMu.Unlock()
	if err := manager.files.Remove(logicalPath.rootName()); err != nil {
		if errors.Is(err, unix.ENOTEMPTY) {
			return ErrDirectoryNotEmpty
		}
		return mapFilesystemError(err)
	}
	return nil
}

func (manager *Manager) Move(source, destination Path) error {
	if source.value == "" || destination.value == "" || source.value == destination.value {
		return ErrInvalidPath
	}
	manager.mutationMu.Lock()
	defer manager.mutationMu.Unlock()

	if _, err := manager.safeSourceInfo(source); err != nil {
		return err
	}
	if err := manager.ensureDestinationAvailable(destination); err != nil {
		return err
	}
	if err := manager.ensureFilesParent(destination); err != nil {
		return err
	}
	if err := manager.files.Rename(source.rootName(), destination.rootName()); err != nil {
		return mapFilesystemError(err)
	}
	return nil
}

func (manager *Manager) MoveToTrash(source Path, trashName string) error {
	if source.value == "" || !validInternalName(trashName) {
		return ErrInvalidPath
	}
	manager.mutationMu.Lock()
	defer manager.mutationMu.Unlock()

	if _, err := manager.safeSourceInfo(source); err != nil {
		return err
	}
	if _, err := manager.trash.Lstat(trashName); err == nil {
		return ErrConflict
	} else if !errors.Is(err, fs.ErrNotExist) {
		return mapFilesystemError(err)
	}
	if err := manager.root.Rename("files/"+source.rootName(), "trash/"+trashName); err != nil {
		return mapFilesystemError(err)
	}
	return nil
}

func (manager *Manager) RestoreFromTrash(trashName string, destination Path) error {
	if !validInternalName(trashName) || destination.value == "" {
		return ErrInvalidPath
	}
	manager.mutationMu.Lock()
	defer manager.mutationMu.Unlock()

	trashInfo, err := manager.trash.Lstat(trashName)
	if err != nil {
		return mapFilesystemError(err)
	}
	if trashInfo.Mode()&os.ModeSymlink != 0 {
		return ErrSymlink
	}
	if err := manager.ensureDestinationAvailable(destination); err != nil {
		return err
	}
	if err := manager.ensureFilesParent(destination); err != nil {
		return err
	}
	if err := manager.root.Rename("trash/"+trashName, "files/"+destination.rootName()); err != nil {
		return mapFilesystemError(err)
	}
	return nil
}

func (manager *Manager) TrashState(trashName string, destination Path) (trashExists, destinationExists bool, err error) {
	if !validInternalName(trashName) || destination.value == "" {
		return false, false, ErrInvalidPath
	}
	manager.mutationMu.Lock()
	defer manager.mutationMu.Unlock()

	trashExists, err = regularOrDirectoryExists(manager.trash, trashName)
	if err != nil {
		return false, false, err
	}
	destinationExists, err = regularOrDirectoryExists(manager.files, destination.rootName())
	if err != nil {
		return false, false, err
	}
	return trashExists, destinationExists, nil
}

func (manager *Manager) OpenDownload(logicalPath Path) (*os.File, Entry, error) {
	info, err := manager.safeSourceInfo(logicalPath)
	if err != nil {
		return nil, Entry{}, err
	}
	if !info.Mode().IsRegular() {
		return nil, Entry{}, ErrNotRegularFile
	}
	file, err := manager.files.Open(logicalPath.rootName())
	if err != nil {
		return nil, Entry{}, mapFilesystemError(err)
	}
	entry := Entry{
		Path:       logicalPath.String(),
		Name:       path.Base(logicalPath.String()),
		Size:       info.Size(),
		ModifiedAt: info.ModTime().UTC(),
	}
	return file, entry, nil
}

func (manager *Manager) PrepareUpload(destination Path) error {
	manager.mutationMu.Lock()
	defer manager.mutationMu.Unlock()
	if err := manager.ensureDestinationAvailable(destination); err != nil {
		return err
	}
	return manager.ensureFilesParent(destination)
}

func (manager *Manager) CreatePart(name string) error {
	if !validInternalName(name) {
		return ErrInvalidPath
	}
	file, err := manager.uploads.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return mapFilesystemError(err)
	}
	return file.Close()
}

func (manager *Manager) OpenPart(name string) (*os.File, error) {
	if !validInternalName(name) {
		return nil, ErrInvalidPath
	}
	file, err := manager.uploads.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		return nil, mapFilesystemError(err)
	}
	return file, nil
}

func (manager *Manager) RemovePart(name string) error {
	if !validInternalName(name) {
		return ErrInvalidPath
	}
	err := manager.uploads.Remove(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return mapFilesystemError(err)
}

func (manager *Manager) PartInfo(name string) (os.FileInfo, error) {
	if !validInternalName(name) {
		return nil, ErrInvalidPath
	}
	info, err := manager.uploads.Lstat(name)
	if err != nil {
		return nil, mapFilesystemError(err)
	}
	if !info.Mode().IsRegular() {
		return nil, ErrNotRegularFile
	}
	return info, nil
}

func (manager *Manager) FinalizePart(partName string, destination Path) error {
	if !validInternalName(partName) || destination.value == "" {
		return ErrInvalidPath
	}
	manager.mutationMu.Lock()
	defer manager.mutationMu.Unlock()

	if _, err := manager.PartInfo(partName); err != nil {
		return err
	}
	if err := manager.ensureDestinationAvailable(destination); err != nil {
		return err
	}
	if err := manager.ensureFilesParent(destination); err != nil {
		return err
	}
	if err := manager.root.Rename("uploads/"+partName, "files/"+destination.rootName()); err != nil {
		return mapFilesystemError(err)
	}
	return nil
}

func (manager *Manager) FinalizationState(partName string, destination Path) (PublicationState, error) {
	if !validInternalName(partName) || destination.value == "" {
		return PublicationState{}, ErrInvalidPath
	}

	manager.mutationMu.Lock()
	defer manager.mutationMu.Unlock()

	var state PublicationState

	partInfo, statErr := manager.uploads.Lstat(partName)
	switch {
	case statErr == nil:
		if partInfo.Mode()&os.ModeSymlink != 0 {
			return PublicationState{}, ErrSymlink
		}
		if !partInfo.Mode().IsRegular() {
			return PublicationState{}, ErrNotRegularFile
		}
		state.PartExists = true
	case errors.Is(statErr, fs.ErrNotExist):
	default:
		return PublicationState{}, mapFilesystemError(statErr)
	}

	destinationInfo, statErr := manager.files.Lstat(destination.rootName())
	switch {
	case statErr == nil:
		if destinationInfo.Mode()&os.ModeSymlink != 0 {
			return PublicationState{}, ErrSymlink
		}
		if !destinationInfo.Mode().IsRegular() {
			return PublicationState{}, ErrNotRegularFile
		}
		state.DestinationExists = true
		state.DestinationSize = destinationInfo.Size()
		state.DestinationModifiedAt = destinationInfo.ModTime().UTC()
	case errors.Is(statErr, fs.ErrNotExist):
	default:
		return PublicationState{}, mapFilesystemError(statErr)
	}

	return state, nil
}

func (manager *Manager) CheckAvailable(required, reserve uint64) error {
	var statistics unix.Statfs_t
	if err := unix.Statfs(manager.uploads.Name(), &statistics); err != nil {
		return fmt.Errorf("read storage free space: %w", err)
	}
	blockSize := uint64(statistics.Bsize)
	availableBlocks := statistics.Bavail
	var available uint64
	if blockSize != 0 && uint64(availableBlocks) > math.MaxUint64/blockSize {
		available = math.MaxUint64
	} else {
		available = uint64(availableBlocks) * blockSize
	}
	if required > available || reserve > available-required {
		return ErrInsufficientSpace
	}
	return nil
}

func (manager *Manager) safeSourceInfo(logicalPath Path) (os.FileInfo, error) {
	if logicalPath.value == "" {
		return nil, ErrInvalidPath
	}
	info, err := manager.files.Lstat(logicalPath.rootName())
	if err != nil {
		return nil, mapFilesystemError(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrSymlink
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return nil, ErrNotRegularFile
	}
	return info, nil
}

func (manager *Manager) ensureDestinationAvailable(destination Path) error {
	if _, err := manager.files.Lstat(destination.rootName()); err == nil {
		return ErrConflict
	} else if !errors.Is(err, fs.ErrNotExist) {
		return mapFilesystemError(err)
	}
	return nil
}

func (manager *Manager) ensureFilesParent(destination Path) error {
	parent := path.Dir(destination.rootName())
	info, err := manager.files.Lstat(parent)
	if err != nil {
		return mapFilesystemError(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ErrSymlink
	}
	if !info.IsDir() {
		return ErrNotDirectory
	}
	return nil
}

func ensureDirectory(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		if err := root.Mkdir(name, 0o750); err != nil {
			return fmt.Errorf("create storage directory %s: %w", name, err)
		}
		info, err = root.Lstat(name)
	}
	if err != nil {
		return fmt.Errorf("inspect storage directory %s: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("storage directory %s must be a real directory", name)
	}
	return nil
}

func ensureSameFilesystem(roots ...*os.Root) error {
	var expected uint64
	for index, root := range roots {
		file, err := root.Open(".")
		if err != nil {
			return fmt.Errorf("inspect storage filesystem: %w", err)
		}
		info, statErr := file.Stat()
		closeErr := file.Close()
		if statErr != nil || closeErr != nil {
			return fmt.Errorf("inspect storage filesystem: %w", errors.Join(statErr, closeErr))
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return errors.New("inspect storage filesystem: unsupported file information")
		}
		device := uint64(stat.Dev)
		if index == 0 {
			expected = device
			continue
		}
		if device != expected {
			return ErrDifferentFilesystem
		}
	}
	return nil
}

func mapFilesystemError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return ErrNotFound
	case errors.Is(err, fs.ErrExist):
		return ErrConflict
	default:
		return err
	}
}

func regularOrDirectoryExists(root *os.Root, name string) (bool, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, mapFilesystemError(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, ErrSymlink
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return false, ErrNotRegularFile
	}
	return true, nil
}
