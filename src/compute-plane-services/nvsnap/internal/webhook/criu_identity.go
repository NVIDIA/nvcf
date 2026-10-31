// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"strconv"

	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelvolume"
)

// criuIdentityPatches stamps a GPU pod with the identity a CRIU instance
// capture keys on (agent criu_group.go): its configuration (cache-uri),
// its rank and the size of its group. The compile-cache set used to be
// the only writer of these, so an instance was captured only while its
// cache set and model volume were healthy and enabled. With CRIU the
// capture holds the whole process; the identity needs only the
// configuration hash and the group's shape.
func (m *Mutator) criuIdentityPatches(ctx context.Context, pod *corev1.Pod) []PatchOp {
	if !m.CRIUCapture || gpuRequest(pod) == 0 {
		return nil
	}
	uri, ok := m.cacheURI(pod)
	if !ok {
		return nil
	}
	mp := newMetaPatcher(pod)
	patches := mp.annotation(CacheURIAnnotation, uri)
	patches = append(patches, mp.annotation(modelvolume.CacheOrdinalAnnotation, strconv.Itoa(podOrdinal(pod)))...)
	patches = append(patches, mp.annotation(modelvolume.CacheGroupSizeAnnotation, strconv.Itoa(m.groupSize(ctx, pod)))...)
	return patches
}
