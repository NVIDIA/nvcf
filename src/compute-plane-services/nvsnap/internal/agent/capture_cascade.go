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
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/tracing"
)

// EnsureCaptureLocal is the rootfs-only counterpart of EnsureLocal:
// makes a captured rootfs tree locally available under
// <RootfsCapture.CacheDir>/<hash>/ before the in-agent admission
// webhook resolves Backend.Mount(hash, vm).
//
// Tier 1: same-node short-circuit. If <CacheDir>/<hash>/ already
//
//	exists, return nil — this is the capture-source node or a
//	previously-served peer-fetched copy.
//
// Tier 2: peer fetch. Read Manifest.CapturedOnNodes from the
//
//	ConfigMap (nvsnap-capture-<shortHash> in CMNamespace). For each
//	node in the list (excluding ourself), resolve the node's
//	InternalIP and pull the capture's file tree from
//	http://<nodeIP>:<port>/v1/captures/{hash}/manifest and
//	.../file?path=<rel>. First peer that succeeds wins.
//
// Tier 3: nvsnap-blobstore. If all peers fail (or none exist — e.g.
// the source node was drained), fall back to fetching from the
// cluster's nvsnap-blobstore. The blobstore stores per-file content-
// addressed blobs + a path→sha256 manifest under
// /v1/capture/<hash>/manifest.json. This is the resilience path: it
// keeps restore working when CapturedOnNodes are unreachable.
//
// Idempotent — safe for the webhook to call before every Mount.
func (a *Agent) EnsureCaptureLocal(ctx context.Context, hash string) error {
	ctx, span := tracing.Tracer().Start(ctx, "capture.ensure_local")
	defer span.End()
	span.SetAttributes(attribute.String("nvsnap.hash", hash))
	if hash == "" {
		return errors.New("EnsureCaptureLocal: hash required")
	}
	cacheRoot := a.config.RootfsCapture.CacheDir
	if cacheRoot == "" {
		return errors.New("EnsureCaptureLocal: --rootfs-capture not enabled (CacheDir empty)")
	}
	if a.kubeClient == nil {
		return errors.New("EnsureCaptureLocal: kube client unavailable")
	}

	localDir := filepath.Join(cacheRoot, hash)

	// Tier 1: same-node short-circuit.
	if fi, err := os.Stat(localDir); err == nil && fi.IsDir() {
		return nil
	}

	// Resolve the manifest from the cluster-wide ConfigMap. The CM
	// name uses short-hash (first 32 hex chars). See
	// internal/checkpointstore/cmregistry.go:CMNameFor.
	cmName := checkpointstore.CMNameFor(hash)
	cmNS := a.config.RootfsCapture.CMNamespace
	if cmNS == "" {
		return errors.New("EnsureCaptureLocal: rootfs-cm-namespace not set")
	}

	cm, err := a.kubeClient.CoreV1().ConfigMaps(cmNS).Get(ctx, cmName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read manifest CM %s/%s: %w", cmNS, cmName, err)
	}
	manifestJSON, ok := cm.Data[checkpointstore.CMDataKey]
	if !ok {
		return fmt.Errorf("manifest CM %s/%s missing %q key", cmNS, cmName, checkpointstore.CMDataKey)
	}
	var manifest checkpointstore.Manifest
	if err := json.Unmarshal([]byte(manifestJSON), &manifest); err != nil {
		return fmt.Errorf("decode manifest CM: %w", err)
	}
	if len(manifest.CapturedOnNodes) == 0 {
		return fmt.Errorf("manifest %s has empty CapturedOnNodes; no peer to fetch from", checkpointstore.ShortHash(hash))
	}

	log := a.log.WithFields(map[string]interface{}{
		"hash":     checkpointstore.ShortHash(hash),
		"peers":    manifest.CapturedOnNodes,
		"self":     a.config.NodeName,
		"localDir": localDir,
	})
	log.Info("EnsureCaptureLocal: cascade starting")

	// Tier 2: try each peer node in order. Skip ourselves — we already
	// checked Tier 1 above. Resolve node IP via the K8s API.
	for _, peerNode := range manifest.CapturedOnNodes {
		if peerNode == a.config.NodeName {
			continue
		}
		peerURL, lookupErr := a.peerAgentURL(ctx, peerNode)
		if lookupErr != nil {
			log.WithError(lookupErr).WithField("peer", peerNode).
				Warn("EnsureCaptureLocal: peer URL lookup failed; trying next")
			continue
		}
		peerLog := log.WithFields(map[string]interface{}{
			"peer_node": peerNode,
			"peer_url":  peerURL,
		})
		peerLog.Info("EnsureCaptureLocal: trying peer")
		start := time.Now()
		if fetchErr := a.fetchCaptureFromPeer(ctx, peerURL, hash, localDir); fetchErr != nil {
			peerLog.WithError(fetchErr).Warn("EnsureCaptureLocal: peer fetch failed; trying next")
			// Atomicity: remove any partial dir so a subsequent peer
			// retry (or the next webhook call) starts clean.
			_ = os.RemoveAll(localDir)
			continue
		}
		peerLog.WithField("elapsed", time.Since(start).String()).
			Info("EnsureCaptureLocal: peer fetch complete")
		return nil
	}

	// Tier 3: nvsnap-blobstore (content-addressed). Reachable if the
	// agent's upload pipeline put this capture's blobs there. Builds
	// the same on-disk layout as the peer fetch via fetchFromBlobStore
	// (reused from the CRIU cascade — content-addressed lookup is
	// identical for both paths).
	if a.config.BlobStoreURL != "" {
		blobLog := log.WithField("blobstore", a.config.BlobStoreURL)
		blobLog.Info("EnsureCaptureLocal: trying tier-3 nvsnap-blobstore")
		// Stage under .tmp.<pid> like fetchCaptureFromPeer for atomicity.
		tmpDir := localDir + fmt.Sprintf(".tmp.%d", os.Getpid())
		_ = os.RemoveAll(tmpDir)
		start := time.Now()
		if err := a.fetchFromBlobStore(ctx, a.config.BlobStoreURL, hash, tmpDir); err != nil {
			_ = os.RemoveAll(tmpDir)
			blobLog.WithError(err).Warn("EnsureCaptureLocal: blobstore fetch failed")
		} else if err := os.Rename(tmpDir, localDir); err != nil {
			_ = os.RemoveAll(tmpDir)
			blobLog.WithError(err).Warn("EnsureCaptureLocal: promote blobstore tmpdir failed")
		} else {
			blobLog.WithField("elapsed", time.Since(start).String()).
				Info("EnsureCaptureLocal: blobstore fetch complete")
			return nil
		}
	}

	return fmt.Errorf("all %d peers failed for capture %s (blobstore %s)",
		len(manifest.CapturedOnNodes), checkpointstore.ShortHash(hash),
		coalesce(a.config.BlobStoreURL, "not configured"))
}

