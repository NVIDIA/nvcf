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

// Integration tests for the deleteCheckpoint hash-keyed cascade
// (nvsnap#137). This is the acceptance bar that should have caught
// yesterday's premature close of #137: assert that after a single
// DELETE call, EVERY related resource is gone — catalog rows
// sharing the hash, L2 PVCs, VolumeSnapshot, promote Lease,
// GPUCheckpoint CRDs, blobstore captures.
//
// If a regression strips a cleanup step (refactor, accidental
// short-circuit, missed RBAC), one of these assertions fails
// before the change reaches main.

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	coordv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/db"
)

// newCascadeTestServer is a richer fixture than newTestServerWithCatalog:
// it pre-loads the fake K8s clients with PVCs / VolumeSnapshots /
// GPUCheckpoint CRDs / Leases so the cascade has something concrete
// to delete and the assertions can verify their absence afterwards.
func newCascadeTestServer(
	t *testing.T,
	hash string,
	namespace string,
	rowIDs []string,
	blobstoreURL string,
) *Server {
	t.Helper()

	short := checkpointstore.ShortHash(hash) // canonical — never assume length

	// Pre-seed K8s with PVCs, snapshot, lease the cascade should kill.
	rox := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "rox-" + short, Namespace: namespace},
	}
	rwx := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "rwx-" + short, Namespace: namespace},
	}
	lease := &coordv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: "nvsnap-promote-" + short, Namespace: namespace},
	}
	// Capture manifest CM — ALWAYS in captureManifestNamespace (nvsnap-system),
	// NOT the source pod's namespace. This is the Reconciler's source of
	// truth; the cascade must delete it to stop row resurrection.
	captureCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:        checkpointstore.CMNameFor(hash),
			Namespace:   captureManifestNamespace,
			Labels:      map[string]string{checkpointstore.CMLabelKind: checkpointstore.CMLabelKindCapture},
			Annotations: map[string]string{checkpointstore.CMAnnotationHash: hash},
		},
	}
	kubeClient := kubefake.NewSimpleClientset(rox, rwx, lease, captureCM)

	// Dynamic client carries the VolumeSnapshot + GPUCheckpoint CRDs.
	scheme := runtime.NewScheme()
	gvrToListKind := map[schema.GroupVersionResource]string{
		volumeSnapshotGVR: "VolumeSnapshotList",
		checkpointGVR:     "GPUCheckpointList",
	}
	snap := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "snapshot.storage.k8s.io/v1",
		"kind":       "VolumeSnapshot",
		"metadata":   map[string]any{"name": "snap-" + short, "namespace": namespace},
	}}
	objs := []runtime.Object{snap}
	for _, id := range rowIDs {
		crd := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "nvsnap.io/v1alpha1",
			"kind":       "GPUCheckpoint",
			"metadata":   map[string]any{"name": id, "namespace": namespace},
		}}
		objs = append(objs, crd)
	}
	dynClient := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, objs...)

	// Catalog with all rowIDs sharing the same hash + namespace.
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open in-mem catalog: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	for i, id := range rowIDs {
		if err := d.UpsertCheckpoint(&db.Checkpoint{
			ID:             id,
			CheckpointID:   id,
			Hash:           hash,
			Namespace:      namespace,
			PodName:        "test-pod",
			NodeName:       "node-1",
			Status:         "Completed",
			CheckpointPath: "/var/lib/nvsnap/checkpoints/" + short + "__capture-" + string(rune('a'+i)),
			CreatedAt:      time.Now().UTC().Add(-time.Duration(i) * time.Hour),
		}); err != nil {
			t.Fatalf("seed row %s: %v", id, err)
		}
	}

	s := newTestServer()
	s.kubeClient = kubeClient
	s.dynClient = dynClient
	s.catalog = d
	s.config.BlobstoreURL = blobstoreURL
	s.setupRoutes()
	return s
}

