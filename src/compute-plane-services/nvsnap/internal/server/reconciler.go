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
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/db"
)

// Reconciler ingests rootfs-only captures from cluster state into the
// catalog DB. The cluster (ConfigMap registry under nvsnap.io/kind=
// rootfs-capture-manifest) is the source of truth; the DB is a friendly
// index for the UI's Checkpoints list. Captures created outside the
// server's UI flow (the agent's auto-capture watcher, direct API calls)
// land in the cluster — without this loop they'd be invisible in the UI.
//
// The CRIU path doesn't need this because CRIU checkpoints are created
// via the server's own /checkpoint API, which writes the DB row directly.
type Reconciler struct {
	KubeClient kubernetes.Interface
	Catalog    *db.DB
	Namespace  string        // nvsnap-system; where capture CMs live
	Interval   time.Duration // default 30s
	Log        logrus.FieldLogger
}

// Run blocks reconciling on every Interval until ctx is cancelled. Errors
// are logged but never bubble up (one bad CM shouldn't kill the loop).
func (r *Reconciler) Run(ctx context.Context) {
	if r.Interval == 0 {
		r.Interval = 30 * time.Second
	}
	if r.Log == nil {
		r.Log = logrus.NewEntry(logrus.New()).WithField("subsys", "server.reconciler")
	}

	// Run once immediately so the UI is populated on first load, not
	// after Interval seconds.
	r.reconcileOnce(ctx)

	t := time.NewTicker(r.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.reconcileOnce(ctx)
		}
	}
}

// reconcileOnce lists every capture-manifest CM in the namespace,
// decodes the embedded Manifest, and upserts a Checkpoint row.
// Idempotent: existing rows by ID are skipped (CreateCheckpoint returns
// an error for duplicates; we treat it as no-op).
func (r *Reconciler) reconcileOnce(ctx context.Context) {
	selector := fmt.Sprintf("%s=%s", checkpointstore.CMLabelKind, checkpointstore.CMLabelKindCapture)
	cms, err := r.KubeClient.CoreV1().ConfigMaps(r.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		r.Log.WithError(err).Warn("reconciler: list capture ConfigMaps failed")
		return
	}
	ingested := 0
	for i := range cms.Items {
		if r.ingestCM(&cms.Items[i]) {
			ingested++
		}
	}
	if ingested > 0 {
		r.Log.WithField("ingested", ingested).Info("reconciler: ingested rootfs captures into catalog")
	}
}

// ingestCM decodes one capture CM and inserts a Checkpoint row if the
// hash isn't already known. Returns true if a new row was written.
func (r *Reconciler) ingestCM(cm *corev1.ConfigMap) bool {
	hash, ok := cm.Annotations[checkpointstore.CMAnnotationHash]
	if !ok || hash == "" {
		return false
	}
	// Existence check MUST key on the content hash, not the id. A capture's
	// row may be keyed by pod-uid in `id` with the hash in the `hash` column
	// (the NVCA / replicate register path). GetCheckpoint queries WHERE
	// id=?, so it misses that row, and the reconciler would re-Upsert a
	// second hash-keyed row on every tick forever (observed: "ingested=1"
	// each cycle) — and resurrect any hash-keyed row a user just deleted.
	// ListByHash matches on the hash column, so both the NVCA row and the
	// reconciler's own prior row are recognized → ingest once, never churn.
	if rows, err := r.Catalog.ListByHash(hash); err == nil && len(rows) > 0 {
		return false // a row already carries this hash
	}
	raw, ok := cm.Data[checkpointstore.CMDataKey]
	if !ok || raw == "" {
		return false
	}
	var m checkpointstore.Manifest
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		r.Log.WithError(err).WithField("cm", cm.Name).Warn("reconciler: decode manifest failed")
		return false
	}

	// Build the Checkpoint row from the manifest. Field origins:
	//   ID            = full sha256 hex (annotation, also m.Hash)
	//   PodName/NS    = SourcePodMeta["name","namespace"]
	//   Image/Model   = SourcePodMeta["image","model_id"] (when set)
	//   WorkloadType  = SourcePodMeta["engine"] (vllm | sglang | trtllm | nim)
	//   NodeName      = first of CapturedOnNodes (rootfs may capture on
	//                   any GPU node; we record the first reporter)
	//   Size/Status   = m.TotalSizeBytes / always "Completed" (CMs only
	//                   exist for committed captures)
	//   CreatedAt     = m.CapturedAt
	c := &db.Checkpoint{
		ID:             hash,
		CheckpointID:   hash,
		Namespace:      m.SourcePodMeta["namespace"],
		PodName:        m.SourcePodMeta["name"],
		ContainerImage: m.SourcePodMeta["image"],
		ModelName:      m.SourcePodMeta["model_id"],
		WorkloadType:   m.SourcePodMeta["engine"],
		// Content-addressed lookup columns (nvsnap#101). The
		// POST /lookup query keys on image_ref (required) + model_id;
		// without these populated, NVCA Hook A's cross-cluster /
		// cross-fvID dedup never matches a rootfs capture and the pod
		// cold-starts despite the rox being present. These mirror the
		// display fields above but feed the indexed lookup path.
		// engine_flags is intentionally left empty: rootfs manifests
		// don't carry canonical engine args, and an empty stored value
		// matches an empty lookup request (NVCF start-script pods have
		// no engine args to canonicalize).
		ImageRef:       m.SourcePodMeta["image"],
		ModelID:        m.SourcePodMeta["model_id"],
		ContainerName:  m.SourcePodMeta["container"],
		CheckpointSize: m.TotalSizeBytes,
		Status:         "Completed",
		HasGPU:         true,
		Message:        fmt.Sprintf("rootfs-only capture (%d files)", m.FileCount),
		CreatedAt:      m.CapturedAt,
	}
	// GPU count + driver major (agent records these in SourcePodMeta at
	// capture time; older manifests omit them → stay zero).
	if n, err := strconv.Atoi(m.SourcePodMeta["gpu_count"]); err == nil {
		c.GPUCount = n
	}
	if n, err := strconv.Atoi(m.SourcePodMeta["driver_major"]); err == nil {
		c.DriverMajor = n
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = cm.CreationTimestamp.Time
	}
	if len(m.CapturedOnNodes) > 0 {
		c.NodeName = m.CapturedOnNodes[0]
	}

	// Upsert (not Create): the reconciler must reflect the manifest into
	// the row idempotently, including BACKFILLING the content-addressed
	// lookup columns (image_ref/model_id) onto rows that predate this
	// fix. CreateCheckpoint skipped existing rows, so a row written before
	// image_ref was populated stayed unmatchable forever (nvsnap#101).
	if err := r.Catalog.UpsertCheckpoint(c); err != nil {
		r.Log.WithError(err).WithField("hash", checkpointstore.ShortHash(hash)).
			Debug("reconciler: UpsertCheckpoint failed (benign on race)")
		return false
	}
	return true
}
