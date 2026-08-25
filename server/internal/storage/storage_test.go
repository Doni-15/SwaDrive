package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestParsePathRejectsTraversalAndInvalidComponents(t *testing.T) {
	valid, err := ParsePath("documents/reports/file.txt", false)
	if err != nil || valid.String() != "documents/reports/file.txt" {
		t.Fatalf("ParsePath(valid) = %q, %v", valid.String(), err)
	}

	invalid := []string{
		".",
		"../outside",
		"documents/../outside",
		"/absolute/path",
		"double//separator",
		"dot/./component",
		"nul\x00byte",
		"control/line\nbreak",
		"back\\slash",
		string([]byte{0xff, 0xfe}),
		strings.Repeat("a", MaximumComponentBytes+1),
		strings.Repeat("deep/", MaximumPathDepth) + "file",
		strings.Repeat("a/", MaximumPathBytes/2) + "z",
	}
	for _, value := range invalid {
		if _, err := ParsePath(value, false); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("ParsePath(%q) error = %v; want ErrInvalidPath", value, err)
		}
	}
	if _, err := ParsePath("", false); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("ParsePath(empty, false) error = %v; want ErrInvalidPath", err)
	}
	if _, err := ParsePath("", true); err != nil {
		t.Fatalf("ParsePath(empty, true) error = %v", err)
	}
}

