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

package dynamodb

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
)

func TestKeyLayout(t *testing.T) {
	ek := store.ExportKey{ClusterSet: "prod", Cluster: "c1", Namespace: "llm", Name: "router"}
	pk := store.PoolKey{ClusterSet: "prod", Cluster: "c1", PoolID: "1b4e28ba"}
	tests := []struct {
		name string
		got  key
		want key
	}{
		{"clusterset index", clusterSetIndexKey("prod"), key{"CSX", "CS#prod"}},
		{"cluster", clusterKey(store.ClusterKey{ClusterSet: "prod", Cluster: "c1"}), key{"CS#prod", "CL#c1"}},
		{"export", exportKey(ek), key{"CS#prod", "EX#c1#llm#router"}},
		{"pool", poolKey(pk), key{"CS#prod", "PL#c1#1b4e28ba"}},
		{"pool name", poolNameKey("prod", "c1", "pool-a"), key{"CS#prod", "PN#c1#pool-a"}},
		{"pool agent", poolAgentKey(store.PoolAgentKey{ClusterSet: "prod", Cluster: "c1", PoolID: "1b4e28ba", Role: store.FrontendRolePylon, AgentID: "pylon-0"}),
			key{"CS#prod", "PA#c1#1b4e28ba#PYLON#pylon-0"}},
		{"endpoint partition marker", endpointMarkKey("prod", "c1"), key{"CS#prod", "EPX#c1"}},
		{"endpoint set", endpointSetKey(ek), key{"EP#prod#c1", "llm#router"}},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, tt.got, tt.name)
	}
	assert.Equal(t, "EX#c1#", exportSKPrefix("c1"))
	assert.Equal(t, "PL#c1#", poolSKPrefix("c1"))
	assert.Equal(t, "PN#c1#", poolNameSKPrefix("c1"))
	assert.Equal(t, "PA#c1#", poolAgentSKPrefix("c1"))
	assert.Equal(t, "PA#c1#1b4e28ba#", poolAgentPoolSKPrefix(pk))
}

func TestKeyEscaping(t *testing.T) {
	odd := "a#b%23c%"
	assert.Equal(t, "a%23b%2523c%25", esc(odd))
	assert.Equal(t, odd, unesc(esc(odd)))
	assert.NotContains(t, esc(odd), "#")

	ek := store.ExportKey{ClusterSet: odd, Cluster: odd, Namespace: odd, Name: odd}
	k := exportKey(ek)
	cs, ok := parseClusterSetPK(k.pk)
	assert.True(t, ok)
	assert.Equal(t, odd, cs)
	got, ok := parseExportSK(cs, k.sk)
	assert.True(t, ok)
	assert.Equal(t, ek, got)

	ek2 := endpointSetKey(ek)
	gcs, gcluster, ok := parseEndpointPK(ek2.pk)
	assert.True(t, ok)
	got, ok = parseEndpointSetSK(gcs, gcluster, ek2.sk)
	assert.True(t, ok)
	assert.Equal(t, ek, got)

	pk := store.PoolKey{ClusterSet: odd, Cluster: odd, PoolID: odd}
	gotPool, ok := parsePoolSK(odd, poolKey(pk).sk)
	assert.True(t, ok)
	assert.Equal(t, pk, gotPool)

	ak := store.PoolAgentKey{ClusterSet: odd, Cluster: odd, PoolID: odd, Role: store.FrontendRole(odd), AgentID: odd}
	gotAgent, ok := parsePoolAgentSK(odd, poolAgentKey(ak).sk)
	assert.True(t, ok)
	assert.Equal(t, ak, gotAgent)

	ck := store.ClusterKey{ClusterSet: odd, Cluster: odd}
	gotCluster, ok := parseClusterSK(odd, clusterKey(ck).sk)
	assert.True(t, ok)
	assert.Equal(t, ck, gotCluster)

	name, ok := parseClusterSetIndexSK(clusterSetIndexKey(odd).sk)
	assert.True(t, ok)
	assert.Equal(t, odd, name)
	cluster, ok := parseEndpointMarkSK(endpointMarkKey(odd, odd).sk)
	assert.True(t, ok)
	assert.Equal(t, odd, cluster)
}

// TestKeyPrefixesDoNotCollide checks that the prefix of one cluster never
// matches the records of another whose name extends it.
func TestKeyPrefixesDoNotCollide(t *testing.T) {
	c10 := exportKey(store.ExportKey{ClusterSet: "cs", Cluster: "c10", Namespace: "ns", Name: "svc"})
	assert.False(t, strings.HasPrefix(c10.sk, exportSKPrefix("c1")))
	p10 := poolKey(store.PoolKey{ClusterSet: "cs", Cluster: "c10", PoolID: "p"})
	assert.False(t, strings.HasPrefix(p10.sk, poolSKPrefix("c1")))
	n10 := poolNameKey("cs", "c10", "n")
	assert.False(t, strings.HasPrefix(n10.sk, poolNameSKPrefix("c1")))
	a10 := poolAgentKey(store.PoolAgentKey{ClusterSet: "cs", Cluster: "c10", PoolID: "p", Role: store.FrontendRoleOpenAI})
	assert.False(t, strings.HasPrefix(a10.sk, poolAgentSKPrefix("c1")))
	p10a := poolAgentKey(store.PoolAgentKey{ClusterSet: "cs", Cluster: "c1", PoolID: "p10", Role: store.FrontendRoleOpenAI})
	assert.False(t, strings.HasPrefix(p10a.sk, poolAgentPoolSKPrefix(store.PoolKey{ClusterSet: "cs", Cluster: "c1", PoolID: "p1"})))
	assert.NotEqual(t, endpointPK("cs", "c1#x"), endpointPK("cs#c1", "x"))
}

func TestParseRejectsOtherShapes(t *testing.T) {
	_, ok := parseClusterSK("cs", "EX#c1#ns#name")
	assert.False(t, ok)
	_, ok = parseExportSK("cs", "EX#c1#ns")
	assert.False(t, ok)
	_, ok = parsePoolSK("cs", "PL#c1")
	assert.False(t, ok)
	_, ok = parsePoolAgentSK("cs", "PA#c1#p#PYLON")
	assert.False(t, ok)
	_, ok = parseEndpointSetSK("cs", "c1", "ns")
	assert.False(t, ok)
	_, _, ok = parseEndpointPK("CS#cs")
	assert.False(t, ok)
	_, ok = parseClusterSetPK("CSX")
	assert.False(t, ok)
	_, ok = parseEndpointMarkSK("EP#c1")
	assert.False(t, ok)
	_, ok = parseClusterSetIndexSK("PING")
	assert.False(t, ok)
}
