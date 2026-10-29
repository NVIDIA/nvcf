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

// Unit tests for PerCapturePVCBackend (nvsnap#63). End-to-end Put
// requires a real K8s control plane (or a sophisticated controller
// simulator) because it depends on PVC binding + VolumeSnapshot
// readyToUse transitions — those land in the e2e MR (nvsnap#67).
//
// What this file covers:
//   - Stat / Get / Delete: pure CRUD against the fake client
//   - acquireLease + releaseLease: the cross-node serialization
//   - createRWXPVC: spec shape (access mode, storage class, size)
//   - encodeCapturePlan: round-trip of the writer-Job payload
//   - State writes via the CatalogStateWriter contract

package checkpointstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

// stubCopier is the v0.0.51 default Copier for tests — records each
// Copy call and returns 0/0/nil unless an error is wired in. Lets
// Put-driven tests reach completion without an actual filesystem.
type stubCopier struct {
	mu    sync.Mutex
	calls []stubCopierCall
	err   error
	bytes int64
	files int64
}

type stubCopierCall struct {
	destRoot string
	sources  []CaptureSource
}

func (s *stubCopier) Copy(_ context.Context, destRoot string, sources []CaptureSource) (bytes, files int64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, stubCopierCall{destRoot: destRoot, sources: sources})
	return s.bytes, s.files, s.err
}

// stubCatalog records every UpdatePVCPromoteState call so tests can
// assert state transitions happened in the right order with the right
// pvcName arguments.
type stubCatalog struct {
	mu    sync.Mutex
	calls []stubCatalogCall
}

type stubCatalogCall struct {
	id      string
	state   string
	pvcName string
}

func (s *stubCatalog) UpdatePVCPromoteState(id, state, pvcName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, stubCatalogCall{id: id, state: state, pvcName: pvcName})
	return nil
}

func (s *stubCatalog) snapshot() []stubCatalogCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]stubCatalogCall, len(s.calls))
	copy(out, s.calls)
	return out
}

func newTestBackend(t *testing.T, opts ...func(*PerCapturePVCBackend)) *PerCapturePVCBackend { //nolint:unparam // opts is a test-helper extension point used by future callers
	t.Helper()
	scheme := runtime.NewScheme()
	b := &PerCapturePVCBackend{
		KubeClient:    kubefake.NewSimpleClientset(),
		DynClient:     dynamicfake.NewSimpleDynamicClient(scheme),
		Catalog:       &stubCatalog{},
		Namespace:     "nvsnap-system",
		StorageClass:  "hyperdisk-ml",
		SnapshotClass: "csi-gce-pd-snapshot-class",
		WriterImage:   "nvsnap-agent:test",
		HolderID:      "test-holder",
		NodeName:      "test-node",   // v0.0.51 — mount-holder nodeName pin
		HostRoot:      "/host",       // v0.0.51 — agent's host-mount root
		Copier:        &stubCopier{}, // v0.0.51 — in-process copier
		// Snug timing knobs for tests so polls don't block.
		LeaseDuration:   30 * time.Second,
		PollInterval:    10 * time.Millisecond,
		PollTimeout:     200 * time.Millisecond,
		JobTimeout:      200 * time.Millisecond,
		SnapshotTimeout: 200 * time.Millisecond,
		Log:             logrus.NewEntry(logrus.New()).WithField("test", t.Name()),
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

func TestPerCapturePVCBackend_Stat_NotFoundWhenMissing(t *testing.T) {
	b := newTestBackend(t)
	_, err := b.Stat(context.Background(), "deadbeef")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Stat on missing PVC: got %v, want ErrNotFound", err)
	}
}

// Under WaitForFirstConsumer storage classes (Hyperdisk-ML on GKE),
// the rox PVC is Pending until the first restore-target pod mounts it.
// Stat must report the manifest as available the moment the PVC exists
// — otherwise restore is locked out of an already-promoted checkpoint.
// Same semantics as Mount (see nvsnap#147) and snapshotAndClone which
// also no longer waits for Bound.
func TestPerCapturePVCBackend_Stat_ReturnsManifestWhenPending(t *testing.T) {
	b := newTestBackend(t)
	hash := "aaaaaaaaaaaaaaaa" + "bbbbbbbbbbbbbbbb" // 32 chars
	roxName := "rox-" + ShortHash(hash)
	_, err := b.KubeClient.CoreV1().PersistentVolumeClaims(b.Namespace).Create(context.Background(),
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: roxName, Namespace: b.Namespace},
			Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending},
		}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("seed PVC: %v", err)
	}
	got, err := b.Stat(context.Background(), hash)
	if err != nil {
		t.Fatalf("Stat on Pending PVC: %v (WaitForFirstConsumer regression — see nvsnap#147 + the snapshotAndClone Bound-wait deadlock)", err)
	}
	if got.Hash != hash {
		t.Errorf("manifest hash = %q, want %q", got.Hash, hash)
	}
}

