// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/election"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/rootfsonly"
)

// electionPatches is the admission decision for a model workload that
// carries no explicit restore-from (docs/proposals/helm-chart-cache-election.md):
//
//   - a promoted cachedir capture exists for the composed hash: restore.
//   - otherwise elect. The leader gets the capture decoration and the
//     capture label so the watcher picks it up; followers get the restore
//     decoration against the claim the promote will bind, plus a
//     scheduling gate nvsnap-server removes on ready.
//
// Returns (nil, nil) when the pod is not a model workload, the election is
// off, or the pod is better left to the existing paths. Every returned
// patch set starts with the hash stamp, so the watcher captures under the
// same hash the followers were decorated against.
func (m *Mutator) electionPatches(ctx context.Context, pod *corev1.Pod) ([]PatchOp, error) {
	if m.Elector == nil || m.Composer == nil || m.CacheDir == "" || m.L2Backend == nil {
		return nil, nil
	}
	modelID, ok := rootfsonly.IsModelWorkload(pod, m.MainContainer)
	if !ok {
		return nil, nil
	}
	hash := checkpointstore.ComputeHash(m.Composer.Compose(pod, m.MainContainer))
	log := m.logger().WithFields(logrus.Fields{
		"pod":   election.PodIdentity(pod),
		"hash":  checkpointstore.ShortHash(hash),
		"model": modelID,
	})

	mp := newMetaPatcher(pod)
	manifest, statErr := m.Backend.Stat(ctx, hash)
	switch {
	case statErr == nil:
		if manifest.CaptureMethod != "cachedir" {
			log.WithField("method", manifest.CaptureMethod).Info("election: existing capture is not cachedir; leaving pod to the explicit paths")
			return nil, nil
		}
		patches, err := m.tryL2CacheDir(ctx, pod, hash, manifest)
		if err == nil && patches != nil {
			log.Info("election: promoted capture exists; restoring")
			return append(mp.stamp(hash, election.RoleRestore), patches...), nil
		}
		if err != nil && !errors.Is(err, checkpointstore.ErrNotFound) {
			return nil, fmt.Errorf("restore from existing capture: %w", err)
		}
		// A manifest without a bound claim: promote pending or failed. A
		// leader elected now would skip the capture (hash exists) and never
		// promote, leaving followers gated until the deadline. Stay out.
		log.Info("election: capture exists but its volume is not bound; admitting unchanged")
		return nil, nil
	case !errors.Is(statErr, checkpointstore.ErrNotFound):
		return nil, fmt.Errorf("backend stat: %w", statErr)
	}

	role, err := m.Elector.Elect(ctx, hash, pod)
	if err != nil {
		return nil, err
	}
	switch role {
	case election.RoleLeader:
		patches := mp.stamp(hash, election.RoleLeader)
		patches = append(patches, mp.label(CaptureLabel, "true")...)
		patches = append(patches, m.cacheDirCapturePatchesFor(pod, true)...)
		log.Info("election: leader; capture decoration applied")
		return patches, nil
	case election.RoleFollower:
		pending, ok := m.L2Backend.(checkpointstore.PendingMounter)
		if !ok {
			log.Info("election: follower but the L2 backend cannot name the claim ahead of promote; cold start")
			return nil, nil
		}
		pm, ok := pending.PendingMountSpec(hash, cacheDirVolumeMeta(m.CacheDir, pod.Namespace))
		if !ok {
			log.Info("election: follower but storage is per-pod clone; cold start")
			return nil, nil
		}
		patches := mp.stamp(hash, election.RoleFollower)
		patches = append(patches, mp.label(election.GatedLabel, "true")...)
		restore, err := m.cacheDirRestorePatches(pod, pm.Volume, m.cacheEnvVars(m.CacheDir))
		if err != nil {
			return nil, err
		}
		if restore == nil {
			log.Info("election: follower already carries the cache mount; admitting unchanged")
			return nil, nil
		}
		patches = append(patches, restore...)
		patches = append(patches, gatePatch(pod))
		if m.L2WaitImage != "" {
			// The restore decoration created /spec/initContainers if it was
			// missing, so an insert at index 0 is valid here.
			waitC := buildL2WaitContainer(m.L2WaitImage, m.NvSnapServerURL, hash, m.L2WaitTimeout)
			patches = append(patches, PatchOp{Op: "add", Path: "/spec/initContainers/0", Value: waitC})
		}
		log.WithField("claim", pm.Volume.PersistentVolumeClaim.ClaimName).Info("election: follower; gated until the capture is promoted")
		return patches, nil
	default:
		return nil, fmt.Errorf("election returned unknown role %q", role)
	}
}

// metaPatcher sets labels and annotations on the admitted pod, creating
// each map at most once. A second "add" of the map path would replace
// the map and drop the keys set before it.
type metaPatcher struct {
	pod          *corev1.Pod
	bootstrapped map[string]bool
}

func newMetaPatcher(pod *corev1.Pod) *metaPatcher {
	return &metaPatcher{pod: pod, bootstrapped: map[string]bool{}}
}

func (mp *metaPatcher) set(mapPath string, existing map[string]string, key, value string) []PatchOp {
	var patches []PatchOp
	if existing == nil && !mp.bootstrapped[mapPath] {
		mp.bootstrapped[mapPath] = true
		patches = append(patches, PatchOp{Op: "add", Path: mapPath, Value: map[string]string{}})
	}
	return append(patches, PatchOp{Op: "add", Path: mapPath + "/" + strings.ReplaceAll(key, "/", "~1"), Value: value})
}

func (mp *metaPatcher) label(key, value string) []PatchOp {
	return mp.set("/metadata/labels", mp.pod.Labels, key, value)
}

func (mp *metaPatcher) annotation(key, value string) []PatchOp {
	return mp.set("/metadata/annotations", mp.pod.Annotations, key, value)
}

// stamp records the composed hash (annotation full, label short) and the
// role on the pod.
func (mp *metaPatcher) stamp(hash string, role election.Role) []PatchOp {
	patches := mp.annotation(election.HashAnnotation, hash)
	patches = append(patches, mp.annotation(election.RoleAnnotation, string(role))...)
	patches = append(patches, mp.label(election.HashLabel, checkpointstore.ShortHash(hash))...)
	return patches
}

// gatePatch adds the wait-for-cache scheduling gate. Gates may only be
// removed after creation, so this is the one moment to add it.
func gatePatch(pod *corev1.Pod) PatchOp {
	gate := corev1.PodSchedulingGate{Name: election.GateName}
	if pod.Spec.SchedulingGates == nil {
		return PatchOp{Op: "add", Path: "/spec/schedulingGates", Value: []corev1.PodSchedulingGate{gate}}
	}
	return PatchOp{Op: "add", Path: "/spec/schedulingGates/-", Value: gate}
}
