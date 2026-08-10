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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/tracing"
)

// Phase 5d.2 capture-side blob uploader.
//
// After a successful CRIU dump completes locally, UploadCheckpoint
// walks the dump dir, content-addresses each file, and uploads
// missing blobs to the cluster's nvsnap-blobstore. On completion it
// PUTs a per-capture manifest and POSTs a /blob-uploaded callback
// to nvsnap-server so /sources can route restores to the blob store
// when no peer is reachable.
//
// Async by design: the source pod has already resumed before this
// runs. A node crash during upload loses ~30 s of in-flight bytes;
// the catalog still has the entry but no blob URI. Future captures
// of the same hash de-dup against existing blobs (HEAD before PUT).

// blobUploadConcurrency is the number of parallel per-file uploads.
// 8 saturates ~10 Gbps in-VPC for a typical CRIU dump (handful of
// large pages-*.img + many small core-*.img). Bumping higher just
// loads the blobstore PVC's IOPS without speeding total upload.
const blobUploadConcurrency = 8

// blobUploadTimeoutPerFile bounds one file's upload. pages-*.img
// can be GBs; on slow links the whole dump can take minutes, but
// per-file should still finish under this budget.
const blobUploadTimeoutPerFile = 10 * time.Minute

// fallbackString returns a if non-empty, else b.
func fallbackString(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// registerCheckpointInCatalog upserts a catalog row for this
// checkpoint via nvsnap-server. Idempotent — safe to call from
// retry paths. Best-effort: failures don't fail capture.
//
// Required so peer-add, blob-uploaded, and /sources can find the
// row. Without this, agents capturing via the direct /v1/checkpoint
// API land orphan checkpoints (rows only exist when capture is
// initiated through nvsnap-server's CRD-driven flow).
//
// catalog (nvsnap#59) carries the content-addressed identity fields
// — image_ref, model_id, engine_flags, driver_version, etc. They
// land on the nvsnap-server row so NVCA's Hook A can find this
// checkpoint via POST /api/v1/checkpoints/lookup across fvIDs.
// Pass an empty CatalogInfo to skip (older agents, tests).
func (a *Agent) registerCheckpointInCatalog(ctx context.Context, checkpointID, namespace, podName, containerName, containerImage string, size int64, duration float64, hasGPU bool, catalog CatalogInfo) error {
	if a.config.CatalogURL == "" {
		return errors.New("CatalogURL not configured")
	}
	body, _ := json.Marshal(struct {
		CheckpointID      string   `json:"checkpoint_id"`
		Namespace         string   `json:"namespace"`
		PodName           string   `json:"pod_name"`
		ContainerName     string   `json:"container_name,omitempty"`
		ContainerImage    string   `json:"container_image,omitempty"`
		NodeName          string   `json:"node_name"`
		CheckpointSize    int64    `json:"checkpoint_size,omitempty"`
		Status            string   `json:"status"`
		HasGPU            bool     `json:"has_gpu"`
		DurationSecs      float64  `json:"duration_secs,omitempty"`
		Hash              string   `json:"hash,omitempty"`
		ImageRef          string   `json:"image_ref,omitempty"`
		ImageDigest       string   `json:"image_digest,omitempty"`
		ModelID           string   `json:"model_id,omitempty"`
		EngineFlags       []string `json:"engine_flags,omitempty"`
		GPUType           string   `json:"gpu_type,omitempty"`
		GPUCount          int      `json:"gpu_count,omitempty"`
		DriverVersion     string   `json:"driver_version,omitempty"`
		CUDAVersion       string   `json:"cuda_version,omitempty"`
		CPUArchitecture   string   `json:"cpu_architecture,omitempty"`
		FunctionName      string   `json:"function_name,omitempty"`
		FunctionVersionID string   `json:"function_version_id,omitempty"`
	}{
		CheckpointID:   checkpointID,
		Namespace:      namespace,
		PodName:        podName,
		ContainerName:  containerName,
		ContainerImage: containerImage,
		NodeName:       a.config.NodeName,
		CheckpointSize: size,
		Status:         "Completed",
		HasGPU:         hasGPU,
		DurationSecs:   duration,
		// CatalogInfo passthrough. Image ref defaults to the container
		// image when the catalog lookup couldn't resolve a digest yet
		// — server still needs ImageRef populated for indexed lookup.
		Hash:              catalog.Hash,
		ImageRef:          fallbackString(catalog.ImageRef, containerImage),
		ImageDigest:       catalog.ImageDigest,
		ModelID:           catalog.ModelID,
		EngineFlags:       catalog.EngineFlags,
		GPUType:           catalog.GPUType,
		GPUCount:          catalog.GPUCount,
		DriverVersion:     catalog.DriverVersion,
		CUDAVersion:       catalog.CUDAVersion,
		CPUArchitecture:   catalog.CPUArchitecture,
		FunctionName:      catalog.FunctionName,
		FunctionVersionID: catalog.FunctionVersionID,
	})
	u := fmt.Sprintf("%s/api/v1/checkpoints/register", a.config.CatalogURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytesReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("register: status %d: %s", resp.StatusCode, b)
	}
	return nil
}

// UploadCheckpoint pushes the local dump at <CheckpointDir>/<id>
// to the nvsnap-blobstore configured in a.config.BlobStoreURL, then
// fires the /blob-uploaded catalog callback. Safe to call multiple
// times for the same id (idempotent server-side; HEAD-then-PUT
// dedups blobs already present).
//
// Returns nil on success. If the agent isn't configured for blob
// upload (BlobStoreURL or CatalogURL empty), returns nil silently
// — capture-side uploads are best-effort, and a missing config
// just means "phase 5d.2 disabled, fall back to peer-only fanout".
func (a *Agent) UploadCheckpoint(ctx context.Context, checkpointID string) error {
	ctx, span := tracing.Tracer().Start(ctx, "checkpoint.blobstore_upload")
	defer span.End()
	span.SetAttributes(attribute.String("nvsnap.checkpoint_id", checkpointID))

	if checkpointID == "" {
		span.SetStatus(codes.Error, "checkpoint id required")
		return errors.New("checkpoint id required")
	}
	if a.config.BlobStoreURL == "" {
		span.SetAttributes(attribute.String("nvsnap.upload.skip_reason", "no-blobstore"))
		a.log.WithField("checkpoint_id", checkpointID).
			Debug("blob upload skipped: BlobStoreURL not configured")
		return nil
	}

	dumpDir := filepath.Join(a.config.CheckpointDir, checkpointID)
	if fi, err := os.Stat(dumpDir); err != nil || !fi.IsDir() {
		span.SetStatus(codes.Error, "dump dir not found")
		return fmt.Errorf("checkpoint dir %s not found", dumpDir)
	}

	log := a.log.WithFields(map[string]interface{}{
		"checkpoint_id":  checkpointID,
		"blob_store_url": a.config.BlobStoreURL,
	})
	log.Info("UploadCheckpoint: starting")
	start := time.Now()

	files, err := walkDumpDir(dumpDir)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "walk dump dir")
		return fmt.Errorf("walk dump dir: %w", err)
	}

	manifestFiles, err := a.uploadFilesParallel(ctx, dumpDir, files)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "upload files")
		return fmt.Errorf("upload files: %w", err)
	}

	if err := a.putManifest(ctx, checkpointID, manifestFiles); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "put manifest")
		return fmt.Errorf("put manifest: %w", err)
	}

	if err := a.notifyBlobUploaded(ctx, checkpointID); err != nil {
		// Catalog callback is best-effort — the blobs and manifest
		// are durable. A retry sweep on nvsnap-server can reconcile.
		log.WithError(err).Warn("blob-uploaded callback failed (non-fatal)")
	}

	span.SetAttributes(attribute.Int("nvsnap.upload.file_count", len(manifestFiles)))
	log.WithFields(map[string]interface{}{
		"file_count": len(manifestFiles),
		"elapsed":    time.Since(start).String(),
	}).Info("UploadCheckpoint: complete")
	return nil
}