func TestPerCapturePVCBackend_Stat_ReturnsManifestWhenBound(t *testing.T) {
	b := newTestBackend(t)
	hash := "deadbeefcafef00d" + "1111111111111111" // 32 chars; ShortHash truncates to 16 hex
	roxName := "rox-" + ShortHash(hash)
	_, err := b.KubeClient.CoreV1().PersistentVolumeClaims(b.Namespace).Create(context.Background(),
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: roxName, Namespace: b.Namespace},
			Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
		}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("seed PVC: %v", err)
	}
	got, err := b.Stat(context.Background(), hash)
	if err != nil {
		t.Fatalf("Stat on Bound PVC: %v", err)
	}
	if got.Hash != hash {
		t.Errorf("manifest hash = %q, want %q", got.Hash, hash)
	}
}

// StatInNamespace scopes the rox-<hash> lookup to a caller-supplied
// namespace (the cross-cluster replicate path passes the source pod's
// namespace, where the rox PVC actually lands — not b.Namespace). A rox
// in namespace X must be invisible to a Stat scoped to namespace Y, and
// present when scoped to X.
func TestPerCapturePVCBackend_StatInNamespace_ScopesToNamespace(t *testing.T) {
	b := newTestBackend(t)
	hash := "cccccccccccccccc" + "dddddddddddddddd"
	roxName := "rox-" + ShortHash(hash)
	const workloadNS = "nvcf-backend"
	_, err := b.KubeClient.CoreV1().PersistentVolumeClaims(workloadNS).Create(context.Background(),
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: roxName, Namespace: workloadNS},
			Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending},
		}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("seed PVC: %v", err)
	}

	// Present in the workload namespace.
	if got, err := b.StatInNamespace(context.Background(), hash, workloadNS); err != nil {
		t.Fatalf("StatInNamespace(%s): %v", workloadNS, err)
	} else if got.Hash != hash {
		t.Errorf("hash = %q, want %q", got.Hash, hash)
	}

	// Invisible in b.Namespace (nvsnap-system) — this is exactly the
	// cross-cluster replicate bug: a b.Namespace-scoped Stat would
	// never see the replicated rox and mis-report "not committed"...
	// but more importantly the chain-Stat L1 success was masking a
	// missing L2. Here we assert the namespace scoping is honored.
	if _, err := b.StatInNamespace(context.Background(), hash, b.Namespace); !errors.Is(err, ErrNotFound) {
		t.Errorf("StatInNamespace(%s): got %v, want ErrNotFound (rox is in %s only)", b.Namespace, err, workloadNS)
	}
}

// TestPerCapturePVCBackend_Mount_MissingPVCReturnsNotFound — the genuine
// "no L2 promote happened for this hash" case. Webhook must fall
// through to L1 hostPath.
func TestPerCapturePVCBackend_Mount_MissingPVCReturnsNotFound(t *testing.T) {
	b := newTestBackend(t)
	hash := "aaaaaaaaaaaaaaaa" + "bbbbbbbbbbbbbbbb"
	_, err := b.Mount(context.Background(), hash, VolumeMeta{
		Name:      "nvsnap-checkpoint",
		MountPath: "/nvsnap-checkpoint",
	})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Mount on missing PVC: got %v, want ErrNotFound", err)
	}
}

