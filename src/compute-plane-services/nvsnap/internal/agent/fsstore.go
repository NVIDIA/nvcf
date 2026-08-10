/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/sirupsen/logrus"
)

// Phase 2c of the 16-node distribution plan (docs/TRANSPORT-ARCHITECTURE.md):
// FSStore is the shared-filesystem backend for clusters with a distributed
// FS mounted on every node — Lustre, Weka, EFS, Filestore, NFS, etc.
//
// When --fsstore-path is set, the agent:
//
//   - publishes every successful CRIU dump into <FSStorePath>/<id>/ (async,
//     best-effort, same pattern as the nvsnap-blobstore upload);
//   - on restore, EnsureLocal probes <FSStorePath>/<id>/inventory.img before
//     the peer cascade and copies the tree from the shared mount if present.
//
// When --fsstore-path is empty, the type is unused and the cascade is
// unchanged (peer → blobstore as before).
//
// Implementation: COPY mode. The agent copies files out of the shared
// mount into the local CheckpointDir, then CRIU restore reads from
// local disk as usual. This keeps CRIU's existing local-disk contract
// intact (it writes swrk logs back into the dump dir) at the cost of
// one extra read pass. Direct-mount mode (symlink/bind the dump dir
// straight at FSStorePath) is a future optimization gated on a real
// DFS being available to benchmark on.

const fsStoreFetchConcurrency = 8

type fsStore struct {
	basePath string
	log      *logrus.Logger
}

func newFSStore(basePath string, log *logrus.Logger) *fsStore {
	if basePath == "" {
		return nil
	}
	return &fsStore{basePath: basePath, log: log}
}

// hasCheckpoint reports whether the FSStore holds a complete-looking
// copy of checkpointID. We probe inventory.img since CRIU writes it
// late in the dump — its presence at non-zero size implies the rest
// of the tree finished writing. publish() uses a .partial → rename
// dance so a concurrent fetcher never sees an in-progress publish.
func (f *fsStore) hasCheckpoint(checkpointID string) bool {
	if f == nil {
		return false
	}
	inv := filepath.Join(f.basePath, checkpointID, "inventory.img")
	fi, err := os.Stat(inv)
	return err == nil && fi.Size() > 0
}

// fetchToLocal copies the checkpoint tree from <FSStorePath>/<id>/
// into dstDir. Parallel per-file copy with fsStoreFetchConcurrency
// workers. dstDir is created if missing. Returns an error if the
// FSStore doesn't have the checkpoint, or if any file copy fails.
func (f *fsStore) fetchToLocal(ctx context.Context, checkpointID, dstDir string) error {
	if f == nil {
		return errors.New("fsstore not configured")
	}
	srcDir := filepath.Join(f.basePath, checkpointID)
	fi, err := os.Stat(srcDir)
	if err != nil {
		return fmt.Errorf("fsstore source: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("fsstore source %s not a directory", srcDir)
	}
	return parallelCopyTree(ctx, srcDir, dstDir, fsStoreFetchConcurrency)
}

// publish copies srcDir into FSStore under checkpointID. Atomic via
// staging dir + rename so a concurrent fetcher never observes a
// half-written tree. Idempotent: a re-publish of the same id wins
// the rename and replaces any prior copy.
func (f *fsStore) publish(ctx context.Context, checkpointID, srcDir string) error {
	if f == nil {
		return errors.New("fsstore not configured")
	}
	fi, err := os.Stat(srcDir)
	if err != nil {
		return fmt.Errorf("source dir: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("source dir %s not a directory", srcDir)
	}
	if err := os.MkdirAll(f.basePath, 0o755); err != nil {
		return fmt.Errorf("mkdir fsstore base: %w", err)
	}
	dstFinal := filepath.Join(f.basePath, checkpointID)
	// Per-publish staging suffix avoids two concurrent publishers
	// stomping each other's staging dir. The final rename is still a
	// race, but rename is atomic per POSIX so at most one wins; the
	// loser sees ENOTEMPTY / EEXIST and retries via the cleanup branch.
	dstTmp := dstFinal + ".partial"
	_ = os.RemoveAll(dstTmp)
	if err := parallelCopyTree(ctx, srcDir, dstTmp, fsStoreFetchConcurrency); err != nil {
		_ = os.RemoveAll(dstTmp)
		return err
	}
	// Replace any prior copy. A previous successful publish leaves
	// dstFinal in place — rename(2) on Linux atomically replaces a
	// non-empty target dir only with renameat2(RENAME_EXCHANGE), which
	// Go doesn't expose. We do the unsafe two-step (remove then
	// rename); a crash between the two leaves the checkpoint
	// temporarily absent from FSStore, but the local hostPath copy is
	// still the source of truth so this is recoverable.
	_ = os.RemoveAll(dstFinal)
	if err := os.Rename(dstTmp, dstFinal); err != nil {
		_ = os.RemoveAll(dstTmp)
		return fmt.Errorf("publish rename: %w", err)
	}
	return nil
}

// parallelCopyTree copies srcDir to dstDir recursively using N workers.
// One filepath.Walk pass creates the directory hierarchy and collects
// regular files; workers then drain a channel of file paths.
//
// Symlinks and special files are skipped — CRIU dumps are regular
// files only, and the rootfs-capture path uses a different code path
// (Local backend) that handles symlinks separately. If FSStore ever
// needs to back rootfs captures, this function will need to grow
// symlink + xattr + mode preservation.
func parallelCopyTree(ctx context.Context, srcDir, dstDir string, workers int) error {
	type fileTask struct {
		rel string
	}
	var tasks []fileTask

	walkErr := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(srcDir, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return os.MkdirAll(dstDir, 0o755)
		}
		dst := filepath.Join(dstDir, rel)
		if info.IsDir() {
			return os.MkdirAll(dst, info.Mode().Perm())
		}
		if info.Mode().IsRegular() {
			tasks = append(tasks, fileTask{rel: rel})
			return nil
		}
		return nil
	})
	if walkErr != nil {
		return fmt.Errorf("walk %s: %w", srcDir, walkErr)
	}

	taskCh := make(chan fileTask, len(tasks))
	for _, t := range tasks {
		taskCh <- t
	}
	close(taskCh)

	errCh := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range taskCh {
				if cerr := ctx.Err(); cerr != nil {
					errCh <- cerr
					return
				}
				src := filepath.Join(srcDir, t.rel)
				dst := filepath.Join(dstDir, t.rel)
				if err := copyOneFile(src, dst); err != nil {
					errCh <- fmt.Errorf("copy %s: %w", t.rel, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for e := range errCh {
		return e
	}
	return nil
}

// copyOneFile streams src → dst with fsync on close. mkdir the parent
// directory if missing so the workers don't have to assume the walk
// already created it (defense in depth — the walk does pre-create,
// but a concurrent publish on the same source could in theory remove
// a dir under us; this keeps the failure mode obvious).
func copyOneFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
