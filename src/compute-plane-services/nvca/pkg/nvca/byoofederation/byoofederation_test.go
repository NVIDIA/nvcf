/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package byoofederation

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"

	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/health"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

type fakeRecorder struct {
	mu     sync.Mutex
	states []nvcametrics.BYOOPlatformMetricsFederationState
}

func (r *fakeRecorder) SetBYOOPlatformMetricsFederationState(state nvcametrics.BYOOPlatformMetricsFederationState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states = append(r.states, state)
}

func (r *fakeRecorder) last() nvcametrics.BYOOPlatformMetricsFederationState {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.states) == 0 {
		return ""
	}
	return r.states[len(r.states)-1]
}

// federationServer serves /federate with the given status codes in order,
// repeating the last one.
func federationServer(t *testing.T, statusCodes ...int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := int(calls.Add(1)) - 1
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/federate", r.URL.Path)
		assert.Equal(t, []string{probeSelector}, r.URL.Query()["match[]"])
		w.WriteHeader(statusCodes[min(call, len(statusCodes)-1)])
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func serverTarget(t *testing.T, server *httptest.Server) string {
	t.Helper()
	serverURL, err := url.Parse(server.URL)
	require.NoError(t, err)
	return serverURL.Host
}

func componentHealth(t *testing.T, checker *Checker) nvcatypes.ComponentHealth {
	t.Helper()
	status, err := checker.GetComponentStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, nvcatypes.HealthStatusHealthy, status.Status)
	component, ok := status.Components[ComponentName]
	require.True(t, ok)
	assert.Equal(t, nvcatypes.StatusLevelWarn, component.StatusLevel)
	return component
}

func TestTarget(t *testing.T) {
	const target = "vmsingle.monitoring.svc.cluster.local:8428"
	tests := []struct {
		name string
		cfg  nvcaconfig.BYOOOTelPlatformMetricsFederationConfig
		want string
	}{
		{name: "unset uses the collector default, which is not checked"},
		{name: "enabled without a target is not checked", cfg: nvcaconfig.BYOOOTelPlatformMetricsFederationConfig{
			Enabled: ptr.To(true)}},
		{name: "target", cfg: nvcaconfig.BYOOOTelPlatformMetricsFederationConfig{Target: target}, want: target},
		{name: "enabled with target", cfg: nvcaconfig.BYOOOTelPlatformMetricsFederationConfig{
			Enabled: ptr.To(true), Target: target}, want: target},
		{name: "disabled with target", cfg: nvcaconfig.BYOOOTelPlatformMetricsFederationConfig{
			Enabled: ptr.To(false), Target: target}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Target(tt.cfg))
		})
	}
}

func TestCheckerHealthyBeforeFirstProbe(t *testing.T) {
	checker := NewChecker("vmsingle.monitoring.svc.cluster.local:8428", nil)
	component := componentHealth(t, checker)
	assert.Equal(t, nvcatypes.HealthStatusHealthy, component.Status)
	assert.Empty(t, component.Errors)
}

func TestCheckerProbe(t *testing.T) {
	tests := []struct {
		name        string
		statusCodes []int
		wantErr     string
	}{
		{name: "reachable", statusCodes: []int{http.StatusOK}},
		{name: "not a federation endpoint", statusCodes: []int{http.StatusNotFound}, wantErr: "returned HTTP 404"},
		{name: "server error", statusCodes: []int{http.StatusServiceUnavailable}, wantErr: "returned HTTP 503"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, calls := federationServer(t, tt.statusCodes...)
			recorder := &fakeRecorder{}
			checker := NewChecker(serverTarget(t, server), recorder)

			err := checker.Probe(context.Background())
			assert.Equal(t, int32(1), calls.Load())
			component := componentHealth(t, checker)
			if tt.wantErr == "" {
				require.NoError(t, err)
				assert.Equal(t, nvcatypes.HealthStatusHealthy, component.Status)
				assert.Empty(t, component.Errors)
				assert.Equal(t, nvcametrics.BYOOPlatformMetricsFederationReachable, recorder.last())
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
			assert.Equal(t, nvcatypes.HealthStatusUnhealthy, component.Status)
			require.Len(t, component.Errors, 1)
			assert.Contains(t, component.Errors[0], tt.wantErr)
			assert.Contains(t, component.Errors[0], "byoo.otelCollector.platformMetricsFederation.enabled to false")
			assert.Equal(t, nvcametrics.BYOOPlatformMetricsFederationUnreachable, recorder.last())
		})
	}
}

