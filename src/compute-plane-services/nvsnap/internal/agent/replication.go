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
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/objectstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/tracing"
)

// objectStoreUploadConcurrency is the number of parallel object PUTs during
// a push. Object stores sustain many concurrent streams; 8 matches the
// blobstore uploader.
const objectStoreUploadConcurrency = 8

// startReplication opens the shared object-store client and resolves the
// home + peer bucket handles when replication is enabled (provider AND
// HomeBucket set). No-op otherwise. Called once during agent startup, after
// the rootfs-capture wiring. The bucket handles are stored on the Agent for
// the push (UploadCaptureToObjectStore) and pull (ReplicateFromObjectStore)
// paths.
func (a *Agent) startReplication(ctx context.Context) error {
	cfg := a.config.Replication.ObjectStore
	if cfg.Provider == "" || cfg.HomeBucket == "" {
		return nil
	}
	home, closeFn, err := objectstore.NewBucket(ctx, cfg.Provider, cfg.HomeBucket)
	if err != nil {
		return fmt.Errorf("replication object store: %w", err)
	}
	a.objectStoreClose = closeFn
	a.objectStoreHome = home
	for _, name := range cfg.PeerBuckets {
		if name == "" || name == cfg.HomeBucket {
			continue
		}
		peer, _, perr := objectstore.NewBucket(ctx, cfg.Provider, name)
		if perr != nil {
			return fmt.Errorf("replication object store peer %q: %w", name, perr)
		}
		a.objectStorePeers = append(a.objectStorePeers, peer)
	}
	a.log.WithFields(map[string]any{
		"provider":     cfg.Provider,
		"home_bucket":  cfg.HomeBucket,
		"peer_buckets": cfg.PeerBuckets,
	}).Info("cross-cluster replication enabled")
	return nil
}

// postCaptureCommit is the rootfs PostCommit hook: it pushes a committed
// capture to the same-cluster nvsnap-blobstore (tier-3 fallback) and, when
// cross-cluster replication is enabled, to this cluster's home bucket (the
// L4 cross-cluster tier). Both are best-effort and independent — a failure
// in one is logged but doesn't block the other or roll back the capture.
func (a *Agent) postCaptureCommit(ctx context.Context, hash string) error {
	var firstErr error
	if err := a.UploadCapture(ctx, hash); err != nil {
		a.log.WithError(err).WithField("hash", hash).Warn("blobstore upload failed")
		firstErr = err
	}
	if err := a.UploadCaptureToObjectStore(ctx, hash); err != nil {
		a.log.WithError(err).WithField("hash", hash).Warn("replication push failed")
		if firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// UploadCaptureToObjectStore pushes a committed rootfs capture's on-disk
// tree + manifest to this cluster's home bucket, keyed by content hash:
//
//	<HomeBucket>/<hash>/manifest.json
//	<HomeBucket>/<hash>/tree/...
//
// The local layout (<CacheDir>/<hash>/{manifest.json,tree/}) is uploaded
// verbatim — object keys are "<hash>/<relpath>", so a remote cluster GETs
// the same files into the same layout and replays the capture commit.
//
// Best-effort, like the blobstore uploader: a missing HomeBucket or a
// transient PUT failure returns an error (logged by the PostCommit caller)
// but never rolls back the local capture.
func (a *Agent) UploadCaptureToObjectStore(ctx context.Context, hash string) error {
	if a.objectStoreHome == nil {
		return nil // replication disabled
	}
	ctx, span := tracing.Tracer().Start(ctx, "capture.replication_push")
	defer span.End()
	span.SetAttributes(
		attribute.String("nvsnap.hash", hash),
		attribute.String("nvsnap.object_store_bucket", a.objectStoreHome.Name()),
	)
	if hash == "" {
		return errors.New("hash required")
	}
	cacheRoot := a.config.RootfsCapture.CacheDir
	if cacheRoot == "" {
		return errors.New("UploadCaptureToObjectStore: RootfsCapture.CacheDir empty")
	}
	captureDir := filepath.Join(cacheRoot, hash)
	if fi, err := os.Stat(captureDir); err != nil || !fi.IsDir() {
		return fmt.Errorf("capture dir %s not found", captureDir)
	}

	log := a.log.WithFields(map[string]any{"hash": hash, "bucket": a.objectStoreHome.Name()})
	log.Info("UploadCaptureToObjectStore: starting")
	start := time.Now()

	files, err := walkDumpDir(captureDir)
	if err != nil {
		return fmt.Errorf("walk capture dir: %w", err)
	}

	if err := a.uploadFilesToObjectStoreParallel(ctx, hash, captureDir, files); err != nil {
		return fmt.Errorf("upload to object store: %w", err)
	}

	var total int64
	for _, f := range files {
		total += f.size
	}
	span.SetAttributes(
		attribute.Int("nvsnap.file_count", len(files)),
		attribute.Int64("nvsnap.total_bytes", total),
	)
	log.WithFields(map[string]any{
		"file_count": len(files),
		"bytes":      total,
		"elapsed":    time.Since(start).String(),
	}).Info("UploadCaptureToObjectStore: complete")
	return nil
}

// uploadFilesToObjectStoreParallel PUTs each file under captureDir to
// <HomeBucket>/<hash>/<relpath>, HEAD-skipping objects already present at
// the same size (idempotent re-push). Workers stop on the first error.
func (a *Agent) uploadFilesToObjectStoreParallel(ctx context.Context, hash, captureDir string, files []dumpFile) error {
	jobs := make(chan dumpFile, len(files))
	for _, f := range files {
		jobs <- f
	}
	close(jobs)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errs := make(chan error, objectStoreUploadConcurrency)
	var wg sync.WaitGroup
	for i := 0; i < objectStoreUploadConcurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobs {
				if ctx.Err() != nil {
					return
				}
				if err := a.uploadOneFileToObjectStore(ctx, hash, captureDir, f); err != nil {
					select {
					case errs <- err:
						cancel()
					default:
					}
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	return <-errs
}

// uploadOneFileToObjectStore uploads a single file. key = "<hash>/<relpath>".
func (a *Agent) uploadOneFileToObjectStore(ctx context.Context, hash, captureDir string, f dumpFile) error {
	key := hash + "/" + f.relPath
	// Idempotent skip: same key+size already in the bucket.
	if info, err := a.objectStoreHome.Head(ctx, key); err == nil && info.Size == f.size {
		return nil
	}
	fh, err := os.Open(filepath.Join(captureDir, filepath.FromSlash(f.relPath)))
	if err != nil {
		return fmt.Errorf("open %s: %w", f.relPath, err)
	}
	defer func() { _ = fh.Close() }()
	if err := a.objectStoreHome.Put(ctx, key, fh, f.size); err != nil {
		return fmt.Errorf("put %s: %w", key, err)
	}
	return nil
}