// TestDeleteCascade_SingleRow — the acceptance test for #137, now
// under the #106 hash-uniqueness invariant: the catalog enforces ONE
// row per content hash, so the #137 sibling-survival bug is
// structurally prevented (no siblings can exist). A single DELETE
// must remove the one catalog row AND all the per-hash L2 + K8s
// artifacts AND fire a blobstore DELETE for the row's agentID.
func TestDeleteCascade_SingleRow(t *testing.T) {
	hash := "deadbeefcafe0123deadbeefcafe0123deadbeefcafe0123deadbeefcafe0123"
	ns := "nvcf-backend"

	fb, fbSrv := newFakeBlobstore()
	defer fbSrv.Close()

	s := newCascadeTestServer(t, hash, ns,
		[]string{"row-a"}, fbSrv.URL)

	// Call deleteCheckpoint on the single converged row.
	res := s.cascadeDeleteCheckpoint(context.Background(), "row-a",
		short(hash)+"__capture-a",
		mustGetRow(t, s, "row-a"))

	if !res.AnySuccess {
		t.Fatalf("AnySuccess=false; result=%+v", res)
	}

	// Catalog: the single row must be gone. #106's hash uniqueness
	// means there can be no surviving sibling — the #137 regression
	// is structurally impossible now.
	if got, _ := s.catalog.GetCheckpoint("row-a"); got != nil {
		t.Errorf("row-a still present in catalog after delete")
	}
	if res.CatalogRows != 1 {
		t.Errorf("CatalogRows=%d, want 1 (single converged row deleted)", res.CatalogRows)
	}

	// L2 PVCs gone.
	for _, name := range []string{"rox-" + short(hash), "rwx-" + short(hash)} {
		_, err := s.kubeClient.CoreV1().PersistentVolumeClaims(ns).
			Get(context.Background(), name, metav1.GetOptions{})
		if !apierrors.IsNotFound(err) {
			t.Errorf("PVC %s/%s should be NotFound after cascade; got err=%v", ns, name, err)
		}
	}
	if res.L2PVCs != 2 {
		t.Errorf("L2PVCs=%d, want 2", res.L2PVCs)
	}

	// VolumeSnapshot gone.
	_, err := s.dynClient.Resource(volumeSnapshotGVR).Namespace(ns).
		Get(context.Background(), "snap-"+short(hash), metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("snap-%s should be NotFound; got err=%v", short(hash), err)
	}
	if res.L2Snapshots != 1 {
		t.Errorf("L2Snapshots=%d, want 1", res.L2Snapshots)
	}

	// Promote Lease gone.
	_, err = s.kubeClient.CoordinationV1().Leases(ns).
		Get(context.Background(), "nvsnap-promote-"+short(hash), metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("lease nvsnap-promote-%s should be NotFound; got err=%v", short(hash), err)
	}
	if res.L2Leases != 1 {
		t.Errorf("L2Leases=%d, want 1", res.L2Leases)
	}

	// GPUCheckpoint CRD (one per row) gone.
	_, gerr := s.dynClient.Resource(checkpointGVR).Namespace(ns).
		Get(context.Background(), "row-a", metav1.GetOptions{})
	if !apierrors.IsNotFound(gerr) {
		t.Errorf("GPUCheckpoint CRD %s/row-a should be NotFound; got err=%v", ns, gerr)
	}
	if res.CRDsDeleted != 1 {
		t.Errorf("CRDsDeleted=%d, want 1", res.CRDsDeleted)
	}

	// Capture manifest CM gone (in nvsnap-system, not ns) — without this
	// the Reconciler resurrects the deleted rows on its next tick.
	_, err = s.kubeClient.CoreV1().ConfigMaps(captureManifestNamespace).
		Get(context.Background(), checkpointstore.CMNameFor(hash), metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("capture manifest CM %s should be NotFound after cascade; got err=%v",
			checkpointstore.CMNameFor(hash), err)
	}
	if res.CaptureCMs != 1 {
		t.Errorf("CaptureCMs=%d, want 1", res.CaptureCMs)
	}

	// Blobstore must have received a DELETE for the row's agentID
	// (CAS dedup means blobs only get GC'd when refcount drops to
	// zero — that requires the capture to be deleted).
	gotDeletes := fb.deletes()
	if len(gotDeletes) != 1 {
		t.Errorf("blobstore DELETEs=%d, want 1 (single row); got=%v", len(gotDeletes), gotDeletes)
	}
	if res.Blobstores != 1 {
		t.Errorf("Blobstores=%d, want 1", res.Blobstores)
	}
}

