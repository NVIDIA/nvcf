// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"fmt"
	"regexp"
	"strconv"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelvolume"
)

// Compile-cache volume for Helm functions on block storage (mechanism 5 of
// docs/proposals/helm-shared-model-volume.md). Each pod compiles into its
// local cachedir emptyDir; the first pod of a given engine configuration
// and ordinal that becomes Ready has its cache subtree captured by the
// agent on its node into a sized read-only volume (the model-volume
// machinery with KindCache). Every later pod with the same key gets the
// volume mounted read-only and a seed init copies it into its writable
// cachedir before the engine starts, so torch.compile, Inductor, Triton
// and FlashInfer find their artifacts.
//
// Why per ordinal: in a tensor-parallel group every rank writes rank-
// specific artifacts, and shared paths (autotune results) differ in
// content between ranks (measured 2026-09-29), so caches are never merged.
// A single-pod TP group is ordinal 0 and holds every rank's directory.

// Stamps on a pod whose cachedir the agent captures once it is Ready.
const (
	// CacheCaptureLabel opts the pod into capture.
	CacheCaptureLabel = "nvsnap.io/cache-capture"
	// CacheURIAnnotation carries the cache identity (config hash, ordinal).
	CacheURIAnnotation = "nvsnap.io/cache-uri"
	// CacheVolumeAnnotation names the emptyDir volume holding the cache.
	CacheVolumeAnnotation = "nvsnap.io/cache-volume"
	// CacheSubpathAnnotation is the cache subtree inside that volume.
	CacheSubpathAnnotation = "nvsnap.io/cache-subpath"
)

const (
	cacheSeedVolumeName = "nvsnap-cache-seed"
	cacheSeedInitName   = "nvsnap-seed-cache"
	cacheSeedMount      = "/nvsnap-cache-seed"
	cacheSubdir         = "cache"
	lwsWorkerIndexLabel = "leaderworkerset.sigs.k8s.io/worker-index"
	lwsSizeAnnotation   = "leaderworkerset.sigs.k8s.io/size"
)

var ordinalSuffix = regexp.MustCompile(`-(\d+)$`)

// podOrdinal is the pod's index in its group: the LeaderWorkerSet worker
// index, else a StatefulSet-style numeric name suffix, else 0.
func podOrdinal(pod *corev1.Pod) int {
	if v, ok := pod.Labels[lwsWorkerIndexLabel]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	if m := ordinalSuffix.FindStringSubmatch(pod.Name); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n
		}
	}
	return 0
}

// cacheURI is the cache set identity: the engine configuration hash. All
// ranks of a group share it; each rank lives in its own directory inside.
func (m *Mutator) cacheURI(pod *corev1.Pod) (string, bool) {
	if m.Composer == nil {
		return "", false
	}
	key := checkpointstore.ShortHash(checkpointstore.ComputeHash(m.Composer.Compose(pod, m.MainContainer)))[:16]
	return "cache://" + key, true
}

// groupSize is how many ranks the pod's group has: the LeaderWorkerSet
// size annotation, else the owning StatefulSet's replicas, else 1. A set
// is only collected once every rank is ready, so this must not guess low.
func (m *Mutator) groupSize(ctx context.Context, pod *corev1.Pod) int {
	if v, ok := pod.Annotations[lwsSizeAnnotation]; ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	for _, o := range pod.OwnerReferences {
		if o.Kind != "StatefulSet" || m.CacheVolume == nil || m.CacheVolume.Kube == nil {
			continue
		}
		sts, err := m.CacheVolume.Kube.AppsV1().StatefulSets(pod.Namespace).Get(ctx, o.Name, metav1.GetOptions{})
		if err == nil && sts.Spec.Replicas != nil && *sts.Spec.Replicas > 0 {
			return int(*sts.Spec.Replicas)
		}
		m.logger().WithError(err).WithField("statefulset", pod.Namespace+"/"+o.Name).Warn("cache volume: group size unknown; treating the pod as a group of one")
	}
	return 1
}

