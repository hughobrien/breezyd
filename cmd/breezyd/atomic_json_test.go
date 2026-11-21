// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matryer/is"
)

// TestWriteJSONAtomic_FsyncsFileThenRenameThenDir pins the durability
// order: temp file synced before the rename, parent dir synced after it.
func TestWriteJSONAtomic_FsyncsFileThenRenameThenDir(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "x.json")
	is.NoErr(os.WriteFile(path, []byte(`"old"`), 0o600))

	var events []string
	origF, origD := fsyncFile, fsyncDir
	t.Cleanup(func() { fsyncFile, fsyncDir = origF, origD })
	fsyncFile = func(f *os.File) error {
		// Before the rename the target must still hold the old content.
		b, _ := os.ReadFile(path)
		events = append(events, "file:"+string(b))
		return f.Sync()
	}
	fsyncDir = func(d string) error {
		b, _ := os.ReadFile(path)
		events = append(events, "dir:"+d+":"+string(b))
		return nil
	}

	is.NoErr(writeJSONAtomic(path, "new"))
	is.Equal(events, []string{`file:"old"`, "dir:" + dir + `:"new"`})
	_, err := os.Stat(path + ".tmp")
	is.True(os.IsNotExist(err)) // temp file consumed by rename
}

func TestWriteJSONAtomic_DirSyncErrorIgnored(t *testing.T) {
	is := is.New(t)
	origD := fsyncDir
	t.Cleanup(func() { fsyncDir = origD })
	fsyncDir = func(string) error { return os.ErrInvalid }
	is.NoErr(writeJSONAtomic(filepath.Join(t.TempDir(), "x.json"), "v"))
}

func TestWriteJSONAtomic_FileSyncErrorFails(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "x.json")
	origF := fsyncFile
	t.Cleanup(func() { fsyncFile = origF })
	fsyncFile = func(*os.File) error { return os.ErrInvalid }
	err := writeJSONAtomic(path, "v")
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "sync"))
	_, statErr := os.Stat(path)
	is.True(os.IsNotExist(statErr)) // never renamed into place
	_, statErr = os.Stat(path + ".tmp")
	is.True(os.IsNotExist(statErr)) // temp cleaned up
}
