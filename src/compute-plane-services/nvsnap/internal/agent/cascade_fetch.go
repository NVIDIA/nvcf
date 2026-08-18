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
	"strconv"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/tracing"
)

// Phase 5d.1 restore-side cascading peer fetch.
//
// EnsureLocal makes a checkpoint locally available on this agent's
// node before restore-entrypoint runs. Three priority tiers:
//
//	1. Same-node hostPath — already there, zero transit.
//	2. Peer agent HTTP — any node in the catalog's peer list serves
//	   it; we try most-recently-seen first. Each successful fetch
//	   registers THIS agent as a new peer for future fanouts.
//	3. (Stage 5d.2) Blob store — last-resort fallback; not wired in
//	   this stage. EnsureLocal returns an error after all peers fail.
//
// Concurrency: parallel per-file downloads with a fixed worker pool.
// 8 workers saturate ~10 Gbps in-VPC for the typical CRIU dump file
// mix (handful of large pages-*.img + many small core-*.img).

// peerFetchConcurrency is the number of files fetched in parallel from
// one peer. Each in-flight file holds 1 TCP stream (chunks=1, splice
// path off by default) to the peer.
//
// Default 8: vllm-small bench history (30 GiB cross-node):
//
//	conc=8 chunks=8 userspace:  1.00 GB/s  ← Phase 2 baseline (deprecated)
//	conc=8 chunks=1 userspace:  1.20 GB/s  ← current stable
//	conc=1 chunks=4 splice:     0.88 GB/s  ← regressed in cascade
//	(microbench, big-file only, 4-chunk splice: 1.7 GB/s — didn't
//	 translate to cascade due to small-file http.Client contention)
//
// Settable via NVSNAP_PEER_FETCH_CONCURRENCY for in-cluster perf tuning.
var peerFetchConcurrency = func() int {
	if v := os.Getenv("NVSNAP_PEER_FETCH_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 8
}()

// peerFetchTimeoutPerFile bounds how long ONE file download can take
// before we abandon this peer and try the next. Larger pages-*.img
// files at slow links should still finish under this; if they don't,
// the peer is degraded and we'd rather fail fast.
const peerFetchTimeoutPerFile = 5 * time.Minute

// peerHTTPClient is the HTTP client used for peer + blob-store fetches.
// http.DefaultClient's default Transport has MaxIdleConnsPerHost: 2,
// which forces a fresh TCP handshake on most concurrent requests when
// cascade fetches 8 files in parallel from the same peer (each opening
// a new connection, only 2 cached). With many small CRIU files
// (core-*.img) this thrashes connection setup.
//
// We size the idle pool to the worker count so connections get reused
// across all concurrent fetches. MaxConnsPerHost stays at 0 (no limit)
// so bursts during cascade aren't queued behind the cap.
//
// All cascade-fetch call sites go through this client; downloadToFile
// receives it as an explicit argument so tests can substitute.
var peerHTTPClient = &http.Client{
	// authTransport wraps the tuned transport rather than replacing it: every
	// agent-to-agent request carries the bearer token (a no-op until one is
	// configured) without any cascade call site knowing about auth. See
	// auth.go and GH #486.
	Transport: &authTransport{base: &http.Transport{
		MaxIdleConns:        peerFetchConcurrency * 2,
		MaxIdleConnsPerHost: peerFetchConcurrency * 2,
		IdleConnTimeout:     90 * time.Second,
		// Disable HTTP/2 forced upgrade; we want plain HTTP/1.1 so we
		// can reason about TCP stream count for the Cilium-multi-stream
		// hypothesis. Re-enable explicitly if/when we switch to h2c.
		ForceAttemptHTTP2: false,
	}},
	// Do not follow redirects. net/http drops Authorization when a redirect
	// crosses origins, but authTransport runs on the redirected request too
	// and re-adds it -- so a peer that answers a fetch with a 302 to any host
	// would be handed the shared token. No peer endpoint redirects, so
	// surfacing the 3xx to the caller loses nothing.
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// EnsureLocal guarantees that /var/lib/nvsnap/checkpoints/<checkpointID>/
// exists on this agent's filesystem with all CRIU image files plus
// helpers (rootfs-diff/, mounts/, metadata.json). Returns nil on
// success; error if the cascade exhausted all peers without success.
//
// Idempotent: same-node check first means a no-op if the dump is
// already local (e.g., this is the capture node itself).
func (a *Agent) EnsureLocal(ctx context.Context, checkpointID string) error {
	ctx, span := tracing.Tracer().Start(ctx, "cascade.ensure_local")
	defer span.End()
	span.SetAttributes(attribute.String("nvsnap.checkpoint_id", checkpointID))

	// This ID becomes a directory that the cascade below creates, deletes
	// and writes files into. Everything downstream trusts it.
	if err := validPathSegment("checkpoint id", checkpointID); err != nil {
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	localDir := filepath.Join(a.config.CheckpointDir, checkpointID)

	// Tier 1: same-node short-circuit. If inventory.img exists locally,
	// the checkpoint is already here — capture-source node, or a
	// previous restore deposited it.
	if fi, err := os.Stat(filepath.Join(localDir, "inventory.img")); err == nil && fi.Size() > 0 {
		span.SetAttributes(attribute.String("nvsnap.cascade.tier", "L1-local"))
		a.log.WithField("checkpoint_id", checkpointID).Info("EnsureLocal: same-node hit, skipping cascade")
		// Still register as peer in case we somehow weren't tracked.
		_ = a.registerAsPeer(ctx, checkpointID)
		return nil
	}

	// Tier 2: shared filesystem (FSStore). Phase 2c — when the cluster
	// has a DFS mounted on every node, the agent reads directly from
	// the shared mount and skips the network cascade entirely. Probed
	// before catalog lookup so DFS-equipped clusters don't even hit
	// nvsnap-server for already-published checkpoints.
	if a.fsStore.hasCheckpoint(checkpointID) {
		fsCtx, fsSpan := tracing.Tracer().Start(ctx, "cascade.fsstore_fetch")
		fsSpan.SetAttributes(attribute.String("nvsnap.cascade.tier", "L2-fsstore"))
		a.log.WithField("checkpoint_id", checkpointID).Info("EnsureLocal: FSStore hit, copying from shared mount")
		fsStart := time.Now()
		if err := a.fsStore.fetchToLocal(fsCtx, checkpointID, localDir); err != nil {
			fsSpan.RecordError(err)
			fsSpan.SetStatus(codes.Error, "fsstore copy failed")
			fsSpan.End()
			a.log.WithError(err).WithField("checkpoint_id", checkpointID).
				Warn("FSStore copy failed; falling through to peer cascade")
			_ = os.RemoveAll(localDir)
		} else {
			fsSpan.End()
			span.SetAttributes(attribute.String("nvsnap.cascade.tier", "L2-fsstore"))
			a.log.WithFields(map[string]interface{}{
				"checkpoint_id": checkpointID,
				"elapsed":       time.Since(fsStart).String(),
			}).Info("EnsureLocal: FSStore copy complete")
			if err := a.registerAsPeer(ctx, checkpointID); err != nil {
				a.log.WithError(err).Warn("peer-add to catalog failed (non-fatal)")
			}
			return nil
		}
	}

	if a.config.CatalogURL == "" {
		return fmt.Errorf("checkpoint %s not local and CatalogURL unset; cannot cascade", checkpointID)
	}

	sources, err := a.fetchCheckpointSources(ctx, checkpointID)
	if err != nil {
		return fmt.Errorf("query catalog sources: %w", err)
	}

	a.log.WithFields(map[string]interface{}{
		"checkpoint_id": checkpointID,
		"peer_count":    len(sources.Peers),
		"blob_uri":      sources.BlobURI,
	}).Info("EnsureLocal: cascade starting")

	// Tier 2: try peers in LEAST-LOADED order. Skip self.
	//
	// Phase 1 fan-out fix: when 15 receivers hit the catalog at the
	// same time and only the capture node has the data, naive in-order
	// iteration sends all 15 receivers to peer[0] (the capture node)
	// concurrently — a 15-to-1 bottleneck. Sorting by the agent's
	// process-local in-progress-stream count spreads load across peers
	// as soon as more than one peer has the data: the first receiver
	// pulls from the capture node, completes, registers as a peer; the
	// second receiver sees the capture node has 0 in-flight (already
	// done) AND a new peer at 0; etc. Tree forms naturally.
	//
	// Counter is process-local (this agent's view of its own in-flight
	// fetches). For cross-agent coordination at very large fan-out we'd
	// need a distributed counter, but for 16 nodes the local view is
	// sufficient and avoids the catalog round-trip overhead.
	selfURL := a.selfAgentURL()
	candidates := make([]peerInfo, 0, len(sources.Peers))
	for _, peer := range sources.Peers {
		if peer.AgentURL == selfURL {
			continue
		}
		candidates = append(candidates, peer)
	}
	sortPeersByLoad(candidates, a.peerLoad)
	for i, peer := range candidates {
		// Phase 3 of the 16-node distribution plan: when more than one
		// peer has the checkpoint, large-file chunks distribute across
		// all of them. `alts` is every other candidate; primary handles
		// manifest + small files + its share of chunks.
		alts := make([]string, 0, len(candidates)-1)
		for j, p := range candidates {
			if i == j {
				continue
			}
			alts = append(alts, p.AgentURL)
		}
		log := a.log.WithFields(map[string]interface{}{
			"checkpoint_id": checkpointID,
			"peer_node":     peer.NodeName,
			"peer_url":      peer.AgentURL,
			"peer_load":     a.peerLoad.get(peer.AgentURL),
			"alt_count":     len(alts),
		})
		log.Info("EnsureLocal: trying peer")
		peerCtx, peerSpan := tracing.Tracer().Start(ctx, "cascade.peer_fetch")
		peerSpan.SetAttributes(
			attribute.String("nvsnap.cascade.tier", "L2-peer"),
			attribute.String("nvsnap.peer.node", peer.NodeName),
			attribute.String("nvsnap.peer.url", peer.AgentURL),
			attribute.Int("nvsnap.peer.alt_count", len(alts)),
		)
		start := time.Now()
		a.peerLoad.inc(peer.AgentURL)
		peerErr := a.fetchFromPeer(peerCtx, peer.AgentURL, alts, checkpointID, localDir)
		a.peerLoad.dec(peer.AgentURL)
		if peerErr != nil {
			peerSpan.RecordError(peerErr)
			peerSpan.SetStatus(codes.Error, "peer fetch failed")
			peerSpan.End()
			log.WithError(peerErr).Warn("peer fetch failed; trying next")
			// Best-effort cleanup of partial download.
			_ = os.RemoveAll(localDir)
			continue
		}
		peerSpan.End()
		span.SetAttributes(attribute.String("nvsnap.cascade.tier", "L2-peer"))
		log.WithField("elapsed", time.Since(start).String()).Info("EnsureLocal: peer fetch complete")
		// Register self as a new peer so future fanouts can use us.
		if regErr := a.registerAsPeer(ctx, checkpointID); regErr != nil {
			a.log.WithError(regErr).Warn("peer-add to catalog failed (non-fatal — restore proceeds)")
		}
		return nil
	}

	// Tier 3: blob-store fallback. The catalog hands us the
	// nvsnap-blobstore base URL; we fetch the per-capture manifest
	// and parallel-download blobs into the same destDir layout
	// the peer path produces.
	if sources.BlobURI != "" {
		blobCtx, blobSpan := tracing.Tracer().Start(ctx, "cascade.blobstore_fetch")
		blobSpan.SetAttributes(
			attribute.String("nvsnap.cascade.tier", "L3-blobstore"),
			attribute.String("nvsnap.blob.uri", sources.BlobURI),
		)
		log := a.log.WithFields(map[string]interface{}{
			"checkpoint_id": checkpointID,
			"blob_uri":      sources.BlobURI,
		})
		log.Info("EnsureLocal: falling back to blob store")
		blobStart := time.Now()
		if blobErr := a.fetchFromBlobStore(blobCtx, sources.BlobURI, checkpointID, localDir); blobErr != nil {
			blobSpan.RecordError(blobErr)
			blobSpan.SetStatus(codes.Error, "blob fallback failed")
			blobSpan.End()
			_ = os.RemoveAll(localDir)
			return fmt.Errorf("blob fallback failed: %w", blobErr)
		}
		blobSpan.End()
		span.SetAttributes(attribute.String("nvsnap.cascade.tier", "L3-blobstore"))
		log.WithField("elapsed", time.Since(blobStart).String()).Info("EnsureLocal: blob store fetch complete")
		if regErr := a.registerAsPeer(ctx, checkpointID); regErr != nil {
			a.log.WithError(regErr).Warn("peer-add to catalog failed (non-fatal)")
		}
		return nil
	}
	err = fmt.Errorf("all %d peers failed for checkpoint %s and no blob URI in catalog", len(sources.Peers), checkpointID)
	span.RecordError(err)
	span.SetStatus(codes.Error, "all tiers failed")
	return err
}

// blobStoreManifest mirrors the manifest format from
// internal/blobstore.Manifest. Defined here to keep the agent
// → blobstore dep one-way (HTTP only).
type blobStoreManifest struct {
	Files []struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
	} `json:"files"`
}

// fetchFromBlobStore downloads a complete checkpoint from the
// cluster nvsnap-blobstore. Two-step: GET manifest, then
// parallel-fetch each blob by sha256 into destDir/<path>.
//
// The manifest is the source of truth for path layout — blobs
// are content-addressed so the same blob can back many paths
// (across captures, or even within one capture if files dedupe).
func (a *Agent) fetchFromBlobStore(ctx context.Context, blobBaseURL, checkpointID, destDir string) error {
	manifestURL := fmt.Sprintf("%s/v1/capture/%s/manifest.json",
		blobBaseURL, url.PathEscape(checkpointID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := peerHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("manifest GET: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("manifest GET %d: %s", resp.StatusCode, body)
	}
	var m blobStoreManifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return fmt.Errorf("decode manifest: %w", err)
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}

	type blobTask struct {
		path string
		sha  string
		size int64
	}
	tasks := make(chan blobTask, len(m.Files))
	for _, f := range m.Files {
		tasks <- blobTask{path: f.Path, sha: f.SHA256, size: f.Size}
	}
	close(tasks)

	errCh := make(chan error, peerFetchConcurrency)
	var wg sync.WaitGroup
	for i := 0; i < peerFetchConcurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range tasks {
				if err := a.fetchOneBlob(ctx, blobBaseURL, t.sha, t.size, t.path, destDir); err != nil {
					errCh <- fmt.Errorf("blob %s (path=%s): %w", t.sha, t.path, err)
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
	return nil
}

// fetchOneBlob downloads a single blob by sha256 from the blob
// store and writes it to destDir/<relPath>. Range-chunks files
// >= rangeFetchThreshold for parallelism on large blobs.
func (a *Agent) fetchOneBlob(ctx context.Context, blobBaseURL, sha string, expectedSize int64, relPath, destDir string) error {
	blobURL := fmt.Sprintf("%s/v1/blob/%s", blobBaseURL, sha)
	fileCtx, cancel := context.WithTimeout(ctx, peerFetchTimeoutPerFile)
	defer cancel()
	// Same as the peer path: relPath comes from a manifest the blob store
	// served, not from us.
	dst, err := joinWithinRoot(destDir, filepath.FromSlash(relPath))
	if err != nil {
		return fmt.Errorf("manifest entry %q: %w", relPath, err)
	}
	return downloadToFile(fileCtx, peerHTTPClient, []string{blobURL}, expectedSize, dst)
}

// catalogSources mirrors the JSON returned by nvsnap-server's
// GET /api/v1/checkpoints/{id}/sources. Field names are tag-bound so
// future server-side renames are caught by the JSON decoder.
type catalogSources struct {
	CheckpointID string     `json:"checkpoint_id"`
	Peers        []peerInfo `json:"peers"`
	BlobURI      string     `json:"blob_uri"`
}

func (a *Agent) fetchCheckpointSources(ctx context.Context, checkpointID string) (*catalogSources, error) {
	u := fmt.Sprintf("%s/api/v1/checkpoints/%s/sources", a.config.CatalogURL, url.PathEscape(checkpointID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := peerHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("catalog returned %d: %s", resp.StatusCode, body)
	}
	var s catalogSources
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, fmt.Errorf("decode sources: %w", err)
	}
	return &s, nil
}

// peerManifest mirrors the agent peer-server's /manifest response.
type peerManifest struct {
	CheckpointID string `json:"checkpoint_id"`
	TotalSize    int64  `json:"total_size"`
	FileCount    int    `json:"file_count"`
	Files        []struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	} `json:"files"`
}

// fetchFromPeer downloads a complete checkpoint from one peer agent.
// Failure modes:
//
//   - Peer returns non-200 on /manifest: peer doesn't have it (perhaps
//     evicted between catalog query and our request). Caller tries next.
//   - Any per-file download fails: caller tries next peer, partial
//     dest dir is removed first to avoid corrupting future attempts.
//   - Size mismatch on a downloaded file: bytes truncated mid-stream;
//     count as a peer failure.
func (a *Agent) fetchFromPeer(ctx context.Context, peerURL string, alternateURLs []string, checkpointID, destDir string) error {
	manifestURL := fmt.Sprintf("%s/v1/checkpoints/%s/manifest", peerURL, url.PathEscape(checkpointID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := peerHTTPClient.Do(req)
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

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}

	type fetchTask struct {
		path string
		size int64
	}
	tasks := make(chan fetchTask, len(m.Files))
	for _, f := range m.Files {
		tasks <- fetchTask{path: f.Path, size: f.Size}
	}
	close(tasks)

	// Worker pool of peerFetchConcurrency. First worker error cancels
	// the rest via the firstErr channel.
	errCh := make(chan error, peerFetchConcurrency)
	var wg sync.WaitGroup
	for i := 0; i < peerFetchConcurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range tasks {
				if err := a.fetchOneFile(ctx, peerURL, alternateURLs, checkpointID, t.path, t.size, destDir); err != nil {
					errCh <- fmt.Errorf("file %q: %w", t.path, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	var firstErr error
	for e := range errCh {
		if firstErr == nil {
			firstErr = e
		}
	}
	if firstErr != nil {
		return firstErr
	}
	return nil
}

func (a *Agent) fetchOneFile(ctx context.Context, peerURL string, alternateURLs []string, checkpointID, relPath string, expectedSize int64, destDir string) error {
	// Build the source-URL list: primary first, then any alternates.
	// downloadToFile uses urls[0] for small files; large files chunk
	// round-robin across all of them (Phase 3). If an alt peer doesn't
	// actually have this file the chunk 404s and the file fails — the
	// caller then wipes destDir and retries from the next primary, same
	// as the Phase 2 path.
	urls := make([]string, 0, 1+len(alternateURLs))
	urls = append(urls, fmt.Sprintf("%s/v1/checkpoints/%s/file?path=%s",
		peerURL, url.PathEscape(checkpointID), url.QueryEscape(relPath)))
	for _, alt := range alternateURLs {
		if alt == "" || alt == peerURL {
			continue
		}
		urls = append(urls, fmt.Sprintf("%s/v1/checkpoints/%s/file?path=%s",
			alt, url.PathEscape(checkpointID), url.QueryEscape(relPath)))
	}
	fileCtx, cancel := context.WithTimeout(ctx, peerFetchTimeoutPerFile)
	defer cancel()
	// relPath is an entry in the manifest the peer served, so it decides
	// where we write. Confine it to destDir.
	dst, err := joinWithinRoot(destDir, relPath)
	if err != nil {
		return fmt.Errorf("manifest entry %q: %w", relPath, err)
	}
	return downloadToFile(fileCtx, peerHTTPClient, urls, expectedSize, dst)
}

// registerAsPeer tells the catalog this agent now has a local copy.
// Best-effort: failures don't fail the restore — the catalog will
// catch up on the next health-sweep, and same-node restores work
// regardless of catalog freshness.
func (a *Agent) registerAsPeer(ctx context.Context, checkpointID string) error {
	if a.config.CatalogURL == "" || a.config.NodeName == "" {
		return errors.New("CatalogURL or NodeName empty")
	}
	body := struct {
		NodeName string `json:"node_name"`
		AgentURL string `json:"agent_url"`
	}{
		NodeName: a.config.NodeName,
		AgentURL: a.selfAgentURL(),
	}
	if body.AgentURL == "" {
		return errors.New("self agent URL not derivable (NodeIP empty?)")
	}
	buf, _ := json.Marshal(body)
	u := fmt.Sprintf("%s/api/v1/checkpoints/%s/peer-add", a.config.CatalogURL, url.PathEscape(checkpointID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytesReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := peerHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("peer-add returned %d: %s", resp.StatusCode, b)
	}
	return nil
}

// selfAgentURL returns the URL other agents should use to reach this
// agent's peer-server endpoints. Empty string if we don't have
// enough config to construct it (NodeIP missing).
func (a *Agent) selfAgentURL() string {
	// AdvertiseIP first: under pod networking peers must dial the pod IP,
	// since the node IP only resolves to us via hostPort (GH #490). Falls
	// back to NodeIP so a deployment that sets neither, or only the older
	// value, keeps working.
	ip := a.config.AdvertiseIP
	if ip == "" {
		ip = a.config.NodeIP
	}
	if ip == "" {
		return ""
	}
	port := "8081"
	if addr := a.config.ListenAddr; len(addr) > 1 && addr[0] == ':' {
		port = addr[1:]
	}
	return fmt.Sprintf("http://%s:%s", ip, port)
}

// bytesReader returns an io.Reader for a byte slice. Tiny helper to
// avoid pulling bytes.NewReader into the package-level imports.
func bytesReader(b []byte) io.Reader {
	return &byteSliceReader{b: b}
}

type byteSliceReader struct {
	b []byte
	i int
}

func (r *byteSliceReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}
