package storage

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestProviderStartsAvailableOnlyForVerifiedStorage(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "storage")
	if err := os.Mkdir(rootPath, 0o750); err != nil {
		t.Fatal(err)
	}
	const volumeID = "provider-test-volume"
	if err := os.WriteFile(filepath.Join(rootPath, storageVolumeMarkerName), []byte(volumeID+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var states []bool
	provider := OpenProvider(rootPath, volumeID, func(available bool) {
		states = append(states, available)
	})

	if !provider.Available() {
		t.Fatal("verified provider is unavailable")
	}
	if !reflect.DeepEqual(states, []bool{true}) {
		t.Fatalf("state changes = %v; want [true]", states)
	}
	path, _ := ParsePath("documents", false)
	if err := provider.CreateDirectory(path); err != nil {
		t.Fatalf("CreateDirectory() error = %v", err)
	}
}

func TestProviderFailsClosedWithoutExpectedVolume(t *testing.T) {
	tests := []struct {
		name       string
		markerData string
	}{
		{name: "missing marker"},
		{name: "wrong marker", markerData: "another-volume\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rootPath := filepath.Join(t.TempDir(), "storage")
			if err := os.Mkdir(rootPath, 0o750); err != nil {
				t.Fatal(err)
			}
			if test.markerData != "" {
				if err := os.WriteFile(filepath.Join(rootPath, storageVolumeMarkerName), []byte(test.markerData), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			provider := OpenProvider(rootPath, "expected-volume", nil)
			if provider.Available() {
				t.Fatal("provider accepted unavailable or mismatched storage")
			}
			path, _ := ParsePath("must-not-exist", false)
			if err := provider.CreateDirectory(path); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("CreateDirectory() error = %v; want ErrUnavailable", err)
			}
			for _, directory := range []string{"files", "uploads", "trash"} {
				if _, err := os.Stat(filepath.Join(rootPath, directory)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("%s exists on rejected storage root", directory)
				}
			}
		})
	}
}

func TestProviderRuntimeLossNeverWritesFallbackAndRequiresRestartToRecover(t *testing.T) {
	base := t.TempDir()
	rootPath := filepath.Join(base, "storage")
	if err := os.Mkdir(rootPath, 0o750); err != nil {
		t.Fatal(err)
	}
	const volumeID = "runtime-loss-volume"
	if err := os.WriteFile(filepath.Join(rootPath, storageVolumeMarkerName), []byte(volumeID+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var states []bool
	provider := OpenProvider(rootPath, volumeID, func(available bool) {
		states = append(states, available)
	})
	if !provider.Available() {
		t.Fatal("provider did not start available")
	}

	detachedPath := filepath.Join(base, "detached-volume")
	if err := os.Rename(rootPath, detachedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(rootPath, 0o750); err != nil {
		t.Fatal(err)
	}

	if provider.Available() {
		t.Fatal("provider remained available after configured volume disappeared")
	}
	logicalPath, _ := ParsePath("must-not-exist", false)
	if err := provider.CreateDirectory(logicalPath); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("CreateDirectory() error = %v; want ErrUnavailable", err)
	}
	entries, err := os.ReadDir(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("fallback root contains %d entries; want empty", len(entries))
	}
	if _, err := os.Stat(filepath.Join(detachedPath, "files", "must-not-exist")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("operation reached the detached content volume after loss")
	}

	if err := os.Remove(rootPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(detachedPath, rootPath); err != nil {
		t.Fatal(err)
	}
	if provider.Available() {
		t.Fatal("provider recovered without restart and startup reconciliation")
	}
	if !reflect.DeepEqual(states, []bool{true, false}) {
		t.Fatalf("state changes = %v; want [true false]", states)
	}
}
