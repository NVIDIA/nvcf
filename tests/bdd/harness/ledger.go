/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package harness

import (
	"errors"
	"fmt"
	"os"
	"sort"
)

// Ledger snapshots files before their first write inside the suite and
// restores every snapshotted path at suite teardown. The strict DSL
// promises this to feature files: any path you author via I copy / I
// update yaml / I substitute is yours for the suite, and the runner
// cleans up.
//
// Snapshots are held in memory for the lifetime of the suite. The
// snapshotsDir argument is reserved for an on-disk variant if very
// large fixtures ever push memory limits; today it is unused.
//
// Snapshot is idempotent on repeat calls for the same path; only the
// first call records state. This matches the Helmfile feature's
// per-Rule Background re-running the file authoring before every
// scenario in that rule.
type Ledger struct {
	snapshotsDir string
	originals    map[string]ledgerEntry
}

type ledgerEntry struct {
	existed bool
	body    []byte
	mode    os.FileMode
}

// NewLedger constructs a Ledger whose snapshot copies live under
// snapshotsDir. The directory is created on first Snapshot call.
func NewLedger(snapshotsDir string) *Ledger {
	return &Ledger{
		snapshotsDir: snapshotsDir,
		originals:    map[string]ledgerEntry{},
	}
}

// Snapshot records the pre-write state of path. If path already exists,
// the body and mode are captured in memory. If path does not exist, an
// "absent" marker is stored so RestoreAll knows to delete the file.
// Calling Snapshot more than once on the same path is a no-op so
// scenarios in the same Rule re-running their Background do not
// overwrite the original snapshot with their own mid-suite write.
func (l *Ledger) Snapshot(path string) error {
	if _, recorded := l.originals[path]; recorded {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			l.originals[path] = ledgerEntry{existed: false}
			return nil
		}
		return fmt.Errorf("stat %s: %w", path, err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	l.originals[path] = ledgerEntry{
		existed: true,
		body:    body,
		mode:    info.Mode().Perm(),
	}
	return nil
}

// RestoreAll walks every recorded path in sorted order. Files that did
// not exist before are removed; files that did are rewritten with the
// original bytes and mode. A best-effort restore proceeds across all
// paths even if some fail; the first error encountered is returned.
func (l *Ledger) RestoreAll() error {
	paths := make([]string, 0, len(l.originals))
	for path := range l.originals {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var firstErr error
	for _, path := range paths {
		if err := l.restoreOne(path, l.originals[path]); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	l.originals = map[string]ledgerEntry{}
	return firstErr
}

func (l *Ledger) restoreOne(path string, entry ledgerEntry) error {
	if !entry.existed {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", path, err)
		}
		return nil
	}
	if err := os.WriteFile(path, entry.body, entry.mode); err != nil {
		return fmt.Errorf("restore %s: %w", path, err)
	}
	return nil
}

