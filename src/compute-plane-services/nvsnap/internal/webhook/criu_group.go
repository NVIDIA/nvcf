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
	"fmt"
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
	// CRIUGroupCompanionAnnotation marks a placeholder that restores a
	// companion of the instance: a pod without GPUs that its GPU ranks
	// were connected to (an etcd, a NATS), captured and restored with them.
	CRIUGroupCompanionAnnotation = "nvsnap.io/criu-group-companion"
)

// CRIUGroup is a complete group capture: the checkpoint of each rank, in
// rank order, and the node it was taken on.
type CRIUGroup struct {
	Key         string
	Checkpoints []string
	Nodes       []string
	GPUShare    bool
	// Companion marks the members without GPUs (by slot), captured with
	// the GPU ranks, which hold slots 0 to Ranks()-1.
	Companion []bool
	// Containers names the container each member's checkpoint was taken
	// from (by slot); empty in records from before it was recorded.
	Containers []string
	// Bundles names the node bundle version each member's pod mounted (by
	// slot); its placeholder mounts the same. Empty for none recorded.
	Bundles []string
	// VerifyNamespace, when set, is the only namespace that may restore
	// from the capture: the instance stopped for it, whose restore
	// verifies it. Empty once a restore has succeeded.
	VerifyNamespace string
	// Slots are the members a companion's own configuration names, when
	// the record was found through a companion's alias: one, or several
	// for identical companions (replicas of one template), any of which a
	// pod of that configuration can restore. Empty otherwise.
	Slots []int
}

// Ranks is the number of GPU ranks of the group.
func (g CRIUGroup) Ranks() int {
	n := 0
	for i := range g.Checkpoints {
		if i >= len(g.Companion) || !g.Companion[i] {
			n++
		}
	}
	return n
}

// CompanionSlots returns the companion slots the record names for the
// looked-up pod; none when it is no companion of the record.
func (g CRIUGroup) CompanionSlots() []int {
	if len(g.Slots) == 0 {
		return nil
	}
	out := make([]int, 0, len(g.Slots))
	for _, s := range g.Slots {
		if s < 0 || s >= len(g.Checkpoints) || s >= len(g.Companion) || !g.Companion[s] {
			return nil
		}
		out = append(out, s)
	}
	return out
}

// criuGroupRestorePatches admits pod as its member's restore placeholder
// when a group capture of its configuration exists, else returns nil: a
// GPU rank by its ordinal in its instance, a companion by the slot its own
// configuration names.
func (m *Mutator) criuGroupRestorePatches(ctx context.Context, pod *corev1.Pod) []PatchOp {
	if m.CRIUGroups == nil || pod.Annotations[CRIUGroupAnnotation] != "" {
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
	slots := g.CompanionSlots()
	companion := len(slots) > 0
	if gpuRequest(pod) == 0 && !companion {
		return nil
	}
	size := len(g.Checkpoints)
	var ordinal int
	if companion {
		if m.CRIUCompanionAdmits != nil {
			if ok, why := m.CRIUCompanionAdmits(ctx, pod); !ok {
				log.WithField("reason", why).Info("CRIU group restore: the companion cannot restore with its instance; cold start")
				return nil
			}
		}
	} else {
		if name := NamesOwnInstance(pod); name != "" && !m.dynamoRestorable(pod) {
			log.WithField("env", name).Info("CRIU group restore: the pod names its own instance in its configuration; it starts from the caches")
			return nil
		}
		ranks := m.groupSize(ctx, pod)
		ordinal = podOrdinal(pod)
		if ranks != g.Ranks() || ordinal >= ranks {
			log.WithFields(logrus.Fields{"ordinal": ordinal, "size": ranks, "captured": g.Ranks()}).
				Info("CRIU group restore: the capture is of a different instance size; cold start")
			return nil
		}
		// Only an instance of several ranks waits for all of them; one
		// pod restores alone whatever its controller.
		if ranks > 1 && m.CRIUGroupAdmits != nil {
			if ok, why := m.CRIUGroupAdmits(ctx, pod); !ok {
				log.WithField("reason", why).Info("CRIU group restore: the instance cannot restore as a group; cold start")
				return nil
			}
		}
	}
	if m.CRIURestoreBlocked != nil {
		for _, id := range g.Checkpoints {
			if m.CRIURestoreBlocked(ctx, id) {
				log.WithField("checkpoint", id).Warn("CRIU group restore: a checkpoint of this group failed to restore before; cold start")
				return nil
			}
		}
	}
	if companion {
		// Last, so a pod refused above holds no slot.
		var err error
		if ordinal, err = m.companionSlot(ctx, pod, g, slots); err != nil {
			log.WithError(err).Info("CRIU group restore: no slot of the capture for the companion; cold start")
			return nil
		}
	}
	man := checkpointstore.Manifest{SourcePodMeta: map[string]string{}}
	if g.GPUShare && !companion {
		man.SourcePodMeta["gpushare"] = "true"
	}
	if ordinal < len(g.Nodes) && g.Nodes[ordinal] != "" {
		man.CapturedOnNodes = []string{g.Nodes[ordinal]}
	}
	if ordinal < len(g.Containers) && g.Containers[ordinal] != "" {
		man.SourcePodMeta[restoreContainerMeta] = g.Containers[ordinal]
	}
	if ordinal < len(g.Bundles) && g.Bundles[ordinal] != "" {
		man.SourcePodMeta[BundleMeta] = g.Bundles[ordinal]
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
	if companion {
		cp = append(cp, PatchOp{Op: "add", Path: "/metadata/annotations/" + jsonPointerEscape(CRIUGroupCompanionAnnotation), Value: "true"})
		log.WithFields(logrus.Fields{"checkpoint": id, "slot": ordinal, "size": size}).Info("CRIU group restore: pod admitted as its instance's companion placeholder")
		return mergePatchPlan(withPIDReserve(pod, cp))
	}
	// The restored processes keep the files they had open on the model
	// and cache volumes, so the placeholder gets the same mounts the
	// captured pod had. The placeholder's volumes come first: the
	// checkpoint's chunk store replaces the pod's own empty one.
	vp, err := m.modelVolumePatches(ctx, pod)
	if err != nil {
		log.WithError(err).Warn("CRIU group restore: model volume decision failed; cold start")
		return nil
	}
	if vp == nil {
		// A pod without a model volume got the cache directory at capture.
		vp = m.cacheDirCapturePatches(pod)
	}
	log.WithFields(logrus.Fields{"checkpoint": id, "ordinal": ordinal, "size": size}).Info("CRIU group restore: pod admitted as its rank's restore placeholder")
	return mergePatchPlan(withPIDReserve(pod, append(cp, vp...)))
}

// companionSlot picks the slot a companion placeholder restores: its
// configuration's only one, else one of the identical companions' slots
// that no other placeholder of the instance holds (CRIUCompanionSlot).
func (m *Mutator) companionSlot(ctx context.Context, pod *corev1.Pod, g CRIUGroup, slots []int) (int, error) {
	if len(slots) == 1 {
		return slots[0], nil
	}
	if m.CRIUCompanionSlot == nil {
		return 0, fmt.Errorf("%d identical companions and no slot claims", len(slots))
	}
	return m.CRIUCompanionSlot(ctx, pod, g, slots)
}