func TestCheckerProbeConnectionRefused(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	target := serverTarget(t, server)
	server.Close()

	recorder := &fakeRecorder{}
	checker := NewChecker(target, recorder)
	require.ErrorContains(t, checker.Probe(context.Background()), "GET /federate")

	component := componentHealth(t, checker)
	assert.Equal(t, nvcatypes.HealthStatusUnhealthy, component.Status)
	require.Len(t, component.Errors, 1)
	assert.Contains(t, component.Errors[0], target+" is unreachable")
	assert.Equal(t, nvcametrics.BYOOPlatformMetricsFederationUnreachable, recorder.last())
}

func TestCheckerRecovers(t *testing.T) {
	server, _ := federationServer(t, http.StatusServiceUnavailable, http.StatusOK)
	recorder := &fakeRecorder{}
	checker := NewChecker(serverTarget(t, server), recorder)

	require.Error(t, checker.Probe(context.Background()))
	assert.Equal(t, nvcatypes.HealthStatusUnhealthy, componentHealth(t, checker).Status)

	require.NoError(t, checker.Probe(context.Background()))
	component := componentHealth(t, checker)
	assert.Equal(t, nvcatypes.HealthStatusHealthy, component.Status)
	assert.Empty(t, component.Errors)
	assert.Equal(t, []nvcametrics.BYOOPlatformMetricsFederationState{
		nvcametrics.BYOOPlatformMetricsFederationUnreachable,
		nvcametrics.BYOOPlatformMetricsFederationReachable,
	}, recorder.states)
}

func TestCheckerRunProbesUntilCanceled(t *testing.T) {
	server, calls := federationServer(t, http.StatusOK)
	checker := NewChecker(serverTarget(t, server), nil, WithInterval(10*time.Millisecond))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		checker.Run(ctx)
		close(done)
	}()

	require.Eventually(t, func() bool { return calls.Load() >= 2 }, 5*time.Second, 5*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the context was canceled")
	}
}

// An unreachable target is reported in /healthz at warn level but leaves NVCA
// healthy, so it never blocks startup or workload deployments.
func TestCheckerDoesNotMakeNVCAUnhealthy(t *testing.T) {
	server, _ := federationServer(t, http.StatusNotFound)
	checker := NewChecker(serverTarget(t, server), nil)
	require.Error(t, checker.Probe(context.Background()))

	cache := health.NewBackendStatusCache(0, checker)
	status, err := cache.RefreshStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, nvcatypes.HealthStatusHealthy, status.Status)
	assert.NotContains(t, status.Components, ComponentName)

	readiness := cache.GetStatusForLevel(nvcatypes.StatusLevelWarn)
	assert.Equal(t, nvcatypes.HealthStatusHealthy, readiness.Status)
	require.Contains(t, readiness.Components, ComponentName)
	assert.Equal(t, nvcatypes.HealthStatusUnhealthy, readiness.Components[ComponentName].Status)
}

func TestMetricsImplementsStateRecorder(t *testing.T) {
	var recorder StateRecorder = (*nvcametrics.Metrics)(nil)
	assert.NotPanics(t, func() {
		recorder.SetBYOOPlatformMetricsFederationState(nvcametrics.BYOOPlatformMetricsFederationReachable)
	})
}