// TestDeleteCascade_ByHashEndpoint — the new /by-hash/{hash} DELETE
// alias does the same cascade without requiring a row id.
func TestDeleteCascade_ByHashEndpoint(t *testing.T) {
	hash := "feedfacecafe0123feedfacecafe0123feedfacecafe0123feedfacecafe0123"
	ns := "nvcf-backend"

	fb, fbSrv := newFakeBlobstore()
	defer fbSrv.Close()

	s := newCascadeTestServer(t, hash, ns, []string{"r1"}, fbSrv.URL)

	req := httptest.NewRequest("DELETE", "/api/v1/checkpoints/by-hash/"+hash, http.NoBody)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	if rec.Code != 204 {
		t.Fatalf("status=%d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	// The single converged row is gone (#106 hash uniqueness).
	if got, _ := s.catalog.GetCheckpoint("r1"); got != nil {
		t.Errorf("row r1 still in catalog after by-hash delete")
	}

	// One blobstore DELETE (single row).
	if len(fb.deletes()) != 1 {
		t.Errorf("blobstore DELETEs=%d, want 1; got=%v", len(fb.deletes()), fb.deletes())
	}
}

// TestDeleteCascade_ByHashNotFound — 404 when no rows match.
func TestDeleteCascade_ByHashNotFound(t *testing.T) {
	s := newTestServerWithCatalog(t)
	req := httptest.NewRequest("DELETE", "/api/v1/checkpoints/by-hash/0000000000000000000000000000000000000000000000000000000000000000", http.NoBody)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Errorf("status=%d, want 404 for unknown hash", rec.Code)
	}
}

// TestDeleteCascade_NotFoundTolerant — re-running cascade after
// resources are already gone should NOT generate errors (operator
// re-issues DELETE during cleanup; cascade must be idempotent).
func TestDeleteCascade_NotFoundTolerant(t *testing.T) {
	hash := "abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234"
	ns := "nvcf-backend"
	s := newCascadeTestServer(t, hash, ns, []string{"row-x"}, "")

	// First call should succeed.
	row := mustGetRow(t, s, "row-x")
	res1 := s.cascadeDeleteCheckpoint(context.Background(), "row-x",
		short(hash)+"__capture-a", row)
	if len(res1.Errors) > 0 {
		t.Errorf("first cascade had errors: %v", res1.Errors)
	}

	// Second call: catalog already empty (DeleteByHash ran), PVCs
	// gone, snapshot gone, lease gone, CRD gone. All NotFound-tolerant
	// — must report no errors.
	res2 := s.cascadeDeleteCheckpoint(context.Background(), "row-x",
		short(hash)+"__capture-a", nil) // row is nil since catalog is empty now
	if len(res2.Errors) > 0 {
		t.Errorf("idempotent second cascade had errors: %v", res2.Errors)
	}
}

// short delegates to checkpointstore.ShortHash so test assertions
// stay in sync with the production naming scheme — never assume a
// fixed length here.
func short(hash string) string {
	return checkpointstore.ShortHash(hash)
}

// mustGetRow fetches a catalog row or fails the test.
func mustGetRow(t *testing.T, s *Server, id string) *db.Checkpoint {
	t.Helper()
	row, err := s.catalog.GetCheckpoint(id)
	if err != nil {
		t.Fatalf("GetCheckpoint(%s): %v", id, err)
	}
	if row == nil {
		t.Fatalf("row %s not in catalog", id)
	}
	return row
}
