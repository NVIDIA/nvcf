// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// A checkpoint fetched to this node lands in <CheckpointDir>/<id> in
// place. A restore placeholder may already have that directory (and its
// gpushare store) bind-mounted, created empty by the kubelet, so a fetch
// never replaces the directory or its subdirectories: it fills them.
//
// A fetch marks itself in <CheckpointDir>/.fetching/<id>, outside the
// checkpoint (peers serve the checkpoint's whole tree). The checkpoint is
// complete when its images are there and no fetch is marked: a fetch that
// stopped part way, with the agent, keeps its mark and is redone.

import (
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

const fetchingDirName = ".fetching"

// localFetchLocks serializes the fetches of one checkpoint on this node.
var localFetchLocks sync.Map // checkpoint id -> *sync.Mutex

func lockLocalFetch(id string) func() {
	m, _ := localFetchLocks.LoadOrStore(id, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func fetchMarker(checkpointDir, id string) string {
	return filepath.Join(checkpointDir, fetchingDirName, id)
}

// localCheckpointComplete reports whether checkpoint id is whole on this
// node.
func localCheckpointComplete(checkpointDir, id string) bool {
	if _, err := os.Stat(fetchMarker(checkpointDir, id)); err == nil {
		return false
	}
	fi, err := os.Stat(filepath.Join(checkpointDir, id, "inventory.img"))
	return err == nil && fi.Size() > 0
}

// beginLocalFetch marks a fetch of id and empties what an earlier fetch
// left, keeping the directories.
func beginLocalFetch(checkpointDir, id string) error {
	if err := os.MkdirAll(filepath.Join(checkpointDir, fetchingDirName), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(fetchMarker(checkpointDir, id), nil, 0o644); err != nil {
		return err
	}
	dir := filepath.Join(checkpointDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return clearFiles(dir)
}

// endLocalFetch records the fetch's outcome: done, or emptied for the
// next try.
func endLocalFetch(checkpointDir, id string, ok bool) error {
	if !ok {
		_ = clearFiles(filepath.Join(checkpointDir, id))
		return nil
	}
	return os.Remove(fetchMarker(checkpointDir, id))
}

// clearFiles removes every non-directory under dir and keeps the
// directories, which a placeholder may hold bind-mounted.
func clearFiles(dir string) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		return os.Remove(p)
	})
}
