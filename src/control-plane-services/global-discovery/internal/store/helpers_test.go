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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNextEpoch(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	ms := uint64(now.UnixMilli())
	tests := []struct {
		name   string
		stored uint64
		now    time.Time
		want   uint64
	}{
		{"first registration takes the clock", 0, now, ms},
		{"clock ahead of stored", ms - 5000, now, ms},
		{"same millisecond", ms, now, ms + 1},
		{"clock behind stored", ms + 60000, now, ms + 60001},
		{"zero time", 0, time.Time{}, 1},
		{"before the unix epoch", 9, time.Unix(-10, 0), 10},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, NextEpoch(tt.stored, tt.now), tt.name)
	}
}

func TestLeaseExpiresAt(t *testing.T) {
	renewed := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	l := Lease{Epoch: 1, TTL: 30 * time.Second, RenewedAt: renewed}
	assert.Equal(t, renewed.Add(30*time.Second), l.ExpiresAt())
}

func TestKeyHelpers(t *testing.T) {
	ek := ExportKey{ClusterSet: "cs", Cluster: "c1", Namespace: "ns", Name: "svc"}
	assert.Equal(t, ClusterKey{ClusterSet: "cs", Cluster: "c1"}, ek.ClusterKey())
	pk := PoolKey{ClusterSet: "cs", Cluster: "c2", PoolID: "p"}
	assert.Equal(t, ClusterKey{ClusterSet: "cs", Cluster: "c2"}, pk.ClusterKey())
}

func TestSnapshotSort(t *testing.T) {
	snap := ClusterSetSnapshot{
		Clusters: []ClusterRecord{{Key: ClusterKey{"cs", "b"}}, {Key: ClusterKey{"cs", "a"}}},
		Exports: []ExportRecord{
			{Key: ExportKey{"cs", "b", "ns", "a"}},
			{Key: ExportKey{"cs", "a", "ns", "b"}},
			{Key: ExportKey{"cs", "a", "ns", "a"}},
			{Key: ExportKey{"cs", "a", "a", "z"}},
		},
		EndpointSets: []EndpointSetRecord{{Key: ExportKey{"cs", "b", "ns", "a"}}, {Key: ExportKey{"cs", "a", "ns", "a"}}},
		Pools:        []PoolRecord{{Key: PoolKey{"cs", "b", "1"}}, {Key: PoolKey{"cs", "a", "2"}}, {Key: PoolKey{"cs", "a", "1"}}},
	}
	snap.Sort()

	assert.Equal(t, []ClusterKey{{"cs", "a"}, {"cs", "b"}}, []ClusterKey{snap.Clusters[0].Key, snap.Clusters[1].Key})
	var exports []ExportKey
	for _, e := range snap.Exports {
		exports = append(exports, e.Key)
	}
	assert.Equal(t, []ExportKey{{"cs", "a", "a", "z"}, {"cs", "a", "ns", "a"}, {"cs", "a", "ns", "b"}, {"cs", "b", "ns", "a"}}, exports)
	assert.Equal(t, "a", snap.EndpointSets[0].Key.Cluster)
	var pools []PoolKey
	for _, p := range snap.Pools {
		pools = append(pools, p.Key)
	}
	assert.Equal(t, []PoolKey{{"cs", "a", "1"}, {"cs", "a", "2"}, {"cs", "b", "1"}}, pools)

	assert.Negative(t, CompareClusterKeys(ClusterKey{"a", "z"}, ClusterKey{"b", "a"}))
	assert.Zero(t, CompareExportKeys(ExportKey{"a", "b", "c", "d"}, ExportKey{"a", "b", "c", "d"}))
	assert.Positive(t, ComparePoolKeys(PoolKey{"a", "b", "2"}, PoolKey{"a", "b", "1"}))
}

func TestPoolHeartbeatPatchApply(t *testing.T) {
	spec := PoolSpec{WorkerCount: 4, ReadyWorkerCount: 2, DesiredState: DesiredStateActive}

	assert.False(t, PoolHeartbeatPatch{}.Apply(&spec), "an empty patch changes nothing")

	same := int32(4)
	assert.False(t, PoolHeartbeatPatch{WorkerCount: &same, DesiredState: DesiredStateActive}.Apply(&spec), "equal values change nothing")

	workers, ready := int32(8), int32(6)
	assert.True(t, PoolHeartbeatPatch{WorkerCount: &workers, ReadyWorkerCount: &ready, DesiredState: DesiredStateDraining}.Apply(&spec))
	assert.Equal(t, PoolSpec{WorkerCount: 8, ReadyWorkerCount: 6, DesiredState: DesiredStateDraining}, spec)
}

