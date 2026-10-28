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

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store/memory"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/storefactory"
)

var t0 = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func env(vars map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := vars[k]
		return v, ok
	}
}

func seeded(t *testing.T) *memory.Store {
	t.Helper()
	ctx := context.Background()
	st := memory.New()
	for _, c := range []string{"a", "b"} {
		_, err := st.PutCluster(ctx, store.ClusterRecord{
			Key: store.ClusterKey{ClusterSet: "prod", Cluster: c}, Region: "us-east-1", Lease: store.Lease{TTL: 30 * time.Second},
		}, t0)
		require.NoError(t, err)
		key := store.ExportKey{ClusterSet: "prod", Cluster: c, Namespace: "llm", Name: "router"}
		require.NoError(t, st.PutExport(ctx, store.ExportRecord{Key: key, Generation: 1, Spec: store.ExportSpec{
			Type: store.ServiceTypeClusterSetIP, Labels: map[string]string{api.LabelRole: api.RoleRouter}, ExportCreationTimestamp: t0,
		}}))
		require.NoError(t, st.PutEndpointSet(ctx, store.EndpointSetRecord{Key: key, Generation: 1, Endpoints: []store.Endpoint{
			{Addresses: []string{"10.0.0.1"}, Conditions: store.EndpointConditions{Ready: true}},
			{Addresses: []string{"10.0.0.2"}},
		}}))
	}
	_, err := st.PutPool(ctx, store.PoolRecord{
		Key: store.PoolKey{ClusterSet: "prod", Cluster: "b", PoolID: "p1"}, Name: "mistral", Generation: 1,
		Lease: store.Lease{TTL: time.Minute}, Spec: store.PoolSpec{DesiredState: store.DesiredStateDraining},
	}, store.PutPoolOptions{Now: t0, MintLease: true})
	require.NoError(t, err)
	// Agent registrations: two Pylons of pool mistral in b, one with a 5s
	// lease that expires first, and a relay of the agent-only pool llama in a.
	for _, r := range []struct {
		cluster, poolID, pool string
		role                  store.FrontendRole
		agentID               string
		ttl                   time.Duration
	}{
		{"b", "p1", "mistral", store.FrontendRolePylon, "pylon-0", time.Minute},
		{"b", "p1", "mistral", store.FrontendRolePylon, "pylon-1", 5 * time.Second},
		{"a", "p2", "llama", store.FrontendRoleKVRelay, "relay-0", time.Minute},
	} {
		_, err = st.PutPoolAgent(ctx, store.PoolAgentRecord{
			Key:      store.PoolAgentKey{ClusterSet: "prod", Cluster: r.cluster, PoolID: r.poolID, Role: r.role, AgentID: r.agentID},
			PoolName: r.pool, Generation: 1, Lease: store.Lease{TTL: r.ttl},
		}, store.PutPoolAgentOptions{Now: t0, MintLease: true})
		require.NoError(t, err)
	}
	_, err = st.PutCluster(ctx, store.ClusterRecord{Key: store.ClusterKey{ClusterSet: "dev", Cluster: "x"}}, t0)
	require.NoError(t, err)
	return st
}

// keepOpen ignores Close so that one store serves several runs.
type keepOpen struct{ store.Store }

func (keepOpen) Close() error { return nil }

func runWith(t *testing.T, st store.Store, now time.Time, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	open := func(context.Context, *config.Config) (store.Store, error) { return keepOpen{st}, nil }
	code := run(context.Background(), args, env(nil), &stdout, &stderr, open, func() time.Time { return now })
	return code, stdout.String(), stderr.String()
}

func TestDescribeText(t *testing.T) {
	st := seeded(t)
	code, out, errOut := runWith(t, st, t0.Add(10*time.Second), "describe")
	require.Equal(t, 0, code, errOut)
	assert.Contains(t, out, "Clusterset dev: 1 clusters")
	assert.Contains(t, out, "Clusterset prod: 2 clusters (2 active), 2 exports, 2 endpoint sets (4 endpoints), 1 pools, 3 pool agent registrations\n")
	assert.Contains(t, out, "llm/router")
	assert.Contains(t, out, "router")
	assert.Contains(t, out, "mistral")
	assert.Contains(t, out, "DRAINING")
	// One row per pool and role: registrations, then active leases.
	assert.Regexp(t, `POOL AGENT\s+POOL ID\s+CLUSTER\s+REGISTRATIONS\s+ACTIVE\n`, out)
	assert.Regexp(t, `llama/KV_RELAY\s+p2\s+a\s+1\s+1\n`, out)
	assert.Regexp(t, `mistral/PYLON\s+p1\s+b\s+2\s+1\n`, out)
	assert.Contains(t, out, "Clusterset dev: 1 clusters (0 active), 0 exports, 0 endpoint sets (0 endpoints), 0 pools, 0 pool agent registrations\n")
	assert.NotContains(t, out[:strings.Index(out, "Clusterset prod")], "POOL AGENT")

	code, out, _ = runWith(t, st, t0.Add(time.Hour), "describe", "--clusterset", "prod")
	require.Equal(t, 0, code)
	assert.Contains(t, out, "(0 active)")
	assert.Contains(t, out, "EXPIRED")
	assert.NotContains(t, out, "Clusterset dev")
}