// UploadCapture is the rootfs counterpart of UploadCheckpoint. Pushes
// every file under <RootfsCapture.CacheDir>/<hash>/ to the nvsnap-
// blobstore (content-addressed blobs + path→sha256 manifest) so that
// EnsureCaptureLocal's tier-3 fallback can reach the bytes when peers
// are gone. Idempotent (HEAD-then-PUT skips already-present blobs).
//
// Best-effort: a missing BlobStoreURL or a transient upload failure
// returns an error but does NOT roll back the local capture. Peer
// fetch (tier-2) keeps working as long as CapturedOnNodes are alive.
func (a *Agent) UploadCapture(ctx context.Context, hash string) error {
	ctx, span := tracing.Tracer().Start(ctx, "capture.blobstore_upload")
	defer span.End()
	span.SetAttributes(attribute.String("nvsnap.hash", hash))
	if hash == "" {
		return errors.New("hash required")
	}
	if a.config.BlobStoreURL == "" {
		a.log.WithField("hash", hash).
			Debug("capture blob upload skipped: BlobStoreURL not configured")
		return nil
	}
	cacheRoot := a.config.RootfsCapture.CacheDir
	if cacheRoot == "" {
		return errors.New("UploadCapture: RootfsCapture.CacheDir empty")
	}
	captureDir := filepath.Join(cacheRoot, hash)
	if fi, err := os.Stat(captureDir); err != nil || !fi.IsDir() {
		return fmt.Errorf("capture dir %s not found", captureDir)
	}

	log := a.log.WithFields(map[string]interface{}{
		"hash":           hash,
		"blob_store_url": a.config.BlobStoreURL,
	})
	log.Info("UploadCapture: starting")
	start := time.Now()

	files, err := walkDumpDir(captureDir)
	if err != nil {
		return fmt.Errorf("walk capture dir: %w", err)
	}

	manifestFiles, err := a.uploadFilesParallel(ctx, captureDir, files)
	if err != nil {
		return fmt.Errorf("upload files: %w", err)
	}

	if err := a.putManifest(ctx, hash, manifestFiles); err != nil {
		return fmt.Errorf("put manifest: %w", err)
	}

	log.WithFields(map[string]interface{}{
		"file_count": len(manifestFiles),
		"elapsed":    time.Since(start).String(),
	}).Info("UploadCapture: complete")
	return nil
}

