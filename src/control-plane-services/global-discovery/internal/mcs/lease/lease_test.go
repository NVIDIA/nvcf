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

package lease_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/mcs/lease"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/mcs/mcstest"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/mcs/metrics"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/pkg/client"
)

type harness struct {
	gds   *mcstest.GDS
	lease *lease.Lease
	reg   *prometheus.Registry

	mu   sync.Mutex
	regs []bool
}

func (h *harness) registrations() []bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]bool(nil), h.regs...)
}

func start(t *testing.T, deregister bool) (*harness, context.CancelFunc, chan error) {
	t.Helper()
	h := &harness{gds: mcstest.StartGDS(t), reg: prometheus.NewRegistry()}
	m := metrics.New()
	require.NoError(t, m.Register(h.reg))
	h.lease = lease.New(lease.Options{
		GDS:                  h.gds.Client,
		Identity:             mcstest.Identity(),
		TTL:                  300 * time.Millisecond,
		AgentID:              "pod-0",
		AgentVersion:         "test",
		DeregisterOnShutdown: deregister,
		Metrics:              m,
		Logger:               testr.New(t),
		OnRegistered: func(first bool) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.regs = append(h.regs, first)
		},
		Backoff: client.Backoff{Initial: 10 * time.Millisecond, Max: 50 * time.Millisecond, Multiplier: 2},
	})
	assert.True(t, h.lease.NeedLeaderElection())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.lease.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return h, cancel, done
}

func (h *harness) cluster(t *testing.T) (*gdsv1.Cluster, error) {
	t.Helper()
	resp, err := h.gds.Client.Clusters().Get(mcstest.Ctx(t), &gdsv1.GetClusterRequest{Clusterset: mcstest.ClusterSet, Cluster: mcstest.Cluster})
	return resp.GetCluster(), err
}

func TestRegisterAndHeartbeat(t *testing.T) {
	h, _, _ := start(t, false)
	require.NoError(t, h.lease.WaitRegistered(mcstest.Ctx(t)))
	assert.True(t, h.lease.Registered())
	c, err := h.cluster(t)
	require.NoError(t, err)
	assert.Equal(t, h.lease.Epoch(), c.GetLease().GetEpoch())
	assert.Equal(t, "pod-0", c.GetAgentId())
	assert.Equal(t, mcstest.Region, c.GetRegion())
	require.Eventually(t, func() bool { return h.gds.Server.CallCount("HeartbeatCluster") >= 3 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []bool{true}, h.registrations())
	assert.Equal(t, 1.0, mcstest.MetricValue(t, h.reg, "nvcf_gds_mcs_controller_cluster_registered", nil))
	assert.Equal(t, 1.0, mcstest.MetricValue(t, h.reg, "nvcf_gds_mcs_controller_registrations_total", map[string]string{"result": "success"}))
}

func TestReregistersWhenTheLeaseIsLost(t *testing.T) {
	h, _, _ := start(t, false)
	require.NoError(t, h.lease.WaitRegistered(mcstest.Ctx(t)))
	first := h.lease.Epoch()

	// Another registration takes the lease: ABORTED LEASE_EPOCH_MISMATCH.
	_, err := h.gds.Client.Clusters().Register(mcstest.Ctx(t), &gdsv1.RegisterClusterRequest{Clusterset: mcstest.ClusterSet, Cluster: mcstest.Cluster})
	require.NoError(t, err)
	h.lease.Kick()
	require.Eventually(t, func() bool { return len(h.registrations()) == 2 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []bool{true, false}, h.registrations())
	assert.Greater(t, h.lease.Epoch(), first)
	assert.Equal(t, 1.0, mcstest.MetricValue(t, h.reg, "nvcf_gds_mcs_controller_heartbeat_failures_total", map[string]string{"reason": "epoch_mismatch"}))

	// GDS forgets the cluster: NOT_FOUND.
	h.gds.Server.RemoveCluster(mcstest.ClusterSet, mcstest.Cluster)
	h.lease.Kick()
	require.Eventually(t, func() bool { return len(h.registrations()) == 3 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, 1.0, mcstest.MetricValue(t, h.reg, "nvcf_gds_mcs_controller_heartbeat_failures_total", map[string]string{"reason": "not_registered"}))
	_, err = h.cluster(t)
	require.NoError(t, err)
}

func TestTransientFailuresKeepTheLease(t *testing.T) {
	h, _, _ := start(t, false)
	require.NoError(t, h.lease.WaitRegistered(mcstest.Ctx(t)))
	for range 4 {
		h.gds.Server.FailNext("HeartbeatCluster", codes.Internal)
	}
	h.lease.Kick()
	require.Eventually(t, func() bool {
		return mcstest.MetricValue(t, h.reg, "nvcf_gds_mcs_controller_heartbeat_failures_total", map[string]string{"reason": "error"}) >= 4
	}, 5*time.Second, 10*time.Millisecond)
	assert.True(t, h.lease.Registered())
	assert.Equal(t, []bool{true}, h.registrations())
}

func TestRegistrationRetries(t *testing.T) {
	srv := mcstest.StartGDS(t)
	for range 4 {
		srv.Server.FailNext("RegisterCluster", codes.Internal)
	}
	l := lease.New(lease.Options{GDS: srv.Client, Identity: mcstest.Identity(), Logger: testr.New(t),
		Backoff: client.Backoff{Initial: 5 * time.Millisecond, Max: 20 * time.Millisecond}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Start(ctx) }()
	require.NoError(t, l.WaitRegistered(mcstest.Ctx(t)))
	assert.Equal(t, 5, srv.Server.CallCount("RegisterCluster"))
	cancel()
	require.NoError(t, <-done)
	assert.False(t, l.Registered())

	waitCtx, waitCancel := context.WithCancel(context.Background())
	waitCancel()
	assert.ErrorIs(t, l.WaitRegistered(waitCtx), context.Canceled)
}

func TestDeregisterOnShutdown(t *testing.T) {
	for _, deregister := range []bool{false, true} {
		h, cancel, done := start(t, deregister)
		require.NoError(t, h.lease.WaitRegistered(mcstest.Ctx(t)))
		cancel()
		require.NoError(t, <-done)
		done <- nil // for the cleanup
		_, err := h.cluster(t)
		if deregister {
			assert.Equal(t, codes.NotFound, status.Code(err))
		} else {
			assert.NoError(t, err)
		}
	}
}

func TestDeregisterFailureIsLogged(t *testing.T) {
	h, cancel, done := start(t, true)
	require.NoError(t, h.lease.WaitRegistered(mcstest.Ctx(t)))
	h.gds.Server.FailNext("DeregisterCluster", codes.Internal)
	cancel()
	require.NoError(t, <-done)
	done <- nil
	_, err := h.cluster(t)
	assert.NoError(t, err, "the lease expires instead")
}
