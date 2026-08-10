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

package server

import (
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/db"
)

// 64-hex content hash for a capture manifest CM.
const testCaptureHash = "99ed58534170f9d331868605dec1f55a61fdcb59e9c6f4a5c1061054136244cb"

func newCaptureCM(hash string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nvsnap-capture-" + hash[:24],
			Namespace:         "nvsnap-system",
			CreationTimestamp: metav1.NewTime(time.Now()),
			Labels:            map[string]string{checkpointstore.CMLabelKind: checkpointstore.CMLabelKindCapture},
			Annotations:       map[string]string{checkpointstore.CMAnnotationHash: hash},
		},
		Data: map[string]string{checkpointstore.CMDataKey: `{"file_count":10,"total_size_bytes":123}`},
	}
}

func newReconciler(t *testing.T) *Reconciler {
	t.Helper()
	catalog, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	return &Reconciler{Catalog: catalog, Namespace: "nvsnap-system", Log: logrus.New()}
}

// ingestCM must ingest a fresh capture exactly once, then no-op on repeat —
// otherwise the 30s reconcile loop re-Upserts forever ("ingested=1" each tick).
func TestIngestCM_IngestsOnceThenIdempotent(t *testing.T) {
	r := newReconciler(t)
	cm := newCaptureCM(testCaptureHash)

	if !r.ingestCM(cm) {
		t.Fatal("first ingestCM should ingest (return true)")
	}
	if r.ingestCM(cm) {
		t.Fatal("second ingestCM should be a no-op (return false) — the hash is now cataloged")
	}
	rows, err := r.Catalog.ListByHash(testCaptureHash)
	if err != nil {
		t.Fatalf("ListByHash: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 row for the hash, got %d (reconciler churned a duplicate)", len(rows))
	}
}

// A row keyed by pod-uid in `id` with the hash in the `hash` column (the
// NVCA / replicate register path) must be recognized by ingestCM — otherwise
// it re-Upserts a second hash-keyed row every tick and resurrects deletes.
func TestIngestCM_RecognizesPodUIDKeyedRow(t *testing.T) {
	r := newReconciler(t)
	if err := r.Catalog.UpsertCheckpoint(&db.Checkpoint{
		ID:     "pod-uid-1234",
		Hash:   testCaptureHash,
		Status: "Completed",
	}); err != nil {
		t.Fatalf("seed pod-uid-keyed row: %v", err)
	}

	if r.ingestCM(newCaptureCM(testCaptureHash)) {
		t.Fatal("ingestCM should NOT ingest — a pod-uid-keyed row already carries this hash")
	}
	rows, err := r.Catalog.ListByHash(testCaptureHash)
	if err != nil {
		t.Fatalf("ListByHash: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row (no duplicate), got %d", len(rows))
	}
}