// dumpFile is a per-file work item for the upload pool.
type dumpFile struct {
	relPath string
	size    int64
}

// walkDumpDir returns every regular file under dumpDir as a list
// of (relPath, size) entries with forward-slash paths. Directories
// and symlinks are skipped — CRIU dumps don't contain symlinks
// inside the images dir, and dirs are implicit from path prefixes.
func walkDumpDir(dumpDir string) ([]dumpFile, error) {
	var out []dumpFile
	err := filepath.Walk(dumpDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dumpDir, path)
		if err != nil {
			return err
		}
		out = append(out, dumpFile{
			relPath: filepath.ToSlash(rel),
			size:    info.Size(),
		})
		return nil
	})
	return out, err
}

// uploadFilesParallel runs blobUploadConcurrency workers; each
// worker pulls one file off the channel, sha256-streams it from
// disk into a HEAD/PUT against the blob store, and emits a
// ManifestFile for the manifest assembly step.
//
// Returns the assembled manifest entries on success. On any
// worker error, the first one wins; the others drain quickly via
// context cancellation.
func (a *Agent) uploadFilesParallel(ctx context.Context, dumpDir string, files []dumpFile) ([]manifestFile, error) {
	type result struct {
		mf  manifestFile
		err error
	}
	jobs := make(chan dumpFile, len(files))
	for _, f := range files {
		jobs <- f
	}
	close(jobs)

	results := make(chan result, len(files))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < blobUploadConcurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobs {
				if ctx.Err() != nil {
					return
				}
				mf, err := a.uploadOneFile(ctx, dumpDir, f)
				results <- result{mf: mf, err: err}
				if err != nil {
					cancel()
					return
				}
			}
		}()
	}
	wg.Wait()
	close(results)

	var out []manifestFile //nolint:prealloc // filled from a channel range, no known length
	var firstErr error
	for r := range results {
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		out = append(out, r.mf)
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// uploadOneFile sha256-streams the file into a HEAD/PUT against
// the blob store. HEAD-then-PUT skips re-upload of duplicate
// blobs across captures (typical for sglang/trtllm runs that
// share libuv/libzmq blobs across recaptures).
func (a *Agent) uploadOneFile(ctx context.Context, dumpDir string, f dumpFile) (manifestFile, error) {
	abs := filepath.Join(dumpDir, filepath.FromSlash(f.relPath))
	sha, err := sha256File(abs)
	if err != nil {
		return manifestFile{}, fmt.Errorf("sha256 %s: %w", f.relPath, err)
	}

	mf := manifestFile{Path: f.relPath, SHA256: sha, Size: f.size}

	fileCtx, cancel := context.WithTimeout(ctx, blobUploadTimeoutPerFile)
	defer cancel()

	// HEAD first — dedup. 200 = present, skip upload entirely.
	headURL := fmt.Sprintf("%s/v1/blob/%s", a.config.BlobStoreURL, sha)
	headReq, _ := http.NewRequestWithContext(fileCtx, http.MethodHead, headURL, http.NoBody)
	if resp, headErr := http.DefaultClient.Do(headReq); headErr == nil {
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return mf, nil
		}
	}

	// PUT the bytes. We re-open the file (not reuse the sha256
	// pass) so the upload streams without buffering — important
	// for multi-GB blobs where buffering would OOM the agent pod.
	src, err := os.Open(abs)
	if err != nil {
		return manifestFile{}, err
	}
	defer func() { _ = src.Close() }()
	putURL := fmt.Sprintf("%s/v1/blob/%s", a.config.BlobStoreURL, sha)
	putReq, err := http.NewRequestWithContext(fileCtx, http.MethodPut, putURL, src)
	if err != nil {
		return manifestFile{}, err
	}
	putReq.ContentLength = f.size
	putReq.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		return manifestFile{}, fmt.Errorf("PUT %s: %w", f.relPath, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusOK:
		return mf, nil
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return manifestFile{}, fmt.Errorf("PUT %s: status %d: %s", f.relPath, resp.StatusCode, body)
	}
}

