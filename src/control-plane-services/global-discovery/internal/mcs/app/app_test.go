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

package app

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/mcs/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/mcs/identity"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/mcs/mcstest"
)

func testConfig(url string) config.Config {
	return config.Config{
		GDSURLs: []string{url}, GDSAllowInsecureHTTP: true, IdentitySource: config.IdentitySourceFlags,
		ClusterSetID: mcstest.ClusterSet, ClusterID: mcstest.Cluster, LeaseTTL: config.DefaultLeaseTTL,
		AutoExportPools: true, WriteClusterProperties: true, EndpointsPerSlice: 100, MaxConcurrentReconciles: 2,
		MetricsBindAddress: "0", HealthProbeBindAddress: "0",
	}
}

// unreachable is a REST configuration of an API server that never answers;
// the manager does not dial it before Start.
var unreachable = &rest.Config{Host: "http://127.0.0.1:1", Timeout: 100 * time.Millisecond}

func skipNames(o *ctrl.Options) { o.Controller.SkipNameValidation = ptr.To(true) }

func TestNewWiresEverything(t *testing.T) {
	gds := mcstest.StartGDS(t)
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1", Labels: map[string]string{identity.LabelRegion: "us-east-1"}}}
	c := mcstest.FakeClient(t, node)
	reg := prometheus.NewRegistry()
	a, err := New(context.Background(), testConfig(gds.Running.URL()), unreachable, Options{
		Registry: reg, Logger: testr.New(t), ManagerOptions: skipNames, Client: c, AgentID: "pod-0",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = a.GDS.Close() })
	assert.Equal(t, "us-east-1", a.Identity.Region)
	assert.NotNil(t, a.Lease)
	assert.NotNil(t, a.Exports)
	assert.NotNil(t, a.Importer)
	assert.NotNil(t, a.Resyncer)
	v, err := identity.GetClusterProperty(context.Background(), c, api.ClusterPropertyClusterID)
	require.NoError(t, err)
	assert.Equal(t, mcstest.Cluster, v, "--write-cluster-properties")

	// Ready once the client connected; this replica is not the leader.
	require.Eventually(t, func() bool { return a.ReadyCheck(nil) == nil }, 5*time.Second, 10*time.Millisecond)
	families, err := reg.Gather()
	require.NoError(t, err)
	var names []string
	for _, f := range families {
		names = append(names, f.GetName())
	}
	assert.Contains(t, names, "nvcf_gds_mcs_controller_exports")
	assert.Contains(t, names, "nvcf_gds_client_requests_total")

	// The OnReconnect hook triggers a resync.
	gds.Running.Stop()
	_, err = a.GDS.ServerInfo().Get(context.Background())
	assert.Error(t, err)
}

func TestNewErrors(t *testing.T) {
	gds := mcstest.StartGDS(t)
	cfg := testConfig(gds.Running.URL())

	_, err := New(context.Background(), cfg, unreachable, Options{Logger: testr.New(t), ManagerOptions: skipNames})
	require.ErrorContains(t, err, "resolve cluster identity")

	taken := mcstest.FakeClient(t)
	require.NoError(t, identity.WriteClusterProperties(context.Background(), taken, identity.Identity{ClusterSet: "other", Cluster: "x"}))
	_, err = New(context.Background(), cfg, unreachable, Options{Logger: testr.New(t), ManagerOptions: skipNames, Client: taken})
	require.ErrorContains(t, err, "refusing to overwrite")

	bad := cfg
	bad.WriteClusterProperties = false
	bad.GDSAllowInsecureHTTP = false
	_, err = New(context.Background(), bad, unreachable, Options{Logger: testr.New(t), ManagerOptions: skipNames, Client: mcstest.FakeClient(t)})
	require.ErrorContains(t, err, "create GDS client")

	reg := prometheus.NewRegistry()
	_, err = New(context.Background(), cfg, unreachable, Options{Registry: reg, ManagerOptions: skipNames, Client: mcstest.FakeClient(t)})
	require.NoError(t, err)
	_, err = New(context.Background(), cfg, unreachable, Options{Registry: reg, ManagerOptions: skipNames, Client: mcstest.FakeClient(t)})
	require.ErrorContains(t, err, "register metrics")

	_, err = New(context.Background(), cfg, &rest.Config{Host: "::not a url"}, Options{ManagerOptions: skipNames})
	require.Error(t, err)
}

func TestHostname(t *testing.T) {
	t.Setenv("HOSTNAME", "pod-7")
	assert.Equal(t, "pod-7", hostname())
	t.Setenv("HOSTNAME", "")
	assert.NotEmpty(t, hostname())
}

type electedManager struct {
	manager.Manager
	elected chan struct{}
}

func (m electedManager) Elected() <-chan struct{} { return m.elected }

func TestReadyCheckOnTheLeader(t *testing.T) {
	gds := mcstest.StartGDS(t)
	a, err := New(context.Background(), testConfig(gds.Running.URL()), unreachable, Options{
		ManagerOptions: skipNames, Client: mcstest.FakeClient(t),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = a.GDS.Close() })
	require.Eventually(t, func() bool { return a.ReadyCheck(nil) == nil }, 5*time.Second, 10*time.Millisecond)
	elected := make(chan struct{})
	close(elected)
	a.Manager = electedManager{Manager: a.Manager, elected: elected}
	require.ErrorContains(t, a.ReadyCheck(nil), "registered")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Lease.Start(ctx) }()
	require.NoError(t, a.Lease.WaitRegistered(mcstest.Ctx(t)))
	assert.NoError(t, a.ReadyCheck(nil))
	cancel()
	require.NoError(t, <-done)
}

func TestReadyCheckBeforeTheFirstConnection(t *testing.T) {
	a, err := New(context.Background(), testConfig("http://127.0.0.1:1"), unreachable, Options{
		ManagerOptions: skipNames, Client: mcstest.FakeClient(t),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = a.GDS.Close() })
	require.ErrorContains(t, a.ReadyCheck(nil), "not connected")
}