// TestPerCapturePVCBackend_Mount_PendingPVCReturnsPodMount — the
// critical nvsnap#147 change. Under WaitForFirstConsumer the rox PVC
// is always Pending until a pod consumes it; the OLD behavior of
// returning ErrNotFound here created a deadlock. The new behavior is
// to return the PodMount and let the webhook inject the volume — the
// nvsnap-l2-wait init container handles the "snap+clone not finished
// yet" race separately, and kubelet's WFC binder runs on pod
// scheduling.
func TestPerCapturePVCBackend_Mount_PendingPVCReturnsPodMount(t *testing.T) {
	b := newTestBackend(t)
	hash := "1111111111111111" + "2222222222222222"
	roxName := "rox-" + ShortHash(hash)
	_, err := b.KubeClient.CoreV1().PersistentVolumeClaims(b.Namespace).Create(context.Background(),
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: roxName, Namespace: b.Namespace},
			Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending},
		}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("seed PVC: %v", err)
	}
	got, err := b.Mount(context.Background(), hash, VolumeMeta{
		Name:      "nvsnap-checkpoint",
		MountPath: "/nvsnap-checkpoint",
		Namespace: b.Namespace,
	})
	if err != nil {
		t.Fatalf("Mount on Pending PVC: %v (Bound-gate regression?)", err)
	}
	if got.Volume.PersistentVolumeClaim == nil || got.Volume.PersistentVolumeClaim.ClaimName != roxName {
		t.Errorf("Volume.PVC.ClaimName = %v, want %q", got.Volume.PersistentVolumeClaim, roxName)
	}
	if !got.Volume.PersistentVolumeClaim.ReadOnly || !got.VolumeMount.ReadOnly {
		t.Errorf("rox mount should be ReadOnly on both VolumeSource and VolumeMount; got vol.RO=%v mount.RO=%v",
			got.Volume.PersistentVolumeClaim.ReadOnly, got.VolumeMount.ReadOnly)
	}
}

// TestPerCapturePVCBackend_Mount_BoundPVCReturnsPodMount — sanity
// check that the always-inject behavior still works for the easy
// case (PVC already Bound). Same expected shape as the Pending path.
func TestPerCapturePVCBackend_Mount_BoundPVCReturnsPodMount(t *testing.T) {
	b := newTestBackend(t)
	hash := "3333333333333333" + "4444444444444444"
	roxName := "rox-" + ShortHash(hash)
	_, err := b.KubeClient.CoreV1().PersistentVolumeClaims(b.Namespace).Create(context.Background(),
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: roxName, Namespace: b.Namespace},
			Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
		}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("seed PVC: %v", err)
	}
	got, err := b.Mount(context.Background(), hash, VolumeMeta{
		Name:      "nvsnap-checkpoint",
		MountPath: "/nvsnap-checkpoint",
		Namespace: b.Namespace,
	})
	if err != nil {
		t.Fatalf("Mount on Bound PVC: %v", err)
	}
	if got.Volume.PersistentVolumeClaim.ClaimName != roxName {
		t.Errorf("ClaimName = %q, want %q", got.Volume.PersistentVolumeClaim.ClaimName, roxName)
	}
}

func TestPerCapturePVCBackend_Delete_Idempotent(t *testing.T) {
	b := newTestBackend(t)
	// No resources exist. Delete should succeed silently.
	if err := b.Delete(context.Background(), "deadbeef"); err != nil {
		t.Errorf("Delete on empty cluster: %v", err)
	}
}

func TestPerCapturePVCBackend_Delete_RemovesAllArtifacts(t *testing.T) {
	b := newTestBackend(t)
	hash := "abc123def456" + "1234567890abcdef"
	short := ShortHash(hash)

	// Seed both PVCs.
	ctx := context.Background()
	for _, name := range []string{"rwx-" + short, "rox-" + short} {
		_, err := b.KubeClient.CoreV1().PersistentVolumeClaims(b.Namespace).Create(ctx,
			&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: b.Namespace}},
			metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	if err := b.Delete(ctx, hash); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, name := range []string{"rwx-" + short, "rox-" + short} {
		_, err := b.KubeClient.CoreV1().PersistentVolumeClaims(b.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			t.Errorf("%s still exists after Delete", name)
		}
	}
}

func TestPerCapturePVCBackend_AcquireLease_FreshCreate(t *testing.T) {
	b := newTestBackend(t)
	ok, holder, err := b.acquireLease(context.Background(), b.Namespace, "nvsnap-promote-test")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if !ok {
		t.Fatalf("first acquire should succeed")
	}
	if holder != "test-holder" {
		t.Errorf("holder = %q, want test-holder", holder)
	}
	// Confirm the Lease object exists.
	l, err := b.KubeClient.CoordinationV1().Leases(b.Namespace).Get(context.Background(), "nvsnap-promote-test", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get lease: %v", err)
	}
	if l.Spec.HolderIdentity == nil || *l.Spec.HolderIdentity != "test-holder" {
		t.Errorf("lease holder = %v, want test-holder", l.Spec.HolderIdentity)
	}
}

