/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package store

import (
	"cmp"
	"maps"
	"slices"
	"time"
)

// NextEpoch returns the lease epoch a registration mints:
// max(stored + 1, now in unix milliseconds). Epochs therefore increase across
// re-registrations even when the clock of the handling replica is behind.
func NextEpoch(stored uint64, now time.Time) uint64 {
	next := stored + 1
	if ms := now.UnixMilli(); ms > 0 && uint64(ms) > next {
		next = uint64(ms)
	}
	return next
}

// Sort orders every slice of the snapshot by key.
func (s *ClusterSetSnapshot) Sort() {
	slices.SortFunc(s.Clusters, func(a, b ClusterRecord) int { return CompareClusterKeys(a.Key, b.Key) })
	slices.SortFunc(s.Exports, func(a, b ExportRecord) int { return CompareExportKeys(a.Key, b.Key) })
	slices.SortFunc(s.EndpointSets, func(a, b EndpointSetRecord) int { return CompareExportKeys(a.Key, b.Key) })
	slices.SortFunc(s.Pools, func(a, b PoolRecord) int { return ComparePoolKeys(a.Key, b.Key) })
	slices.SortFunc(s.PoolAgents, func(a, b PoolAgentRecord) int { return ComparePoolAgentKeys(a.Key, b.Key) })
}

// CompareClusterKeys orders cluster keys by clusterset, then cluster.
func CompareClusterKeys(a, b ClusterKey) int {
	return cmp.Or(cmp.Compare(a.ClusterSet, b.ClusterSet), cmp.Compare(a.Cluster, b.Cluster))
}

// CompareExportKeys orders export keys by clusterset, cluster, namespace,
// then name.
func CompareExportKeys(a, b ExportKey) int {
	return cmp.Or(
		cmp.Compare(a.ClusterSet, b.ClusterSet),
		cmp.Compare(a.Cluster, b.Cluster),
		cmp.Compare(a.Namespace, b.Namespace),
		cmp.Compare(a.Name, b.Name),
	)
}

// ComparePoolKeys orders pool keys by clusterset, cluster, then pool id.
func ComparePoolKeys(a, b PoolKey) int {
	return cmp.Or(
		cmp.Compare(a.ClusterSet, b.ClusterSet),
		cmp.Compare(a.Cluster, b.Cluster),
		cmp.Compare(a.PoolID, b.PoolID),
	)
}

// ComparePoolAgentKeys orders pool agent keys by clusterset, cluster, pool
// id, then role.
func ComparePoolAgentKeys(a, b PoolAgentKey) int {
	return cmp.Or(ComparePoolKeys(a.PoolKey(), b.PoolKey()), cmp.Compare(a.Role, b.Role))
}

// Apply applies the patch to an agent registration and reports whether it
// changed it. Empty attributes leave the stored ones unchanged.
func (p PoolAgentHeartbeatPatch) Apply(rec *PoolAgentRecord) bool {
	if len(p.Attributes) == 0 || maps.Equal(p.Attributes, rec.Attributes) {
		return false
	}
	rec.Attributes = maps.Clone(p.Attributes)
	return true
}

// Apply applies the patch to a pool spec and reports whether it changed it.
func (p PoolHeartbeatPatch) Apply(spec *PoolSpec) bool {
	changed := false
	if p.WorkerCount != nil && *p.WorkerCount != spec.WorkerCount {
		spec.WorkerCount = *p.WorkerCount
		changed = true
	}
	if p.ReadyWorkerCount != nil && *p.ReadyWorkerCount != spec.ReadyWorkerCount {
		spec.ReadyWorkerCount = *p.ReadyWorkerCount
		changed = true
	}
	if p.DesiredState != "" && p.DesiredState != spec.DesiredState {
		spec.DesiredState = p.DesiredState
		changed = true
	}
	return changed
}
