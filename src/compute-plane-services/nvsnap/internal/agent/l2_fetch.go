// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// A CRIU checkpoint captured on another node reaches this one from its L2
// volume when the promote finished: the published read-only claim is
// mounted here through a mount holder and the tree is copied into the
// local checkpoint directory. Without a ready L2 copy the caller falls
// through to the peers.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/tracing"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/treecopy"
)

// errL2Unavailable reports that the checkpoint has no ready L2 copy, or
// that L2 is not configured on this agent: not a failure, the caller
// fetches from peers instead.
var errL2Unavailable = errors.New("no ready L2 copy")

// l2CatalogTimeout bounds each catalog request of an L2 fetch.
const l2CatalogTimeout = 15 * time.Second

// l2FetchLocks serializes the fetches of one hash on this node: they share
// one holder pod, which the first to finish would delete under the other.
var l2FetchLocks sync.Map // hash -> *sync.Mutex

// l2Attach mounts claim ns/name on this node and returns the agent-visible
// path of the mount plus its release. A variable so tests can stand in for
// the holder pod, which cannot run there.
var l2Attach = attachL2Claim

// l2CatalogRow is the subset of GET /api/v1/checkpoints/{id} an L2 fetch
// reads. The server answers from a capture node's metadata.json when that
// node is up (podNamespace) and from the catalog row otherwise
// (namespace); both carry the hash the promote was keyed on.
type l2CatalogRow struct {
	Hash         string `json:"hash"`
	Namespace    string `json:"namespace"`
	PodNamespace string `json:"podNamespace"`
}

// l2PromoteState mirrors GET /api/v1/checkpoints/by-hash/{hash}/pvc-state.
type l2PromoteState struct {
	State   string `json:"state"`
	PVCName string `json:"pvc_name"`
}

// fetchFromL2 copies checkpoint checkpointID from its L2 volume into localDir
// (an existing directory, possibly bind-mounted by a pod: write files INTO it,
// never remove/rename localDir or any directory under it).
// Returns errL2Unavailable (wrapped) when the checkpoint has no ready L2 copy
// or L2 is not configured, so the caller falls through to peers.
func (a *Agent) fetchFromL2(ctx context.Context, checkpointID, localDir string) error {
	backend, ok := a.l2Backend.(*checkpointstore.PerCapturePVCBackend)
	if !ok || backend == nil || backend.KubeClient == nil {
		return fmt.Errorf("L2 fetch %s: L2 backend not configured: %w", checkpointID, errL2Unavailable)
	}
	if a.config.CatalogURL == "" {
		return fmt.Errorf("L2 fetch %s: no catalog URL: %w", checkpointID, errL2Unavailable)
	}
	if a.config.NodeName == "" {
		return fmt.Errorf("L2 fetch %s: node name unknown: %w", checkpointID, errL2Unavailable)
	}

	ctx, span := tracing.Tracer().Start(ctx, "l2.fetch")
	defer span.End()
	span.SetAttributes(attribute.String("nvsnap.checkpoint_id", checkpointID))
	fail := func(err error) error {
		if !errors.Is(err, errL2Unavailable) {
			span.RecordError(err)
			span.SetStatus(codes.Error, "l2 fetch failed")
		}
		return err
	}

	row, err := a.l2CatalogRow(ctx, checkpointID)
	if err != nil {
		return fail(fmt.Errorf("L2 fetch %s: %w", checkpointID, err))
	}
	short := checkpointstore.ShortHash(row.Hash)
	span.SetAttributes(attribute.String("nvsnap.hash", short))
	st, err := a.l2PromoteState(ctx, row.Hash)
	if err != nil {
		return fail(fmt.Errorf("L2 fetch %s: %w", checkpointID, err))
	}
	if st.State != "ready" {
		return fmt.Errorf("L2 fetch %s: promote state %q: %w", checkpointID, st.State, errL2Unavailable)
	}

	// The claim the promote published, in the capture's namespace (the
	// backend's targetNamespace: the pod's, else its own), while that
	// namespace lives. A function's namespace goes when it is undeployed,
	// and its redeploy is the restore that needs L2 most: then the claim
	// is minted in the L2 namespace, which nvsnap owns.
	ns := row.Namespace
	if ns == "" {
		ns = row.PodNamespace
	}
	claim := st.PVCName
	if claim == "" {
		claim = "rox-" + short
	}
	if ns == "" || !claimBound(ctx, backend.KubeClient, ns, claim) {
		ns, claim = backend.Namespace, "rox-"+short
	}

	unlock := lockL2Fetch(row.Hash)
	defer unlock()

	// EnsureClaim is a lookup when the claim exists and re-mints a
	// missing one where the strategy can; per-pod storage has no claim to
	// share. The claim is not ours to delete either way: the promote
	// created it, the promoter's Delete reclaims it with the checkpoint,
	// and restore pods in that namespace mount it too.
	if backend.Promoter != nil {
		if err := backend.Promoter.EnsureClaim(ctx, row.Hash, ns); err != nil {
			if errors.Is(err, checkpointstore.ErrNotFound) || errors.Is(err, checkpointstore.ErrUnsupported) {
				return fmt.Errorf("L2 fetch %s: claim %s/%s: %v: %w", checkpointID, ns, claim, err, errL2Unavailable)
			}
			return fail(fmt.Errorf("L2 fetch %s: ensure claim %s/%s: %w", checkpointID, ns, claim, err))
		}
	}

	log := a.log.WithFields(logrus.Fields{
		"checkpoint": checkpointID, "hash": short, "namespace": ns, "claim": claim, "node": a.config.NodeName,
	})
	src, release, err := l2Attach(ctx, backend, log, ns, claim, l2HolderName(row.Hash, a.config.NodeName), a.config.NodeName)
	if err != nil {
		return fail(fmt.Errorf("L2 fetch %s: %w", checkpointID, err))
	}
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), checkpointstore.MountHolderDeleteTimeout+5*time.Second)
		defer cancel()
		if rerr := release(rctx); rerr != nil {
			log.WithError(rerr).Warn("L2 fetch: holder delete failed; it goes with its claim")
		}
	}()

	bytes, err := copyL2Tree(ctx, src, localDir, log)
	span.SetAttributes(attribute.Int64("nvsnap.bytes_copied", bytes))
	if err != nil {
		return fail(fmt.Errorf("L2 fetch %s: %w", checkpointID, err))
	}
	return nil
}