func TestPerCapturePVCBackend_AcquireLease_SecondCallerLoses(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()
	// First caller wins.
	ok, _, _ := b.acquireLease(ctx, b.Namespace, "nvsnap-promote-test")
	if !ok {
		t.Fatalf("first acquire should succeed")
	}
	// Second caller (different holder) sees an active lease.
	b.HolderID = "other-holder"
	ok2, _, err := b.acquireLease(ctx, b.Namespace, "nvsnap-promote-test")
	if err != nil {
		t.Fatalf("acquire #2: %v", err)
	}
	if ok2 {
		t.Errorf("second acquire should have lost; got ok=true")
	}
}

func TestPerCapturePVCBackend_ReleaseLease_OnlyOwnerCanDelete(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()
	// Acquire as test-holder.
	b.HolderID = "test-holder"
	_, _, _ = b.acquireLease(ctx, b.Namespace, "nvsnap-promote-test")
	// A different "holder" attempts to release — should be a no-op
	// (lease object stays).
	if err := b.releaseLease(ctx, b.Namespace, "nvsnap-promote-test", "imposter"); err != nil {
		t.Fatalf("release as imposter: %v", err)
	}
	_, err := b.KubeClient.CoordinationV1().Leases(b.Namespace).Get(ctx, "nvsnap-promote-test", metav1.GetOptions{})
	if err != nil {
		t.Errorf("lease was deleted by non-owner: %v", err)
	}
	// True owner releases successfully.
	if err := b.releaseLease(ctx, b.Namespace, "nvsnap-promote-test", "test-holder"); err != nil {
		t.Fatalf("release as owner: %v", err)
	}
}

func TestPerCapturePVCBackend_CreateRWXPVC_Spec(t *testing.T) {
	b := newTestBackend(t)
	pvc, err := b.createRWXPVC(context.Background(), b.Namespace, "rwx-abc", 96*1024*1024*1024)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if pvc == nil {
		t.Fatal("expected returned PVC, got nil")
	}
	got, err := b.KubeClient.CoreV1().PersistentVolumeClaims(b.Namespace).Get(context.Background(), "rwx-abc", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// nvsnap#81: writer PVC is ReadWriteOnce. Only one writer Job pod
	// ever mounts it (lease-serialized one-per-hash), so RWO is
	// sufficient AND lets us use block-storage CSIs like pd.csi (the
	// earlier RWX requirement excluded GKE Hyperdisk ML).
	if len(got.Spec.AccessModes) != 1 || got.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		t.Errorf("access modes = %v, want [ReadWriteOnce] (nvsnap#81)", got.Spec.AccessModes)
	}
	if got.Spec.StorageClassName == nil || *got.Spec.StorageClassName != "hyperdisk-ml" {
		t.Errorf("storage class = %v, want hyperdisk-ml", got.Spec.StorageClassName)
	}
	if got.Labels["nvsnap.io/role"] != "writer" {
		t.Errorf("role label = %q, want writer", got.Labels["nvsnap.io/role"])
	}
	if got.Labels["nvsnap.io/per-capture"] != "true" {
		t.Errorf("per-capture label missing")
	}
}

func TestPerCapturePVCBackend_CreateRWXPVC_Idempotent(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()
	if _, err := b.createRWXPVC(ctx, b.Namespace, "rwx-abc", 96*1024*1024*1024); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Second call must succeed AND return the existing PVC so callers
	// can use its UID for OwnerReference on retried Puts.
	pvc, err := b.createRWXPVC(ctx, b.Namespace, "rwx-abc", 96*1024*1024*1024)
	if err != nil {
		t.Errorf("second create should be idempotent, got %v", err)
	}
	if pvc == nil {
		t.Errorf("idempotent re-create must return the existing PVC, not nil")
	}
}