func TestCloneIsDeep(t *testing.T) {
	cluster := ClusterRecord{Zones: []string{"z"}, Labels: map[string]string{"k": "v"}}
	cc := cluster.Clone()
	cc.Zones[0], cc.Labels["k"] = "x", "x"
	assert.Equal(t, "z", cluster.Zones[0])
	assert.Equal(t, "v", cluster.Labels["k"])

	export := ExportRecord{Spec: ExportSpec{
		Annotations:           map[string]string{"a": "1"},
		Labels:                map[string]string{"l": "1"},
		Ports:                 []ServicePort{{Port: 1}},
		SessionAffinityConfig: &SessionAffinityConfig{ClientIP: &ClientIPConfig{TimeoutSeconds: 1}},
		IPFamilies:            []IPFamily{IPFamilyIPv4},
	}}
	ec := export.Clone()
	ec.Spec.Annotations["a"], ec.Spec.Labels["l"] = "x", "x"
	ec.Spec.Ports[0].Port = 2
	ec.Spec.SessionAffinityConfig.ClientIP.TimeoutSeconds = 2
	ec.Spec.IPFamilies[0] = IPFamilyIPv6
	assert.Equal(t, "1", export.Spec.Annotations["a"])
	assert.Equal(t, "1", export.Spec.Labels["l"])
	assert.Equal(t, int32(1), export.Spec.Ports[0].Port)
	assert.Equal(t, int32(1), export.Spec.SessionAffinityConfig.ClientIP.TimeoutSeconds)
	assert.Equal(t, IPFamilyIPv4, export.Spec.IPFamilies[0])

	noClientIP := ExportSpec{SessionAffinityConfig: &SessionAffinityConfig{}}.Clone()
	assert.Nil(t, noClientIP.SessionAffinityConfig.ClientIP)

	eps := EndpointSetRecord{Endpoints: []Endpoint{{Addresses: []string{"a"}, Ports: []EndpointPort{{Port: 1}}, Attributes: map[string]string{"k": "v"}}}}
	epc := eps.Clone()
	epc.Endpoints[0].Addresses[0] = "x"
	epc.Endpoints[0].Ports[0].Port = 2
	epc.Endpoints[0].Attributes["k"] = "x"
	assert.Equal(t, Endpoint{Addresses: []string{"a"}, Ports: []EndpointPort{{Port: 1}}, Attributes: map[string]string{"k": "v"}}, eps.Endpoints[0])
	assert.Nil(t, EndpointSetRecord{}.Clone().Endpoints)

	pool := PoolRecord{Spec: PoolSpec{
		Location:  Location{Zones: []string{"z"}},
		Models:    []ModelServed{{Aliases: []string{"a"}, Attributes: map[string]string{"k": "v"}}},
		Frontends: []FrontendEndpoint{{Service: &ServiceRef{Name: "svc"}}, {URL: "https://x"}},
		Services:  []ServiceRef{{Name: "svc"}},
		Labels:    map[string]string{"k": "v"},
	}}
	pc := pool.Clone()
	pc.Spec.Location.Zones[0] = "x"
	pc.Spec.Models[0].Aliases[0] = "x"
	pc.Spec.Models[0].Attributes["k"] = "x"
	pc.Spec.Frontends[0].Service.Name = "x"
	pc.Spec.Services[0].Name = "x"
	pc.Spec.Labels["k"] = "x"
	assert.Equal(t, "z", pool.Spec.Location.Zones[0])
	assert.Equal(t, "a", pool.Spec.Models[0].Aliases[0])
	assert.Equal(t, "v", pool.Spec.Models[0].Attributes["k"])
	assert.Equal(t, "svc", pool.Spec.Frontends[0].Service.Name)
	assert.Nil(t, pc.Spec.Frontends[1].Service)
	assert.Equal(t, "svc", pool.Spec.Services[0].Name)
	assert.Equal(t, "v", pool.Spec.Labels["k"])
}