// claimBound reports whether claim ns/name exists and is bound.
func claimBound(ctx context.Context, kc kubernetes.Interface, ns, name string) bool {
	if kc == nil {
		return false
	}
	c, err := kc.CoreV1().PersistentVolumeClaims(ns).Get(ctx, name, metav1.GetOptions{})
	return err == nil && c.DeletionTimestamp == nil && c.Status.Phase == corev1.ClaimBound
}

// l2CatalogRow reads the checkpoint's hash and capture namespace.
func (a *Agent) l2CatalogRow(ctx context.Context, checkpointID string) (l2CatalogRow, error) {
	var row l2CatalogRow
	u := fmt.Sprintf("%s/api/v1/checkpoints/%s", a.config.CatalogURL, url.PathEscape(checkpointID))
	found, err := getCatalogJSON(ctx, u, &row)
	if err != nil {
		return row, fmt.Errorf("catalog checkpoint: %w", err)
	}
	if !found {
		return row, fmt.Errorf("checkpoint not in catalog: %w", errL2Unavailable)
	}
	if row.Hash == "" {
		return row, fmt.Errorf("catalog row has no hash: %w", errL2Unavailable)
	}
	return row, nil
}

// l2PromoteState reads the hash's promote state, which the backend writes
// on every catalog row of the hash (metadata.json does not carry it).
func (a *Agent) l2PromoteState(ctx context.Context, hash string) (l2PromoteState, error) {
	var st l2PromoteState
	u := fmt.Sprintf("%s/api/v1/checkpoints/by-hash/%s/pvc-state", a.config.CatalogURL, url.PathEscape(hash))
	found, err := getCatalogJSON(ctx, u, &st)
	if err != nil {
		return st, fmt.Errorf("catalog promote state: %w", err)
	}
	if !found {
		return st, fmt.Errorf("no catalog row for hash %s: %w", checkpointstore.ShortHash(hash), errL2Unavailable)
	}
	return st, nil
}

// getCatalogJSON decodes a catalog GET into out; found is false on 404.
func getCatalogJSON(ctx context.Context, u string, out any) (found bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, l2CatalogTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return false, err
	}
	resp, err := peerHTTPClient.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return false, fmt.Errorf("GET %s returned %d: %s", u, resp.StatusCode, body)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return false, fmt.Errorf("decode %s: %w", u, err)
	}
	return true, nil
}

