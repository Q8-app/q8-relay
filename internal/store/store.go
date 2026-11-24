// Package store keeps Relay's small JSON files in its data folder,
// written whole through a temporary file so a crash never leaves half of
// one behind.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Load reads name into v. A missing file leaves v as it is. A file that
// won't read is copied aside as name.unreadable before anything writes
// over it, and v is left as it is.
func Load(dir, name string, v any) error {
	path := filepath.Join(dir, name)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		aside := path + ".unreadable"
		_ = os.WriteFile(aside, data, 0o600)
		return fmt.Errorf("%s didn't read (kept as %s): %w", name, filepath.Base(aside), err)
	}
	return nil
}

// Save writes v as name.
func Save(dir, name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, name+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}
