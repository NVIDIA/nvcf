// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

// CRIU restore of a multi-pod instance (a Helm chart's StatefulSet or
// LeaderWorkerSet). The agent checkpoints a Ready instance as one group
// (internal/agent/criu_group.go) and records the checkpoints by the
// instance's configuration, one per rank. A later instance of the same
// configuration and size is admitted pod by pod: each becomes the restore
// placeholder for its rank's checkpoint, and the agent restores the group
// together once every placeholder runs.

import (
	"context"
	"strconv"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/election"
)

const (
	// CRIUGroupAnnotation names the group record a placeholder restores
	// from; CRIUGroupOrdinalAnnotation and CRIUGroupSizeAnnotation place it
	// in the instance.
	CRIUGroupAnnotation        = "nvsnap.io/criu-group"
	CRIUGroupOrdinalAnnotation = "nvsnap.io/criu-group-ordinal"
	CRIUGroupSizeAnnotation    = "nvsnap.io/criu-group-size"
	// CRIURestoredAnnotation marks a placeholder the agent restored into, so
	// an agent that restarts does not restore into it again.
	CRIURestoredAnnotation = "nvsnap.io/criu-restored"
)

// CRIUGroup is a complete group capture: the checkpoint of each rank, in
// rank order, and the node it was taken on.
type CRIUGroup struct {
	Key         string
	Checkpoints []string
	Nodes       []string
	GPUShare    bool
}

// criuGroupRestorePatches admits pod as its rank's restore placeholder when
// a group capture of its configuration and size exists, else returns nil.
func (m *Mutator) criuGroupRestorePatches(ctx context.Context, pod *corev1.Pod) []PatchOp {
	if m.CRIUGroups == nil || gpuRequest(pod) == 0 || pod.Annotations[CRIUGroupAnnotation] != "" {
		return nil
	}
	uri, ok := m.cacheURI(pod)
	if !ok {
		return nil
	}
	g, ok := m.CRIUGroups(ctx, pod.Namespace, uri)
	if !ok {
		return nil
	}
	key := g.Key
	log := m.logger().WithFields(logrus.Fields{"pod": election.PodIdentity(pod), "criuGroup": key})
	ordinal, size := podOrdinal(pod), m.groupSize(ctx, pod)
	if size != len(g.Checkpoints) || ordinal >= size {
		log.WithFields(logrus.Fields{"ordinal": ordinal, "size": size, "captured": len(g.Checkpoints)}).
			Info("CRIU group restore: the capture is of a different instance size; cold start")
		return nil
	}
	if m.CRIURestoreBlocked != nil {
		for _, id := range g.Checkpoints {
			if m.CRIURestoreBlocked(ctx, id) {
				log.WithField("checkpoint", id).Warn("CRIU group restore: a checkpoint of this group failed to restore before; cold start")
				return nil
			}
		}
	}
	man := checkpointstore.Manifest{SourcePodMeta: map[string]string{}}
	if g.GPUShare {
		man.SourcePodMeta["gpushare"] = "true"
	}
	if ordinal < len(g.Nodes) && g.Nodes[ordinal] != "" {
		man.CapturedOnNodes = []string{g.Nodes[ordinal]}
	}
	id := g.Checkpoints[ordinal]
	cp, err := m.criuRestorePatches(pod, id, man)
	if err != nil {
		log.WithError(err).Warn("CRIU group restore: cannot prepare the pod; cold start")
		return nil
	}
	cp = append(cp,
		PatchOp{Op: "add", Path: "/metadata/annotations/" + jsonPointerEscape(CRIUGroupAnnotation), Value: key},
		PatchOp{Op: "add", Path: "/metadata/annotations/" + jsonPointerEscape(CRIUGroupOrdinalAnnotation), Value: strconv.Itoa(ordinal)},
		PatchOp{Op: "add", Path: "/metadata/annotations/" + jsonPointerEscape(CRIUGroupSizeAnnotation), Value: strconv.Itoa(size)},
	)
	// The restored processes keep the files they had open on the model
	// and cache volumes, so the placeholder gets the same mounts the
	// captured pod had. The placeholder's volumes come first: the
	// checkpoint's chunk store replaces the pod's own empty one.
	vp, err := m.modelVolumePatches(ctx, pod)
	if err != nil {
		log.WithError(err).Warn("CRIU group restore: model volume decision failed; cold start")
		return nil
	}
	log.WithFields(logrus.Fields{"checkpoint": id, "ordinal": ordinal, "size": size}).Info("CRIU group restore: pod admitted as its rank's restore placeholder")
	return mergePatchPlan(append(cp, vp...))
}