func TestPerCapturePVCBackend_Put_RequiresStorageClass(t *testing.T) {
	b := newTestBackend(t)
	b.StorageClass = ""
	_, err := b.Put(context.Background(), "hash", nil, Manifest{})
	if err == nil || !contains(err.Error(), "StorageClass") {
		t.Errorf("expected StorageClass-required error, got %v", err)
	}
}

func TestPerCapturePVCBackend_Put_RequiresHash(t *testing.T) {
	b := newTestBackend(t)
	_, err := b.Put(context.Background(), "", nil, Manifest{})
	if err == nil || !contains(err.Error(), "hash is required") {
		t.Errorf("expected hash-required error, got %v", err)
	}
}

func TestPerCapturePVCBackend_Put_Idempotent_OnExistingRoxPVC(t *testing.T) {
	b := newTestBackend(t)
	stub := b.Catalog.(*stubCatalog)
	ctx := context.Background()
	hash := "abc123def4567890" + "1234567890abcdef"

	// Seed an already-Bound rox PVC — simulates a prior Put completing
	// but the caller not seeing the catalog row yet (e.g., agent crash
	// between rox-Bound and setState(ready) being observed).
	roxName := "rox-" + ShortHash(hash)
	_, err := b.KubeClient.CoreV1().PersistentVolumeClaims(b.Namespace).Create(ctx,
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: roxName, Namespace: b.Namespace},
			Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
		}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := b.Put(ctx, hash, nil, Manifest{})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got.Hash != hash {
		t.Errorf("manifest hash = %q, want %q", got.Hash, hash)
	}
	// Should have written exactly two state rows: pending (initial)
	// + ready (idempotent short-circuit). No writing/snapshotting in
	// between because we didn't actually run them.
	calls := stub.snapshot()
	if len(calls) < 2 {
		t.Fatalf("expected at least 2 state writes (pending+ready), got %d: %+v", len(calls), calls)
	}
	last := calls[len(calls)-1]
	if last.state != pvcStateReady || last.pvcName != roxName {
		t.Errorf("final state = (%s, %s), want (ready, %s)", last.state, last.pvcName, roxName)
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// targetNamespace must prefer the source pod's namespace over the
// backend default. K8s PVCs are namespace-scoped and the consuming
// pod can only mount same-namespace PVCs, so L2 must create artifacts
// where the source pod lives. nvsnap#82 regression.
func TestPerCapturePVCBackend_TargetNamespace_PrefersSourcePodMeta(t *testing.T) {
	b := newTestBackend(t)
	m := Manifest{SourcePodMeta: map[string]string{"namespace": "nvcf-backend"}}
	if got := b.targetNamespace(m); got != "nvcf-backend" {
		t.Errorf("targetNamespace = %q; want nvcf-backend (from SourcePodMeta)", got)
	}
}

func TestPerCapturePVCBackend_TargetNamespace_FallsBackToBackendDefault(t *testing.T) {
	b := newTestBackend(t)
	m := Manifest{} // no SourcePodMeta
	if got := b.targetNamespace(m); got != "nvsnap-system" {
		t.Errorf("targetNamespace = %q; want nvsnap-system (b.Namespace fallback)", got)
	}
}

// Empty SourcePodMeta["namespace"] should also fall back — distinguishes
// "key present with empty value" (treated as missing) vs the fallback.
func TestPerCapturePVCBackend_TargetNamespace_EmptySourceMetaFalsBack(t *testing.T) {
	b := newTestBackend(t)
	m := Manifest{SourcePodMeta: map[string]string{"namespace": ""}}
	if got := b.targetNamespace(m); got != "nvsnap-system" {
		t.Errorf("targetNamespace = %q; want nvsnap-system (empty source ns falls back)", got)
	}
}

// End-to-end via Put: capture in nvcf-backend lands all artifacts in
// nvcf-backend, not nvsnap-system. Stops the namespace-mismatch bug
// from re-appearing.
func TestPerCapturePVCBackend_Put_CreatesArtifactsInSourcePodNamespace(t *testing.T) {
	b := newTestBackend(t)
	hash := "a4f7818605da321ee9c3cb80bb5e6fe7289bac9736d153e04e67e2e3f4a7407b"
	m := Manifest{
		SourcePodMeta: map[string]string{"namespace": "nvcf-backend"},
	}

	// Drive Put through enough state to create the rwx PVC + Lease,
	// then bail on the writer Job (we won't fake an entire Job run).
	// All we need is to observe where the rwx PVC + Lease landed.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = b.Put(ctx, hash, nil, m)

	// rwx PVC must be in nvcf-backend, not nvsnap-system.
	if _, err := b.KubeClient.CoreV1().PersistentVolumeClaims("nvcf-backend").
		Get(context.Background(), "rwx-"+ShortHash(hash), metav1.GetOptions{}); err != nil {
		t.Errorf("rwx PVC should be in nvcf-backend; got: %v", err)
	}
	// And NOT in nvsnap-system.
	if _, err := b.KubeClient.CoreV1().PersistentVolumeClaims("nvsnap-system").
		Get(context.Background(), "rwx-"+ShortHash(hash), metav1.GetOptions{}); err == nil {
		t.Errorf("rwx PVC must NOT be in nvsnap-system (the legacy bug)")
	}
	// (Lease was created+released in nvcf-backend over Put's
	// lifetime — deferred releaseLease deletes it on return. The
	// rwx PVC's location is sufficient evidence that the namespace
	// plumbed through every helper.)
}

// v0.0.51 replaces the writer Job with a mount-holder pod + agent-
// direct copy. Pod-spec compliance is tested in mount_holder_test.go
// (Kyverno fields, OwnerReference, nodeName pin, sleep entrypoint).
// runMountHolderCopy itself is exercised end-to-end via Put-driven
// tests below — the unit-level surface is small enough that mocking
// the K8s API + Copier here would mostly retest mount_holder_test.go.

// stubCatalogNotFound is a CatalogStateWriter that returns the typed
// "no catalog row with this hash" sentinel — the case PerCapturePVCBackend
// must tolerate during rootfs path's Backend.Put when nvsnap-server hasn't
// yet populated the catalog row's hash field.
type stubCatalogNotFound struct {
	calls int
}

func (s *stubCatalogNotFound) UpdatePVCPromoteState(hash, state, pvcName string) error {
	s.calls++
	return fmt.Errorf("simulated 404 wrapping: %w", ErrCatalogHashNotFound)
}

// TestSetState_TolerantOfCatalogHashNotFound covers the v0.0.50 fix.
// When the catalog row doesn't yet have a hash (rootfs path's chicken-
// and-egg), setState must NOT bubble the 404 — the L2 artifact creation
// continues, and nvsnap-server's runRootfsCheckpoint backfills state=ready
// later.
func TestSetState_TolerantOfCatalogHashNotFound(t *testing.T) {
	b := newTestBackend(t)
	b.Catalog = &stubCatalogNotFound{}
	// Internal setState call (mirrors the early-Put writes that fire
	// before nvsnap-server has the hash).
	if err := b.setState("hash-without-row", "pending", ""); err != nil {
		t.Fatalf("setState should tolerate ErrCatalogHashNotFound; got: %v", err)
	}
	// Each state transition is a separate call; verify the writer was
	// actually invoked (we're not just no-opping the path entirely).
	c, _ := b.Catalog.(*stubCatalogNotFound)
	if c.calls != 1 {
		t.Errorf("expected 1 catalog call, got %d", c.calls)
	}
}

// TestSetState_PropagatesRealCatalogErrors — non-404 errors from the
// writer MUST abort the Put. Today the only one we surface specially is
// ErrCatalogHashNotFound; everything else stays a fatal error so an
// operator-visible failure shows up instead of silently dropping state
// writes.
type stubCatalogTransportErr struct{}

func (stubCatalogTransportErr) UpdatePVCPromoteState(_, _, _ string) error {
	return fmt.Errorf("simulated connection reset")
}

func TestSetState_PropagatesNon404Errors(t *testing.T) {
	b := newTestBackend(t)
	b.Catalog = stubCatalogTransportErr{}
	err := b.setState("hash", "pending", "")
	if err == nil {
		t.Fatal("setState should propagate non-404 catalog errors")
	}
	if !strings.Contains(err.Error(), "connection reset") {
		t.Errorf("expected error to mention transport problem; got: %v", err)
	}
}

// flakyCatalog returns ErrCatalogHashNotFound for the first failN calls to
// simulate the real ordering on the cachedir path: the agent promotes before
// nvsnap-server has written the hash onto the catalog row.
type flakyCatalog struct {
	mu      sync.Mutex
	failN   int
	calls   []stubCatalogCall
	hardErr error
}

func (f *flakyCatalog) UpdatePVCPromoteState(id, state, pvcName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, stubCatalogCall{id: id, state: state, pvcName: pvcName})
	if f.hardErr != nil {
		return f.hardErr
	}
	if len(f.calls) <= f.failN {
		return ErrCatalogHashNotFound
	}
	return nil
}