func TestManagerUsesFilesRootAndRejectsSymlinkEscapeAndConflicts(t *testing.T) {
	rootPath := t.TempDir()
	manager, err := Open(rootPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	filesPath := filepath.Join(rootPath, "files")
	if err := os.Mkdir(filepath.Join(filesPath, "nested"), 0o750); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.WriteFile(filepath.Join(filesPath, "nested", "inside.txt"), []byte("inside"), 0o600); err != nil {
		t.Fatalf("write inside file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "uploads", "hidden.part"), []byte("hidden"), 0o600); err != nil {
		t.Fatalf("write upload marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "trash", "hidden-trash"), []byte("hidden"), 0o600); err != nil {
		t.Fatalf("write trash marker: %v", err)
	}

	var entries []ReindexEntry
	err = manager.WalkFilesForReindex(context.Background(), func(entry ReindexEntry) error {
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		t.Fatalf("WalkFilesForReindex() error = %v", err)
	}
	if len(entries) != 2 || entries[0].RelativePath != "nested" || entries[1].RelativePath != "nested/inside.txt" {
		t.Fatalf("root entries = %+v; want only client-visible files content", entries)
	}

	outsidePath := filepath.Join(t.TempDir(), "outside-secret.txt")
	if err := os.WriteFile(outsidePath, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outsidePath, filepath.Join(filesPath, "escape-link")); err != nil {
		t.Fatalf("create escape symlink: %v", err)
	}
	symlinkPath, _ := ParsePath("escape-link", false)
	if _, _, err := manager.OpenDownload(symlinkPath); !errors.Is(err, ErrSymlink) {
		t.Fatalf("OpenDownload(symlink) error = %v; want ErrSymlink", err)
	}
	moveDestination, _ := ParsePath("moved-link", false)
	if err := manager.Move(symlinkPath, moveDestination); !errors.Is(err, ErrSymlink) {
		t.Fatalf("Move(symlink source) error = %v; want ErrSymlink", err)
	}
	if err := manager.MoveToTrash(symlinkPath, "symlink-trash"); !errors.Is(err, ErrSymlink) {
		t.Fatalf("MoveToTrash(symlink source) error = %v; want ErrSymlink", err)
	}
	outsideDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDirectory, "secret.txt"), []byte("outside-directory"), 0o600); err != nil {
		t.Fatalf("write outside directory file: %v", err)
	}
	if err := os.Symlink(outsideDirectory, filepath.Join(filesPath, "escape-directory")); err != nil {
		t.Fatalf("create directory escape symlink: %v", err)
	}
	nestedSymlinkPath, _ := ParsePath("escape-directory/secret.txt", false)
	if file, _, err := manager.OpenDownload(nestedSymlinkPath); err == nil {
		_ = file.Close()
		t.Fatal("OpenDownload(path through outside symlink) succeeded")
	}
	destinationBehindSymlink, _ := ParsePath("escape-directory/new.txt", false)
	if err := manager.CreateDirectory(destinationBehindSymlink); err == nil {
		t.Fatal("CreateDirectory(destination behind symlink) succeeded")
	}
	if err := manager.PrepareUpload(destinationBehindSymlink); err == nil {
		t.Fatal("PrepareUpload(destination behind symlink) succeeded")
	}
	if err := manager.CreatePart("symlink-destination.part"); err != nil {
		t.Fatalf("CreatePart() error = %v", err)
	}
	if err := manager.FinalizePart("symlink-destination.part", destinationBehindSymlink); err == nil {
		t.Fatal("FinalizePart(destination behind symlink) succeeded")
	}
	if err := os.Symlink(outsidePath, filepath.Join(rootPath, "trash", "restore-link")); err != nil {
		t.Fatalf("create trash source symlink: %v", err)
	}
	restoreDestination, _ := ParsePath("restored.txt", false)
	if err := manager.RestoreFromTrash("restore-link", restoreDestination); !errors.Is(err, ErrSymlink) {
		t.Fatalf("RestoreFromTrash(symlink source) error = %v; want ErrSymlink", err)
	}

	if err := os.WriteFile(filepath.Join(filesPath, "source.txt"), []byte("source"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(filesPath, "destination.txt"), []byte("destination"), 0o600); err != nil {
		t.Fatalf("write destination: %v", err)
	}
	source, _ := ParsePath("source.txt", false)
	destination, _ := ParsePath("destination.txt", false)
	if err := manager.Move(source, destination); !errors.Is(err, ErrConflict) {
		t.Fatalf("Move(conflict) error = %v; want ErrConflict", err)
	}
	contents, err := os.ReadFile(filepath.Join(filesPath, "destination.txt"))
	if err != nil || string(contents) != "destination" {
		t.Fatalf("destination contents = %q, %v; want unchanged", contents, err)
	}
}

func TestExplicitReindexTraversalStreamsLargeDirectory(t *testing.T) {
	rootPath := t.TempDir()
	manager, err := Open(rootPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	const entries = 1001
	for index := 0; index < entries; index++ {
		name := filepath.Join(rootPath, "files", fmt.Sprintf("entry-%04d", index))
		if err := os.WriteFile(name, nil, 0o600); err != nil {
			t.Fatalf("write bounded-list fixture %d: %v", index, err)
		}
	}
	count := 0
	if err := manager.WalkFilesForReindex(context.Background(), func(ReindexEntry) error {
		count++
		return nil
	}); err != nil || count != entries {
		t.Fatalf("WalkFilesForReindex() count = %d, error = %v; want %d", count, err, entries)
	}
}

func TestReindexTraversalHonorsCancellation(t *testing.T) {
	manager, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.WalkFilesForReindex(ctx, func(ReindexEntry) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("WalkFilesForReindex(cancelled) error = %v; want context.Canceled", err)
	}
}

func TestInternalNamesRejectDotComponents(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../part", "a/part"} {
		if validInternalName(name) {
			t.Fatalf("validInternalName(%q) = true", name)
		}
	}
	for _, name := range []string{"0123456789abcdef", "0123456789abcdef.part"} {
		if !validInternalName(name) {
			t.Fatalf("validInternalName(%q) = false", name)
		}
	}
}

func BenchmarkConcurrentRangeReads(b *testing.B) {
	rootPath := b.TempDir()
	manager, err := Open(rootPath)
	if err != nil {
		b.Fatalf("Open() error = %v", err)
	}
	b.Cleanup(func() { _ = manager.Close() })
	contents := make([]byte, 16<<20)
	for index := range contents {
		contents[index] = byte(index)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "files", "benchmark.bin"), contents, 0o600); err != nil {
		b.Fatalf("write benchmark file: %v", err)
	}
	logicalPath, err := ParsePath("benchmark.bin", false)
	if err != nil {
		b.Fatalf("ParsePath() error = %v", err)
	}
	const readSize = 64 << 10
	var sequence atomic.Uint64
	b.SetBytes(readSize)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(parallel *testing.PB) {
		buffer := make([]byte, readSize)
		for parallel.Next() {
			file, _, openErr := manager.OpenDownload(logicalPath)
			if openErr != nil {
				b.Errorf("OpenDownload() error = %v", openErr)
				return
			}
			offset := int64(sequence.Add(1) % uint64((len(contents)-readSize)/readSize))
			offset *= readSize
			count, readErr := file.ReadAt(buffer, offset)
			closeErr := file.Close()
			if readErr != nil || closeErr != nil || count != readSize {
				b.Errorf("range read = %d, %v, close %v", count, readErr, closeErr)
				return
			}
		}
	})
}
