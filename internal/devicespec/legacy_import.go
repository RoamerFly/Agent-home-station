package devicespec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"
)

// ReadLegacyImport reads and hashes a pinned legacy snapshot without changing
// task state or source files. It does not construct or wake a controller.
func ReadLegacyImport(dir string) (LegacyImport, bool, error) {
	root, err := openLegacyRoot(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return LegacyImport{}, false, nil
		}
		return LegacyImport{}, false, errors.New("device_migration_required")
	}
	defer root.Close()
	settingsFile, err := readOptionalLegacy(root, "settings.json")
	if err != nil {
		return LegacyImport{}, false, err
	}
	tasksFile, err := readOptionalLegacy(root, "tasks.json")
	if err != nil {
		return LegacyImport{}, false, err
	}
	if !settingsFile.found && !tasksFile.found {
		return LegacyImport{}, false, nil
	}
	// Verify both paths again after all reads. This catches replacement or
	// concurrent writes before the host state becomes active; we never repair
	// or overwrite the source JSON on failure.
	if err := verifyOptionalLegacy(root, settingsFile); err != nil {
		return LegacyImport{}, false, err
	}
	if err := verifyOptionalLegacy(root, tasksFile); err != nil {
		return LegacyImport{}, false, err
	}
	settings := Defaults()
	if settingsFile.found && json.Unmarshal(settingsFile.data, &settings) != nil {
		return LegacyImport{}, false, errors.New("device_migration_required")
	}
	tasks := map[string]Task{}
	if tasksFile.found && json.Unmarshal(tasksFile.data, &tasks) != nil {
		return LegacyImport{}, false, errors.New("device_migration_required")
	}
	snapshot := RepositorySnapshot{Settings: settings, Tasks: tasks, Revision: 1, Initialized: true}
	if err := ValidateRepositorySnapshot(snapshot); err != nil {
		return LegacyImport{}, false, err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("mobile-legacy-v1\x00"))
	for _, item := range []struct {
		found bool
		body  []byte
	}{{settingsFile.found, settingsFile.data}, {tasksFile.found, tasksFile.data}} {
		if item.found {
			_, _ = hash.Write([]byte{1})
			_, _ = hash.Write(item.body)
		} else {
			_, _ = hash.Write([]byte{0})
		}
		_, _ = hash.Write([]byte{0})
	}
	return LegacyImport{Settings: settings, Tasks: tasks, SourceHash: hex.EncodeToString(hash.Sum(nil)), ManifestVersion: 1}, true, nil
}

type legacyFile struct {
	name   string
	found  bool
	data   []byte
	digest string
	mode   os.FileMode
	size   int64
	mtime  time.Time
}

func openLegacyRoot(dir string) (*os.Root, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeIrregular != 0 {
		return nil, errors.New("legacy directory unsafe")
	}
	return os.OpenRoot(dir)
}

func readOptionalLegacy(root *os.Root, name string) (legacyFile, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return legacyFile{name: name}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeIrregular != 0 || info.Size() > 4<<20 {
		return legacyFile{}, errors.New("device_migration_required")
	}
	file, err := root.Open(name)
	if err != nil {
		return legacyFile{}, errors.New("device_migration_required")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4<<20+1))
	if err != nil || len(data) > 4<<20 {
		return legacyFile{}, errors.New("device_migration_required")
	}
	opened, err := file.Stat()
	if err != nil || !sameLegacyFile(info, opened) {
		return legacyFile{}, errors.New("device_migration_required")
	}
	digest := sha256.Sum256(data)
	return legacyFile{name: name, found: true, data: data, digest: hex.EncodeToString(digest[:]), mode: info.Mode(), size: info.Size(), mtime: info.ModTime()}, nil
}

func verifyOptionalLegacy(root *os.Root, want legacyFile) error {
	if !want.found {
		if _, err := root.Lstat(want.name); errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return errors.New("device_migration_required")
	}
	got, err := readOptionalLegacy(root, want.name)
	if err != nil || !got.found || got.digest != want.digest || got.mode != want.mode || got.size != want.size || !got.mtime.Equal(want.mtime) {
		return errors.New("device_migration_required")
	}
	return nil
}

func sameLegacyFile(before, after os.FileInfo) bool {
	return before.Mode() == after.Mode() && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime())
}
