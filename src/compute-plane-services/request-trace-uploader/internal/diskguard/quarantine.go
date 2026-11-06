// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package diskguard

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ErrDeletedNotSynced reports data that was deleted but whose directory
// could not be synced. The deletion happened and is counted; the error is
// still returned so the failing volume is seen. Segments.Evict implementations
// wrap it in the same case.
var ErrDeletedNotSynced = errors.New("deleted, but the directory sync failed")

// onSync lets tests observe the order of directory syncs.
var onSync func(dir string)

// RejectsDirName holds reject artifacts: raw lines that could not be read,
// kept per segment. They count against the quarantine bound and outlive the
// source.
const RejectsDirName = "rejects"

// Quarantine is the quarantine directory on the spool volume:
//
//	<dir>/<segment id>/          a quarantined segment and its files
//	<dir>/rejects/<segment id>.* reject artifacts
type Quarantine struct {
	dir string
}

// NewQuarantine returns the quarantine at dir.
func NewQuarantine(dir string) *Quarantine {
	return &Quarantine{dir: dir}
}

// RejectsDir is where reject artifacts are written.
func (q *Quarantine) RejectsDir() string {
	return filepath.Join(q.dir, RejectsDirName)
}

// Admit moves files into the quarantine entry for id, by rename, so they
// must be on the same volume. New directories are linked durably first, and
// each source directory is synced only after the entry, so a power loss never
// keeps a removal without the moved file. A path already gone is skipped, so
// an interrupted Admit can be repeated.
func (q *Quarantine) Admit(id string, paths ...string) error {
	if !validEntryName(id) {
		return fmt.Errorf("quarantine id %q is not a plain name", id)
	}
	entry := filepath.Join(q.dir, id)
	if err := mkdirDurable(entry); err != nil {
		return err
	}
	var sources []string
	for _, path := range paths {
		err := os.Rename(path, filepath.Join(entry, filepath.Base(path)))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("move file into quarantine: %w", err)
		}
		if dir := filepath.Dir(path); !slices.Contains(sources, dir) {
			sources = append(sources, dir)
		}
	}
	for _, dir := range append([]string{entry}, sources...) {
		if err := syncDir(dir); err != nil {
			return err
		}
	}
	return nil
}

// Entries lists every quarantined segment and every reject artifact, with
// its size and modification time. A quarantine that does not exist yet is
// empty.
func (q *Quarantine) Entries() ([]Candidate, error) {
	items, err := os.ReadDir(q.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read quarantine: %w", err)
	}
	var entries []Candidate
	for _, item := range items {
		if item.Name() == RejectsDirName && item.IsDir() {
			rejects, err := q.rejects()
			if err != nil {
				return nil, err
			}
			entries = append(entries, rejects...)
			continue
		}
		info, err := item.Info()
		if err != nil {
			return nil, fmt.Errorf("stat quarantine entry: %w", err)
		}
		size, err := treeSize(filepath.Join(q.dir, item.Name()))
		if err != nil {
			return nil, err
		}
		entries = append(entries, Candidate{Key: item.Name(), Bytes: size, ModTime: info.ModTime()})
	}
	return entries, nil
}

func (q *Quarantine) rejects() ([]Candidate, error) {
	items, err := os.ReadDir(q.RejectsDir())
	if err != nil {
		return nil, fmt.Errorf("read quarantine rejects: %w", err)
	}
	entries := make([]Candidate, 0, len(items))
	for _, item := range items {
		info, err := item.Info()
		if err != nil {
			return nil, fmt.Errorf("stat reject artifact: %w", err)
		}
		size, err := treeSize(filepath.Join(q.RejectsDir(), item.Name()))
		if err != nil {
			return nil, err
		}
		entries = append(entries, Candidate{Key: RejectsDirName + "/" + item.Name(), Bytes: size, ModTime: info.ModTime()})
	}
	return entries, nil
}

// Remove deletes one entry by the key Entries returned: "<id>" or
// "rejects/<name>". Any other key is refused, so a key can never reach
// outside the quarantine.
func (q *Quarantine) Remove(key string) error {
	parts := strings.Split(key, "/")
	valid := (len(parts) == 1 && validEntryName(parts[0])) ||
		(len(parts) == 2 && parts[0] == RejectsDirName && validEntryName(parts[1]))
	if !valid {
		return fmt.Errorf("quarantine key %q is not an entry", key)
	}
	path := filepath.Join(q.dir, filepath.FromSlash(key))
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove quarantine entry: %w", err)
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("%w: %v", ErrDeletedNotSynced, err)
	}
	return nil
}

// mkdirDurable creates dir and any missing parents, then syncs the parent of
// each directory it created, top down.
func mkdirDurable(dir string) error {
	var created []string
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if _, err := os.Stat(d); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat quarantine directory: %w", err)
		}
		created = append(created, d)
		if filepath.Dir(d) == d {
			break
		}
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create quarantine directory: %w", err)
	}
	for i := len(created) - 1; i >= 0; i-- {
		if err := syncDir(filepath.Dir(created[i])); err != nil {
			return err
		}
	}
	return nil
}

func validEntryName(name string) bool {
	return name != "" && name != "." && name != ".." && name != RejectsDirName && !strings.ContainsAny(name, `/\`)
}

func treeSize(root string) (int64, error) {
	var size int64
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			size += info.Size()
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("measure quarantine entry: %w", err)
	}
	return size, nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open directory for sync: %w", err)
	}
	syncErr := d.Sync()
	closeErr := d.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("sync directory: %w", err)
	}
	if onSync != nil {
		onSync(dir)
	}
	return nil
}
