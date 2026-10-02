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

package operator

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/workqueue"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/internal/kubeclients"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/reconcile/clustermgmt"
	nvcaoptypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
)

const startTestPodName = "nvca-operator-5d9f7c-abcde"

// startTestAgent is an Agent prepared to run Start without a reachable
// Kubernetes API server: kube clients are fakes and no ticker events fire.
type startTestAgent struct {
	*Agent
	clients *kubeclients.KubeClients
}

func newStartTestAgent(t *testing.T, mutate func(*AgentOptions)) *startTestAgent {
	t.Helper()

	opts := &AgentOptions{
		SystemNamespace:               NVCAOperatorNamespace,
		SvcAddress:                    fmt.Sprintf("localhost:%d", getEphemeralPort(t)),
		AdminAddr:                     fmt.Sprintf("localhost:%d", getEphemeralPort(t)),
		ShutdownAddr:                  fmt.Sprintf("localhost:%d", getEphemeralPort(t)),
		K8sVersionOverride:            "1.25.8",
		TokenFetcher:                  &mockTokenFetcher{token: "randomkey"},
		NVCFClusterID:                 "random-clusterid",
		NVCAClusterAPIRefreshInterval: time.Hour,
		NVCAClusterManagementAPIURL:   "http://127.0.0.1:1",
		ClusterSource:                 nvcaoptypes.ClusterSourceNGCManaged,
		DeploymentName:                "nvca-operator",
	}
	if mutate != nil {
		mutate(opts)
	}

	ag, err := NewAgent(newTestContext(), opts)
	require.NoError(t, err)

	// Start registers metrics under metricsName, so each test needs its own.
	ag.metricsName = fmt.Sprintf("nvca_operator_start_test_%d", time.Now().UnixNano())
	ag.getTickerEventsFunc = func(context.Context) <-chan *core.Event {
		return make(chan *core.Event)
	}

	clients := mockKubeClients()
	ag.newKubeClientsFunc = func(context.Context, string) (*kubeclients.KubeClients, error) {
		return clients, nil
	}

	return &startTestAgent{Agent: ag, clients: clients}
}

func startTestContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)
	return ctx, cancel
}

func startTestGet(t *testing.T, addr, path string) int {
	t.Helper()
	return startTestRequest(t, http.MethodGet, addr, path)
}

func startTestRequest(t *testing.T, method, addr, path string) int {
	t.Helper()
	req, err := http.NewRequest(method, "http://"+addr+path, nil)
	require.NoError(t, err)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	return resp.StatusCode
}

// occupyAddr binds addr so a server started on it fails to listen.
func occupyAddr(t *testing.T, addr string) {
	t.Helper()
	l, err := net.Listen("tcp", addr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
}

func addStartTestPod(t *testing.T, clients *kubeclients.KubeClients, ownerRefs []metav1.OwnerReference) {
	t.Helper()
	_, err := clients.K8s.CoreV1().Pods(NVCAOperatorNamespace).Create(context.Background(), &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            startTestPodName,
			Namespace:       NVCAOperatorNamespace,
			OwnerReferences: ownerRefs,
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
}

func TestAgent_Start_ServesRoutes(t *testing.T) {
	ag := newStartTestAgent(t, nil)
	ctx, _ := startTestContext(t)

	require.NoError(t, ag.Start(ctx))

	for _, path := range []string{"/healthz", "/version", "/info", "/metrics"} {
		assert.Equal(t, http.StatusOK, startTestGet(t, ag.SvcAddress, path), "service route %s", path)
	}
	assert.Equal(t, http.StatusOK, startTestGet(t, ag.AdminAddr, "/admin"))
	// /shutdown only accepts GET, and a GET would run the shutdown sequence.
	// A 405 for POST proves the route is registered without triggering it.
	assert.Equal(t, http.StatusMethodNotAllowed,
		startTestRequest(t, http.MethodPost, ag.ShutdownAddr, "/shutdown"))

	require.NotNil(t, ag.backendk8scache)
	assert.Equal(t, nvcaoptypes.ClusterSourceNGCManaged, ag.backendk8scache.clusterSource)
	assert.NotEmpty(t, ag.resourceEventWorkerQueues)
}

func TestAgent_Start_ClusterManagementClient(t *testing.T) {
	tests := []struct {
		name          string
		clusterSource nvcaoptypes.ClusterSource
		wantClient    clustermgmt.Client
	}{
		{
			name:          "ngc managed",
			clusterSource: nvcaoptypes.ClusterSourceNGCManaged,
			wantClient:    &clustermgmt.NGCManagedClient{},
		},
		{
			name:          "helm managed",
			clusterSource: nvcaoptypes.ClusterSourceHelmManaged,
			wantClient:    &clustermgmt.HelmManagedClient{},
		},
		{
			name:          "self hosted",
			clusterSource: nvcaoptypes.ClusterSourceSelfHosted,
			wantClient:    &clustermgmt.SelfManagedClient{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ag := newStartTestAgent(t, func(o *AgentOptions) { o.ClusterSource = tt.clusterSource })
			ctx, _ := startTestContext(t)

			require.NoError(t, ag.Start(ctx))
			assert.IsType(t, tt.wantClient, ag.clusterMgmtClient)
		})
	}
}

func TestAgent_Start_DiscoversDeploymentName(t *testing.T) {
	ag := newStartTestAgent(t, func(o *AgentOptions) {
		o.DeploymentName = ""
		o.PodName = startTestPodName
		o.PodNamespace = NVCAOperatorNamespace
	})
	addStartTestPod(t, ag.clients, []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "nvca-operator-5d9f7c"}})
	_, err := ag.clients.K8s.AppsV1().ReplicaSets(NVCAOperatorNamespace).Create(context.Background(), &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "nvca-operator-5d9f7c",
			Namespace:       NVCAOperatorNamespace,
			OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "nvca-operator"}},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	ctx, _ := startTestContext(t)

	require.NoError(t, ag.Start(ctx))
	assert.Equal(t, http.StatusMethodNotAllowed,
		startTestRequest(t, http.MethodPost, ag.ShutdownAddr, "/shutdown"))
}