// cacheVolumePatches decorates a Block-mode model-volume reader with the
// compile-cache volume: complete, the read-only claim is minted in the
// pod's namespace and a seed init fills the cachedir from it; otherwise
// the pod is marked for capture. patches are the admission's patches so
// far; the result extends them.
func (m *Mutator) cacheVolumePatches(ctx context.Context, pod *corev1.Pod, main *corev1.Container, log logrus.FieldLogger, patches []PatchOp) []PatchOp {
	if m.CacheVolume == nil || m.CacheDir == "" {
		return patches
	}
	uri, ok := m.cacheURI(pod)
	if !ok {
		return patches
	}
	log = log.WithField("cache", uri)
	st, err := m.CacheVolume.Lookup(ctx, uri)
	if err != nil {
		log.WithError(err).Warn("cache volume: lookup failed; pod compiles locally")
		return patches
	}
	mp := newMetaPatcher(pod)
	switch {
	case st.Complete:
		if m.ReadOnlyMinter == nil {
			return patches
		}
		claim := m.CacheVolume.Cfg.ReadOnlyClaimName(uri)
		if err := m.ReadOnlyMinter.MintReadOnlyFromPVLabels(ctx, st.PrimaryPV, m.CacheVolume.Cfg.ReadOnlyPVName(uri, pod.Namespace), claim, pod.Namespace, m.CacheVolume.Cfg.ReadOnlyLabels(uri)); err != nil {
			log.WithError(err).Warn("cache volume: mint read-only claim failed; pod compiles locally")
			return patches
		}
		if err := m.CacheVolume.TouchLastUsed(ctx, st.PrimaryPV); err != nil {
			log.WithError(err).Warn("cache volume: record last use failed")
		}
		ordinal := podOrdinal(pod)
		patches = append(patches, mp.annotation(CacheURIAnnotation, uri)...)
		patches = append(patches, mp.annotation(modelvolume.CacheOrdinalAnnotation, strconv.Itoa(ordinal))...)
		patches = append(patches, PatchOp{Op: "add", Path: "/spec/volumes/-", Value: corev1.Volume{Name: cacheSeedVolumeName, VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim, ReadOnly: true}}}})
		// The pod's own rank directory of the set lands in its cachedir
		// (reader_steps.go): the engine then finds its compile caches at
		// the same path, writable.
		seed := seedStep(cacheSeedInitName, main, volumeAt{Volume: cacheSeedVolumeName, Path: cacheSeedMount}, strconv.Itoa(ordinal),
			volumeAt{Volume: cacheDirVolumeName, Path: m.CacheDir}, cacheSubdir, fmt.Sprintf("rank %d", ordinal), inheritPosture)
		patches = appendInits(pod, patches, seed)
		log.WithField("claim", claim).Info("cache volume: complete; seeding the cachedir from the read-only claim")
		return patches
	case st.Failed:
		log.Info("cache volume: recent capture failure recorded; pod compiles locally")
		return patches
	default:
		size := m.groupSize(ctx, pod)
		patches = append(patches, mp.label(CacheCaptureLabel, "true")...)
		patches = append(patches, mp.label(modelvolume.CacheKeyLabel, modelvolume.Key(uri))...)
		patches = append(patches, mp.annotation(CacheURIAnnotation, uri)...)
		patches = append(patches, mp.annotation(modelvolume.CacheOrdinalAnnotation, strconv.Itoa(podOrdinal(pod)))...)
		patches = append(patches, mp.annotation(modelvolume.CacheGroupSizeAnnotation, strconv.Itoa(size))...)
		patches = append(patches, mp.annotation(CacheVolumeAnnotation, cacheDirVolumeName)...)
		patches = append(patches, mp.annotation(CacheSubpathAnnotation, cacheSubdir)...)
		log.WithFields(logrus.Fields{"ordinal": podOrdinal(pod), "group_size": size}).Info("cache volume: no set yet; rank marked for collection after Ready")
		return patches
	}
}