func TestDescribeJSON(t *testing.T) {
	code, out, _ := runWith(t, seeded(t), t0, "describe", "--clusterset=prod", "--output=json")
	require.Equal(t, 0, code)
	var d []description
	require.NoError(t, json.Unmarshal([]byte(out), &d))
	require.Len(t, d, 1)
	assert.Equal(t, "prod", d[0].ClusterSet)
	assert.Len(t, d[0].Clusters, 2)
	assert.Equal(t, "ACTIVE", d[0].Clusters[0].LeaseState)
	assert.Equal(t, 1, d[0].EndpointSets[0].Ready)
	assert.Equal(t, "router", d[0].Exports[0].Role)
	assert.Equal(t, "p1", d[0].Pools[0].PoolID)
	assert.Equal(t, []poolAgentRow{
		{Cluster: "a", PoolID: "p2", Pool: "llama", Role: "KV_RELAY", Registrations: 1, Active: 1},
		{Cluster: "b", PoolID: "p1", Pool: "mistral", Role: "PYLON", Registrations: 2, Active: 2},
	}, d[0].PoolAgents)
	assert.Equal(t, 3, d[0].Counts["poolAgentRegistrations"])

	// Leases are evaluated at the time of the call: after an hour none is
	// active, and the rows remain.
	code, out, _ = runWith(t, seeded(t), t0.Add(time.Hour), "describe", "--clusterset=prod", "--output=json")
	require.Equal(t, 0, code)
	require.NoError(t, json.Unmarshal([]byte(out), &d))
	require.Len(t, d[0].PoolAgents, 2)
	assert.Equal(t, 2, d[0].PoolAgents[1].Registrations)
	assert.Zero(t, d[0].PoolAgents[1].Active)

	code, out, _ = runWith(t, memory.New(), t0, "describe")
	require.Equal(t, 0, code)
	assert.Contains(t, out, "No clustersets.")
}

func TestDeleteCluster(t *testing.T) {
	st := seeded(t)
	code, _, errOut := runWith(t, st, t0, "delete-cluster", "--clusterset=prod", "--cluster=b")
	assert.Equal(t, 2, code)
	assert.Contains(t, errOut, "would delete cluster prod/b: 1 exports, 1 endpoint sets, 1 pools, 2 pool agent registrations; pass --yes")

	code, out, _ := runWith(t, st, t0, "delete-cluster", "--clusterset=prod", "--cluster=b", "--yes")
	require.Equal(t, 0, code)
	assert.Contains(t, out, "Deleted cluster prod/b: 1 exports, 1 endpoint sets, 1 pools, 2 pool agent registrations.")
	snap, err := st.LoadClusterSet(context.Background(), "prod")
	require.NoError(t, err)
	assert.Len(t, snap.Clusters, 1)
	assert.Empty(t, snap.Pools)
	// The registrations of cluster a stay.
	require.Len(t, snap.PoolAgents, 1)
	assert.Equal(t, "a", snap.PoolAgents[0].Key.Cluster)

	// Cluster a owns no pool record, only the relay registration.
	code, _, errOut = runWith(t, st, t0, "delete-cluster", "--clusterset=prod", "--cluster=a")
	assert.Equal(t, 2, code)
	assert.Contains(t, errOut, "would delete cluster prod/a: 1 exports, 1 endpoint sets, 0 pools, 1 pool agent registrations; pass --yes")

	code, _, errOut = runWith(t, st, t0, "delete-cluster", "--clusterset=prod")
	assert.Equal(t, 2, code)
	assert.Contains(t, errOut, "DNS labels")
}

type brokenStore struct{ store.Store }

func (brokenStore) ListClusterSets(context.Context) ([]string, error) {
	return nil, store.ErrUnavailable
}
func (brokenStore) LoadClusterSet(context.Context, string) (store.ClusterSetSnapshot, error) {
	return store.ClusterSetSnapshot{}, store.ErrUnavailable
}
func (brokenStore) Close() error { return nil }

func TestErrors(t *testing.T) {
	broken := brokenStore{}
	for _, args := range [][]string{
		{"describe"},
		{"describe", "--clusterset=prod"},
		{"delete-cluster", "--clusterset=prod", "--cluster=a", "--yes"},
	} {
		code, _, errOut := runWith(t, broken, t0, args...)
		assert.Equal(t, 1, code, args)
		assert.Contains(t, errOut, "unavailable")
	}
	for _, args := range [][]string{
		{},
		{"frobnicate"},
		{"create-table", "--bogus"},
		{"describe", "--output=yaml"},
		{"describe", "--bogus"},
		{"delete-cluster", "--bogus"},
		{"--bogus"},
	} {
		code, _, _ := runWith(t, memory.New(), t0, args...)
		assert.Equal(t, 2, code, args)
	}
	code, _, errOut := runWith(t, memory.New(), t0, "--help")
	assert.Equal(t, 0, code)
	assert.Contains(t, errOut, "delete-cluster  delete a cluster")

	var stdout, stderr bytes.Buffer
	failOpen := func(context.Context, *config.Config) (store.Store, error) { return nil, errors.New("no backend") }
	assert.Equal(t, 1, run(context.Background(), []string{"describe"}, env(nil), &stdout, &stderr, failOpen, time.Now))
	assert.Contains(t, stderr.String(), "no backend")

	// The store flags have GDS_ environment fallbacks.
	stderr.Reset()
	t.Setenv("AWS_REGION", "")
	code = run(context.Background(), []string{"describe"}, env(map[string]string{"GDS_STORE": "dynamodb"}),
		&stdout, &stderr, storefactory.Open, time.Now)
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr.String(), "no AWS region")
	stderr.Reset()
	code = run(context.Background(), []string{"describe"}, env(map[string]string{"GDS_LEASE_SKEW": "soon"}),
		&stdout, &stderr, storefactory.Open, time.Now)
	assert.Equal(t, 2, code)
}
