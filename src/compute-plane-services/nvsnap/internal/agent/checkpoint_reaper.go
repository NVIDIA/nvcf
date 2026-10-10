// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// A CRIU checkpoint holds its pods' memory: a TB and more per node for a
// large model, on the node and again on L2. Two kinds are never restored
// again and are retired, here and on L2:
//   - a blocked checkpoint (its restore failed);
//   - an instance capture no complete group record names: one rank of a
//     capture that failed, or a capture since replaced. It gets
//     checkpointRetireAge first, past any capture and L2 promote still
//     running.
// A checkpoint a pod on this node is restoring from or restored from is
// kept either way.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

const checkpointRetireAge = 3 * time.Hour

// retirableCheckpoints returns the checkpoints under the agent's
// checkpoint directory to retire, with their metadata.
func (a *Agent) retirableCheckpoints(ctx context.Context, pods []corev1.Pod, now time.Time) (map[string]*CheckpointMetadata, error) {
	cms := a.kubeClient.CoreV1().ConfigMaps(a.criuFailedNamespace())
	blocked := map[string]bool{}
	if cm, err := cms.Get(ctx, criuRestoreFailedConfigMap, metav1.GetOptions{}); err == nil {
		for id := range cm.Data {
			blocked[id] = true
		}
	}
	records, err := cms.List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=nvsnap"})
	if err != nil {
		return nil, err // without the records nothing reads as unreferenced
	}
	named := map[string]bool{}
	for _, r := range records.Items {
		if !strings.HasPrefix(r.Name, criuGroupConfigMapPrefix) || r.Data["state"] != criuGroupStateComplete {
			continue
		}
		for k, v := range r.Data {
			if strings.HasPrefix(k, "checkpoint.") {
				named[v] = true
			}
		}
	}
	inUse := map[string]bool{}
	for _, p := range pods {
		if id := p.Annotations[webhook.CRIURestoreAnnotation]; id != "" && p.DeletionTimestamp == nil {
			inUse[id] = true
		}
	}
	ents, err := os.ReadDir(a.config.CheckpointDir)
	if err != nil {
		return nil, err
	}
	out := map[string]*CheckpointMetadata{}
	for _, e := range ents {
		id := e.Name()
		if !e.IsDir() || strings.HasPrefix(id, ".") || id == GPUSharePodStoresSubdir || inUse[id] {
			continue
		}
		b, err := os.ReadFile(filepath.Join(a.config.CheckpointDir, id, "metadata.json"))
		if err != nil {
			continue // not a checkpoint, or one being written
		}
		var md CheckpointMetadata
		if json.Unmarshal(b, &md) != nil {
			continue
		}
		instance := md.CatalogInfo != nil && md.CatalogInfo.InstanceCapture
		if blocked[id] || (instance && !named[id] && now.Sub(md.CreatedAt) > checkpointRetireAge) {
			out[id] = &md
		}
	}
	return out, nil
}

// reapCheckpoints retires this node's dead checkpoints and their L2 copies.
func (a *Agent) reapCheckpoints(ctx context.Context, pods []corev1.Pod, now time.Time) {
	dead, err := a.retirableCheckpoints(ctx, pods, now)
	if err != nil {
		a.log.WithError(err).Warn("checkpoint reaper: cannot tell the dead checkpoints; keeping all")
		return
	}
	backend, _ := a.l2Backend.(*checkpointstore.PerCapturePVCBackend)
	for id, md := range dead {
		log := a.log.WithField("checkpoint", id)
		// Only an instance capture's L2 copy is its alone: a lone capture's
		// is keyed by its configuration, which a later capture may share.
		instance := md.CatalogInfo != nil && md.CatalogInfo.InstanceCapture
		if instance && backend != nil && backend.Promoter != nil && md.Hash != "" {
			ns := md.PodNamespace
			if ns == "" {
				ns = backend.Namespace
			}
			if err := backend.Promoter.Delete(ctx, md.Hash, ns); err != nil {
				log.WithError(err).Warn("checkpoint reaper: cannot retire the L2 copy; keeping the checkpoint to try again")
				continue
			}
		}
		if err := os.RemoveAll(filepath.Join(a.config.CheckpointDir, id)); err != nil {
			log.WithError(err).Warn("checkpoint reaper: cannot remove the checkpoint")
			continue
		}
		log.Info("checkpoint reaper: retired a checkpoint that is never restored again")
	}
}