func lockL2Fetch(hash string) func() {
	m, _ := l2FetchLocks.LoadOrStore(hash, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// l2HolderName is unique per (hash, node) and at most 54 characters:
// "nvsnap-l2f-" + 32-char short hash + "-" + 10 hex of the node name.
func l2HolderName(hash, node string) string {
	sum := sha256.Sum256([]byte(node))
	return "nvsnap-l2f-" + checkpointstore.ShortHash(hash) + "-" + hex.EncodeToString(sum[:])[:10]
}

// attachL2Claim mounts claim ns/name read-only on node through a mount
// holder and returns the agent-visible mount path. A holder of the same
// name left by an earlier agent is removed first: under the per-hash lock
// nothing else on this node uses it.
func attachL2Claim(ctx context.Context, backend *checkpointstore.PerCapturePVCBackend, log *logrus.Entry, ns, claim, name, node string) (string, func(context.Context) error, error) {
	kc := backend.KubeClient
	pvc, err := kc.CoreV1().PersistentVolumeClaims(ns).Get(ctx, claim, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil, fmt.Errorf("claim %s/%s: %w", ns, claim, errL2Unavailable)
		}
		return "", nil, fmt.Errorf("get claim %s/%s: %w", ns, claim, err)
	}
	h := checkpointstore.NewMountHolder(kc, log, ns, name, node, claim, pvc.UID,
		backend.WriterImage, backend.HostRoot, backend.WriterPullSecrets).ReadOnly()
	if err := deleteStaleHolder(ctx, kc, h, ns, name); err != nil {
		return "", nil, err
	}
	release := h.Delete
	if err := h.Create(ctx); err != nil {
		return "", nil, fmt.Errorf("create L2 fetch holder: %w", err)
	}
	if err := h.WaitRunning(ctx); err != nil {
		releaseQuietly(ctx, release, log)
		return "", nil, fmt.Errorf("L2 fetch holder not running: %w", err)
	}
	p, err := h.PVMountPath()
	if err != nil {
		releaseQuietly(ctx, release, log)
		return "", nil, err
	}
	return p, release, nil
}

func deleteStaleHolder(ctx context.Context, kc kubernetes.Interface, h *checkpointstore.MountHolder, ns, name string) error {
	if _, err := kc.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{}); apierrors.IsNotFound(err) {
		return nil
	}
	if err := h.Delete(ctx); err != nil {
		return fmt.Errorf("remove stale L2 fetch holder %s/%s: %w", ns, name, err)
	}
	return nil
}

func releaseQuietly(ctx context.Context, release func(context.Context) error, log *logrus.Entry) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), checkpointstore.MountHolderDeleteTimeout+5*time.Second)
	defer cancel()
	if err := release(rctx); err != nil {
		log.WithError(err).Warn("L2 fetch: holder delete failed; it goes with its claim")
	}
}

// copyL2Tree copies the mounted L2 tree src into localDir. Existing
// directories are filled in place (treecopy creates with MkdirAll and
// writes files with O_TRUNC), so bind mounts under localDir stay put.
// treecopy streams whole files with sendfile on a worker pool
// (NVSNAP_TREECOPY_WORKERS), which suits a tree of large chunk files.
func copyL2Tree(ctx context.Context, src, localDir string, log *logrus.Entry) (int64, error) {
	start := time.Now()
	log.WithFields(logrus.Fields{"src": src, "dst": localDir}).Info("L2 fetch: copy starting")
	// lost+found is the volume's filesystem, not the checkpoint.
	tc := treecopy.NewCopier([]string{"/lost+found"}, log)
	bytes, files, err := tc.Copy(ctx, src, localDir)
	elapsed := time.Since(start)
	if err != nil {
		return bytes, fmt.Errorf("copy %s -> %s (%d bytes in %.1fs): %w", src, localDir, bytes, elapsed.Seconds(), err)
	}
	log.WithFields(logrus.Fields{
		"bytes":   bytes,
		"files":   files,
		"elapsed": fmt.Sprintf("%.1fs", elapsed.Seconds()),
		"gbps":    fmt.Sprintf("%.2f", float64(bytes)/1e9/max(elapsed.Seconds(), 1e-3)),
	}).Info("L2 fetch: copy complete")
	return bytes, nil
}