func (f *flakyCatalog) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }
func (f *flakyCatalog) succeeded() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	// The final call is the one that returned nil.
	return len(f.calls) > f.failN
}

// A terminal ready-state write must survive the catalog row not yet carrying
// the hash. Dropping it leaves pvc_promote_state empty forever, which is what
// stalls consumers that gate warm-start on it (nvsnap#469).
func TestSetStateRetriesTerminalWriteUntilHashLands(t *testing.T) {
	prevInterval, prevAttempts := stateRetryInterval, stateRetryAttempts
	stateRetryInterval, stateRetryAttempts = time.Millisecond, 50
	defer func() { stateRetryInterval, stateRetryAttempts = prevInterval, prevAttempts }()

	cat := &flakyCatalog{failN: 3}
	b := &PerCapturePVCBackend{Catalog: cat}

	if err := b.setState("hash", pvcStateReady, "rox-hash"); err != nil {
		t.Fatalf("setState returned %v; the retry must be asynchronous, not surfaced", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !cat.succeeded() {
		time.Sleep(2 * time.Millisecond)
	}
	if !cat.succeeded() {
		t.Fatalf("ready state never written after %d attempts", cat.count())
	}
	last := cat.calls[len(cat.calls)-1]
	if last.state != pvcStateReady || last.pvcName != "rox-hash" {
		t.Errorf("final write = (%s,%s), want (%s,rox-hash)", last.state, last.pvcName, pvcStateReady)
	}
}

// A non-retryable catalog error must stop the loop rather than spin to the cap.
// The error is scripted up front so the branch is reached deterministically,
// rather than racing a sleep against the first retry.
func TestSetStateRetryStopsOnNonRetryableError(t *testing.T) {
	prevInterval, prevAttempts := stateRetryInterval, stateRetryAttempts
	stateRetryInterval, stateRetryAttempts = time.Millisecond, 200
	defer func() { stateRetryInterval, stateRetryAttempts = prevInterval, prevAttempts }()

	// First call: hash-not-found (triggers the retry goroutine).
	// Every call after that: a hard error the retry must not survive.
	cat := &scriptedCatalog{results: []error{ErrCatalogHashNotFound}, rest: errors.New("catalog unreachable")}
	b := &PerCapturePVCBackend{Catalog: cat}
	if err := b.setState("hash", pvcStateReady, "rox-hash"); err != nil {
		t.Fatalf("setState: %v", err)
	}

	// The loop must stop on the hard error: exactly one retry after the initial
	// call, and no growth thereafter.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && cat.count() < 2 {
		time.Sleep(time.Millisecond)
	}
	settled := cat.count()
	time.Sleep(100 * time.Millisecond)
	if got := cat.count(); got != settled {
		t.Errorf("retry continued past a non-retryable error (%d -> %d)", settled, got)
	}
	if settled > 2 {
		t.Errorf("expected to stop at the first hard error, saw %d calls", settled)
	}
}

// scriptedCatalog returns results in order, then rest for every later call.
type scriptedCatalog struct {
	mu      sync.Mutex
	results []error
	rest    error
	n       int
}

func (s *scriptedCatalog) UpdatePVCPromoteState(_, _, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	if s.n <= len(s.results) {
		return s.results[s.n-1]
	}
	return s.rest
}
func (s *scriptedCatalog) count() int { s.mu.Lock(); defer s.mu.Unlock(); return s.n }

// Intermediate states stay best-effort: a later write supersedes them, so
// retrying each one would be noise.
func TestSetStateDoesNotRetryIntermediateStates(t *testing.T) {
	cat := &flakyCatalog{failN: 99}
	b := &PerCapturePVCBackend{Catalog: cat}
	if err := b.setState("hash", pvcStateSnapshotting, ""); err != nil {
		t.Fatalf("setState: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	if got := cat.count(); got != 1 {
		t.Errorf("intermediate state attempted %d times, want 1 (no retry)", got)
	}
}
