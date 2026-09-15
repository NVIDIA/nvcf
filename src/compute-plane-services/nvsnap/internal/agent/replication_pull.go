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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"go.opentelemetry.io/otel/attribute"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/objectstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/tracing"
)

// replicateHandler triggers a cross-cluster pull-through for the {hash} path
// var. The pull is asynchronous: materializing the capture into a local L2
// rox PVC involves Hyperdisk provisioning + a mount-holder copy that can take
// minutes (independent of size), far longer than any HTTP client timeout. So
// the handler first does a fast bucket HEAD to confirm the hash is reachable,
// then kicks ReplicateFromObjectStore in the background (context.Background,
// not the request ctx — a client disconnect must NOT abort an in-flight
// promote) and returns 202. Callers observe readiness via the catalog's
// pvc_promote_state, exactly like a native capture's async L2 promote (#166).
//
// 202 Accepted: pull started (idempotency is handled by
// ReplicateFromObjectStore, which returns fast if the L2 rox PVC already
// exists). 404: no configured bucket holds the hash. 501: replication
// disabled.
func (a *Agent) replicateHandler(w http.ResponseWriter, r *http.Request) {
	hash := mux.Vars(r)["hash"]
	if hash == "" {
		http.Error(w, "capture hash required", http.StatusBadRequest)
		return
	}
	if a.objectStoreHome == nil {
		http.Error(w, "replication not enabled on this agent", http.StatusNotImplemented)
		return
	}
	// NOTE: no synchronous "already committed → 409" short-circuit here.
	// The chain Stat used to gate this on L1 (local hostPath cache),
	// which made a replicate that failed at L2 (the rox PVC restore
	// actually mounts) report "already committed" forever and never
	// self-heal. The authoritative L2 idempotency check now lives in
	// ReplicateFromObjectStore (StatInNamespace against rox-<hash> in the
	// source namespace), which returns nil fast when L2 is truly
	// present and otherwise re-promotes. The handler just kicks it off.
	// Fast reachability check so we can 404 synchronously instead of
	// accepting a pull that will never find bytes.
	if _, err := a.probeBucketsForHash(r.Context(), hash); err != nil {
		if errors.Is(err, checkpointstore.ErrNotFound) {
			http.Error(w, "hash not found in any configured bucket", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	go func() {
		if err := a.ReplicateFromObjectStore(context.Background(), hash); err != nil {
			a.log.WithError(err).WithField("hash", hash).Warn("background replicate from object store failed")
		}
	}()
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte("replication started; poll pvc_promote_state for readiness\n"))
}

// objectStoreDownloadConcurrency is the number of parallel object GETs during a pull.
const objectStoreDownloadConcurrency = 8

// ReplicateFromObjectStore is the cross-cluster pull-through: given a content hash,
// it probes the home + peer buckets for that capture, downloads its tree +
// manifest into a local staging dir, and replays the capture commit through
// the agent's normal backend chain (manifest CM + L2 rox PVC). After that,
// NVCA and the admission webhook treat the hash exactly like a native local
// capture. If the capture was pulled from a peer bucket (not home), the bytes
// are also pushed into the home bucket so the next local restore reads home
// (pull-through cache).
//
// Returns checkpointstore.ErrNotFound if no configured bucket holds the hash.
// Idempotent: if the hash is already committed locally, returns nil fast.
func (a *Agent) ReplicateFromObjectStore(ctx context.Context, hash string) error {
	if a.objectStoreHome == nil {
		return errors.New("ReplicateFromObjectStore: replication not enabled")
	}
	if a.captureBackend == nil {
		return errors.New("ReplicateFromObjectStore: capture backend not initialized")
	}
	if hash == "" {
		return errors.New("hash required")
	}
	ctx, span := tracing.Tracer().Start(ctx, "replicate.object_store_pull")
	defer span.End()
	span.SetAttributes(attribute.String("nvsnap.hash", hash))

	log := a.log.WithField("hash", hash)

	// Probe home first, then peers, for "<hash>/manifest.json".
	src, err := a.probeBucketsForHash(ctx, hash)
	if err != nil {
		return err
	}
	span.SetAttributes(attribute.String("nvsnap.object_store_source", src.Name()))
	log = log.WithField("source_bucket", src.Name())

	// Idempotency must be checked against the L2 rox PVC specifically —
	// NOT the chain Stat, which also succeeds when only L1 (the local
	// hostPath cache) has the hash. Cross-cluster restore mounts the L2
	// rox PVC, so a replicate that committed L1 but failed at L2 (e.g. a
	// transient mount-holder ImagePullBackOff) must NOT report "already
	// committed" — that's the permanent stuck state this guards against.
	// The rox PVC lives in the source pod's namespace (targetNamespace,
	// nvsnap#82), which we learn from the pulled manifest, so fetch just
	// the manifest first before committing to the full tree download.
	mfManifest, err := a.fetchManifest(ctx, src, hash)
	if err != nil {
		return fmt.Errorf("fetch manifest: %w", err)
	}
	roxNS := mfManifest.SourcePodMeta["namespace"]
	if l2, ok := a.l2Backend.(interface {
		StatInNamespace(context.Context, string, string) (checkpointstore.Manifest, error)
	}); ok && roxNS != "" {
		if _, serr := l2.StatInNamespace(ctx, hash, roxNS); serr == nil {
			log.WithField("namespace", roxNS).Info("ReplicateFromObjectStore: L2 rox PVC already present; nothing to do")
			return nil
		} else if !errors.Is(serr, checkpointstore.ErrNotFound) {
			return fmt.Errorf("L2 rox stat: %w", serr)
		}
	}
	log.Info("ReplicateFromObjectStore: capture found, pulling")
	start := time.Now()

	// Download the whole "<hash>/" prefix into a staging dir.
	cacheRoot := a.config.RootfsCapture.CacheDir
	if cacheRoot == "" {
		return errors.New("ReplicateFromObjectStore: RootfsCapture.CacheDir empty")
	}
	staging := filepath.Join(cacheRoot, ".pull", hash)
	if rmErr := os.RemoveAll(staging); rmErr != nil {
		return fmt.Errorf("clean staging: %w", rmErr)
	}
	if mkErr := os.MkdirAll(staging, 0o755); mkErr != nil {
		return fmt.Errorf("mkdir staging: %w", mkErr)
	}
	defer func() { _ = os.RemoveAll(staging) }()

	bytes, manifest, err := a.downloadCapture(ctx, src, hash, staging)
	if err != nil {
		return fmt.Errorf("download capture: %w", err)
	}

	// Replay the commit. The downloaded tree lives at <staging>/tree;
	// hand it to the backend as a single rootfs source rooted at the
	// tree dir (DstSubpath "" → copy the whole tree). The backend chain
	// writes the manifest CM (→ Reconciler → DB) and promotes to the
	// local L2 rox PVC.
	treeDir := filepath.Join(staging, "tree")
	if fi, statErr := os.Stat(treeDir); statErr != nil || !fi.IsDir() {
		return fmt.Errorf("pulled capture missing tree/ dir at %s", treeDir)
	}
	// CapturedOnNodes from the source cluster are meaningless here — the
	// L2 rox PVC is RWX (any node). Empty = "any node" to the webhook.
	manifest.CapturedOnNodes = nil
	sources := []checkpointstore.CaptureSource{{
		Kind:       checkpointstore.SourceKindRootfs,
		SrcPath:    treeDir,
		DstSubpath: "",
	}}
	if _, err := a.captureBackend.Put(ctx, hash, sources, manifest); err != nil {
		if errors.Is(err, checkpointstore.ErrExists) {
			log.Info("ReplicateFromObjectStore: hash committed concurrently")
			return nil
		}
		return fmt.Errorf("replay commit: %w", err)
	}

	span.SetAttributes(attribute.Int64("nvsnap.total_bytes", bytes))
	log.WithFields(map[string]any{
		"bytes":   bytes,
		"elapsed": time.Since(start).String(),
	}).Info("ReplicateFromObjectStore: committed locally")

	// Pull-through cache: if pulled from a peer, also land in home so the
	// next local restore reads home, not cross-cluster. Best-effort.
	if src.Name() != a.objectStoreHome.Name() {
		if err := a.UploadCaptureToObjectStore(ctx, hash); err != nil {
			log.WithError(err).Warn("ReplicateFromObjectStore: cache-to-home failed (non-fatal)")
		}
	}
	return nil
}

// probeBucketsForHash HEADs "<hash>/manifest.json" across home then peers and
// returns the first bucket that has it, or checkpointstore.ErrNotFound.
func (a *Agent) probeBucketsForHash(ctx context.Context, hash string) (objectstore.Bucket, error) {
	key := hash + "/manifest.json"
	buckets := append([]objectstore.Bucket{a.objectStoreHome}, a.objectStorePeers...)
	for _, b := range buckets {
		_, err := b.Head(ctx, key)
		if err == nil {
			return b, nil
		}
		if !errors.Is(err, objectstore.ErrNotFound) {
			a.log.WithError(err).WithField("bucket", b.Name()).
				Warn("ReplicateFromObjectStore: bucket probe error (continuing)")
		}
	}
	return nil, checkpointstore.ErrNotFound
}

// fetchManifest GETs just "<hash>/manifest.json" from src and parses it.
// Used by the L2 idempotency check to learn the source pod's namespace
// (where the rox PVC lives) before committing to the full tree download.
func (a *Agent) fetchManifest(ctx context.Context, src objectstore.Bucket, hash string) (checkpointstore.Manifest, error) {
	r, err := src.Get(ctx, hash+"/manifest.json")
	if err != nil {
		return checkpointstore.Manifest{}, err
	}
	defer func() { _ = r.Close() }()
	var m checkpointstore.Manifest
	if err := json.NewDecoder(r).Decode(&m); err != nil {
		return checkpointstore.Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	return m, nil
}

// downloadCapture lists "<hash>/" in src and downloads every object into
// staging (stripping the "<hash>/" key prefix so files land at their tree
// paths). Returns total bytes and the parsed manifest.
func (a *Agent) downloadCapture(ctx context.Context, src objectstore.Bucket, hash, staging string) (int64, checkpointstore.Manifest, error) {
	prefix := hash + "/"
	objs, err := src.List(ctx, prefix)
	if err != nil {
		return 0, checkpointstore.Manifest{}, err
	}
	if len(objs) == 0 {
		return 0, checkpointstore.Manifest{}, checkpointstore.ErrNotFound
	}

	var total int64
	jobs := make(chan objectstore.ObjectInfo, len(objs))
	for _, o := range objs {
		jobs <- o
		total += o.Size
	}
	close(jobs)

	dctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, objectStoreDownloadConcurrency)
	var wg sync.WaitGroup
	for i := 0; i < objectStoreDownloadConcurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for o := range jobs {
				if dctx.Err() != nil {
					return
				}
				rel := strings.TrimPrefix(o.Key, prefix)
				if rel == "" {
					continue
				}
				if dErr := a.downloadOneObject(dctx, src, o.Key, filepath.Join(staging, filepath.FromSlash(rel))); dErr != nil {
					select {
					case errs <- dErr:
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
	if dlErr := <-errs; dlErr != nil {
		return 0, checkpointstore.Manifest{}, dlErr
	}

	// Parse the manifest we just downloaded.
	mf, err := os.Open(filepath.Join(staging, "manifest.json"))
	if err != nil {
		return 0, checkpointstore.Manifest{}, fmt.Errorf("open pulled manifest: %w", err)
	}
	defer func() { _ = mf.Close() }()
	var m checkpointstore.Manifest
	if err := json.NewDecoder(mf).Decode(&m); err != nil {
		return 0, checkpointstore.Manifest{}, fmt.Errorf("decode pulled manifest: %w", err)
	}
	return total, m, nil
}

// downloadOneObject GETs key from src and writes it to dst (creating parent
// dirs). The write is direct (no temp+rename) — the whole staging dir is
// removed on any error by the caller.
func (a *Agent) downloadOneObject(ctx context.Context, src objectstore.Bucket, key, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("mkdir for %s: %w", dst, err)
	}
	r, err := src.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("get %s: %w", key, err)
	}
	defer func() { _ = r.Close() }()
	f, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return f.Close()
}