func TestAgent_Start_Errors(t *testing.T) {
	errKubeClients := errors.New("kubeconfig unavailable")

	tests := []struct {
		name      string
		mutate    func(*AgentOptions)
		prepare   func(t *testing.T, ag *startTestAgent)
		wantErr   error
		wantErrIn string
	}{
		{
			name: "event processing workers already started",
			prepare: func(_ *testing.T, ag *startTestAgent) {
				ag.resourceEventWorkerQueues = map[string]workqueue.TypedRateLimitingInterface[any]{}
			},
			wantErrIn: "cannot be called twice",
		},
		{
			name: "kube clients cannot be created",
			prepare: func(_ *testing.T, ag *startTestAgent) {
				ag.newKubeClientsFunc = func(context.Context, string) (*kubeclients.KubeClients, error) {
					return nil, errKubeClients
				}
			},
			wantErr: errKubeClients,
		},
		{
			name:      "unsupported cluster source",
			mutate:    func(o *AgentOptions) { o.ClusterSource = "unknown-source" },
			wantErrIn: "unsupported cluster-source: unknown-source",
		},
		{
			name:      "invalid cluster management API URL",
			mutate:    func(o *AgentOptions) { o.NVCAClusterManagementAPIURL = "http://[::1" },
			wantErrIn: "missing ']' in host",
		},
		{
			name:      "service address in use",
			prepare:   func(t *testing.T, ag *startTestAgent) { occupyAddr(t, ag.SvcAddress) },
			wantErrIn: "address already in use",
		},
		{
			name:      "admin address in use",
			prepare:   func(t *testing.T, ag *startTestAgent) { occupyAddr(t, ag.AdminAddr) },
			wantErrIn: "address already in use",
		},
		{
			name:      "shutdown address in use",
			prepare:   func(t *testing.T, ag *startTestAgent) { occupyAddr(t, ag.ShutdownAddr) },
			wantErrIn: "address already in use",
		},
		{
			name: "operator pod not found during deployment discovery",
			mutate: func(o *AgentOptions) {
				o.DeploymentName = ""
				o.PodName = startTestPodName
				o.PodNamespace = NVCAOperatorNamespace
			},
			wantErrIn: "failed to discover deployment name",
		},
		{
			name: "operator pod has no deployment owner",
			mutate: func(o *AgentOptions) {
				o.DeploymentName = ""
				o.PodName = startTestPodName
				o.PodNamespace = NVCAOperatorNamespace
			},
			prepare: func(t *testing.T, ag *startTestAgent) {
				addStartTestPod(t, ag.clients, nil)
			},
			wantErrIn: "pod has no deployment owner",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ag := newStartTestAgent(t, tt.mutate)
			if tt.prepare != nil {
				tt.prepare(t, ag)
			}
			ctx, _ := startTestContext(t)

			err := ag.Start(ctx)
			require.Error(t, err)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			}
			if tt.wantErrIn != "" {
				assert.ErrorContains(t, err, tt.wantErrIn)
			}
		})
	}
}

func TestAgent_Start_ContextCancelStopsServersAndWorkers(t *testing.T) {
	ag := newStartTestAgent(t, nil)
	ctx, cancel := startTestContext(t)

	require.NoError(t, ag.Start(ctx))
	require.Equal(t, http.StatusOK, startTestGet(t, ag.SvcAddress, "/healthz"))

	cancel()

	for _, addr := range []string{ag.SvcAddress, ag.AdminAddr, ag.ShutdownAddr} {
		assert.Eventually(t, func() bool {
			conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
			if err != nil {
				return true
			}
			_ = conn.Close()
			return false
		}, 5*time.Second, 20*time.Millisecond, "server on %s still accepting connections", addr)
	}
	assert.Eventually(t, func() bool {
		for _, q := range ag.resourceEventWorkerQueues {
			if !q.ShuttingDown() {
				return false
			}
		}
		return true
	}, 5*time.Second, 20*time.Millisecond, "event worker queues not shut down")
}