// sha256File computes sha256 of a file by streaming through a
// 1 MiB buffer. Memory bounded regardless of file size.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// manifestFile mirrors blobstore.ManifestFile exactly. Defined
// here to keep the agent package free of an import on the
// blobstore package (one-way dependency: agent calls blobstore
// over HTTP only).
type manifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type manifestPayload struct {
	Files []manifestFile `json:"files"`
}

// putManifest uploads the per-capture manifest (path → sha256,
// size) so a future restore can reverse-map files. Without the
// manifest, the blobstore's blobs are content-addressed but
// path-less.
func (a *Agent) putManifest(ctx context.Context, checkpointID string, files []manifestFile) error {
	body, err := json.Marshal(manifestPayload{Files: files})
	if err != nil {
		return err
	}
	manifestURL := fmt.Sprintf("%s/v1/capture/%s/manifest.json",
		a.config.BlobStoreURL, url.PathEscape(checkpointID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, manifestURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("PUT manifest: status %d: %s", resp.StatusCode, b)
	}
	return nil
}

// notifyBlobUploaded fires the catalog callback so future
// /sources queries surface the blob URI. Best-effort — the bytes
// are durable regardless.
func (a *Agent) notifyBlobUploaded(ctx context.Context, checkpointID string) error {
	if a.config.CatalogURL == "" {
		return errors.New("CatalogURL not configured")
	}
	body, _ := json.Marshal(struct {
		BlobURI string `json:"blob_uri"`
	}{
		BlobURI: a.config.BlobStoreURL,
	})
	u := fmt.Sprintf("%s/api/v1/checkpoints/%s/blob-uploaded",
		a.config.CatalogURL, url.PathEscape(checkpointID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("blob-uploaded callback: status %d: %s", resp.StatusCode, b)
	}
	return nil
}
