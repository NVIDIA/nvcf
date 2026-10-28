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

package view

import (
	"fmt"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
)

// The Apply methods are the write-through ("ApplyLocal") path: an API
// handler calls one after each successful store write with the record the
// store returned, so the handling replica serves the write at once. Each
// call derives the affected objects and publishes the changes to watchers.

// ApplyLocal applies records that a handler stored through this replica, in
// order: store.ClusterRecord, store.ExportRecord, store.EndpointSetRecord,
// store.PoolRecord or store.PoolAgentRecord. It returns an error for any
// other type. Deletions use
// the typed Apply*Delete methods.
func (v *View) ApplyLocal(records ...any) error {
	for _, r := range records {
		switch rec := r.(type) {
		case store.ClusterRecord:
			v.ApplyCluster(rec)
		case store.ExportRecord:
			v.ApplyExport(rec)
		case store.EndpointSetRecord:
			v.ApplyEndpointSet(rec)
		case store.PoolRecord:
			v.ApplyPool(rec)
		case store.PoolAgentRecord:
			v.ApplyPoolAgent(rec)
		default:
			return fmt.Errorf("view: cannot apply %T", r)
		}
	}
	return nil
}

// apply runs fn on the clusterset under its lock and recomputes it.
func (v *View) apply(clusterSet string, fn func(cs *clusterSet)) {
	cs := v.ensure(clusterSet)
	now := v.clock.Now()
	cs.mu.Lock()
	defer cs.mu.Unlock()
	fn(cs)
	v.recompute(cs, now)
}

// ApplyCluster applies a registered or renewed cluster.
func (v *View) ApplyCluster(rec store.ClusterRecord) {
	v.apply(rec.Key.ClusterSet, func(cs *clusterSet) {
		cs.clusters[rec.Key.Cluster] = rec.Clone()
		cs.local.clusters[rec.Key.Cluster] = mark[store.ClusterRecord]{at: v.clock.Now()}
	})
}

// ApplyClusterDelete applies a cluster deletion and its cascade to the
// cluster's exports, endpoint sets, pools and pool agent registrations.
func (v *View) ApplyClusterDelete(key store.ClusterKey) {
	v.apply(key.ClusterSet, func(cs *clusterSet) {
		now := v.clock.Now()
		if prev, ok := cs.clusters[key.Cluster]; ok {
			cs.local.clusters[key.Cluster] = mark[store.ClusterRecord]{at: now, deleted: true, prev: &prev}
		} else {
			cs.local.clusters[key.Cluster] = mark[store.ClusterRecord]{at: now, deleted: true}
		}
		delete(cs.clusters, key.Cluster)
		for k := range cs.exports {
			if k.Cluster == key.Cluster {
				delete(cs.exports, k)
				delete(cs.local.exports, k)
			}
		}
		for k := range cs.endpoints {
			if k.Cluster == key.Cluster {
				delete(cs.endpoints, k)
				delete(cs.local.endpoints, k)
			}
		}
		for k := range cs.pools {
			if k.Cluster == key.Cluster {
				delete(cs.pools, k)
				delete(cs.local.pools, k)
			}
		}
		for k := range cs.agents {
			if k.Cluster == key.Cluster {
				delete(cs.agents, k)
				delete(cs.local.agents, k)
			}
		}
		cs.local.clusterDeletes[key.Cluster] = now
	})
}

// ApplyExport applies a stored export.
func (v *View) ApplyExport(rec store.ExportRecord) {
	v.apply(rec.Key.ClusterSet, func(cs *clusterSet) {
		cs.exports[rec.Key] = rec.Clone()
		cs.local.exports[rec.Key] = mark[store.ExportRecord]{at: v.clock.Now()}
	})
}

// ApplyExportDelete applies an export deletion and its endpoint set's.
func (v *View) ApplyExportDelete(key store.ExportKey) {
	v.apply(key.ClusterSet, func(cs *clusterSet) {
		now := v.clock.Now()
		cs.local.exports[key] = deleteMark(cs.exports, key, now)
		cs.local.endpoints[key] = deleteMark(cs.endpoints, key, now)
		delete(cs.exports, key)
		delete(cs.endpoints, key)
	})
}

// ApplyEndpointSet applies a stored endpoint set.
func (v *View) ApplyEndpointSet(rec store.EndpointSetRecord) {
	v.apply(rec.Key.ClusterSet, func(cs *clusterSet) {
		cs.endpoints[rec.Key] = rec.Clone()
		cs.local.endpoints[rec.Key] = mark[store.EndpointSetRecord]{at: v.clock.Now()}
	})
}

// ApplyEndpointSetDelete applies an endpoint set deletion.
func (v *View) ApplyEndpointSetDelete(key store.ExportKey) {
	v.apply(key.ClusterSet, func(cs *clusterSet) {
		cs.local.endpoints[key] = deleteMark(cs.endpoints, key, v.clock.Now())
		delete(cs.endpoints, key)
	})
}

// ApplyPool applies a stored pool.
func (v *View) ApplyPool(rec store.PoolRecord) {
	v.apply(rec.Key.ClusterSet, func(cs *clusterSet) {
		cs.pools[rec.Key] = rec.Clone()
		cs.local.pools[rec.Key] = mark[store.PoolRecord]{at: v.clock.Now()}
	})
}

// ApplyPoolDelete applies a pool deletion and its cascade to the pool's
// agent registrations (same cluster and pool id), as store.DeletePool does.
func (v *View) ApplyPoolDelete(key store.PoolKey) {
	v.apply(key.ClusterSet, func(cs *clusterSet) {
		now := v.clock.Now()
		cs.local.pools[key] = deleteMark(cs.pools, key, now)
		delete(cs.pools, key)
		for k := range cs.agents {
			if k.PoolKey() == key {
				cs.local.agents[k] = deleteMark(cs.agents, k, now)
				delete(cs.agents, k)
			}
		}
	})
}

// ApplyPoolAgent applies a stored or renewed pool agent registration.
func (v *View) ApplyPoolAgent(rec store.PoolAgentRecord) {
	v.apply(rec.Key.ClusterSet, func(cs *clusterSet) {
		cs.agents[rec.Key] = rec.Clone()
		cs.local.agents[rec.Key] = mark[store.PoolAgentRecord]{at: v.clock.Now()}
	})
}

// ApplyPoolAgentDelete applies the deletion of one pool agent registration.
func (v *View) ApplyPoolAgentDelete(key store.PoolAgentKey) {
	v.apply(key.ClusterSet, func(cs *clusterSet) {
		cs.local.agents[key] = deleteMark(cs.agents, key, v.clock.Now())
		delete(cs.agents, key)
	})
}

func deleteMark[K comparable, R any](recs map[K]R, key K, now time.Time) mark[R] {
	if prev, ok := recs[key]; ok {
		return mark[R]{at: now, deleted: true, prev: &prev}
	}
	return mark[R]{at: now, deleted: true}
}