// coalesce returns the first non-empty string.
func coalesce(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// fetchCaptureFromPeer pulls every file under <cacheRoot>/<hash>/ on
// one peer agent. Two-step: GET /manifest, then parallel
// /file?path=<rel> for each entry. Mirrors fetchFromPeer (CRIU) but
// scoped to the rootfs cache URL space.
func (a *Agent) fetchCaptureFromPeer(ctx context.Context, peerURL, hash, destDir string) error {
	manifestURL := fmt.Sprintf("%s/v1/captures/%s/manifest", peerURL, url.PathEscape(hash))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("manifest GET: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("manifest GET %d: %s", resp.StatusCode, body)
	}
	var m peerManifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return fmt.Errorf("decode manifest: %w", err)
	}

	// Stage the final dir under a .tmp.<pid> sibling so concurrent
	// webhook calls don't observe a half-fetched dir under the
	// final hash name. Atomic rename at the end.
	tmpDir := destDir + fmt.Sprintf(".tmp.%d", os.Getpid())
	_ = os.RemoveAll(tmpDir)
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return err
	}
	defer func() {
		// Only remove on early return paths — successful rename above
		// will have moved the dir out from under us.
		if _, statErr := os.Stat(tmpDir); statErr == nil {
			_ = os.RemoveAll(tmpDir)
		}
	}()

	type fetchTask struct {
		path string
		size int64
	}
	tasks := make(chan fetchTask, len(m.Files))
	for _, f := range m.Files {
		tasks <- fetchTask{path: f.Path, size: f.Size}
	}
	close(tasks)

	errCh := make(chan error, peerFetchConcurrency)
	var wg sync.WaitGroup
	for i := 0; i < peerFetchConcurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range tasks {
				if err := a.fetchOneCaptureFile(ctx, peerURL, hash, t.path, t.size, tmpDir); err != nil {
					errCh <- fmt.Errorf("file %q: %w", t.path, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for e := range errCh {
		return e // first error wins
	}

	// Atomic promote: rename tmp dir into the final hash dir.
	if err := os.Rename(tmpDir, destDir); err != nil {
		return fmt.Errorf("promote %s -> %s: %w", tmpDir, destDir, err)
	}
	return nil
}

// fetchOneCaptureFile downloads a single file from
// <peerURL>/v1/captures/<hash>/file?path=<rel> into destDir/<rel>.
// Per-file timeout matches fetchOneFile (CRIU) so a slow file failover
// finishes within a single peer round.
func (a *Agent) fetchOneCaptureFile(ctx context.Context, peerURL, hash, relPath string, expectedSize int64, destDir string) error {
	fileURL := fmt.Sprintf("%s/v1/captures/%s/file?path=%s",
		peerURL, url.PathEscape(hash), url.QueryEscape(relPath))
	fileCtx, cancel := context.WithTimeout(ctx, peerFetchTimeoutPerFile)
	defer cancel()
	req, err := http.NewRequestWithContext(fileCtx, http.MethodGet, fileURL, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("GET: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("status %d: %s", resp.StatusCode, body)
	}

	dst := filepath.Join(destDir, filepath.FromSlash(relPath))
	if mkErr := os.MkdirAll(filepath.Dir(dst), 0o755); mkErr != nil {
		return mkErr
	}
	tmp := dst + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(tmp)
	}()

	n, err := io.Copy(f, resp.Body)
	if err != nil {
		return fmt.Errorf("io.Copy: %w", err)
	}
	if expectedSize > 0 && n != expectedSize {
		return fmt.Errorf("size mismatch: got %d, expected %d", n, expectedSize)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("fsync: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// peerAgentURL returns http://<peerInternalIP>:<port> for the nvsnap-agent
// pod running on the given node. The InternalIP is taken from the Node's
// status.addresses. The port matches our own listen port (default 8081).
func (a *Agent) peerAgentURL(ctx context.Context, nodeName string) (string, error) {
	if a.kubeClient == nil {
		return "", errors.New("kube client unavailable")
	}
	node, err := a.kubeClient.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get node %s: %w", nodeName, err)
	}
	var ip string
	for _, addr := range node.Status.Addresses {
		if addr.Type == "InternalIP" && addr.Address != "" {
			ip = addr.Address
			break
		}
	}
	if ip == "" {
		return "", fmt.Errorf("node %s has no InternalIP", nodeName)
	}
	port := "8081"
	if addr := a.config.ListenAddr; len(addr) > 1 && addr[0] == ':' {
		port = addr[1:]
	}
	return fmt.Sprintf("http://%s:%s", ip, port), nil
}
